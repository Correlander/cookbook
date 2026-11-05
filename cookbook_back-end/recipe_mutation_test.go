package main

import (
	"database/sql"
	"net/http"
	"testing"
)

func TestRecipeUpdateTransactionRollsBackOnRevisionFailure(t *testing.T) {
	db, handler, cookie := recipeMutationHandlerFixture(t, false)
	if _, err := db.Exec(`CREATE TRIGGER reject_recipe_revision BEFORE INSERT ON recipe_revisions BEGIN SELECT RAISE(ABORT, 'revision rejected'); END`); err != nil {
		t.Fatal(err)
	}

	response := callAPI(handler, http.MethodPut, "/api/recipes/"+testFederationID, map[string]any{
		"title": "Updated", "description": "Updated description",
		"ingredients": []map[string]string{{"name": "salt"}}, "steps": []map[string]string{{"text": "Stir"}},
		"servings": 3, "prep_time_minutes": 5, "cook_time_minutes": 8, "tags": []string{"quick"}, "version": 2,
	}, cookie)
	if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"error\":\"storage_error\"}\n" {
		t.Fatalf("update failure response=%d %s", response.Code, response.Body)
	}
	assertRecipeMutationState(t, db, "Current", "private", "current.png", 2, 2, 1, 0)
}

func TestRecipeUpdateTransactionCommitsRecipeRevisionAndCookbookRefs(t *testing.T) {
	db, handler, cookie := recipeMutationHandlerFixture(t, false)
	response := callAPI(handler, http.MethodPut, "/api/recipes/"+testFederationID, map[string]any{
		"title": "Updated", "description": "Updated description",
		"ingredients": []map[string]string{{"name": " salt "}}, "steps": []map[string]string{{"text": " Stir "}},
		"servings": 3, "prep_time_minutes": 5, "cook_time_minutes": 8, "tags": []string{" Quick "}, "version": 2,
	}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("update response=%d %s", response.Code, response.Body)
	}
	assertRecipeMutationState(t, db, "Updated", "private", "current.png", 3, 3, 0, 0)
	var ingredients, steps, tags string
	if err := db.QueryRow(`SELECT ingredients_json,steps_json,tags_json FROM recipe_revisions WHERE recipe_id=? AND version=3`, testFederationID).Scan(&ingredients, &steps, &tags); err != nil {
		t.Fatal(err)
	}
	if ingredients != `[{"name":"salt"}]` || steps != `[{"text":"Stir"}]` || tags != `["quick"]` {
		t.Fatalf("revision did not preserve normalized snapshot: ingredients=%s steps=%s tags=%s", ingredients, steps, tags)
	}
}

func TestRecipeRevisionRestoreTransactionRollsBackOnOutboxFailure(t *testing.T) {
	db, handler, cookie := recipeMutationHandlerFixture(t, true)
	if _, err := db.Exec(`CREATE TRIGGER reject_federation_outbox BEFORE INSERT ON federation_outbox BEGIN SELECT RAISE(ABORT, 'outbox rejected'); END`); err != nil {
		t.Fatal(err)
	}

	response := callAPI(handler, http.MethodPost, "/api/recipes/"+testFederationID+"/revisions/1/restore", map[string]any{"expected_version": 2, "confirm_public_access": true}, cookie)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"error\":\"federation_queue_unavailable\"}\n" {
		t.Fatalf("restore failure response=%d %s", response.Code, response.Body)
	}
	assertRecipeMutationState(t, db, "Current", "private", "current.png", 2, 2, 1, 0)
	var localObjects, peerObjects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_local_objects`).Scan(&localObjects); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_local_peer_objects`).Scan(&peerObjects); err != nil {
		t.Fatal(err)
	}
	if localObjects != 0 || peerObjects != 0 {
		t.Fatalf("failed outbox insert left publication state: local=%d peer=%d", localObjects, peerObjects)
	}
}

