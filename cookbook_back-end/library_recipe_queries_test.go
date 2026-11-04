package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLibraryRecipesSearchFiltersAuthorizedUnionAndBookmarksBeforePaging(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES
		('punctuation','owner','Owner','Citrus / Noodles','Bright herb sauce','[]','[]',1,0,0,'["weekday"]','private','owner_only',1,'created','2026-11-05'),
		('description-hit','owner','Owner','Another dinner','Lemony rice with thyme','[]','[]',1,0,0,'["quick"]','private','owner_only',1,'created','2026-11-04'),
		('not-readable','other','Other','Citrus / Noodles','Bright herb sauce','[]','[]',1,0,0,'["weekday"]','private','owner_only',1,'created','2026-11-06');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','punctuation','owner','now');`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,local_instance_id,local_recipe_id,created_at) VALUES('saved-readable','viewer','local',?,'punctuation','2026-11-07'),('saved-unavailable','viewer','local',?,'missing','2026-11-08')`, instanceID, instanceID); err != nil {
		t.Fatal(err)
	}
	remoteURI := testFederationOrigin + "/federation/recipes/" + testFederationID
	remoteBody := `{"title":"Remote Citrus Tart","description":"orange glaze","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":["dessert"]}`
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('saved-remote','viewer','remote',?,'2026-11-09');
		INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, remoteURI, testFederationOrigin, testFederationID, 1, "Remote creator", remoteBody, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	writeRemoteProjectionFixture(t, db, RemoteRecipeReadView{ObjectURI: remoteURI, SourceOrigin: testFederationOrigin, SourceRecipeID: testFederationID, CreatorName: "Remote creator", Sequence: 1, UpdatedAt: "2026-11-03T12:00:00Z"}, remoteBody)
	badURI := testFederationOrigin + "/federation/recipes/1123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('saved-malformed','viewer','remote',?,'2026-11-10');
		INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, badURI, testFederationOrigin, "1123456789abcdef0123456789abcdef", 1, "Remote creator", `{"title":`, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	duplicateURI := testFederationOrigin + "/federation/recipes/2123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('saved-duplicate','viewer','remote',?,'2026-11-11');
		INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, duplicateURI, testFederationOrigin, "2123456789abcdef0123456789abcdef", 1, "Remote creator", `{"title":"Remote Citrus","title":"Remote Citrus","description":"duplicate","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}

	request := func(actor, query string) (LibraryRecipePage, *httptest.ResponseRecorder) {
		t.Helper()
		response := doBookmarkRequest(t, h, actor, http.MethodGet, "/api/library/recipes"+query, "")
		var page LibraryRecipePage
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil {
			t.Fatalf("library search %s: %d %s", query, response.Code, response.Body)
		}
		return page, response
	}

	ownerPage, _ := request("owner", "?q=%20CITRUS%20%2F%20")
	if ownerPage.Total != 1 || len(ownerPage.Items) != 1 || ownerPage.Items[0].Recipe == nil || ownerPage.Items[0].Recipe.ID != "punctuation" {
		t.Fatalf("trimmed case-insensitive punctuation query: %+v", ownerPage)
	}
	page, _ := request("owner", "?q=HERB")
	if page.Total != 1 || page.Items[0].Recipe == nil || page.Items[0].Recipe.ID != "punctuation" {
		t.Fatalf("description query: %+v", page)
	}
	page, _ = request("owner", "?q=weekday")
	if page.Total != 1 || page.Items[0].Recipe == nil || page.Items[0].Recipe.ID != "punctuation" {
		t.Fatalf("tag query: %+v", page)
	}
	page, _ = request("viewer", "?q=noodles")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Recipe == nil || page.Items[0].Recipe.ID != "punctuation" || strings.Join(page.Items[0].Relationships, ",") != "shared,saved" {
		t.Fatalf("saved readable local match: %+v", page)
	}
	page, _ = request("viewer", "?q=orange")
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].RemoteRecipe == nil || page.Items[0].RemoteRecipe.Recipe.Title != "Remote Citrus Tart" {
		t.Fatalf("saved cached remote match: %+v", page)
	}
	page, _ = request("viewer", "?q=duplicate")
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("malformed cached remote row was searchable: %+v", page)
	}
	page, _ = request("owner", "?limit=1&offset=0&q=rice")
	if page.Total != 1 || page.Limit != 1 || page.Offset != 0 || len(page.Items) != 1 || page.Items[0].Recipe == nil || page.Items[0].Recipe.ID != "description-hit" {
		t.Fatalf("matching count before pagination: %+v", page)
	}
	page, _ = request("viewer", "?q=unavailable")
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("nonblank query included unavailable or unreadable references: %+v", page)
	}
	adminPage, _ := request("admin", "?q=citrus")
	if adminPage.Total != 0 || len(adminPage.Items) != 0 {
		t.Fatalf("admin query bypassed personal inclusion or recipe ACL: %+v", adminPage)
	}
	page, _ = request("viewer", "?q=%20%20")
	if page.Total != 6 || len(page.Items) != 6 {
		t.Fatalf("blank search should retain unavailable references and readable union: %+v", page)
	}
	list := doBookmarkRequest(t, h, "owner", http.MethodGet, "/api/recipes", "")
	if list.Code != http.StatusOK || list.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("actor recipe list cache policy: %d %s", list.Code, list.Header().Get("Cache-Control"))
	}
	oversized := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?q="+strings.Repeat("a", 201), "")
	if oversized.Code != http.StatusBadRequest || !strings.Contains(oversized.Body.String(), `"error":"invalid_query"`) {
		t.Fatalf("oversized query: %d %s", oversized.Code, oversized.Body)
	}
	byteBoundary := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?q="+url.QueryEscape(strings.Repeat("é", 100)), "")
	if byteBoundary.Code != http.StatusOK {
		t.Fatalf("200-byte query rejected: %d %s", byteBoundary.Code, byteBoundary.Body)
	}
	oversizedUTF8 := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?q="+url.QueryEscape(strings.Repeat("é", 101)), "")
	if oversizedUTF8.Code != http.StatusBadRequest || !strings.Contains(oversizedUTF8.Body.String(), `"error":"invalid_query"`) {
		t.Fatalf("201-byte UTF-8 query accepted: %d %s", oversizedUTF8.Code, oversizedUTF8.Body)
	}
}

func TestLibraryRecipesUsesCanonicalRemoteAvailabilityBeforeSearchAndCount(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	type malformedCache struct {
		id, title, body, updatedAt string
	}
	baseBody := `{"title":"%s","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	byteTitle := strings.Repeat("é", 101)
	fixtures := []malformedCache{
		{"3123456789abcdef0123456789abcdef", "Byte marker " + byteTitle, fmt.Sprintf(baseBody, "Byte marker "+byteTitle), "2026-11-03T12:00:00Z"},
		{"4123456789abcdef0123456789abcdef", "Timestamp marker", fmt.Sprintf(baseBody, "Timestamp marker"), "2026-11-03"},
		{"5123456789abcdef0123456789abcdef", "Oversize marker", fmt.Sprintf(baseBody, "Oversize marker") + strings.Repeat(" ", 1_500_001), "2026-11-03T12:00:00Z"},
	}
	for i, fixture := range fixtures {
		uri := testFederationOrigin + "/federation/recipes/" + fixture.id
		bookmarkID := fmt.Sprintf("malformed-%d", i)
		if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES(?,'viewer','remote',?,'2026-11-04')`, bookmarkID, uri); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,1,'Remote creator',?,0,?)`, uri, testFederationOrigin, fixture.id, fixture.body, fixture.updatedAt); err != nil {
			t.Fatal(err)
		}
		writeRemoteProjectionFixture(t, db, RemoteRecipeReadView{ObjectURI: uri, SourceOrigin: testFederationOrigin, SourceRecipeID: fixture.id, CreatorName: "Remote creator", Sequence: 1, UpdatedAt: fixture.updatedAt}, fixture.body)
	}
	h := apiHandler{db: db}
	blank := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes", "")
	var blankPage LibraryRecipePage
	if blank.Code != http.StatusOK || json.Unmarshal(blank.Body.Bytes(), &blankPage) != nil || blankPage.Total != 4 || len(blankPage.Items) != 4 {
		t.Fatalf("unfiltered malformed-reference page: %d %s", blank.Code, blank.Body)
	}
	remoteUnavailable := 0
	for _, item := range blankPage.Items {
		if item.TargetKind == "remote" && item.Availability == "unavailable" && item.RemoteRecipe == nil && item.Recipe == nil {
			remoteUnavailable++
		}
	}
	if remoteUnavailable != len(fixtures) || strings.Contains(blank.Body.String(), "marker") {
		t.Fatalf("malformed cache projection leaked or disappeared: %s", blank.Body)
	}
	for _, query := range []string{"Byte", "Timestamp", "Oversize"} {
		response := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?q="+url.QueryEscape(query), "")
		var page LibraryRecipePage
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 0 || len(page.Items) != 0 {
			t.Fatalf("malformed cache search %q: %d %s", query, response.Code, response.Body)
		}
	}
}

