package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func insertRecipeSharingFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO users(id,email,display_name,created_at) VALUES
			('owner','owner@example.test','Recipe Owner','now'),
			('editor','editor@example.test','Recipe Editor','now'),
			('hidden-owner','hidden@example.test','Hidden Book Owner','now'),
			('reader','reader@example.test','Book Reader','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES
			('source-book','owner','Source book','private','now','now'),
			('hidden-book','hidden-owner','Hidden book','private','now','now'),
			('owner-extra','owner','Owner extra','private','now','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES
			('source-book','editor','editor','now'),
			('owner-extra','reader','viewer','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
			VALUES('sharing-recipe','owner','Recipe Owner','Private soup','','[]','[]','[]','private','cookbook_editors',1,'now','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES
			('source-book','sharing-recipe','owner','now'),
			('hidden-book','sharing-recipe','hidden-owner','now');
	`); err != nil {
		t.Fatal(err)
	}
}

func recipeContentUpdate(title string, version int) map[string]any {
	return map[string]any{
		"title": title, "description": "Updated description",
		"ingredients": []map[string]string{{"name": "beans"}},
		"steps":       []map[string]string{{"text": "Cook"}},
		"version":     version, "visibility": "private",
	}
}

func TestCookbookEditorCannotExpandPrivateRecipeAudience(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`
		INSERT INTO users(id,email,display_name,created_at) VALUES
			('owner','owner@example.test','Recipe Owner','now'),
			('editor','editor@example.test','Recipe Editor','now'),
			('recipient','recipient@example.test','New Reader','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES
			('source-book','owner','Source book','private','now','now'),
			('editor-book','editor','Editor book','private','now','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES
			('source-book','editor','editor','now'),
			('editor-book','recipient','viewer','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
			VALUES('0123456789abcdef0123456789abcdef','owner','Recipe Owner','Private soup','','[]','[]','[]','private','cookbook_editors',1,'now','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at)
			VALUES('source-book','0123456789abcdef0123456789abcdef','owner','now');
		UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin='https://cookbook.example.test' WHERE id=1;
		INSERT INTO federation_peers(origin,enabled,created_at,updated_at)
			VALUES('https://peer.example.test',1,'now','now');
		INSERT INTO federation_local_peer_objects(peer_origin,recipe_id,published,updated_at)
			VALUES('https://peer.example.test','0123456789abcdef0123456789abcdef',1,'now');
	`); err != nil {
		t.Fatal(err)
	}

	editorToken, _, err := issueSession(db, "editor")
	if err != nil {
		t.Fatal(err)
	}
	requestCookie := &http.Cookie{Name: sessionCookie, Value: editorToken}
	handler := handlerWithAuth(db, testAuth(db))
	readerToken, _, err := issueSession(db, "recipient")
	if err != nil {
		t.Fatal(err)
	}
	readerCookie := &http.Cookie{Name: sessionCookie, Value: readerToken}
	readerBefore := callAPI(handler, http.MethodGet, "/api/recipes/0123456789abcdef0123456789abcdef", nil, readerCookie)
	if readerBefore.Code != http.StatusNotFound {
		t.Fatalf("unlinked cookbook member read status=%d, want 404: %s", readerBefore.Code, readerBefore.Body)
	}

	var revisionsBefore, outboxBefore, localObjectsBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&revisionsBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outboxBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_local_objects`).Scan(&localObjectsBefore); err != nil {
		t.Fatal(err)
	}

	update := map[string]any{
		"title":        "Changed while resharing",
		"ingredients":  []map[string]string{{"name": "beans"}},
		"steps":        []map[string]string{{"text": "Cook"}},
		"version":      1,
		"visibility":   "private",
		"cookbook_ids": []string{"source-book", "editor-book"},
	}
	response := callAPI(handler, http.MethodPut, "/api/recipes/0123456789abcdef0123456789abcdef", update, requestCookie)
	if response.Code != http.StatusForbidden {
		t.Errorf("editor audience expansion status=%d, want 403: %s", response.Code, response.Body)
	}

	var version int
	var title string
	if err := db.QueryRow(`SELECT version,title FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&version, &title); err != nil {
		t.Fatal(err)
	}
	var editorBookLink, revisionsAfter, outboxAfter, localObjectsAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE cookbook_id='editor-book' AND recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&editorBookLink); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&revisionsAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outboxAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_local_objects`).Scan(&localObjectsAfter); err != nil {
		t.Fatal(err)
	}
	if version != 1 || title != "Private soup" || editorBookLink != 0 || revisionsAfter != revisionsBefore || outboxAfter != outboxBefore || localObjectsAfter != localObjectsBefore {
		t.Errorf("denied share changed recipe state: version=%d title=%q link=%d revisions=%d/%d outbox=%d/%d federation_objects=%d/%d", version, title, editorBookLink, revisionsAfter, revisionsBefore, outboxAfter, outboxBefore, localObjectsAfter, localObjectsBefore)
	}

	readerAfter := callAPI(handler, http.MethodGet, "/api/recipes/0123456789abcdef0123456789abcdef", nil, readerCookie)
	if readerAfter.Code != http.StatusNotFound {
		t.Errorf("unlinked cookbook member read after denied share status=%d, want 404: %s", readerAfter.Code, readerAfter.Body)
	}
}

func TestCookbookEditorPreservesHiddenReferencesOnBodyEdit(t *testing.T) {
	db := testDatabase(t)
	insertRecipeSharingFixture(t, db)
	editorToken, _, err := issueSession(db, "editor")
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	editorCookie := &http.Cookie{Name: sessionCookie, Value: editorToken}

	detail := callAPI(handler, http.MethodGet, "/api/recipes/sharing-recipe", nil, editorCookie)
	var editorView recipe
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &editorView) != nil {
		t.Fatalf("editor recipe detail status=%d: %s", detail.Code, detail.Body)
	}
	if editorView.CanManageSharing || len(editorView.CookbookIDs) != 1 || editorView.CookbookIDs[0] != "source-book" {
		t.Fatalf("editor sharing projection = manage:%v refs:%v; want false and only source-book", editorView.CanManageSharing, editorView.CookbookIDs)
	}
	forgedCapability := recipeContentUpdate("Forged sharing capability", 1)
	forgedCapability["can_manage_sharing"] = true
	forged := callAPI(handler, http.MethodPut, "/api/recipes/sharing-recipe", forgedCapability, editorCookie)
	if forged.Code != http.StatusBadRequest || forged.Body.String() != "{\"error\":\"invalid_json\"}\n" {
		t.Fatalf("client-supplied sharing capability status=%d body=%s", forged.Code, forged.Body)
	}
	list := callAPI(handler, http.MethodGet, "/api/recipes?limit=24&offset=0", nil, editorCookie)
	var listed []recipe
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].CanManageSharing {
		t.Fatalf("editor recipe-list sharing capability status=%d recipes=%+v", list.Code, listed)
	}
	ownerDetail := callAPI(handler, http.MethodGet, "/api/recipes/sharing-recipe", nil, &http.Cookie{Name: sessionCookie, Value: ownerToken})
	var ownerView recipe
	if ownerDetail.Code != http.StatusOK || json.Unmarshal(ownerDetail.Body.Bytes(), &ownerView) != nil || !ownerView.CanManageSharing {
		t.Fatalf("owner sharing projection status=%d view=%+v", ownerDetail.Code, ownerView)
	}
	ownerList := callAPI(handler, http.MethodGet, "/api/recipes?limit=24&offset=0", nil, &http.Cookie{Name: sessionCookie, Value: ownerToken})
	var ownerListed []recipe
	if ownerList.Code != http.StatusOK || json.Unmarshal(ownerList.Body.Bytes(), &ownerListed) != nil || len(ownerListed) != 1 || !ownerListed[0].CanManageSharing {
		t.Fatalf("owner recipe-list sharing capability status=%d recipes=%+v", ownerList.Code, ownerListed)
	}

	withoutReferences := recipeContentUpdate("Body edit without sharing field", 1)
	response := callAPI(handler, http.MethodPut, "/api/recipes/sharing-recipe", withoutReferences, editorCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("body update with omitted sharing input status=%d: %s", response.Code, response.Body)
	}
	assertRecipeSharingFixtureLinks(t, db, 2)

	withVisibleEcho := recipeContentUpdate("Body edit with visible reference echo", 2)
	withVisibleEcho["cookbook_ids"] = []string{"source-book"}
	response = callAPI(handler, http.MethodPut, "/api/recipes/sharing-recipe", withVisibleEcho, editorCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("body update with unchanged visible-reference echo status=%d: %s", response.Code, response.Body)
	}
	assertRecipeSharingFixtureLinks(t, db, 2)

	removeVisibleReference := recipeContentUpdate("Unauthorized unlink", 3)
	removeVisibleReference["cookbook_ids"] = []string{}
	response = callAPI(handler, http.MethodPut, "/api/recipes/sharing-recipe", removeVisibleReference, editorCookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("editor link removal status=%d, want 403: %s", response.Code, response.Body)
	}
	assertRecipeSharingFixtureLinks(t, db, 2)

	var version int
	var title string
	if err := db.QueryRow(`SELECT version,title FROM recipes WHERE id='sharing-recipe'`).Scan(&version, &title); err != nil {
		t.Fatal(err)
	}
	if version != 3 || title != "Body edit with visible reference echo" {
		t.Fatalf("editor body edits version=%d title=%q", version, title)
	}
}

func TestRecipeOwnerCanManageCookbookReferences(t *testing.T) {
	db := testDatabase(t)
	insertRecipeSharingFixture(t, db)
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	readerToken, _, err := issueSession(db, "reader")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	readerCookie := &http.Cookie{Name: sessionCookie, Value: readerToken}

	detail := callAPI(handler, http.MethodGet, "/api/recipes/sharing-recipe", nil, ownerCookie)
	var ownerView recipe
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &ownerView) != nil || !ownerView.CanManageSharing {
		t.Fatalf("owner sharing projection status=%d view=%+v", detail.Code, ownerView)
	}
	update := recipeContentUpdate("Owner shared intentionally", 1)
	update["cookbook_ids"] = []string{"source-book", "owner-extra"}
	response := callAPI(handler, http.MethodPut, "/api/recipes/sharing-recipe", update, ownerCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("owner sharing update status=%d: %s", response.Code, response.Body)
	}
	var shared int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE recipe_id='sharing-recipe' AND cookbook_id='owner-extra'`).Scan(&shared); err != nil || shared != 1 {
		t.Fatalf("owner-added cookbook reference count=%d err=%v", shared, err)
	}
	reader := callAPI(handler, http.MethodGet, "/api/recipes/sharing-recipe", nil, readerCookie)
	if reader.Code != http.StatusOK {
		t.Fatalf("member of owner-selected cookbook read status=%d: %s", reader.Code, reader.Body)
	}
	assertRecipeSharingFixtureLinks(t, db, 3)
}

func TestCookbookEditorCannotRestoreOwnerOnlyVisibilityChange(t *testing.T) {
	db := testDatabase(t)
	insertRecipeSharingFixture(t, db)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,created_at)
		VALUES('public-revision','sharing-recipe',1,'owner','Public snapshot','','[]','[]',1,0,0,'[]','public','now')`); err != nil {
		t.Fatal(err)
	}
	editorToken, _, err := issueSession(db, "editor")
	if err != nil {
		t.Fatal(err)
	}
	response := callAPI(handlerWithAuth(db, testAuth(db)), http.MethodPost, "/api/recipes/sharing-recipe/revisions/1/restore", map[string]int{"expected_version": 1}, &http.Cookie{Name: sessionCookie, Value: editorToken})
	if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":\"visibility_owner_only\"}\n" {
		t.Fatalf("editor visibility restore status=%d body=%s", response.Code, response.Body)
	}
	var visibilityValue visibility
	var version int
	if err := db.QueryRow(`SELECT visibility,version FROM recipes WHERE id='sharing-recipe'`).Scan(&visibilityValue, &version); err != nil {
		t.Fatal(err)
	}
	if visibilityValue != visibilityPrivate || version != 1 {
		t.Fatalf("denied editor restore persisted visibility=%q version=%d", visibilityValue, version)
	}
}

func TestRecipeMutationRechecksEditorMembershipInsideTransaction(t *testing.T) {
	db := testDatabase(t)
	insertRecipeSharingFixture(t, db)
	var ownerID, editPolicy string
	if err := db.QueryRow(`SELECT owner_id,edit_policy FROM recipes WHERE id='sharing-recipe'`).Scan(&ownerID, &editPolicy); err != nil {
		t.Fatal(err)
	}
	if !canEditRecipe("editor", access{ownerID: ownerID, memberRole: recipeMemberRole(db, "sharing-recipe", "editor")}, editPolicy) {
		t.Fatal("editor preflight should allow the initial state")
	}
	if _, err := db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id='source-book' AND user_id='editor'`); err != nil {
		t.Fatal(err)
	}
	_, err := commitRecipeMutation(db, recipeMutation{
		RecipeID: "sharing-recipe", ActorID: "editor", ExpectedVersion: 1,
		Snapshot: recipeMutationSnapshot{Title: "Revoked editor update", Visibility: visibilityPrivate, IngredientsJSON: []byte("[]"), StepsJSON: []byte("[]"), TagsJSON: []byte("[]")},
	})
	if !errors.Is(err, errRecipeMutationDenied) {
		t.Fatalf("mutation after revocation error=%v, want authorization denial", err)
	}
	var title string
	var version, revisions int
	if err := db.QueryRow(`SELECT title,version FROM recipes WHERE id='sharing-recipe'`).Scan(&title, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='sharing-recipe'`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if title != "Private soup" || version != 1 || revisions != 0 {
		t.Fatalf("revoked editor mutation persisted: title=%q version=%d revisions=%d", title, version, revisions)
	}
}

func TestRecipeMutationRechecksCurrentOwnerInsideTransaction(t *testing.T) {
	db := testDatabase(t)
	insertRecipeSharingFixture(t, db)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('new-owner','new-owner@example.test','New Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if !canEditRecipe("owner", access{ownerID: "owner"}, "owner_only") {
		t.Fatal("preflight should recognize the original owner")
	}
	if _, err := db.Exec(`UPDATE recipes SET owner_id='new-owner' WHERE id='sharing-recipe'`); err != nil {
		t.Fatal(err)
	}
	_, err := commitRecipeMutation(db, recipeMutation{
		RecipeID: "sharing-recipe", ActorID: "owner", ExpectedVersion: 1,
		Snapshot: recipeMutationSnapshot{Title: "Former owner update", Visibility: visibilityPrivate, IngredientsJSON: []byte("[]"), StepsJSON: []byte("[]"), TagsJSON: []byte("[]")},
	})
	if !errors.Is(err, errRecipeMutationDenied) {
		t.Fatalf("mutation after transfer error=%v, want authorization denial", err)
	}
	var title string
	var version int
	if err := db.QueryRow(`SELECT title,version FROM recipes WHERE id='sharing-recipe'`).Scan(&title, &version); err != nil {
		t.Fatal(err)
	}
	if title != "Private soup" || version != 1 {
		t.Fatalf("former-owner mutation persisted: title=%q version=%d", title, version)
	}
}

func assertRecipeSharingFixtureLinks(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got, source, hidden int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE recipe_id='sharing-recipe'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE recipe_id='sharing-recipe' AND cookbook_id='source-book'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE recipe_id='sharing-recipe' AND cookbook_id='hidden-book'`).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	if got != want || source != 1 || hidden != 1 {
		t.Fatalf("recipe cookbook link count=%d source=%d hidden=%d, want total %d with both original links", got, source, hidden, want)
	}
}
