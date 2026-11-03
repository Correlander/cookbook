package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func insertPublicationReadFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1,courses_enabled=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('member','member@example.test','Member',0,'now'),
		('outsider','outsider@example.test','Outsider',0,'now'),('admin','admin@example.test','Admin',1,'now');
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES
		('public-book','owner','Public recipes','','public','now','now'),('member-book','owner','Member recipes','','private','now','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('member-book','member','viewer','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,tags_json,visibility,edit_policy,image_path,created_at,updated_at,deleted_at) VALUES
		('published','owner','Owner','Visible soup','PUBLIC_MARKER','[]','[]','[]','public','owner_only','', 'now','now',NULL),
		('unpublished','owner','Owner','Unpublished secret','UNPUBLISHED_MARKER','[]','[]','[]','public','owner_only','', 'now','now',NULL),
		('withdrawn','owner','Owner','Withdrawn secret','WITHDRAWN_MARKER','[]','[]','[]','public','owner_only','', 'now','now',NULL),
		('private','owner','Owner','Private secret','','[]','[]','[]','private','owner_only','', 'now','now',NULL),
		('unlisted','owner','Owner','Unlisted secret','','[]','[]','[]','unlisted','owner_only','', 'now','now',NULL),
		('deleted','owner','Owner','Deleted secret','','[]','[]','[]','public','owner_only','', 'now','now','2026-01-01');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES
		('published','published','off',0,'now'),('unpublished','unpublished','off',0,'now'),
		('withdrawn','withdrawn','off',0,'now'),('private','unpublished','off',0,'now'),
		('unlisted','unpublished','off',0,'now'),('deleted','published','off',0,'now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES
		('public-book','published','owner','now'),('public-book','unpublished','owner','now'),
		('public-book','withdrawn','owner','now'),('public-book','deleted','owner','now'),
		('member-book','withdrawn','owner','now');
		INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) VALUES
		('public-course','owner','Public course','','public','now','now');
		INSERT INTO course_lessons(id,course_id,title,content,recipe_id,position,created_at,updated_at) VALUES
		('lesson-visible','public-course','Visible','Visible text','published',0,'now','now'),
		('lesson-withdrawn','public-course','Withdrawn','Still public lesson text','withdrawn',1,'now','now')`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicationPolicyGatesPublicRecipeReadProjections(t *testing.T) {
	db := testDatabase(t)
	insertPublicationReadFixture(t, db)
	handler := handlerWithAuth(db, testAuth(db))

	publicList := callAPI(handler, http.MethodGet, "/api/public/recipes?q=MARKER", nil, nil)
	var publicItems []recipe
	if publicList.Code != http.StatusOK || json.Unmarshal(publicList.Body.Bytes(), &publicItems) != nil || len(publicItems) != 1 || publicItems[0].ID != "published" {
		t.Fatalf("anonymous list included unpublished content or hid published row: %d %s", publicList.Code, publicList.Body)
	}
	withdrawnSearch := callAPI(handler, http.MethodGet, "/api/public/recipes?q=WITHDRAWN_MARKER", nil, nil)
	if withdrawnSearch.Code != http.StatusOK || withdrawnSearch.Body.String() != "[]\n" {
		t.Fatalf("anonymous search disclosed withdrawn snippet: %d %s", withdrawnSearch.Code, withdrawnSearch.Body)
	}

	outsiderToken, _, err := issueSession(db, "outsider")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for name, cookie := range map[string]*http.Cookie{
		"unrelated account": {Name: sessionCookie, Value: outsiderToken},
		"instance admin":    {Name: sessionCookie, Value: adminToken},
	} {
		list := callAPI(handler, http.MethodGet, "/api/recipes?limit=24&offset=0", nil, cookie)
		var items []recipe
		if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &items) != nil || len(items) != 1 || items[0].ID != "published" {
			t.Errorf("%s public branch did not enforce publication: %d %s", name, list.Code, list.Body)
		}
		for _, id := range []string{"unpublished", "withdrawn", "private", "unlisted", "deleted"} {
			response := callAPI(handler, http.MethodGet, "/api/recipes/"+id, nil, cookie)
			if response.Code != http.StatusNotFound {
				t.Errorf("%s read %s status=%d body=%s, want 404", name, id, response.Code, response.Body)
			}
		}
	}

	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	for _, id := range []string{"unpublished", "withdrawn", "private", "unlisted"} {
		response := callAPI(handler, http.MethodGet, "/api/recipes/"+id, nil, ownerCookie)
		if response.Code != http.StatusOK {
			t.Errorf("owner local read of %s lost access: %d %s", id, response.Code, response.Body)
		}
	}
	ownerList := callAPI(handler, http.MethodGet, "/api/recipes?limit=24&offset=0", nil, ownerCookie)
	var owned []recipe
	if ownerList.Code != http.StatusOK || json.Unmarshal(ownerList.Body.Bytes(), &owned) != nil || len(owned) != 5 {
		t.Fatalf("owner local list lost withdrawn/private content: %d %s", ownerList.Code, ownerList.Body)
	}

	memberToken, _, err := issueSession(db, "member")
	if err != nil {
		t.Fatal(err)
	}
	memberRead := callAPI(handler, http.MethodGet, "/api/recipes/withdrawn", nil, &http.Cookie{Name: sessionCookie, Value: memberToken})
	if memberRead.Code != http.StatusOK {
		t.Fatalf("authorized cookbook member lost withdrawn local access: %d %s", memberRead.Code, memberRead.Body)
	}

	books := callAPI(handler, http.MethodGet, "/api/public/cookbooks", nil, nil)
	var publicBooks []publicCookbook
	if books.Code != http.StatusOK || json.Unmarshal(books.Body.Bytes(), &publicBooks) != nil || len(publicBooks) != 1 || publicBooks[0].RecipeCount != 1 {
		t.Fatalf("public cookbook count includes unpublished/deleted recipe: %d %s", books.Code, books.Body)
	}
	bookRecipes := callAPI(handler, http.MethodGet, "/api/public/cookbooks/public-book/recipes", nil, nil)
	var listed []recipe
	if bookRecipes.Code != http.StatusOK || json.Unmarshal(bookRecipes.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].ID != "published" {
		t.Fatalf("public cookbook recipe projection not gated: %d %s", bookRecipes.Code, bookRecipes.Body)
	}
	lessonResponse := callAPI(handler, http.MethodGet, "/api/public/courses/public-course/lessons", nil, nil)
	var lessons []courseLesson
	if lessonResponse.Code != http.StatusOK || json.Unmarshal(lessonResponse.Body.Bytes(), &lessons) != nil || len(lessons) != 2 || lessons[0].RecipeID != "published" || lessons[1].RecipeID != "" {
		t.Fatalf("public course recipe references not reauthorized: %d %s", lessonResponse.Code, lessonResponse.Body)
	}
	if _, err := db.Exec(`DELETE FROM recipe_publication_policy WHERE recipe_id='published'`); err != nil {
		t.Fatal(err)
	}
	_, missingPolicyErr := readRecipeDetail(context.Background(), db, ContentActor{}, "published")
	if !errors.Is(missingPolicyErr, errRecipeDetailDenied) {
		t.Fatalf("missing-policy public detail err=%v, want fail-closed denial", missingPolicyErr)
	}
	publicAfterMissing := callAPI(handler, http.MethodGet, "/api/public/recipes", nil, nil)
	if publicAfterMissing.Code != http.StatusOK || publicAfterMissing.Body.String() != "[]\n" {
		t.Fatalf("missing-policy public list did not fail closed: %d %s", publicAfterMissing.Code, publicAfterMissing.Body)
	}
	stillMissing := 0
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_publication_policy WHERE recipe_id='published'`).Scan(&stillMissing); err != nil || stillMissing != 0 {
		t.Fatalf("public read silently repaired missing policy: count=%d err=%v", stillMissing, err)
	}
}

