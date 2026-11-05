package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func assertPublisherProfileRequired(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusConflict || response.Body.String() != "{\"error\":\"publisher_profile_required\"}\n" {
		t.Fatalf("publisher profile response=%d %s", response.Code, response.Body)
	}
}

func TestPublishedRecipeCreationRequiresCompletedProfileButDraftDoesNot(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	request := map[string]any{
		"title": "Public soup", "ingredients": []ingredient{{Name: "beans"}},
		"steps": []recipeStep{{Text: "Cook"}}, "visibility": "public", "confirm_public_access": true,
		"initial_publication": map[string]any{"publication_state": "published", "federation_mode": "off", "index_eligible": false},
	}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPost, "/api/recipes", request, cookie))
	var recipes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes`).Scan(&recipes); err != nil || recipes != 0 {
		t.Fatalf("denied public create rows=%d err=%v", recipes, err)
	}

	draft := map[string]any{"title": "Private-profile draft", "ingredients": []ingredient{}, "steps": []recipeStep{}, "visibility": "public", "confirm_public_access": true}
	draftResponse := callAPI(handler, http.MethodPost, "/api/recipes", draft, cookie)
	if draftResponse.Code != http.StatusOK {
		t.Fatalf("unpublished public-visibility draft status=%d body=%s", draftResponse.Code, draftResponse.Body)
	}

	insertCompletedPublisherProfile(t, db, "owner", "Private publisher")
	publishedResponse := callAPI(handler, http.MethodPost, "/api/recipes", request, cookie)
	if publishedResponse.Code != http.StatusOK {
		t.Fatalf("profiled public recipe create status=%d body=%s", publishedResponse.Code, publishedResponse.Body)
	}
	var profileVisibility string
	if err := db.QueryRow(`SELECT visibility FROM creator_profiles WHERE user_id='owner'`).Scan(&profileVisibility); err != nil || profileVisibility != "private" {
		t.Fatalf("publisher profile visibility=%q err=%v", profileVisibility, err)
	}
}

func TestPublicCookbookCreationRequiresProfileAndPrivateCreationDoesNot(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	handler := handlerWithAuth(db, testAuth(db))
	privateBook := callAPI(handler, http.MethodPost, "/api/cookbooks", map[string]string{"title": "Private book", "visibility": "private"}, cookie)
	if privateBook.Code != http.StatusCreated {
		t.Fatalf("private cookbook create status=%d body=%s", privateBook.Code, privateBook.Body)
	}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPost, "/api/cookbooks", map[string]string{"title": "Public book", "visibility": "public"}, cookie))
	var publicBooks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE visibility='public'`).Scan(&publicBooks); err != nil || publicBooks != 0 {
		t.Fatalf("denied public cookbook count=%d err=%v", publicBooks, err)
	}
	insertCompletedPublisherProfile(t, db, "owner", "Private cookbook profile")
	publicBook := callAPI(handler, http.MethodPost, "/api/cookbooks", map[string]string{"title": "Public book", "visibility": "public"}, cookie)
	if publicBook.Code != http.StatusCreated {
		t.Fatalf("profiled public cookbook create status=%d body=%s", publicBook.Code, publicBook.Body)
	}
}

func TestRecipePublicVersionUsesActingEditorProfileForPutAndRestore(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('editor','editor@example.test','Editor','now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('book','owner','Book','private','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES('recipe','owner','Original creator','Soup','[]','[]','[]','public','cookbook_editors',1,'now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at,published_at)
		VALUES('recipe','published','off',0,'now','now');
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,tags_json,visibility,created_at)
		VALUES('revision','recipe',1,'owner','Soup','','[]','[]','[]','public','now');
		INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('book','recipe','owner','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('book','editor','editor','now')`); err != nil {
		t.Fatal(err)
	}
	editorToken, _, err := issueSession(db, "editor")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	editorCookie := &http.Cookie{Name: sessionCookie, Value: editorToken}
	insertCompletedPublisherProfile(t, db, "owner", "Owner profile")
	putBody := map[string]any{"title": "Edited soup", "ingredients": []ingredient{}, "steps": []recipeStep{}, "tags": []string{}, "visibility": "public", "version": 1}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPut, "/api/recipes/recipe", putBody, editorCookie))
	restoreBody := map[string]int{"expected_version": 1}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPost, "/api/recipes/recipe/revisions/1/restore", restoreBody, editorCookie))
	var version int
	if err := db.QueryRow(`SELECT version FROM recipes WHERE id='recipe'`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("denied editor mutation version=%d err=%v", version, err)
	}
	insertCompletedPublisherProfile(t, db, "editor", "Editor private profile")
	put := callAPI(handler, http.MethodPut, "/api/recipes/recipe", putBody, editorCookie)
	if put.Code != http.StatusOK {
		t.Fatalf("profiled editor PUT status=%d body=%s", put.Code, put.Body)
	}
	var updated recipe
	if err := json.Unmarshal(put.Body.Bytes(), &updated); err != nil || updated.Version != 2 || updated.CreatorName != "Original creator" {
		t.Fatalf("edited public recipe=%+v err=%v", updated, err)
	}
	restore := callAPI(handler, http.MethodPost, "/api/recipes/recipe/revisions/1/restore", map[string]int{"expected_version": 2}, editorCookie)
	if restore.Code != http.StatusOK {
		t.Fatalf("profiled editor restore status=%d body=%s", restore.Code, restore.Body)
	}
}

