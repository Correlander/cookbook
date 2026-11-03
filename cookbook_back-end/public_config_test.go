package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicConfigReturnsOnlySafeFormConfiguration(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET registration_policy='open',public_instance=1,courses_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	publicConfigFor(db, testAuth(db)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/config", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET public config status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 4 {
		t.Fatalf("public config keys=%v, want exactly the safe four fields", body)
	}
	var got publicConfigResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.AuthMethods.EmailLink || !got.AuthMethods.Passkey || got.RegistrationPolicy != "open" || !got.PublicInstance || !got.CoursesEnabled {
		t.Fatalf("unexpected public config: %+v", got)
	}
	for _, forbidden := range []string{"smtp", "federation", "retention", "audit", "owner", "email", "key", "secret"} {
		if _, exists := body[forbidden]; exists {
			t.Fatalf("public config leaked field %q", forbidden)
		}
	}
}

func TestPublicConfigReflectsUnavailableAuthenticationMethods(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET registration_policy='invite_only',public_instance=0,courses_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	auth := testAuth(db)
	auth.sender = nil
	response := httptest.NewRecorder()
	publicConfigFor(db, auth).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/config", nil))
	var got publicConfigResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil {
		t.Fatalf("GET public config status=%d body=%s", response.Code, response.Body)
	}
	if got.AuthMethods.EmailLink || !got.AuthMethods.Passkey || got.RegistrationPolicy != "invite_only" || got.PublicInstance || got.CoursesEnabled {
		t.Fatalf("unexpected no-email public config: %+v", got)
	}

	auth.webauthn = nil
	response = httptest.NewRecorder()
	publicConfigFor(db, auth).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/config", nil))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || got.AuthMethods.Passkey {
		t.Fatalf("unavailable WebAuthn should be reported false: status=%d body=%s config=%+v", response.Code, response.Body, got)
	}
}

func TestPublicConfigRejectsMutationMethods(t *testing.T) {
	db := testDatabase(t)
	response := httptest.NewRecorder()
	publicConfigFor(db, testAuth(db)).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/public/config", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST public config status=%d body=%s, want 405", response.Code, response.Body)
	}
}
