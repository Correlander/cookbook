package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestAuthRequestLimiterBlocksAfterLimit(t *testing.T) {
	db := testDatabase(t)
	req := httptest.NewRequest("POST", "/api/auth/email", nil)
	req.RemoteAddr = "198.51.100.24:4312"
	for i := 0; i < 2; i++ {
		allowed, err := allowAuthRequest(db, req, "email", 2, time.Hour, nil)
		if err != nil || !allowed {
			t.Fatalf("request %d allowed=%v err=%v", i+1, allowed, err)
		}
	}
	allowed, err := allowAuthRequest(db, req, "email", 2, time.Hour, nil)
	if err != nil || allowed {
		t.Fatalf("over-limit request allowed=%v err=%v", allowed, err)
	}
	var storedCount int
	if err := db.QueryRow(`SELECT SUM(request_count) FROM auth_rate_limits`).Scan(&storedCount); err != nil || storedCount != 2 {
		t.Fatalf("blocked requests changed the stored count to %d (err=%v), want it capped at 2", storedCount, err)
	}
}

func TestPasskeyLoginFinishIsRateLimitedByClientIP(t *testing.T) {
	db := testDatabase(t)
	handler := handlerWithAuth(db, testAuth(db))
	for attempt := 1; attempt <= 60; attempt++ {
		response := callAPI(handler, http.MethodPost, "/api/auth/passkey/login/finish", nil, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("finish attempt %d status = %d, want %d", attempt, response.Code, http.StatusBadRequest)
		}
	}
	blocked := callAPI(handler, http.MethodPost, "/api/auth/passkey/login/finish", nil, nil)
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") != "3600" {
		t.Fatalf("over-limit finish status=%d retry-after=%q body=%q", blocked.Code, blocked.Header().Get("Retry-After"), blocked.Body.String())
	}
}

func TestEmailSubjectLimiterAppliesConcurrentLimit(t *testing.T) {
	db := testDatabase(t)
	const requests = 20
	start := make(chan struct{})
	results := make(chan bool, requests)
	errors := make(chan error, requests)
	var workers sync.WaitGroup
	for i := 0; i < requests; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			allowed, err := allowAuthSubject(db, "email-link-address", "cook@example.test", 5, time.Hour)
			results <- allowed
			if err != nil {
				errors <- err
			}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != 5 {
		t.Fatalf("concurrent allowed requests=%d, want 5", allowedCount)
	}
	allowed, err := allowAuthSubject(db, "email-link-address", "other@example.test", 5, time.Hour)
	if err != nil || !allowed {
		t.Fatalf("separate email subject allowed=%v err=%v", allowed, err)
	}
}

func TestForwardedClientAddressRequiresTrustedImmediateProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientAddress(req, nil); got != "10.0.0.2" {
		t.Fatalf("untrusted forwarded address=%q", got)
	}
	_, trusted, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if got := clientAddress(req, []*net.IPNet{trusted}); got != "203.0.113.9" {
		t.Fatalf("trusted forwarded address=%q", got)
	}
}
