package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicRecipeDetailUsesAnonymousProjectionRegardlessOfCookies(t *testing.T) {
	db := publicRecipeDetailTestDB(t)
	mediaDir := t.TempDir()
	imageName := strings.Repeat("a", 32) + ".png"
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, imageName), pngBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE recipes SET image_path=? WHERE id='public'`, imageName); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, testAuth(db), config{mediaDir: mediaDir})
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}

	responses := []*httptest.ResponseRecorder{
		publicRecipeDetailRequest(handler, http.MethodGet, "/api/public/recipes/public", nil),
		publicRecipeDetailRequest(handler, http.MethodGet, "/api/public/recipes/public", &http.Cookie{Name: sessionCookie, Value: ownerToken}),
		publicRecipeDetailRequest(handler, http.MethodGet, "/api/public/recipes/public", &http.Cookie{Name: sessionCookie, Value: adminToken}),
	}
	for _, response := range responses {
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("public detail status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body)
		}
	}
	for _, response := range responses[1:] {
		if response.Body.String() != responses[0].Body.String() {
			t.Fatalf("cookie changed public projection: anonymous=%s cookie=%s", responses[0].Body, response.Body)
		}
	}
	var got publicRecipeDetail
	if err := json.Unmarshal(responses[0].Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "public" || got.Title != "Public recipe" || got.Image != "/api/recipes/public/image" || got.Version != 3 {
		t.Fatalf("public detail projection = %+v", got)
	}
	for _, forbidden := range []string{"owner_id", "cookbook_ids", "source_recipe_id", "edit_policy", "can_edit", "can_delete", "can_manage_sharing", "visibility"} {
		if strings.Contains(responses[0].Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("public projection contains forbidden field %q: %s", forbidden, responses[0].Body)
		}
	}

	imageResponse := publicRecipeDetailRequest(handler, http.MethodGet, "/api/recipes/public/image", nil)
	if imageResponse.Code != http.StatusOK || imageResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("published image status=%d cache=%q", imageResponse.Code, imageResponse.Header().Get("Cache-Control"))
	}
	if _, err := db.Exec(`UPDATE recipe_publication_policy SET publication_state='withdrawn' WHERE recipe_id='public'`); err != nil {
		t.Fatal(err)
	}
	withdrawnImage := publicRecipeDetailRequest(handler, http.MethodGet, "/api/recipes/public/image", nil)
	if withdrawnImage.Code != http.StatusNotFound {
		t.Fatalf("withdrawn image status=%d body=%s", withdrawnImage.Code, withdrawnImage.Body)
	}
}

func TestPublicRecipeDetailReturnsGenericNotFoundForEveryPublicGate(t *testing.T) {
	db := publicRecipeDetailTestDB(t)
	handler := handlerWithAuth(db, testAuth(db))
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	assertNotFound := func(path string, cookie *http.Cookie) {
		t.Helper()
		response := publicRecipeDetailRequest(handler, http.MethodGet, path, cookie)
		if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":\"not_found\"}\n" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("detail %s cookie=%v status=%d cache=%q body=%q", path, cookie != nil, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
		}
	}

	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	assertNotFound("/api/public/recipes/public", &http.Cookie{Name: sessionCookie, Value: ownerToken})
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"private", "unlisted", "unpublished", "withdrawn", "deleted", "missing"} {
		assertNotFound("/api/public/recipes/"+id, nil)
	}
	assertNotFound("/api/public/recipes/private", &http.Cookie{Name: sessionCookie, Value: ownerToken})
	assertNotFound("/api/public/recipes/private", &http.Cookie{Name: sessionCookie, Value: adminToken})

	for _, path := range []string{
		"/api/public/recipes/" + strings.Repeat("a", maxPublicRecipeDetailIDBytes+1),
		"/api/public/recipes/a%2Fb",
	} {
		assertNotFound(path, nil)
	}

	head := publicRecipeDetailRequest(handler, http.MethodHead, "/api/public/recipes/public", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD detail status=%d cache=%q body=%q", head.Code, head.Header().Get("Cache-Control"), head.Body.String())
	}
	headMissing := publicRecipeDetailRequest(handler, http.MethodHead, "/api/public/recipes/missing", nil)
	if headMissing.Code != http.StatusNotFound || headMissing.Body.Len() != 0 {
		t.Fatalf("HEAD missing status=%d body=%q", headMissing.Code, headMissing.Body.String())
	}

	privateWithoutCookie := publicRecipeDetailRequest(handler, http.MethodGet, "/api/recipes/private", nil)
	privateWithOwner := publicRecipeDetailRequest(handler, http.MethodGet, "/api/recipes/private", &http.Cookie{Name: sessionCookie, Value: ownerToken})
	if privateWithoutCookie.Code != http.StatusUnauthorized || privateWithOwner.Code != http.StatusOK {
		t.Fatalf("protected private route changed: anonymous=%d owner=%d", privateWithoutCookie.Code, privateWithOwner.Code)
	}
}

func TestPublicRecipeDetailMapsUnexpectedReadErrorsToStorageError(t *testing.T) {
	db := publicRecipeDetailTestDB(t)
	if _, err := db.Exec(`DROP TABLE recipe_publication_policy`); err != nil {
		t.Fatal(err)
	}
	response := publicRecipeDetailRequest(handlerWithAuth(db, testAuth(db)), http.MethodGet, "/api/public/recipes/public", nil)
	if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"error\":\"storage_error\"}\n" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("storage failure status=%d cache=%q body=%q", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func publicRecipeDetailTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
	('owner','owner@example.test','Owner',0,'now'),('admin','admin@example.test','Admin',1,'now');
INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at,deleted_at) VALUES
	('public','owner','Public Chef','Public recipe','Public description','[{"name":"tomato"}]','[{"text":"Cook"}]',2,5,10,'["soup"]','public','owner_only',3,'2026-11-01T00:00:00Z','2026-11-02T00:00:00Z',NULL),
	('private','owner','Private Chef','Private recipe','','[]','[]',0,0,0,'[]','private','owner_only',1,'now','now',NULL),
	('unlisted','owner','Hidden Chef','Unlisted recipe','','[]','[]',0,0,0,'[]','unlisted','owner_only',1,'now','now',NULL),
	('unpublished','owner','Hidden Chef','Unpublished recipe','','[]','[]',0,0,0,'[]','public','owner_only',1,'now','now',NULL),
	('withdrawn','owner','Hidden Chef','Withdrawn recipe','','[]','[]',0,0,0,'[]','public','owner_only',1,'now','now',NULL),
	('deleted','owner','Hidden Chef','Deleted recipe','','[]','[]',0,0,0,'[]','public','owner_only',1,'now','now','2026-11-02T00:00:00Z');
INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES
	('public','published','off',0,'now'),('private','published','off',0,'now'),('unlisted','published','off',0,'now'),
	('withdrawn','withdrawn','off',0,'now'),('deleted','published','off',0,'now');`); err != nil {
		t.Fatal(err)
	}
	return db
}

func publicRecipeDetailRequest(handler http.Handler, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
