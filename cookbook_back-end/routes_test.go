package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplicationPublicConfigRouteIsAnonymousAndNoStore(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET registration_policy='disabled',public_instance=1,courses_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	auth := testAuth(db)
	auth.sender = nil
	cfg := config{mediaDir: t.TempDir(), federationKeyDir: t.TempDir()}
	handler := handlerWithAuthConfig(db, auth, cfg)
	request := httptest.NewRequest(http.MethodGet, "/api/public/config", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("anonymous GET public config status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body)
	}
	var got publicConfigResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AuthMethods.EmailLink || !got.AuthMethods.Passkey || got.RegistrationPolicy != "disabled" || !got.PublicInstance || got.CoursesEnabled {
		t.Fatalf("unexpected composed public config: %+v", got)
	}
}

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
		{name: "retention assertion requires recent session authentication", method: http.MethodPost, path: "/api/recipes/0123456789abcdef0123456789abcdef/federation-retention", origin: true, session: "old", wantStatus: http.StatusForbidden, wantBody: `{"error":"reauthentication_required"}`, wantAudit: true},
		{name: "federation resources read requires session", method: http.MethodGet, path: "/api/admin/federation/resources", wantStatus: http.StatusUnauthorized, wantBody: `{"error":"unauthorized"}`},
		{name: "federation resources read after session", method: http.MethodGet, path: "/api/admin/federation/resources", session: "fresh", wantStatus: http.StatusOK},
		{name: "federation resources update checks origin", method: http.MethodPatch, path: "/api/admin/federation/resources", wantStatus: http.StatusForbidden, wantBody: `{"error":"csrf_origin_required"}`},
		{name: "federation resources update requires recent authentication", method: http.MethodPatch, path: "/api/admin/federation/resources", origin: true, session: "old", wantStatus: http.StatusForbidden, wantBody: `{"error":"reauthentication_required"}`},
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
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "no-referrer",
				"X-Frame-Options":         "DENY",
				"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'",
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

func TestApplicationFederationInboxRouteAcceptsSignedRequestsWithoutBrowserSession(t *testing.T) {
	db := testDatabase(t)
	localOrigin := "https://local.example"
	peerOrigin := "https://peer.example.org"
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, localOrigin); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, peerOrigin, true); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, peerOrigin, "peer-key", publicKey); err != nil {
		t.Fatal(err)
	}
	wrongPublicKey, wrongPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(wrongPublicKey) == string(publicKey) {
		t.Fatal("generated duplicate federation public key")
	}

	event, err := serializeFederationEvent(peerOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Remote cook", Title: "Signed recipe", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, testAuth(db), config{
		mediaDir: t.TempDir(), federationKeyDir: t.TempDir(), federationOrigin: localOrigin,
	})

	validRequest := signedFederationRequest(t, privateKey, "peer-key", strings.Repeat("valid", 8), string(event))
	if got := validRequest.Header.Get("Origin"); got != "" {
		t.Fatalf("federation request Origin = %q, want none", got)
	}
	if _, err := validRequest.Cookie(sessionCookie); err == nil {
		t.Fatal("federation request unexpectedly contains a browser session")
	}
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusAccepted || !strings.Contains(validResponse.Body.String(), `"status":"accepted"`) {
		t.Fatalf("valid signed inbox response status=%d body=%q", validResponse.Code, validResponse.Body.String())
	}

	invalidRequest := signedFederationRequest(t, wrongPrivateKey, "peer-key", strings.Repeat("invalid", 6), string(event))
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status=%d body=%q, want %d", invalidResponse.Code, invalidResponse.Body.String(), http.StatusUnauthorized)
	}
	var acceptedEvents, remoteRecipes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events`).Scan(&acceptedEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipes`).Scan(&remoteRecipes); err != nil {
		t.Fatal(err)
	}
	if acceptedEvents != 1 || remoteRecipes != 1 {
		t.Fatalf("invalid signature changed inbox state: accepted events=%d remote recipes=%d", acceptedEvents, remoteRecipes)
	}
}
