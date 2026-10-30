package main

import (
	"context"
	"testing"
)

func TestDatabaseCreatesPrivateInstanceAndConstraints(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var public, courses, federation int
	if err := db.QueryRowContext(context.Background(), `SELECT public_instance, courses_enabled, federation_enabled FROM instance_settings WHERE id = 1`).Scan(&public, &courses, &federation); err != nil {
		t.Fatal(err)
	}
	if public != 0 || courses != 0 || federation != 0 {
		t.Fatalf("unsafe defaults: public=%d courses=%d federation=%d", public, courses, federation)
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