func TestLibraryRecipeSearchMatchesSQLiteASCIICaseFoldingForLocalAndRemote(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES('umlaut-local','owner','Owner','Äpfel','', '[]','[]',1,0,0,'[]','private','owner_only',1,'created','2026-11-04');
		INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	remoteID := "6123456789abcdef0123456789abcdef"
	remoteURI := testFederationOrigin + "/federation/recipes/" + remoteID
	remoteBody := `{"title":"Äpfel","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('umlaut-remote-bookmark','owner','remote',?,'2026-11-04')`, remoteURI); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,1,'Remote creator',?,0,'2026-11-03T12:00:00Z')`, remoteURI, testFederationOrigin, remoteID, remoteBody); err != nil {
		t.Fatal(err)
	}
	writeRemoteProjectionFixture(t, db, RemoteRecipeReadView{ObjectURI: remoteURI, SourceOrigin: testFederationOrigin, SourceRecipeID: remoteID, CreatorName: "Remote creator", Sequence: 1, UpdatedAt: "2026-11-03T12:00:00Z"}, remoteBody)
	h := apiHandler{db: db}
	for _, query := range []string{"äpfel"} {
		response := doBookmarkRequest(t, h, "owner", http.MethodGet, "/api/library/recipes?q="+url.QueryEscape(query), "")
		var page LibraryRecipePage
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 0 {
			t.Fatalf("non-ASCII lower-case query %q must follow SQLite lower(): %d %s", query, response.Code, response.Body)
		}
	}
	response := doBookmarkRequest(t, h, "owner", http.MethodGet, "/api/library/recipes?q="+url.QueryEscape("ÄPFEL"), "")
	var page LibraryRecipePage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 2 {
		t.Fatalf("ASCII-only case folding should match both local and remote entries: %d %s", response.Code, response.Body)
	}
	localMatch, remoteMatch := false, false
	for _, item := range page.Items {
		localMatch = localMatch || (item.Recipe != nil && item.Recipe.ID == "umlaut-local")
		remoteMatch = remoteMatch || (item.RemoteRecipe != nil && item.RemoteRecipe.Recipe.Title == "Äpfel")
	}
	if !localMatch || !remoteMatch {
		t.Fatalf("local and remote case-fold results diverged: %s", response.Body)
	}
}

func TestLibraryRecipesPagesCombinedProjectedRemoteAndLocalEntriesInSQLOrder(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,version,created_at,updated_at) VALUES
		('page-local-new','other','Other','Local new','[]','[]','[]','private',1,'created','2026-11-05'),
		('page-local-old','other','Other','Local old','[]','[]','[]','private',1,'created','2026-11-03')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	uri := testFederationOrigin + "/federation/recipes/" + testFederationID
	body := `{"title":"Remote","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,1,'Remote creator',?,0,'2026-11-04T12:00:00Z')`, uri, testFederationOrigin, testFederationID, body); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('remote-page','other','remote',?,'2026-11-02')`, uri); err != nil {
		t.Fatal(err)
	}
	writeRemoteProjectionFixture(t, db, RemoteRecipeReadView{ObjectURI: uri, SourceOrigin: testFederationOrigin, SourceRecipeID: testFederationID, CreatorName: "Remote creator", Sequence: 1, UpdatedAt: "2026-11-04T12:00:00Z"}, body)
	h := apiHandler{db: db}
	want := []struct {
		offset     int
		kind       string
		localID    string
		bookmarkID string
	}{
		{0, "local", "page-local-new", ""},
		{1, "remote", "", "remote-page"},
		{2, "local", "page-local-old", ""},
	}
	for _, expected := range want {
		response := doBookmarkRequest(t, h, "other", http.MethodGet, fmt.Sprintf("/api/library/recipes?limit=1&offset=%d", expected.offset), "")
		var page LibraryRecipePage
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 3 || len(page.Items) != 1 || page.Offset != expected.offset || page.Items[0].Availability != "available" || page.Items[0].TargetKind != expected.kind || page.Items[0].BookmarkID != expected.bookmarkID {
			t.Fatalf("combined ordered page offset %d: %d %s", expected.offset, response.Code, response.Body)
		}
		if expected.kind == "local" && (page.Items[0].Recipe == nil || page.Items[0].Recipe.ID != expected.localID) {
			t.Fatalf("combined local page offset %d: %s", expected.offset, response.Body)
		}
	}
}

