package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPortableV5ExportImportPreservesCookbookReferenceIdentityOnly(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('reader','reader@example.test','Reader','now'),
		('importer','importer@example.test','Importer','now'),
		('other','other@example.test','Other','now');
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at)
		VALUES('source-book','reader','Owned source title','Owned source description','private','now','now')`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	foreignInstanceID := strings.Repeat("f", 32)
	createdAt := "2026-11-04T01:00:00.123456789Z"
	if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES
		('owned-reference','reader',?,'source-book',?),
		('foreign-reference','reader',?,'remote-book',?),
		('other-reference','other',?,'must-not-export','now')`, instanceID, createdAt, foreignInstanceID, createdAt, instanceID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	manifest, bundle := exportPortableBookmarkManifest(t, h, "reader")
	if bundle.Format != portableFormatV10 || len(bundle.Cookbooks) != 1 {
		t.Fatalf("v5 export format/cookbooks=%q/%d", bundle.Format, len(bundle.Cookbooks))
	}
	bookmarks, err := portableCookbookBookmarksFromBundle(bundle)
	if err != nil || len(bookmarks) != 2 {
		t.Fatalf("v5 cookbook bookmarks=%+v err=%v manifest=%s", bookmarks, err, manifest)
	}
	byID := make(map[string]portableCookbookBookmark)
	for _, bookmark := range bookmarks {
		byID[bookmark.LocalCookbookID] = bookmark
	}
	if byID["source-book"].LocalInstanceID != instanceID || byID["source-book"].CreatedAt != createdAt || byID["remote-book"].LocalInstanceID != foreignInstanceID {
		t.Fatalf("export did not preserve exact reference identities: %+v", bookmarks)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &raw); err != nil {
		t.Fatal(err)
	}
	var rawBookmarks []map[string]json.RawMessage
	if err := json.Unmarshal(raw["cookbook_bookmarks"], &rawBookmarks); err != nil || len(rawBookmarks) != 2 {
		t.Fatalf("raw cookbook bookmark fields=%s err=%v", raw["cookbook_bookmarks"], err)
	}
	for _, item := range rawBookmarks {
		if len(item) != 3 || item["local_instance_id"] == nil || item["local_cookbook_id"] == nil || item["created_at"] == nil {
			t.Fatalf("bookmark exported fields other than namespace, ID and time: %v", item)
		}
	}
	if strings.Contains(string(manifest), "must-not-export") {
		t.Fatal("export included another account's saved reference")
	}
	archive := portableBookmarkArchive(t, bundle, nil)
	imported := importPortableBookmarkArchive(t, h, "importer", archive)
	if imported.Code != http.StatusCreated {
		t.Fatalf("v5 import status=%d body=%s", imported.Code, imported.Body)
	}
	var importedTarget, importedNamespace, importedTime string
	if err := db.QueryRow(`SELECT local_cookbook_id,local_instance_id,created_at FROM cookbook_bookmarks WHERE user_id='importer' AND local_cookbook_id='source-book'`).Scan(&importedTarget, &importedNamespace, &importedTime); err != nil || importedTarget != "source-book" || importedNamespace != instanceID || importedTime != createdAt {
		t.Fatalf("imported source identity target=%q namespace=%q time=%q err=%v", importedTarget, importedNamespace, importedTime, err)
	}
	var copiedID string
	if err := db.QueryRow(`SELECT id FROM cookbooks WHERE owner_id='importer' AND title='Owned source title'`).Scan(&copiedID); err != nil || copiedID == "source-book" {
		t.Fatalf("imported cookbook copy id=%q err=%v", copiedID, err)
	}
	list := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute, "importer", nil)
	var page CookbookBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("imported bookmark list status=%d body=%s", list.Code, list.Body)
	}
	for _, item := range page.Items {
		if item.Availability != "unavailable" || item.Cookbook != nil || item.Target != nil {
			t.Fatalf("import rebound reference to copied or inaccessible cookbook: %+v", item)
		}
	}
}

