package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCoursesStayDisabledUntilAdminEnablesFeature(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	for _, route := range []struct {
		endpoint string
		cookie   *http.Cookie
	}{{"/api/courses", &http.Cookie{Name: sessionCookie, Value: token}}, {"/api/public/courses", nil}} {
		response := callAPI(handler, http.MethodGet, route.endpoint, nil, route.cookie)
		if response.Code != http.StatusNotFound {
			t.Fatalf("disabled endpoint %s status=%d, want 404", route.endpoint, response.Code)
		}
	}
	denied := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]bool{"courses_enabled": true}, nil)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous course toggle status=%d, want 401", denied.Code)
	}
	enabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]bool{"courses_enabled": true}, &http.Cookie{Name: sessionCookie, Value: token})
	var settings instanceSettings
	if enabled.Code != http.StatusOK || json.Unmarshal(enabled.Body.Bytes(), &settings) != nil || !settings.CoursesEnabled {
		t.Fatalf("admin course toggle status=%d body=%s", enabled.Code, enabled.Body)
	}
	disabled := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]bool{"courses_enabled": false}, &http.Cookie{Name: sessionCookie, Value: token})
	if disabled.Code != http.StatusOK {
		t.Fatalf("admin disable courses status=%d body=%s", disabled.Code, disabled.Body)
	}
}

func TestEnablingCoursesRequiresReviewOfExistingPublicCourses(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.test','Admin',1,'now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO courses(id,owner_id,title,visibility,created_at,updated_at) VALUES('public-course','admin','Public course','public','now','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	settingsResponse := callAPI(handler, http.MethodGet, "/api/admin/settings", nil, cookie)
	var settings instanceSettings
	if settingsResponse.Code != http.StatusOK || json.Unmarshal(settingsResponse.Body.Bytes(), &settings) != nil || settings.PublicCourses != 1 {
		t.Fatalf("admin settings did not report public courses: status=%d body=%s", settingsResponse.Code, settingsResponse.Body)
	}
	refused := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]bool{"courses_enabled": true}, cookie)
	if refused.Code != http.StatusConflict {
		t.Fatalf("enable courses without publication review status=%d, want 409: %s", refused.Code, refused.Body)
	}
	stillDisabled := callAPI(handler, http.MethodGet, "/api/public/courses", nil, nil)
	if stillDisabled.Code != http.StatusNotFound {
		t.Fatalf("refused update enabled public course route: status=%d", stillDisabled.Code)
	}
	confirmed := callAPI(handler, http.MethodPatch, "/api/admin/settings", map[string]any{"courses_enabled": true, "confirm_publication": true}, cookie)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmed enable courses status=%d body=%s", confirmed.Code, confirmed.Body)
	}
	publicList := callAPI(handler, http.MethodGet, "/api/public/courses", nil, nil)
	if publicList.Code != http.StatusOK || !strings.Contains(publicList.Body.String(), "Public course") {
		t.Fatalf("confirmed course publication status=%d body=%s", publicList.Code, publicList.Body)
	}
}