func TestLibraryRemoteRecipeEntriesHonorsContextCancellation(t *testing.T) {
	db := testDatabase(t)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := libraryCanonicalRemoteRecipeEntries(ctx, tx, "viewer", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled batch walk error=%v, want context.Canceled", err)
	}
}

const libraryRecipeSearchBenchmarkBody = `{"title":"Weeknight tomato soup","description":"Roasted tomatoes with herbs and a little cream.","ingredients":[{"name":"tomatoes","quantity":"500 g"},{"name":"onion","quantity":"1"}],"steps":[{"text":"Roast the tomatoes until soft."},{"text":"Blend with onion and simmer."}],"servings":4,"prep_time_minutes":15,"cook_time_minutes":30,"tags":["soup","weeknight","vegetarian"]}`

func seedLibraryRecipeSearchBenchmark(b *testing.B, count int, withProjection bool) (*sql.DB, int) {
	b.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now');
		INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		db.Close()
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		b.Fatal(err)
	}
	bookmarkInsert, err := tx.Prepare(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES(?,'viewer','remote',?,'2026-11-04')`)
	if err != nil {
		tx.Rollback()
		db.Close()
		b.Fatal(err)
	}
	cacheInsert, err := tx.Prepare(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,1,'Remote creator',?,0,'2026-11-03T12:00:00Z')`)
	if err != nil {
		bookmarkInsert.Close()
		tx.Rollback()
		db.Close()
		b.Fatal(err)
	}
	datasetBytes := 0
	for index := 0; index < count; index++ {
		recipeID := fmt.Sprintf("%032x", index+1)
		uri := testFederationOrigin + "/federation/recipes/" + recipeID
		bookmarkID := fmt.Sprintf("bookmark-%05d", index)
		if _, err := bookmarkInsert.Exec(bookmarkID, uri); err != nil {
			b.Fatal(err)
		}
		if _, err := cacheInsert.Exec(uri, testFederationOrigin, recipeID, libraryRecipeSearchBenchmarkBody); err != nil {
			b.Fatal(err)
		}
		if withProjection {
			source := RemoteRecipeReadView{ObjectURI: uri, SourceOrigin: testFederationOrigin, SourceRecipeID: recipeID, CreatorName: "Remote creator", Sequence: 1, UpdatedAt: "2026-11-03T12:00:00Z"}
			if err := writeRemoteRecipeSearchProjection(context.Background(), tx, source, libraryRecipeSearchBenchmarkBody); err != nil {
				b.Fatal(err)
			}
		}
		datasetBytes += len(bookmarkID) + len(uri) + len(libraryRecipeSearchBenchmarkBody) + len("2026-11-04") + len("2026-11-03T12:00:00Z")
	}
	if err := bookmarkInsert.Close(); err != nil {
		b.Fatal(err)
	}
	if err := cacheInsert.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		b.Fatal(err)
	}
	return db, datasetBytes
}

