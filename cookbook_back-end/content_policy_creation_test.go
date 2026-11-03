package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const policyCreationImageName = "0123456789abcdef0123456789abcdef.png"

func TestRecipeCreationWritesDefaultPolicyEvenWhenLocallyPublic(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{
		"title": "Public soup", "ingredients": []ingredient{{Name: "beans"}},
		"steps": []recipeStep{{Text: "Cook"}}, "visibility": string(visibilityPublic),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/recipes", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "owner"))
	apiHandler{db: db}.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body)
	}
	var created recipe
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.OwnerID != "owner" || created.Visibility != visibilityPublic || created.Title != "Public soup" {
		t.Fatalf("created response changed: %+v", created)
	}
	assertDefaultRecipePolicy(t, db, created.ID)
	assertTableCount(t, db, "recipes", 1)
	assertTableCount(t, db, "recipe_revisions", 1)
	assertTableCount(t, db, "recipe_publication_policy", 1)
	assertNoFederationRows(t, db)
}

func TestRecipeCreationPolicyFailureRollsBackRecipeAndReferences(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Family','private','now','now');
		CREATE TRIGGER reject_recipe_policy BEFORE INSERT ON recipe_publication_policy
		BEGIN SELECT RAISE(ABORT,'injected policy insertion failure'); END;`); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"title": "Soup", "ingredients": []ingredient{{Name: "beans"}},
		"steps": []recipeStep{{Text: "Cook"}}, "cookbook_ids": []string{"book"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/recipes", bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "owner"))
	apiHandler{db: db}.ServeHTTP(response, request)
	assertStorageError(t, response)
	assertTableCount(t, db, "recipes", 0)
	assertTableCount(t, db, "recipe_revisions", 0)
	assertTableCount(t, db, "cookbook_recipes", 0)
	assertTableCount(t, db, "recipe_publication_policy", 0)
	assertTableCount(t, db, "cookbooks", 1)
	assertNoFederationRows(t, db)
}

func TestForkWritesFreshDefaultPolicyWithoutCopyingSourcePolicy(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('forker','forker@example.test','Forker','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES('source','owner','Original creator','Pie','[{"name":"flour"}]','[{"text":"Bake"}]','[]','public','owner_only',3,'created','updated');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_by,updated_at,published_at)
		VALUES('source','published','followers',1,'owner','policy-updated','policy-published');
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at)
		VALUES('source-revision','source',3,'owner','Pie','','[{"name":"flour"}]','[{"text":"Bake"}]','public','revision-created');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('forker-book','forker','Copies','private','created','updated')`); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/recipes/source/fork", strings.NewReader(`{"cookbook_ids":["forker-book"]}`))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "forker"))
	apiHandler{db: db}.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("fork status=%d body=%s", response.Code, response.Body)
	}
	var copied recipe
	if err := json.Unmarshal(response.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	if copied.ID == "" || copied.ID == "source" || copied.OwnerID != "forker" || copied.SourceRecipeID != "source" || copied.CreatorName != "Original creator" || copied.Visibility != visibilityPrivate || copied.Version != 1 {
		t.Fatalf("fork response changed: %+v", copied)
	}
	assertDefaultRecipePolicy(t, db, copied.ID)
	assertTableCount(t, db, "recipes", 2)
	assertTableCount(t, db, "recipe_publication_policy", 2)
	assertTableCount(t, db, "recipe_revisions", 2)
	assertTableCount(t, db, "cookbook_recipes", 1)
	var state, federation, updatedBy, updatedAt, publishedAt string
	var indexEligible int
	if err := db.QueryRow(`SELECT publication_state,federation_mode,index_eligible,updated_by,updated_at,published_at FROM recipe_publication_policy WHERE recipe_id='source'`).Scan(&state, &federation, &indexEligible, &updatedBy, &updatedAt, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationPublished) || federation != string(RecipeFederationFollowers) || indexEligible != 1 || updatedBy != "owner" || updatedAt != "policy-updated" || publishedAt != "policy-published" {
		t.Fatalf("source policy changed: state=%s federation=%s index=%d updater=%s updated=%s published=%s", state, federation, indexEligible, updatedBy, updatedAt, publishedAt)
	}
	var sourceTitle string
	var sourceVersion int
	if err := db.QueryRow(`SELECT title,version FROM recipes WHERE id='source'`).Scan(&sourceTitle, &sourceVersion); err != nil || sourceTitle != "Pie" || sourceVersion != 3 {
		t.Fatalf("source recipe changed: title=%q version=%d err=%v", sourceTitle, sourceVersion, err)
	}
	assertNoFederationRows(t, db)
}

