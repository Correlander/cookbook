package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 3 * time.Minute
	serverWriteTimeout      = 3 * time.Minute
	serverIdleTimeout       = 60 * time.Second
	serverMaxHeaderBytes    = 1 << 20
)

func newApplicationServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/auth/confirm" || r.URL.Query().Has("invite") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func main() {
	if handled, err := runInstanceCLI(os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	maintenance, err := acquireMaintenanceLock(cfg.database, maintenanceLockShared)
	if err != nil {
		return fmt.Errorf("acquire server maintenance lock: %w", err)
	}
	defer maintenance.Close()
	if err := os.MkdirAll(cfg.mediaDir, 0700); err != nil {
		return fmt.Errorf("create media directory: %w", err)
	}

	db, err := openDatabase(cfg.database)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := configureFederationOrigin(db, cfg.federationOrigin); err != nil {
		return fmt.Errorf("configure federation origin: %w", err)
	}
	authCleanupDone := startAuthCleanup(ctx, db)
	defer stopAndWaitForWorker(stop, authCleanupDone)
	trashCleanupDone := startTrashCleanup(ctx, db, cfg.trashRetentionDays, cfg.mediaDir)
	defer stopAndWaitForWorker(stop, trashCleanupDone)
	if err := configureRegistrationPolicy(db); err != nil {
		return fmt.Errorf("configure registration policy: %w", err)
	}
	if err := provisionAdministrator(db); err != nil {
		return fmt.Errorf("provision administrator: %w", err)
	}

	auth, err := authConfig(db)
	if err != nil {
		return fmt.Errorf("configure authentication: %w", err)
	}
	server := newApplicationServer(cfg.address, handlerWithAuthConfig(db, auth, cfg))
	listener, err := net.Listen("tcp", cfg.address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.address, err)
	}
	if _, err := loadOrCreateFederationSigningKey(cfg.federationKeyDir); err != nil {
		return fmt.Errorf("load federation signing key: %w", err)
	}
	startFederationOutboxWorker(ctx, db, cfg.federationKeyDir)
	backfillDone := startLibraryRemoteSearchBackfill(ctx, db)
	defer stopAndWaitForWorker(stop, backfillDone)
	log.Printf("Cookbook API listening on %s", cfg.address)
	serverErr := runHTTPServer(ctx, server, listener, 20*time.Second)
	return serverErr
}

func stopAndWaitForWorker(stop context.CancelFunc, done <-chan struct{}) {
	stop()
	<-done
}

func requireSession(db *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := sessionUser(db, r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, uid))
		next.ServeHTTP(w, r)
	})
}

type userContextKey struct{}

func authenticatedUser(r *http.Request) string {
	userID, _ := r.Context().Value(userContextKey{}).(string)
	return userID
}

func csrfProtection(expectedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			writeError(w, http.StatusForbidden, "csrf_origin_required")
			return
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			writeError(w, http.StatusForbidden, "csrf_origin_invalid")
			return
		}
		if r.Host != u.Host || origin != expectedOrigin {
			writeError(w, http.StatusForbidden, "csrf_origin_mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}
