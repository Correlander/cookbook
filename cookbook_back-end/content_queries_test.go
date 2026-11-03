package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func insertRecipeQueryFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
			('owner','owner@example.test','Recipe Owner',0,'now'),
			('viewer','viewer@example.test','Cookbook Viewer',0,'now'),
			('editor','editor@example.test','Cookbook Editor',0,'now'),
			('admin','admin@example.test','Instance Admin',1,'now');
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at)
			VALUES('book','owner','Family book','','private','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at)
			VALUES('private-recipe','owner','Grandma','Soup','Warm soup','[{"name":"water"}]','[{"text":"Boil"}]',2,5,10,'["family"]','private','cookbook_editors',1,'created','updated');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','private-recipe','owner','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES
			('book','viewer','viewer','now'),('book','editor','editor','now');`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadRecipeDetailUsesLocalAccountAndRecipeACL(t *testing.T) {
	db := testDatabase(t)
	insertRecipeQueryFixture(t, db)

	for _, test := range []struct {
		name       string
		accountID  string
		wantEdit   bool
		wantDelete bool
	}{
		{name: "owner", accountID: "owner", wantEdit: true, wantDelete: true},
		{name: "viewer", accountID: "viewer"},
		{name: "editor", accountID: "editor", wantEdit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := readRecipeDetail(context.Background(), db, ContentActor{AccountID: test.accountID}, "private-recipe")
			if err != nil {
				t.Fatalf("readRecipeDetail() error = %v", err)
			}
			if got.ID != "private-recipe" || got.OwnerID != "owner" || got.CreatorName != "Grandma" || got.Version != 1 {
				t.Fatalf("recipe identity/history projection = %+v", got)
			}
			if got.CanEdit != test.wantEdit || got.CanDelete != test.wantDelete {
				t.Fatalf("capabilities edit=%v delete=%v, want %v/%v", got.CanEdit, got.CanDelete, test.wantEdit, test.wantDelete)
			}
			if test.accountID == "viewer" || test.accountID == "editor" {
				if len(got.CookbookIDs) != 1 || got.CookbookIDs[0] != "book" {
					t.Fatalf("readable cookbook projection = %#v", got.CookbookIDs)
				}
			}
		})
	}

	for _, test := range []struct {
		name      string
		accountID string
	}{
		{name: "anonymous", accountID: ""},
		{name: "instance admin", accountID: "admin"},
		{name: "unrelated account", accountID: "outsider"},
	} {
		t.Run(test.name+" denied", func(t *testing.T) {
			_, err := readRecipeDetail(context.Background(), db, ContentActor{AccountID: test.accountID}, "private-recipe")
			if !errors.Is(err, errRecipeDetailDenied) {
				t.Fatalf("readRecipeDetail() error = %v, want access denied", err)
			}
		})
	}

	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1; UPDATE recipes SET visibility='public' WHERE id='private-recipe'; INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('private-recipe','published','off',0,'now')`); err != nil {
		t.Fatal(err)
	}
	public, err := readRecipeDetail(context.Background(), db, ContentActor{}, "private-recipe")
	if err != nil || public.CanEdit || public.CanDelete {
		t.Fatalf("anonymous public read = %+v, err=%v", public, err)
	}
}

func TestReadRecipeDetailReportsStorageFailure(t *testing.T) {
	db := testDatabase(t)
	insertRecipeQueryFixture(t, db)
	if _, err := db.Exec(`DROP TABLE cookbook_memberships`); err != nil {
		t.Fatal(err)
	}
	_, err := readRecipeDetail(context.Background(), db, ContentActor{AccountID: "owner"}, "private-recipe")
	if err == nil || errors.Is(err, errRecipeDetailNotFound) || errors.Is(err, errRecipeDetailDenied) {
		t.Fatalf("readRecipeDetail() error = %v, want storage failure", err)
	}
}

