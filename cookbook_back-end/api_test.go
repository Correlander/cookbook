package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func callAPI(h http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	var data bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&data).Encode(body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, &data)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if method != http.MethodGet {
		req.Host = "localhost:5173"
		req.Header.Set("Origin", "http://localhost:5173")
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestPortableExportAndImportKeepOwnershipAndPrivacySafe(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('other','other@example.test','Other','now'); INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('book','owner','Family recipes','Notes','public','now','now'),('other-book','other','Other book','','private','now','now'); INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES('recipe','owner','Grandma','Soup','Warm soup','[{"name":"water"}]','[{"text":"Boil"}]',4,10,20,'["family"]','public','owner_only',1,'now','now'),('other-recipe','other','Other','Secret','','[]','[]',0,0,0,'[]','private','owner_only',1,'now','now'); INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','recipe','owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	export := httptest.NewRecorder()
	h.ServeHTTP(export, requestForUser(http.MethodGet, "/api/export", "owner"))
	if export.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", export.Code, export.Body)
	}
	var bundle portableBundle
	if err := json.Unmarshal(export.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Cookbooks) != 1 || len(bundle.Recipes) != 1 || bundle.Recipes[0].CreatorName != "Grandma" || len(bundle.Cookbooks[0].RecipeIDs) != 1 {
		t.Fatalf("incomplete export: %+v", bundle)
	}
	importResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(export.Body.String()))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "owner"))
	h.ServeHTTP(importResponse, request)
	if importResponse.Code != http.StatusCreated {
		t.Fatalf("import status=%d body=%s", importResponse.Code, importResponse.Body)
	}
	var recipeCount, bookCount, publicCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='owner' AND visibility='private' AND creator_name='Grandma'`).Scan(&recipeCount); err != nil || recipeCount != 1 {
		t.Fatalf("imported private attributed recipes=%d err=%v", recipeCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE owner_id='owner' AND visibility='private' AND title='Family recipes'`).Scan(&bookCount); err != nil || bookCount != 1 {
		t.Fatalf("imported private cookbooks=%d err=%v", bookCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='other'`).Scan(&publicCount); err != nil || publicCount != 1 {
		t.Fatalf("other user's data affected=%d err=%v", publicCount, err)
	}
}

func TestPortableImportRejectsInvalidAndOversizedBundles(t *testing.T) {
	if err := validatePortableBundle(portableBundle{Format: portableFormat, Recipes: []portableRecipe{{ID: "r", Title: "Dish", Visibility: "public", CookbookIDs: []string{"missing"}}}}); err == nil {
		t.Fatal("accepted unknown cookbook reference")
	}
	if err := validatePortableBundle(portableBundle{Format: "unknown"}); err == nil {
		t.Fatal("accepted unsupported format")
	}
	db := testDatabase(t)
	h := apiHandler{db: db}
	request := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(strings.Repeat("x", maxImportBytes+1)))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, "owner"))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized import status=%d", response.Code)
	}
}

func TestAuthStateChangesRequireSameOrigin(t *testing.T) {
	db := testDatabase(t)
	h := handlerWithAuth(db, testAuth(db))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/consume", strings.NewReader(`{"token":"invalid"}`))
	req.Host = "localhost:5173"
	req.Header.Set("Origin", "https://attacker.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin auth request status = %d, want 403", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/auth/consume", strings.NewReader(`{"token":"invalid"}`))
	req.Host = "localhost:5173"
	req.Header.Set("Origin", "https://localhost:5173")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("same-host wrong-scheme auth request status = %d, want 403", rec.Code)
	}
}

func TestPublicRecipeListingRequiresPublicInstanceAndPublishedRecipe(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at) VALUES('private','owner','Owner','Private dish','','[]','[]','private','owner_only','now','now'),('public','owner','Owner','Shared dish','','[]','[]','public','owner_only','now','now')`)
	if err != nil {
		t.Fatal(err)
	}
	h := handlerWithAuth(db, testAuth(db))
	privateInstance := httptest.NewRecorder()
	h.ServeHTTP(privateInstance, httptest.NewRequest(http.MethodGet, "/api/public/recipes", nil))
	if privateInstance.Code != http.StatusOK || strings.TrimSpace(privateInstance.Body.String()) != "[]" {
		t.Fatalf("private instance listing = %d %s", privateInstance.Code, privateInstance.Body)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	publicInstance := httptest.NewRecorder()
	h.ServeHTTP(publicInstance, httptest.NewRequest(http.MethodGet, "/api/public/recipes", nil))
	var items []recipe
	if publicInstance.Code != http.StatusOK || json.Unmarshal(publicInstance.Body.Bytes(), &items) != nil {
		t.Fatalf("public instance listing = %d %s", publicInstance.Code, publicInstance.Body)
	}
	if len(items) != 1 || items[0].ID != "public" {
		t.Fatalf("public listing leaked or omitted content: %#v", items)
	}
	if strings.Contains(publicInstance.Body.String(), `"owner_id"`) {
		t.Fatalf("public listing exposed the internal owner ID: %s", publicInstance.Body)
	}
}

func TestPublicRecipeSearchAndPagination(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1; INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'); INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at) VALUES('soup-a','owner','Owner','Soup A','','[]','[]','public','owner_only','now','2026-01-01'),('soup-b','owner','Owner','Soup B','','[]','[]','public','owner_only','now','2026-01-02'),('cake','owner','Owner','Cake','','[]','[]','public','owner_only','now','2026-01-03')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE recipes SET tags_json='["quick"]' WHERE id='soup-b'`); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/public/recipes?q=SOUP&limit=1&offset=0", nil))
	var page []recipe
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page) != 1 || page[0].ID != "soup-b" {
		t.Fatalf("first search page = %d %s", first.Code, first.Body)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	privateEndpoint := callAPI(handler, http.MethodGet, "/api/recipes?q=SOUP&limit=1&offset=0", nil, &http.Cookie{Name: sessionCookie, Value: token})
	if privateEndpoint.Code != http.StatusOK || json.Unmarshal(privateEndpoint.Body.Bytes(), &page) != nil || len(page) != 1 || page[0].ID != "soup-b" {
		t.Fatalf("authenticated recipe endpoint search page = %d %s", privateEndpoint.Code, privateEndpoint.Body)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/public/recipes?q=soup&limit=1&offset=1", nil))
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &page) != nil || len(page) != 1 || page[0].ID != "soup-a" {
		t.Fatalf("second search page = %d %s", second.Code, second.Body)
	}
	tagSearch := httptest.NewRecorder()
	handler.ServeHTTP(tagSearch, httptest.NewRequest(http.MethodGet, "/api/public/recipes?q=quick", nil))
	if tagSearch.Code != http.StatusOK || json.Unmarshal(tagSearch.Body.Bytes(), &page) != nil || len(page) != 1 || page[0].ID != "soup-b" {
		t.Fatalf("tag search = %d %s", tagSearch.Code, tagSearch.Body)
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/public/recipes?limit=101", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid page limit status=%d, want 400", invalid.Code)
	}
}

