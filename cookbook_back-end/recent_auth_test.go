package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSensitiveOperationsRequireRecentAuthentication(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',1,'now'),('recipient','recipient@example.test','Recipient',0,'now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,created_at,updated_at) VALUES
		('recipe','owner','Owner','Pie','[]','[]','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_by,updated_at)
		VALUES('recipe','unpublished','off',0,'owner','2026-11-03T12:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	oldToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	oldIssuedAt := time.Now().UTC().Add(-recentAuthenticationWindow - time.Minute).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE sessions SET created_at=? WHERE token_hash=?`, oldIssuedAt, sessionTokenHash(oldToken)); err != nil {
		t.Fatal(err)
	}
	oldCookie := &http.Cookie{Name: sessionCookie, Value: oldToken}
	h := handlerWithAuth(db, testAuth(db))
	for _, route := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/auth/passkey/register/begin", nil},
		{http.MethodPost, "/api/auth/passkey/register/finish", map[string]string{}},
		{http.MethodDelete, "/api/auth/passkeys/unknown", nil},
		{http.MethodPost, "/api/recipes/recipe/transfer", map[string]string{"email": "recipient@example.test"}},
		{http.MethodPatch, "/api/admin/users/recipient", map[string]bool{"is_admin": true}},
	} {
		response := callAPI(h, route.method, route.path, route.body, oldCookie)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "reauthentication_required") {
			t.Fatalf("stale session %s %s status=%d body=%s", route.method, route.path, response.Code, response.Body)
		}
	}
	var owner string
	if err := db.QueryRow(`SELECT owner_id FROM recipes WHERE id='recipe'`).Scan(&owner); err != nil || owner != "owner" {
		t.Fatalf("stale session transferred recipe: owner=%q err=%v", owner, err)
	}

	freshToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	freshCookie := &http.Cookie{Name: sessionCookie, Value: freshToken}
	begin := callAPI(h, http.MethodPost, "/api/auth/passkey/register/begin", nil, freshCookie)
	if begin.Code != http.StatusOK {
		t.Fatalf("recent session passkey registration status=%d body=%s", begin.Code, begin.Body)
	}
	transfer := callAPI(h, http.MethodPost, "/api/recipes/recipe/transfer", map[string]string{"email": "recipient@example.test"}, freshCookie)
	if transfer.Code != http.StatusOK {
		t.Fatalf("recent session recipe transfer status=%d body=%s", transfer.Code, transfer.Body)
	}
	roleChange := callAPI(h, http.MethodPatch, "/api/admin/users/recipient", map[string]bool{"is_admin": true}, freshCookie)
	if roleChange.Code != http.StatusOK {
		t.Fatalf("recent session administrator change status=%d body=%s", roleChange.Code, roleChange.Body)
	}
	if err := db.QueryRow(`SELECT owner_id FROM recipes WHERE id='recipe'`).Scan(&owner); err != nil || owner != "recipient" {
		t.Fatalf("fresh session did not transfer recipe: owner=%q err=%v", owner, err)
	}
}