func TestRecipeDetailGETAndReadAfterWriteUseTypedQuery(t *testing.T) {
	db := testDatabase(t)
	insertRecipeQueryFixture(t, db)
	h := apiHandler{db: db}

	get := httptest.NewRecorder()
	h.ServeHTTP(get, requestForUser(http.MethodGet, "/api/recipes/private-recipe", "editor"))
	var detail recipe
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &detail) != nil || detail.CreatorName != "Grandma" || !detail.CanEdit || detail.CanDelete {
		t.Fatalf("recipe GET = %d %s, parsed=%+v", get.Code, get.Body, detail)
	}

	create := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/recipes", strings.NewReader(`{"title":"New dish","description":"Created","ingredients":[{"name":"rice"}],"steps":[{"text":"Cook"}],"servings":2,"prep_time_minutes":5,"cook_time_minutes":10,"tags":["weeknight"],"visibility":"private"}`))
	createRequest = createRequest.WithContext(requestForUser(http.MethodPost, "/api/recipes", "owner").Context())
	h.ServeHTTP(create, createRequest)
	var created recipe
	if create.Code != http.StatusOK || json.Unmarshal(create.Body.Bytes(), &created) != nil {
		t.Fatalf("recipe create response = %d %s", create.Code, create.Body)
	}
	if created.ID == "" || created.OwnerID != "owner" || created.CreatorName != "Recipe Owner" || created.Title != "New dish" || created.Version != 1 || !created.CanEdit || !created.CanDelete || len(created.CookbookIDs) != 0 {
		t.Fatalf("recipe create response projection = %+v", created)
	}

	update := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(http.MethodPut, "/api/recipes/"+created.ID, strings.NewReader(`{"title":"Revised dish","description":"Updated","ingredients":[{"name":"rice"}],"steps":[{"text":"Cook gently"}],"servings":2,"prep_time_minutes":5,"cook_time_minutes":10,"tags":["weeknight"],"visibility":"private","version":1}`))
	updateRequest = updateRequest.WithContext(requestForUser(http.MethodPut, "/api/recipes/"+created.ID, "owner").Context())
	h.ServeHTTP(update, updateRequest)
	var updated recipe
	if update.Code != http.StatusOK || json.Unmarshal(update.Body.Bytes(), &updated) != nil {
		t.Fatalf("recipe update response = %d %s", update.Code, update.Body)
	}
	if updated.ID != created.ID || updated.OwnerID != "owner" || updated.CreatorName != "Recipe Owner" || updated.Title != "Revised dish" || updated.Version != 2 || !updated.CanEdit || !updated.CanDelete {
		t.Fatalf("recipe update response projection = %+v", updated)
	}
}

func TestRecipeDetailGETAndReadAfterWritePreserveStorageErrors(t *testing.T) {
	db := testDatabase(t)
	insertRecipeQueryFixture(t, db)
	if _, err := db.Exec(`DROP TABLE cookbook_memberships`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}

	get := httptest.NewRecorder()
	h.ServeHTTP(get, requestForUser(http.MethodGet, "/api/recipes/private-recipe", "owner"))
	if get.Code != http.StatusInternalServerError || !strings.Contains(get.Body.String(), `"storage_error"`) {
		t.Fatalf("recipe GET storage failure = %d %s", get.Code, get.Body)
	}

	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/recipes", strings.NewReader(`{"title":"Committed dish","ingredients":[],"steps":[{"text":"Cook"}],"servings":1,"prep_time_minutes":1,"cook_time_minutes":1,"tags":[],"visibility":"private"}`))
	request = request.WithContext(requestForUser(http.MethodPost, "/api/recipes", "owner").Context())
	h.ServeHTTP(create, request)
	if create.Code != http.StatusInternalServerError || !strings.Contains(create.Body.String(), `"storage_error"`) {
		t.Fatalf("recipe create read-after-write storage failure = %d %s", create.Code, create.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='owner' AND title='Committed dish'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("committed write count=%d err=%v", count, err)
	}
}