func TestEnabledCoursesEnforceVisibilityAndProtectLinkedRecipes(t *testing.T) {
	db := testDatabase(t)
	_, err := db.Exec(`UPDATE instance_settings SET public_instance=1,courses_enabled=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('other','other@example.test','Other','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES('private-recipe','owner','Owner','Private pie','[]','[]','[]','private','owner_only','now','now')`)
	if err != nil {
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
	ownerCookie, otherCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}, &http.Cookie{Name: sessionCookie, Value: otherToken}
	private := callAPI(handler, http.MethodPost, "/api/courses", map[string]string{"title": "Kitchen basics", "visibility": "private"}, ownerCookie)
	if private.Code != http.StatusCreated {
		t.Fatalf("create private course status=%d body=%s", private.Code, private.Body)
	}
	var privateCourse course
	if err := json.Unmarshal(private.Body.Bytes(), &privateCourse); err != nil {
		t.Fatal(err)
	}
	lesson := callAPI(handler, http.MethodPost, "/api/courses/"+privateCourse.ID+"/lessons", map[string]any{"title": "Knife skills", "content": "Keep the blade steady.", "position": 0}, ownerCookie)
	if lesson.Code != http.StatusCreated {
		t.Fatalf("create lesson status=%d body=%s", lesson.Code, lesson.Body)
	}
	var firstLesson courseLesson
	if err := json.Unmarshal(lesson.Body.Bytes(), &firstLesson); err != nil {
		t.Fatal(err)
	}
	secondLessonResponse := callAPI(handler, http.MethodPost, "/api/courses/"+privateCourse.ID+"/lessons", map[string]any{"title": "Seasoning", "content": "Taste as you go.", "position": 1}, ownerCookie)
	var secondLesson courseLesson
	if secondLessonResponse.Code != http.StatusCreated || json.Unmarshal(secondLessonResponse.Body.Bytes(), &secondLesson) != nil {
		t.Fatalf("create second lesson status=%d body=%s", secondLessonResponse.Code, secondLessonResponse.Body)
	}
	moveFirst := callAPI(handler, http.MethodPatch, "/api/courses/"+privateCourse.ID+"/lessons/"+firstLesson.ID, map[string]int{"position": 1}, ownerCookie)
	moveSecond := callAPI(handler, http.MethodPatch, "/api/courses/"+privateCourse.ID+"/lessons/"+secondLesson.ID, map[string]int{"position": 0}, ownerCookie)
	var updatedLesson courseLesson
	if moveFirst.Code != http.StatusOK || json.Unmarshal(moveFirst.Body.Bytes(), &updatedLesson) != nil || updatedLesson.Position != 1 {
		t.Fatalf("lesson update response status=%d body=%s", moveFirst.Code, moveFirst.Body)
	}
	orderedResponse := callAPI(handler, http.MethodGet, "/api/courses/"+privateCourse.ID+"/lessons", nil, ownerCookie)
	var ordered []courseLesson
	if moveFirst.Code != http.StatusOK || moveSecond.Code != http.StatusOK || orderedResponse.Code != http.StatusOK || json.Unmarshal(orderedResponse.Body.Bytes(), &ordered) != nil || len(ordered) != 2 || ordered[0].ID != secondLesson.ID {
		t.Fatalf("course lesson order after changes: first=%d second=%d list=%s", moveFirst.Code, moveSecond.Code, orderedResponse.Body)
	}
	privateRead := callAPI(handler, http.MethodGet, "/api/courses/"+privateCourse.ID, nil, otherCookie)
	if privateRead.Code != http.StatusNotFound {
		t.Fatalf("private course leaked to another user: status=%d", privateRead.Code)
	}
	public := callAPI(handler, http.MethodPost, "/api/courses", map[string]string{"title": "Home cooking", "visibility": "public"}, ownerCookie)
	if public.Code != http.StatusCreated {
		t.Fatalf("create public course status=%d body=%s", public.Code, public.Body)
	}
	var publicCourse course
	if err := json.Unmarshal(public.Body.Bytes(), &publicCourse); err != nil {
		t.Fatal(err)
	}
	linked := callAPI(handler, http.MethodPost, "/api/courses/"+publicCourse.ID+"/lessons", map[string]any{"title": "Family pie", "content": "A private recipe reference.", "recipe_id": "private-recipe", "position": 0}, ownerCookie)
	if linked.Code != http.StatusCreated {
		t.Fatalf("create linked lesson status=%d body=%s", linked.Code, linked.Body)
	}
	publicLessons := callAPI(handler, http.MethodGet, "/api/public/courses/"+publicCourse.ID+"/lessons", nil, nil)
	var lessons []courseLesson
	if publicLessons.Code != http.StatusOK || json.Unmarshal(publicLessons.Body.Bytes(), &lessons) != nil || len(lessons) != 1 || lessons[0].RecipeID != "" {
		t.Fatalf("public lessons exposed private recipe: status=%d body=%s", publicLessons.Code, publicLessons.Body)
	}
	publicCourseRead := callAPI(handler, http.MethodGet, "/api/public/courses/"+publicCourse.ID, nil, nil)
	if publicCourseRead.Code != http.StatusOK {
		t.Fatalf("public course read status=%d body=%s", publicCourseRead.Code, publicCourseRead.Body)
	}
	publicList := callAPI(handler, http.MethodGet, "/api/public/courses", nil, nil)
	if publicList.Code != http.StatusOK || !strings.Contains(publicList.Body.String(), "Home cooking") || strings.Contains(publicList.Body.String(), "Kitchen basics") {
		t.Fatalf("public course listing status=%d body=%s", publicList.Code, publicList.Body)
	}
}