func TestPortableCookbookBookmarkVersionsPresenceAndValidation(t *testing.T) {
	baseV5 := portableBundle{
		Format: portableFormatV5, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{},
		Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]"),
	}
	if err := validatePortableBundle(baseV5); err != nil {
		t.Fatalf("valid empty v5 rejected: %v", err)
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("{}"), json.RawMessage(`[{"local_instance_id":"bad","local_cookbook_id":"book","created_at":"2026-11-04T00:00:00Z"}]`), json.RawMessage(`[{"local_instance_id":"` + strings.Repeat("a", 32) + `","local_cookbook_id":"book","created_at":"bad"}]`), json.RawMessage(`[{"local_instance_id":"` + strings.Repeat("a", 32) + `","local_cookbook_id":"book","created_at":"2026-11-04T00:00:00Z","title":"stale"}]`)} {
		candidate := baseV5
		candidate.CookbookBookmarks = raw
		if err := validatePortableBundle(candidate); err == nil {
			t.Errorf("invalid v5 cookbook_bookmarks accepted: %s", raw)
		}
	}
	validBookmark := portableCookbookBookmark{LocalInstanceID: strings.Repeat("a", 32), LocalCookbookID: "book", CreatedAt: "2026-11-04T00:00:00Z"}
	encodedBookmark, err := json.Marshal([]portableCookbookBookmark{validBookmark})
	if err != nil {
		t.Fatal(err)
	}
	baseV5.CookbookBookmarks = encodedBookmark
	if err := validatePortableBundle(baseV5); err != nil {
		t.Fatalf("valid v5 reference rejected: %v", err)
	}
	for _, format := range []string{portableFormatV4, portableFormatV3, portableFormat, legacyPortableFormat} {
		candidate := baseV5
		candidate.Format = format
		candidate.CookbookBookmarks = nil
		if format == portableFormatV3 || format == portableFormatV4 {
			candidate.Bookmarks = []portableBookmark{}
		}
		if format == portableFormatV4 {
			candidate.Attributions = []portableAttribution{}
		}
		if err := validatePortableBundle(candidate); err != nil {
			t.Errorf("legacy format %s rejected without v5 field: %v", format, err)
		}
		candidate.CookbookBookmarks = json.RawMessage("null")
		if err := validatePortableBundle(candidate); err == nil {
			t.Errorf("legacy format %s accepted explicit cookbook_bookmarks null", format)
		}
	}
	v4 := portableBundle{Format: portableFormatV4, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}}
	serializedV4, err := json.Marshal(v4)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serializedV4), "cookbook_bookmarks") {
		t.Fatalf("new optional field changed v4 serialization: %s", serializedV4)
	}
	unknown := []byte(`{"format":"cookbook-export-v5","cookbooks":[],"recipes":[],"bookmarks":[],"attributions":[],"cookbook_bookmarks":[],"extra":true}`)
	if _, _, err := decodePortableArchive(unknown); err == nil {
		t.Fatal("v5 archive accepted unknown fields")
	}
}

