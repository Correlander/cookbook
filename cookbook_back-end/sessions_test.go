package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountSessionsCanBeListedAndRevokedIndividually(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('owner','owner@example.test','Owner','now'),('other','other@example.test','Other','now')`); err != nil {
		t.Fatal(err)
	}
	currentRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	currentRequest.Header.Set("User-Agent", "Cookbook Browser 1.0")
	currentToken, _, err := issueSessionFromRequest(db, "owner", currentRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondaryToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, err := issueSession(db, "other")
	if err != nil {
		t.Fatal(err)
	}
	currentID := testSessionID(t, db, currentToken)
	secondaryID := testSessionID(t, db, secondaryToken)
	otherID := testSessionID(t, db, otherToken)
	h := handlerWithAuth(db, testAuth(db))
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: currentToken}

	listed := callAPI(h, http.MethodGet, "/api/auth/sessions", nil, ownerCookie)
	var items []sessionSummary
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &items) != nil || len(items) != 2 {
		t.Fatalf("session list status=%d body=%s", listed.Code, listed.Body)
	}
	if strings.Contains(listed.Body.String(), currentToken) {
		t.Fatal("session list exposed an authentication token")
	}
	currentCount := 0
	for _, item := range items {
		if item.Current {
			currentCount++
			if item.ID != currentID {
				t.Fatalf("current session id=%q, want %q", item.ID, currentID)
			}
			if item.UserAgent != "Cookbook Browser 1.0" {
				t.Fatalf("current session user agent=%q", item.UserAgent)
			}
		}
	}
	if currentCount != 1 {
		t.Fatalf("current sessions=%d, want exactly one", currentCount)
	}

	crossAccount := callAPI(h, http.MethodDelete, "/api/auth/sessions/"+otherID, nil, ownerCookie)
	if crossAccount.Code != http.StatusNotFound {
		t.Fatalf("cross-account revoke status=%d, want 404", crossAccount.Code)
	}
	var otherSessionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, otherID).Scan(&otherSessionCount); err != nil || otherSessionCount != 1 {
		t.Fatalf("cross-account revoke affected other session: rows=%d err=%v", otherSessionCount, err)
	}

	revoked := callAPI(h, http.MethodDelete, "/api/auth/sessions/"+secondaryID, nil, ownerCookie)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("secondary session revoke status=%d body=%s", revoked.Code, revoked.Body)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, secondaryID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("revoked session rows=%d err=%v", remaining, err)
	}

	revokeCurrent := callAPI(h, http.MethodDelete, "/api/auth/sessions/"+currentID, nil, ownerCookie)
	if revokeCurrent.Code != http.StatusNoContent {
		t.Fatalf("current session revoke status=%d body=%s", revokeCurrent.Code, revokeCurrent.Body)
	}
	cleared := false
	for _, cookie := range revokeCurrent.Result().Cookies() {
		if cookie.Name == sessionCookie && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("revoking the current session did not clear its cookie")
	}
	unauthorized := callAPI(h, http.MethodGet, "/api/auth/me", nil, ownerCookie)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("revoked current session remains usable: status=%d", unauthorized.Code)
	}
	var revocationEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE actor_id='owner' AND action='DELETE /auth/sessions/{id}' AND resource_type='auth' AND resource_id=''`).Scan(&revocationEvents); err != nil || revocationEvents != 2 {
		t.Fatalf("session revocation audit events=%d err=%v", revocationEvents, err)
	}
}

func TestSessionUserAgentIsSafeAndBounded(t *testing.T) {
	if got := normalizedUserAgent("Browser\nInjected"); got != "Browser Injected" {
		t.Fatalf("control characters were not normalized: %q", got)
	}
	if got := normalizedUserAgent(strings.Repeat("x", 300)); len(got) != 255 {
		t.Fatalf("user-agent length=%d, want 255", len(got))
	}
}

func testSessionID(t *testing.T, db *sql.DB, token string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash=?`, sessionTokenHash(token)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