func TestForkPolicyFailureRollsBackCopyAndReference(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('forker','forker@example.test','Forker','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES('source','owner','Original creator','Pie','[]','[]','[]','public','owner_only',3,'created','updated');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at)
		VALUES('source','published','followers',1,'source-policy');
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at)
		VALUES('source-revision','source',3,'owner','Pie','','[]','[]','private','revision-created');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('forker-book','forker','Copies','private','created','updated');
		CREATE TRIGGER reject_recipe_policy BEFORE INSERT ON recipe_publication_policy
		WHEN NEW.recipe_id!='source'
		BEGIN SELECT RAISE(ABORT,'injected policy insertion failure'); END;`); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/recipes/source/fork", strings.NewReader(`{"cookbook_ids":["forker-book"]}`))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "forker"))
	apiHandler{db: db}.ServeHTTP(response, request)
	assertStorageError(t, response)
	assertTableCount(t, db, "recipes", 1)
	assertTableCount(t, db, "recipe_revisions", 1)
	assertTableCount(t, db, "recipe_publication_policy", 1)
	assertTableCount(t, db, "cookbook_recipes", 0)
	var owner, title string
	var version int
	if err := db.QueryRow(`SELECT owner_id,title,version FROM recipes WHERE id='source'`).Scan(&owner, &title, &version); err != nil || owner != "owner" || title != "Pie" || version != 3 {
		t.Fatalf("source recipe changed: owner=%s title=%s version=%d err=%v", owner, title, version, err)
	}
	assertNoFederationRows(t, db)
}

func TestImportWritesDefaultPolicyAndKeepsImportedContentPrivate(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	mediaDir := t.TempDir()
	archive := policyCreationArchive(t, portableBundle{
		Format:    portableFormat,
		Cookbooks: []portableCookbook{{ID: "source-book", Title: "Imported book", Visibility: "public", RecipeIDs: []string{"source-recipe"}}},
		Recipes:   []portableRecipe{{ID: "source-recipe", CreatorName: "Claimed creator", SourceRecipeID: "claimed-source", Title: "Imported dish", Ingredients: []ingredient{{Name: "beans"}}, Steps: []recipeStep{{Text: "Cook"}}, Visibility: "public", CookbookIDs: []string{"source-book"}, ImageFile: policyCreationImageName}},
	}, map[string][]byte{policyCreationImageName: policyCreationPNG(t)})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(archive))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "owner"))
	apiHandler{db: db, mediaDir: mediaDir}.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("import status=%d body=%s", response.Code, response.Body)
	}
	var recipeID string
	if err := db.QueryRow(`SELECT id FROM recipes WHERE owner_id='owner'`).Scan(&recipeID); err != nil || recipeID == "source-recipe" {
		t.Fatalf("imported recipe id=%q err=%v", recipeID, err)
	}
	var owner, creator, sourceID, title, visibilityValue, imagePath string
	var version int
	if err := db.QueryRow(`SELECT owner_id,creator_name,source_recipe_id,title,visibility,image_path,version FROM recipes WHERE id=?`, recipeID).Scan(&owner, &creator, &sourceID, &title, &visibilityValue, &imagePath, &version); err != nil {
		t.Fatal(err)
	}
	if owner != "owner" || creator != "Claimed creator" || sourceID != "claimed-source" || title != "Imported dish" || visibilityValue != string(visibilityPrivate) || version != 1 {
		t.Fatalf("imported recipe changed: owner=%q creator=%q source=%q title=%q visibility=%q version=%d", owner, creator, sourceID, title, visibilityValue, version)
	}
	assertDefaultRecipePolicy(t, db, recipeID)
	assertTableCount(t, db, "recipes", 1)
	assertTableCount(t, db, "cookbooks", 1)
	assertTableCount(t, db, "recipe_revisions", 1)
	assertTableCount(t, db, "cookbook_recipes", 1)
	assertTableCount(t, db, "recipe_publication_policy", 1)
	if imagePath == "" {
		t.Fatal("import lost the staged media reference")
	}
	if _, err := os.Stat(filepath.Join(mediaDir, imagePath)); err != nil {
		t.Fatalf("imported media file missing: %v", err)
	}
	assertNoFederationRows(t, db)
}

func TestImportPolicyFailureRollsBackRowsAndRemovesStagedMedia(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		CREATE TRIGGER reject_recipe_policy BEFORE INSERT ON recipe_publication_policy
		BEGIN SELECT RAISE(ABORT,'injected policy insertion failure'); END;`); err != nil {
		t.Fatal(err)
	}
	mediaDir := t.TempDir()
	archive := policyCreationArchive(t, portableBundle{
		Format:    portableFormat,
		Cookbooks: []portableCookbook{{ID: "source-book", Title: "Imported book", Visibility: "private", RecipeIDs: []string{"source-recipe"}}},
		Recipes:   []portableRecipe{{ID: "source-recipe", CreatorName: "Creator", SourceRecipeID: "claimed-source", Title: "Imported dish", Ingredients: []ingredient{{Name: "beans"}}, Steps: []recipeStep{{Text: "Cook"}}, Visibility: "private", CookbookIDs: []string{"source-book"}, ImageFile: policyCreationImageName}},
	}, map[string][]byte{policyCreationImageName: policyCreationPNG(t)})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(archive))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "owner"))
	apiHandler{db: db, mediaDir: mediaDir}.ServeHTTP(response, request)
	assertStorageError(t, response)
	assertTableCount(t, db, "cookbooks", 0)
	assertTableCount(t, db, "recipes", 0)
	assertTableCount(t, db, "recipe_revisions", 0)
	assertTableCount(t, db, "cookbook_recipes", 0)
	assertTableCount(t, db, "recipe_publication_policy", 0)
	assertNoFederationRows(t, db)
	entries, err := os.ReadDir(mediaDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staged media files remain after rollback: entries=%v err=%v", entries, err)
	}
}

