package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCreatorProfileListsApplyVisibilityAndResourceACLWithoutRoleBypass(t *testing.T) {
	db := testDatabase(t)
	ownerProfile := strings.Repeat("a", 32)
	privateProfile := strings.Repeat("b", 32)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('reader','reader@example.test','Reader',0,'now'),
		('admin','admin@example.test','Admin',1,'now'),('instance-owner','instance@example.test','Instance owner',0,'now')`)
	exec(`UPDATE instance_authority SET owner_user_id='instance-owner',owner_claimed_at='now',generation=1 WHERE singleton_id=1`)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'owner','Shared name','instance','now','now')`, ownerProfile)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'reader','Secret private name','private','now','now')`, privateProfile)
	exec(`INSERT INTO recipes(id,owner_id,creator_name,title,visibility,created_at,updated_at) VALUES
		('private-recipe','owner','','Owner only','private','now','now'),
		('member-recipe','owner','','Household','private','now','now'),
		('private-profile-recipe','reader','','Private profile resource','private','now','now')`)
	exec(`INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('shared-book','owner','Shared book','private','now','now')`)
	exec(`INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('shared-book','reader','viewer','now')`)
	exec(`INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('shared-book','member-recipe','owner','now')`)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('11111111111111111111111111111111','private-recipe','local',?,'creator','self_asserted_at_creation','now')`, ownerProfile)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('22222222222222222222222222222222','member-recipe','local',?,'creator','self_asserted_at_creation','now')`, ownerProfile)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('33333333333333333333333333333333','private-profile-recipe','local',?,'creator','self_asserted_at_creation','now')`, privateProfile)
	exec(`INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at) VALUES('44444444444444444444444444444444','shared-book','local',?,'creator','local_record_creation','now')`, ownerProfile)
	h := apiHandler{db: db}
	readerRecipes := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+ownerProfile+"/recipes", "reader", nil)
	if readerRecipes.Code != http.StatusOK || !strings.Contains(readerRecipes.Body.String(), `"items":[`) || !strings.Contains(readerRecipes.Body.String(), `"role":"creator"`) {
		t.Fatalf("member recipe profile list status=%d body=%s", readerRecipes.Code, readerRecipes.Body)
	}
	var recipeList creatorProfileRecipeList
	if err := json.Unmarshal(readerRecipes.Body.Bytes(), &recipeList); err != nil {
		t.Fatal(err)
	}
	if recipeList.Profile.ID != ownerProfile || recipeList.Profile.PublicName != "Shared name" || len(recipeList.Items) != 1 || recipeList.Items[0].Recipe.ID != "member-recipe" || recipeList.Items[0].Recipe.CanEdit || recipeList.Items[0].Recipe.OwnerID != "" {
		t.Fatalf("creator recipe list escaped ACL or DTO rules: %+v", recipeList)
	}
	bookList := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+ownerProfile+"/cookbooks", "reader", nil)
	if bookList.Code != http.StatusOK || strings.Contains(bookList.Body.String(), `"owner_id"`) {
		t.Fatalf("creator cookbook projection status=%d body=%s", bookList.Code, bookList.Body)
	}
	var cookbookList creatorProfileCookbookList
	if err := json.Unmarshal(bookList.Body.Bytes(), &cookbookList); err != nil {
		t.Fatal(err)
	}
	if len(cookbookList.Items) != 1 || cookbookList.Items[0].Cookbook.ID != "shared-book" || cookbookList.Items[0].Role != "creator" {
		t.Fatalf("unexpected creator cookbook list: %+v", cookbookList)
	}
	for _, userID := range []string{"admin", "instance-owner"} {
		empty := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+ownerProfile+"/recipes", userID, nil)
		if empty.Code != http.StatusOK {
			t.Fatalf("%s cannot read instance profile: status=%d body=%s", userID, empty.Code, empty.Body)
		}
		var page creatorProfileRecipeList
		if err := json.Unmarshal(empty.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("%s role bypassed recipe ACL: %+v", userID, page.Items)
		}
		private := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+privateProfile+"/recipes", userID, nil)
		if private.Code != http.StatusNotFound || private.Body.String() != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("%s role bypassed private profile: status=%d body=%s", userID, private.Code, private.Body)
		}
	}
	missing := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+strings.Repeat("c", 32)+"/recipes", "reader", nil)
	if missing.Code != http.StatusNotFound || missing.Body.String() != "{\"error\":\"not_found\"}\n" {
		t.Fatalf("missing profile list was not generic 404: status=%d body=%s", missing.Code, missing.Body)
	}
}

func TestVisibleCreatorProfileCanReturnNoReadableResources(t *testing.T) {
	db := testDatabase(t)
	creatorID := strings.Repeat("d", 32)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('reader','reader@example.test','Reader','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'owner','Household','instance','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,visibility,created_at,updated_at) VALUES('private-recipe','owner','','Private','private','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('55555555555555555555555555555555','private-recipe','local',?,'creator','self_asserted_at_creation','now')`, creatorID, creatorID); err != nil {
		t.Fatal(err)
	}
	response := callCreatorProfileAPI(t, apiHandler{db: db}, http.MethodGet, "/api/creator-profiles/"+creatorID+"/recipes", "reader", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("visible profile without resource access status=%d body=%s", response.Code, response.Body)
	}
}