func TestRecipeRevisionsAndOptimisticUpdates(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	sessionToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: sessionToken}
	h := handlerWithAuth(db, testAuth(db))
	bookResponse := callAPI(h, http.MethodPost, "/api/cookbooks", map[string]string{"title": "Family"}, cookie)
	if bookResponse.Code != http.StatusCreated {
		t.Fatalf("cookbook create status = %d: %s", bookResponse.Code, bookResponse.Body)
	}
	var book cookbook
	if err := json.Unmarshal(bookResponse.Body.Bytes(), &book); err != nil {
		t.Fatal(err)
	}
	create := map[string]any{"title": "First title", "ingredients": []map[string]string{{"name": "flour"}}, "steps": []map[string]string{{"text": "Mix"}}, "servings": 4, "prep_time_minutes": 15, "cook_time_minutes": 35, "tags": []string{"Dinner", " vegetarian ", "dinner"}, "cookbook_ids": []string{book.ID}}
	created := callAPI(h, http.MethodPost, "/api/recipes", create, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", created.Code, created.Body)
	}
	var current recipe
	if err := json.Unmarshal(created.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.CookbookIDs) != 1 || current.CookbookIDs[0] != book.ID {
		t.Fatalf("recipe cookbook association = %#v", current.CookbookIDs)
	}
	if current.Servings != 4 || current.PrepTime != 15 || current.CookTime != 35 || len(current.Tags) != 2 || current.Tags[0] != "dinner" || current.Tags[1] != "vegetarian" {
		t.Fatalf("recipe metadata normalization = %+v", current)
	}
	update := map[string]any{"title": "Second title", "ingredients": []map[string]string{{"name": "flour"}}, "steps": []map[string]string{{"text": "Mix"}}, "servings": 2, "prep_time_minutes": 10, "cook_time_minutes": 25, "tags": []string{"quick"}, "version": current.Version}
	updated := callAPI(h, http.MethodPut, "/api/recipes/"+current.ID, update, cookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", updated.Code, updated.Body)
	}
	stale := callAPI(h, http.MethodPut, "/api/recipes/"+current.ID, update, cookie)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409", stale.Code)
	}
	revisions := callAPI(h, http.MethodGet, "/api/recipes/"+current.ID+"/revisions", nil, cookie)
	var history []recipeRevision
	if revisions.Code != http.StatusOK || json.Unmarshal(revisions.Body.Bytes(), &history) != nil {
		t.Fatalf("revision listing status = %d: %s", revisions.Code, revisions.Body)
	}
	if len(history) != 2 || history[0].Version != 2 || history[0].Title != "Second title" || history[0].Servings != 2 || history[0].CookTime != 25 || len(history[0].Tags) != 1 || history[0].Tags[0] != "quick" || history[1].Version != 1 || history[1].Title != "First title" || history[1].Servings != 4 || history[1].PrepTime != 15 || len(history[1].Tags) != 2 {
		t.Fatalf("unexpected revision history: %#v", history)
	}
	diffResponse := callAPI(h, http.MethodGet, "/api/recipes/"+current.ID+"/revisions/diff?from=1&to=2", nil, cookie)
	var diff revisionDiff
	if diffResponse.Code != http.StatusOK || json.Unmarshal(diffResponse.Body.Bytes(), &diff) != nil || diff.FromVersion != 1 || diff.ToVersion != 2 {
		t.Fatalf("revision diff status=%d body=%s", diffResponse.Code, diffResponse.Body)
	}
	if diff.Changes["title"].Before != "First title" || diff.Changes["title"].After != "Second title" || diff.Changes["servings"].Before != float64(4) || diff.Changes["servings"].After != float64(2) {
		t.Fatalf("revision diff changes=%#v", diff.Changes)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('stranger','stranger@example.test','Stranger','now')`); err != nil {
		t.Fatal(err)
	}
	strangerToken, _, err := issueSession(db, "stranger")
	if err != nil {
		t.Fatal(err)
	}
	privateDiff := callAPI(h, http.MethodGet, "/api/recipes/"+current.ID+"/revisions/diff?from=1&to=2", nil, &http.Cookie{Name: sessionCookie, Value: strangerToken})
	if privateDiff.Code != http.StatusNotFound {
		t.Fatalf("private revision diff status=%d, want 404", privateDiff.Code)
	}
	invalidDiff := callAPI(h, http.MethodGet, "/api/recipes/"+current.ID+"/revisions/diff?from=0&to=2", nil, cookie)
	if invalidDiff.Code != http.StatusBadRequest {
		t.Fatalf("invalid revision diff status=%d, want 400", invalidDiff.Code)
	}
	restored := callAPI(h, http.MethodPost, "/api/recipes/"+current.ID+"/revisions/1/restore", map[string]int{"expected_version": current.Version + 1}, cookie)
	if restored.Code != http.StatusOK {
		t.Fatalf("restore revision status = %d: %s", restored.Code, restored.Body)
	}
	var afterRestore recipe
	if err := json.Unmarshal(restored.Body.Bytes(), &afterRestore); err != nil || afterRestore.Version != 3 || afterRestore.Title != "First title" || afterRestore.Servings != 4 || afterRestore.PrepTime != 15 || afterRestore.CookTime != 35 || len(afterRestore.Tags) != 2 || afterRestore.Tags[0] != "dinner" {
		t.Fatalf("restored recipe = %#v, err=%v", afterRestore, err)
	}
	staleRestore := callAPI(h, http.MethodPost, "/api/recipes/"+current.ID+"/revisions/2/restore", map[string]int{"expected_version": current.Version + 1}, cookie)
	if staleRestore.Code != http.StatusConflict {
		t.Fatalf("stale restore status = %d, want 409", staleRestore.Code)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','reader@example.test','Reader','now')`); err != nil {
		t.Fatal(err)
	}
	readerToken, _, err := issueSession(db, "reader")
	if err != nil {
		t.Fatal(err)
	}
	denied := callAPI(h, http.MethodGet, "/api/recipes/"+current.ID+"/revisions", nil, &http.Cookie{Name: sessionCookie, Value: readerToken})
	if denied.Code != http.StatusNotFound {
		t.Fatalf("private revision read status = %d, want 404", denied.Code)
	}
}

