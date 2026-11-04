package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"time"
)

func purgeExpiredTrash(db *sql.DB, retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	_, err := db.Exec(`DELETE FROM recipes WHERE deleted_at IS NOT NULL AND deleted_at<?`, cutoff)
	return err
}

func purgeExpiredRevisions(db *sql.DB, retentionDays int, now time.Time) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := now.UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	_, err := db.Exec(`DELETE FROM recipe_revisions WHERE id IN (
		SELECT old.id FROM recipe_revisions old
		WHERE old.created_at<? AND old.version<(SELECT MAX(latest.version) FROM recipe_revisions latest WHERE latest.recipe_id=old.recipe_id)
		ORDER BY old.created_at,old.id LIMIT 500
	)`, cutoff)
	return err
}

func purgeExpiredAuditEvents(db *sql.DB, retentionDays int, now time.Time) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := now.UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
	_, err := db.Exec(`DELETE FROM audit_events WHERE id IN (
		SELECT id FROM audit_events WHERE created_at<? ORDER BY created_at,id LIMIT 500
	)`, cutoff)
	return err
}

func purgeExpiredHistory(db *sql.DB) error {
	var revisionRetention, auditRetention int
	if err := db.QueryRow(`SELECT revision_retention_days,audit_retention_days FROM instance_settings WHERE id=1`).Scan(&revisionRetention, &auditRetention); err != nil {
		return err
	}
	now := time.Now()
	if err := purgeExpiredRevisions(db, revisionRetention, now); err != nil {
		return err
	}
	return purgeExpiredAuditEvents(db, auditRetention, now)
}

func startTrashCleanup(ctx context.Context, db *sql.DB, retentionDays int, mediaDir string) <-chan struct{} {
	done := make(chan struct{})
	cleanup := func() {
		if err := purgeExpiredTrash(db, retentionDays); err != nil {
			log.Printf("trash retention cleanup failed: %v", err)
		}
		if err := purgeExpiredHistory(db); err != nil {
			log.Printf("history retention cleanup failed: %v", err)
		}
		if err := cleanupOrphanedMedia(db, mediaDir); err != nil {
			log.Printf("media cleanup failed: %v", err)
		}
	}
	cleanup()
	go func() {
		defer close(done)
		ticker := time.NewTicker(24 * time.Hour)
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

func cleanupOrphanedMedia(db *sql.DB, mediaDir string) error {
	if mediaDir == "" {
		return nil
	}
	entries, err := os.ReadDir(mediaDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !validStoredImageName(name) {
			continue
		}
		var referenced int
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM recipes WHERE image_path=?) OR EXISTS(SELECT 1 FROM recipe_revisions WHERE image_path=?)`, name, name).Scan(&referenced); err != nil {
			return err
		}
		if referenced == 0 {
			if err := os.Remove(filepath.Join(mediaDir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
