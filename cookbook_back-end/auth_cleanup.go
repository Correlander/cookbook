package main

import (
	"database/sql"
	"time"
)

const authCleanupBatchSize = 500

func purgeExpiredAuthState(db *sql.DB, now time.Time) error {
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
			sql:  `DELETE FROM auth_rate_limits WHERE bucket_key IN (SELECT bucket_key FROM auth_rate_limits WHERE window_start<? ORDER BY window_start,bucket_key LIMIT ?)`,
			args: []any{now.Add(-24 * time.Hour).Unix(), authCleanupBatchSize},
		},
		{
			sql:  `DELETE FROM federation_nonces WHERE nonce_hash IN (SELECT nonce_hash FROM federation_nonces WHERE expires_at<? ORDER BY expires_at,nonce_hash LIMIT ?)`,
			args: []any{now.Unix(), authCleanupBatchSize},
		},
	}
	for _, query := range queries {
		if _, err := db.Exec(query.sql, query.args...); err != nil {
			return err
		}
	}
	return nil
}
