package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFederationRateLimitCapacityResponseIsPrivateAndRetryable(t *testing.T) {
	db := testDatabase(t)
	stmt, err := db.Prepare(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES(?,?,1)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for i := 0; i < authRateLimitMaxRows; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("seed-%05d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	request := httptest.NewRequest(http.MethodPost, "https://local.example/api/federation/inbox", strings.NewReader("{}"))
	request.Host = "local.example"
	request.RemoteAddr = "198.51.100.88:4312"
	handler := federationSignatureMiddleware(db, "https://local.example", nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != authRateLimitRetryAfter {
		t.Fatalf("capacity status=%d retry-after=%q body=%q", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
	if got := response.Body.String(); got != "{\"error\":\"rate_limit_capacity\"}\n" || strings.Contains(got, "198.51.100.88") {
		t.Fatalf("capacity response leaked detail or wrong code: %q", got)
	}
}

func TestSharedRateLimitHTTPErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantRetry  string
	}{
		{name: "capacity", err: authRateLimitCapacityError{}, wantStatus: http.StatusServiceUnavailable, wantCode: "rate_limit_capacity", wantRetry: authRateLimitRetryAfter},
		{name: "storage", err: errors.New("sensitive database detail"), wantStatus: http.StatusServiceUnavailable, wantCode: "rate_limit_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if !writeAuthRateLimitError(response, tc.err) {
				t.Fatal("error was not handled")
			}
			if response.Code != tc.wantStatus || response.Header().Get("Retry-After") != tc.wantRetry || !strings.Contains(response.Body.String(), tc.wantCode) || strings.Contains(response.Body.String(), "sensitive database detail") {
				t.Fatalf("status=%d retry=%q body=%q", response.Code, response.Header().Get("Retry-After"), response.Body.String())
			}
		})
	}
}
