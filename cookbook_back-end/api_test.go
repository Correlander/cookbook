package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func callAPI(h http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	var data bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&data).Encode(body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, &data)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if method != http.MethodGet {
		req.Host = "example.test"
		req.Header.Set("Origin", "http://example.test")
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthStateChangesRequireSameOrigin(t *testing.T) {
	db := testDatabase(t)
	h := handlerWithAuth(db, testAuth(db))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/consume", strings.NewReader(`{"token":"invalid"}`))
	req.Host = "example.test"
	req.Header.Set("Origin", "https://attacker.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin auth request status = %d, want 403", rec.Code)
	}
}
func TestUnauthenticatedAndForgedUserCannotCreateCookbook(t *testing.T) {
	db := testDatabase(t)
	sender := testEmailSender{sent: make(chan string, 1)}
	auth := testAuth(db)
	auth.sender = sender
	h := handlerWithAuth(db, auth)
	rec := callAPI(h, http.MethodPost, "/api/cookbooks", map[string]any{"owner_id": "victim", "title": "Family"}, nil)
	if rec.Code != 401 {
		t.Fatalf("anonymous create status %d", rec.Code)
	}
}
func TestPasswordlessLoginAndSessionGatesAPI(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	sender := testEmailSender{sent: make(chan string, 1)}
	auth := testAuth(db)
	auth.sender = sender
	h := handlerWithAuth(db, auth)
	rec := callAPI(h, http.MethodPost, "/api/auth/email", map[string]string{"email": "chef@example.test", "display_name": "Chef"}, nil)
	if rec.Code != 200 {
		t.Fatalf("request link status %d: %s", rec.Code, rec.Body)
	}
	link := <-sender.sent
	token := link[strings.Index(link, "token=")+6:]
	rec = callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	if rec.Code != 200 {
		t.Fatalf("consume status %d: %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("secure session cookie missing attributes")
	}
	rec = callAPI(h, http.MethodPost, "/api/cookbooks", map[string]any{"title": "Family"}, session)
	if rec.Code != 201 {
		t.Fatalf("authenticated cookbook create status %d: %s", rec.Code, rec.Body)
	}
	var c cookbook
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	var email string
	if err := db.QueryRow(`SELECT email FROM users WHERE id=?`, c.OwnerID).Scan(&email); err != nil || email != "chef@example.test" {
		t.Fatalf("owner was not session user: %q, %v", email, err)
	}
}
func TestLoginTokenSingleUse(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	sender := testEmailSender{sent: make(chan string, 1)}
	h := handlerWithAuth(db, func() authHandler { a := testAuth(db); a.sender = sender; return a }())
	_ = callAPI(h, http.MethodPost, "/api/auth/email", map[string]string{"email": "one@example.test"}, nil)
	token := <-sender.sent
	token = token[strings.Index(token, "token=")+6:]
	first := callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	second := callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	if first.Code != 200 || second.Code != 400 {
		t.Fatalf("consume statuses %d then %d", first.Code, second.Code)
	}
}

func TestExpiredLoginChallengeRejected(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	sender := testEmailSender{sent: make(chan string, 1)}
	auth := testAuth(db)
	auth.sender = sender
	h := handlerWithAuth(db, auth)
	_ = callAPI(h, http.MethodPost, "/api/auth/email", map[string]string{"email": "expired@example.test"}, nil)
	link := <-sender.sent
	token := link[strings.Index(link, "token=")+6:]
	_, err := db.Exec(`UPDATE email_challenges SET expires_at=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	rec := callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	if rec.Code != 400 {
		t.Fatalf("expired token status %d", rec.Code)
	}
}

func TestPrivateInstanceCannotPublishCookbook(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	h := testAuth(db)
	auth := h
	sender := testEmailSender{sent: make(chan string, 1)}
	auth.sender = sender
	handler := handlerWithAuth(db, auth)
	_ = callAPI(handler, http.MethodPost, "/api/auth/email", map[string]string{"email": "private@example.test"}, nil)
	link := <-sender.sent
	token := link[strings.Index(link, "token=")+6:]
	signed := callAPI(handler, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	session := signed.Result().Cookies()[0]
	resp := callAPI(handler, http.MethodPost, "/api/cookbooks", map[string]any{"title": "Public?", "visibility": "public"}, session)
	if resp.Code != 403 {
		t.Fatalf("public cookbook on private instance status %d", resp.Code)
	}
}
