package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestDatabaseCreatesPrivateInstanceAndConstraints(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var public, courses, federation int
	var registration string
	if err := db.QueryRowContext(context.Background(), `SELECT public_instance, courses_enabled, federation_enabled, registration_policy FROM instance_settings WHERE id = 1`).Scan(&public, &courses, &federation, &registration); err != nil {
		t.Fatal(err)
	}
	if public != 0 || courses != 0 || federation != 0 {
		t.Fatalf("unsafe defaults: public=%d courses=%d federation=%d", public, courses, federation)
	}
	if registration != "invite_only" {
		t.Fatalf("registration default = %q, want invite_only", registration)
	}
	if _, err := db.Exec(`INSERT INTO cookbooks (id, owner_id, title, visibility, created_at, updated_at) VALUES ('book', 'missing-user', 'title', 'private', 'now', 'now')`); err == nil {
		t.Fatal("expected foreign key constraint to reject unknown owner")
	}
}

func TestExistingInstanceSettingsReceiveRetentionDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings-upgrade.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE instance_settings(id INTEGER PRIMARY KEY CHECK(id=1),public_instance INTEGER NOT NULL DEFAULT 0,registration_policy TEXT NOT NULL DEFAULT 'invite_only'); INSERT INTO instance_settings(id,public_instance,registration_policy) VALUES(1,1,'open')`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var public int
	var registration string
	var revisionRetention, auditRetention int
	if err := db.QueryRow(`SELECT public_instance,registration_policy,revision_retention_days,audit_retention_days FROM instance_settings WHERE id=1`).Scan(&public, &registration, &revisionRetention, &auditRetention); err != nil {
		t.Fatal(err)
	}
	if public != 1 || registration != "open" || revisionRetention != 365 || auditRetention != 365 {
		t.Fatalf("upgraded settings = public %d, registration %q, revisions %d, audit %d", public, registration, revisionRetention, auditRetention)
	}
}

func TestExistingPasskeyCredentialsReceiveDefaultName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passkey-upgrade.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE webauthn_credentials(credential_id BLOB PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),credential_json TEXT NOT NULL,created_at TEXT NOT NULL,last_used_at TEXT); INSERT INTO users VALUES('owner','owner@example.test','Owner','now'); INSERT INTO webauthn_credentials VALUES(X'0102','owner','{}','now',NULL);`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM webauthn_credentials WHERE credential_id=X'0102'`).Scan(&name); err != nil || name != "Passkey" {
		t.Fatalf("migrated passkey name = %q, err=%v", name, err)
	}
}

func TestExistingSessionsReceiveStableIdentifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-upgrade.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE sessions(token_hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),expires_at TEXT NOT NULL,created_at TEXT NOT NULL); INSERT INTO users VALUES('owner','owner@example.test','Owner','now'); INSERT INTO sessions VALUES('existing-token-hash','owner','2099-01-01T00:00:00Z','2026-10-31T00:00:00Z');`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var firstID string
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash='existing-token-hash'`).Scan(&firstID); err != nil || firstID == "" {
		t.Fatalf("migrated session identifier=%q err=%v", firstID, err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('second','second@example.test','Second','now')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := issueSession(db, "second"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var reopenedID string
	if err := db.QueryRow(`SELECT id FROM sessions WHERE token_hash='existing-token-hash'`).Scan(&reopenedID); err != nil || reopenedID != firstID {
		t.Fatalf("session id changed across restart: first=%q reopened=%q err=%v", firstID, reopenedID, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT id) FROM sessions`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unique session identifiers=%d err=%v", count, err)
	}
}

func TestAccessPolicyDefaults(t *testing.T) {
	publicBook := access{instancePublic: true, visibility: visibilityPublic, ownerID: "owner"}
	privateBook := access{instancePublic: true, visibility: visibilityPrivate, ownerID: "owner"}
	if !canReadResource("", publicBook) || canReadResource("", privateBook) {
		t.Fatal("anonymous visibility policy is incorrect")
	}
	if canEditResource("", publicBook) || !canEditResource("editor", access{ownerID: "owner", memberRole: roleEditor}) {
		t.Fatal("edit authorization policy is incorrect")
	}
	if canReadResource("", access{instancePublic: true, visibility: visibilityPublic}) == false {
		t.Fatal("public anonymous read should be allowed when explicitly published")
	}
}

func TestSchemaBackfillsCurrentRecipeRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('legacy-owner','legacy@example.test','Legacy','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,version,created_at,updated_at) VALUES('legacy-recipe','legacy-owner','Legacy','Old title','','[]','[]','private','owner_only',4,'old','updated')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE recipe_revisions`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var title string
	if err := db.QueryRow(`SELECT version,title FROM recipe_revisions WHERE recipe_id='legacy-recipe'`).Scan(&version, &title); err != nil {
		t.Fatal(err)
	}
	if version != 4 || title != "Old title" {
		t.Fatalf("backfilled revision = version %d, title %q", version, title)
	}
}

