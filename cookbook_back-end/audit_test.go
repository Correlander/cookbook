package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAuditEventsRecordSuccessfulChangesAndRequireAdmin(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now'),('member','member@example.test','Member',0,'now')`); err != nil {
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
	h := handlerWithAuth(db, testAuth(db))
	created := callAPI(h, http.MethodPost, "/api/recipes", map[string]any{"title": "Soup", "ingredients": []map[string]string{{"name": "water"}}, "steps": []map[string]string{{"text": "Boil"}}}, &http.Cookie{Name: sessionCookie, Value: memberToken})
	if created.Code != http.StatusOK {
		t.Fatalf("recipe create status=%d: %s", created.Code, created.Body)
	}
	var recipeItem recipe
	if err := json.Unmarshal(created.Body.Bytes(), &recipeItem); err != nil {
		t.Fatal(err)
	}
	deniedUpdate := callAPI(h, http.MethodPut, "/api/recipes/"+recipeItem.ID, map[string]any{"title": "Changed", "ingredients": []map[string]string{{"name": "water"}}, "steps": []map[string]string{{"text": "Boil"}}, "version": 1}, &http.Cookie{Name: sessionCookie, Value: adminToken})
	if deniedUpdate.Code != http.StatusForbidden {
		t.Fatalf("non-owner recipe update status=%d, want 403", deniedUpdate.Code)
	}
	list := callAPI(h, http.MethodGet, "/api/admin/audit?limit=10&offset=0", nil, &http.Cookie{Name: sessionCookie, Value: adminToken})
	var events []auditEvent
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &events) != nil || len(events) != 2 {
		t.Fatalf("audit response=%d %s; events=%#v", list.Code, list.Body, events)
	}
	var sawCreate, sawDenied bool
	for _, event := range events {
		if event.ActorID == "member" && event.ActorName == "Member" && event.Action == "recipe.publication.initialize" && event.ResourceType == "recipes" && event.ResourceID == recipeItem.ID && event.ResponseStatus == http.StatusOK {
			sawCreate = true
		}
		if event.ActorID == "admin" && event.Action == "PUT /recipes/{id}" && event.ResourceType == "recipes" && event.ResourceID == recipeItem.ID && event.ResponseStatus == http.StatusForbidden {
			sawDenied = true
		}
	}
	if !sawCreate || !sawDenied {
		t.Fatalf("missing create or denied update audit event: %#v", events)
	}
	denied := callAPI(h, http.MethodGet, "/api/admin/audit", nil, &http.Cookie{Name: sessionCookie, Value: memberToken})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-admin audit status=%d, want 403", denied.Code)
	}
	invalid := callAPI(h, http.MethodPost, "/api/recipes", map[string]any{"title": ""}, &http.Cookie{Name: sessionCookie, Value: memberToken})
	if invalid.Code < 400 {
		t.Fatalf("invalid recipe create status=%d, want failure", invalid.Code)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("failed mutation was audited: count=%d err=%v", count, err)
	}
}
