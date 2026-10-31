package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstanceSettingsRequireAdminAndConfirmExistingPublication(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now'),('member','member@example.test','Member',0,'now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('public-book','member','Public cookbook','public','now','now'),('private-book','member','Private cookbook','private','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,visibility,edit_policy,created_at,updated_at) VALUES('public-recipe','member','Member','Public recipe','public','owner_only','now','now'),('private-recipe','member','Member','Private recipe','private','owner_only','now','now')`); err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	memberToken, _, err := issueSession(db, "member")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	adminCookie := &http.Cookie{Name: sessionCookie, Value: adminToken}
	memberCookie := &http.Cookie{Name: sessionCookie, Value: memberToken}

	denied := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, memberCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-admin settings status = %d, want 403", denied.Code)
	}

	initial := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, adminCookie)
	var settings instanceSettings
	if initial.Code != http.StatusOK || json.Unmarshal(initial.Body.Bytes(), &settings) != nil {
		t.Fatalf("admin settings status=%d body=%s", initial.Code, initial.Body)
	}
	if settings.PublicInstance || settings.PublicCookbooks != 1 || settings.PublicRecipes != 1 || settings.RegistrationPolicy != "invite_only" {
		t.Fatalf("unexpected initial settings: %+v", settings)
	}

	refused := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"public_instance": true}, adminCookie)
	if refused.Code != http.StatusConflict {
		t.Fatalf("publication without review status = %d, want 409: %s", refused.Code, refused.Body)
	}
	stillPrivate := callAPI(handler, http.MethodGet, "/api/public/recipes", nil, nil)
	if stillPrivate.Code != http.StatusOK || stillPrivate.Body.String() != "[]\n" {
		t.Fatalf("failed setting update enabled public reads: status=%d body=%s", stillPrivate.Code, stillPrivate.Body)
	}

	enabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"public_instance": true, "registration_policy": "open", "confirm_publication": true}, adminCookie)
	if enabled.Code != http.StatusOK || json.Unmarshal(enabled.Body.Bytes(), &settings) != nil {
		t.Fatalf("confirmed setting update status=%d body=%s", enabled.Code, enabled.Body)
	}
	if !settings.PublicInstance || settings.RegistrationPolicy != "open" {
		t.Fatalf("settings not updated: %+v", settings)
	}
	publicRecipes := callAPI(handler, http.MethodGet, "/api/public/recipes", nil, nil)
	var recipes []recipe
	if publicRecipes.Code != http.StatusOK || json.Unmarshal(publicRecipes.Body.Bytes(), &recipes) != nil || len(recipes) != 1 || recipes[0].ID != "public-recipe" {
		t.Fatalf("public mode exposed wrong recipes: status=%d body=%s", publicRecipes.Code, publicRecipes.Body)
	}

	disabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"public_instance": false}, adminCookie)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable public mode status=%d body=%s", disabled.Code, disabled.Body)
	}
	reenabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"public_instance": true}, adminCookie)
	if reenabled.Code != http.StatusConflict {
		t.Fatalf("re-enabling publication without review status=%d, want 409", reenabled.Code)
	}
}

func TestInstanceSettingsRejectInvalidRegistrationPolicyAndAnonymousAccess(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous settings status = %d, want 401", anonymous.Code)
	}
	invalid := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "anyone"}, &http.Cookie{Name: sessionCookie, Value: token})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid registration policy status = %d, want 400", invalid.Code)
	}
}
