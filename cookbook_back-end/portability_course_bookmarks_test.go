package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPortableV6CourseBookmarksPreserveOnlyReferenceIdentity(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('source','s@example.test','Source','now'),('destination','d@example.test','Destination','now'); INSERT INTO courses(id,owner_id,title,visibility,created_at,updated_at) VALUES('course-copy','source','Private course','private','now','now');`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	foreignID := strings.Repeat("f", 32)
	createdAt := "2026-11-04T01:00:00.123456789Z"
	if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES('local','source',?,'deleted-course',?),('foreign','source',?,'remote-course',?),('other','destination',?,'must-not-export','now')`, instanceID, createdAt, foreignID, createdAt, instanceID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	manifest, bundle := exportPortableBookmarkManifest(t, h, "source")
	if bundle.Format != portableFormatV9 {
		t.Fatalf("format=%q", bundle.Format)
	}
	items, err := portableCourseBookmarksFromBundle(bundle)
	if err != nil || len(items) != 2 {
		t.Fatalf("course refs=%+v err=%v", items, err)
	}
	byID := map[string]portableCourseBookmark{}
	for _, item := range items {
		byID[item.LocalCourseID] = item
	}
	if byID["deleted-course"].LocalInstanceID != instanceID || byID["deleted-course"].CreatedAt != createdAt || byID["remote-course"].LocalInstanceID != foreignID {
		t.Fatalf("reference identity changed: %+v", items)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &raw); err != nil {
		t.Fatal(err)
	}
	var encoded []map[string]json.RawMessage
	if err := json.Unmarshal(raw["course_bookmarks"], &encoded); err != nil || len(encoded) != 2 {
		t.Fatalf("manifest refs=%s err=%v", raw["course_bookmarks"], err)
	}
	for _, item := range encoded {
		if len(item) != 3 || item["local_instance_id"] == nil || item["local_course_id"] == nil || item["created_at"] == nil {
			t.Fatalf("course bookmark carries extra fields: %v", item)
		}
	}
	if !strings.Contains(string(manifest), "Private course") || strings.Contains(string(manifest), "must-not-export") {
		t.Fatal("export omitted owned course data or included another account reference")
	}
	response := importPortableBookmarkArchive(t, h, "destination", portableBookmarkArchive(t, bundle, nil))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"course_bookmarks":2`) || !strings.Contains(response.Body.String(), `"courses":1`) {
		t.Fatalf("import response=%d %s", response.Code, response.Body)
	}
	var id, namespace, created string
	if err := db.QueryRow(`SELECT local_course_id,local_instance_id,created_at FROM course_bookmarks WHERE user_id='destination' AND local_course_id='deleted-course'`).Scan(&id, &namespace, &created); err != nil || id != "deleted-course" || namespace != instanceID || created != createdAt {
		t.Fatalf("imported identity=%q/%q/%q err=%v", id, namespace, created, err)
	}
	reimport := importPortableBookmarkArchive(t, h, "destination", portableBookmarkArchive(t, bundle, nil))
	if reimport.Code != http.StatusCreated || !strings.Contains(reimport.Body.String(), `"course_bookmarks":0`) || !strings.Contains(reimport.Body.String(), `"courses":1`) {
		t.Fatalf("reimport did not dedupe saved identities: %d %s", reimport.Code, reimport.Body)
	}
	var importedRefs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM course_bookmarks WHERE user_id='destination'`).Scan(&importedRefs); err != nil || importedRefs != 3 {
		t.Fatalf("reimport bookmark count=%d err=%v", importedRefs, err)
	}
	var copiedCourses int
	if err := db.QueryRow(`SELECT COUNT(*) FROM courses WHERE owner_id='destination' AND title='Private course'`).Scan(&copiedCourses); err != nil || copiedCourses != 2 {
		t.Fatalf("repeat content import did not create independent copies: count=%d err=%v", copiedCourses, err)
	}
	pageResponse := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/courses", "destination", nil)
	var page LibraryCoursePage
	if pageResponse.Code != 200 || json.Unmarshal(pageResponse.Body.Bytes(), &page) != nil {
		t.Fatalf("library=%d %s", pageResponse.Code, pageResponse.Body)
	}
	for _, item := range page.Items {
		if item.Course != nil && item.Course.ID == "course-copy" {
			t.Fatal("import rebound saved identity to copied course")
		}
	}
}

func TestPortableCourseBookmarkFormatRulesAndLegacyCompatibility(t *testing.T) {
	valid := portableCourseBookmark{LocalInstanceID: strings.Repeat("a", 32), LocalCourseID: "course", CreatedAt: "2026-11-04T00:00:00Z"}
	encoded, _ := json.Marshal([]portableCourseBookmark{valid})
	base := portableBundle{Format: portableFormatV6, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: encoded}
	if err := validatePortableBundle(base); err != nil {
		t.Fatalf("valid v6 rejected: %v", err)
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("{}"), json.RawMessage(`[{"local_instance_id":"bad","local_course_id":"course","created_at":"2026-11-04T00:00:00Z"}]`), json.RawMessage(`[{"local_instance_id":"` + strings.Repeat("a", 32) + `","local_course_id":"course","created_at":"bad"}]`), json.RawMessage(`[{"local_instance_id":"` + strings.Repeat("a", 32) + `","local_course_id":"course","created_at":"2026-11-04T00:00:00Z","title":"stale"}]`)} {
		candidate := base
		candidate.CourseBookmarks = raw
		if err := validatePortableBundle(candidate); err == nil {
			t.Errorf("invalid v6 course_bookmarks accepted: %s", raw)
		}
	}
	for _, format := range []string{portableFormatV5, portableFormatV4, portableFormatV3, portableFormat, legacyPortableFormat} {
		candidate := base
		candidate.Format = format
		candidate.CourseBookmarks = nil
		if format != portableFormatV5 {
			candidate.CookbookBookmarks = nil
		}
		if format == portableFormatV3 || format == portableFormatV4 {
			candidate.Bookmarks = []portableBookmark{}
		}
		if format == portableFormatV4 {
			candidate.Attributions = []portableAttribution{}
		}
		if err := validatePortableBundle(candidate); err != nil {
			t.Errorf("legacy format %s rejected: %v", format, err)
		}
	}
	unknown := []byte(`{"format":"cookbook-export-v6","cookbooks":[],"recipes":[],"bookmarks":[],"attributions":[],"cookbook_bookmarks":[],"course_bookmarks":[{"local_instance_id":"` + strings.Repeat("a", 32) + `","local_course_id":"course","created_at":"2026-11-04T00:00:00Z","visibility":"public"}]}`)
	if _, _, err := decodePortableArchive(unknown); err == nil {
		t.Fatal("v6 archive accepted unsupported course metadata")
	}
}
