package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed web/dist
var frontendFiles embed.FS

func handlerWithAuth(db *sql.DB, auth authHandler) http.Handler {
	cfg, _ := loadConfig()
	return handlerWithAuthConfig(db, auth, cfg)
}

func handlerWithAuthConfig(db *sql.DB, auth authHandler, cfg config) http.Handler {
	mux := http.NewServeMux()
	auth.trustedProxies = cfg.trustedProxies
	passkeys := passkeyHandler{db: db, webauthn: auth.webauthn, secureCookies: auth.secureCookies, trustedProxies: cfg.trustedProxies}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/api/auth/passkey/register/", csrfProtection(auth.origin, requireSession(db, auditChanges(db, passkeys))))
	mux.Handle("/api/auth/passkey/login/", csrfProtection(auth.origin, passkeys))
	mux.Handle("/api/auth/passkeys", csrfProtection(auth.origin, requireSession(db, auditChanges(db, passkeys))))
	mux.Handle("/api/auth/passkeys/", csrfProtection(auth.origin, requireSession(db, auditChanges(db, passkeys))))
	mux.Handle("/api/auth/", auth)
	mux.Handle("GET /api/public/recipes", publicRecipeHandler{db: db})
	mux.Handle("/api/public/cookbooks", publicCookbooksHandler{db: db})
	mux.Handle("/api/public/cookbooks/{id}/recipes", publicCookbookRecipesHandler{db: db})
	api := apiHandler{db: db, mediaDir: cfg.mediaDir}
	mux.Handle("/api/public/courses", api)
	mux.Handle("/api/public/courses/", api)
	media := mediaHandler{db: db, rootDir: cfg.mediaDir}
	serveMedia := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uid, ok := sessionUser(db, r); ok {
			r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, uid))
		}
		media.ServeHTTP(w, r, r.PathValue("id"))
	})
	mux.Handle("GET /api/recipes/{id}/image", serveMedia)
	mux.Handle("HEAD /api/recipes/{id}/image", serveMedia)
	mux.Handle("POST /api/recipes/{id}/image", csrfProtection(auth.origin, requireSession(db, auditChanges(db, serveMedia))))
	mux.Handle("PUT /api/recipes/{id}/image", csrfProtection(auth.origin, requireSession(db, auditChanges(db, serveMedia))))
	mux.Handle("DELETE /api/recipes/{id}/image", csrfProtection(auth.origin, requireSession(db, auditChanges(db, serveMedia))))
	mux.Handle("/api/", csrfProtection(auth.origin, requireSession(db, auditChanges(db, api))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		static, err := fs.Sub(frontendFiles, "web/dist")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "static_unavailable"})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		file, err := static.Open(path)
		if err != nil || strings.HasSuffix(r.URL.Path, "/") {
			if file != nil {
				_ = file.Close()
			}
			path = "index.html"
			file, err = static.Open(path)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		reader, readable := file.(io.ReadSeeker)
		if err != nil || !readable {
			http.NotFound(w, r)
			return
		}
		contentType := mime.TypeByExtension(filepath.Ext(path))
		if contentType == "" {
			switch filepath.Ext(path) {
			case ".css":
				contentType = "text/css; charset=utf-8"
			case ".html":
				contentType = "text/html; charset=utf-8"
			case ".js":
				contentType = "text/javascript; charset=utf-8"
			}
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), reader)
	})

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
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
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.mediaDir, 0700); err != nil {
		log.Fatal("create media directory: ", err)
	}

	db, err := openDatabase(cfg.database)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	startTrashCleanup(db, cfg.trashRetentionDays, cfg.mediaDir)
	if err := configureRegistrationPolicy(db); err != nil {
		log.Fatal(err)
	}
	if err := provisionAdministrator(db); err != nil {
		log.Fatal(err)
	}

	auth, err := authConfig(db)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              cfg.address,
		Handler:           handlerWithAuthConfig(db, auth, cfg),
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