// Run with -benchtime=1x for a capped 100/1k/10k steady-state comparison.
func BenchmarkLibraryRecipeSearchRemoteScale(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			db, datasetBytes := seedLibraryRecipeSearchBenchmark(b, count, true)
			defer db.Close()
			h := apiHandler{db: db}
			b.ReportAllocs()
			b.SetBytes(int64(datasetBytes))
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				response := httptest.NewRecorder()
				h.ServeHTTP(response, requestForUser(http.MethodGet, "/api/library/recipes", "viewer"))
				if response.Code != http.StatusOK {
					b.Fatalf("Library request status=%d body=%s", response.Code, response.Body)
				}
			}
			b.ReportMetric(float64(datasetBytes), "fixture-B")
			b.ReportMetric(float64(count), "bookmarks/op")
		})
	}
}

// Run with -benchtime=1x for capped cold-backfill cost at each dataset size.
func BenchmarkLibraryRecipeSearchRemoteColdBackfill(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			db, datasetBytes := seedLibraryRecipeSearchBenchmark(b, count, false)
			defer db.Close()
			b.ReportAllocs()
			b.SetBytes(int64(datasetBytes))
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if iteration > 0 {
					b.StopTimer()
					if _, err := db.Exec(`DELETE FROM federation_remote_recipe_search`); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
				for {
					processed, err := backfillLibraryRemoteSearchPass(context.Background(), db)
					if err != nil {
						b.Fatal(err)
					}
					if !processed {
						break
					}
				}
			}
			b.ReportMetric(float64(datasetBytes), "fixture-B")
			b.ReportMetric(float64(count), "bookmarks/op")
		})
	}
}