func TestCookbookEditorCannotChangeRecipeVisibility(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('editor','editor@example.test','Editor','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Shared','private','now','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('book','editor','editor','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at) VALUES('recipe','owner','Owner','Soup','','[]','[]','private','cookbook_editors','now','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','recipe','owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	editorToken, _, err := issueSession(db, "editor")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	editorCookie := &http.Cookie{Name: sessionCookie, Value: editorToken}
	update := map[string]any{
		"title": "Soup", "ingredients": []map[string]string{{"name": "beans"}},
		"steps": []map[string]string{{"text": "Cook"}}, "version": 1,
		"visibility": "public", "cookbook_ids": []string{"book"},
	}
	denied := callAPI(handler, http.MethodPut, "/api/recipes/recipe", update, editorCookie)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("editor visibility change status=%d, want 403: %s", denied.Code, denied.Body)
	}
	var visibilityValue visibility
	if err := db.QueryRow(`SELECT visibility FROM recipes WHERE id='recipe'`).Scan(&visibilityValue); err != nil || visibilityValue != visibilityPrivate {
		t.Fatalf("denied visibility change persisted: %q, %v", visibilityValue, err)
	}
	publicList := callAPI(handler, http.MethodGet, "/api/public/recipes", nil, nil)
	if publicList.Code != http.StatusOK || publicList.Body.String() != "[]\n" {
		t.Fatalf("editor's denied update leaked the recipe: status=%d body=%s", publicList.Code, publicList.Body)
	}

	update["visibility"] = "private"
	allowed := callAPI(handler, http.MethodPut, "/api/recipes/recipe", update, editorCookie)
	if allowed.Code != http.StatusOK {
		t.Fatalf("editor content update status=%d: %s", allowed.Code, allowed.Body)
	}
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	update["visibility"] = "public"
	update["version"] = 2
	ownerUpdate := callAPI(handler, http.MethodPut, "/api/recipes/recipe", update, ownerCookie)
	if ownerUpdate.Code != http.StatusOK {
		t.Fatalf("owner visibility update status=%d: %s", ownerUpdate.Code, ownerUpdate.Body)
	}
}

func TestCookbookPagination(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'); INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book-a','owner','A','private','now','2026-01-01'),('book-b','owner','B','private','now','2026-01-02'),('book-c','owner','C','private','now','2026-01-03')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	h := handlerWithAuth(db, testAuth(db))
	first := callAPI(h, http.MethodGet, "/api/cookbooks?limit=2&offset=0", nil, &http.Cookie{Name: sessionCookie, Value: token})
	second := callAPI(h, http.MethodGet, "/api/cookbooks?limit=2&offset=2", nil, &http.Cookie{Name: sessionCookie, Value: token})
	var firstPage, secondPage []cookbook
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &secondPage) != nil {
		t.Fatalf("page statuses = %d/%d, bodies = %s / %s", first.Code, second.Code, first.Body, second.Body)
	}
	if len(firstPage) != 2 || len(secondPage) != 1 || firstPage[0].ID != "book-c" || firstPage[1].ID != "book-b" || secondPage[0].ID != "book-a" {
		t.Fatalf("unexpected pages: %#v / %#v", firstPage, secondPage)
	}
	invalid := callAPI(h, http.MethodGet, "/api/cookbooks?limit=101", nil, &http.Cookie{Name: sessionCookie, Value: token})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid pagination status = %d, want 400", invalid.Code)
	}
}

