package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeCachedPublicRemoteRecipeSupportsExchangeV2PayloadLimit(t *testing.T) {
	base := `{"title":"Dish","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	result := RemoteRecipeReadView{ObjectURI: testFederationOrigin + "/federation/recipes/" + testFederationID, SourceOrigin: testFederationOrigin, SourceRecipeID: testFederationID, CreatorName: "Creator", Sequence: 1, UpdatedAt: "2026-11-03T12:00:00Z"}
	for _, size := range []int{1 << 20, 1_500_000, 1_500_001} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			if len(base) > size {
				t.Fatal("test base exceeds boundary")
			}
			body := base + strings.Repeat(" ", size-len(base))
			_, err := decodeCachedPublicRemoteRecipe(result.ObjectURI, result.SourceOrigin, result.SourceRecipeID, result, body)
			if size <= 1_500_000 && err != nil {
				t.Fatalf("accepted boundary rejected: %v", err)
			}
			if size > 1_500_000 && err == nil {
				t.Fatal("one-byte-over body accepted")
			}
		})
	}
}

func TestReadCachedPublicRemoteRecipeIsPrivateInstanceSafeAndCacheOnly(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	objectURI := testFederationOrigin + "/federation/recipes/" + testFederationID
	body := `{"title":" Tomato soup ","description":" Warm soup ","ingredients":[{"name":" tomatoes ","quantity":"4","notes":"ripe"}],"steps":[{"text":" Simmer. "}],"servings":4,"prep_time_minutes":10,"cook_time_minutes":25,"tags":[" Soup ","soup"]}`
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1;
INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, objectURI, testFederationOrigin, testFederationID, 2, " Ada Cook ", body, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, err := readCachedPublicRemoteRecipe(context.Background(), db, ContentActor{AccountID: "viewer"}, objectURI)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectURI != objectURI || got.SourceOrigin != testFederationOrigin || got.SourceRecipeID != testFederationID || got.CreatorName != "Ada Cook" || got.Sequence != 2 {
		t.Fatalf("remote metadata = %+v", got)
	}
	if got.Recipe.Title != "Tomato soup" || got.Recipe.Description != "Warm soup" || len(got.Recipe.Tags) != 1 || got.Recipe.Tags[0] != "soup" {
		t.Fatalf("normalized remote recipe = %+v", got.Recipe)
	}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, ContentActor{}, objectURI); !errors.Is(err, errRemoteRecipeNotFound) {
		t.Fatalf("anonymous actor error = %v, want unavailable", err)
	}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, ContentActor{AccountID: "viewer"}, "https://cookbook.example.org/federation/recipes/"+testFederationID+"?fetch=true"); !errors.Is(err, errRemoteRecipeInvalidURI) {
		t.Fatalf("query URI error = %v, want invalid URI", err)
	}
	anonymous := httptest.NewRecorder()
	publicFederationRecipesHandler{db: db}.ServeHTTP(anonymous, httptest.NewRequest("GET", "/api/public/remote-recipes", nil))
	if anonymous.Code != 404 {
		t.Fatalf("anonymous remote listing on private instance status=%d, want 404", anonymous.Code)
	}
}

func TestReadCachedPublicRemoteRecipeFailsClosedOnTombstoneMissingAndCorruption(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	objectURI := testFederationOrigin + "/federation/recipes/" + testFederationID
	actor := ContentActor{AccountID: "viewer"}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, actor, objectURI); !errors.Is(err, errRemoteRecipeNotFound) {
		t.Fatalf("missing cache error = %v, want unavailable", err)
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,1,?)`, objectURI, testFederationOrigin, testFederationID, 3, "", "", "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, actor, objectURI); !errors.Is(err, errRemoteRecipeNotFound) {
		t.Fatalf("tombstone error = %v, want unavailable", err)
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET deleted=0,creator_name='Creator',recipe_json='{"title":"ok","unknown":"private"}',updated_at='2026-11-03T12:00:00Z' WHERE object_id=?`, objectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, actor, objectURI); err == nil || errors.Is(err, errRemoteRecipeNotFound) {
		t.Fatalf("corrupt cache error = %v, want fail-closed storage error", err)
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET recipe_json='{"title":"a","title":"b"}' WHERE object_id=?`, objectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, actor, objectURI); err == nil {
		t.Fatal("duplicate JSON keys were accepted")
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET recipe_json='{"Title":"case variant"}' WHERE object_id=?`, objectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, actor, objectURI); err == nil {
		t.Fatal("case-variant cache field was accepted")
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET source_recipe_id='1123456789abcdef0123456789abcdef' WHERE object_id=?`, objectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := readCachedPublicRemoteRecipe(context.Background(), db, actor, objectURI); err == nil {
		t.Fatal("source origin/object URI mismatch was accepted")
	}
}

func TestParseFederationRecipeObjectURIRequiresCanonicalExactObject(t *testing.T) {
	valid := testFederationOrigin + "/federation/recipes/" + testFederationID
	if origin, id, err := parseFederationRecipeObjectURI(valid); err != nil || origin != testFederationOrigin || id != testFederationID {
		t.Fatalf("valid URI parsed as %q %q err=%v", origin, id, err)
	}
	for _, uri := range []string{
		"http://cookbook.example.org/federation/recipes/" + testFederationID,
		"https://user@cookbook.example.org/federation/recipes/" + testFederationID,
		"https://cookbook.example.org:443/federation/recipes/" + testFederationID,
		"https://cookbook.example.org/federation/recipes/" + testFederationID + "#fragment",
		"https://cookbook.example.org/federation/recipes/%30" + testFederationID[1:],
		"https://cookbook.example.org/federation/recipes/../" + testFederationID,
	} {
		if _, _, err := parseFederationRecipeObjectURI(uri); !errors.Is(err, errRemoteRecipeInvalidURI) {
			t.Errorf("URI %q error=%v, want invalid", uri, err)
		}
	}
}

func TestReadCachedPublicRemoteRecipesValidatesBoundedBatch(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now');
		INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	uri := testFederationOrigin + "/federation/recipes/" + testFederationID
	body := `{"title":"Batch dish","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, uri, testFederationOrigin, testFederationID, 1, "Creator", body, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, err := readCachedPublicRemoteRecipes(context.Background(), db, ContentActor{AccountID: "viewer"}, []string{uri, uri, "https://invalid.example/?fetch=true"})
	if err != nil || len(got) != 1 || got[uri].Recipe.Title != "Batch dish" {
		t.Fatalf("validated batch = %+v err=%v", got, err)
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET source_recipe_id=? WHERE object_id=?`, "1123456789abcdef0123456789abcdef", uri); err != nil {
		t.Fatal(err)
	}
	if _, err := readCachedPublicRemoteRecipes(context.Background(), db, ContentActor{AccountID: "viewer"}, []string{uri}); err == nil {
		t.Fatal("mismatched cache source binding accepted")
	}
}