func TestRecipePublishedOwnerPutRequiresAndAcceptsProfile(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES('recipe','owner','Original creator','Soup','[]','[]','[]','public','owner_only',1,'now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at,published_at)
		VALUES('recipe','published','off',0,'now','now');
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,tags_json,visibility,created_at)
		VALUES('revision','recipe',1,'owner','Soup','','[]','[]','[]','public','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	handler := handlerWithAuth(db, testAuth(db))
	body := map[string]any{"title": "Edited soup", "ingredients": []ingredient{}, "steps": []recipeStep{}, "tags": []string{}, "visibility": "public", "version": 1}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPut, "/api/recipes/recipe", body, cookie))
	insertCompletedPublisherProfile(t, db, "owner", "Private owner profile")
	updated := callAPI(handler, http.MethodPut, "/api/recipes/recipe", body, cookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("profiled owner PUT status=%d body=%s", updated.Code, updated.Body)
	}
	restored := callAPI(handler, http.MethodPost, "/api/recipes/recipe/revisions/1/restore", map[string]int{"expected_version": 2}, cookie)
	if restored.Code != http.StatusOK {
		t.Fatalf("profiled owner restore status=%d body=%s", restored.Code, restored.Body)
	}
	if strings.Contains(updated.Body.String(), "Private owner profile") || strings.Contains(restored.Body.String(), "Private owner profile") {
		t.Fatalf("private publisher profile leaked from recipe write: %s / %s", updated.Body, restored.Body)
	}
}

func TestRecipePublicationAndFederationExpansionRequireProfileOnlyWhenAudienceGrows(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationUnpublished, RecipeFederationOff, false)
	view, etag := publicationGET(t, env)
	if denied := publicationPUT(t, env, env.owner, etag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff}); denied.Code != http.StatusConflict || denied.Body.String() != "{\"error\":\"publisher_profile_required\"}\n" {
		t.Fatalf("publish without profile status=%d body=%s", denied.Code, denied.Body)
	}
	view, etag = publicationGET(t, env)
	if view.PublicationState != RecipePublicationUnpublished {
		t.Fatalf("denied publication changed state: %+v", view)
	}
	insertCompletedPublisherProfile(t, env.db, "owner", "Private publisher")
	if published := publicationPUT(t, env, env.owner, etag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff}); published.Code != http.StatusOK {
		t.Fatalf("profiled publish status=%d body=%s", published.Code, published.Body)
	}
	view, etag = publicationGET(t, env)
	if view.PublicationState != RecipePublicationPublished || view.FederationMode != RecipeFederationOff {
		t.Fatalf("unexpected publication after profile gate: %+v", view)
	}

	federated := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationOff, true)
	_, federationETag := publicationGET(t, federated)
	if denied := publicationPUT(t, federated, federated.owner, federationETag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationFollowers}); denied.Code != http.StatusConflict || denied.Body.String() != "{\"error\":\"publisher_profile_required\"}\n" {
		t.Fatalf("federation expansion without profile status=%d body=%s", denied.Code, denied.Body)
	}
	insertCompletedPublisherProfile(t, federated.db, "owner", "Private publisher")
	_, federationETag = publicationGET(t, federated)
	if expanded := publicationPUT(t, federated, federated.owner, federationETag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationFollowers}); expanded.Code != http.StatusOK {
		t.Fatalf("profiled federation expansion status=%d body=%s", expanded.Code, expanded.Body)
	}
}

