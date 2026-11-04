package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestAuthRateLimitCleanupCutoffAndLifecycle(t *testing.T) {
	db := testDatabase(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES('boundary',?,1),('stale',?,1)`, cutoff, cutoff-1); err != nil {
		t.Fatal(err)
	}
	if err := purgeStaleAuthRateLimitBatch(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cutoff rows=%d err=%v; exact boundary must remain", count, err)
	}

	if _, err := db.Exec(`UPDATE auth_rate_limits SET window_start=? WHERE bucket_key='boundary'`, cutoff-1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := startAuthCleanupAtInterval(ctx, db, 10*time.Millisecond)
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("startup cleanup rows=%d err=%v; want stale row removed synchronously", count, err)
	}
	if _, err := db.Exec(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES('periodic-stale',?,1)`, cutoff-1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if count != 0 {
		t.Fatal("startup auth cleanup did not remove stale bucket")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("auth cleanup lifecycle did not stop after cancellation")
	}
}

func TestRevisionRetentionKeepsCurrentSnapshotAndDeletesHistoryInBatches(t *testing.T) {
	db := testDatabase(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -400).Format(time.RFC3339Nano)
	bulkOld := now.AddDate(0, 0, -401).Format(time.RFC3339Nano)
	recent := now.AddDate(0, 0, -10).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,visibility,edit_policy,version,created_at,updated_at) VALUES
		('multi','owner','Owner','Multi','private','owner_only',3,'now','now'),
		('single','owner','Owner','Single','private','owner_only',1,'now','now'),
		('bulk','owner','Owner','Bulk','private','owner_only',502,'now','now')`); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []struct {
		id, recipeID string
		version      int
		created      string
	}{
		{"multi-1", "multi", 1, old},
		{"multi-2", "multi", 2, now.AddDate(0, 0, -200).Format(time.RFC3339Nano)},
		{"multi-3", "multi", 3, recent},
		{"single-1", "single", 1, old},
	} {
		if err := insertTestRevision(db, revision.id, revision.recipeID, revision.version, revision.created); err != nil {
			t.Fatal(err)
		}
	}
	for version := 1; version <= 501; version++ {
		if err := insertTestRevision(db, fmt.Sprintf("bulk-%03d", version), "bulk", version, bulkOld); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertTestRevision(db, "bulk-current", "bulk", 502, recent); err != nil {
		t.Fatal(err)
	}

	if err := purgeExpiredRevisions(db, 365, now); err != nil {
		t.Fatal(err)
	}
	var multiCount, singleCount, bulkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='multi'`).Scan(&multiCount); err != nil || multiCount != 3 {
		t.Fatalf("first bounded cleanup changed a later batch unexpectedly: %d, %v", multiCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='single'`).Scan(&singleCount); err != nil || singleCount != 1 {
		t.Fatalf("sole current revision was removed: %d, %v", singleCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='bulk'`).Scan(&bulkCount); err != nil || bulkCount != 2 {
		t.Fatalf("first bounded cleanup removed wrong number of revisions: %d, %v", bulkCount, err)
	}
	if err := purgeExpiredRevisions(db, 365, now); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='multi'`).Scan(&multiCount); err != nil || multiCount != 2 {
		t.Fatalf("second cleanup did not retain current and in-range revisions: %d, %v", multiCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='bulk'`).Scan(&bulkCount); err != nil || bulkCount != 1 {
		t.Fatalf("second bounded cleanup did not finish the backlog: %d, %v", bulkCount, err)
	}
	if err := purgeExpiredRevisions(db, 0, now); err != nil {
		t.Fatal(err)
	}
}