func TestRecipeMetadataColumnsMigrateWithDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recipe-metadata-upgrade.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE recipes(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL REFERENCES users(id),creator_name TEXT NOT NULL,title TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',ingredients_json TEXT NOT NULL DEFAULT '[]',steps_json TEXT NOT NULL DEFAULT '[]',visibility TEXT NOT NULL DEFAULT 'private',edit_policy TEXT NOT NULL DEFAULT 'owner_only',version INTEGER NOT NULL DEFAULT 1,created_at TEXT NOT NULL,updated_at TEXT NOT NULL); CREATE TABLE recipe_revisions(id TEXT PRIMARY KEY,recipe_id TEXT NOT NULL REFERENCES recipes(id),version INTEGER NOT NULL,editor_id TEXT NOT NULL REFERENCES users(id),title TEXT NOT NULL,description TEXT NOT NULL,ingredients_json TEXT NOT NULL,steps_json TEXT NOT NULL,visibility TEXT NOT NULL,created_at TEXT NOT NULL,UNIQUE(recipe_id,version)); INSERT INTO users VALUES('owner','owner@example.test','Owner','now'); INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,version,created_at,updated_at) VALUES('recipe','owner','Owner','Legacy recipe','','[]','[]','private','owner_only',1,'now','now');`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var servings, prepTime, cookTime int
	var tags string
	if err := db.QueryRow(`SELECT servings,prep_time_minutes,cook_time_minutes,tags_json FROM recipes WHERE id='recipe'`).Scan(&servings, &prepTime, &cookTime, &tags); err != nil || servings != 0 || prepTime != 0 || cookTime != 0 || tags != "[]" {
		t.Fatalf("migrated recipe metadata=%d/%d/%d %q err=%v", servings, prepTime, cookTime, tags, err)
	}
	if err := db.QueryRow(`SELECT servings,prep_time_minutes,cook_time_minutes,tags_json FROM recipe_revisions WHERE recipe_id='recipe'`).Scan(&servings, &prepTime, &cookTime, &tags); err != nil || servings != 0 || prepTime != 0 || cookTime != 0 || tags != "[]" {
		t.Fatalf("migrated revision metadata=%d/%d/%d %q err=%v", servings, prepTime, cookTime, tags, err)
	}
	var deletedAtColumn int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('recipes') WHERE name='deleted_at'`).Scan(&deletedAtColumn); err != nil || deletedAtColumn != 1 {
		t.Fatalf("deleted_at migration count=%d err=%v", deletedAtColumn, err)
	}
}

func TestTrashRetentionPurgesExpiredRecipesAndCanBeDisabled(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'); INSERT INTO recipes(id,owner_id,creator_name,title,deleted_at,created_at,updated_at) VALUES('expired','owner','Owner','Expired',?,'now','now'),('recent','owner','Owner','Recent',?,'now','now'),('active','owner','Owner','Active',NULL,'now','now')`, time.Now().UTC().AddDate(0, 0, -60).Format(time.RFC3339Nano), time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES('book','owner','Collection','now','now'); INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','expired','owner','now'); INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at) VALUES('revision','expired',1,'owner','Expired','','[]','[]','private','now')`); err != nil {
		t.Fatal(err)
	}
	if err := purgeExpiredTrash(db, 30); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("recipes after retention cleanup=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='expired')+(SELECT COUNT(*) FROM cookbook_recipes WHERE recipe_id='expired')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retention left expired recipe references count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,deleted_at,created_at,updated_at) VALUES('retained','owner','Owner','Retained',?,'now','now')`, time.Now().UTC().AddDate(0, 0, -100).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := purgeExpiredTrash(db, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE id='retained'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("disabled retention removed recipe count=%d err=%v", count, err)
	}
}

func TestExistingAccountsAndChallengesReceiveAdminInvitationColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-auth.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL); CREATE TABLE email_challenges(id TEXT PRIMARY KEY,email TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,expires_at TEXT NOT NULL,consumed_at TEXT,attempts INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL); CREATE TABLE invitations(id TEXT PRIMARY KEY,token_hash TEXT NOT NULL UNIQUE,email TEXT NOT NULL,invited_by TEXT NOT NULL REFERENCES users(id),expires_at TEXT NOT NULL,accepted_at TEXT,revoked_at TEXT,created_at TEXT NOT NULL); INSERT INTO users VALUES('existing','existing@example.test','Existing','now'); INSERT INTO invitations(id,token_hash,email,invited_by,expires_at,created_at) VALUES('old','oldhash','invite@example.test','existing','2099-01-01T00:00:00Z','2025-01-01T00:00:00Z'),('new','newhash','invite@example.test','existing','2099-01-01T00:00:00Z','2025-01-02T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, column := range []struct{ table, name string }{{"users", "is_admin"}, {"email_challenges", "display_name"}, {"email_challenges", "invitation_id"}, {"invitations", "cookbook_id"}, {"invitations", "role"}} {
		var found int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, column.table, column.name).Scan(&found); err != nil || found != 1 {
			t.Fatalf("missing %s.%s: found=%d err=%v", column.table, column.name, found, err)
		}
	}
	var admin int
	if err := db.QueryRow(`SELECT is_admin FROM users WHERE id='existing'`).Scan(&admin); err != nil || admin != 0 {
		t.Fatalf("existing account admin=%d err=%v", admin, err)
	}
	var active, revoked int
	if err := db.QueryRow(`SELECT SUM(CASE WHEN revoked_at IS NULL THEN 1 ELSE 0 END),SUM(CASE WHEN revoked_at IS NOT NULL THEN 1 ELSE 0 END) FROM invitations WHERE email='invite@example.test'`).Scan(&active, &revoked); err != nil || active != 1 || revoked != 1 {
		t.Fatalf("duplicate invitation migration: active=%d revoked=%d err=%v", active, revoked, err)
	}
}
