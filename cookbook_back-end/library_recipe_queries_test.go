package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

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
