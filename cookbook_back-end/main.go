package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"time"
)

func handlerWithAuth(db *sql.DB, auth authHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/api/auth/", auth)
	mux.Handle("/api/", csrfProtection(requireSession(db, apiHandler{db: db})))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	})

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	db, err := openDatabase(cfg.database)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	auth, err := authConfig(db)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              cfg.address,
		Handler:           handlerWithAuth(db, auth),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("Cookbook API listening on %s", cfg.address)
	log.Fatal(server.ListenAndServe())
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

func csrfProtection(next http.Handler) http.Handler {
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
		if r.Host != u.Host {
			writeError(w, http.StatusForbidden, "csrf_origin_mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}