func TestRecipeImageUploadAccessAndRevisionRestore(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('other','other@example.test','Other','now'); INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,version,created_at,updated_at) VALUES('private-recipe','owner','Owner','Soup','','[]','[]','private','owner_only',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, err := issueSession(db, "other")
	if err != nil {
		t.Fatal(err)
	}
	mediaDir := filepath.Join(t.TempDir(), "media")
	h := handlerWithAuthConfig(db, testAuth(db), config{mediaDir: mediaDir})
	encodePNG := func() []byte {
		var buffer bytes.Buffer
		pixels := image.NewRGBA(image.Rect(0, 0, 2, 2))
		for y := 0; y < 2; y++ {
			for x := 0; x < 2; x++ {
				pixels.Set(x, y, color.RGBA{R: 180, G: 70, B: 30, A: 255})
			}
		}
		if err := png.Encode(&buffer, pixels); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}
	upload := func(cookie *http.Cookie, data []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("image", "food.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(data)
		_ = writer.Close()
		request := httptest.NewRequest(http.MethodPut, "/api/recipes/private-recipe/image", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("Origin", "http://localhost:5173")
		request.Host = "localhost:5173"
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, request)
		return recorder
	}
	denied := upload(&http.Cookie{Name: sessionCookie, Value: otherToken}, encodePNG())
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-owner upload status = %d, want 403", denied.Code)
	}
	accepted := upload(&http.Cookie{Name: sessionCookie, Value: ownerToken}, encodePNG())
	if accepted.Code != http.StatusOK {
		t.Fatalf("valid upload status = %d: %s", accepted.Code, accepted.Body)
	}
	var uploaded struct {
		Image   string `json:"image"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	storedFiles, err := os.ReadDir(mediaDir)
	if err != nil || len(storedFiles) != 1 {
		t.Fatalf("stored image files = %d, err=%v", len(storedFiles), err)
	}
	privateFetch := httptest.NewRecorder()
	h.ServeHTTP(privateFetch, httptest.NewRequest(http.MethodGet, uploaded.Image, nil))
	if privateFetch.Code != http.StatusNotFound {
		t.Fatalf("anonymous private image status = %d, want 404", privateFetch.Code)
	}
	ownerFetch := httptest.NewRequest(http.MethodGet, uploaded.Image, nil)
	ownerFetch.AddCookie(&http.Cookie{Name: sessionCookie, Value: ownerToken})
	privateFetch = httptest.NewRecorder()
	h.ServeHTTP(privateFetch, ownerFetch)
	if privateFetch.Code != http.StatusOK || privateFetch.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("owner image response = %d %q", privateFetch.Code, privateFetch.Header().Get("Content-Type"))
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1; UPDATE recipes SET visibility='public' WHERE id='private-recipe'`); err != nil {
		t.Fatal(err)
	}
	publicFetch := httptest.NewRecorder()
	h.ServeHTTP(publicFetch, httptest.NewRequest(http.MethodGet, uploaded.Image, nil))
	if publicFetch.Code != http.StatusOK {
		t.Fatalf("public image response = %d", publicFetch.Code)
	}
	invalid := upload(&http.Cookie{Name: sessionCookie, Value: ownerToken}, []byte("not an image"))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("spoofed upload status = %d, want 400", invalid.Code)
	}
	tooLarge := upload(&http.Cookie{Name: sessionCookie, Value: ownerToken}, make([]byte, maxRecipeImageBytes+1))
	if tooLarge.Code != http.StatusBadRequest {
		t.Fatalf("oversized upload status = %d, want 400", tooLarge.Code)
	}
	var currentVersion int
	var currentPath string
	if err := db.QueryRow(`SELECT version,image_path FROM recipes WHERE id='private-recipe'`).Scan(&currentVersion, &currentPath); err != nil {
		t.Fatal(err)
	}
	var revisionPath string
	if err := db.QueryRow(`SELECT image_path FROM recipe_revisions WHERE recipe_id='private-recipe' AND version=?`, currentVersion).Scan(&revisionPath); err != nil || revisionPath != currentPath {
		t.Fatalf("revision image path=%q current=%q err=%v", revisionPath, currentPath, err)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/recipes/private-recipe/image", nil)
	deleteRequest.Header.Set("Origin", "http://localhost:5173")
	deleteRequest.Host = "localhost:5173"
	deleteRequest.AddCookie(&http.Cookie{Name: sessionCookie, Value: ownerToken})
	deleteResponse := httptest.NewRecorder()
	h.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("image removal status = %d", deleteResponse.Code)
	}
	restore := callAPI(h, http.MethodPost, "/api/recipes/private-recipe/revisions/"+strconv.Itoa(currentVersion)+"/restore", map[string]int{"expected_version": currentVersion + 1}, &http.Cookie{Name: sessionCookie, Value: ownerToken})
	if restore.Code != http.StatusOK {
		t.Fatalf("revision restore status = %d: %s", restore.Code, restore.Body)
	}
	var restored recipe
	if err := json.Unmarshal(restore.Body.Bytes(), &restored); err != nil || restored.Image == "" {
		t.Fatalf("restored image=%q err=%v", restored.Image, err)
	}
}

