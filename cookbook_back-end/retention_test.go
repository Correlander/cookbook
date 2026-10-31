package main

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

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

func insertTestRevision(db *sql.DB, id, recipeID string, version int, createdAt string) error {
	_, err := db.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at) VALUES(?,?,?,'owner','Test','','[]','[]','private',?)`, id, recipeID, version, createdAt)
	return err
}
