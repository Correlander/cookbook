package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestLibraryCoursesScopesAndRedactsSavedReferences(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=1,public_instance=1 WHERE id=1;
INSERT INTO users(id,email,display_name,created_at) VALUES('owner','o@example.test','Course Owner','now'),('viewer','v@example.test','Viewer','now'),('other','x@example.test','Other','now');
INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('owned','viewer','My course','','private','1','1'),('public','owner','Readable','','public','2','2'),('public-foreign','owner','Readable foreign collision','','public','2','2'),('private','owner','Hidden','','private','3','3');
INSERT INTO course_lessons(id,course_id,title,content,position,created_at,updated_at) VALUES('lesson','public','Lesson','Text',1,'now','now');`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	// Add a foreign reference and a deleted identity owned by the viewer.
	foreignID := strings.Repeat("a", 32)
	if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES('saved-public','viewer',?,'public','4'),('deleted','viewer',?,'gone','5'),('foreign','viewer',?,'secret','6'),('foreign-owned-collision','viewer',?,'owned','7'),('foreign-public-collision','viewer',?,'public-foreign','8')`, instanceID, instanceID, foreignID, foreignID, foreignID); err != nil {
		t.Fatal(err)
	}
	response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/courses", "viewer", nil)
	var page LibraryCoursePage
	if response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(response.Body.Bytes(), &page) != nil {
		t.Fatalf("library %d %s", response.Code, response.Body)
	}
	if page.Total != 6 || len(page.Items) != 6 {
		t.Fatalf("page=%+v", page)
	}
	byID := map[string]LibraryCourseItem{}
	unavailableIDs := map[string]bool{}
	for _, item := range page.Items {
		if item.Course != nil {
			byID[item.Course.ID] = item
		} else if item.Availability != "unavailable" || item.BookmarkID == "" || item.CreatedAt == "" || strings.Contains(string(mustJSON(t, item)), "course_id") || strings.Contains(string(mustJSON(t, item)), "secret") {
			t.Fatalf("unavailable projection leaks: %+v", item)
		} else {
			unavailableIDs[item.BookmarkID] = true
		}
	}
	if !unavailableIDs["foreign-owned-collision"] || !unavailableIDs["foreign-public-collision"] {
		t.Fatalf("foreign collision references were not kept unavailable: %v", unavailableIDs)
	}
	if got := byID["owned"]; got.Course == nil || len(got.Relationships) != 1 || got.Relationships[0] != "owned" || got.BookmarkID != "" || got.Course.OwnerID != "viewer" {
		t.Fatalf("owned entry=%+v", got)
	}
	if got := byID["public"]; got.Course == nil || got.BookmarkID != "saved-public" || got.Relationships[0] != "saved" || got.Course.OwnerID != "" || got.Course.OwnerName != "Course Owner" {
		t.Fatalf("saved readable entry=%+v", got)
	}
	if _, included := byID["public-foreign"]; included {
		t.Fatal("foreign reference caused an unsaved public course to enter the local Library")
	}
	for _, targetID := range []string{"owned", "public-foreign"} {
		status := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/course-bookmarks/status?local_course_id="+targetID, "viewer", nil)
		if status.Code != 200 || strings.TrimSpace(status.Body.String()) != `{"saved":false}` {
			t.Fatalf("foreign source identity changed %q save status: %d %s", targetID, status.Code, status.Body)
		}
	}
	status := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/course-bookmarks/status?local_course_id=private", "viewer", nil)
	if status.Code != 404 {
		t.Fatalf("private status leaked: %d %s", status.Code, status.Body)
	}
	if got := callCreatorProfileAPI(t, h, http.MethodGet, "/api/courses/public", "viewer", nil); got.Code != 200 {
		t.Fatalf("course ACL regression: %d %s", got.Code, got.Body)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	disabled := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/courses", "viewer", nil)
	var hidden LibraryCoursePage
	if disabled.Code != 200 || json.Unmarshal(disabled.Body.Bytes(), &hidden) != nil || hidden.Total != 5 {
		t.Fatalf("disabled references: %d %s", disabled.Code, disabled.Body)
	}
	for _, item := range hidden.Items {
		if item.Availability != "unavailable" || item.Course != nil {
			t.Fatalf("disabled course content promoted: %+v", item)
		}
	}
	if got := callCreatorProfileAPI(t, h, http.MethodDelete, "/api/library/course-bookmarks/deleted", "viewer", nil); got.Code != 204 {
		t.Fatalf("disabled delete: %d %s", got.Code, got.Body)
	}
}

