package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstanceSettingsRequireAdminAndConfirmExistingPublication(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now'),('member','member@example.test','Member',0,'now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('public-book','member','Public cookbook','public','now','now'),('private-book','member','Private cookbook','private','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,visibility,edit_policy,created_at,updated_at) VALUES('public-recipe','member','Member','Public recipe','public','owner_only','now','now'),('private-recipe','member','Member','Private recipe','private','owner_only','now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('public-recipe','published','off',0,'now'),('private-recipe','unpublished','off',0,'now')`); err != nil {
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
	if settings.PublicInstance || settings.PublicCookbooks != 1 || settings.PublicRecipes != 1 || settings.RegistrationPolicy != "invite_only" || settings.RevisionRetention != 365 || settings.AuditRetention != 365 || settings.PrivateSnapshotStorageLimitBytes != defaultRemoteSnapshotStoreSize {
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

	enabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"public_instance": true, "registration_policy": "open", "revision_retention_days": 90, "audit_retention_days": 30, "confirm_publication": true}, adminCookie)
	if enabled.Code != http.StatusOK || json.Unmarshal(enabled.Body.Bytes(), &settings) != nil {
		t.Fatalf("confirmed setting update status=%d body=%s", enabled.Code, enabled.Body)
	}
	if !settings.PublicInstance || settings.RegistrationPolicy != "open" || settings.RevisionRetention != 90 || settings.AuditRetention != 30 {
		t.Fatalf("settings not updated: %+v", settings)
	}
	quota := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"private_snapshot_storage_limit_bytes": int64(1024)}, adminCookie)
	if quota.Code != http.StatusOK || json.Unmarshal(quota.Body.Bytes(), &settings) != nil || settings.PrivateSnapshotStorageLimitBytes != 1024 {
		t.Fatalf("snapshot quota update status=%d settings=%+v body=%s", quota.Code, settings, quota.Body)
	}
	invalidQuota := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"private_snapshot_storage_limit_bytes": 0}, adminCookie)
	if invalidQuota.Code != http.StatusBadRequest {
		t.Fatalf("zero snapshot quota status=%d body=%s", invalidQuota.Code, invalidQuota.Body)
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
	invalidRetention := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]int{"revision_retention_days": 45}, &http.Cookie{Name: sessionCookie, Value: token})
	if invalidRetention.Code != http.StatusBadRequest {
		t.Fatalf("invalid revision retention status = %d, want 400", invalidRetention.Code)
	}
}

func TestInstanceSettingsUseCurrentOwnerAndAdminCapabilities(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('successor','successor@example.test','Successor',0,'now'),
		('admin','admin@example.test','Admin',1,'now'),('backup-admin','backup@example.test','Backup Admin',1,'now'),
		('ordinary','ordinary@example.test','Ordinary',0,'now')`); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	cookies := make(map[string]*http.Cookie)
	for _, accountID := range []string{"owner", "successor", "admin", "ordinary"} {
		token, _, err := issueSession(db, accountID)
		if err != nil {
			t.Fatal(err)
		}
		cookies[accountID] = &http.Cookie{Name: sessionCookie, Value: token}
	}

	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous settings GET status=%d body=%s", got.Code, got.Body)
	}
	if got := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "open"}, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous settings PATCH status=%d body=%s", got.Code, got.Body)
	}
	preclaimAdmin := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["admin"])
	if preclaimAdmin.Code != http.StatusOK {
		t.Fatalf("preclaim admin lost settings access: status=%d body=%s", preclaimAdmin.Code, preclaimAdmin.Body)
	}
	preclaimUpdate := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "open"}, cookies["admin"])
	if preclaimUpdate.Code != http.StatusOK {
		t.Fatalf("preclaim admin settings update status=%d body=%s", preclaimUpdate.Code, preclaimUpdate.Body)
	}
	preclaimOwner := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["owner"])
	if preclaimOwner.Code != http.StatusForbidden {
		t.Fatalf("unclaimed owner account gained settings access: status=%d body=%s", preclaimOwner.Code, preclaimOwner.Body)
	}
	preclaimWrite := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "disabled"}, cookies["owner"])
	if preclaimWrite.Code != http.StatusForbidden {
		t.Fatalf("unclaimed owner account changed settings: status=%d body=%s", preclaimWrite.Code, preclaimWrite.Body)
	}

	if _, err := ClaimInstanceOwner(context.Background(), db, "owner"); err != nil {
		t.Fatal(err)
	}
	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["owner"]); got.Code != http.StatusOK {
		t.Fatalf("claimed non-admin owner settings GET status=%d body=%s", got.Code, got.Body)
	}
	ownerUpdate := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "invite_only"}, cookies["owner"])
	if ownerUpdate.Code != http.StatusOK {
		t.Fatalf("claimed non-admin owner settings PATCH status=%d body=%s", ownerUpdate.Code, ownerUpdate.Body)
	}
	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["admin"]); got.Code != http.StatusOK {
		t.Fatalf("claimed instance admin lost general settings access: status=%d body=%s", got.Code, got.Body)
	}
	adminUpdate := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "open"}, cookies["admin"])
	if adminUpdate.Code != http.StatusOK {
		t.Fatalf("claimed instance admin settings PATCH status=%d body=%s", adminUpdate.Code, adminUpdate.Body)
	}
	ownerRestore := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "invite_only"}, cookies["owner"])
	if ownerRestore.Code != http.StatusOK {
		t.Fatalf("owner settings PATCH after admin update status=%d body=%s", ownerRestore.Code, ownerRestore.Body)
	}
	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["ordinary"]); got.Code != http.StatusForbidden {
		t.Fatalf("ordinary account settings GET status=%d body=%s", got.Code, got.Body)
	}
	deniedWrite := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "disabled"}, cookies["ordinary"])
	if deniedWrite.Code != http.StatusForbidden {
		t.Fatalf("ordinary account settings PATCH status=%d body=%s", deniedWrite.Code, deniedWrite.Body)
	}
	var policy string
	if err := db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&policy); err != nil || policy != "invite_only" {
		t.Fatalf("denied settings update changed policy to %q: err=%v", policy, err)
	}
	csrfRequest := httptest.NewRequest(http.MethodPatch, "/api/admin/settings", strings.NewReader(`{"registration_policy":"disabled"}`))
	csrfRequest.AddCookie(cookies["admin"])
	csrfResponse := httptest.NewRecorder()
	handler.ServeHTTP(csrfResponse, csrfRequest)
	if csrfResponse.Code != http.StatusForbidden || !strings.Contains(csrfResponse.Body.String(), "csrf_origin_required") {
		t.Fatalf("settings PATCH lost CSRF protection: status=%d body=%s", csrfResponse.Code, csrfResponse.Body)
	}
	staleAt := time.Now().UTC().Add(-recentAuthenticationWindow - time.Minute).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE sessions SET created_at=? WHERE token_hash=?`, staleAt, sessionTokenHash(cookies["admin"].Value)); err != nil {
		t.Fatal(err)
	}
	staleRecentAuth := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "disabled"}, cookies["admin"])
	if staleRecentAuth.Code != http.StatusForbidden || !strings.Contains(staleRecentAuth.Body.String(), "reauthentication_required") {
		t.Fatalf("settings PATCH lost recent-auth protection: status=%d body=%s", staleRecentAuth.Code, staleRecentAuth.Body)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cookies["admin"] = &http.Cookie{Name: sessionCookie, Value: adminToken}

	created := callAPI(handler, http.MethodPost, "/api/instance/owner-transfers", map[string]string{"target_user_id": "successor"}, cookies["owner"])
	var transfer instanceOwnerTransferResponse
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &transfer) != nil {
		t.Fatalf("owner transfer create status=%d body=%s", created.Code, created.Body)
	}
	accepted := callAPI(handler, http.MethodPost, "/api/instance/owner-transfers/"+transfer.ID+"/accept", nil, cookies["successor"])
	if accepted.Code != http.StatusOK {
		t.Fatalf("owner transfer accept status=%d body=%s", accepted.Code, accepted.Body)
	}
	for _, accountID := range []string{"owner"} {
		if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies[accountID]); got.Code != http.StatusForbidden {
			t.Fatalf("former owner retained settings read access: status=%d body=%s", got.Code, got.Body)
		}
		if got := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "disabled"}, cookies[accountID]); got.Code != http.StatusForbidden {
			t.Fatalf("former owner retained settings write access: status=%d body=%s", got.Code, got.Body)
		}
	}
	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["successor"]); got.Code != http.StatusOK {
		t.Fatalf("new owner settings access status=%d body=%s", got.Code, got.Body)
	}
	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["admin"]); got.Code != http.StatusOK {
		t.Fatalf("admin access disappeared after owner transfer: status=%d body=%s", got.Code, got.Body)
	}
	if err := db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&policy); err != nil || policy != "invite_only" {
		t.Fatalf("denied former-owner update changed policy to %q: err=%v", policy, err)
	}

	revoked := callAPI(handler, http.MethodPatch, "/api/admin/users/admin", map[string]bool{"is_admin": false}, cookies["successor"])
	if revoked.Code != http.StatusOK {
		t.Fatalf("new owner could not revoke admin role: status=%d body=%s", revoked.Code, revoked.Body)
	}
	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["admin"]); got.Code != http.StatusForbidden {
		t.Fatalf("revoked admin retained settings read access: status=%d body=%s", got.Code, got.Body)
	}
	revokedWrite := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "disabled"}, cookies["admin"])
	if revokedWrite.Code != http.StatusForbidden {
		t.Fatalf("revoked admin retained settings write access: status=%d body=%s", revokedWrite.Code, revokedWrite.Body)
	}
	if got := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookies["successor"]); got.Code != http.StatusOK {
		t.Fatalf("new owner lost settings access after admin revocation: status=%d body=%s", got.Code, got.Body)
	}
	if err := db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&policy); err != nil || policy != "invite_only" {
		t.Fatalf("denied revoked-admin update changed policy to %q: err=%v", policy, err)
	}
}

func TestAdminRetentionUpdatePurgesExpiredHistoryImmediately(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now'),('owner','owner@example.test','Owner',0,'now');
		INSERT INTO recipes(id,owner_id,creator_name,title,visibility,edit_policy,version,created_at,updated_at) VALUES('recipe','owner','Owner','Soup','private','owner_only',2,'now','now')`); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -400).Format(time.RFC3339Nano)
	recent := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at) VALUES('old-revision','recipe',1,'owner','Old','','[]','[]','private',?),('current-revision','recipe',2,'owner','Current','','[]','[]','private',?);
		INSERT INTO audit_events(id,action,resource_type,response_status,created_at) VALUES('old-audit','PATCH /api/recipes/{id}','recipes',200,?),('recent-audit','PATCH /api/recipes/{id}','recipes',200,?)`, old, recent, old, recent); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, testAuth(db), config{address: "127.0.0.1:8000", mediaDir: t.TempDir()})
	response := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]int{"revision_retention_days": 90, "audit_retention_days": 90}, &http.Cookie{Name: sessionCookie, Value: token})
	if response.Code != http.StatusOK {
		t.Fatalf("retention update status=%d body=%s", response.Code, response.Body)
	}
	var oldRevisionCount, currentRevisionCount, oldAuditCount, recentAuditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE id='old-revision'`).Scan(&oldRevisionCount); err != nil || oldRevisionCount != 0 {
		t.Fatalf("expired revision count=%d err=%v", oldRevisionCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE id='current-revision'`).Scan(&currentRevisionCount); err != nil || currentRevisionCount != 1 {
		t.Fatalf("current revision count=%d err=%v", currentRevisionCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE id='old-audit'`).Scan(&oldAuditCount); err != nil || oldAuditCount != 0 {
		t.Fatalf("expired audit count=%d err=%v", oldAuditCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE id='recent-audit'`).Scan(&recentAuditCount); err != nil || recentAuditCount != 1 {
		t.Fatalf("recent audit count=%d err=%v", recentAuditCount, err)
	}
}
