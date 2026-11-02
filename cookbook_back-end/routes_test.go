package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplicationRoutesPreserveMethodsAndMiddleware(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}

	auth := testAuth(db)
	cfg := config{mediaDir: t.TempDir(), federationKeyDir: t.TempDir(), federationOrigin: "https://local.example.org"}
	handler := handlerWithAuthConfig(db, auth, cfg)
	freshSession, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	oldSession, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET created_at=? WHERE token_hash=?`, time.Now().UTC().Add(-recentAuthenticationWindow-time.Minute).Format(time.RFC3339Nano), sessionTokenHash(oldSession)); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		origin     bool
		session    string
		wantStatus int
		wantBody   string
		wantAudit  bool
	}{
		{name: "health route method mismatch falls through to SPA handler", method: http.MethodPost, path: "/healthz", wantStatus: http.StatusNotFound},
		{name: "public route method mismatch reaches protected API fallback", method: http.MethodPost, path: "/api/public/recipes", wantStatus: http.StatusForbidden, wantBody: `{"error":"csrf_origin_required"}`},
		{name: "public recipe route", method: http.MethodGet, path: "/api/public/recipes", wantStatus: http.StatusOK},
		{name: "federation inbox wrong method reaches protected fallback", method: http.MethodGet, path: "/api/federation/inbox", wantStatus: http.StatusUnauthorized, wantBody: `{"error":"unauthorized"}`},
		{name: "unknown API route requires session", method: http.MethodGet, path: "/api/unknown", wantStatus: http.StatusUnauthorized, wantBody: `{"error":"unauthorized"}`},
		{name: "unknown API route with session", method: http.MethodGet, path: "/api/unknown", session: "fresh", wantStatus: http.StatusNotFound, wantBody: `{"error":"not_found"}`},
		{name: "admin mutation checks origin before session", method: http.MethodPatch, path: "/api/admin/settings", wantStatus: http.StatusForbidden, wantBody: `{"error":"csrf_origin_required"}`},
		{name: "admin mutation requires session after valid origin", method: http.MethodPatch, path: "/api/admin/settings", origin: true, wantStatus: http.StatusUnauthorized, wantBody: `{"error":"unauthorized"}`},
		{name: "admin mutation requires recent authentication", method: http.MethodPatch, path: "/api/admin/settings", origin: true, session: "old", wantStatus: http.StatusForbidden, wantBody: `{"error":"reauthentication_required"}`, wantAudit: true},
		{name: "unknown auth route bypasses API session middleware", method: http.MethodGet, path: "/api/auth/unknown", wantStatus: http.StatusNotFound, wantBody: `{"error":"not_found"}`},
		{name: "SPA fallback serves unknown page", method: http.MethodGet, path: "/missing", wantStatus: http.StatusOK, wantBody: "<!doctype html"},
		{name: "SPA fallback rejects mutation", method: http.MethodPost, path: "/missing", wantStatus: http.StatusNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, nil)
			if test.origin {
				req.Host = "localhost:5173"
				req.Header.Set("Origin", "http://localhost:5173")
			}
			switch test.session {
			case "fresh":
				req.AddCookie(&http.Cookie{Name: sessionCookie, Value: freshSession})
			case "old":
				req.AddCookie(&http.Cookie{Name: sessionCookie, Value: oldSession})
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, body=%q, want %d", rec.Code, rec.Body.String(), test.wantStatus)
			}
			if test.wantBody != "" && !strings.HasPrefix(rec.Body.String(), test.wantBody) {
				t.Fatalf("body = %q, want prefix %q", rec.Body.String(), test.wantBody)
			}
			for header, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "no-referrer",
				"X-Frame-Options":        "DENY",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if strings.HasPrefix(test.path, "/api/") && rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("API Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
			}
			if test.wantAudit {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE actor_id='admin' AND action='PATCH /admin/settings' AND response_status=?`, test.wantStatus).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Errorf("audited recent-auth denial count = %d, want 1", count)
				}
			}
		})
	}
}
