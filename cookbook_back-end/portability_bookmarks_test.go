package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func portableBookmarkArchive(t *testing.T, bundle portableBundle, media map[string][]byte) []byte {
	t.Helper()
	manifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	entry, err := archive.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(manifest); err != nil {
		t.Fatal(err)
	}
	for name, content := range media {
		entry, err := archive.Create("media/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func importPortableBookmarkArchive(t *testing.T, h http.Handler, userID string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(data))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, userID))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func exportPortableBookmarkManifest(t *testing.T, h http.Handler, userID string) ([]byte, portableBundle) {
	t.Helper()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, requestForUser(http.MethodGet, "/api/export", userID))
	if response.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", response.Code, response.Body)
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		if entry.Name != "manifest.json" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read exported manifest: read=%v close=%v", err, closeErr)
		}
		var bundle portableBundle
		if err := json.Unmarshal(manifest, &bundle); err != nil {
			t.Fatal(err)
		}
		return manifest, bundle
	}
	t.Fatal("export archive has no manifest")
	return nil, portableBundle{}
}

func TestPortableBookmarkExportImportRoundTripsIdentityOnly(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('reader','reader@example.test','Reader','now'),('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('shared-book','owner','Shared','private','now','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('shared-book','reader','viewer','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,tags_json,image_path,visibility,edit_policy,version,created_at,updated_at)
		VALUES('shared-source','owner','Private source creator','PRIVATE_SOURCE_BODY','private description','[]','[]','[]','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png','private','owner_only',1,'now','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('shared-book','shared-source','owner','now');
		INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES('https://remote.example.org',1,'now','now');`); err != nil {
		t.Fatal(err)
	}
	remoteURI := "https://remote.example.org/federation/recipes/0123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at)
		VALUES(?, 'https://remote.example.org','0123456789abcdef0123456789abcdef',1,'Remote secret creator','{"title":"REMOTE_SOURCE_BODY"}',0,'2026-11-03T12:00:00Z')`, remoteURI); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	localCreated := "2026-11-03T12:00:01.000000000Z"
	remoteCreated := "2026-11-03T12:00:02.000000000Z"
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,local_instance_id,local_recipe_id,created_at)
		VALUES('local-ref','reader','local',?,'shared-source',?)`, instanceID, localCreated); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at)
		VALUES('remote-ref','reader','remote',?,?)`, remoteURI, remoteCreated); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at)
		VALUES('owner-only-ref','owner','remote','https://owner.example.org/federation/recipes/1123456789abcdef0123456789abcdef','2026-11-03T12:00:03Z')`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	manifest, bundle := exportPortableBookmarkManifest(t, h, "reader")
	if bundle.Format != portableFormatV10 || bundle.Bookmarks == nil || len(bundle.Bookmarks) != 2 || len(bundle.Recipes) != 0 || len(bundle.Cookbooks) != 0 {
		t.Fatalf("portable v5 export content: format=%q bookmarks=%+v recipes=%d books=%d", bundle.Format, bundle.Bookmarks, len(bundle.Recipes), len(bundle.Cookbooks))
	}
	archiveBytes := mustPortableArchive(t, h, "reader")
	bookmarksByKind := make(map[string]portableBookmark)
	for _, bookmark := range bundle.Bookmarks {
		bookmarksByKind[bookmark.TargetKind] = bookmark
	}
	local := bookmarksByKind["local"]
	if local.LocalInstanceID != instanceID || local.LocalRecipeID != "shared-source" || local.RemoteObjectURI != "" || local.CreatedAt != localCreated {
		t.Fatalf("exported local identity=%+v", local)
	}
	remote := bookmarksByKind["remote"]
	if remote.RemoteObjectURI != remoteURI || remote.LocalInstanceID != "" || remote.LocalRecipeID != "" || remote.CreatedAt != remoteCreated {
		t.Fatalf("exported remote identity=%+v", remote)
	}
	var rawManifest map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &rawManifest); err != nil {
		t.Fatal(err)
	}
	var encodedBookmarks []map[string]json.RawMessage
	if err := json.Unmarshal(rawManifest["bookmarks"], &encodedBookmarks); err != nil || len(encodedBookmarks) != 2 {
		t.Fatalf("portable bookmark fields=%v err=%v", encodedBookmarks, err)
	}
	for _, item := range encodedBookmarks {
		allowed := map[string]bool{"target_kind": true, "created_at": true}
		if string(item["target_kind"]) == `"local"` {
			allowed["local_instance_id"] = true
			allowed["local_recipe_id"] = true
			if len(item) != 4 {
				t.Errorf("local bookmark has unexpected fields: %v", item)
			}
		} else {
			allowed["remote_object_uri"] = true
			if len(item) != 3 {
				t.Errorf("remote bookmark has unexpected fields: %v", item)
			}
		}
		for key := range item {
			if !allowed[key] {
				t.Errorf("portable bookmark includes non-identity field %q: %v", key, item)
			}
		}
	}
	for _, privateValue := range []string{"PRIVATE_SOURCE_BODY", "private description", "Private source creator", "REMOTE_SOURCE_BODY", "Remote secret creator", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"} {
		if bytes.Contains(manifest, []byte(privateValue)) {
			t.Errorf("portable manifest leaked bookmarked source data %q", privateValue)
		}
	}
	if _, err := db.Exec(`DELETE FROM recipe_bookmarks WHERE user_id='reader'`); err != nil {
		t.Fatal(err)
	}
	importResponse := importPortableBookmarkArchive(t, h, "reader", archiveBytes)
	if importResponse.Code != http.StatusCreated {
		t.Fatalf("round-trip import status=%d body=%s", importResponse.Code, importResponse.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id='reader'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("round-trip bookmark count=%d err=%v", count, err)
	}
	var gotInstanceID, gotRecipeID, gotCreatedAt, gotRemoteURI string
	if err := db.QueryRow(`SELECT local_instance_id,local_recipe_id,created_at FROM recipe_bookmarks WHERE user_id='reader' AND target_kind='local'`).Scan(&gotInstanceID, &gotRecipeID, &gotCreatedAt); err != nil || gotInstanceID != instanceID || gotRecipeID != "shared-source" || gotCreatedAt != localCreated {
		t.Fatalf("round-trip local target=%s/%s created=%s err=%v", gotInstanceID, gotRecipeID, gotCreatedAt, err)
	}
	if err := db.QueryRow(`SELECT remote_object_uri,created_at FROM recipe_bookmarks WHERE user_id='reader' AND target_kind='remote'`).Scan(&gotRemoteURI, &gotCreatedAt); err != nil || gotRemoteURI != remoteURI || gotCreatedAt != remoteCreated {
		t.Fatalf("round-trip remote target=%s created=%s err=%v", gotRemoteURI, gotCreatedAt, err)
	}
	list := httptest.NewRecorder()
	h.ServeHTTP(list, requestForUser(http.MethodGet, "/api/library/bookmarks", "reader"))
	var page RecipeBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("round-trip resolution status=%d body=%s", list.Code, list.Body)
	}
	for _, item := range page.Items {
		if item.Availability != "available" {
			t.Errorf("same-instance live target unavailable: %+v", item)
		}
	}
}

func mustPortableArchive(t *testing.T, h http.Handler, userID string) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, requestForUser(http.MethodGet, "/api/export", userID))
	if response.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", response.Code, response.Body)
	}
	return response.Body.Bytes()
}

func TestPortableBookmarkImportsPreserveUnavailableTargetsAndDeduplicate(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','reader@example.test','Reader','now'),('other','other@example.test','Other','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,version,created_at,updated_at)
		VALUES('foreign-target','reader','Reader','Private local recipe','[]','[]','[]','private',1,'now','now'),
		('inaccessible-private','other','Other','Private source','[]','[]','[]','private',1,'now','now');`); err != nil {
		t.Fatal(err)
	}
	currentID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	foreignID := strings.Repeat("b", 32)
	remoteURI := "https://cache-miss.example.org/federation/recipes/1123456789abcdef0123456789abcdef"
	firstCreated := "2026-11-03T12:00:01Z"
	secondCreated := "2026-11-03T12:00:02Z"
	bundle := portableBundle{
		Format: portableFormatV3, Cookbooks: []portableCookbook{},
		Recipes: []portableRecipe{{ID: "source-copy-id", CreatorName: "Source", Title: "Imported private copy", Ingredients: []ingredient{}, Steps: []recipeStep{}, Tags: []string{}, Visibility: "public", CookbookIDs: []string{}}},
		Bookmarks: []portableBookmark{
			{TargetKind: "local", LocalInstanceID: currentID, LocalRecipeID: "source-copy-id", CreatedAt: firstCreated},
			{TargetKind: "local", LocalInstanceID: foreignID, LocalRecipeID: "foreign-target", CreatedAt: firstCreated},
			{TargetKind: "local", LocalInstanceID: currentID, LocalRecipeID: "inaccessible-private", CreatedAt: firstCreated},
			{TargetKind: "remote", RemoteObjectURI: remoteURI, CreatedAt: firstCreated},
			{TargetKind: "remote", RemoteObjectURI: remoteURI, CreatedAt: secondCreated},
		},
	}
	archive := portableBookmarkArchive(t, bundle, nil)
	h := apiHandler{db: db}
	for attempt := 1; attempt <= 2; attempt++ {
		response := importPortableBookmarkArchive(t, h, "reader", archive)
		if response.Code != http.StatusCreated {
			t.Fatalf("import attempt %d status=%d body=%s", attempt, response.Code, response.Body)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id='reader'`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("deduplicated bookmarks=%d err=%v", count, err)
	}
	var createdAt string
	if err := db.QueryRow(`SELECT created_at FROM recipe_bookmarks WHERE user_id='reader' AND target_kind='remote'`).Scan(&createdAt); err != nil || createdAt != firstCreated {
		t.Fatalf("duplicate changed first creation time=%q err=%v", createdAt, err)
	}
	var importedRecipeID string
	if err := db.QueryRow(`SELECT id FROM recipes WHERE owner_id='reader' AND title='Imported private copy' ORDER BY rowid LIMIT 1`).Scan(&importedRecipeID); err != nil || importedRecipeID == "source-copy-id" {
		t.Fatalf("imported recipe id=%q err=%v; copy must be remapped independently", importedRecipeID, err)
	}
	var importedCopies, cacheRows, peerRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='reader' AND title='Imported private copy'`).Scan(&importedCopies); err != nil || importedCopies != 2 {
		t.Fatalf("repeat import copy count=%d err=%v", importedCopies, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipes`).Scan(&cacheRows); err != nil || cacheRows != 0 {
		t.Fatalf("import fetched or created remote cache rows=%d err=%v", cacheRows, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_peers`).Scan(&peerRows); err != nil || peerRows != 0 {
		t.Fatalf("import created remote peer rows=%d err=%v", peerRows, err)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, requestForUser(http.MethodGet, "/api/library/bookmarks", "reader"))
	var page RecipeBookmarkPage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("unavailable references status=%d body=%s", response.Code, response.Body)
	}
	for _, item := range page.Items {
		if item.Availability != "unavailable" || item.Target != nil || item.LocalRecipe != nil || item.RemoteRecipe != nil {
			t.Errorf("import resolved or exposed an unavailable target: %+v", item)
		}
	}
	var remappedTarget string
	if err := db.QueryRow(`SELECT local_recipe_id FROM recipe_bookmarks WHERE user_id='reader' AND target_kind='local' AND local_instance_id=? AND local_recipe_id='source-copy-id'`, currentID).Scan(&remappedTarget); err != nil || remappedTarget != "source-copy-id" {
		t.Fatalf("bookmark was mapped onto imported copy: target=%q err=%v", remappedTarget, err)
	}
}

