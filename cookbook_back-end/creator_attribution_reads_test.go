package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalRecipeReadsRespectResourceAndProfileVisibility(t *testing.T) {
	db := testDatabase(t)
	insertRecipeQueryFixture(t, db)
	profileID := strings.Repeat("a", 32)
	foreignSourceID := strings.Repeat("b", 32)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('instance-owner','instance@example.test','Instance owner',0,'now');
		UPDATE instance_authority SET owner_user_id='instance-owner',owner_claimed_at='now',generation=1 WHERE singleton_id=1;
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('book','admin','viewer','now'),('book','instance-owner','viewer','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		VALUES(?,'owner','Household cook','instance','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('11111111111111111111111111111111','private-recipe','local',?,'creator','self_asserted_at_creation','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('22222222222222222222222222222222','private-recipe','local',?,'adapter','adapter_source','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		VALUES('33333333333333333333333333333333','private-recipe', 'external_claim',?,?,'Household cook','contributor','portable_claim','now');
		UPDATE recipes SET creator_name='Household cook' WHERE id='private-recipe'`, profileID, profileID, foreignSourceID, profileID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	for _, actor := range []string{"owner", "viewer"} {
		response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/private-recipe", actor, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s detail status=%d body=%s", actor, response.Code, response.Body)
		}
		var detail recipe
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.CreatorName != "Household cook" || len(detail.CreatorAttributions) != 3 {
			t.Fatalf("%s detail lost legacy or typed credits: %+v", actor, detail)
		}
		if detail.CreatorAttributions[0].Role != "creator" || detail.CreatorAttributions[1].Role != "contributor" || detail.CreatorAttributions[2].Role != "adapter" {
			t.Fatalf("attribution role order/values = %+v", detail.CreatorAttributions)
		}
		var external *creatorAttributionRead
		for index := range detail.CreatorAttributions {
			item := &detail.CreatorAttributions[index]
			if item.SubjectKind == "external_claim" {
				external = item
			}
		}
		if external == nil || external.Profile != nil || external.NameClaim != "Household cook" {
			t.Fatalf("external claim was linked or collapsed: %+v", external)
		}
		if strings.Contains(response.Body.String(), foreignSourceID) {
			t.Fatalf("response exposed a foreign identifier: %s", response.Body)
		}
		if actor == "viewer" && detail.CreatorAttributions[0].Profile.Visibility != "instance" {
			t.Fatalf("instance profile not visible to ACL reader: %+v", detail.CreatorAttributions[0])
		}
	}

	// The resource remains readable through cookbook membership when its
	// creator withdraws instance visibility; only the local rows disappear.
	if _, err := db.Exec(`UPDATE creator_profiles SET public_name='',visibility='private' WHERE creator_id=?`, profileID); err != nil {
		t.Fatal(err)
	}
	withdrawn := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/private-recipe", "viewer", nil)
	var detail recipe
	if withdrawn.Code != http.StatusOK || json.Unmarshal(withdrawn.Body.Bytes(), &detail) != nil || len(detail.CreatorAttributions) != 1 || detail.CreatorAttributions[0].SubjectKind != "external_claim" || strings.Contains(withdrawn.Body.String(), profileID) {
		t.Fatalf("withdrawn local rows affected resource access or claim: status=%d body=%s", withdrawn.Code, withdrawn.Body)
	}
	owner := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/private-recipe", "owner", nil)
	if owner.Code != http.StatusOK || json.Unmarshal(owner.Body.Bytes(), &detail) != nil || len(detail.CreatorAttributions) != 3 || detail.CreatorAttributions[0].Profile == nil || detail.CreatorAttributions[0].Profile.PublicName != "" {
		t.Fatalf("private blank-name self profile was not projected to owner: status=%d body=%s", owner.Code, owner.Body)
	}

	for _, actor := range []string{"admin", "instance-owner"} {
		response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/private-recipe", actor, nil)
		var roleReader recipe
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &roleReader) != nil || len(roleReader.CreatorAttributions) != 1 || roleReader.CreatorAttributions[0].SubjectKind != "external_claim" || strings.Contains(response.Body.String(), profileID) {
			t.Fatalf("%s bypassed profile visibility or resource ACL: status=%d body=%s", actor, response.Code, response.Body)
		}
	}
	outsider := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/private-recipe", "outsider", nil)
	if outsider.Code != http.StatusNotFound || strings.Contains(outsider.Body.String(), profileID) {
		t.Fatalf("resource ACL denial leaked recipe attribution: status=%d body=%s", outsider.Code, outsider.Body)
	}
	list := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes?limit=24&offset=0", "viewer", nil)
	var listed []recipe
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed) != 1 || len(listed[0].CreatorAttributions) != 1 || listed[0].CreatorAttributions[0].SubjectKind != "external_claim" {
		t.Fatalf("batched recipe list projection status=%d body=%s", list.Code, list.Body)
	}
	anonymous, err := readCreatorAttributions(context.Background(), db, "", "recipe", []string{"private-recipe"})
	if err != nil || len(anonymous["private-recipe"]) != 0 {
		t.Fatalf("anonymous typed attribution query=%v err=%v", anonymous, err)
	}
}

func TestCookbookReadsAndRecipeMutationsReturnTransactionalAttributions(t *testing.T) {
	db := testDatabase(t)
	insertRecipeQueryFixture(t, db)
	profileID := strings.Repeat("c", 32)
	if _, err := db.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,created_at)
		VALUES('66666666666666666666666666666666','private-recipe',1,'owner','Soup','Warm soup','[{"name":"water"}]','[{"text":"Boil"}]',2,5,10,'["family"]','private','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		VALUES(?,'owner','Collection maker','instance','now','now');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('44444444444444444444444444444444','book','local',?,'creator','local_record_creation','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('55555555555555555555555555555555','private-recipe','local',?,'creator','self_asserted_at_creation','now')`, profileID, profileID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	book := callCreatorProfileAPI(t, h, http.MethodGet, "/api/cookbooks/book", "viewer", nil)
	if book.Code != http.StatusOK || !strings.Contains(book.Body.String(), `"role":"creator"`) {
		t.Fatalf("cookbook detail creator role status=%d body=%s", book.Code, book.Body)
	}
	books := callCreatorProfileAPI(t, h, http.MethodGet, "/api/cookbooks?limit=24&offset=0", "viewer", nil)
	var cookbookPage []cookbook
	if books.Code != http.StatusOK || json.Unmarshal(books.Body.Bytes(), &cookbookPage) != nil || len(cookbookPage) != 1 || len(cookbookPage[0].CreatorAttributions) != 1 {
		t.Fatalf("cookbook page typed credits status=%d body=%s", books.Code, books.Body)
	}

	updated := callCreatorProfileAPI(t, h, http.MethodPut, "/api/recipes/private-recipe", "owner", map[string]any{
		"title": "Updated soup", "description": "", "ingredients": []any{}, "steps": []map[string]string{{"text": "Cook"}},
		"servings": 2, "prep_time_minutes": 5, "cook_time_minutes": 10, "tags": []string{"family"}, "visibility": "private", "version": 1,
	})
	var mutation, fetched recipe
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &mutation) != nil || len(mutation.CreatorAttributions) != 1 {
		t.Fatalf("recipe update typed response status=%d body=%s", updated.Code, updated.Body)
	}
	get := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/private-recipe", "owner", nil)
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &fetched) != nil || len(fetched.CreatorAttributions) != 1 || !reflect.DeepEqual(mutation.CreatorAttributions[0], fetched.CreatorAttributions[0]) {
		t.Fatalf("transactional mutation/detail credits differ: update=%+v get=%+v body=%s", mutation.CreatorAttributions, fetched.CreatorAttributions, get.Body)
	}
	restored := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes/private-recipe/revisions/1/restore", "owner", map[string]any{"expected_version": 2})
	if restored.Code != http.StatusOK || json.Unmarshal(restored.Body.Bytes(), &mutation) != nil || len(mutation.CreatorAttributions) != 1 {
		t.Fatalf("recipe restore typed response status=%d body=%s", restored.Code, restored.Body)
	}
	get = callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/private-recipe", "owner", nil)
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &fetched) != nil || !reflect.DeepEqual(mutation.CreatorAttributions[0], fetched.CreatorAttributions[0]) {
		t.Fatalf("transactional restore/detail credits differ: restore=%+v get=%+v", mutation.CreatorAttributions, fetched.CreatorAttributions)
	}

	patched := callCreatorProfileAPI(t, h, http.MethodPatch, "/api/cookbooks/book", "owner", map[string]any{"title": "Renamed family", "description": "", "visibility": "private"})
	var patchedBook cookbook
	if patched.Code != http.StatusOK || json.Unmarshal(patched.Body.Bytes(), &patchedBook) != nil || len(patchedBook.CreatorAttributions) != 1 {
		t.Fatalf("cookbook patch typed response status=%d body=%s", patched.Code, patched.Body)
	}
	bookGet := callCreatorProfileAPI(t, h, http.MethodGet, "/api/cookbooks/book", "owner", nil)
	var fetchedBook cookbook
	if bookGet.Code != http.StatusOK || json.Unmarshal(bookGet.Body.Bytes(), &fetchedBook) != nil || len(fetchedBook.CreatorAttributions) != 1 || !reflect.DeepEqual(patchedBook.CreatorAttributions[0], fetchedBook.CreatorAttributions[0]) {
		t.Fatalf("transactional cookbook patch/detail credits differ: patch=%+v get=%+v", patchedBook.CreatorAttributions, fetchedBook.CreatorAttributions)
	}
	created := callCreatorProfileAPI(t, h, http.MethodPost, "/api/cookbooks", "owner", map[string]any{"title": "New collection", "description": "", "visibility": "private"})
	var createdBook cookbook
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdBook) != nil || len(createdBook.CreatorAttributions) != 1 {
		t.Fatalf("cookbook create typed response status=%d body=%s", created.Code, created.Body)
	}
}

