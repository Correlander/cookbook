package main

import (
	"context"
	"database/sql"
	"log"
	"time"
)

const authCleanupBatchSize = 500

const authCleanupInterval = time.Hour

func purgeExpiredAuthState(db *sql.DB, now time.Time) error {
	return purgeExpiredAuthStateContext(context.Background(), db, now)
}

func purgeExpiredAuthStateContext(ctx context.Context, db *sql.DB, now time.Time) error {
	nowText := now.UTC().Format(time.RFC3339Nano)
	challengeCutoff := now.UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	queries := []struct {
		sql  string
		args []any
	}{
		{
			sql:  `DELETE FROM email_challenges WHERE id IN (SELECT id FROM email_challenges WHERE expires_at<? AND created_at<? ORDER BY created_at,id LIMIT ?)`,
			args: []any{nowText, challengeCutoff, authCleanupBatchSize},
		},
		{
			sql:  `DELETE FROM webauthn_ceremonies WHERE id IN (SELECT id FROM webauthn_ceremonies WHERE expires_at<? ORDER BY expires_at,id LIMIT ?)`,
			args: []any{nowText, authCleanupBatchSize},
		},
		{
			sql:  `DELETE FROM sessions WHERE token_hash IN (SELECT token_hash FROM sessions WHERE expires_at<? ORDER BY expires_at,token_hash LIMIT ?)`,
			args: []any{nowText, authCleanupBatchSize},
		},
		{
			sql:  `DELETE FROM federation_nonces WHERE nonce_hash IN (SELECT nonce_hash FROM federation_nonces WHERE expires_at<? ORDER BY expires_at,nonce_hash LIMIT ?)`,
			args: []any{now.Unix(), authCleanupBatchSize},
		},
	}
	for _, query := range queries {
		if _, err := db.ExecContext(ctx, query.sql, query.args...); err != nil {
			return err
		}
	}
	return withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
		return purgeStaleAuthRateLimitBatch(ctx, tx, now)
	})
}

func startAuthCleanup(ctx context.Context, db *sql.DB) <-chan struct{} {
	return startAuthCleanupAtInterval(ctx, db, authCleanupInterval)
}

func startAuthCleanupAtInterval(ctx context.Context, db *sql.DB, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	cleanup := func() {
		if err := purgeExpiredAuthStateContext(ctx, db, time.Now()); err != nil {
			log.Printf("authentication state cleanup failed")
		}
	}
	cleanup()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
	return done
}
