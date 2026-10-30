package main

import (
	"database/sql"
	"log"
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

func startTrashCleanup(db *sql.DB, retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	cleanup := func() {
		if err := purgeExpiredTrash(db, retentionDays); err != nil {
			log.Printf("trash retention cleanup failed: %v", err)
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