func TestCourseReadDistinguishesMissingAndStorageFailure(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('reader','reader@example.test','Reader','now');
		INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('private-course','owner','Private course','','private','now','now')`); err != nil {
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
	handler := handlerWithAuth(db, testAuth(db))
	ownerCookie := &http.Cookie{Name: sessionCookie, Value: ownerToken}
	readerCookie := &http.Cookie{Name: sessionCookie, Value: readerToken}

	missing := callAPI(handler, http.MethodGet, "/api/courses/missing-course", nil, ownerCookie)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"error":"not_found"`) {
		t.Fatalf("missing course status=%d body=%s, want 404 not_found", missing.Code, missing.Body)
	}
	inaccessible := callAPI(handler, http.MethodGet, "/api/courses/private-course", nil, readerCookie)
	if inaccessible.Code != http.StatusNotFound || !strings.Contains(inaccessible.Body.String(), `"error":"not_found"`) {
		t.Fatalf("inaccessible course status=%d body=%s, want 404 not_found", inaccessible.Code, inaccessible.Body)
	}

	// The owner projection failure occurs after the course insert begins. Course
	// creation must roll back instead of committing data without its response.
	if _, err := db.Exec(`ALTER TABLE users DROP COLUMN display_name`); err != nil {
		t.Fatal(err)
	}

	created := callAPI(handler, http.MethodPost, "/api/courses", map[string]string{"title": "Committed course", "visibility": "private"}, ownerCookie)
	if created.Code != http.StatusInternalServerError || !strings.Contains(created.Body.String(), `"error":"storage_error"`) {
		t.Fatalf("course create post-write read status=%d body=%s, want 500 storage_error", created.Code, created.Body)
	}
	var createdCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM courses WHERE title='Committed course' AND owner_id='owner'`).Scan(&createdCount); err != nil || createdCount != 0 {
		t.Fatalf("course create did not roll back after read failure: count=%d err=%v", createdCount, err)
	}

	updated := callAPI(handler, http.MethodPatch, "/api/courses/private-course", map[string]string{"title": "Committed update"}, ownerCookie)
	if updated.Code != http.StatusInternalServerError || !strings.Contains(updated.Body.String(), `"error":"storage_error"`) {
		t.Fatalf("course update post-write read status=%d body=%s, want 500 storage_error", updated.Code, updated.Body)
	}
	var storedTitle string
	if err := db.QueryRow(`SELECT title FROM courses WHERE id='private-course'`).Scan(&storedTitle); err != nil || storedTitle != "Committed update" {
		t.Fatalf("course update did not commit before read failure: title=%q err=%v", storedTitle, err)
	}
}

func TestCourseCreateAndUpdateReturnCurrentCourse(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=1 WHERE id=1;
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	cookie := &http.Cookie{Name: sessionCookie, Value: token}

	created := callAPI(handler, http.MethodPost, "/api/courses", map[string]string{"title": "  Kitchen basics  ", "description": "  First draft  ", "visibility": "private"}, cookie)
	var createdCourse course
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &createdCourse) != nil || createdCourse.ID == "" || createdCourse.Title != "Kitchen basics" || createdCourse.Description != "First draft" {
		t.Fatalf("course create status=%d body=%s", created.Code, created.Body)
	}

	updated := callAPI(handler, http.MethodPatch, "/api/courses/"+createdCourse.ID, map[string]string{"title": "Updated basics", "description": "Updated description"}, cookie)
	var updatedCourse course
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &updatedCourse) != nil || updatedCourse.ID != createdCourse.ID || updatedCourse.Title != "Updated basics" || updatedCourse.Description != "Updated description" {
		t.Fatalf("course update status=%d body=%s", updated.Code, updated.Body)
	}
	var storedTitle, storedDescription string
	if err := db.QueryRow(`SELECT title,description FROM courses WHERE id=?`, createdCourse.ID).Scan(&storedTitle, &storedDescription); err != nil || storedTitle != updatedCourse.Title || storedDescription != updatedCourse.Description {
		t.Fatalf("updated course not persisted: title=%q description=%q err=%v", storedTitle, storedDescription, err)
	}
}
