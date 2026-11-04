package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func remoteRecipeDetailRequest(t *testing.T, handler http.Handler, userID, method, query string) *httptest.ResponseRecorder {
	t.Helper()
	request := requestForUser(method, "/api/library/recipes/remote?"+query, userID)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestLibraryRemoteRecipeDetailUsesExactValidatedCacheOnPrivateInstance(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now');
		INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now');
		UPDATE instance_settings SET public_instance=0 WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	uri := testFederationOrigin + "/federation/recipes/" + testFederationID
	body := `{"title":"Cached dish","description":"A local cached view.","ingredients":[{"name":"Tomato","quantity":"2"}],"steps":[{"text":"Simmer."}],"servings":2,"prep_time_minutes":5,"cook_time_minutes":10,"tags":["soup"]}`
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, uri, testFederationOrigin, testFederationID, 3, "Remote cook", body, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var bookmarkCount, cachedCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks`).Scan(&bookmarkCount); err != nil || bookmarkCount != 0 {
		t.Fatalf("initial bookmark count=%d err=%v", bookmarkCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipes`).Scan(&cachedCount); err != nil {
		t.Fatal(err)
	}

	response := remoteRecipeDetailRequest(t, apiHandler{db: db}, "viewer", http.MethodGet, "remote_object_uri="+url.QueryEscape(uri))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("detail status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body)
	}
	var view RemoteRecipeReadView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ObjectURI != uri || view.SourceOrigin != testFederationOrigin || view.SourceRecipeID != testFederationID || view.CreatorName != "Remote cook" || view.Sequence != 3 || view.Recipe.Title != "Cached dish" || len(view.Recipe.Ingredients) != 1 || view.Recipe.Ingredients[0].Name != "Tomato" {
		t.Fatalf("remote detail projection=%+v", view)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"owner_id", "can_edit", "can_delete", "can_manage_sharing", "cookbook_ids", "image"} {
		if _, exists := fields[forbidden]; exists {
			t.Errorf("response synthesized local field %q: %s", forbidden, response.Body)
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks`).Scan(&bookmarkCount); err != nil || bookmarkCount != 0 {
		t.Fatalf("detail read changed bookmark count: %d err=%v", bookmarkCount, err)
	}
	var afterCacheCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipes`).Scan(&afterCacheCount); err != nil || afterCacheCount != cachedCount {
		t.Fatalf("detail read changed cached row count from %d to %d err=%v", cachedCount, afterCacheCount, err)
	}
}

func TestLibraryRemoteRecipeDetailRejectsUnavailableMalformedAndWrongMethod(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now')`); err != nil {
		t.Fatal(err)
	}
	handler := apiHandler{db: db}
	uri := testFederationOrigin + "/federation/recipes/" + testFederationID
	for _, test := range []struct {
		name   string
		method string
		query  string
		user   string
		want   int
	}{
		{name: "missing selector", method: http.MethodGet, query: "", user: "viewer", want: http.StatusBadRequest},
		{name: "duplicate selector", method: http.MethodGet, query: "remote_object_uri=" + url.QueryEscape(uri) + "&remote_object_uri=" + url.QueryEscape(uri), user: "viewer", want: http.StatusBadRequest},
		{name: "extra selector", method: http.MethodGet, query: "remote_object_uri=" + url.QueryEscape(uri) + "&extra=1", user: "viewer", want: http.StatusBadRequest},
		{name: "malformed query escape", method: http.MethodGet, query: "remote_object_uri=" + url.QueryEscape(uri) + "&extra=%ZZ", user: "viewer", want: http.StatusBadRequest},
		{name: "malformed URI", method: http.MethodGet, query: "remote_object_uri=" + url.QueryEscape(uri+"?fetch=true"), user: "viewer", want: http.StatusBadRequest},
		{name: "anonymous", method: http.MethodGet, query: "remote_object_uri=" + url.QueryEscape(uri), want: http.StatusUnauthorized},
		{name: "wrong method", method: http.MethodPost, query: "remote_object_uri=" + url.QueryEscape(uri), user: "viewer", want: http.StatusMethodNotAllowed},
		{name: "missing cache", method: http.MethodGet, query: "remote_object_uri=" + url.QueryEscape(uri), user: "viewer", want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := remoteRecipeDetailRequest(t, handler, test.user, test.method, test.query)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body)
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestLibraryRemoteRecipeDetailHidesSourceMismatchAndFailsOnCorruptCache(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now');
		INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	uri := testFederationOrigin + "/federation/recipes/" + testFederationID
	body := `{"title":"Cached dish","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, uri, testFederationOrigin, testFederationID, 1, "Creator", body, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	handler := apiHandler{db: db}
	query := "remote_object_uri=" + url.QueryEscape(uri)
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET source_recipe_id='1123456789abcdef0123456789abcdef' WHERE object_id=?`, uri); err != nil {
		t.Fatal(err)
	}
	response := remoteRecipeDetailRequest(t, handler, "viewer", http.MethodGet, query)
	if response.Code != http.StatusNotFound {
		t.Fatalf("source mismatch status=%d body=%s", response.Code, response.Body)
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET source_recipe_id=?,recipe_json='{"title":"ok","unknown":"invalid"}' WHERE object_id=?`, testFederationID, uri); err != nil {
		t.Fatal(err)
	}
	response = remoteRecipeDetailRequest(t, handler, "viewer", http.MethodGet, query)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt cache status=%d body=%s", response.Code, response.Body)
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET recipe_json=?,creator_name='' WHERE object_id=?`, body, uri); err != nil {
		t.Fatal(err)
	}
	response = remoteRecipeDetailRequest(t, handler, "viewer", http.MethodGet, query)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt metadata status=%d body=%s", response.Code, response.Body)
	}
}

func TestLibraryRemoteRecipeDetailStorageFailureIsInternalServerError(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.test','Viewer','now')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	uri := testFederationOrigin + "/federation/recipes/" + testFederationID
	response := remoteRecipeDetailRequest(t, apiHandler{db: db}, "viewer", http.MethodGet, "remote_object_uri="+url.QueryEscape(uri))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("closed database status=%d body=%s", response.Code, response.Body)
	}
}
