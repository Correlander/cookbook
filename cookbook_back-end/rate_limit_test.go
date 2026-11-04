package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthRateLimitCapacityAllowsExistingBucketAtRowCap(t *testing.T) {
	db := testDatabase(t)
	now := time.Now().UTC().Truncate(time.Hour)
	window := now.Unix()
	target := sha256.Sum256([]byte("email\x00kept-subject"))
	targetKey := hex.EncodeToString(target[:])
	stmt, err := db.Prepare(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES(?,?,1)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if _, err := stmt.Exec(targetKey, window); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < authRateLimitMaxRows; i++ {
		key := fmt.Sprintf("row-%05d", i)
		if _, err := stmt.Exec(key, window); err != nil {
			t.Fatal(err)
		}
	}
	allowed, err := allowAuthBucketContext(context.Background(), db, "email", "kept-subject", 3, time.Hour, now)
	if err != nil || !allowed {
		t.Fatalf("existing bucket update at cap allowed=%v err=%v", allowed, err)
	}
	allowed, err = allowAuthBucketContext(context.Background(), db, "email", "new-subject", 3, time.Hour, now)
	if allowed || !errors.Is(err, errAuthRateLimitCapacity) {
		t.Fatalf("new bucket at cap allowed=%v err=%v; want typed capacity error", allowed, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&count); err != nil || count != authRateLimitMaxRows {
		t.Fatalf("row count=%d err=%v; want exact row cap", count, err)
	}
}

func TestAuthRateLimitCapacityLogicalByteCeiling(t *testing.T) {
	db := testDatabase(t)
	now := time.Now().UTC()
	targetHash := sha256.Sum256([]byte("byte\x00existing"))
	targetKey := hex.EncodeToString(targetHash[:])
	filler := strings.Repeat("k", int(int64(authRateLimitMaxLogicalBytes)-authRateLimitLogicalBytes(targetKey)-16))
	if _, err := db.Exec(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES(?,?,1),(?,?,1)`, targetKey, now.Unix(), filler, now.Unix()); err != nil {
		t.Fatal(err)
	}
	_, bytes, err := authRateLimitUsage(context.Background(), db)
	if err != nil || bytes != authRateLimitMaxLogicalBytes {
		t.Fatalf("logical bytes=%d err=%v; want exact %d-byte cap", bytes, err, authRateLimitMaxLogicalBytes)
	}
	allowed, err := allowAuthBucketContext(context.Background(), db, "byte", "existing", 3, time.Hour, now)
	if err != nil || !allowed {
		t.Fatalf("existing bucket update at byte cap allowed=%v err=%v", allowed, err)
	}
	allowed, err = allowAuthBucketContext(context.Background(), db, "byte", "new", 3, time.Hour, now)
	if allowed || !errors.Is(err, errAuthRateLimitCapacity) {
		t.Fatalf("new bucket at byte cap allowed=%v err=%v; want typed capacity error", allowed, err)
	}
}

func TestConcurrentAuthRateLimitNewBucketsStayWithinRowCap(t *testing.T) {
	db := testDatabase(t)
	now := time.Now().UTC().Truncate(time.Hour)
	stmt, err := db.Prepare(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES(?,?,1)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < authRateLimitMaxRows-10; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("seed-%05d", i), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	const workers = 30
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			allowed, err := allowAuthBucketContext(context.Background(), db, "race", fmt.Sprintf("subject-%d", i), 1, time.Hour, now)
			if err != nil && !errors.Is(err, errAuthRateLimitCapacity) {
				t.Errorf("bucket admission: %v", err)
			}
			if allowed {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if accepted != 10 || rows != authRateLimitMaxRows {
		t.Fatalf("accepted=%d rows=%d; want 10 admitted and exact cap", accepted, rows)
	}
}

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
