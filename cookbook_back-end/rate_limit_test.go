package main

import (
	"net"
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