func TestForkKeepsAdapterSeparateFromSourceCreator(t *testing.T) {
	db := testDatabase(t)
	profileID := strings.Repeat("d", 32)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('forker','forker@example.test','Forker','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES('source','owner','Original creator','Pie','Notes','[]','[{"text":"Bake"}]',6,20,40,'[]','public','owner_only',1,'created','updated');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at,published_at)
		VALUES('source','published','off',0,'now','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		VALUES(?,'owner','Original profile','instance','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('77777777777777777777777777777777','source','local',?,'creator','self_asserted_at_creation','now')`, profileID, profileID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	forkResponse := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes/source/fork", "forker", nil)
	var forked recipe
	if forkResponse.Code != http.StatusCreated || json.Unmarshal(forkResponse.Body.Bytes(), &forked) != nil || forked.SourceRecipeID != "source" || forked.CreatorName != "Original creator" {
		t.Fatalf("fork response identity/status=%d body=%s", forkResponse.Code, forkResponse.Body)
	}
	if len(forked.CreatorAttributions) != 1 || forked.CreatorAttributions[0].Role != "adapter" || forked.CreatorAttributions[0].Profile == nil || forked.CreatorAttributions[0].Profile.ID == profileID {
		t.Fatalf("fork copied/synthesized source creator relation: %+v", forked.CreatorAttributions)
	}
	source := callCreatorProfileAPI(t, h, http.MethodGet, "/api/recipes/source", "forker", nil)
	var sourceRecipe recipe
	if source.Code != http.StatusOK || json.Unmarshal(source.Body.Bytes(), &sourceRecipe) != nil || len(sourceRecipe.CreatorAttributions) != 1 || sourceRecipe.CreatorAttributions[0].Role != "creator" || sourceRecipe.CreatorAttributions[0].Profile.ID != profileID {
		t.Fatalf("source creator relation missing from source: status=%d body=%s", source.Code, source.Body)
	}
	publicRequest := httptest.NewRequest(http.MethodGet, "/api/public/recipes/source", nil)
	publicRequest.SetPathValue("id", "source")
	publicResponse := httptest.NewRecorder()
	(publicRecipeDetailHandler{db: db}).ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusOK || strings.Contains(publicResponse.Body.String(), "creator_attributions") || strings.Contains(publicResponse.Body.String(), profileID) {
		t.Fatalf("public recipe response gained private typed attribution: status=%d body=%s", publicResponse.Code, publicResponse.Body)
	}
}

type contentMutationCounts struct {
	recipes         int
	cookbooks       int
	profiles        int
	attributions    int
	revisions       int
	policies        int
	cookbookRecipes int
	audits          int
	outbox          int
}

func readContentMutationCounts(t *testing.T, db *sql.DB) contentMutationCounts {
	t.Helper()
	var counts contentMutationCounts
	queries := []struct {
		query  string
		target *int
	}{
		{`SELECT COUNT(*) FROM recipes`, &counts.recipes},
		{`SELECT COUNT(*) FROM cookbooks`, &counts.cookbooks},
		{`SELECT COUNT(*) FROM creator_profiles`, &counts.profiles},
		{`SELECT COUNT(*) FROM content_attributions`, &counts.attributions},
		{`SELECT COUNT(*) FROM recipe_revisions`, &counts.revisions},
		{`SELECT COUNT(*) FROM recipe_publication_policy`, &counts.policies},
		{`SELECT COUNT(*) FROM cookbook_recipes`, &counts.cookbookRecipes},
		{`SELECT COUNT(*) FROM audit_events`, &counts.audits},
		{`SELECT COUNT(*) FROM federation_outbox`, &counts.outbox},
	}
	for _, item := range queries {
		if err := db.QueryRow(item.query).Scan(item.target); err != nil {
			t.Fatalf("count mutation effects (%s): %v", item.query, err)
		}
	}
	return counts
}

func induceCanonicalReadFailure(t *testing.T, db *sql.DB, before contentMutationCounts, mutate func() *httptest.ResponseRecorder) {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE cookbook_memberships`); err != nil {
		t.Fatal(err)
	}
	response := mutate()
	if response.Code != http.StatusInternalServerError || response.Body.String() != `{"error":"storage_error"}`+"\n" {
		t.Fatalf("canonical projection failure status=%d body=%s", response.Code, response.Body)
	}
	if after := readContentMutationCounts(t, db); after != before {
		t.Fatalf("projection failure committed partial mutation: before=%+v after=%+v", before, after)
	}
}

func TestCanonicalProjectionFailuresRollBackContentMutations(t *testing.T) {
	t.Run("recipe create rolls back created profile attribution revision policy audit and outbox", func(t *testing.T) {
		db := testDatabase(t)
		if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
			t.Fatal(err)
		}
		before := readContentMutationCounts(t, db)
		h := apiHandler{db: db}
		induceCanonicalReadFailure(t, db, before, func() *httptest.ResponseRecorder {
			return callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes", "owner", map[string]any{
				"title": "Atomic recipe", "ingredients": []any{}, "steps": []map[string]string{{"text": "Cook"}},
				"servings": 1, "prep_time_minutes": 1, "cook_time_minutes": 1, "tags": []string{}, "visibility": "private",
				"creator_attribution": map[string]any{"kind": "local_profile"},
			})
		})
	})

	for _, operation := range []string{"recipe update", "recipe restore"} {
		t.Run(operation+" rolls back version revision attribution audit and outbox", func(t *testing.T) {
			db := testDatabase(t)
			insertRecipeQueryFixture(t, db)
			profileID := strings.Repeat("e", 32)
			if _, err := db.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,created_at)
				VALUES('88888888888888888888888888888888','private-recipe',1,'owner','Soup','Warm soup','[{"name":"water"}]','[{"text":"Boil"}]',2,5,10,'["family"]','private','now');
				INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'owner','Owner profile','instance','now','now');
				INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('99999999999999999999999999999999','private-recipe','local',?,'creator','self_asserted_at_creation','now')`, profileID, profileID); err != nil {
				t.Fatal(err)
			}
			before := readContentMutationCounts(t, db)
			h := apiHandler{db: db}
			induceCanonicalReadFailure(t, db, before, func() *httptest.ResponseRecorder {
				if operation == "recipe update" {
					return callCreatorProfileAPI(t, h, http.MethodPut, "/api/recipes/private-recipe", "owner", map[string]any{
						"title": "Must roll back", "ingredients": []any{}, "steps": []map[string]string{{"text": "Cook"}},
						"servings": 2, "prep_time_minutes": 5, "cook_time_minutes": 10, "tags": []string{}, "visibility": "private", "version": 1,
						"cookbook_ids": []string{},
					})
				}
				return callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes/private-recipe/revisions/1/restore", "owner", map[string]any{"expected_version": 1})
			})
			var title string
			var version int
			if err := db.QueryRow(`SELECT title,version FROM recipes WHERE id='private-recipe'`).Scan(&title, &version); err != nil || title != "Soup" || version != 1 {
				t.Fatalf("failed %s changed recipe title=%q version=%d err=%v", operation, title, version, err)
			}
			if operation == "recipe update" {
				var links int
				if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE recipe_id='private-recipe' AND cookbook_id='book'`).Scan(&links); err != nil || links != 1 {
					t.Fatalf("failed owner PUT did not restore prior cookbook link count=%d err=%v", links, err)
				}
			}
		})
	}

	t.Run("cookbook create rolls back profile creator attribution and record", func(t *testing.T) {
		db := testDatabase(t)
		if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
			t.Fatal(err)
		}
		before := readContentMutationCounts(t, db)
		h := apiHandler{db: db}
		induceCanonicalReadFailure(t, db, before, func() *httptest.ResponseRecorder {
			return callCreatorProfileAPI(t, h, http.MethodPost, "/api/cookbooks", "owner", map[string]any{"title": "Atomic cookbook", "description": "", "visibility": "private"})
		})
	})

	t.Run("cookbook patch rolls back record update", func(t *testing.T) {
		db := testDatabase(t)
		insertRecipeQueryFixture(t, db)
		if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'owner','Collection','instance','now','now');
			INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','book','local',?,'creator','local_record_creation','now')`, strings.Repeat("f", 32), strings.Repeat("f", 32)); err != nil {
			t.Fatal(err)
		}
		before := readContentMutationCounts(t, db)
		h := apiHandler{db: db}
		induceCanonicalReadFailure(t, db, before, func() *httptest.ResponseRecorder {
			return callCreatorProfileAPI(t, h, http.MethodPatch, "/api/cookbooks/book", "owner", map[string]any{"title": "Must roll back", "description": "", "visibility": "private"})
		})
		var title string
		if err := db.QueryRow(`SELECT title FROM cookbooks WHERE id='book'`).Scan(&title); err != nil || title != "Family book" {
			t.Fatalf("failed cookbook PATCH changed title=%q err=%v", title, err)
		}
	})
}