func TestRecipeTrashIsOwnerOnlyAndRestorable(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1; INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('other','other@example.test','Other','now'); INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at) VALUES('recipe','owner','Owner','Public soup','','[]','[]','public','owner_only','now','now')`); err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, err := issueSession(db, "other")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	otherCookie := &http.Cookie{Name: sessionCookie, Value: otherToken}
	forbiddenDelete := callAPI(handler, http.MethodDelete, "/api/recipes/recipe", nil, otherCookie)
	if forbiddenDelete.Code != http.StatusNotFound {
		t.Fatalf("non-owner delete status=%d, want 404", forbiddenDelete.Code)
	}
	trashed := callAPI(handler, http.MethodDelete, "/api/recipes/recipe", nil, ownerCookie)
	if trashed.Code != http.StatusOK {
		t.Fatalf("owner trash status=%d: %s", trashed.Code, trashed.Body)
	}
	missing := callAPI(handler, http.MethodGet, "/api/recipes/recipe", nil, ownerCookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("trashed recipe GET status=%d, want 404", missing.Code)
	}
	privateList := callAPI(handler, http.MethodGet, "/api/recipes", nil, ownerCookie)
	if privateList.Code != http.StatusOK || strings.TrimSpace(privateList.Body.String()) != "[]" {
		t.Fatalf("private list still includes trashed recipe: %d %s", privateList.Code, privateList.Body)
	}
	public := httptest.NewRecorder()
	handler.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/public/recipes", nil))
	if public.Code != http.StatusOK || strings.TrimSpace(public.Body.String()) != "[]" {
		t.Fatalf("public listing still exposes trashed recipe: %d %s", public.Code, public.Body)
	}
	trash := callAPI(handler, http.MethodGet, "/api/recipes/trash", nil, ownerCookie)
	var items []recipe
	if trash.Code != http.StatusOK || json.Unmarshal(trash.Body.Bytes(), &items) != nil || len(items) != 1 || items[0].ID != "recipe" || items[0].DeletedAt == "" {
		t.Fatalf("owner trash listing=%d %s", trash.Code, trash.Body)
	}
	otherTrash := callAPI(handler, http.MethodGet, "/api/recipes/trash", nil, otherCookie)
	if otherTrash.Code != http.StatusOK || strings.TrimSpace(otherTrash.Body.String()) != "[]" {
		t.Fatalf("another account can see trash: %d %s", otherTrash.Code, otherTrash.Body)
	}
	forbiddenRestore := callAPI(handler, http.MethodPost, "/api/recipes/recipe/restore", nil, otherCookie)
	if forbiddenRestore.Code != http.StatusNotFound {
		t.Fatalf("non-owner restore status=%d, want 404", forbiddenRestore.Code)
	}
	restored := callAPI(handler, http.MethodPost, "/api/recipes/recipe/restore", nil, ownerCookie)
	var result recipe
	if restored.Code != http.StatusOK || json.Unmarshal(restored.Body.Bytes(), &result) != nil || result.DeletedAt != "" || !result.CanDelete {
		t.Fatalf("owner restore=%d %s", restored.Code, restored.Body)
	}
}

func TestPasskeyCeremoniesAreShortLivedAndSingleUse(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	sessionToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	h := handlerWithAuth(db, testAuth(db))
	cookie := &http.Cookie{Name: sessionCookie, Value: sessionToken}
	started := callAPI(h, http.MethodPost, "/api/auth/passkey/register/begin", nil, cookie)
	if started.Code != http.StatusOK {
		t.Fatalf("registration begin status = %d: %s", started.Code, started.Body)
	}
	var ceremony struct {
		ID      string `json:"ceremony_id"`
		Options struct {
			PublicKey struct {
				Challenge string `json:"challenge"`
			} `json:"publicKey"`
		} `json:"options"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &ceremony); err != nil || ceremony.ID == "" || ceremony.Options.PublicKey.Challenge == "" {
		t.Fatalf("invalid registration options: %s (%v)", started.Body, err)
	}
	finish := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/finish", strings.NewReader(`{}`))
	finish.Host = "localhost:5173"
	finish.Header.Set("Origin", "http://localhost:5173")
	finish.Header.Set("X-Cookbook-Ceremony", ceremony.ID)
	finish.AddCookie(cookie)
	finishRecorder := httptest.NewRecorder()
	h.ServeHTTP(finishRecorder, finish)
	if finishRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid attestation response status = %d", finishRecorder.Code)
	}
	replay := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/register/finish", strings.NewReader(`{}`))
	replay.Host = "localhost:5173"
	replay.Header.Set("Origin", "http://localhost:5173")
	replay.Header.Set("X-Cookbook-Ceremony", ceremony.ID)
	replay.AddCookie(cookie)
	replayRecorder := httptest.NewRecorder()
	h.ServeHTTP(replayRecorder, replay)
	if replayRecorder.Code != http.StatusBadRequest {
		t.Fatalf("registration ceremony replay status = %d", replayRecorder.Code)
	}
	loginBegin := callAPI(h, http.MethodPost, "/api/auth/passkey/login/begin", nil, nil)
	if loginBegin.Code != http.StatusOK {
		t.Fatalf("discoverable login begin status = %d: %s", loginBegin.Code, loginBegin.Body)
	}
	var loginCeremony struct {
		ID string `json:"ceremony_id"`
	}
	if err := json.Unmarshal(loginBegin.Body.Bytes(), &loginCeremony); err != nil || loginCeremony.ID == "" {
		t.Fatalf("invalid login ceremony: %s (%v)", loginBegin.Body, err)
	}
	loginFinish := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/login/finish", strings.NewReader(`{}`))
	loginFinish.Host = "localhost:5173"
	loginFinish.Header.Set("Origin", "http://localhost:5173")
	loginFinish.Header.Set("X-Cookbook-Ceremony", loginCeremony.ID)
	loginRecorder := httptest.NewRecorder()
	h.ServeHTTP(loginRecorder, loginFinish)
	if loginRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid passkey assertion status = %d", loginRecorder.Code)
	}
	loginReplay := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/login/finish", strings.NewReader(`{}`))
	loginReplay.Host = "localhost:5173"
	loginReplay.Header.Set("Origin", "http://localhost:5173")
	loginReplay.Header.Set("X-Cookbook-Ceremony", loginCeremony.ID)
	loginReplayRecorder := httptest.NewRecorder()
	h.ServeHTTP(loginReplayRecorder, loginReplay)
	if loginReplayRecorder.Code != http.StatusBadRequest {
		t.Fatalf("login ceremony replay status = %d", loginReplayRecorder.Code)
	}
}

