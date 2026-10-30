package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
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
