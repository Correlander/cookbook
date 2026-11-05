package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func insertCompletedPublisherProfile(t *testing.T, db *sql.DB, accountID, publicName string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,bio,visibility,created_at,updated_at)
		VALUES(?,?,?,'','private','now','now')
		ON CONFLICT(user_id) DO UPDATE SET public_name=excluded.public_name,visibility='private',updated_at=excluded.updated_at`, newID(), accountID, publicName); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedPublisherProfileUsesNamedAccountAndAllowsPrivateVisibility(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('owner','owner@example.test','Owner','now'),('other','other@example.test','Other','now')`); err != nil {
		t.Fatal(err)
	}
	insertCompletedPublisherProfile(t, db, "owner", "Private publisher")
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := requireCompletedPublisherProfile(context.Background(), tx, "owner"); err != nil {
		t.Fatalf("completed private profile rejected: %v", err)
	}
	for _, accountID := range []string{"other", ""} {
		if err := requireCompletedPublisherProfile(context.Background(), tx, accountID); !errors.Is(err, errPublisherProfileRequired) {
			t.Fatalf("account %q error=%v, want publisher profile required", accountID, err)
		}
	}
}

func TestBlankPublisherProfileDoesNotQualify(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'owner','','private','now','now')`, newID()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := requireCompletedPublisherProfile(context.Background(), tx, "owner"); !errors.Is(err, errPublisherProfileRequired) {
		t.Fatalf("blank profile error=%v, want publisher profile required", err)
	}
}