func TestPasskeyCredentialCannotBeRegisteredToTwoAccounts(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('another-user','another@example.test','Another','now')`); err != nil {
		t.Fatal(err)
	}
	h := passkeyHandler{db: db}
	credential := webauthn.Credential{ID: []byte("credential-id")}
	if err := h.storeCredential("owner", credential, "Kitchen laptop"); err != nil {
		t.Fatal(err)
	}
	if err := h.storeCredential("another-user", credential, "Other device"); !errors.Is(err, errPasskeyAlreadyRegistered) {
		t.Fatalf("duplicate credential error = %v, want already registered", err)
	}
	var owner, name string
	if err := db.QueryRow(`SELECT user_id,name FROM webauthn_credentials WHERE credential_id=?`, credential.ID).Scan(&owner, &name); err != nil || owner != "owner" || name != "Kitchen laptop" {
		t.Fatalf("credential owner/name = %q/%q, err = %v", owner, name, err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	response := callAPI(handlerWithAuth(db, testAuth(db)), http.MethodGet, "/api/auth/passkeys", nil, &http.Cookie{Name: sessionCookie, Value: token})
	var items []passkeySummary
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil || len(items) != 1 || items[0].Name != "Kitchen laptop" {
		t.Fatalf("passkey list response=%d body=%s", response.Code, response.Body)
	}
}

func TestPasskeyNamesHaveSafeLengthAndDefault(t *testing.T) {
	if name, err := normalizePasskeyName("  "); err != nil || name != "Passkey" {
		t.Fatalf("blank name = %q, %v", name, err)
	}
	if _, err := normalizePasskeyName(strings.Repeat("a", 81)); err == nil {
		t.Fatal("overlong passkey name was accepted")
	}
	if _, err := normalizePasskeyName("device\nname"); err == nil {
		t.Fatal("control character in passkey name was accepted")
	}
}

func TestAdminInvitationRequiresVerifiedEmailAndCanBeRevoked(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuth(db)
	handler := handlerWithAuth(db, auth)
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	created := callAPI(handler, http.MethodPost, "/api/admin/invitations", map[string]string{"email": "new@example.test"}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create invitation = %d: %s", created.Code, created.Body)
	}
	var invitation struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &invitation); err != nil || invitation.ID == "" || invitation.Token == "" {
		t.Fatalf("invalid invitation response: %s (%v)", created.Body, err)
	}
	request := callAPI(handler, http.MethodPost, "/api/auth/email", map[string]string{"email": "new@example.test", "invitation": invitation.Token}, nil)
	if request.Code != http.StatusOK {
		t.Fatalf("invited registration request = %d: %s", request.Code, request.Body)
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='new@example.test'`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("unverified invited users = %d, err %v", users, err)
	}
	var link string
	select {
	case link = <-auth.sender.(testEmailSender).sent:
	default:
		t.Fatal("email sender channel unavailable")
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	magicToken := parsed.Query().Get("token")
	consumed := callAPI(handler, http.MethodPost, "/api/auth/consume", map[string]string{"token": magicToken}, nil)
	if consumed.Code != http.StatusOK {
		t.Fatalf("consume invite email link = %d: %s", consumed.Code, consumed.Body)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='new@example.test'`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("verified invited users = %d, err %v", users, err)
	}
	listed := callAPI(handler, http.MethodGet, "/api/admin/invitations", nil, cookie)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), invitation.Token) {
		t.Fatalf("invitation listing leaked token or failed: %d %s", listed.Code, listed.Body)
	}
	second := callAPI(handler, http.MethodPost, "/api/admin/invitations", map[string]string{"email": "other@example.test"}, cookie)
	var secondInvitation struct {
		ID string `json:"id"`
	}
	if second.Code != http.StatusCreated || json.Unmarshal(second.Body.Bytes(), &secondInvitation) != nil {
		t.Fatalf("second invitation failed: %d %s", second.Code, second.Body)
	}
	revoked := callAPI(handler, http.MethodDelete, "/api/admin/invitations?id="+secondInvitation.ID, nil, cookie)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke invitation = %d: %s", revoked.Code, revoked.Body)
	}
}

func TestRecipeOwnerUpdatePreservesReferenceToSharedCookbook(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('recipe-owner','recipe@example.test','Recipe owner','now'),('book-owner','book@example.test','Book owner','now'); INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('shared-book','book-owner','Shared','private','now','now'); INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('shared-book','recipe-owner','viewer','now'); INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,version,created_at,updated_at) VALUES('shared-recipe','recipe-owner','Recipe owner','Soup','','[]','[]','private','owner_only',1,'now','now'); INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('shared-book','shared-recipe','book-owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "recipe-owner")
	if err != nil {
		t.Fatal(err)
	}
	response := callAPI(handlerWithAuth(db, testAuth(db)), http.MethodPut, "/api/recipes/shared-recipe", map[string]any{"title": "Better soup", "ingredients": []map[string]string{}, "steps": []map[string]string{}, "visibility": "private", "edit_policy": "owner_only", "version": 1, "cookbook_ids": []string{"shared-book"}}, &http.Cookie{Name: sessionCookie, Value: token})
	if response.Code != http.StatusOK {
		t.Fatalf("owner update = %d: %s", response.Code, response.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE cookbook_id='shared-book' AND recipe_id='shared-recipe'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared cookbook reference count=%d, err=%v", count, err)
	}
}

func TestCookbookEditorPolicyAndViewerRestrictions(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('editor','editor@example.test','Editor','now'),('viewer','viewer@example.test','Viewer','now'); INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Shared','private','now','now'); INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('book','editor','editor','now'),('book','viewer','viewer','now'); INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,version,created_at,updated_at) VALUES('recipe','owner','Owner','Soup','','[]','[]','private','owner_only',1,'now','now'); INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','recipe','owner','now')`)
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	editorToken, _, err := issueSession(db, "editor")
	if err != nil {
		t.Fatal(err)
	}
	editorCookie := &http.Cookie{Name: sessionCookie, Value: editorToken}
	ownerOnlyRecipe := callAPI(handler, http.MethodGet, "/api/recipes/recipe", nil, editorCookie)
	var editorView recipe
	if ownerOnlyRecipe.Code != http.StatusOK || json.Unmarshal(ownerOnlyRecipe.Body.Bytes(), &editorView) != nil || editorView.CanEdit {
		t.Fatalf("owner-only recipe edit capability = %d %+v", ownerOnlyRecipe.Code, editorView)
	}
	makeUpdate := func(userID string, version int) *httptest.ResponseRecorder {
		token, _, issueErr := issueSession(db, userID)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return callAPI(handler, http.MethodPut, "/api/recipes/recipe", map[string]any{"title": "Changed", "ingredients": []map[string]string{}, "steps": []map[string]string{}, "visibility": "private", "edit_policy": "owner_only", "version": version, "cookbook_ids": []string{"book"}}, &http.Cookie{Name: sessionCookie, Value: token})
	}
	if response := makeUpdate("editor", 1); response.Code != http.StatusForbidden {
		t.Fatalf("editor changed owner-only recipe: %d", response.Code)
	}
	if _, err := db.Exec(`UPDATE recipes SET edit_policy='cookbook_editors' WHERE id='recipe'`); err != nil {
		t.Fatal(err)
	}
	editableRecipe := callAPI(handler, http.MethodGet, "/api/recipes/recipe", nil, editorCookie)
	if editableRecipe.Code != http.StatusOK || json.Unmarshal(editableRecipe.Body.Bytes(), &editorView) != nil || !editorView.CanEdit {
		t.Fatalf("editor-enabled recipe edit capability = %d %+v", editableRecipe.Code, editorView)
	}
	if response := makeUpdate("viewer", 1); response.Code != http.StatusForbidden {
		t.Fatalf("viewer changed editor-enabled recipe: %d", response.Code)
	}
	if response := makeUpdate("editor", 1); response.Code != http.StatusOK {
		t.Fatalf("editor could not update editor-enabled recipe: %d %s", response.Code, response.Body)
	}
}