func TestLibraryRecipesUnionsCanonicalRelationshipsAndPagesUniqueEntries(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES
		('public-recipe','owner','Owner','Public recipe','','[]','[]',1,0,0,'[]','public','owner_only',1,'created','2026-11-01'),
		('owned-cookbook-readable','owner','Owner','Readable linked recipe','','[]','[]',1,0,0,'[]','public','owner_only',1,'created','2026-11-03'),
		('owned-cookbook-private','other','Other','Private linked recipe','','[]','[]',1,0,0,'[]','private','owner_only',1,'created','2026-11-03');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('public-recipe','published','off',0,'now'),('owned-cookbook-readable','published','off',0,'now');
		INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('book_owner','book-owner@example.test','Book owner',0,'now');
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('owner-book','book_owner','Owned book','','private','now','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('owner-book','owned-cookbook-readable','book_owner','now'),('owner-book','owned-cookbook-private','book_owner','now');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('book_owner','book-owner@example.test','Book owner','now')`); err == nil {
		t.Fatal("duplicate user fixture unexpectedly succeeded")
	}
	// `book_owner` was inserted by the shared insert above after the cookbook row; SQLite permits that order.
	h := apiHandler{db: db}
	viewer := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"local_recipe_id":"public-recipe"}}`)
	if viewer.Code != http.StatusCreated {
		t.Fatalf("save public: %d %s", viewer.Code, viewer.Body)
	}
	sharedSave := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"local_recipe_id":"shared"}}`)
	if sharedSave.Code != http.StatusCreated {
		t.Fatalf("save shared: %d %s", sharedSave.Code, sharedSave.Body)
	}
	sharedPageResponse := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes", "")
	var sharedPage LibraryRecipePage
	if sharedPageResponse.Code != http.StatusOK || json.Unmarshal(sharedPageResponse.Body.Bytes(), &sharedPage) != nil || sharedPage.Total != 2 {
		t.Fatalf("owned/member/saved union before revocation: %d %s", sharedPageResponse.Code, sharedPageResponse.Body)
	}
	var mergedShared bool
	for _, item := range sharedPage.Items {
		if item.Recipe != nil && item.Recipe.ID == "shared" {
			mergedShared = len(item.Relationships) == 2 && item.Relationships[0] == "shared" && item.Relationships[1] == "saved" && item.BookmarkID != "" && item.MembershipRole == "viewer" && len(item.CookbookContexts) == 1 && item.CookbookContexts[0].ID == "book" && item.CookbookContexts[0].MembershipRole == "viewer"
		}
	}
	if !mergedShared {
		t.Fatalf("same canonical recipe was not merged with current relationships: %s", sharedPageResponse.Body)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	foreign := strings.Repeat("f", 32)
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,local_instance_id,local_recipe_id,created_at) VALUES
		('foreign-local','viewer','local',?,'private','2026-11-04'),
		('deleted-local','viewer','local',?,'deleted-recipe','2026-11-03')`, foreign, instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,version,created_at,updated_at,deleted_at) VALUES('deleted-recipe','owner','Owner','Deleted','[]','[]','[]','private',1,'created','updated','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id='book' AND user_id='viewer'`); err != nil {
		t.Fatal(err)
	}
	response := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes", "")
	var page LibraryRecipePage
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(response.Body.Bytes(), &page) != nil {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body)
	}
	if page.Total != 4 || len(page.Items) != 4 || page.Limit != 24 || page.Offset != 0 {
		t.Fatalf("saved readable/unavailable union: %+v", page)
	}
	available, unavailable := 0, 0
	for _, item := range page.Items {
		if item.Availability == "available" {
			available++
			if item.Recipe == nil || item.RemoteRecipe != nil || item.Recipe.ID != "public-recipe" || len(item.Relationships) != 1 || item.Relationships[0] != "saved" || item.BookmarkID == "" {
				t.Fatalf("canonical public saved item: %+v", item)
			}
		} else {
			unavailable++
			if item.Recipe != nil || item.RemoteRecipe != nil || item.BookmarkID == "" || item.CreatedAt == "" || item.TargetKind != "local" || len(item.Relationships) != 1 || item.Relationships[0] != "saved" {
				t.Fatalf("unavailable reference projection: %+v", item)
			}
		}
	}
	if available != 1 || unavailable != 3 || containsAny(response.Body.String(), "private-recipe", `"title":"Deleted"`) {
		t.Fatalf("wrong visibility/redaction: %s", response.Body)
	}
	bookOwner := doBookmarkRequest(t, h, "book_owner", http.MethodGet, "/api/library/recipes", "")
	var ownerPage LibraryRecipePage
	if bookOwner.Code != http.StatusOK || json.Unmarshal(bookOwner.Body.Bytes(), &ownerPage) != nil || ownerPage.Total != 1 || len(ownerPage.Items) != 1 {
		t.Fatalf("owned-cookbook candidates: %d %s", bookOwner.Code, bookOwner.Body)
	}
	linked := ownerPage.Items[0]
	if linked.Recipe == nil || linked.Recipe.ID != "owned-cookbook-readable" || linked.InOwnedCookbook != true || linked.MembershipRole != "" || len(linked.CookbookContexts) != 1 || linked.CookbookContexts[0].ID != "owner-book" || linked.CookbookContexts[0].MembershipRole != "" || len(linked.Relationships) != 1 || linked.Relationships[0] != "in_owned_cookbook" {
		t.Fatalf("owned cookbook relation synthesized membership or leaked denied private recipe: %+v", linked)
	}
	linkedSearch := doBookmarkRequest(t, h, "book_owner", http.MethodGet, "/api/library/recipes?q=linked", "")
	if linkedSearch.Code != http.StatusOK || !strings.Contains(linkedSearch.Body.String(), `"id":"owned-cookbook-readable"`) || strings.Contains(linkedSearch.Body.String(), `"id":"owned-cookbook-private"`) {
		t.Fatalf("owned cookbook search bypassed canonical ACL: %d %s", linkedSearch.Code, linkedSearch.Body)
	}
	first := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?limit=1&offset=0", "")
	var firstPage LibraryRecipePage
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || firstPage.Total != 4 || firstPage.Limit != 1 || firstPage.Offset != 0 || len(firstPage.Items) != 1 {
		t.Fatalf("first page: %d %s", first.Code, first.Body)
	}
	second := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?limit=1&offset=1", "")
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &firstPage) != nil || firstPage.Total != 4 || firstPage.Limit != 1 || firstPage.Offset != 1 || len(firstPage.Items) != 1 || firstPage.Items[0].BookmarkID == page.Items[0].BookmarkID {
		t.Fatalf("second page: %d %s", second.Code, second.Body)
	}
	if denied := doBookmarkRequest(t, h, "admin", http.MethodGet, "/api/library/recipes", ""); denied.Code != http.StatusOK || strings.Contains(denied.Body.String(), "private-recipe") || strings.Contains(denied.Body.String(), "public-recipe") {
		t.Fatalf("admin read bypass: %d %s", denied.Code, denied.Body)
	}
	if denied := doBookmarkRequest(t, h, "", http.MethodGet, "/api/library/recipes", ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", denied.Code)
	}
	if invalid := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?limit=101", ""); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid page status=%d", invalid.Code)
	}
	maximum := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?limit=100", "")
	if maximum.Code != http.StatusOK || json.Unmarshal(maximum.Body.Bytes(), &page) != nil || page.Limit != 100 || page.Offset != 0 || page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("maximum page: %d %s", maximum.Code, maximum.Body)
	}
}

