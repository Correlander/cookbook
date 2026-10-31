package main

import (
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

func startTrashCleanup(db *sql.DB, retentionDays int, mediaDir string) {
	cleanup := func() {
		if err := purgeExpiredTrash(db, retentionDays); err != nil {
			log.Printf("trash retention cleanup failed: %v", err)
		}
		if err := cleanupOrphanedMedia(db, mediaDir); err != nil {
			log.Printf("media cleanup failed: %v", err)
		}
	}
	cleanup()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cleanup()
		}
	}()
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
