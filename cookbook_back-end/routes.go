package main

import (
	"context"
	"database/sql"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
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
	mux.Handle("POST /api/federation/inbox", federationInboxHandler{db: db, origin: cfg.federationOrigin, trustedProxies: cfg.trustedProxies})
	mux.Handle("POST /api/auth/passkey/enrollment/begin", csrfProtection(auth.origin, passkeys))
	mux.Handle("POST /api/auth/passkey/enrollment/finish", csrfProtection(auth.origin, passkeys))
	mux.Handle("/api/auth/passkey/register/", csrfProtection(auth.origin, requireSession(db, auditChanges(db, passkeys))))
	recentPasskeyChange := func(next http.Handler) http.Handler {
		return csrfProtection(auth.origin, requireSession(db, auditChanges(db, requireRecentAuthentication(db, next))))
	}
	mux.Handle("POST /api/auth/passkey/register/begin", recentPasskeyChange(passkeys))
	mux.Handle("POST /api/auth/passkey/register/finish", recentPasskeyChange(passkeys))
	mux.Handle("/api/auth/passkey/login/", csrfProtection(auth.origin, passkeys))
	mux.Handle("/api/auth/passkeys", csrfProtection(auth.origin, requireSession(db, auditChanges(db, passkeys))))
	mux.Handle("/api/auth/passkeys/", csrfProtection(auth.origin, requireSession(db, auditChanges(db, passkeys))))
	mux.Handle("DELETE /api/auth/passkeys/{id}", recentPasskeyChange(passkeys))
	sessions := sessionHandler{db: db, secureCookies: auth.secureCookies}
	mux.Handle("GET /api/auth/sessions", requireSession(db, sessions))
	mux.Handle("DELETE /api/auth/sessions/{id}", csrfProtection(auth.origin, requireSession(db, auditChanges(db, sessions))))
	mux.Handle("/api/auth/", auth)
	mux.Handle("GET /api/public/config", publicConfigFor(db, auth))
	mux.Handle("GET /api/public/recipes", publicRecipeHandler{db: db})
	mux.Handle("GET /api/public/recipes/{id}", publicRecipeDetailHandler{db: db})
	mux.Handle("GET /api/public/remote-recipes", publicFederationRecipesHandler{db: db})
	mux.Handle("/api/public/cookbooks", publicCookbooksHandler{db: db})
	mux.Handle("GET /api/public/cookbooks/{id}", publicCookbookByIDHandler{db: db})
	mux.Handle("/api/public/cookbooks/{id}/recipes", publicCookbookRecipesHandler{db: db})
	api := apiHandler{db: db, mediaDir: cfg.mediaDir, federationKeyDir: cfg.federationKeyDir}
	mux.Handle("GET /api/admin/federation/peers", requireSession(db, api))
	mux.Handle("POST /api/admin/federation/peers", recentPasskeyChange(api))
	mux.Handle("DELETE /api/admin/federation/peers", recentPasskeyChange(api))
	mux.Handle("POST /api/admin/federation/keys", recentPasskeyChange(api))
	mux.Handle("DELETE /api/admin/federation/keys", recentPasskeyChange(api))
	mux.Handle("GET /api/admin/federation/resources", requireSession(db, http.HandlerFunc(api.federationResources)))
	mux.Handle("PATCH /api/admin/federation/resources", recentPasskeyChange(http.HandlerFunc(api.federationResources)))
	mux.Handle("GET /api/admin/federation/identity", requireSession(db, api))
	mux.Handle("POST /api/admin/federation/identity/rotation", recentPasskeyChange(api))
	mux.Handle("GET /api/admin/federation/outbox", requireSession(db, api))
	mux.Handle("POST /api/admin/federation/outbox", recentPasskeyChange(api))
	mux.Handle("DELETE /api/admin/federation/outbox", recentPasskeyChange(api))
	mux.Handle("GET /api/admin/settings", requireSession(db, api))
	mux.Handle("PATCH /api/admin/settings", recentPasskeyChange(api))
	mux.Handle("GET /api/instance/authority", requireSession(db, http.HandlerFunc(api.instanceOwnerAPI)))
	mux.Handle("GET /api/instance/owner-transfers", requireSession(db, ownerTransferReadHandler{db: db}))
	mux.Handle("POST /api/instance/owner-transfers", recentPasskeyChange(http.HandlerFunc(api.instanceOwnerAPI)))
	mux.Handle("POST /api/instance/owner-transfers/{id}/accept", recentPasskeyChange(http.HandlerFunc(api.instanceOwnerAPI)))
	mux.Handle("DELETE /api/instance/owner-transfers/{id}", recentPasskeyChange(http.HandlerFunc(api.instanceOwnerAPI)))
	mux.Handle("POST /api/recipes/{id}/transfer", csrfProtection(auth.origin, requireSession(db, auditChanges(db, requireRecentAuthentication(db, api)))))
	mux.Handle("POST /api/recipes/{id}/federation-retention", csrfProtection(auth.origin, requireSession(db, auditChanges(db, requireRecentAuthentication(db, api)))))
	mux.Handle("PATCH /api/admin/users/{id}", csrfProtection(auth.origin, requireSession(db, auditChanges(db, requireRecentAuthentication(db, api)))))
	mux.Handle("PATCH /api/admin/library/snapshots/{id}", recentPasskeyChange(api))
	mux.Handle("GET /api/library/recipes/remote", requireSession(db, http.HandlerFunc(api.libraryRemoteRecipeDetailGuard)))
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
	mux.HandleFunc("/", staticFrontendHandler)

	return securityHeaders(apiPathCanonicalityGuard(mux))
}

func apiPathCanonicalityGuard(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		apiPath := strings.TrimLeft(path, "/")
		if apiPath != "api" && !strings.HasPrefix(apiPath, "api/") {
			mux.ServeHTTP(w, r)
			return
		}

		escapedPath := strings.ToLower(r.URL.EscapedPath())
		ambiguous := strings.Contains(escapedPath, "%2f") || strings.Contains(path, "//")
		if !ambiguous {
			for _, segment := range strings.Split(path, "/") {
				if segment == "." || segment == ".." {
					ambiguous = true
					break
				}
			}
		}
		if !ambiguous && strings.HasSuffix(path, "/") {
			_, pattern := mux.Handler(r)
			if _, routePath, hasMethod := strings.Cut(pattern, " "); hasMethod {
				pattern = routePath
			}
			// Keep exact registered subtree roots working, while leaf aliases
			// continue to the generic /api/ fallback only after rejection.
			ambiguous = pattern != path
		}
		if ambiguous {
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func staticFrontendHandler(w http.ResponseWriter, r *http.Request) {
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
}