func TestPublicCourseLessonWritesUsePointInTimeProfileCheck(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,courses_enabled=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO courses(id,owner_id,title,visibility,created_at,updated_at) VALUES('legacy-public','owner','Existing public course','public','now','now');
		INSERT INTO course_lessons(id,course_id,title,content,position,created_at,updated_at) VALUES('legacy-lesson','legacy-public','Lesson','Original',0,'now','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	handler := handlerWithAuth(db, testAuth(db))
	privateCourse := callAPI(handler, http.MethodPost, "/api/courses", map[string]string{"title": "Private course", "visibility": "private"}, cookie)
	if privateCourse.Code != http.StatusCreated {
		t.Fatalf("private course create status=%d body=%s", privateCourse.Code, privateCourse.Body)
	}
	var private course
	if err := json.Unmarshal(privateCourse.Body.Bytes(), &private); err != nil {
		t.Fatal(err)
	}
	privateLesson := callAPI(handler, http.MethodPost, "/api/courses/"+private.ID+"/lessons", map[string]any{"title": "Draft lesson", "content": "Private text", "position": 0}, cookie)
	if privateLesson.Code != http.StatusCreated {
		t.Fatalf("private lesson create status=%d body=%s", privateLesson.Code, privateLesson.Body)
	}
	publicCourse := callAPI(handler, http.MethodPost, "/api/courses", map[string]string{"title": "New public course", "visibility": "public"}, cookie)
	assertPublisherProfileRequired(t, publicCourse)
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPatch, "/api/courses/legacy-public", map[string]string{"title": "Changed course"}, cookie))
	post := map[string]any{"title": "New lesson", "content": "New content", "position": 1}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPost, "/api/courses/legacy-public/lessons", post, cookie))
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPatch, "/api/courses/legacy-public/lessons/legacy-lesson", map[string]string{"content": "Changed"}, cookie))
	var content string
	if err := db.QueryRow(`SELECT content FROM course_lessons WHERE id='legacy-lesson'`).Scan(&content); err != nil || content != "Original" {
		t.Fatalf("denied lesson PATCH stored %q err=%v", content, err)
	}
	insertCompletedPublisherProfile(t, db, "owner", "Private course publisher")
	created := callAPI(handler, http.MethodPost, "/api/courses", map[string]string{"title": "New public course", "visibility": "public"}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("profiled public course create status=%d body=%s", created.Code, created.Body)
	}
	updatedCourse := callAPI(handler, http.MethodPatch, "/api/courses/legacy-public", map[string]string{"title": "Updated course"}, cookie)
	if updatedCourse.Code != http.StatusOK {
		t.Fatalf("profiled public course update status=%d body=%s", updatedCourse.Code, updatedCourse.Body)
	}
	createdLesson := callAPI(handler, http.MethodPost, "/api/courses/legacy-public/lessons", post, cookie)
	if createdLesson.Code != http.StatusCreated {
		t.Fatalf("profiled public lesson create status=%d body=%s", createdLesson.Code, createdLesson.Body)
	}
	cleared := callAPI(handler, http.MethodPut, "/api/account/creator-profile", map[string]string{"public_name": "", "bio": "", "visibility": "private"}, cookie)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clearing published profile status=%d body=%s", cleared.Code, cleared.Body)
	}
	clearedEdit := callAPI(handler, http.MethodPatch, "/api/courses/legacy-public/lessons/legacy-lesson", map[string]string{"content": "Changed"}, cookie)
	assertPublisherProfileRequired(t, clearedEdit)
	stillOriginal := callAPI(handler, http.MethodGet, "/api/public/courses/legacy-public/lessons", nil, nil)
	if stillOriginal.Code != http.StatusOK || !strings.Contains(stillOriginal.Body.String(), "Original") {
		t.Fatalf("clearing publisher profile changed existing public lesson: status=%d body=%s", stillOriginal.Code, stillOriginal.Body)
	}
	deleted := callAPI(handler, http.MethodDelete, "/api/courses/legacy-public/lessons/legacy-lesson", nil, cookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("lesson withdrawal without profile status=%d body=%s", deleted.Code, deleted.Body)
	}
}

