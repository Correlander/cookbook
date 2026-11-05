package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestApplicationPublicConfigRouteIsAnonymousAndNoStore(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET registration_policy='disabled',public_instance=0,courses_enabled=0 WHERE id=1`); err != nil {
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
	if got.AuthMethods.EmailLink || !got.AuthMethods.Passkey || got.RegistrationPolicy != "disabled" || got.PublicInstance || got.CoursesEnabled || !got.MenuAPI {
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
		{name: "federation peer profile checks origin", method: http.MethodPatch, path: "/api/admin/federation/peers", wantStatus: http.StatusForbidden, wantBody: `{"error":"csrf_origin_required"}`},
		{name: "federation peer profile requires session", method: http.MethodPatch, path: "/api/admin/federation/peers", origin: true, wantStatus: http.StatusUnauthorized, wantBody: `{"error":"unauthorized"}`},
		{name: "federation peer profile requires recent session age", method: http.MethodPatch, path: "/api/admin/federation/peers", origin: true, session: "old", wantStatus: http.StatusForbidden, wantBody: `{"error":"reauthentication_required"}`, wantAudit: true},
		{name: "snapshot moderation checks origin", method: http.MethodPatch, path: "/api/admin/library/snapshots/pin-one", wantStatus: http.StatusForbidden, wantBody: `{"error":"csrf_origin_required"}`},
		{name: "snapshot moderation requires session", method: http.MethodPatch, path: "/api/admin/library/snapshots/pin-one", origin: true, wantStatus: http.StatusUnauthorized, wantBody: `{"error":"unauthorized"}`},
		{name: "snapshot moderation requires recent authentication", method: http.MethodPatch, path: "/api/admin/library/snapshots/pin-one", origin: true, session: "old", wantStatus: http.StatusForbidden, wantBody: `{"error":"reauthentication_required"}`},
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

func TestApplicationAPICanonicalPathGuardRejectsAliasesBeforeDispatch(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES
		(?,?,?,1,'Remote cook',?,0,'2026-11-03T12:00:00Z'),(?,?,?,1,'Remote cook',?,0,'2026-11-03T12:00:00Z')`,
		testFederationOrigin+"/federation/recipes/"+testFederationID, testFederationOrigin, testFederationID,
		`{"title":"Readable","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`,
		testFederationOrigin+"/federation/recipes/1123456789abcdef0123456789abcdef", testFederationOrigin, "1123456789abcdef0123456789abcdef",
		`{"title":"Blocked","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`); err != nil {
		t.Fatal(err)
	}
	blockedURI := testFederationOrigin + "/federation/recipes/1123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('blocked-pin','viewer','remote',?,'now');
		INSERT INTO library_remote_snapshot_pins(bookmark_id,version_id,state,created_at,removed_version_id) VALUES('blocked-pin',NULL,'removed','now','removed-version')`, blockedURI); err != nil {
		t.Fatal(err)
	}

	auth := testAuth(db)
	handler := handlerWithAuthConfig(db, auth, config{mediaDir: t.TempDir(), federationKeyDir: t.TempDir()})
	adminFresh, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	adminStale, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ownerStale, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	viewerFresh, _, err := issueSession(db, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET created_at=? WHERE token_hash IN (?,?)`, time.Now().UTC().Add(-recentAuthenticationWindow-time.Minute).Format(time.RFC3339Nano), sessionTokenHash(adminStale), sessionTokenHash(ownerStale)); err != nil {
		t.Fatal(err)
	}

	request := func(method, path, token string, withOrigin bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		if withOrigin {
			req.Host = "localhost:5173"
			req.Header.Set("Origin", "http://localhost:5173")
		}
		if method == http.MethodPatch || method == http.MethodPost || method == http.MethodDelete {
			req.Header.Set("Content-Type", "application/json")
			req.Body = io.NopCloser(strings.NewReader(`{"outcome":"removed","reason_category":"privacy","registration_policy":"disabled","origin":"https://peer.example.org","key_id":"new-key","public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","expected_recipe_version":1,"email":"other@example.test","target_user_id":"other"}`))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	assertDenied := func(name, method, path, token string, withOrigin bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			response := request(method, path, token, withOrigin)
			if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"error":"not_found"`) || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q body=%q; want terminal no-store 404", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}

	assertDenied("restricted snapshot detail slash cannot reach cache fallback", http.MethodGet,
		"/api/library/recipes/remote/?remote_object_uri="+url.QueryEscape(blockedURI), viewerFresh, false)
	assertDenied("moderation slash cannot skip recent authentication", http.MethodPatch, "/api/admin/library/snapshots/blocked-pin/", adminStale, true)
	assertDenied("settings slash cannot skip recent authentication", http.MethodPatch, "/api/admin/settings/", adminStale, true)
	assertDenied("federation key slash cannot skip recent authentication", http.MethodPost, "/api/admin/federation/keys/", adminStale, true)
	assertDenied("federation key revocation slash cannot skip recent authentication", http.MethodDelete, "/api/admin/federation/keys/", adminStale, true)
	assertDenied("federation peer profile slash cannot skip recent authentication", http.MethodPatch, "/api/admin/federation/peers/", adminStale, true)
	assertDenied("retention slash cannot skip recent authentication", http.MethodPost, "/api/recipes/shared/federation-retention/", ownerStale, true)
	assertDenied("recipe transfer slash cannot skip recent authentication", http.MethodPost, "/api/recipes/shared/transfer/", ownerStale, true)
	assertDenied("owner transfer slash remains non-mutating", http.MethodPost, "/api/instance/owner-transfers/", ownerStale, true)

	for _, path := range []string{
		"/api/recipes/shared%2Ffederation-retention",
		"/api/recipes/shared%2ffederation-retention",
		"/api%2Fadmin/settings",
		"/api/admin/federation%2Fpeers",
		"/%2Fapi/admin/settings",
	} {
		assertDenied("encoded separator "+path, http.MethodPatch, path, adminStale, true)
	}
	for _, path := range []string{
		"/api//admin/settings",
		"/api/./admin/settings",
		"/api/x/../admin/settings",
	} {
		assertDenied("path cleanup alias "+path, http.MethodPatch, path, adminStale, true)
	}

	encodedPrefix := request(http.MethodPatch, "/%61pi/admin/settings", adminStale, true)
	if encodedPrefix.Code != http.StatusForbidden || !strings.Contains(encodedPrefix.Body.String(), `"error":"reauthentication_required"`) || encodedPrefix.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("encoded unreserved API prefix status=%d cache=%q body=%q; want normal stale-session route guard", encodedPrefix.Code, encodedPrefix.Header().Get("Cache-Control"), encodedPrefix.Body.String())
	}
	canonicalModeration := request(http.MethodPatch, "/api/admin/library/snapshots/blocked-pin", adminStale, true)
	if canonicalModeration.Code != http.StatusForbidden || !strings.Contains(canonicalModeration.Body.String(), `"error":"reauthentication_required"`) {
		t.Fatalf("canonical stale moderation status=%d body=%q", canonicalModeration.Code, canonicalModeration.Body.String())
	}
	wrongMethod := request(http.MethodGet, "/api/admin/library/snapshots/blocked-pin", adminFresh, false)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("canonical moderation method mismatch status=%d body=%q", wrongMethod.Code, wrongMethod.Body.String())
	}
	canonicalFresh := request(http.MethodGet, "/api/admin/settings", adminFresh, false)
	if canonicalFresh.Code != http.StatusOK {
		t.Fatalf("canonical fresh admin route status=%d body=%q", canonicalFresh.Code, canonicalFresh.Body.String())
	}
	canonicalQuery := request(http.MethodGet, "/api/library/recipes/remote?remote_object_uri="+url.QueryEscape(testFederationOrigin+"/federation/recipes/"+testFederationID), viewerFresh, false)
	if canonicalQuery.Code != http.StatusOK || canonicalQuery.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(canonicalQuery.Body.String(), `"title":"Readable"`) {
		t.Fatalf("encoded slash in query URI was rejected or mishandled: status=%d cache=%q body=%q", canonicalQuery.Code, canonicalQuery.Header().Get("Cache-Control"), canonicalQuery.Body.String())
	}
	publicSubtree := request(http.MethodGet, "/api/public/courses/example/lessons", "", false)
	if publicSubtree.Code != http.StatusNotFound || !strings.Contains(publicSubtree.Body.String(), `"error":"courses_disabled"`) {
		t.Fatalf("valid deep API subtree was blocked by canonicality guard: status=%d body=%q", publicSubtree.Code, publicSubtree.Body.String())
	}
	publicSubtreeRoot := request(http.MethodGet, "/api/public/courses/", "", false)
	if publicSubtreeRoot.Code != http.StatusNotFound || !strings.Contains(publicSubtreeRoot.Body.String(), `"error":"courses_disabled"`) {
		t.Fatalf("registered API subtree root changed: status=%d body=%q", publicSubtreeRoot.Code, publicSubtreeRoot.Body.String())
	}
	staticFallback := request(http.MethodGet, "/missing", "", false)
	if staticFallback.Code != http.StatusOK || staticFallback.Header().Get("Cache-Control") == "no-store" {
		t.Fatalf("non-API static behavior changed: status=%d cache=%q", staticFallback.Code, staticFallback.Header().Get("Cache-Control"))
	}

	var ownerID, registrationPolicy, snapshotState string
	var keyCount, outboxCount int
	if err := db.QueryRow(`SELECT owner_id FROM recipes WHERE id='shared'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&registrationPolicy); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM library_remote_snapshot_pins WHERE bookmark_id='blocked-pin'`).Scan(&snapshotState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_peer_keys`).Scan(&keyCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if ownerID != "owner" || registrationPolicy != "invite_only" || snapshotState != "removed" || keyCount != 0 || outboxCount != 0 {
		t.Fatalf("rejected aliases mutated protected state: owner=%q registration=%q snapshot=%q keys=%d outbox=%d", ownerID, registrationPolicy, snapshotState, keyCount, outboxCount)
	}
}
