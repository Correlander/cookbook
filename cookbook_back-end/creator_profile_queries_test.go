package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreatorProfileListsApplyVisibilityAndResourceACLWithoutRoleBypass(t *testing.T) {
	db := testDatabase(t)
	ownerProfile := strings.Repeat("a", 32)
	privateProfile := strings.Repeat("b", 32)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('reader','reader@example.test','Reader',0,'now'),
		('admin','admin@example.test','Admin',1,'now'),('instance-owner','instance@example.test','Instance owner',0,'now')`)
	exec(`UPDATE instance_authority SET owner_user_id='instance-owner',owner_claimed_at='now',generation=1 WHERE singleton_id=1`)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'owner','Shared name','instance','now','now')`, ownerProfile)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'reader','Secret private name','private','now','now')`, privateProfile)
	exec(`INSERT INTO recipes(id,owner_id,creator_name,title,visibility,created_at,updated_at) VALUES
		('private-recipe','owner','','Owner only','private','now','now'),
		('member-recipe','owner','','Household','private','now','now'),
		('private-profile-recipe','reader','','Private profile resource','private','now','now')`)
	exec(`INSERT INTO cookbooks(id,owner_id,title,visibility,created_at,updated_at) VALUES('shared-book','owner','Shared book','private','now','now')`)
	exec(`INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('shared-book','reader','viewer','now')`)
	exec(`INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('shared-book','member-recipe','owner','now')`)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('11111111111111111111111111111111','private-recipe','local',?,'creator','self_asserted_at_creation','now')`, ownerProfile)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('22222222222222222222222222222222','member-recipe','local',?,'creator','self_asserted_at_creation','now')`, ownerProfile)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('33333333333333333333333333333333','private-profile-recipe','local',?,'creator','self_asserted_at_creation','now')`, privateProfile)
	exec(`INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at) VALUES('44444444444444444444444444444444','shared-book','local',?,'creator','local_record_creation','now')`, ownerProfile)
	h := apiHandler{db: db}
	readerRecipes := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+ownerProfile+"/recipes", "reader", nil)
	if readerRecipes.Code != http.StatusOK || !strings.Contains(readerRecipes.Body.String(), `"items":[`) || !strings.Contains(readerRecipes.Body.String(), `"role":"creator"`) {
		t.Fatalf("member recipe profile list status=%d body=%s", readerRecipes.Code, readerRecipes.Body)
	}
	var recipeList creatorProfileRecipeList
	if err := json.Unmarshal(readerRecipes.Body.Bytes(), &recipeList); err != nil {
		t.Fatal(err)
	}
	if recipeList.Profile.ID != ownerProfile || recipeList.Profile.PublicName != "Shared name" || len(recipeList.Items) != 1 || recipeList.Items[0].Recipe.ID != "member-recipe" || recipeList.Items[0].Recipe.CanEdit || recipeList.Items[0].Recipe.OwnerID != "" {
		t.Fatalf("creator recipe list escaped ACL or DTO rules: %+v", recipeList)
	}
	bookList := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+ownerProfile+"/cookbooks", "reader", nil)
	if bookList.Code != http.StatusOK || strings.Contains(bookList.Body.String(), `"owner_id"`) {
		t.Fatalf("creator cookbook projection status=%d body=%s", bookList.Code, bookList.Body)
	}
	var cookbookList creatorProfileCookbookList
	if err := json.Unmarshal(bookList.Body.Bytes(), &cookbookList); err != nil {
		t.Fatal(err)
	}
	if len(cookbookList.Items) != 1 || cookbookList.Items[0].Cookbook.ID != "shared-book" || cookbookList.Items[0].Role != "creator" {
		t.Fatalf("unexpected creator cookbook list: %+v", cookbookList)
	}
	for _, userID := range []string{"admin", "instance-owner"} {
		empty := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+ownerProfile+"/recipes", userID, nil)
		if empty.Code != http.StatusOK {
			t.Fatalf("%s cannot read instance profile: status=%d body=%s", userID, empty.Code, empty.Body)
		}
		var page creatorProfileRecipeList
		if err := json.Unmarshal(empty.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("%s role bypassed recipe ACL: %+v", userID, page.Items)
		}
		private := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+privateProfile+"/recipes", userID, nil)
		if private.Code != http.StatusNotFound || private.Body.String() != "{\"error\":\"not_found\"}\n" {
			t.Fatalf("%s role bypassed private profile: status=%d body=%s", userID, private.Code, private.Body)
		}
	}
	missing := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+strings.Repeat("c", 32)+"/recipes", "reader", nil)
	if missing.Code != http.StatusNotFound || missing.Body.String() != "{\"error\":\"not_found\"}\n" {
		t.Fatalf("missing profile list was not generic 404: status=%d body=%s", missing.Code, missing.Body)
	}
}

