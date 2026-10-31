package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestAdministratorAccountManagementProtectsAdminAccess(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('admin','admin@example.test','Admin',1,'2026-01-01'),
		('member','member@example.test','Member',0,'2026-01-02')`); err != nil {
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

	anonymous := callAPI(handler, http.MethodGet, "/api/admin/users", nil, nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous account listing status=%d, want 401", anonymous.Code)
	}
	denied := callAPI(handler, http.MethodGet, "/api/admin/users", nil, memberCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-admin account listing status=%d, want 403", denied.Code)
	}
	deniedChange := callAPI(handler, http.MethodPatch, "/api/admin/users/member", map[string]bool{"is_admin": true}, memberCookie)
	if deniedChange.Code != http.StatusForbidden {
		t.Fatalf("non-admin role change status=%d, want 403", deniedChange.Code)
	}
	list := callAPI(handler, http.MethodGet, "/api/admin/users?limit=10", nil, adminCookie)
	var users []adminUser
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &users) != nil || len(users) != 2 {
		t.Fatalf("admin account listing status=%d body=%s", list.Code, list.Body)
	}
	selfChange := callAPI(handler, http.MethodPatch, "/api/admin/users/admin", map[string]bool{"is_admin": false}, adminCookie)
	if selfChange.Code != http.StatusConflict {
		t.Fatalf("self-demotion status=%d, want 409", selfChange.Code)
	}
	promote := callAPI(handler, http.MethodPatch, "/api/admin/users/member", map[string]bool{"is_admin": true}, adminCookie)
	var promoted adminUser
	if promote.Code != http.StatusOK || json.Unmarshal(promote.Body.Bytes(), &promoted) != nil || !promoted.IsAdmin {
		t.Fatalf("promote account status=%d body=%s", promote.Code, promote.Body)
	}
	newAdminList := callAPI(handler, http.MethodGet, "/api/admin/users", nil, memberCookie)
	if newAdminList.Code != http.StatusOK {
		t.Fatalf("promoted user did not gain admin access: status=%d", newAdminList.Code)
	}

	// A second administrator can demote the first, but the remaining administrator cannot be removed.
	demote := callAPI(handler, http.MethodPatch, "/api/admin/users/admin", map[string]bool{"is_admin": false}, memberCookie)
	if demote.Code != http.StatusOK {
		t.Fatalf("demote another administrator status=%d body=%s", demote.Code, demote.Body)
	}
	if err := setUserAdministrator(db, "member", false); !errors.Is(err, errLastAdministrator) {
		t.Fatalf("last administrator demotion error=%v, want errLastAdministrator", err)
	}
	var isAdmin int
	if err := db.QueryRow(`SELECT is_admin FROM users WHERE id='member'`).Scan(&isAdmin); err != nil || isAdmin != 1 {
		t.Fatalf("last administrator role=%d err=%v", isAdmin, err)
	}
	audit := callAPI(handler, http.MethodGet, "/api/admin/audit?limit=10", nil, memberCookie)
	var events []auditEvent
	if audit.Code != http.StatusOK || json.Unmarshal(audit.Body.Bytes(), &events) != nil {
		t.Fatalf("admin role audit listing status=%d body=%s", audit.Code, audit.Body)
	}
	var sawRedactedRoleChange bool
	for _, event := range events {
		if event.Action == "PATCH /admin/users/{id}" && event.ResourceType == "admin" && event.ResourceID == "" {
			sawRedactedRoleChange = true
		}
	}
	if !sawRedactedRoleChange {
		t.Fatalf("role change audit action missing or exposed target id: %#v", events)
	}
}