func TestAuditRetentionIsSeparateAndBounded(t *testing.T) {
	db := testDatabase(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -400).Format(time.RFC3339Nano)
	recent := now.AddDate(0, 0, -10).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO audit_events(id,action,resource_type,response_status,created_at) VALUES('recent','POST /api/recipes','recipes',200,?)`, recent); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= 500; index++ {
		if _, err := db.Exec(`INSERT INTO audit_events(id,action,resource_type,response_status,created_at) VALUES(?, 'POST /api/recipes','recipes',200,?)`, fmt.Sprintf("old-%03d", index), old); err != nil {
			t.Fatal(err)
		}
	}
	if err := purgeExpiredAuditEvents(db, 365, now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("first bounded audit cleanup = %d, %v", count, err)
	}
	if err := purgeExpiredAuditEvents(db, 365, now); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("second audit cleanup did not finish the backlog: count=%d err=%v", count, err)
	}
	if err := purgeExpiredAuditEvents(db, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unlimited audit retention removed events: count=%d err=%v", count, err)
	}
}

func TestExpiredAuthenticationStateIsPurgedInBoundedBatches(t *testing.T) {
	db := testDatabase(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	recent := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	future := now.Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner',?)`, old); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < authCleanupBatchSize+1; index++ {
		if _, err := db.Exec(`INSERT INTO email_challenges(id,email,token_hash,expires_at,created_at) VALUES(?,?,?,?,?)`, fmt.Sprintf("expired-%03d", index), "owner@example.test", fmt.Sprintf("hash-%03d", index), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO email_challenges(id,email,token_hash,expires_at,created_at) VALUES('recent','owner@example.test','recent-hash',?,?)`, old, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO email_challenges(id,email,token_hash,expires_at,created_at) VALUES('valid','owner@example.test','valid-hash',?,?)`, future, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO webauthn_ceremonies(id,purpose,session_data,expires_at,created_at) VALUES('expired','login','{}',?,?),('valid','login','{}',?,?)`, old, old, future, recent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,token_hash,user_id,expires_at,created_at) VALUES('expired-id','expired','owner',?,?),('valid-id','valid','owner',?,?)`, old, old, future, recent); err != nil {
		t.Fatal(err)
	}
	oldWindow := now.Add(-48 * time.Hour).Unix()
	currentWindow := now.Truncate(time.Hour).Unix()
	for index := 0; index < authCleanupBatchSize+1; index++ {
		if _, err := db.Exec(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES(?,?,1)`, fmt.Sprintf("old-bucket-%03d", index), oldWindow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO auth_rate_limits(bucket_key,window_start,request_count) VALUES('current-bucket',?,1)`, currentWindow); err != nil {
		t.Fatal(err)
	}

	if err := purgeExpiredAuthState(db, now); err != nil {
		t.Fatal(err)
	}
	var challengeCount, ceremonyCount, sessionCount, rateLimitCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM email_challenges`).Scan(&challengeCount); err != nil || challengeCount != 3 {
		t.Fatalf("first batch challenge count=%d err=%v; want one old and two recent rows", challengeCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_ceremonies`).Scan(&ceremonyCount); err != nil || ceremonyCount != 1 {
		t.Fatalf("ceremony count=%d err=%v; want one unexpired row", ceremonyCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount); err != nil || sessionCount != 1 {
		t.Fatalf("session count=%d err=%v; want one unexpired row", sessionCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&rateLimitCount); err != nil || rateLimitCount != 2 {
		t.Fatalf("first batch rate-limit count=%d err=%v; want one stale and one current bucket", rateLimitCount, err)
	}
	if err := purgeExpiredAuthState(db, now); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM email_challenges`).Scan(&challengeCount); err != nil || challengeCount != 2 {
		t.Fatalf("second batch challenge count=%d err=%v; recent-hour rows must remain for quota checks", challengeCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&rateLimitCount); err != nil || rateLimitCount != 1 {
		t.Fatalf("second batch rate-limit count=%d err=%v; current bucket must remain", rateLimitCount, err)
	}
}

func TestExpiredFederationNoncesArePurgedInBoundedBatches(t *testing.T) {
	db := testDatabase(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for index := 0; index < authCleanupBatchSize+1; index++ {
		hash := fmt.Sprintf("expired-nonce-%064d", index)
		if _, err := db.Exec(`INSERT INTO federation_nonces(nonce_hash,expires_at,source_origin,logical_bytes) VALUES(?,?,?,?)`, hash, now.Add(-time.Second).Unix(), "https://peer.example.org", federationNonceLogicalBytes("https://peer.example.org")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO federation_nonces(nonce_hash,expires_at,source_origin,logical_bytes) VALUES('valid-nonce',?,?,?)`, now.Unix(), "https://peer.example.org", federationNonceLogicalBytes("https://peer.example.org")); err != nil {
		t.Fatal(err)
	}
	if err := purgeExpiredAuthState(db, now); err != nil {
		t.Fatal(err)
	}
	var expiredRows, validRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_nonces WHERE nonce_hash LIKE 'expired-nonce-%'`).Scan(&expiredRows); err != nil || expiredRows != 1 {
		t.Fatalf("first nonce cleanup batch left=%d err=%v; want one expired row", expiredRows, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_nonces WHERE nonce_hash='valid-nonce'`).Scan(&validRows); err != nil || validRows != 1 {
		t.Fatalf("nonce cleanup removed unexpired row count=%d err=%v", validRows, err)
	}
	if err := purgeExpiredAuthState(db, now); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_nonces WHERE nonce_hash LIKE 'expired-nonce-%'`).Scan(&expiredRows); err != nil || expiredRows != 0 {
		t.Fatalf("second nonce cleanup batch left=%d err=%v", expiredRows, err)
	}
}

func insertTestRevision(db *sql.DB, id, recipeID string, version int, createdAt string) error {
	_, err := db.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at) VALUES(?,?,?,'owner','Test','','[]','[]','private',?)`, id, recipeID, version, createdAt)
	return err
}
