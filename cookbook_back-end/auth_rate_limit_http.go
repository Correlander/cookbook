package main

import (
	"errors"
	"net/http"
)

func writeAuthRateLimitError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errAuthRateLimitCapacity) {
		w.Header().Set("Retry-After", authRateLimitRetryAfter)
		writeError(w, http.StatusServiceUnavailable, "rate_limit_capacity")
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "rate_limit_unavailable")
	return true
}
