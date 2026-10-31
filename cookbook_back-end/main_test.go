package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	db := testDatabase(t)
	recorder := httptest.NewRecorder()
	handlerWithAuth(db, testAuth(db)).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, location=%q body=%q, want %d", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String(), http.StatusOK)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestUnknownPageServesFrontendShell(t *testing.T) {
	db := testDatabase(t)
	recorder := httptest.NewRecorder()
	handlerWithAuth(db, testAuth(db)).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d location=%q body=%q, want %d", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String(), http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestConfigDefaultsAndOverride(t *testing.T) {
	t.Setenv("COOKBOOK_ADDR", "")
	t.Setenv("COOKBOOK_DATABASE", "./data/cookbook.db")
	t.Setenv("COOKBOOK_MEDIA_DIR", "")
	t.Setenv("COOKBOOK_TRASH_RETENTION_DAYS", "")
	cfg, err := loadConfig()
	if err != nil || cfg.address != "127.0.0.1:8000" || cfg.mediaDir != filepath.Join("data", "media") || cfg.trashRetentionDays != 30 {
		t.Fatalf("default config = %#v, %v", cfg, err)
	}

	t.Setenv("COOKBOOK_ADDR", "127.0.0.1:9000")
	t.Setenv("COOKBOOK_MEDIA_DIR", "./custom-images")
	cfg, err = loadConfig()
	if err != nil || cfg.address != "127.0.0.1:9000" || cfg.mediaDir != "./custom-images" {
		t.Fatalf("override config = %#v, %v", cfg, err)
	}
	t.Setenv("COOKBOOK_TRASH_RETENTION_DAYS", "0")
	cfg, err = loadConfig()
	if err != nil || cfg.trashRetentionDays != 0 {
		t.Fatalf("disabled retention config = %#v, %v", cfg, err)
	}
	t.Setenv("COOKBOOK_TRASH_RETENTION_DAYS", "3651")
	if _, err := loadConfig(); err == nil {
		t.Fatal("retention longer than 3650 days was accepted")
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

func TestAdministratorBootstrapIsOneTimeAndConfigurable(t *testing.T) {
	db := testDatabase(t)
	t.Setenv("COOKBOOK_ADMIN_EMAIL", "owner@example.test")
	t.Setenv("COOKBOOK_ADMIN_NAME", "Instance Owner")
	if err := provisionAdministrator(db); err != nil {
		t.Fatal(err)
	}
	var email, name string
	var admin int
	if err := db.QueryRow(`SELECT email,display_name,is_admin FROM users`).Scan(&email, &name, &admin); err != nil || email != "owner@example.test" || name != "Instance Owner" || admin != 1 {
		t.Fatalf("bootstrap account = %q %q admin=%d, err=%v", email, name, admin, err)
	}
	if err := provisionAdministrator(db); err != nil {
		t.Fatalf("persistent bootstrap setting should be harmless after first admin: %v", err)
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