func TestPortableBookmarkImportEnforcesCombinedAccountLimit(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','reader@example.test','Reader','now')`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<9998)
		INSERT INTO recipe_bookmarks(id,user_id,target_kind,local_instance_id,local_recipe_id,created_at)
		SELECT printf('existing-%05d',i),'reader','local',?,printf('target-%05d',i),'2026-11-03T12:00:00Z' FROM n`, instanceID); err != nil {
		t.Fatal(err)
	}
	bundle := portableBundle{
		Format: portableFormatV3, Cookbooks: []portableCookbook{},
		Recipes: []portableRecipe{{ID: "would-copy", CreatorName: "Reader", Title: "One copy", Ingredients: []ingredient{}, Steps: []recipeStep{}, Tags: []string{}, Visibility: "private", CookbookIDs: []string{}}},
		Bookmarks: []portableBookmark{
			{TargetKind: "remote", RemoteObjectURI: "https://cap-one.example.org/federation/recipes/2123456789abcdef0123456789abcdef", CreatedAt: "2026-11-03T12:00:00Z"},
			{TargetKind: "remote", RemoteObjectURI: "https://cap-two.example.org/federation/recipes/3123456789abcdef0123456789abcdef", CreatedAt: "2026-11-03T12:00:00Z"},
		},
	}
	h := apiHandler{db: db}
	response := importPortableBookmarkArchive(t, h, "reader", portableBookmarkArchive(t, bundle, nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("import to exactly 10,000 status=%d body=%s", response.Code, response.Body)
	}
	var count, copies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id='reader'`).Scan(&count); err != nil || count != maxRecipeBookmarksPerAccount {
		t.Fatalf("bookmarks at cap=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='reader' AND title='One copy'`).Scan(&copies); err != nil || copies != 1 {
		t.Fatalf("first recipe import count=%d err=%v", copies, err)
	}
	bundle.Bookmarks = append(bundle.Bookmarks, portableBookmark{TargetKind: "remote", RemoteObjectURI: "https://cap-three.example.org/federation/recipes/4123456789abcdef0123456789abcdef", CreatedAt: "2026-11-03T12:00:00Z"})
	bundle.Recipes[0].ID = "must-not-copy"
	response = importPortableBookmarkArchive(t, h, "reader", portableBookmarkArchive(t, bundle, nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "bookmark_limit_reached") {
		t.Fatalf("over-cap import status=%d body=%s", response.Code, response.Body)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id='reader'`).Scan(&count); err != nil || count != maxRecipeBookmarksPerAccount {
		t.Fatalf("over-cap import changed bookmarks=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='reader'`).Scan(&copies); err != nil || copies != 1 {
		t.Fatalf("over-cap import left recipe rows=%d err=%v", copies, err)
	}
}

func TestPortableBookmarkImportRollbackRemovesMediaAndRecipeRows(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','reader@example.test','Reader','now');
		CREATE TRIGGER reject_portable_bookmark BEFORE INSERT ON recipe_bookmarks
		BEGIN SELECT RAISE(ABORT,'injected bookmark failure'); END;`); err != nil {
		t.Fatal(err)
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	mediaName := strings.Repeat("a", 32) + ".png"
	bundle := portableBundle{
		Format:    portableFormatV3,
		Cookbooks: []portableCookbook{},
		Recipes:   []portableRecipe{{ID: "will-rollback", CreatorName: "Reader", Title: "Must roll back", Ingredients: []ingredient{}, Steps: []recipeStep{}, Tags: []string{}, Visibility: "private", CookbookIDs: []string{}, ImageFile: mediaName}},
		Bookmarks: []portableBookmark{{TargetKind: "local", LocalInstanceID: strings.Repeat("c", 32), LocalRecipeID: "source", CreatedAt: "2026-11-03T12:00:00Z"}},
	}
	mediaDir := t.TempDir()
	h := apiHandler{db: db, mediaDir: mediaDir}
	response := importPortableBookmarkArchive(t, h, "reader", portableBookmarkArchive(t, bundle, map[string][]byte{mediaName: imageBytes.Bytes()}))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "storage_error") {
		t.Fatalf("injected bookmark failure status=%d body=%s", response.Code, response.Body)
	}
	var recipeCount, revisionCount, bookmarkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='reader'`).Scan(&recipeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id IN (SELECT id FROM recipes WHERE owner_id='reader')`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id='reader'`).Scan(&bookmarkCount); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	if recipeCount != 0 || revisionCount != 0 || bookmarkCount != 0 || len(files) != 0 {
		t.Fatalf("failed import left recipe/revision/bookmark/files=%d/%d/%d/%v", recipeCount, revisionCount, bookmarkCount, files)
	}
}

