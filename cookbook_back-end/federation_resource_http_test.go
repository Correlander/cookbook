package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFederationResourceAdminReadAndLimitUpdate(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now'),('viewer','viewer@example.test','Viewer',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	body := resourceEvent(t, "11111111111111111111111111111111", federationEventSnapshot, 1, testFederationID, "Private payload title")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, body); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	denied := httptest.NewRecorder()
	h.federationResources(denied, requestForUser(http.MethodGet, "/api/admin/federation/resources", "viewer"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("ordinary account status=%d body=%s", denied.Code, denied.Body.String())
	}
	missing := httptest.NewRecorder()
	h.federationResources(missing, requestForUser(http.MethodGet, "/api/admin/federation/resources", ""))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("anonymous status=%d body=%s", missing.Code, missing.Body.String())
	}
	read := httptest.NewRecorder()
	h.federationResources(read, requestForUser(http.MethodGet, "/api/admin/federation/resources", "admin"))
	if read.Code != http.StatusOK || strings.Contains(read.Body.String(), "Private payload title") || strings.Contains(read.Body.String(), "body_digest") {
		t.Fatalf("unsafe resource view: status=%d body=%s", read.Code, read.Body.String())
	}
	var snapshot federationResourceSnapshot
	if err := json.Unmarshal(read.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.GlobalUsage.ObjectRows != 1 || snapshot.GlobalUsage.EventRows != 1 || len(snapshot.Origins) != 1 || snapshot.Origins[0].Origin != testFederationOrigin {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	belowDefaults := snapshot.DefaultPeerLimits
	belowDefaults.ObjectRows, belowDefaults.ObjectBytes = 1, 1
	defaultReduction, _ := json.Marshal(map[string]any{"global_limits": snapshot.GlobalLimits, "default_peer_limits": belowDefaults, "origins": []federationResourceOrigin{}})
	below := defaultFederationResourceLimits()
	below.ObjectRows, below.ObjectBytes, below.EventRows, below.EventBytes = 1, 1, 1, 1
	requestBody, _ := json.Marshal(map[string]any{"global_limits": snapshot.GlobalLimits, "default_peer_limits": snapshot.DefaultPeerLimits, "origins": []federationResourceOrigin{{Origin: testFederationOrigin, Limits: below}}})
	patch := func(payload []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/admin/federation/resources", strings.NewReader(string(payload)))
		req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, "admin"))
		rec := httptest.NewRecorder()
		h.federationResources(rec, req)
		return rec
	}
	update := patch(requestBody)
	if update.Code != http.StatusConflict || !strings.Contains(update.Body.String(), "resource_limits_below_usage") {
		t.Fatalf("below-use limit update status=%d body=%s", update.Code, update.Body.String())
	}
	if got := patch(defaultReduction); got.Code != http.StatusConflict {
		t.Fatalf("below-use default limit update status=%d body=%s", got.Code, got.Body.String())
	}
	safe := defaultFederationResourceLimits()
	safe.ObjectRows, safe.ObjectBytes, safe.EventRows, safe.EventBytes = 100, 2<<20, 100, 2<<20
	safeDefaults := safe
	requestBody, _ = json.Marshal(map[string]any{"global_limits": snapshot.GlobalLimits, "default_peer_limits": safeDefaults, "origins": []federationResourceOrigin{{Origin: testFederationOrigin, Limits: safe}}})
	update = patch(requestBody)
	if update.Code != http.StatusOK {
		t.Fatalf("safe limit increase status=%d body=%s", update.Code, update.Body.String())
	}
	invalid := safe
	invalid.EventRows = maxPeerEventRows + 1
	requestBody, _ = json.Marshal(map[string]any{"global_limits": snapshot.GlobalLimits, "default_peer_limits": snapshot.DefaultPeerLimits, "origins": []federationResourceOrigin{{Origin: testFederationOrigin, Limits: invalid}}})
	update = patch(requestBody)
	if update.Code != http.StatusBadRequest || strings.Contains(update.Body.String(), testFederationOrigin) {
		t.Fatalf("invalid limit diagnostics status=%d body=%s", update.Code, update.Body.String())
	}
}