func TestPortableCookbookBookmarkImportFailureIsAtomic(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('importer','importer@example.test','Importer','now')`); err != nil {
		t.Fatal(err)
	}
	bundle := portableBundle{
		Format:    portableFormatV5,
		Cookbooks: []portableCookbook{{ID: "copied", Title: "Copy", Visibility: "private", RecipeIDs: []string{}}},
		Recipes:   []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{},
		CookbookBookmarks: json.RawMessage(`[{"local_instance_id":"` + strings.Repeat("z", 32) + `","local_cookbook_id":"bad","created_at":"2026-11-04T00:00:00Z"}]`),
	}
	response := importPortableBookmarkArchive(t, apiHandler{db: db}, "importer", portableBookmarkArchive(t, bundle, nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid reference import status=%d body=%s", response.Code, response.Body)
	}
	var cookbooks, bookmarks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks`).Scan(&cookbooks); err != nil || cookbooks != 0 {
		t.Fatalf("malformed v5 import created cookbooks=%d err=%v", cookbooks, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks`).Scan(&bookmarks); err != nil || bookmarks != 0 {
		t.Fatalf("malformed v5 import created bookmarks=%d err=%v", bookmarks, err)
	}
}

func TestPortableCookbookBookmarkDatabaseFailureRollsBackImportedCopy(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('importer','importer@example.test','Importer','now');
		CREATE TRIGGER reject_cookbook_bookmark_import BEFORE INSERT ON cookbook_bookmarks
		BEGIN SELECT RAISE(ABORT,'forced cookbook bookmark failure'); END`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	bookmarkJSON, _ := json.Marshal([]portableCookbookBookmark{{LocalInstanceID: instanceID, LocalCookbookID: "source-book", CreatedAt: "2026-11-04T00:00:00Z"}})
	bundle := portableBundle{
		Format:    portableFormatV5,
		Cookbooks: []portableCookbook{{ID: "source-book", Title: "Copy", Visibility: "private", RecipeIDs: []string{}}},
		Recipes:   []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: bookmarkJSON,
	}
	response := importPortableBookmarkArchive(t, apiHandler{db: db}, "importer", portableBookmarkArchive(t, bundle, nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("forced database failure status=%d body=%s", response.Code, response.Body)
	}
	var cookbooks, bookmarks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE owner_id='importer'`).Scan(&cookbooks); err != nil || cookbooks != 0 {
		t.Fatalf("failed v5 import retained copied cookbook count=%d err=%v", cookbooks, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id='importer'`).Scan(&bookmarks); err != nil || bookmarks != 0 {
		t.Fatalf("failed v5 import retained bookmarks count=%d err=%v", bookmarks, err)
	}
}

func TestPortableV9V10CookbookRecipeAssociationsAreReciprocalAndAtomic(t *testing.T) {
	db := testDatabase(t)
	users := []string{"valid-v9", "valid-v10", "invalid-v9-recipe-side", "invalid-v9-cookbook-side", "invalid-v10-recipe-side", "invalid-v10-cookbook-side"}
	for _, user := range users {
		if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES(?,?,?,'now')`, user, user+"@example.test", user); err != nil {
			t.Fatal(err)
		}
	}
	makeBundle := func(format string, bookRecipes, recipeBooks []string) portableBundle {
		bundle := portableBundle{
			Format:    format,
			Cookbooks: []portableCookbook{{ID: "source-book", Title: "Source cookbook", Visibility: "private", RecipeIDs: bookRecipes}},
			Recipes:   []portableRecipe{{ID: "source-recipe", CreatorName: "Recipe creator", Title: "Source recipe", Ingredients: []ingredient{}, Steps: []recipeStep{}, Servings: 1, Tags: []string{}, Visibility: "private", EditPolicy: "owner_only", CookbookIDs: recipeBooks}},
			Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{},
			CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: json.RawMessage("[]"),
		}
		if format == portableFormatV9 || format == portableFormatV10 {
			courses := []portableCourse{}
			omittedCourseLinks := 0
			snapshots := []portableSnapshotReference{}
			bundle.Courses, bundle.OmittedCourseRecipeLinks, bundle.SnapshotEntries = &courses, &omittedCourseLinks, &snapshots
		}
		if format == portableFormatV10 {
			menus := []portableMenu{}
			omittedMenuLinks := 0
			bundle.Menus, bundle.OmittedMenuRecipeLinks = &menus, &omittedMenuLinks
		}
		return bundle
	}

	for _, format := range []string{portableFormatV9, portableFormatV10} {
		validArchive := portableBookmarkArchive(t, makeBundle(format, []string{"source-recipe"}, []string{"source-book"}), nil)
		validUser := "valid-v9"
		if format == portableFormatV10 {
			validUser = "valid-v10"
		}
		validImport := importPortableBookmarkArchive(t, apiHandler{db: db}, validUser, validArchive)
		if validImport.Code != http.StatusCreated {
			t.Fatalf("valid %s association import failed: %d %s", format, validImport.Code, validImport.Body)
		}
		var relationships int
		if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id JOIN recipes r ON r.id=cr.recipe_id WHERE c.owner_id=? AND r.owner_id=?`, validUser, validUser).Scan(&relationships); err != nil || relationships != 1 {
			t.Fatalf("valid %s association was silently lost: count=%d err=%v", format, relationships, err)
		}

		invalidCases := []struct {
			name        string
			bookRecipes []string
			recipeBooks []string
		}{
			{name: "recipe-side-only", recipeBooks: []string{"source-book"}},
			{name: "cookbook-side-only", bookRecipes: []string{"source-recipe"}},
		}
		for _, invalid := range invalidCases {
			direction := "recipe"
			if invalid.name == "cookbook-side-only" {
				direction = "cookbook"
			}
			user := "invalid-" + strings.TrimPrefix(format, "cookbook-export-") + "-" + direction + "-side"
			archive := portableBookmarkArchive(t, makeBundle(format, invalid.bookRecipes, invalid.recipeBooks), nil)
			response := importPortableBookmarkArchive(t, apiHandler{db: db}, user, archive)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("mismatched %s %s association accepted: %d %s", format, invalid.name, response.Code, response.Body)
			}
			var cookbooks, recipes, relationships int
			if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE owner_id=?`, user).Scan(&cookbooks); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id=?`, user).Scan(&recipes); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id WHERE c.owner_id=?`, user).Scan(&relationships); err != nil {
				t.Fatal(err)
			}
			if cookbooks != 0 || recipes != 0 || relationships != 0 {
				t.Fatalf("invalid %s %s archive partially imported: cookbooks=%d recipes=%d relationships=%d", format, invalid.name, cookbooks, recipes, relationships)
			}
		}
	}
}
