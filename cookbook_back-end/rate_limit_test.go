package main

import (
	"net"
	"net/http/httptest"
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
