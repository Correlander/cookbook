package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicCookbooksExposeOnlyPublicResources(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES
		('public-book','owner','Public book','A public collection','public','now','2026-01-02'),
		('private-book','owner','Private book','Keep this private','private','now','2026-01-03');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES
		('public-recipe','owner','Owner','Public soup','','[]','[]','[]','public','owner_only','now','2026-01-02'),
		('private-recipe','owner','Owner','Secret soup','','[]','[]','[]','private','owner_only','now','2026-01-03'),
		('unlisted-recipe','owner','Owner','Unlisted soup','','[]','[]','[]','unlisted','owner_only','now','2026-01-01');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES
		('public-book','public-recipe','owner','now'),('public-book','private-recipe','owner','now'),('public-book','unlisted-recipe','owner','now'),('private-book','public-recipe','owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))

	privateInstance := httptest.NewRecorder()
	handler.ServeHTTP(privateInstance, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks", nil))
	if privateInstance.Code != http.StatusOK || privateInstance.Body.String() != "[]\n" {
		t.Fatalf("private instance cookbook list = %d %s", privateInstance.Code, privateInstance.Body)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks?q=PUBLIC&limit=1&offset=0", nil))
	var books []publicCookbook
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &books) != nil || len(books) != 1 {
		t.Fatalf("public cookbook list = %d %s", list.Code, list.Body)
	}
	if books[0].ID != "public-book" || books[0].RecipeCount != 1 || books[0].Title != "Public book" {
		t.Fatalf("public cookbook list exposed wrong values: %+v", books[0])
	}
	if containsJSONField(list.Body.String(), "owner_id") || containsJSONField(list.Body.String(), "email") {
		t.Fatalf("public cookbook list leaked owner data: %s", list.Body)
	}

	recipesResponse := httptest.NewRecorder()
	handler.ServeHTTP(recipesResponse, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/public-book/recipes?limit=10", nil))
	var recipes []recipe
	if recipesResponse.Code != http.StatusOK || json.Unmarshal(recipesResponse.Body.Bytes(), &recipes) != nil || len(recipes) != 1 {
		t.Fatalf("public cookbook recipes = %d %s", recipesResponse.Code, recipesResponse.Body)
	}
	if recipes[0].ID != "public-recipe" || recipes[0].CreatorName != "Owner" || recipes[0].OwnerID != "" || len(recipes[0].CookbookIDs) != 0 {
		t.Fatalf("public recipe response included wrong or private fields: %+v", recipes[0])
	}
	if containsJSONField(recipesResponse.Body.String(), "owner_id") || containsJSONField(recipesResponse.Body.String(), "cookbook_ids") || containsJSONField(recipesResponse.Body.String(), "edit_policy") {
		t.Fatalf("public recipe response exposed internal fields: %s", recipesResponse.Body)
	}

	privateRecipes := httptest.NewRecorder()
	handler.ServeHTTP(privateRecipes, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/private-book/recipes", nil))
	if privateRecipes.Code != http.StatusNotFound {
		t.Fatalf("private cookbook detail status=%d, want 404", privateRecipes.Code)
	}
}

func TestPublicCookbookEndpointsValidateMethodsPaginationAndSearch(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Soup book','public','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES('soup','owner','Owner','Tomato soup','','[]','[]','[]','public','owner_only','now','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','soup','owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	badPage := httptest.NewRecorder()
	handler.ServeHTTP(badPage, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks?limit=101", nil))
	if badPage.Code != http.StatusBadRequest {
		t.Fatalf("invalid cookbook page status=%d, want 400", badPage.Code)
	}
	badQuery := httptest.NewRecorder()
	handler.ServeHTTP(badQuery, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks?q="+strings.Repeat("a", 201), nil))
	if badQuery.Code != http.StatusBadRequest {
		t.Fatalf("invalid cookbook search status=%d, want 400", badQuery.Code)
	}
	search := httptest.NewRecorder()
	handler.ServeHTTP(search, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/book/recipes?q=tomato", nil))
	var recipes []recipe
	if search.Code != http.StatusOK || json.Unmarshal(search.Body.Bytes(), &recipes) != nil || len(recipes) != 1 || recipes[0].ID != "soup" {
		t.Fatalf("cookbook recipe search = %d %s", search.Code, search.Body)
	}
	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/api/public/cookbooks", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("public cookbook write status=%d, want 405", wrongMethod.Code)
	}
}

func containsJSONField(body, field string) bool {
	var value any
	if json.Unmarshal([]byte(body), &value) != nil {
		return false
	}
	return fieldPresent(value, field)
}

func fieldPresent(value any, field string) bool {
	switch item := value.(type) {
	case map[string]any:
		if _, ok := item[field]; ok {
			return true
		}
		for _, child := range item {
			if fieldPresent(child, field) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if fieldPresent(child, field) {
				return true
			}
		}
	}
	return false
}
