package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestInstanceOwnerAPIExposesCapabilityAndTransfersOnlyThroughRecentSession(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('target','target@example.test','Target',0,'now'),('admin','admin@example.test','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner"); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	targetToken, _, err := issueSession(db, "target")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	targetCookie := &http.Cookie{Name: sessionCookie, Value: targetToken}
	adminCookie := &http.Cookie{Name: sessionCookie, Value: adminToken}
	capability := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, ownerCookie)
	var state instanceAuthorityResponse
	if capability.Code != http.StatusOK || capability.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(capability.Body.Bytes(), &state) != nil || !state.IsInstanceOwner || state.IsAdmin || !state.OwnerClaimed {
		t.Fatalf("owner capability status=%d cache=%q body=%s state=%+v", capability.Code, capability.Header().Get("Cache-Control"), capability.Body, state)
	}
	adminRole := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, adminCookie)
	if adminRole.Code != http.StatusOK || json.Unmarshal(adminRole.Body.Bytes(), &state) != nil || state.IsInstanceOwner || !state.IsAdmin || !state.OwnerClaimed {
		t.Fatalf("admin capability status=%d body=%s state=%+v", adminRole.Code, adminRole.Body, state)
	}
	created := callAPI(handler, http.MethodPost, "/api/instance/owner-transfers", map[string]string{"target_user_id": "target"}, ownerCookie)
	var transfer instanceOwnerTransferResponse
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &transfer) != nil || transfer.ID == "" || transfer.ToUserID != "target" || transfer.State != string(OwnerTransferPending) {
		t.Fatalf("transfer create status=%d body=%s transfer=%+v", created.Code, created.Body, transfer)
	}
	wrongAcceptor := callAPI(handler, http.MethodPost, "/api/instance/owner-transfers/"+transfer.ID+"/accept", nil, adminCookie)
	if wrongAcceptor.Code != http.StatusForbidden {
		t.Fatalf("wrong account accepted transfer: status=%d body=%s", wrongAcceptor.Code, wrongAcceptor.Body)
	}
	accepted := callAPI(handler, http.MethodPost, "/api/instance/owner-transfers/"+transfer.ID+"/accept", nil, targetCookie)
	if accepted.Code != http.StatusOK {
		t.Fatalf("target acceptance status=%d body=%s", accepted.Code, accepted.Body)
	}
	newCapability := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, targetCookie)
	if newCapability.Code != http.StatusOK || json.Unmarshal(newCapability.Body.Bytes(), &state) != nil || !state.IsInstanceOwner || state.IsAdmin {
		t.Fatalf("transferred owner capability status=%d body=%s state=%+v", newCapability.Code, newCapability.Body, state)
	}
	deniedAdminTransfer := callAPI(handler, http.MethodPost, "/api/instance/owner-transfers", map[string]string{"target_user_id": "owner"}, adminCookie)
	if deniedAdminTransfer.Code != http.StatusForbidden {
		t.Fatalf("ordinary admin initiated owner transfer: status=%d body=%s", deniedAdminTransfer.Code, deniedAdminTransfer.Body)
	}
}

func TestInstanceOwnerRoleDelegationSwitchesAtClaimWithoutContentBypass(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('admin','admin@example.test','Admin',1,'now'),('member','member@example.test','Member',0,'now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at)
		VALUES('private-recipe','member','Member','Private','[]','[]','private','owner_only','now','now')`); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	adminCookie := &http.Cookie{Name: sessionCookie, Value: adminToken}
	compatibilityPromotion := callAPI(handler, http.MethodPatch, "/api/admin/users/member", map[string]bool{"is_admin": true}, adminCookie)
	if compatibilityPromotion.Code != http.StatusOK {
		t.Fatalf("unclaimed admin compatibility promotion status=%d body=%s", compatibilityPromotion.Code, compatibilityPromotion.Body)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner"); err != nil {
		t.Fatal(err)
	}
	ownerList := callAPI(handler, http.MethodGet, "/api/admin/users", nil, ownerCookie)
	if ownerList.Code != http.StatusOK {
		t.Fatalf("non-admin owner could not inspect account IDs for role delegation: status=%d body=%s", ownerList.Code, ownerList.Body)
	}
	adminList := callAPI(handler, http.MethodGet, "/api/admin/users", nil, adminCookie)
	if adminList.Code != http.StatusOK {
		t.Fatalf("ordinary admin lost account listing after claim: status=%d body=%s", adminList.Code, adminList.Body)
	}
	ordinaryAdminPromotion := callAPI(handler, http.MethodPatch, "/api/admin/users/admin", map[string]bool{"is_admin": true}, adminCookie)
	if ordinaryAdminPromotion.Code != http.StatusForbidden {
		t.Fatalf("claimed ordinary admin changed roles: status=%d body=%s", ordinaryAdminPromotion.Code, ordinaryAdminPromotion.Body)
	}
	ownerPromotion := callAPI(handler, http.MethodPatch, "/api/admin/users/member", map[string]bool{"is_admin": true}, ownerCookie)
	if ownerPromotion.Code != http.StatusOK {
		t.Fatalf("non-admin owner could not delegate admin: status=%d body=%s", ownerPromotion.Code, ownerPromotion.Body)
	}
	ownerPrivateRead := callAPI(handler, http.MethodGet, "/api/recipes/private-recipe", nil, ownerCookie)
	if ownerPrivateRead.Code == http.StatusOK {
		t.Fatalf("instance owner read another user's private recipe: %s", ownerPrivateRead.Body)
	}
	var ownerAdmin int
	if err := db.QueryRow(`SELECT is_admin FROM users WHERE id='owner'`).Scan(&ownerAdmin); err != nil || ownerAdmin != 0 {
		t.Fatalf("owner admin flag=%d err=%v, owner status must not imply admin", ownerAdmin, err)
	}
}