func TestCourseBookmarkSaveAndStatusRequireReadableEnabledCourse(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=1,public_instance=1 WHERE id=1; INSERT INTO users(id,email,display_name,created_at) VALUES('owner','o@example.test','Owner','now'),('viewer','v@example.test','Viewer','now'); INSERT INTO courses(id,owner_id,title,visibility,created_at,updated_at) VALUES('public','owner','Public','public','now','now'),('private','owner','Private','private','now','now');`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	first := callCreatorProfileAPI(t, h, http.MethodPost, "/api/library/course-bookmarks", "viewer", CourseBookmarkInput{LocalCourseID: "public"})
	var saved CourseBookmarkView
	if first.Code != 201 || json.Unmarshal(first.Body.Bytes(), &saved) != nil || saved.CourseID != "public" || saved.BookmarkID == "" {
		t.Fatalf("save: %d %s", first.Code, first.Body)
	}
	second := callCreatorProfileAPI(t, h, http.MethodPost, "/api/library/course-bookmarks", "viewer", CourseBookmarkInput{LocalCourseID: "public"})
	var repeated CourseBookmarkView
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &repeated) != nil || repeated.BookmarkID != saved.BookmarkID || repeated.CreatedAt != saved.CreatedAt {
		t.Fatalf("idempotent save: %d %s", second.Code, second.Body)
	}
	status := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/course-bookmarks/status?local_course_id=public", "viewer", nil)
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"saved":true`) || !strings.Contains(status.Body.String(), saved.BookmarkID) {
		t.Fatalf("status: %d %s", status.Code, status.Body)
	}
	denied := callCreatorProfileAPI(t, h, http.MethodPost, "/api/library/course-bookmarks", "viewer", CourseBookmarkInput{LocalCourseID: "private"})
	if denied.Code != 404 {
		t.Fatalf("private save: %d %s", denied.Code, denied.Body)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	disabled := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/course-bookmarks/status?local_course_id=public", "viewer", nil)
	if disabled.Code != 404 {
		t.Fatalf("disabled status: %d", disabled.Code)
	}
}

func TestCourseBookmarkIdentityConstraintsAndAccountLimit(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','r@example.test','Reader','now')`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ id, user, instance, course string }{{"bad-user", "missing-user", instanceID, "course"}, {"bad-instance", "reader", "bad", "course"}, {"bad-course", "reader", instanceID, "  "}, {"bad-course-long", "reader", instanceID, strings.Repeat("c", 81)}} {
		if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES(?,?,?,?,?)`, bad.id, bad.user, bad.instance, bad.course, "now"); err == nil {
			t.Errorf("invalid bookmark accepted: %+v", bad)
		}
	}
	if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES('same','reader',?,'course','now')`, instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES('duplicate','reader',?,'course','later')`, instanceID); err == nil {
		t.Fatal("duplicate source identity accepted")
	}
	if _, err := db.Exec(`DROP TRIGGER course_bookmarks_account_limit;
WITH RECURSIVE seq(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM seq WHERE n<9998)
INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at)
SELECT printf('%032x',n+1),'reader','` + instanceID + `',printf('course-%d',n),'now' FROM seq;
CREATE TRIGGER course_bookmarks_account_limit BEFORE INSERT ON course_bookmarks
WHEN (SELECT COUNT(*) FROM course_bookmarks WHERE user_id=NEW.user_id)>=10000
BEGIN SELECT RAISE(ABORT,'course bookmark limit reached'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES('over-limit','reader',?,'over-limit','now')`, instanceID); err == nil || !strings.Contains(err.Error(), "course bookmark limit reached") {
		t.Fatalf("account bookmark limit error=%v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
