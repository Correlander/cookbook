package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func insertRemoteProjectionFixture(t *testing.T, db *sql.DB, id, body, updatedAt string) RemoteRecipeReadView {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	uri := testFederationOrigin + "/federation/recipes/" + id
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,1,'Remote creator',?,0,?)`, uri, testFederationOrigin, id, body, updatedAt); err != nil {
		t.Fatal(err)
	}
	return RemoteRecipeReadView{ObjectURI: uri, SourceOrigin: testFederationOrigin, SourceRecipeID: id, CreatorName: "Remote creator", Sequence: 1, UpdatedAt: updatedAt}
}

func writeRemoteProjectionFixture(t *testing.T, db *sql.DB, source RemoteRecipeReadView, body string) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRemoteRecipeSearchProjection(context.Background(), tx, source, body); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRemoteRecipeSearchProjectionUsesCanonicalDecoderAndSourceGuard(t *testing.T) {
	db := testDatabase(t)
	validBody := `{"title":" Tomato soup ","description":" Warm soup ","ingredients":[{"name":"tomatoes"}],"steps":[{"text":"Simmer"}],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[" Soup "]}`
	source := insertRemoteProjectionFixture(t, db, testFederationID, validBody, "2026-11-03T12:00:00Z")
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRemoteRecipeSearchProjection(context.Background(), tx, source, validBody); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version, available int
	var title, description, tags string
	if err := db.QueryRow(`SELECT projection_version,available,title,description,tags_json FROM federation_remote_recipe_search WHERE object_id=?`, source.ObjectURI).Scan(&version, &available, &title, &description, &tags); err != nil {
		t.Fatal(err)
	}
	if version != remoteRecipeSearchProjectionVersion || available != 1 || title != "Tomato soup" || description != "Warm soup" || tags != `["soup"]` {
		t.Fatalf("canonical projection=(%d,%d,%q,%q,%s)", version, available, title, description, tags)
	}

	stale := source
	stale.Sequence++
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRemoteRecipeSearchProjection(context.Background(), tx, stale, validBody); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT title FROM federation_remote_recipe_search WHERE object_id=?`, source.ObjectURI).Scan(&title); err != nil || title != "Tomato soup" {
		t.Fatalf("stale captured source replaced current projection: title=%q err=%v", title, err)
	}

	invalidID := "1123456789abcdef0123456789abcdef"
	invalidBody := `{"title":"Invalid","unknown":"field"}`
	invalidSource := insertRemoteProjectionFixture(t, db, invalidID, invalidBody, "2026-11-03T12:00:00Z")
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRemoteRecipeSearchProjection(context.Background(), tx, invalidSource, invalidBody); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT projection_version,available,title,description,tags_json FROM federation_remote_recipe_search WHERE object_id=?`, invalidSource.ObjectURI).Scan(&version, &available, &title, &description, &tags); err != nil {
		t.Fatal(err)
	}
	if version != remoteRecipeSearchProjectionVersion || available != 0 || title != "" || description != "" || tags != `[]` {
		t.Fatalf("invalid cache projection exposed fields: (%d,%d,%q,%q,%s)", version, available, title, description, tags)
	}

	newerID := "2123456789abcdef0123456789abcdef"
	newerSource := insertRemoteProjectionFixture(t, db, newerID, validBody, "2026-11-03T12:00:00Z")
	if _, err := db.Exec(`INSERT INTO federation_remote_recipe_search(object_id,projection_version,available,title,description,tags_json) VALUES(?,2,0,'','','[]')`, newerSource.ObjectURI); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRemoteRecipeSearchProjection(context.Background(), tx, newerSource, validBody); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT projection_version,available FROM federation_remote_recipe_search WHERE object_id=?`, newerSource.ObjectURI).Scan(&version, &available); err != nil || version != 2 || available != 0 {
		t.Fatalf("newer projection version was downgraded: version=%d available=%d err=%v", version, available, err)
	}
}

func TestLibraryRemoteSearchBackfillResumesAndBoundsLegacyRows(t *testing.T) {
	db := testDatabase(t)
	body := `{"title":"Legacy soup","description":"Herbs","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":["soup"]}`
	for index := 0; index < remoteRecipeSearchBackfillBatchSize*2+3; index++ {
		id := fmt.Sprintf("%032x", index+1)
		insertRemoteProjectionFixture(t, db, id, body, "2026-11-03T12:00:00Z")
	}
	processed, err := backfillLibraryRemoteSearchPass(context.Background(), db)
	if err != nil || !processed {
		t.Fatalf("first backfill pass processed=%v err=%v", processed, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search WHERE projection_version=? AND available=1`, remoteRecipeSearchProjectionVersion).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != remoteRecipeSearchBackfillBatchSize*2+3 {
		t.Fatalf("backfilled %d rows", count)
	}
	processed, err = backfillLibraryRemoteSearchPass(context.Background(), db)
	if err != nil || processed {
		t.Fatalf("second pass should find no work, processed=%v err=%v", processed, err)
	}
}

func TestLibraryRemoteSearchBackfillStopsOnCancellationAndResumes(t *testing.T) {
	db := testDatabase(t)
	body := `{"title":"Restart soup","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	source := insertRemoteProjectionFixture(t, db, testFederationID, body, "2026-11-03T12:00:00Z")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	<-startLibraryRemoteSearchBackfill(ctx, db)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled worker retained projection rows: count=%d err=%v", count, err)
	}
	<-startLibraryRemoteSearchBackfill(context.Background(), db)
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search WHERE object_id=? AND available=1`, source.ObjectURI).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart did not resume backfill: count=%d err=%v", count, err)
	}
}