func TestPortableBookmarkValidationAndLegacyFormats(t *testing.T) {
	for _, format := range []string{legacyPortableFormat, portableFormat} {
		bundle := portableBundle{Format: format, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}}
		if err := validatePortableBundle(bundle); err != nil {
			t.Errorf("legacy format %s without bookmarks rejected: %v", format, err)
		}
	}
	legacyJSON := []byte(`{"format":"cookbook-export-v1","cookbooks":[],"recipes":[]}`)
	bundle, media, err := decodePortableArchive(legacyJSON)
	if err != nil || bundle.Format != legacyPortableFormat || bundle.Bookmarks != nil || len(media) != 0 {
		t.Fatalf("legacy JSON import bundle=%+v media=%d err=%v", bundle, len(media), err)
	}
	if err := validatePortableBundle(bundle); err != nil {
		t.Fatalf("legacy v1 bundle validation: %v", err)
	}
	oldV2Manifest, err := json.Marshal(struct {
		Format    string             `json:"format"`
		Exported  string             `json:"exported_at"`
		Cookbooks []portableCookbook `json:"cookbooks"`
		Recipes   []portableRecipe   `json:"recipes"`
	}{Format: portableFormat, Exported: "2026-11-03T12:00:00Z", Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}})
	if err != nil {
		t.Fatal(err)
	}
	var oldV2 bytes.Buffer
	oldV2Archive := zip.NewWriter(&oldV2)
	oldV2Entry, err := oldV2Archive.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldV2Entry.Write(oldV2Manifest); err != nil {
		t.Fatal(err)
	}
	if err := oldV2Archive.Close(); err != nil {
		t.Fatal(err)
	}
	v2Bundle, _, err := decodePortableArchive(oldV2.Bytes())
	if err != nil || v2Bundle.Format != portableFormat || v2Bundle.Bookmarks != nil {
		t.Fatalf("old v2 archive bundle=%+v err=%v", v2Bundle, err)
	}
	if err := validatePortableBundle(v2Bundle); err != nil {
		t.Fatalf("old v2 bundle validation: %v", err)
	}
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','reader@example.test','Reader','now')`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	for name, archive := range map[string][]byte{"v1": legacyJSON, "v2": oldV2.Bytes()} {
		response := importPortableBookmarkArchive(t, h, "reader", archive)
		if response.Code != http.StatusCreated {
			t.Errorf("old %s import status=%d body=%s", name, response.Code, response.Body)
		}
	}
	var importedBookmarks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id='reader'`).Scan(&importedBookmarks); err != nil || importedBookmarks != 0 {
		t.Fatalf("old-format imports created bookmarks=%d err=%v", importedBookmarks, err)
	}
	if err := validatePortableBundle(portableBundle{Format: portableFormatV3, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}}); err == nil {
		t.Fatal("v3 without its bookmarks array was accepted")
	}
	validDate := "2026-11-03T12:00:00Z"
	invalidCases := []portableBookmark{
		{TargetKind: "local", LocalInstanceID: "bad", LocalRecipeID: "recipe", CreatedAt: validDate},
		{TargetKind: "local", LocalInstanceID: strings.Repeat("a", 32), LocalRecipeID: "recipe", RemoteObjectURI: "https://remote.example.org/federation/recipes/0123456789abcdef0123456789abcdef", CreatedAt: validDate},
		{TargetKind: "remote", RemoteObjectURI: "http://remote.example.org/federation/recipes/0123456789abcdef0123456789abcdef", CreatedAt: validDate},
		{TargetKind: "unknown", CreatedAt: validDate},
		{TargetKind: "remote", RemoteObjectURI: "https://remote.example.org/federation/recipes/0123456789abcdef0123456789abcdef", CreatedAt: "not-a-time"},
	}
	for _, item := range invalidCases {
		if err := validatePortableBundle(portableBundle{Format: portableFormatV3, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{item}}); err == nil {
			t.Errorf("invalid portable bookmark accepted: %+v", item)
		}
	}
	if err := validatePortableBundle(portableBundle{Format: portableFormatV3, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: make([]portableBookmark, maxRecipeBookmarksPerAccount+1)}); err == nil {
		t.Fatal("oversized bookmark array was accepted")
	}
	var zipped bytes.Buffer
	archive := zip.NewWriter(&zipped)
	entry, err := archive.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(`{"format":"cookbook-export-v3","cookbooks":[],"recipes":[],"bookmarks":[],"extra":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodePortableArchive(zipped.Bytes()); err == nil {
		t.Fatal("unknown v3 manifest field was accepted")
	}
}
