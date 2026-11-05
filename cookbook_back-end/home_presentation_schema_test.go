package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAnonymousHomePresetFreshMigrationDefaultAndConstraint(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != 17 {
		t.Fatalf("fresh schema version=%d, want v17", got)
	}
	var preset string
	if err := db.QueryRow(`SELECT anonymous_home_preset FROM instance_settings WHERE id=1`).Scan(&preset); err != nil || preset != defaultAnonymousHomePreset {
		t.Fatalf("fresh homepage preset=%q err=%v, want %q", preset, err, defaultAnonymousHomePreset)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET anonymous_home_preset='publication_first' WHERE id=1`); err == nil {
		t.Fatal("database constraint accepted unsupported anonymous homepage preset")
	}
	if err := db.QueryRow(`SELECT anonymous_home_preset FROM instance_settings WHERE id=1`).Scan(&preset); err != nil || preset != defaultAnonymousHomePreset {
		t.Fatalf("rejected preset changed stored value to %q, err=%v", preset, err)
	}
}

func TestAnonymousHomePresetUpgradesV15AndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anonymous-home-preset-v15.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:15]); err != nil {
		t.Fatalf("create real v15 database: %v", err)
	}
	if got := migrationVersion(t, db); got != 15 {
		t.Fatalf("pre-upgrade schema version=%d, want historical v15", got)
	}
	const ownerID = "33333333333333333333333333333333"
	const recipeID = "44444444444444444444444444444444"
	const menuID = "55555555555555555555555555555555"
	const entryID = "66666666666666666666666666666666"
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES(?, 'preset@example.test','Preset Owner',0,'user-created')`, ownerID); err != nil {
		t.Fatalf("seed v15 user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,visibility,edit_policy,created_at,updated_at) VALUES(?,?,'Owner','Private recipe','private','owner_only','recipe-created','recipe-updated')`, recipeID, ownerID); err != nil {
		t.Fatalf("seed v15 recipe: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meal_menus(id,owner_id,title,note,created_at,updated_at) VALUES(?,?,'Existing menu','Keep this note','menu-created','menu-updated')`, menuID, ownerID); err != nil {
		t.Fatalf("seed v15 menu: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meal_menu_entries(id,menu_id,position,label,note,serving_note) VALUES(?,?,0,'Existing slot','Keep entry note','Serve later')`, entryID, menuID); err != nil {
		t.Fatalf("seed v15 menu entry: %v", err)
	}
	if _, err := db.Exec(`UPDATE instance_authority SET owner_user_id=?,owner_claimed_at='owner-claimed',generation=1 WHERE singleton_id=1`, ownerID); err != nil {
		t.Fatalf("seed v15 owner state: %v", err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0,courses_enabled=1,registration_policy='disabled',revision_retention_days=90,audit_retention_days=30,federation_enabled=1,federation_origin='https://instance.example.test',federation_origin_locked=1,private_snapshot_storage_limit_bytes=4096 WHERE id=1`); err != nil {
		t.Fatalf("seed v15 instance settings: %v", err)
	}

	assertPreservedState := func(current *sql.DB, wantPreset string) {
		t.Helper()
		var preset, policy, origin string
		var public, courses, registrationConfigured, federation, revisionRetention, auditRetention, originLocked int
		var snapshotLimit int64
		if err := current.QueryRow(`SELECT public_instance,courses_enabled,registration_policy,registration_policy_configured,revision_retention_days,audit_retention_days,federation_enabled,federation_origin,federation_origin_locked,private_snapshot_storage_limit_bytes,anonymous_home_preset FROM instance_settings WHERE id=1`).Scan(&public, &courses, &policy, &registrationConfigured, &revisionRetention, &auditRetention, &federation, &origin, &originLocked, &snapshotLimit, &preset); err != nil {
			t.Fatal(err)
		}
		if public != 0 || courses != 1 || policy != "disabled" || registrationConfigured != 0 || revisionRetention != 90 || auditRetention != 30 || federation != 1 || origin != "https://instance.example.test" || originLocked != 1 || snapshotLimit != 4096 || preset != wantPreset {
			t.Fatalf("instance settings changed or preset mismatch: public=%d courses=%d policy=%q policy_configured=%d revision=%d audit=%d federation=%d origin=%q locked=%d snapshot=%d preset=%q", public, courses, policy, registrationConfigured, revisionRetention, auditRetention, federation, origin, originLocked, snapshotLimit, preset)
		}
		var owner, ownerClaimed string
		var generation int
		if err := current.QueryRow(`SELECT owner_user_id,owner_claimed_at,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &ownerClaimed, &generation); err != nil || owner != ownerID || ownerClaimed != "owner-claimed" || generation != 1 {
			t.Fatalf("instance owner state changed: owner=%q claimed=%q generation=%d err=%v", owner, ownerClaimed, generation, err)
		}
		var userEmail, recipeTitle, menuTitle, menuNote, entryLabel, entryNote, servingNote string
		if err := current.QueryRow(`SELECT email FROM users WHERE id=?`, ownerID).Scan(&userEmail); err != nil || userEmail != "preset@example.test" {
			t.Fatalf("existing user changed: email=%q err=%v", userEmail, err)
		}
		if err := current.QueryRow(`SELECT title FROM recipes WHERE id=?`, recipeID).Scan(&recipeTitle); err != nil || recipeTitle != "Private recipe" {
			t.Fatalf("existing recipe changed: title=%q err=%v", recipeTitle, err)
		}
		if err := current.QueryRow(`SELECT title,note FROM meal_menus WHERE id=? AND owner_id=?`, menuID, ownerID).Scan(&menuTitle, &menuNote); err != nil || menuTitle != "Existing menu" || menuNote != "Keep this note" {
			t.Fatalf("existing menu changed: title=%q note=%q err=%v", menuTitle, menuNote, err)
		}
		if err := current.QueryRow(`SELECT label,note,serving_note FROM meal_menu_entries WHERE id=? AND menu_id=?`, entryID, menuID).Scan(&entryLabel, &entryNote, &servingNote); err != nil || entryLabel != "Existing slot" || entryNote != "Keep entry note" || servingNote != "Serve later" {
			t.Fatalf("existing menu entry changed: label=%q note=%q serving=%q err=%v", entryLabel, entryNote, servingNote, err)
		}
	}

	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("upgrade v15 database: %v", err)
	}
	if got := migrationVersion(t, db); got != 17 {
		t.Fatalf("upgraded schema version=%d, want v17", got)
	}
	assertPreservedState(db, defaultAnonymousHomePreset)
	if _, err := db.Exec(`UPDATE instance_settings SET anonymous_home_preset=? WHERE id=1`, anonymousHomePresetDiscoveryFirst); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("idempotent v17 restart: %v", err)
	}
	assertPreservedState(db, anonymousHomePresetDiscoveryFirst)
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
	assertPreservedState(reopened, anonymousHomePresetDiscoveryFirst)
}
