package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplicationServerUsesBoundedTimeouts(t *testing.T) {
	server := newApplicationServer("127.0.0.1:0", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("read header timeout = %s, want 5s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 3*time.Minute {
		t.Errorf("read timeout = %s, want 3m", server.ReadTimeout)
	}
	if server.WriteTimeout != 3*time.Minute {
		t.Errorf("write timeout = %s, want 3m", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Errorf("idle timeout = %s, want 60s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Errorf("maximum header bytes = %d, want 1 MiB", server.MaxHeaderBytes)
	}
}

func TestRunHTTPServerDrainsInFlightRequestOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	server := newApplicationServer(listener.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- runHTTPServer(ctx, server, listener, 2*time.Second)
	}()

	requestResult := make(chan error, 1)
	go func() {
		response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + listener.Addr().String())
		if requestErr == nil {
			if response.StatusCode != http.StatusNoContent {
				requestErr = fmt.Errorf("response status = %d, want %d", response.StatusCode, http.StatusNoContent)
			}
			_ = response.Body.Close()
		}
		requestResult <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the handler")
	}

	cancel()
	select {
	case err := <-serveResult:
		t.Fatalf("server stopped before the active request finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)

	select {
	case err := <-requestResult:
		if err != nil {
			t.Fatalf("active request failed during graceful shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active request did not finish")
	}
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("server shutdown returned an error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not exit after the active request finished")
	}
}

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

func TestAPIResponsesAreNotCacheable(t *testing.T) {
	db := testDatabase(t)
	recorder := httptest.NewRecorder()
	handlerWithAuth(db, testAuth(db)).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/public/recipes", nil))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("API response status=%d cache-control=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
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
	t.Setenv("COOKBOOK_TRUSTED_PROXIES", "")
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
	t.Setenv("COOKBOOK_TRASH_RETENTION_DAYS", "30")
	t.Setenv("COOKBOOK_TRUSTED_PROXIES", "not-a-cidr")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid trusted proxy range was accepted")
	}
	t.Setenv("COOKBOOK_TRUSTED_PROXIES", "10.0.0.0/8,192.168.1.0/24")
	cfg, err = loadConfig()
	if err != nil || len(cfg.trustedProxies) != 2 {
		t.Fatalf("trusted proxies=%v err=%v", cfg.trustedProxies, err)
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
	t.Setenv("COOKBOOK_REGISTRATION", "disabled")
	if err := configureRegistrationPolicy(db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&policy); err != nil || policy != "open" {
		t.Fatalf("configured registration policy changed on restart: %q, %v", policy, err)
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
