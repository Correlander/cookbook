package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	db := testDatabase(t)
	recorder := httptest.NewRecorder()
	handlerWithAuth(db, testAuth(db)).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestUnknownRouteReturnsJSONNotFound(t *testing.T) {
	db := testDatabase(t)
	recorder := httptest.NewRecorder()
	handlerWithAuth(db, testAuth(db)).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestConfigDefaultsAndOverride(t *testing.T) {
	t.Setenv("COOKBOOK_ADDR", "")
	cfg, err := loadConfig()
	if err != nil || cfg.address != "127.0.0.1:8000" {
		t.Fatalf("default config = %#v, %v", cfg, err)
	}

	t.Setenv("COOKBOOK_ADDR", "127.0.0.1:9000")
	cfg, err = loadConfig()
	if err != nil || cfg.address != "127.0.0.1:9000" {
		t.Fatalf("override config = %#v, %v", cfg, err)
	}
}

func TestRegistrationPolicyConfiguration(t *testing.T) {
	db := testDatabase(t)
	t.Setenv("COOKBOOK_REGISTRATION", "open")
	if err := configureRegistrationPolicy(db); err != nil {
		t.Fatal(err)
	}
	var policy string
	if err := db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&policy); err != nil || policy != "open" {
		t.Fatalf("registration policy = %q, %v", policy, err)
	}
	t.Setenv("COOKBOOK_REGISTRATION", "unknown")
	if err := configureRegistrationPolicy(db); err == nil {
		t.Fatal("invalid registration policy should be rejected")
	}
}

func testDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
