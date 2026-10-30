package main

import (
	"context"
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
