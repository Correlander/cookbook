package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func insertRecipeBookmarkFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('viewer','viewer@example.test','Viewer',0,'now'),
		('admin','admin@example.test','Admin',1,'now'),('other','other@example.test','Other',0,'now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Book','private','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES
		('shared','owner','Owner','Shared recipe','','[]','[]',2,5,10,'[]','private','cookbook_editors',1,'created','updated'),
		('private','owner','Owner','Private recipe','','[]','[]',2,5,10,'[]','private','owner_only',1,'created','updated');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','shared','owner','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('book','viewer','viewer','now');`)
	if err != nil {
		t.Fatal(err)
	}
}

func doBookmarkRequest(t *testing.T, h http.Handler, userID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := requestForUser(method, path, userID)
	if body != "" {
		request.Body = io.NopCloser(strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder
}

func TestRecipeBookmarksArePerAccountAndReauthorizeLocalReads(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	h := apiHandler{db: db}
	localTarget := `{"target":{"local_recipe_id":"shared"}}`
	response := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", localTarget)
	if response.Code != http.StatusCreated {
		t.Fatalf("save shared recipe status=%d body=%s", response.Code, response.Body)
	}
	var saved RecipeBookmarkView
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Availability != "available" || saved.TargetKind != "local" || saved.Target == nil || saved.LocalRecipe == nil || saved.LocalRecipe.ID != "shared" || saved.RemoteRecipe != nil {
		t.Fatalf("saved local projection=%+v", saved)
	}
	duplicate := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", localTarget)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate save status=%d body=%s", duplicate.Code, duplicate.Body)
	}
	var duplicateView RecipeBookmarkView
	if err := json.Unmarshal(duplicate.Body.Bytes(), &duplicateView); err != nil || duplicateView.ID != saved.ID {
		t.Fatalf("duplicate response=%+v err=%v", duplicateView, err)
	}
	if got := doBookmarkRequest(t, h, "admin", http.MethodPost, "/api/library/bookmarks", `{"target":{"local_recipe_id":"private"}}`); got.Code != http.StatusNotFound {
		t.Fatalf("admin private save status=%d body=%s, want 404", got.Code, got.Body)
	}
	if got := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"local_recipe_id":"private"}}`); got.Code != http.StatusNotFound {
		t.Fatalf("unrelated private save status=%d body=%s, want 404", got.Code, got.Body)
	}
	otherList := doBookmarkRequest(t, h, "other", http.MethodGet, "/api/library/bookmarks", "")
	var otherPage RecipeBookmarkPage
	if otherList.Code != http.StatusOK || json.Unmarshal(otherList.Body.Bytes(), &otherPage) != nil || otherPage.Total != 0 || len(otherPage.Items) != 0 {
		t.Fatalf("other account list status=%d page=%+v", otherList.Code, otherPage)
	}
	if got := doBookmarkRequest(t, h, "other", http.MethodDelete, "/api/library/bookmarks/"+saved.ID, ""); got.Code != http.StatusNotFound {
		t.Fatalf("cross-account delete status=%d body=%s, want 404", got.Code, got.Body)
	}
	if _, err := db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id='book' AND user_id='viewer'`); err != nil {
		t.Fatal(err)
	}
	list := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/bookmarks?limit=1&offset=0", "")
	var page RecipeBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Availability != "unavailable" || page.Items[0].Target != nil || page.Items[0].LocalRecipe != nil || page.Items[0].RemoteRecipe != nil {
		t.Fatalf("revoked reference was not redacted: %+v", page)
	}
	if got := doBookmarkRequest(t, h, "viewer", http.MethodDelete, "/api/library/bookmarks/"+saved.ID, ""); got.Code != http.StatusNoContent {
		t.Fatalf("owner delete status=%d body=%s", got.Code, got.Body)
	}
	var sourceCount, bookmarkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE id='shared'`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE id=?`, saved.ID).Scan(&bookmarkCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || bookmarkCount != 0 {
		t.Fatalf("delete changed source/bookmark counts=%d/%d", sourceCount, bookmarkCount)
	}
}

func TestRecipeBookmarksPublicAndRemoteCacheTargetsUseSeparateTypedViews(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
	UPDATE recipes SET visibility='public' WHERE id='private';
	INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('private','published','off',0,'now');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'now','now')`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	objectURI := testFederationOrigin + "/federation/recipes/" + testFederationID
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, objectURI, testFederationOrigin, testFederationID, 1, "Remote cook", `{"title":"Remote dish","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`, "2026-11-03T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	for _, test := range []struct{ body, wantKind string }{
		{`{"target":{"local_recipe_id":"private"}}`, "local"},
		{`{"target":{"remote_object_uri":"` + objectURI + `"}}`, "remote"},
	} {
		response := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", test.body)
		if response.Code != http.StatusCreated {
			t.Fatalf("save %s status=%d body=%s", test.wantKind, response.Code, response.Body)
		}
		var view RecipeBookmarkView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.TargetKind != test.wantKind || view.Availability != "available" {
			t.Fatalf("%s view=%+v", test.wantKind, view)
		}
		if test.wantKind == "local" && (view.LocalRecipe == nil || view.RemoteRecipe != nil) {
			t.Fatalf("local view has wrong typed projection: %+v", view)
		}
		if test.wantKind == "remote" && (view.RemoteRecipe == nil || view.LocalRecipe != nil || view.RemoteRecipe.Recipe.Title != "Remote dish") {
			t.Fatalf("remote view has wrong typed projection: %+v", view)
		}
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE recipe_bookmarks SET created_at=CASE target_kind WHEN 'local' THEN '2026-11-03T12:00:00Z' ELSE '2026-11-03T12:00:01Z' END WHERE user_id='viewer'`); err != nil {
		t.Fatal(err)
	}
	list := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/bookmarks?limit=1&offset=1", "")
	var page RecipeBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Total != 2 || page.Limit != 1 || page.Offset != 1 || len(page.Items) != 1 || page.Items[0].TargetKind != "local" || page.Items[0].Availability != "unavailable" {
		t.Fatalf("private-instance authenticated cache list status=%d body=%s", list.Code, list.Body)
	}
	if _, err := db.Exec(`UPDATE federation_remote_recipes SET deleted=1,recipe_json='' WHERE object_id=?`, objectURI); err != nil {
		t.Fatal(err)
	}
	remoteList := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/bookmarks?limit=2", "")
	if remoteList.Code != http.StatusOK || json.Unmarshal(remoteList.Body.Bytes(), &page) != nil {
		t.Fatalf("remote tombstone list status=%d body=%s", remoteList.Code, remoteList.Body)
	}
	var remoteUnavailable bool
	for _, item := range page.Items {
		if item.TargetKind == "remote" {
			remoteUnavailable = item.Availability == "unavailable" && item.Target == nil && item.RemoteRecipe == nil
		}
	}
	if !remoteUnavailable || page.Total != 2 {
		t.Fatalf("remote tombstone was not retained unavailable: %+v", page)
	}
}

func TestRecipeBookmarkRequiresExactTargetAndEnforcesPerAccountLimit(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	h := apiHandler{db: db}
	for _, body := range []string{
		`{"target":{}}`,
		`{"target":{"local_recipe_id":"shared","remote_object_uri":"https://cookbook.example.org/federation/recipes/0123456789abcdef0123456789abcdef"}}`,
		`{"target":{"local_recipe_id":"shared","unknown":true}}`,
	} {
		if got := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", body); got.Code != http.StatusBadRequest {
			t.Errorf("invalid target body %s status=%d body=%s", body, got.Code, got.Body)
		}
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,version,created_at,updated_at) VALUES('limit-recipe','viewer','Viewer','Limit test','[]','[]','[]','private',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	for batch := 0; batch < 10000; batch += 500 {
		statement := `INSERT INTO recipe_bookmarks(id,user_id,target_kind,local_instance_id,local_recipe_id,created_at) VALUES ` + strings.TrimSuffix(strings.Repeat(`(?,?, 'local', ?, ?, 'now'),`, 500), ",")
		args := make([]any, 0, 500*4)
		for i := batch; i < batch+500; i++ {
			args = append(args, newID(), "viewer", instanceID, "saved-"+newID())
		}
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatalf("fill bookmark cap at %d: %v", batch, err)
		}
	}
	response := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"local_recipe_id":"limit-recipe"}}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "bookmark_limit_reached") {
		t.Fatalf("cap response status=%d body=%s", response.Code, response.Body)
	}
}

func TestRecipeBookmarkDoesNotBlockHardTrashPurgeOrDeleteIndependentCopy(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,version,created_at,updated_at,deleted_at) VALUES
		('source','owner','Owner','Source','[]','[]','[]','private',1,'created','updated','2000-01-01T00:00:00Z'),
		('copy','owner','Owner','Independent copy','[]','[]','[]','private',1,'created','updated',NULL);`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	response := doBookmarkRequest(t, h, "owner", http.MethodPost, "/api/library/bookmarks", `{"target":{"local_recipe_id":"source"}}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("trashed source save status=%d body=%s, want not found", response.Code, response.Body)
	}
	if _, err := db.Exec(`UPDATE recipes SET deleted_at=NULL WHERE id='source'`); err != nil {
		t.Fatal(err)
	}
	response = doBookmarkRequest(t, h, "owner", http.MethodPost, "/api/library/bookmarks", `{"target":{"local_recipe_id":"source"}}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("source save status=%d body=%s", response.Code, response.Body)
	}
	var saved RecipeBookmarkView
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE recipes SET deleted_at='2000-01-01T00:00:00Z' WHERE id='source'`); err != nil {
		t.Fatal(err)
	}
	if err := purgeExpiredTrash(db, 1); err != nil {
		t.Fatalf("bookmark blocked hard trash purge: %v", err)
	}
	var sourceCount, copyCount, bookmarkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE id='source'`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE id='copy'`).Scan(&copyCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE id=?`, saved.ID).Scan(&bookmarkCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 || copyCount != 1 || bookmarkCount != 1 {
		t.Fatalf("after purge source/copy/bookmark counts=%d/%d/%d", sourceCount, copyCount, bookmarkCount)
	}
	list := doBookmarkRequest(t, h, "owner", http.MethodGet, "/api/library/bookmarks", "")
	var page RecipeBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Total != 1 || page.Items[0].Availability != "unavailable" || page.Items[0].Target != nil || page.Items[0].LocalRecipe != nil {
		t.Fatalf("purged source bookmark resolution status=%d page=%+v body=%s", list.Code, page, list.Body)
	}
}
