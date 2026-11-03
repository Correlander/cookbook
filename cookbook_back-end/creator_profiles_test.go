package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func callCreatorProfileAPI(t *testing.T, h http.Handler, method, path, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&encoded).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &encoded)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey{}, userID))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	return response
}

func TestCreatorProfileSelfAndHouseholdVisibilityDTO(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Private Account Name',0,'now'),
		('reader','reader@example.test','Reader',0,'now'),
		('admin','admin@example.test','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	missing := callCreatorProfileAPI(t, h, http.MethodGet, "/api/account/creator-profile", "owner", nil)
	if missing.Code != http.StatusOK || strings.TrimSpace(missing.Body.String()) != `{"profile":null}` {
		t.Fatalf("missing self profile status=%d body=%s", missing.Code, missing.Body)
	}
	privatePut := callCreatorProfileAPI(t, h, http.MethodPut, "/api/account/creator-profile", "owner", map[string]string{
		"public_name": "", "bio": "private notes", "visibility": "private",
	})
	if privatePut.Code != http.StatusOK || !strings.Contains(privatePut.Body.String(), `"profile":{`) {
		t.Fatalf("private profile put status=%d body=%s", privatePut.Code, privatePut.Body)
	}
	var envelope creatorProfileEnvelope
	if err := json.Unmarshal(privatePut.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Profile == nil || envelope.Profile.ID == "" || len(envelope.Profile.ID) != 32 || envelope.Profile.PublicName != "" || envelope.Profile.Visibility != "private" {
		t.Fatalf("unexpected private profile DTO: %+v", envelope.Profile)
	}
	var dto map[string]any
	if err := json.Unmarshal(privatePut.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	profileJSON, _ := json.Marshal(dto["profile"])
	for _, forbidden := range []string{"user_id", "email", "actor", "key", "Private Account Name"} {
		if strings.Contains(string(profileJSON), forbidden) {
			t.Fatalf("profile DTO leaked %q: %s", forbidden, profileJSON)
		}
	}
	selfGet := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+envelope.Profile.ID, "owner", nil)
	if selfGet.Code != http.StatusOK {
		t.Fatalf("self profile get status=%d body=%s", selfGet.Code, selfGet.Body)
	}
	for _, reader := range []string{"reader", "admin"} {
		response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+envelope.Profile.ID, reader, nil)
		if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("%s read private profile status=%d body=%s", reader, response.Code, response.Body)
		}
	}
	instancePut := callCreatorProfileAPI(t, h, http.MethodPut, "/api/account/creator-profile", "owner", map[string]string{
		"public_name": "🍲" + strings.Repeat("食", 79), "bio": "household profile", "visibility": "instance",
	})
	if instancePut.Code != http.StatusOK {
		t.Fatalf("instance profile put status=%d body=%s", instancePut.Code, instancePut.Body)
	}
	if err := json.Unmarshal(instancePut.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Profile.ID != dto["profile"].(map[string]any)["id"] || envelope.Profile.PublicName != "🍲"+strings.Repeat("食", 79) {
		t.Fatalf("profile ID/name did not round trip: %+v", envelope.Profile)
	}
	for _, reader := range []string{"reader", "admin"} {
		response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+envelope.Profile.ID, reader, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s read instance profile status=%d body=%s", reader, response.Code, response.Body)
		}
	}
}

func TestCreatorProfileValidatesInstanceNameAndBioBounds(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	for _, profile := range []map[string]string{
		{"public_name": "", "bio": "", "visibility": "instance"},
		{"public_name": strings.Repeat("x", 81), "bio": "", "visibility": "instance"},
		{"public_name": strings.Repeat("🍲", 81), "bio": "", "visibility": "private"},
		{"public_name": "Chef", "bio": strings.Repeat("b", 1001), "visibility": "private"},
		{"public_name": "Chef", "bio": "", "visibility": "public"},
	} {
		response := callCreatorProfileAPI(t, h, http.MethodPut, "/api/account/creator-profile", "owner", profile)
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid profile %#v status=%d body=%s", profile, response.Code, response.Body)
		}
	}
	valid := callCreatorProfileAPI(t, h, http.MethodPut, "/api/account/creator-profile", "owner", map[string]string{
		"public_name": strings.Repeat("🍲", 80), "bio": strings.Repeat("b", 1000), "visibility": "instance",
	})
	if valid.Code != http.StatusOK {
		t.Fatalf("80-scalar profile name status=%d body=%s", valid.Code, valid.Body)
	}
	if _, err := db.Exec(`UPDATE creator_profiles SET public_name='',visibility='instance'`); err == nil {
		t.Fatal("database accepted an unnamed instance-visible profile")
	}
}
