package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newOwnerTransferReadFixture(t *testing.T) (*sql.DB, http.Handler, map[string]*http.Cookie, InstanceOwnerTransfer) {
	t.Helper()
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner-read','owner-read@example.test','Owner Display',0,'now'),
		('target-read','target-read@example.test','Target Display',0,'now'),
		('admin-read','admin-read@example.test','Unrelated Admin',1,'now'),
		('member-read','member-read@example.test','Unrelated Member',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner-read"); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Truncate(time.Second)
	transfer, err := createInstanceOwnerTransferAt(context.Background(), db, "owner-read", "target-read", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	cookies := make(map[string]*http.Cookie)
	for _, userID := range []string{"owner-read", "target-read", "admin-read", "member-read"} {
		token, _, err := issueSession(db, userID)
		if err != nil {
			t.Fatal(err)
		}
		cookies[userID] = &http.Cookie{Name: sessionCookie, Value: token}
	}
	return db, handlerWithAuth(db, testAuth(db)), cookies, transfer
}

func TestOwnerTransferReadRouteScopesCurrentPendingTransfer(t *testing.T) {
	db, handler, cookies, transfer := newOwnerTransferReadFixture(t)
	unauthenticated := callAPI(handler, http.MethodGet, "/api/instance/owner-transfers", nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized || unauthenticated.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated read status=%d cache=%q body=%s", unauthenticated.Code, unauthenticated.Header().Get("Cache-Control"), unauthenticated.Body)
	}

	for _, test := range []struct {
		name             string
		userID           string
		wantDirection    string
		wantCounterparty string
	}{
		{name: "current owner sees outgoing", userID: "owner-read", wantDirection: "outgoing", wantCounterparty: "Target Display"},
		{name: "exact target sees incoming", userID: "target-read", wantDirection: "incoming", wantCounterparty: "Owner Display"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := callAPI(handler, http.MethodGet, "/api/instance/owner-transfers", nil, cookies[test.userID])
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("read status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body)
			}
			var got ownerTransferReadResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Transfers) != 1 {
				t.Fatalf("got %d transfers, want one: %s", len(got.Transfers), response.Body)
			}
			item := got.Transfers[0]
			if item.ID != transfer.ID || item.Direction != test.wantDirection || item.State != string(OwnerTransferPending) ||
				item.CreatedAt != formatOwnerTime(transfer.CreatedAt) || item.ExpiresAt != formatOwnerTime(transfer.ExpiresAt) ||
				item.CounterpartyDisplayName != test.wantCounterparty {
				t.Fatalf("unexpected transfer DTO: %+v", item)
			}
			for _, privateValue := range []string{"owner-read@example.test", "target-read@example.test", "owner-read", "target-read", "from_user_id", "to_user_id", "email", "completed_at"} {
				if strings.Contains(response.Body.String(), privateValue) {
					t.Errorf("response disclosed %q: %s", privateValue, response.Body)
				}
			}
		})
	}

	for _, userID := range []string{"admin-read", "member-read"} {
		response := callAPI(handler, http.MethodGet, "/api/instance/owner-transfers", nil, cookies[userID])
		var got ownerTransferReadResponse
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &got) != nil || len(got.Transfers) != 0 {
			t.Errorf("unrelated %s read status=%d cache=%q body=%s", userID, response.Code, response.Header().Get("Cache-Control"), response.Body)
		}
	}

	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action LIKE 'instance_owner.transfer.%'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	var ownerID string
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&ownerID, &generation); err != nil {
		t.Fatal(err)
	}
	var state string
	var completedAt sql.NullString
	if err := db.QueryRow(`SELECT state,completed_at FROM instance_owner_transfers WHERE id=?`, transfer.ID).Scan(&state, &completedAt); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || ownerID != "owner-read" || generation != 1 || state != string(OwnerTransferPending) || completedAt.Valid {
		t.Fatalf("GET changed owner state: audits=%d owner=%q generation=%d transfer=%q completed=%v", auditCount, ownerID, generation, state, completedAt)
	}
}

func TestOwnerTransferReadFiltersExpiredAndStaleWithoutMutation(t *testing.T) {
	db, handler, cookies, transfer := newOwnerTransferReadFixture(t)
	if _, err := db.Exec(`UPDATE instance_owner_transfers SET expires_at=? WHERE id=?`, formatOwnerTime(time.Now().UTC().Add(-time.Minute)), transfer.ID); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"owner-read", "target-read"} {
		response := callAPI(handler, http.MethodGet, "/api/instance/owner-transfers", nil, cookies[userID])
		var got ownerTransferReadResponse
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || len(got.Transfers) != 0 {
			t.Fatalf("expired transfer visible to %s: status=%d body=%s", userID, response.Code, response.Body)
		}
	}
	var state string
	var completedAt sql.NullString
	if err := db.QueryRow(`SELECT state,completed_at FROM instance_owner_transfers WHERE id=?`, transfer.ID).Scan(&state, &completedAt); err != nil {
		t.Fatal(err)
	}
	if state != string(OwnerTransferPending) || completedAt.Valid {
		t.Fatalf("expired GET mutated transfer: state=%q completed=%v", state, completedAt)
	}

	if _, err := db.Exec(`UPDATE instance_owner_transfers SET expires_at=? WHERE id=?`, formatOwnerTime(time.Now().UTC().Add(time.Hour)), transfer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_authority SET owner_user_id='admin-read',owner_claimed_at=?,generation=generation+1 WHERE singleton_id=1`, formatOwnerTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"owner-read", "target-read", "admin-read"} {
		response := callAPI(handler, http.MethodGet, "/api/instance/owner-transfers", nil, cookies[userID])
		var got ownerTransferReadResponse
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || len(got.Transfers) != 0 {
			t.Fatalf("stale transfer visible to %s: status=%d body=%s", userID, response.Code, response.Body)
		}
	}
	if err := db.QueryRow(`SELECT state,completed_at FROM instance_owner_transfers WHERE id=?`, transfer.ID).Scan(&state, &completedAt); err != nil {
		t.Fatal(err)
	}
	if state != string(OwnerTransferPending) || completedAt.Valid {
		t.Fatalf("stale GET mutated transfer: state=%q completed=%v", state, completedAt)
	}
}

func TestOwnerTransferReadStorageFailureIsNoStore(t *testing.T) {
	db, handler, cookies, _ := newOwnerTransferReadFixture(t)
	if _, err := db.Exec(`DROP TABLE instance_owner_transfers`); err != nil {
		t.Fatal(err)
	}
	response := callAPI(handler, http.MethodGet, "/api/instance/owner-transfers", nil, cookies["owner-read"])
	if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"error\":\"storage_error\"}\n" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("storage failure status=%d cache=%q body=%q", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestOwnerTransferReadMissingAuthorityIsStorageError(t *testing.T) {
	db, handler, cookies, _ := newOwnerTransferReadFixture(t)
	if _, err := db.Exec(`DELETE FROM instance_authority WHERE singleton_id=1`); err != nil {
		t.Fatal(err)
	}
	response := callAPI(handler, http.MethodGet, "/api/instance/owner-transfers", nil, cookies["owner-read"])
	if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"error\":\"storage_error\"}\n" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing authority status=%d cache=%q body=%q", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
}
