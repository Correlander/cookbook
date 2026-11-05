package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCourseCreatorAttributionCreateReadPrivacyAndPatchImmutability(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=1,public_instance=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Custodian','now'),('reader','reader@example.test','Reader','now'),('admin','admin@example.test','Admin','now');
		UPDATE users SET is_admin=1 WHERE id='admin';
		INSERT INTO creator_profiles(creator_id,user_id,public_name,bio,visibility,created_at,updated_at) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner','Private identity','','private','now','now');`); err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	readerToken, _, err := issueSession(db, "reader")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	owner := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	reader := &http.Cookie{Name: sessionCookie, Value: readerToken}
	admin := &http.Cookie{Name: sessionCookie, Value: adminToken}

	local := callAPI(handler, http.MethodPost, "/api/courses", map[string]any{"title": "Private course", "visibility": "private", "creator_attribution": map[string]string{"kind": "local_profile"}}, owner)
	var privateCourse course
	if local.Code != http.StatusCreated || json.Unmarshal(local.Body.Bytes(), &privateCourse) != nil || len(privateCourse.CreatorAttributions) != 1 || privateCourse.CreatorAttributions[0].Profile == nil || privateCourse.OwnerName != "Custodian" {
		t.Fatalf("local credit create status=%d body=%s", local.Code, local.Body)
	}
	if privateCourse.CreatorAttributions[0].Profile.ID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || privateCourse.CreatorAttributions[0].Profile.Visibility != "private" {
		t.Fatalf("owner profile projection=%+v", privateCourse.CreatorAttributions[0])
	}
	patch := callAPI(handler, http.MethodPatch, "/api/courses/"+privateCourse.ID, map[string]any{"title": "Retitled", "creator_attribution": map[string]string{"kind": "unknown"}}, owner)
	if patch.Code != http.StatusBadRequest {
		t.Fatalf("course PATCH unexpectedly accepted creator attribution: status=%d body=%s", patch.Code, patch.Body)
	}
	patch = callAPI(handler, http.MethodGet, "/api/courses/"+privateCourse.ID, nil, owner)
	var patched course
	if patch.Code != http.StatusOK || json.Unmarshal(patch.Body.Bytes(), &patched) != nil || len(patched.CreatorAttributions) != 1 || patched.CreatorAttributions[0].Profile == nil || patched.Title != "Private course" {
		t.Fatalf("course PATCH changed or dropped attribution status=%d body=%s", patch.Code, patch.Body)
	}
	denied := callAPI(handler, http.MethodGet, "/api/courses/"+privateCourse.ID, nil, reader)
	if denied.Code != http.StatusNotFound || strings.Contains(denied.Body.String(), "Private identity") {
		t.Fatalf("foreign private course disclosure status=%d body=%s", denied.Code, denied.Body)
	}
	adminDenied := callAPI(handler, http.MethodGet, "/api/courses/"+privateCourse.ID, nil, admin)
	if adminDenied.Code != http.StatusNotFound {
		t.Fatalf("administrator bypassed private course access policy: status=%d body=%s", adminDenied.Code, adminDenied.Body)
	}

	claimed := callAPI(handler, http.MethodPost, "/api/courses", map[string]any{"title": "Public course", "visibility": "public", "creator_attribution": map[string]any{"kind": "name_claim", "name": "  Claimed chef  "}}, owner)
	var publicCourse course
	if claimed.Code != http.StatusCreated || json.Unmarshal(claimed.Body.Bytes(), &publicCourse) != nil || publicCourse.CreatorName != "Claimed chef" || len(publicCourse.CreatorAttributions) != 0 {
		t.Fatalf("name claim create status=%d body=%s", claimed.Code, claimed.Body)
	}
	externalClaim := portableAttribution{SourceInstanceID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CreatorID: "cccccccccccccccccccccccccccccccc", NameClaim: "Remote creator", Role: "contributor", Provenance: "original_provenance"}
	claimTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertExternalAttribution(context.Background(), claimTx, "course", publicCourse.ID, externalClaim); err != nil {
		claimTx.Rollback()
		t.Fatal(err)
	}
	if err := claimTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('22222222222222222222222222222222',?,'local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','now')`, publicCourse.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE creator_profiles SET visibility='instance' WHERE creator_id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`); err != nil {
		t.Fatal(err)
	}
	publicRead := callAPI(handler, http.MethodGet, "/api/public/courses/"+publicCourse.ID, nil, nil)
	var anonymous course
	if publicRead.Code != http.StatusOK || json.Unmarshal(publicRead.Body.Bytes(), &anonymous) != nil || anonymous.CreatorName != "Claimed chef" || len(anonymous.CreatorAttributions) != 1 || anonymous.CreatorAttributions[0].SubjectKind != "external_claim" || anonymous.CreatorAttributions[0].NameClaim != "Remote creator" || anonymous.CreatorAttributions[0].Role != "contributor" || anonymous.CreatorAttributions[0].Provenance != "original_provenance" || anonymous.CreatorAttributions[0].Profile != nil {
		t.Fatalf("anonymous public projection status=%d body=%s", publicRead.Code, publicRead.Body)
	}
	readerRead := callAPI(handler, http.MethodGet, "/api/courses/"+publicCourse.ID, nil, reader)
	var readerProjection course
	if readerRead.Code != http.StatusOK || json.Unmarshal(readerRead.Body.Bytes(), &readerProjection) != nil || len(readerProjection.CreatorAttributions) != 2 || readerProjection.CreatorAttributions[0].SubjectKind != "local" || readerProjection.CreatorAttributions[0].Profile == nil || readerProjection.CreatorAttributions[0].Profile.ID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || readerProjection.CreatorAttributions[0].Profile.PublicName != "Private identity" || readerProjection.CreatorAttributions[1].SubjectKind != "external_claim" {
		t.Fatalf("authenticated instance reader did not see instance profile credit: status=%d body=%s", readerRead.Code, readerRead.Body)
	}
	publicList := callAPI(handler, http.MethodGet, "/api/public/courses", nil, nil)
	var listed []course
	if publicList.Code != http.StatusOK || json.Unmarshal(publicList.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].CreatorName != "Claimed chef" || len(listed[0].CreatorAttributions) != 1 {
		t.Fatalf("public list projection status=%d body=%s", publicList.Code, publicList.Body)
	}
	ownerRead := callAPI(handler, http.MethodGet, "/api/courses/"+publicCourse.ID, nil, owner)
	var ownerProjection course
	if ownerRead.Code != http.StatusOK || json.Unmarshal(ownerRead.Body.Bytes(), &ownerProjection) != nil || ownerProjection.OwnerID != "owner" || ownerProjection.OwnerName != "Custodian" || len(ownerProjection.CreatorAttributions) != 2 {
		t.Fatalf("owner and creator distinction status=%d body=%s", ownerRead.Code, ownerRead.Body)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES('bookmark','reader',?,?,'now'),('gone','reader',?,'missing','now')`, instanceID, publicCourse.ID, instanceID); err != nil {
		t.Fatal(err)
	}
	library := callAPI(handler, http.MethodGet, "/api/library/courses", nil, reader)
	var page LibraryCoursePage
	if library.Code != http.StatusOK || json.Unmarshal(library.Body.Bytes(), &page) != nil {
		t.Fatalf("library projection status=%d body=%s", library.Code, library.Body)
	}
	foundAvailable, foundUnavailable := false, false
	for _, item := range page.Items {
		if item.Availability == "available" {
			foundAvailable = item.Course != nil && item.Course.ID == publicCourse.ID && len(item.Course.CreatorAttributions) == 2 && item.Course.CreatorAttributions[0].Profile != nil
		} else if item.Availability == "unavailable" {
			foundUnavailable = item.Course == nil
		}
	}
	if !foundAvailable || !foundUnavailable {
		t.Fatalf("library course attribution or placeholder projection missing: %+v", page)
	}
}

func TestCourseCreatorAttributionValidationRollbackAndUnknownChoice(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		CREATE TRIGGER fail_course_credit BEFORE INSERT ON content_attributions WHEN NEW.course_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'fixture'); END;`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	handler := handlerWithAuth(db, testAuth(db))
	for _, attribution := range []any{map[string]any{"kind": "bogus"}, map[string]any{"kind": "name_claim", "name": "   "}, map[string]any{"kind": "unknown", "name": "not allowed"}} {
		response := callAPI(handler, http.MethodPost, "/api/courses", map[string]any{"title": "Invalid", "visibility": "private", "creator_attribution": attribution}, cookie)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid attribution %v status=%d body=%s", attribution, response.Code, response.Body)
		}
	}
	failure := callAPI(handler, http.MethodPost, "/api/courses", map[string]any{"title": "Rollback", "visibility": "private", "creator_attribution": map[string]string{"kind": "local_profile"}}, cookie)
	var courses, profiles int
	if failure.Code != http.StatusInternalServerError || db.QueryRow(`SELECT COUNT(*) FROM courses`).Scan(&courses) != nil || db.QueryRow(`SELECT COUNT(*) FROM creator_profiles`).Scan(&profiles) != nil || courses != 0 || profiles != 0 {
		t.Fatalf("failed credit transaction was not atomic: status=%d courses=%d profiles=%d body=%s", failure.Code, courses, profiles, failure.Body)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_course_credit`); err != nil {
		t.Fatal(err)
	}
	unknown := callAPI(handler, http.MethodPost, "/api/courses", map[string]any{"title": "No credit", "visibility": "private", "creator_attribution": map[string]string{"kind": "unknown"}}, cookie)
	var created course
	if unknown.Code != http.StatusCreated || json.Unmarshal(unknown.Body.Bytes(), &created) != nil || created.CreatorName != "" || len(created.CreatorAttributions) != 0 {
		t.Fatalf("unknown choice should create no credit: status=%d body=%s", unknown.Code, unknown.Body)
	}
	omitted := callAPI(handler, http.MethodPost, "/api/courses", map[string]any{"title": "Legacy request", "visibility": "private"}, cookie)
	if omitted.Code != http.StatusCreated || json.Unmarshal(omitted.Body.Bytes(), &created) != nil || created.CreatorName != "" || len(created.CreatorAttributions) != 0 {
		t.Fatalf("omitted legacy choice should create no credit: status=%d body=%s", omitted.Code, omitted.Body)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	config := publicConfigFor(db, testAuth(db))
	response := callAPI(config, http.MethodGet, "/api/public/config", nil, nil)
	var configBody map[string]any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &configBody) != nil || configBody["courses_enabled"] != false || configBody["course_creator_attribution"] != true {
		t.Fatalf("API capability must remain independent of courses_enabled: status=%d body=%s", response.Code, response.Body)
	}
}
