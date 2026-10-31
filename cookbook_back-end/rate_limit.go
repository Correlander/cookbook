package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"
)

func allowAuthRequest(db *sql.DB, r *http.Request, action string, limit int, window time.Duration, trusted []*net.IPNet) (bool, error) {
	return allowAuthBucket(db, action, clientAddress(r, trusted), limit, window)
}

func allowAuthSubject(db *sql.DB, action, subject string, limit int, window time.Duration) (bool, error) {
	return allowAuthBucket(db, action, subject, limit, window)
}

func allowAuthBucket(db *sql.DB, action, subject string, limit int, window time.Duration) (bool, error) {
	key := sha256.Sum256([]byte(action + "\x00" + subject))
	windowSeconds := int64(window / time.Second)
	now := time.Now().Unix()
	windowStart := now - now%windowSeconds
	var count int
	if err := db.QueryRow(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES(?,?,1) ON CONFLICT(bucket_key) DO UPDATE SET window_start=excluded.window_start,request_count=CASE WHEN auth_rate_limits.window_start=excluded.window_start THEN auth_rate_limits.request_count+1 ELSE 1 END RETURNING request_count`, hex.EncodeToString(key[:]), windowStart).Scan(&count); err != nil {
		return false, err
	}
	if now%97 == 0 {
		_, _ = db.Exec(`DELETE FROM auth_rate_limits WHERE window_start<?`, windowStart-int64((24*time.Hour)/time.Second))
	}
	return count <= limit, nil
}

func clientAddress(r *http.Request, trusted []*net.IPNet) string {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remote = r.RemoteAddr
	}
	remoteIP := net.ParseIP(strings.Trim(remote, "[]"))
	if remoteIP == nil {
		return "unknown"
	}
	if !isTrustedProxy(remoteIP, trusted) {
		return remoteIP.String()
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	chain := make([]net.IP, 0, len(forwarded)+1)
	for _, raw := range forwarded {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil {
			return remoteIP.String()
		}
		chain = append(chain, ip)
	}
	chain = append(chain, remoteIP)
	for i := len(chain) - 1; i >= 0; i-- {
		if isTrustedProxy(chain[i], trusted) {
			continue
		}
		return chain[i].String()
	}
	return remoteIP.String()
}

func isTrustedProxy(ip net.IP, trusted []*net.IPNet) bool {
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