func policyCreationArchive(t *testing.T, bundle portableBundle, media map[string][]byte) []byte {
	t.Helper()
	manifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entry, err := archive.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(manifest); err != nil {
		t.Fatal(err)
	}
	for name, contents := range media {
		entry, err := archive.Create("media/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func policyCreationPNG(t *testing.T) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageData.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageData); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func assertDefaultRecipePolicy(t *testing.T, db *sql.DB, recipeID string) {
	t.Helper()
	var count, indexEligible int
	var state, federation string
	var updatedBy, publishedAt sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_publication_policy WHERE recipe_id=?`, recipeID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("policy rows for %s=%d err=%v, want exactly one", recipeID, count, err)
	}
	if err := db.QueryRow(`SELECT publication_state,federation_mode,index_eligible,updated_by,published_at FROM recipe_publication_policy WHERE recipe_id=?`, recipeID).Scan(&state, &federation, &indexEligible, &updatedBy, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationUnpublished) || federation != string(RecipeFederationOff) || indexEligible != 0 || updatedBy.Valid || publishedAt.Valid {
		t.Fatalf("policy for %s = state=%q federation=%q index=%d updater=%v published_at=%v", recipeID, state, federation, indexEligible, updatedBy, publishedAt)
	}
}

func assertStorageError(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"error":"storage_error"`) {
		t.Fatalf("response status=%d body=%s; want 500 storage_error", response.Code, response.Body)
	}
}

func assertTableCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != want {
		t.Fatalf("%s rows=%d err=%v, want %d", table, count, err, want)
	}
}

func assertNoFederationRows(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"federation_local_objects", "federation_local_peer_objects", "federation_outbox", "federation_remote_recipes"} {
		assertTableCount(t, db, table, 0)
	}
}
