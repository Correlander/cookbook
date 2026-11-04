package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	if capability.Code != http.StatusOK || capability.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(capability.Body.Bytes(), &state) != nil || !state.IsInstanceOwner || state.IsAdmin || !state.OwnerClaimed || !state.CanManageInstanceRoles || !state.CanManageInstanceSettings {
		t.Fatalf("owner capability status=%d cache=%q body=%s state=%+v", capability.Code, capability.Header().Get("Cache-Control"), capability.Body, state)
	}
	adminRole := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, adminCookie)
	if adminRole.Code != http.StatusOK || json.Unmarshal(adminRole.Body.Bytes(), &state) != nil || state.IsInstanceOwner || !state.IsAdmin || !state.OwnerClaimed || state.CanManageInstanceRoles || !state.CanManageInstanceSettings {
		t.Fatalf("admin capability status=%d body=%s state=%+v", adminRole.Code, adminRole.Body, state)
	}
	ordinaryRole := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, targetCookie)
	if ordinaryRole.Code != http.StatusOK || json.Unmarshal(ordinaryRole.Body.Bytes(), &state) != nil || state.IsInstanceOwner || state.IsAdmin || state.CanManageInstanceSettings || !strings.Contains(ordinaryRole.Body.String(), `"can_manage_instance_settings":false`) {
		t.Fatalf("ordinary capability status=%d body=%s state=%+v", ordinaryRole.Code, ordinaryRole.Body, state)
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
	if newCapability.Code != http.StatusOK || json.Unmarshal(newCapability.Body.Bytes(), &state) != nil || !state.IsInstanceOwner || state.IsAdmin || !state.CanManageInstanceSettings {
		t.Fatalf("transferred owner capability status=%d body=%s state=%+v", newCapability.Code, newCapability.Body, state)
	}
	formerOwnerCapability := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, ownerCookie)
	if formerOwnerCapability.Code != http.StatusOK || json.Unmarshal(formerOwnerCapability.Body.Bytes(), &state) != nil || state.IsInstanceOwner || state.IsAdmin || state.CanManageInstanceSettings {
		t.Fatalf("former owner retained settings capability: status=%d body=%s state=%+v", formerOwnerCapability.Code, formerOwnerCapability.Body, state)
	}
	deniedAdminTransfer := callAPI(handler, http.MethodPost, "/api/instance/owner-transfers", map[string]string{"target_user_id": "owner"}, adminCookie)
	if deniedAdminTransfer.Code != http.StatusForbidden {
		t.Fatalf("ordinary admin initiated owner transfer: status=%d body=%s", deniedAdminTransfer.Code, deniedAdminTransfer.Body)
	}
}

func TestInstanceAuthorityOwnerWhoIsAlsoAdminCanManageRoles(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('owner-admin','owner-admin@example.test','Owner Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner-admin"); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner-admin")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	response := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, &http.Cookie{Name: sessionCookie, Value: token})
	var got instanceAuthorityResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || !got.IsInstanceOwner || !got.IsAdmin || !got.OwnerClaimed || !got.CanManageInstanceRoles || !got.CanManageInstanceSettings {
		t.Fatalf("owner-admin capability status=%d body=%s capabilities=%+v", response.Code, response.Body, got)
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
	var capability instanceAuthorityResponse
	ownerBeforeClaim := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, ownerCookie)
	if ownerBeforeClaim.Code != http.StatusOK || json.Unmarshal(ownerBeforeClaim.Body.Bytes(), &capability) != nil || capability.OwnerClaimed || capability.CanManageInstanceRoles || capability.CanManageInstanceSettings {
		t.Fatalf("unclaimed non-admin owner capability status=%d body=%s capability=%+v", ownerBeforeClaim.Code, ownerBeforeClaim.Body, capability)
	}
	adminBeforeClaim := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, adminCookie)
	if adminBeforeClaim.Code != http.StatusOK || json.Unmarshal(adminBeforeClaim.Body.Bytes(), &capability) != nil || capability.OwnerClaimed || !capability.CanManageInstanceRoles || !capability.CanManageInstanceSettings {
		t.Fatalf("unclaimed admin capability status=%d body=%s capability=%+v", adminBeforeClaim.Code, adminBeforeClaim.Body, capability)
	}
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
	ownerAfterClaim := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, ownerCookie)
	if ownerAfterClaim.Code != http.StatusOK || json.Unmarshal(ownerAfterClaim.Body.Bytes(), &capability) != nil || !capability.OwnerClaimed || !capability.IsInstanceOwner || !capability.CanManageInstanceRoles || !capability.CanManageInstanceSettings {
		t.Fatalf("claimed non-admin owner capability status=%d body=%s capability=%+v", ownerAfterClaim.Code, ownerAfterClaim.Body, capability)
	}
	adminAfterClaim := callAPI(handler, http.MethodGet, "/api/instance/authority", nil, adminCookie)
	if adminAfterClaim.Code != http.StatusOK || json.Unmarshal(adminAfterClaim.Body.Bytes(), &capability) != nil || !capability.OwnerClaimed || !capability.IsAdmin || capability.CanManageInstanceRoles || !capability.CanManageInstanceSettings {
		t.Fatalf("claimed ordinary admin capability status=%d body=%s capability=%+v", adminAfterClaim.Code, adminAfterClaim.Body, capability)
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
