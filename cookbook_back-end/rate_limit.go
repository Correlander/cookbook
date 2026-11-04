package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	authRateLimitMaxRows         = 10_000
	authRateLimitMaxLogicalBytes = 2 << 20
	authRateLimitRetryAfter      = "60"
)

var errAuthRateLimitCapacity = errors.New("authentication rate-limit capacity reached")

type authRateLimitCapacityError struct{}

func (authRateLimitCapacityError) Error() string { return errAuthRateLimitCapacity.Error() }
func (authRateLimitCapacityError) Is(target error) bool {
	return target == errAuthRateLimitCapacity
}

func allowAuthRequest(db *sql.DB, r *http.Request, action string, limit int, window time.Duration, trusted []*net.IPNet) (bool, error) {
	return allowAuthRequestContext(r.Context(), db, r, action, limit, window, trusted)
}

func allowAuthRequestContext(ctx context.Context, db *sql.DB, r *http.Request, action string, limit int, window time.Duration, trusted []*net.IPNet) (bool, error) {
	return allowAuthBucketContext(ctx, db, action, clientAddress(r, trusted), limit, window, time.Now())
}

func allowAuthSubject(db *sql.DB, action, subject string, limit int, window time.Duration) (bool, error) {
	return allowAuthBucketContext(context.Background(), db, action, subject, limit, window, time.Now())
}

func allowAuthSubjectContext(ctx context.Context, db *sql.DB, action, subject string, limit int, window time.Duration) (bool, error) {
	return allowAuthBucketContext(ctx, db, action, subject, limit, window, time.Now())
}

func allowAuthBucketContext(ctx context.Context, db *sql.DB, action, subject string, limit int, window time.Duration, now time.Time) (bool, error) {
	if window <= 0 || window%time.Second != 0 {
		return false, fmt.Errorf("invalid authentication rate-limit window")
	}
	key := sha256.Sum256([]byte(action + "\x00" + subject))
	bucketKey := hex.EncodeToString(key[:])
	windowSeconds := int64(window / time.Second)
	nowUnix := now.Unix()
	windowStart := nowUnix - nowUnix%windowSeconds
	var allowed bool
	err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_rate_limits WHERE bucket_key=?)`, bucketKey).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			if err := enforceAuthRateLimitCapacity(ctx, tx, bucketKey, now); err != nil {
				return err
			}
		}
		var count int
		err := tx.QueryRowContext(ctx, `INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES(?,?,1) ON CONFLICT(bucket_key) DO UPDATE SET window_start=excluded.window_start,request_count=CASE WHEN auth_rate_limits.window_start=excluded.window_start THEN auth_rate_limits.request_count+1 ELSE 1 END WHERE auth_rate_limits.window_start<>excluded.window_start OR auth_rate_limits.request_count<? RETURNING request_count`, bucketKey, windowStart, limit).Scan(&count)
		if errors.Is(err, sql.ErrNoRows) {
			allowed = false
			return nil
		}
		if err != nil {
			return err
		}
		allowed = count <= limit
		return nil
	})
	if err != nil {
		return false, err
	}
	return allowed, nil
}

func enforceAuthRateLimitCapacity(ctx context.Context, tx sqliteExecutor, key string, now time.Time) error {
	rows, bytes, err := authRateLimitUsage(ctx, tx)
	if err != nil {
		return err
	}
	logicalBytes := authRateLimitLogicalBytes(key)
	if rows >= authRateLimitMaxRows || bytes+logicalBytes > authRateLimitMaxLogicalBytes {
		if err := purgeStaleAuthRateLimitBatch(ctx, tx, now); err != nil {
			return err
		}
		rows, bytes, err = authRateLimitUsage(ctx, tx)
		if err != nil {
			return err
		}
	}
	if rows >= authRateLimitMaxRows || bytes+logicalBytes > authRateLimitMaxLogicalBytes {
		return authRateLimitCapacityError{}
	}
	return nil
}

func authRateLimitUsage(ctx context.Context, tx sqliteExecutor) (int, int64, error) {
	var rows int
	var bytes int64
	// A legacy table can exceed today's ceiling. Count at most cap+1 rows so
	// admission work stays bounded even before stale cleanup catches up.
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(logical_bytes),0) FROM (SELECT length(bucket_key)+16 AS logical_bytes FROM auth_rate_limits LIMIT ?)`, authRateLimitMaxRows+1).Scan(&rows, &bytes)
	return rows, bytes, err
}

func authRateLimitLogicalBytes(bucketKey string) int64 {
	// Count each INTEGER field at a fixed logical 64-bit width so an update
	// cannot grow a row past the byte cap as its decimal representation changes.
	return int64(len(bucketKey) + 16)
}

func purgeStaleAuthRateLimitBatch(ctx context.Context, tx sqliteExecutor, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM auth_rate_limits WHERE bucket_key IN (SELECT bucket_key FROM auth_rate_limits WHERE window_start<? ORDER BY window_start,bucket_key LIMIT ?)`, now.Add(-24*time.Hour).Unix(), authCleanupBatchSize)
	return err
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