func TestVisibleCreatorProfileCanReturnNoReadableResources(t *testing.T) {
	db := testDatabase(t)
	creatorID := strings.Repeat("d", 32)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('reader','reader@example.test','Reader','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'owner','Household','instance','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,visibility,created_at,updated_at) VALUES('private-recipe','owner','','Private','private','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('55555555555555555555555555555555','private-recipe','local',?,'creator','self_asserted_at_creation','now')`, creatorID, creatorID); err != nil {
		t.Fatal(err)
	}
	response := callCreatorProfileAPI(t, apiHandler{db: db}, http.MethodGet, "/api/creator-profiles/"+creatorID+"/recipes", "reader", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("visible profile without resource access status=%d body=%s", response.Code, response.Body)
	}
}

func TestCreatorProfileCoursesRequireLocalCreditAndCurrentCourseAccess(t *testing.T) {
	db := testDatabase(t)
	profileID := strings.Repeat("a", 32)
	privateProfileID := strings.Repeat("b", 32)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('profile-owner','profile@example.test','Profile Owner',0,'now'),
		('course-owner','course@example.test','Course Owner',0,'now'),
		('reader','reader@example.test','Reader',0,'now'),
		('private-profile-owner','private@example.test','Private Owner',0,'now');
		UPDATE instance_settings SET public_instance=1,courses_enabled=1 WHERE id=1;
		INSERT INTO courses(id,owner_id,title,description,creator_name,visibility,created_at,updated_at) VALUES
		('course-own-private','profile-owner','Own private','private details','Byline','private','now','2026-11-01'),
		('course-public-a','course-owner','Public A','private details','Byline','public','now','2026-11-01'),
		('course-public-b','course-owner','Public B','private details','Byline','public','now','2026-11-01'),
		('course-other-private','course-owner','Other private','private details','Byline','private','now','2026-11-01'),
		('course-legacy','course-owner','Legacy','private details','Legacy byline','public','now','2026-11-01'),
		('course-imported','course-owner','Imported','private details','Imported byline','public','now','2026-11-01'),
		('course-private-profile','course-owner','Private profile course','private details','Private byline','public','now','2026-11-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES
		(?, 'profile-owner','Instance Profile','instance','now','now'),
		(?, 'private-profile-owner','Private Profile','private','now','now')`, profileID, privateProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at) VALUES
		('11111111111111111111111111111111','course-own-private','local',?,'creator','self_asserted_at_creation','now'),
		('22222222222222222222222222222222','course-public-a','local',?,'creator','self_asserted_at_creation','now'),
		('33333333333333333333333333333333','course-public-b','local',?,'creator','self_asserted_at_creation','now'),
		('44444444444444444444444444444444','course-other-private','local',?,'creator','self_asserted_at_creation','now'),
		('55555555555555555555555555555555','course-private-profile','local',?,'creator','self_asserted_at_creation','now')`, profileID, profileID, profileID, profileID, privateProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_attributions(id,course_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		VALUES('66666666666666666666666666666666','course-imported','external_claim',?,?,'Imported Identity Secret','creator','portable_claim','now')`, strings.Repeat("c", 32), strings.Repeat("d", 32)); err != nil {
		t.Fatal(err)
	}
	var privateCourseCreatorID string
	if err := db.QueryRow(`SELECT creator_id FROM content_attributions WHERE course_id='course-private-profile'`).Scan(&privateCourseCreatorID); err != nil || privateCourseCreatorID != privateProfileID {
		t.Fatalf("private profile fixture attribution=%q err=%v, want %q", privateCourseCreatorID, err, privateProfileID)
	}
	h := apiHandler{db: db}
	profilePath := "/api/creator-profiles/" + profileID + "/courses"
	ownerPage := callCreatorProfileAPI(t, h, http.MethodGet, profilePath+"?limit=2", "profile-owner", nil)
	if ownerPage.Code != http.StatusOK {
		t.Fatalf("profile owner status=%d body=%s", ownerPage.Code, ownerPage.Body)
	}
	if tooMany := callCreatorProfileAPI(t, h, http.MethodGet, profilePath+"?limit=101", "profile-owner", nil); tooMany.Code != http.StatusBadRequest {
		t.Fatalf("profile course list exceeded server page bound: status=%d body=%s", tooMany.Code, tooMany.Body)
	}
	var first creatorProfileCourseList
	if err := json.Unmarshal(ownerPage.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Profile.ID != profileID || first.Total != 3 || first.Limit != 2 || first.Offset != 0 || len(first.Items) != 2 {
		t.Fatalf("first page metadata=%+v", first)
	}
	if first.Items[0].Course.ID != "course-own-private" || first.Items[1].Course.ID != "course-public-a" {
		t.Fatalf("first page ordering or ACL=%+v", first.Items)
	}
	for _, forbidden := range []string{"owner_id", "owner_name", "creator_name", "description", "lesson_count", "Imported Identity Secret", "Legacy byline", "course-other-private", "course-legacy", "course-imported"} {
		if strings.Contains(ownerPage.Body.String(), forbidden) {
			t.Fatalf("minimal profile DTO exposed %q: %s", forbidden, ownerPage.Body)
		}
	}
	lastPage := callCreatorProfileAPI(t, h, http.MethodGet, profilePath+"?limit=2&offset=2", "profile-owner", nil)
	var last creatorProfileCourseList
	if lastPage.Code != http.StatusOK || json.Unmarshal(lastPage.Body.Bytes(), &last) != nil || last.Total != 3 || len(last.Items) != 1 || last.Items[0].Course.ID != "course-public-b" {
		t.Fatalf("last page status=%d body=%s result=%+v", lastPage.Code, lastPage.Body, last)
	}
	readerPage := callCreatorProfileAPI(t, h, http.MethodGet, profilePath, "reader", nil)
	var reader creatorProfileCourseList
	if readerPage.Code != http.StatusOK || json.Unmarshal(readerPage.Body.Bytes(), &reader) != nil || reader.Total != 2 || len(reader.Items) != 2 || reader.Items[0].Course.ID != "course-public-a" {
		t.Fatalf("cross-owner reader did not get only independently readable public courses: status=%d body=%s result=%+v", readerPage.Code, readerPage.Body, reader)
	}
	privateProfilePath := "/api/creator-profiles/" + privateProfileID + "/courses"
	if got := callCreatorProfileAPI(t, h, http.MethodGet, privateProfilePath, "reader", nil); got.Code != http.StatusNotFound || got.Body.String() != "{\"error\":\"not_found\"}\n" {
		t.Fatalf("private profile was disclosed: status=%d body=%s", got.Code, got.Body)
	}
	missingProfile := callCreatorProfileAPI(t, h, http.MethodGet, "/api/creator-profiles/"+strings.Repeat("f", 32)+"/courses", "reader", nil)
	if missingProfile.Code != http.StatusNotFound || missingProfile.Body.String() != "{\"error\":\"not_found\"}\n" {
		t.Fatalf("missing profile response differed from unavailable profile: status=%d body=%s", missingProfile.Code, missingProfile.Body)
	}
	privateOwnerPage := callCreatorProfileAPI(t, h, http.MethodGet, privateProfilePath, "private-profile-owner", nil)
	var privateOwner creatorProfileCourseList
	if privateOwnerPage.Code != http.StatusOK || json.Unmarshal(privateOwnerPage.Body.Bytes(), &privateOwner) != nil || len(privateOwner.Items) != 1 || privateOwner.Items[0].Course.ID != "course-private-profile" {
		t.Fatalf("profile owner could not read their private profile's attributed public course: status=%d body=%s result=%+v", privateOwnerPage.Code, privateOwnerPage.Body, privateOwner)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	privateInstancePage := callCreatorProfileAPI(t, h, http.MethodGet, profilePath, "reader", nil)
	var privateInstance creatorProfileCourseList
	if privateInstancePage.Code != http.StatusOK || json.Unmarshal(privateInstancePage.Body.Bytes(), &privateInstance) != nil || privateInstance.Total != 0 || len(privateInstance.Items) != 0 {
		t.Fatalf("course ACL ignored private-instance policy: status=%d body=%s result=%+v", privateInstancePage.Code, privateInstancePage.Body, privateInstance)
	}
	profileOwnerPrivateInstance := callCreatorProfileAPI(t, h, http.MethodGet, profilePath, "profile-owner", nil)
	var ownPrivate creatorProfileCourseList
	if profileOwnerPrivateInstance.Code != http.StatusOK || json.Unmarshal(profileOwnerPrivateInstance.Body.Bytes(), &ownPrivate) != nil || ownPrivate.Total != 1 || len(ownPrivate.Items) != 1 || ownPrivate.Items[0].Course.ID != "course-own-private" {
		t.Fatalf("private-instance owner ACL status=%d body=%s result=%+v", profileOwnerPrivateInstance.Code, profileOwnerPrivateInstance.Body, ownPrivate)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE courses SET visibility='private' WHERE id='course-public-a'`); err != nil {
		t.Fatal(err)
	}
	staleVisibility := callCreatorProfileAPI(t, h, http.MethodGet, profilePath, "reader", nil)
	var stale creatorProfileCourseList
	if staleVisibility.Code != http.StatusOK || json.Unmarshal(staleVisibility.Body.Bytes(), &stale) != nil || stale.Total != 1 || len(stale.Items) != 1 || stale.Items[0].Course.ID != "course-public-b" {
		t.Fatalf("stale/unreadable course relation remained visible: status=%d body=%s result=%+v", staleVisibility.Code, staleVisibility.Body, stale)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	disabled := callCreatorProfileAPI(t, h, http.MethodGet, profilePath, "profile-owner", nil)
	if disabled.Code != http.StatusNotFound || disabled.Body.String() != "{\"error\":\"not_found\"}\n" {
		t.Fatalf("disabled course profile route did not fail closed: status=%d body=%s", disabled.Code, disabled.Body)
	}
}

func TestCreatorProfileCoursesUseComposedMuxAuthenticationAndCanonicalPath(t *testing.T) {
	db := testDatabase(t)
	profileID := strings.Repeat("e", 32)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('creator','creator@example.test','Creator','now');
		UPDATE instance_settings SET courses_enabled=1 WHERE id=1;
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'creator','Creator','instance','now','now')`, profileID); err != nil {
		t.Fatal(err)
	}
	auth := testAuth(db)
	handler := handlerWithAuthConfig(db, auth, config{mediaDir: t.TempDir(), federationKeyDir: t.TempDir()})
	token, _, err := issueSession(db, "creator")
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, session string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if session != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	canonical := request("/api/creator-profiles/"+profileID+"/courses", token)
	if canonical.Code != http.StatusOK || canonical.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("canonical composed route status=%d cache=%q body=%s", canonical.Code, canonical.Header().Get("Cache-Control"), canonical.Body)
	}
	unauthenticated := request("/api/creator-profiles/"+profileID+"/courses", "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("composed route did not require session: status=%d body=%s", unauthenticated.Code, unauthenticated.Body)
	}
	for _, path := range []string{
		"/api/creator-profiles/" + profileID + "/courses/",
		"/api/creator-profiles/" + profileID + "%2fcourses",
		"/api//creator-profiles/" + profileID + "/courses",
	} {
		response := request(path, token)
		if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("noncanonical route %q reached profile handler: status=%d cache=%q body=%s", path, response.Code, response.Header().Get("Cache-Control"), response.Body)
		}
	}
}