func TestAudienceSettingsCheckOnlyOwnersNewlyExposedInResultingState(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('admin','admin@example.test','Admin',1,'now'),('publisher','publisher@example.test','Publisher',0,'now'),('second-publisher','second@example.test','Second Publisher',0,'now'),('course-owner','course@example.test','Course Owner',0,'now');
		INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES
		('public-book','publisher','Public book','public','now','now'),('second-book','second-publisher','Second public book','public','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at)
		VALUES('public-recipe','publisher','Original','Public recipe','[]','[]','[]','public','owner_only','now','now');
		INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at,published_at)
		VALUES('public-recipe','published','off',0,'now','now');
		INSERT INTO courses(id,owner_id,title,visibility,created_at,updated_at) VALUES('public-course','course-owner','Public course','public','now','now')`); err != nil {
		t.Fatal(err)
	}
	insertCompletedPublisherProfile(t, db, "admin", "Admin private profile")
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: adminToken}
	handler := handlerWithAuth(db, testAuth(db))
	confirmedPublic := map[string]any{"public_instance": true, "confirm_publication": true}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPatch, "/api/admin/settings", confirmedPublic, cookie))
	var public, courses int
	if err := db.QueryRow(`SELECT public_instance,courses_enabled FROM instance_settings WHERE id=1`).Scan(&public, &courses); err != nil || public != 0 || courses != 0 {
		t.Fatalf("denied setting update persisted public=%d courses=%d err=%v", public, courses, err)
	}
	insertCompletedPublisherProfile(t, db, "publisher", "Private publisher")
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPatch, "/api/admin/settings", confirmedPublic, cookie))
	insertCompletedPublisherProfile(t, db, "second-publisher", "Private second publisher")
	enabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", confirmedPublic, cookie)
	if enabled.Code != http.StatusOK {
		t.Fatalf("public mode should ignore course owner while courses disabled: %d %s", enabled.Code, enabled.Body)
	}
	confirmedCourses := map[string]any{"courses_enabled": true, "confirm_publication": true}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPatch, "/api/admin/settings", confirmedCourses, cookie))
	if err := db.QueryRow(`SELECT public_instance,courses_enabled FROM instance_settings WHERE id=1`).Scan(&public, &courses); err != nil || public != 1 || courses != 0 {
		t.Fatalf("denied course activation changed settings public=%d courses=%d err=%v", public, courses, err)
	}
	insertCompletedPublisherProfile(t, db, "course-owner", "Private course publisher")
	if enabledCourses := callAPI(handler, http.MethodPatch, "/api/admin/settings", confirmedCourses, cookie); enabledCourses.Code != http.StatusOK {
		t.Fatalf("course activation with owner profile status=%d body=%s", enabledCourses.Code, enabledCourses.Body)
	}
	if _, err := db.Exec(`UPDATE creator_profiles SET public_name='' WHERE user_id='publisher'`); err != nil {
		t.Fatal(err)
	}
	unrelated := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]string{"registration_policy": "open"}, cookie)
	if unrelated.Code != http.StatusOK {
		t.Fatalf("unrelated settings update rechecked existing publisher profiles: status=%d body=%s", unrelated.Code, unrelated.Body)
	}
}

func TestCourseAudienceActivationUsesProspectiveCombinedSettings(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('admin','admin@example.test','Admin',1,'now'),('course-owner','course@example.test','Course Owner',0,'now');
		INSERT INTO courses(id,owner_id,title,visibility,created_at,updated_at) VALUES('public-course','course-owner','Public course','public','now','now')`); err != nil {
		t.Fatal(err)
	}
	insertCompletedPublisherProfile(t, db, "admin", "Admin private profile")
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: adminToken}
	handler := handlerWithAuth(db, testAuth(db))
	privateInstanceCourses := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]bool{"courses_enabled": true}, cookie)
	if privateInstanceCourses.Code != http.StatusOK {
		t.Fatalf("enabling courses on private instance should not expose course: %d %s", privateInstanceCourses.Code, privateInstanceCourses.Body)
	}
	assertPublisherProfileRequired(t, callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"public_instance": true, "confirm_publication": true}, cookie))
	combined := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"courses_enabled": true, "public_instance": true, "confirm_publication": true}, cookie)
	assertPublisherProfileRequired(t, combined)
	var public, courses int
	if err := db.QueryRow(`SELECT public_instance,courses_enabled FROM instance_settings WHERE id=1`).Scan(&public, &courses); err != nil || public != 0 || courses != 1 {
		t.Fatalf("denied combined toggle changed settings public=%d courses=%d err=%v", public, courses, err)
	}
	insertCompletedPublisherProfile(t, db, "course-owner", "Private course owner")
	disabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]bool{"courses_enabled": false, "public_instance": false}, cookie)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disabling audience settings before combined case status=%d body=%s", disabled.Code, disabled.Body)
	}
	combined = callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"courses_enabled": true, "public_instance": true, "confirm_publication": true}, cookie)
	if combined.Code != http.StatusOK {
		t.Fatalf("combined audience activation with owner profile status=%d body=%s", combined.Code, combined.Body)
	}
}