func TestCookbookOwnerCanInviteChangeRoleAndRemoveMember(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('guest','guest@example.test','Guest','now'); INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Family recipes','private','now','now')`)
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuth(db)
	handler := handlerWithAuth(db, auth)
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	created := callAPI(handler, http.MethodPost, "/api/cookbooks/book/members/invitations", map[string]string{"email": "guest@example.test", "role": "editor"}, ownerCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create cookbook invitation = %d: %s", created.Code, created.Body)
	}
	var invitation struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &invitation); err != nil || invitation.ID == "" || invitation.Token == "" {
		t.Fatalf("invalid invitation response: %s (%v)", created.Body, err)
	}
	wrongAddress := callAPI(handler, http.MethodPost, "/api/auth/email", map[string]string{"email": "other@example.test", "invitation": invitation.Token}, nil)
	if wrongAddress.Code != http.StatusOK {
		t.Fatalf("wrong-address invitation response=%d", wrongAddress.Code)
	}
	select {
	case <-auth.sender.(testEmailSender).sent:
		t.Fatal("invitation was sent to an address other than the one invited")
	default:
	}
	guestRequest := callAPI(handler, http.MethodPost, "/api/auth/email", map[string]string{"email": "guest@example.test", "invitation": invitation.Token}, nil)
	if guestRequest.Code != http.StatusOK {
		t.Fatalf("guest email request = %d: %s", guestRequest.Code, guestRequest.Body)
	}
	link := <-auth.sender.(testEmailSender).sent
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	consumed := callAPI(handler, http.MethodPost, "/api/auth/consume", map[string]string{"token": parsed.Query().Get("token")}, nil)
	if consumed.Code != http.StatusOK {
		t.Fatalf("consume cookbook invite = %d: %s", consumed.Code, consumed.Body)
	}
	guestCookie := consumed.Result().Cookies()[0]
	nonOwnerInvite := callAPI(handler, http.MethodPost, "/api/cookbooks/book/members/invitations", map[string]string{"email": "other@example.test", "role": "viewer"}, guestCookie)
	if nonOwnerInvite.Code != http.StatusForbidden {
		t.Fatalf("non-owner invitation status=%d, want 403", nonOwnerInvite.Code)
	}
	var memberRole role
	if err := db.QueryRow(`SELECT role FROM cookbook_memberships WHERE cookbook_id='book' AND user_id='guest'`).Scan(&memberRole); err != nil || memberRole != roleEditor {
		t.Fatalf("invited member role=%q err=%v", memberRole, err)
	}
	listed := callAPI(handler, http.MethodGet, "/api/cookbooks/book/members", nil, guestCookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("member listing = %d: %s", listed.Code, listed.Body)
	}
	changed := callAPI(handler, http.MethodPatch, "/api/cookbooks/book/members", map[string]string{"user_id": "guest", "role": "viewer"}, ownerCookie)
	if changed.Code != http.StatusOK {
		t.Fatalf("change member role = %d: %s", changed.Code, changed.Body)
	}
	removed := callAPI(handler, http.MethodDelete, "/api/cookbooks/book/members?user_id=guest", nil, ownerCookie)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove member = %d: %s", removed.Code, removed.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_memberships WHERE cookbook_id='book' AND user_id='guest'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed membership count=%d err=%v", count, err)
	}
	deniedAfterRemoval := callAPI(handler, http.MethodGet, "/api/cookbooks/book/members", nil, guestCookie)
	if deniedAfterRemoval.Code != http.StatusNotFound {
		t.Fatalf("removed member can still list members: status=%d", deniedAfterRemoval.Code)
	}
}

func TestNonAdminCannotManageInvitations(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('member','member@example.test','Member','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "member")
	if err != nil {
		t.Fatal(err)
	}
	response := callAPI(handlerWithAuth(db, testAuth(db)), http.MethodPost, "/api/admin/invitations", map[string]string{"email": "other@example.test"}, &http.Cookie{Name: sessionCookie, Value: token})
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin invitation status = %d, want 403", response.Code)
	}
}