func TestRecipeRevisionRestoreTransactionCommitsSnapshotImageAndOutbox(t *testing.T) {
	db, handler, cookie := recipeMutationHandlerFixture(t, true)
	response := callAPI(handler, http.MethodPost, "/api/recipes/"+testFederationID+"/revisions/1/restore", map[string]any{"expected_version": 2, "confirm_public_access": true}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("restore response=%d %s", response.Code, response.Body)
	}
	assertRecipeMutationState(t, db, "First revision", "public", "restored.png", 3, 3, 1, 1)
	var editor, imagePath string
	if err := db.QueryRow(`SELECT editor_id,image_path FROM recipe_revisions WHERE recipe_id=? AND version=3`, testFederationID).Scan(&editor, &imagePath); err != nil {
		t.Fatal(err)
	}
	if editor != "owner" || imagePath != "restored.png" {
		t.Fatalf("restore snapshot editor=%q image_path=%q", editor, imagePath)
	}
	var sequence, published int
	if err := db.QueryRow(`SELECT sequence,published FROM federation_local_objects WHERE recipe_id=?`, testFederationID).Scan(&sequence, &published); err != nil || sequence != 1 || published != 1 {
		t.Fatalf("publication sequence=%d published=%d err=%v", sequence, published, err)
	}
	var eventType string
	if err := db.QueryRow(`SELECT json_extract(CAST(payload AS TEXT),'$.event_type') FROM federation_outbox`).Scan(&eventType); err != nil || eventType != federationEventSnapshot {
		t.Fatalf("outbox event=%q err=%v", eventType, err)
	}
}

func recipeMutationHandlerFixture(t *testing.T, federation bool) (*sql.DB, http.Handler, *http.Cookie) {
	t.Helper()
	db := testDatabase(t)
	_, err := db.Exec(`
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Family','private','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,image_path,visibility,edit_policy,version,created_at,updated_at)
		VALUES('0123456789abcdef0123456789abcdef','owner','Owner','Current','Current description','[{"name":"flour","quantity":"1 cup","notes":"sifted"}]','[{"text":"Mix"}]',2,10,20,'["old"]','current.png','private','owner_only',2,'now','now');
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,image_path,visibility,created_at)
		VALUES('revision-1','0123456789abcdef0123456789abcdef',1,'owner','First revision','First description','[{"name":"flour","quantity":"1 cup","notes":"sifted"}]','[{"text":"Mix"}]',2,10,20,'["old"]','restored.png','public','now'),
		('revision-2','0123456789abcdef0123456789abcdef',2,'owner','Current','Current description','[{"name":"flour","quantity":"1 cup","notes":"sifted"}]','[{"text":"Mix"}]',2,10,20,'["old"]','current.png','private','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','0123456789abcdef0123456789abcdef','owner','now');`)
	if err != nil {
		t.Fatal(err)
	}
	if federation {
		if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
			t.Fatal(err)
		}
		insertCompletedPublisherProfile(t, db, "owner", "Private publisher")
		if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at,published_at)
			VALUES(?, 'published', 'followers', 0, 'now', 'now')`, testFederationID); err != nil {
			t.Fatal(err)
		}
		if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
			t.Fatal(err)
		}
	} else if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return db, handlerWithAuth(db, testAuth(db)), &http.Cookie{Name: sessionCookie, Value: token}
}

func assertRecipeMutationState(t *testing.T, db *sql.DB, title, visibilityValue, imagePath string, version, revisions, cookbookRefs, outbox int) {
	t.Helper()
	var storedTitle, storedVisibility, storedImage string
	var storedVersion, revisionCount, cookbookRefCount, outboxCount int
	if err := db.QueryRow(`SELECT title,visibility,image_path,version FROM recipes WHERE id=?`, testFederationID).Scan(&storedTitle, &storedVisibility, &storedImage, &storedVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id=?`, testFederationID).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE recipe_id=?`, testFederationID).Scan(&cookbookRefCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if storedTitle != title || storedVisibility != visibilityValue || storedImage != imagePath || storedVersion != version || revisionCount != revisions || cookbookRefCount != cookbookRefs || outboxCount != outbox {
		t.Fatalf("recipe mutation state title=%q visibility=%q image=%q version=%d revisions=%d cookbook_refs=%d outbox=%d", storedTitle, storedVisibility, storedImage, storedVersion, revisionCount, cookbookRefCount, outboxCount)
	}
}
