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

func TestPublicRecipeListingRequiresPublicInstanceAndPublishedRecipe(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at) VALUES('private','owner','Owner','Private dish','','[]','[]','private','owner_only','now','now'),('public','owner','Owner','Shared dish','','[]','[]','public','owner_only','now','now')`)
	if err != nil {
		t.Fatal(err)
	}
	h := handlerWithAuth(db, testAuth(db))
	privateInstance := httptest.NewRecorder()
	h.ServeHTTP(privateInstance, httptest.NewRequest(http.MethodGet, "/api/public/recipes", nil))
	if privateInstance.Code != http.StatusOK || strings.TrimSpace(privateInstance.Body.String()) != "[]" {
		t.Fatalf("private instance listing = %d %s", privateInstance.Code, privateInstance.Body)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	publicInstance := httptest.NewRecorder()
	h.ServeHTTP(publicInstance, httptest.NewRequest(http.MethodGet, "/api/public/recipes", nil))
	var items []recipe
	if publicInstance.Code != http.StatusOK || json.Unmarshal(publicInstance.Body.Bytes(), &items) != nil {
		t.Fatalf("public instance listing = %d %s", publicInstance.Code, publicInstance.Body)
	}
	if len(items) != 1 || items[0].ID != "public" {
		t.Fatalf("public listing leaked or omitted content: %#v", items)
	}
	if strings.Contains(publicInstance.Body.String(), `"owner_id"`) {
		t.Fatalf("public listing exposed the internal owner ID: %s", publicInstance.Body)
	}
}

func TestRecipeRevisionsAndOptimisticUpdates(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	sessionToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: sessionToken}
	h := handlerWithAuth(db, testAuth(db))
	create := map[string]any{"title": "First title", "ingredients": []map[string]string{{"name": "flour"}}, "steps": []map[string]string{{"text": "Mix"}}}
	created := callAPI(h, http.MethodPost, "/api/recipes", create, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", created.Code, created.Body)
	}
	var current recipe
	if err := json.Unmarshal(created.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	update := map[string]any{"title": "Second title", "ingredients": []map[string]string{{"name": "flour"}}, "steps": []map[string]string{{"text": "Mix"}}, "version": current.Version}
	updated := callAPI(h, http.MethodPut, "/api/recipes/"+current.ID, update, cookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", updated.Code, updated.Body)
	}
	stale := callAPI(h, http.MethodPut, "/api/recipes/"+current.ID, update, cookie)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409", stale.Code)
	}
	revisions := callAPI(h, http.MethodGet, "/api/recipes/"+current.ID+"/revisions", nil, cookie)
	var history []recipeRevision
	if revisions.Code != http.StatusOK || json.Unmarshal(revisions.Body.Bytes(), &history) != nil {
		t.Fatalf("revision listing status = %d: %s", revisions.Code, revisions.Body)
	}
	if len(history) != 2 || history[0].Version != 2 || history[0].Title != "Second title" || history[1].Version != 1 || history[1].Title != "First title" {
		t.Fatalf("unexpected revision history: %#v", history)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','reader@example.test','Reader','now')`); err != nil {
		t.Fatal(err)
	}
	readerToken, _, err := issueSession(db, "reader")
	if err != nil {
		t.Fatal(err)
	}
	denied := callAPI(h, http.MethodGet, "/api/recipes/"+current.ID+"/revisions", nil, &http.Cookie{Name: sessionCookie, Value: readerToken})
	if denied.Code != http.StatusNotFound {
		t.Fatalf("private revision read status = %d, want 404", denied.Code)
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