func TestUnauthenticatedAndForgedUserCannotCreateCookbook(t *testing.T) {
	db := testDatabase(t)
	sender := testEmailSender{sent: make(chan string, 1)}
	auth := testAuth(db)
	auth.sender = sender
	h := handlerWithAuth(db, auth)
	rec := callAPI(h, http.MethodPost, "/api/cookbooks", map[string]any{"owner_id": "victim", "title": "Family"}, nil)
	if rec.Code != 401 {
		t.Fatalf("anonymous create status %d", rec.Code)
	}
}
func TestPasswordlessLoginAndSessionGatesAPI(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	sender := testEmailSender{sent: make(chan string, 1)}
	auth := testAuth(db)
	auth.sender = sender
	h := handlerWithAuth(db, auth)
	rec := callAPI(h, http.MethodPost, "/api/auth/email", map[string]string{"email": "chef@example.test", "display_name": "Chef"}, nil)
	if rec.Code != 200 {
		t.Fatalf("request link status %d: %s", rec.Code, rec.Body)
	}
	link := <-sender.sent
	token := link[strings.Index(link, "token=")+6:]
	rec = callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	if rec.Code != 200 {
		t.Fatalf("consume status %d: %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("secure session cookie missing attributes")
	}
	rec = callAPI(h, http.MethodPost, "/api/cookbooks", map[string]any{"title": "Family"}, session)
	if rec.Code != 201 {
		t.Fatalf("authenticated cookbook create status %d: %s", rec.Code, rec.Body)
	}
	var c cookbook
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	var email string
	if err := db.QueryRow(`SELECT email FROM users WHERE id=?`, c.OwnerID).Scan(&email); err != nil || email != "chef@example.test" {
		t.Fatalf("owner was not session user: %q, %v", email, err)
	}
}
func TestLoginTokenSingleUse(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	sender := testEmailSender{sent: make(chan string, 1)}
	h := handlerWithAuth(db, func() authHandler { a := testAuth(db); a.sender = sender; return a }())
	_ = callAPI(h, http.MethodPost, "/api/auth/email", map[string]string{"email": "one@example.test"}, nil)
	token := <-sender.sent
	token = token[strings.Index(token, "token=")+6:]
	first := callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	second := callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	if first.Code != 200 || second.Code != 400 {
		t.Fatalf("consume statuses %d then %d", first.Code, second.Code)
	}
}

func TestExpiredLoginChallengeRejected(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	sender := testEmailSender{sent: make(chan string, 1)}
	auth := testAuth(db)
	auth.sender = sender
	h := handlerWithAuth(db, auth)
	_ = callAPI(h, http.MethodPost, "/api/auth/email", map[string]string{"email": "expired@example.test"}, nil)
	link := <-sender.sent
	token := link[strings.Index(link, "token=")+6:]
	_, err := db.Exec(`UPDATE email_challenges SET expires_at=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	rec := callAPI(h, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	if rec.Code != 400 {
		t.Fatalf("expired token status %d", rec.Code)
	}
}

func TestPrivateInstanceCannotPublishCookbook(t *testing.T) {
	db := testDatabase(t)
	_, _ = db.Exec(`UPDATE instance_settings SET registration_policy='open' WHERE id=1`)
	h := testAuth(db)
	auth := h
	sender := testEmailSender{sent: make(chan string, 1)}
	auth.sender = sender
	handler := handlerWithAuth(db, auth)
	_ = callAPI(handler, http.MethodPost, "/api/auth/email", map[string]string{"email": "private@example.test"}, nil)
	link := <-sender.sent
	token := link[strings.Index(link, "token=")+6:]
	signed := callAPI(handler, http.MethodPost, "/api/auth/consume", map[string]string{"token": token}, nil)
	session := signed.Result().Cookies()[0]
	resp := callAPI(handler, http.MethodPost, "/api/cookbooks", map[string]any{"title": "Public?", "visibility": "public"}, session)
	if resp.Code != 403 {
		t.Fatalf("public cookbook on private instance status %d", resp.Code)
	}
}

func TestCookbookOwnerCanEditDetailsAndVisibility(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('member','member@example.test','Member','now'); INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('book','owner','Old title','Old description','private','now','now'); INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('book','member','editor','now')`); err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	memberToken, _, err := issueSession(db, "member")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	body := map[string]any{"title": "New title", "description": "New description", "visibility": "private"}
	memberResponse := callAPI(handler, http.MethodPatch, "/api/cookbooks/book", body, &http.Cookie{Name: sessionCookie, Value: memberToken})
	if memberResponse.Code != http.StatusForbidden {
		t.Fatalf("member edit status=%d, want 403", memberResponse.Code)
	}
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	updated := callAPI(handler, http.MethodPatch, "/api/cookbooks/book", body, ownerCookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("owner edit status=%d: %s", updated.Code, updated.Body)
	}
	var result cookbook
	if err := json.Unmarshal(updated.Body.Bytes(), &result); err != nil || result.Title != "New title" || result.Description != "New description" {
		t.Fatalf("edited cookbook=%+v err=%v", result, err)
	}
	body["visibility"] = "public"
	privateInstance := callAPI(handler, http.MethodPatch, "/api/cookbooks/book", body, ownerCookie)
	if privateInstance.Code != http.StatusForbidden {
		t.Fatalf("public visibility on private instance status=%d, want 403", privateInstance.Code)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	publicInstance := callAPI(handler, http.MethodPatch, "/api/cookbooks/book", body, ownerCookie)
	if publicInstance.Code != http.StatusOK {
		t.Fatalf("public visibility on public instance status=%d: %s", publicInstance.Code, publicInstance.Body)
	}
}
