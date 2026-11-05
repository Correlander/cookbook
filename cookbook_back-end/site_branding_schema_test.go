package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSiteBrandingFreshDefaultsAndPaletteConstraint(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != 17 {
		t.Fatalf("fresh schema version=%d, want v17", got)
	}
	var siteName, palette string
	if err := db.QueryRow(`SELECT site_name,palette FROM instance_settings WHERE id=1`).Scan(&siteName, &palette); err != nil || siteName != defaultSiteName || palette != defaultPalette {
		t.Fatalf("fresh branding=(%q,%q) err=%v", siteName, palette, err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET palette='unknown' WHERE id=1`); err == nil {
		t.Fatal("database constraint accepted an unsupported palette")
	}
}

func TestSiteBrandingUpgradesV16AndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "site-branding-v16.db")
	db := rawMigrationDB(t, path)
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:16]); err != nil {
		t.Fatalf("create v16 database: %v", err)
	}
	if got := migrationVersion(t, db); got != 16 {
		t.Fatalf("pre-upgrade schema version=%d, want v16", got)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET registration_policy_configured=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("upgrade v16 database: %v", err)
	}
	var siteName, palette string
	var configured int
	if err := db.QueryRow(`SELECT site_name,palette,registration_policy_configured FROM instance_settings WHERE id=1`).Scan(&siteName, &palette, &configured); err != nil || siteName != defaultSiteName || palette != defaultPalette || configured != 0 {
		t.Fatalf("upgraded branding=(%q,%q) configured=%d err=%v", siteName, palette, configured, err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET site_name='Crème Kitchen',palette='plum' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("idempotent v17 restart: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := migrationVersion(t, reopened); got != 17 {
		t.Fatalf("reopened schema version=%d, want v17", got)
	}
	if err := reopened.QueryRow(`SELECT site_name,palette FROM instance_settings WHERE id=1`).Scan(&siteName, &palette); err != nil || siteName != "Crème Kitchen" || palette != "plum" {
		t.Fatalf("reopened branding=(%q,%q) err=%v", siteName, palette, err)
	}
}
