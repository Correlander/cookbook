package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFederationRemoteSearchProjectionTracksReplayUpdateTombstoneAndRollback(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	objectID := testFederationID
	objectURI := testFederationOrigin + "/federation/recipes/" + objectID
	makeEvent := func(eventID, eventType string, sequence int64, recipeID, title string, deleted bool) []byte {
		t.Helper()
		source := federationRecipeSource{ID: recipeID, CreatorName: "Projection author", Title: title, Visibility: visibilityPublic, Deleted: deleted}
		payload, err := serializeFederationEvent(testFederationOrigin, eventID, eventType, sequence, time.Now(), true, true, source)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	apply := func(payload []byte) error {
		t.Helper()
		_, _, err := acceptFederationEvent(context.Background(), db, testFederationOrigin, payload)
		return err
	}
	assertProjection := func(wantTitle string) {
		t.Helper()
		var version, available int
		var title string
		if err := db.QueryRow(`SELECT projection_version,available,title FROM federation_remote_recipe_search WHERE object_id=?`, objectURI).Scan(&version, &available, &title); err != nil {
			t.Fatal(err)
		}
		if version != remoteRecipeSearchProjectionVersion || available != 1 || title != wantTitle {
			t.Fatalf("projection version=%d available=%d title=%q, want %q", version, available, title, wantTitle)
		}
	}

	snapshot := makeEvent("11111111111111111111111111111111", federationEventSnapshot, 1, objectID, "First title", false)
	if err := apply(snapshot); err != nil {
		t.Fatal(err)
	}
	assertProjection("First title")
	if err := apply(snapshot); err != nil {
		t.Fatalf("replay returned error: %v", err)
	}
	assertProjection("First title")

	if _, err := db.Exec(`CREATE TRIGGER reject_projection_update BEFORE INSERT ON federation_remote_recipe_search
		WHEN NEW.title='Rejected update' BEGIN SELECT RAISE(ABORT,'projection rejected'); END`); err != nil {
		t.Fatal(err)
	}
	failedUpdate := makeEvent("22222222222222222222222222222222", federationEventUpdate, 2, objectID, "Rejected update", false)
	if err := apply(failedUpdate); err == nil {
		t.Fatal("projection write failure did not abort federation update")
	}
	assertProjection("First title")
	var sequence int64
	var rawBody string
	if err := db.QueryRow(`SELECT sequence,recipe_json FROM federation_remote_recipes WHERE object_id=?`, objectURI).Scan(&sequence, &rawBody); err != nil || sequence != 1 || !strings.Contains(rawBody, "First title") {
		t.Fatalf("failed update escaped rollback: seq=%d body=%s err=%v", sequence, rawBody, err)
	}
	var received int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events WHERE object_id=?`, objectURI).Scan(&received); err != nil || received != 1 {
		t.Fatalf("failed update receipt count=%d err=%v", received, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_projection_update`); err != nil {
		t.Fatal(err)
	}

	update := makeEvent("33333333333333333333333333333333", federationEventUpdate, 2, objectID, "Second title", false)
	if err := apply(update); err != nil {
		t.Fatal(err)
	}
	assertProjection("Second title")
	tombstone := makeEvent("44444444444444444444444444444444", federationEventTombstone, 3, objectID, "", true)
	if err := apply(tombstone); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search WHERE object_id=?`, objectURI).Scan(&received); err != nil || received != 0 {
		t.Fatalf("tombstone retained projection: count=%d err=%v", received, err)
	}

	rollbackID := "5123456789abcdef0123456789abcdef"
	rollbackURI := testFederationOrigin + "/federation/recipes/" + rollbackID
	if _, err := db.Exec(`CREATE TRIGGER reject_projection_snapshot BEFORE INSERT ON federation_remote_recipe_search
		WHEN NEW.title='Rolled back' BEGIN SELECT RAISE(ABORT,'projection rejected'); END`); err != nil {
		t.Fatal(err)
	}
	failedSnapshot := makeEvent("55555555555555555555555555555555", federationEventSnapshot, 1, rollbackID, "Rolled back", false)
	if err := apply(failedSnapshot); err == nil {
		t.Fatal("projection write failure did not abort snapshot")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipes WHERE object_id=?`, rollbackURI).Scan(&received); err != nil || received != 0 {
		t.Fatalf("failed snapshot retained raw cache: count=%d err=%v", received, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events WHERE object_id=?`, rollbackURI).Scan(&received); err != nil || received != 0 {
		t.Fatalf("failed snapshot retained received-event ledger: count=%d err=%v", received, err)
	}
}
