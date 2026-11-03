package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForkResponseMatchesOwnerDetailCapabilitiesAndLineage(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('forker','forker@example.test','Forker','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at,image_path)
		VALUES('source','owner','Original creator','Pie','Notes','[{"name":"flour"}]','[{"text":"Bake"}]',6,20,40,'["dessert"]','public','owner_only',3,'created','updated','source-image.png')`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	forkResponse := httptest.NewRecorder()
	h.ServeHTTP(forkResponse, requestForUser(http.MethodPost, "/api/recipes/source/fork", "forker"))
	if forkResponse.Code != http.StatusCreated {
		t.Fatalf("fork status=%d body=%s", forkResponse.Code, forkResponse.Body)
	}
	var forked recipe
	if err := json.Unmarshal(forkResponse.Body.Bytes(), &forked); err != nil {
		t.Fatal(err)
	}

	detailResponse := httptest.NewRecorder()
	h.ServeHTTP(detailResponse, requestForUser(http.MethodGet, "/api/recipes/"+forked.ID, "forker"))
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("owner detail status=%d body=%s", detailResponse.Code, detailResponse.Body)
	}
	var detail recipe
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if forked.ID == "" || forked.ID == "source" || forked.OwnerID != "forker" || detail.OwnerID != forked.OwnerID {
		t.Fatalf("fork/detail identity mismatch: fork=%+v detail=%+v", forked, detail)
	}
	if forked.CanEdit != detail.CanEdit || forked.CanDelete != detail.CanDelete || forked.CanManageSharing != detail.CanManageSharing || !forked.CanEdit || !forked.CanDelete || !forked.CanManageSharing {
		t.Fatalf("fork/detail capabilities mismatch: fork edit=%t delete=%t sharing=%t; detail edit=%t delete=%t sharing=%t", forked.CanEdit, forked.CanDelete, forked.CanManageSharing, detail.CanEdit, detail.CanDelete, detail.CanManageSharing)
	}
	if forked.SourceRecipeID != "source" || detail.SourceRecipeID != "source" || forked.CreatorName != "Original creator" || forked.Visibility != visibilityPrivate || forked.EditPolicy != "owner_only" || forked.Version != 1 {
		t.Fatalf("fork privacy/lineage changed: fork=%+v detail=%+v", forked, detail)
	}
	if forked.Image != "" || len(forked.CookbookIDs) != 0 {
		t.Fatalf("fork copied source image or cookbook links: image=%q cookbook_ids=%v", forked.Image, forked.CookbookIDs)
	}
}
