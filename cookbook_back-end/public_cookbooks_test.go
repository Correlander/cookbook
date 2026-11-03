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
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES
		('public-recipe','published','off',0,'now'),('private-recipe','unpublished','off',0,'now'),('unlisted-recipe','unpublished','off',0,'now');
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

func TestPublicCookbookMetadataByIDUsesAnonymousProjectionAndPublicCount(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES
		('public-book','owner','Public book','A public collection','public','created','updated'),
		('private-book','owner','Private book','Secret collection','private','created-private','updated-private');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES
		('eligible','owner','Owner','Eligible','[]','[]','[]','public','owner_only','created','updated'),
		('unpublished','owner','Owner','Unpublished','[]','[]','[]','public','owner_only','created','updated'),
		('private','owner','Owner','Private','[]','[]','[]','private','owner_only','created','updated'),
		('deleted','owner','Owner','Deleted','[]','[]','[]','public','owner_only','created','updated');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES
		('eligible','published','off',0,'now'),('unpublished','unpublished','off',0,'now'),('private','published','off',0,'now'),('deleted','published','off',0,'now');
		UPDATE recipes SET deleted_at='deleted' WHERE id='deleted';
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES
		('public-book','eligible','owner','now'),('public-book','unpublished','owner','now'),('public-book','private','owner','now'),('public-book','deleted','owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks", nil))
	var listed []publicCookbook
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].RecipeCount != 1 {
		t.Fatalf("public cookbook list status=%d body=%s", list.Code, list.Body)
	}

	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/public-book", nil))
	var item publicCookbook
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &item) != nil {
		t.Fatalf("public cookbook metadata status=%d body=%s", detail.Code, detail.Body)
	}
	if item != listed[0] || item.ID != "public-book" || item.Title != "Public book" || item.Description != "A public collection" || item.RecipeCount != 1 || item.CreatedAt != "created" || item.UpdatedAt != "updated" {
		t.Fatalf("detail projection differs from public list: detail=%+v list=%+v", item, listed[0])
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(detail.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 || containsJSONField(detail.Body.String(), "owner_id") || containsJSONField(detail.Body.String(), "email") || containsJSONField(detail.Body.String(), "creator_name") || containsJSONField(detail.Body.String(), "member") {
		t.Fatalf("public cookbook metadata exposed fields outside its anonymous projection: %s", detail.Body)
	}

	var genericNotFound string
	for _, id := range []string{"private-book", "missing-book"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/"+id, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s metadata status=%d body=%s", id, response.Code, response.Body)
		}
		if genericNotFound == "" {
			genericNotFound = response.Body.String()
		} else if response.Body.String() != genericNotFound {
			t.Fatalf("private and missing cookbook responses differ: %q / %q", genericNotFound, response.Body.String())
		}
	}
	privateInstance, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := privateInstance.RowsAffected(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/public-book", nil))
	if response.Code != http.StatusNotFound || response.Body.String() != genericNotFound {
		t.Fatalf("private-instance cookbook response=%d %q, want generic 404 %q", response.Code, response.Body.String(), genericNotFound)
	}

	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodPost, "/api/public/cookbooks/public-book", nil))
	if wrongMethod.Code != http.StatusForbidden || wrongMethod.Body.String() != "{\"error\":\"csrf_origin_required\"}\n" {
		t.Fatalf("public cookbook metadata POST status=%d body=%s", wrongMethod.Code, wrongMethod.Body)
	}
}

func TestPublicCookbookMetadataByIDStorageError(t *testing.T) {
	db := testDatabase(t)
	handler := handlerWithAuth(db, testAuth(db))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/public-book", nil))
	if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"error\":\"storage_error\"}\n" {
		t.Fatalf("closed database response=%d body=%s", response.Code, response.Body)
	}
}

func TestPublicCookbookRecipeQueryRechecksVisibilityAfterAuthorization(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Public book','public','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES('soup','owner','Owner','Soup','[]','[]','[]','public','owner_only','now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('soup','published','off',0,'now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','soup','owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	var public int
	if err := db.QueryRow(`SELECT s.public_instance FROM cookbooks c JOIN instance_settings s ON s.id=1 WHERE c.id='book' AND c.visibility='public'`).Scan(&public); err != nil || public != 1 {
		t.Fatalf("public authorization check = %d, %v", public, err)
	}
	if _, err := db.Exec(`UPDATE cookbooks SET visibility='private' WHERE id='book'`); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(publicCookbookRecipeRowsQuery+`r.id LIMIT ? OFFSET ?`, "book", "", "", "", "", 24, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("recipe query returned a cookbook association after the cookbook became private")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicCookbookEndpointsValidateMethodsPaginationAndSearch(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Soup book','public','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES('soup','owner','Owner','Tomato soup','','[]','[]','[]','public','owner_only','now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('soup','published','off',0,'now');
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

func TestPublicCookbookRecipeTagFilterAndSortPreserveVisibility(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Desserts','public','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES
		('z-pie','owner','Owner','Zesty pie','[]','[]','["dessert"]','public','owner_only','now','now'),
		('a-tart','owner','Owner','Apple tart','[]','[]','["dessert"]','public','owner_only','now','now'),
		('hidden','owner','Owner','Hidden dessert','[]','[]','["dessert"]','private','owner_only','now','now'),
		('almost','owner','Owner','Dinner pie','[]','[]','["desserts"]','public','owner_only','now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES
		('z-pie','published','off',0,'now'),('a-tart','published','off',0,'now'),('hidden','unpublished','off',0,'now'),('almost','published','off',0,'now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) SELECT 'book',id,'owner','now' FROM recipes`)
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/book/recipes?tag=dessert&sort=title", nil))
	var items []recipe
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil || len(items) != 2 || items[0].ID != "a-tart" || items[1].ID != "z-pie" {
		t.Fatalf("filtered recipes status=%d body=%s", response.Code, response.Body)
	}
	for _, query := range []string{"sort=unknown", "tag=" + strings.Repeat("x", 41)} {
		invalid := httptest.NewRecorder()
		handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/public/cookbooks/book/recipes?"+query, nil))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid recipe filter %q status=%d", query, invalid.Code)
		}
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