func TestLibraryRecipesIncludesRemoteOnlyFromValidatedLocalSnapshot(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	uri := testFederationOrigin + "/federation/recipes/" + testFederationID
	body := `{"title":"Cached remote dish","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, uri, testFederationOrigin, testFederationID, 1, "Remote creator", body, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	writeRemoteProjectionFixture(t, db, RemoteRecipeReadView{ObjectURI: uri, SourceOrigin: testFederationOrigin, SourceRecipeID: testFederationID, CreatorName: "Remote creator", Sequence: 1, UpdatedAt: "2026-11-03T12:00:00Z"}, body)
	if got := doBookmarkRequest(t, apiHandler{db: db}, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"remote_object_uri":"`+uri+`"}}`); got.Code != http.StatusCreated {
		t.Fatalf("save remote %d %s", got.Code, got.Body)
	}
	response := doBookmarkRequest(t, apiHandler{db: db}, "viewer", http.MethodGet, "/api/library/recipes", "")
	var page LibraryRecipePage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("cached remote page: %d %s", response.Code, response.Body)
	}
	var foundRemote bool
	for _, item := range page.Items {
		if item.TargetKind == "remote" && item.RemoteRecipe != nil && item.RemoteRecipe.ObjectURI == uri && item.RemoteRecipe.Recipe.Title == "Cached remote dish" && item.Recipe == nil {
			foundRemote = true
		}
	}
	if !foundRemote {
		t.Fatalf("validated remote snapshot missing: %s", response.Body)
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET source_recipe_id=? WHERE object_id=?`, "1123456789abcdef0123456789abcdef", uri); err != nil {
		t.Fatal(err)
	}
	unbound := doBookmarkRequest(t, apiHandler{db: db}, "viewer", http.MethodGet, "/api/library/recipes", "")
	page = LibraryRecipePage{}
	if unbound.Code != http.StatusOK || json.Unmarshal(unbound.Body.Bytes(), &page) != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("mismatched cache page: %d %s", unbound.Code, unbound.Body)
	}
	var foundUnavailable bool
	for _, item := range page.Items {
		if item.TargetKind == "remote" && item.Availability == "unavailable" && item.RemoteRecipe == nil && item.Recipe == nil {
			foundUnavailable = true
		}
	}
	if !foundUnavailable {
		t.Fatalf("mismatched cache binding was trusted: %s", unbound.Body)
	}
}

func TestLibraryRecipesSurfacesBookmarkStorageErrors(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	h := apiHandler{db: db}
	if _, err := db.Exec(`DROP TABLE recipe_bookmarks`); err != nil {
		t.Fatal(err)
	}
	response := doBookmarkRequest(t, h, "owner", http.MethodGet, "/api/library/recipes", "")
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"error":"storage_error"`) {
		t.Fatalf("storage error status=%d body=%s", response.Code, response.Body)
	}
}

func TestLibraryRecipesUsesStableCanonicalIDOrderingForTimestampTies(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('tie-owner','tie@example.test','Tie owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,version,created_at,updated_at) VALUES
		('tie-b','tie-owner','Tie owner','B','[]','[]','[]','private',1,'created','same'),
		('tie-a','tie-owner','Tie owner','A','[]','[]','[]','private',1,'created','same')`); err != nil {
		t.Fatal(err)
	}
	response := doBookmarkRequest(t, apiHandler{db: db}, "tie-owner", http.MethodGet, "/api/library/recipes?limit=1", "")
	var page LibraryRecipePage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 2 || page.Limit != 1 || len(page.Items) != 1 || page.Items[0].Recipe == nil || page.Items[0].Recipe.ID != "tie-a" {
		t.Fatalf("timestamp tie order: %d %s", response.Code, response.Body)
	}
}