func TestPublicCollectionQueriesRecheckInstanceInRowsStatement(t *testing.T) {
	db := testDatabase(t)
	insertPublicationReadFixture(t, db)
	var public int
	if err := db.QueryRow(`SELECT public_instance FROM instance_settings WHERE id=1`).Scan(&public); err != nil || public != 1 {
		t.Fatalf("fixture public mode=%d err=%v", public, err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	for _, path := range []string{"/api/public/recipes", "/api/public/cookbooks"} {
		response := callAPI(handler, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusOK || response.Body.String() != "[]\n" {
			t.Errorf("%s returned rows after instance became private: %d %s", path, response.Code, response.Body)
		}
	}

	rows, err := db.Query(publicCookbookRecipeRowsQuery+"r.id LIMIT ? OFFSET ?", "public-book", "", "", "", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("public cookbook recipe row query returned content after instance became private")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicRecipeProjectionsRedactInternalSourceRecipeID(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES
		('owner','owner@example.test','Owner','now'),('member','member@example.test','Member','now'),('outsider','outsider@example.test','Outsider','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES
		('public-book','owner','Public','public','now','now'),('member-book','owner','Member private','private','now','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('member-book','member','viewer','now');
		INSERT INTO recipes(id,owner_id,creator_name,source_recipe_id,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES
		('private-source','owner','Owner','','Private source','[]','[]','[]','private','owner_only','now','now'),
		('public-fork','owner','Owner','private-source','Public fork','[]','[]','[]','public','owner_only','now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES
		('private-source','unpublished','off',0,'now'),('public-fork','published','off',0,'now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES
		('public-book','public-fork','owner','now'),('member-book','public-fork','owner','now')`); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	memberToken, _, err := issueSession(db, "member")
	if err != nil {
		t.Fatal(err)
	}
	outsiderToken, _, err := issueSession(db, "outsider")
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]struct {
		path   string
		cookie *http.Cookie
	}{
		"anonymous public list":          {path: "/api/public/recipes"},
		"unrelated authenticated list":   {path: "/api/recipes?limit=24&offset=0", cookie: &http.Cookie{Name: sessionCookie, Value: outsiderToken}},
		"unrelated authenticated detail": {path: "/api/recipes/public-fork", cookie: &http.Cookie{Name: sessionCookie, Value: outsiderToken}},
		"public cookbook list":           {path: "/api/public/cookbooks"},
		"public cookbook recipe list":    {path: "/api/public/cookbooks/public-book/recipes"},
	} {
		response := callAPI(handler, http.MethodGet, request.path, nil, request.cookie)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "private-source") {
			t.Errorf("%s leaked internal source ID: %d %s", name, response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), `"source_recipe_id"`) {
			t.Errorf("%s emitted source_recipe_id field: %s", name, response.Body)
		}
		if name == "public cookbook list" {
			var books []publicCookbook
			if json.Unmarshal(response.Body.Bytes(), &books) != nil || len(books) != 1 || books[0].RecipeCount != 1 {
				t.Errorf("public cookbook list omitted fork/count: %d %s", response.Code, response.Body)
			}
		} else if name == "unrelated authenticated detail" {
			var item recipe
			if json.Unmarshal(response.Body.Bytes(), &item) != nil || item.ID != "public-fork" {
				t.Errorf("unrelated public detail omitted fork: %d %s", response.Code, response.Body)
			}
		} else {
			var items []recipe
			if json.Unmarshal(response.Body.Bytes(), &items) != nil || len(items) != 1 || items[0].ID != "public-fork" {
				t.Errorf("%s omitted public fork: %d %s", name, response.Code, response.Body)
			}
		}
	}
	for name, cookie := range map[string]*http.Cookie{
		"owner":  {Name: sessionCookie, Value: ownerToken},
		"member": {Name: sessionCookie, Value: memberToken},
	} {
		response := callAPI(handler, http.MethodGet, "/api/recipes/public-fork", nil, cookie)
		var item recipe
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &item) != nil || item.SourceRecipeID != "private-source" {
			t.Errorf("authorized %s lost local source history: %d %s", name, response.Code, response.Body)
		}
	}
}

func TestRecipePublicReadFactsSupportTransactionsAndFailClosedMissingPolicy(t *testing.T) {
	db := testDatabase(t)
	insertPublicationReadFixture(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	facts, err := readRecipePublicReadFacts(context.Background(), tx, "published")
	if err != nil || !facts.AnonymousEligible() {
		_ = tx.Rollback()
		t.Fatalf("transactional public facts=%+v err=%v", facts, err)
	}
	if _, err := tx.Exec(`DELETE FROM recipe_publication_policy WHERE recipe_id='published'`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	facts, err = readRecipePublicReadFacts(context.Background(), tx, "published")
	if err != nil || facts.AnonymousEligible() || facts.PolicyExists {
		_ = tx.Rollback()
		t.Fatalf("missing policy did not fail closed: facts=%+v err=%v", facts, err)
	}
	_ = tx.Rollback()
}

func TestRecipeImagePublicGateAndCachePolicy(t *testing.T) {
	db := testDatabase(t)
	insertPublicationReadFixture(t, db)
	mediaDir := t.TempDir()
	filename := "11111111111111111111111111111111.png"
	var imageData bytes.Buffer
	pixels := image.NewRGBA(image.Rect(0, 0, 2, 2))
	pixels.Set(0, 0, color.RGBA{R: 20, G: 80, B: 120, A: 255})
	if err := png.Encode(&imageData, pixels); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, filename), imageData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE recipes SET image_path=? WHERE id IN ('published','withdrawn')`, filename); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, testAuth(db), config{mediaDir: mediaDir})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		visible := callAPI(handler, method, "/api/recipes/published/image", nil, nil)
		if visible.Code != http.StatusOK || visible.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("published image %s status=%d cache=%q", method, visible.Code, visible.Header().Get("Cache-Control"))
		}
		withdrawn := callAPI(handler, method, "/api/recipes/withdrawn/image", nil, nil)
		if withdrawn.Code != http.StatusNotFound {
			t.Errorf("withdrawn image %s status=%d, want 404", method, withdrawn.Code)
		}
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerImage := callAPI(handler, http.MethodGet, "/api/recipes/withdrawn/image", nil, &http.Cookie{Name: sessionCookie, Value: ownerToken})
	if ownerImage.Code != http.StatusOK || ownerImage.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("owner image read after withdrawal status=%d cache=%q", ownerImage.Code, ownerImage.Header().Get("Cache-Control"))
	}
	memberToken, _, err := issueSession(db, "member")
	if err != nil {
		t.Fatal(err)
	}
	memberImage := callAPI(handler, http.MethodHead, "/api/recipes/withdrawn/image", nil, &http.Cookie{Name: sessionCookie, Value: memberToken})
	if memberImage.Code != http.StatusOK || memberImage.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("member image read after withdrawal status=%d cache=%q", memberImage.Code, memberImage.Header().Get("Cache-Control"))
	}
}
