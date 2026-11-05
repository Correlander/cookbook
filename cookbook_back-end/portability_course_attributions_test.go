package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPortableV9CourseClaimsExportAndImportAsExternalClaims(t *testing.T) {
	db := testDatabase(t)
	ownerProfile, foreignProfile, instanceProfile, importerProfile := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	foreignInstance, externalCreator := strings.Repeat("e", 32), strings.Repeat("f", 32)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('owner','owner@example.test','Owner','now'),('foreign','foreign@example.test','Foreign','now'),
		('instance','instance@example.test','Instance','now'),('importer','importer@example.test','Importer','now')`)
	seed(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'owner','Owner private claim','private','now','now')`, ownerProfile)
	seed(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'foreign','Foreign private secret','private','now','now')`, foreignProfile)
	seed(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'instance','Instance-visible claim','instance','now','now')`, instanceProfile)
	seed(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?, 'importer','Importer same-ID profile','private','now','now')`, importerProfile)
	seed(`INSERT INTO courses(id,owner_id,title,description,creator_name,visibility,created_at,updated_at) VALUES
		('source-self','owner','Owner claim course','Description','Explicit unverified name','private','now','now'),
		('source-hidden','owner','Hidden foreign claim course','Description','','private','now','now'),
		('source-instance','owner','Instance claim course','Description','','private','now','now')`)
	seed(`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at) VALUES('11111111111111111111111111111111','source-self','local',?,'creator','self_asserted_at_creation','now')`, ownerProfile)
	seed(`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at) VALUES('22222222222222222222222222222222','source-hidden','local',?,'creator','self_asserted_at_creation','now')`, foreignProfile)
	seed(`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at) VALUES('33333333333333333333333333333333','source-instance','local',?,'creator','self_asserted_at_creation','now')`, instanceProfile)
	seed(`INSERT INTO content_attributions(id,course_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		VALUES('44444444444444444444444444444444','source-self','external_claim',?,?,'External claim','contributor','imported_source','now')`, foreignInstance, externalCreator)
	h := apiHandler{db: db}
	manifest, bundle := exportPortableBookmarkManifest(t, h, "owner")
	if bundle.Format != portableFormatV10 || bundle.Courses == nil || len(*bundle.Courses) != 3 {
		t.Fatalf("v9 course export=%+v format=%q", bundle.Courses, bundle.Format)
	}
	courseNames := map[string]*string{}
	for _, course := range *bundle.Courses {
		courseNames[course.ID] = course.CreatorName
	}
	if courseNames["source-self"] == nil || *courseNames["source-self"] != "Explicit unverified name" {
		t.Fatalf("v9 explicit course name=%v", courseNames["source-self"])
	}
	if len(bundle.Attributions) != 3 {
		t.Fatalf("exported course attributions=%+v", bundle.Attributions)
	}
	for _, item := range bundle.Attributions {
		if item.TargetKind != "course" || item.TargetID == "source-hidden" || item.TargetID != "source-self" && item.TargetID != "source-instance" {
			t.Fatalf("course attribution target=%+v", item)
		}
		if item.CreatorID == foreignProfile || item.NameClaim == "Foreign private secret" {
			t.Fatalf("foreign private profile was serialized: %+v", item)
		}
	}
	foundInstanceClaim, foundExternalClaim := false, false
	foundOwnerPrivateClaim := false
	for _, item := range bundle.Attributions {
		if item.TargetID == "source-self" && item.NameClaim == "Owner private claim" {
			foundOwnerPrivateClaim = true
		}
		if item.TargetID == "source-instance" && item.NameClaim == "Instance-visible claim" {
			foundInstanceClaim = true
		}
		if item.Role == "contributor" && item.SourceInstanceID == foreignInstance && item.CreatorID == externalCreator && item.NameClaim == "External claim" {
			foundExternalClaim = true
		}
	}
	if !foundOwnerPrivateClaim || !foundInstanceClaim || !foundExternalClaim {
		t.Fatalf("eligible owner/private, instance, or external claims missing: %+v", bundle.Attributions)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &raw); err != nil {
		t.Fatal(err)
	}
	var rawCourses []map[string]json.RawMessage
	if err := json.Unmarshal(raw["courses"], &rawCourses); err != nil || len(rawCourses) != 3 {
		t.Fatalf("creator_name wire field=%v err=%v", rawCourses, err)
	}
	foundEncodedName := false
	for _, course := range rawCourses {
		if string(course["id"]) == `"source-self"` && string(course["creator_name"]) == `"Explicit unverified name"` {
			foundEncodedName = true
		}
	}
	if !foundEncodedName {
		t.Fatalf("creator_name field missing from v9 course: %v", rawCourses)
	}

	response := importPortableBookmarkArchive(t, h, "importer", portableBookmarkArchive(t, bundle, nil))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"courses":3`) {
		t.Fatalf("v9 import status=%d body=%s", response.Code, response.Body)
	}
	var importedCourseID, creatorName, visibility string
	if err := db.QueryRow(`SELECT id,creator_name,visibility FROM courses WHERE owner_id='importer' AND title='Owner claim course'`).Scan(&importedCourseID, &creatorName, &visibility); err != nil || importedCourseID == "source-self" || creatorName != "Explicit unverified name" || visibility != "private" {
		t.Fatalf("imported private course id/name/visibility=%q/%q/%q err=%v", importedCourseID, creatorName, visibility, err)
	}
	var importedCount, localBindings, leakedForeign int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE course_id=? AND subject_kind='external_claim'`, importedCourseID).Scan(&importedCount); err != nil || importedCount != 2 {
		t.Fatalf("imported external claims=%d err=%v", importedCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE course_id=? AND creator_id IN (?,?,?)`, importedCourseID, ownerProfile, instanceProfile, importerProfile).Scan(&localBindings); err != nil || localBindings != 0 {
		t.Fatalf("archive claims bound to local profile: count=%d err=%v", localBindings, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE course_id=? AND claim_creator_id=?`, importedCourseID, foreignProfile).Scan(&leakedForeign); err != nil || leakedForeign != 0 {
		t.Fatalf("foreign private profile claim imported: count=%d err=%v", leakedForeign, err)
	}
}

func TestPortableV9CourseCreatorNameVersionPresenceAndBounds(t *testing.T) {
	valid := `{"format":"cookbook-export-v9","cookbooks":[],"recipes":[],"bookmarks":[],"attributions":[],"cookbook_bookmarks":[],"course_bookmarks":[],"courses":[{"id":"course","title":"Course","description":"","creator_name":"","visibility":"private","lessons":[]}],"omitted_course_recipe_links":0,"snapshot_entries":[]}`
	archive := func(manifest string) []byte {
		t.Helper()
		return portableV4ArchiveManifest(t, []byte(manifest))
	}
	if _, _, err := decodePortableArchive(archive(valid)); err != nil {
		t.Fatalf("v9 explicit empty creator_name rejected: %v", err)
	}
	for _, manifest := range []string{
		strings.Replace(valid, `,"creator_name":""`, "", 1),
		strings.Replace(valid, `"creator_name":""`, `"creator_name":null`, 1),
	} {
		if _, _, err := decodePortableArchive(archive(manifest)); err == nil {
			t.Errorf("v9 missing or null creator_name accepted: %s", manifest)
		}
	}
	v8WithName := strings.Replace(valid, "cookbook-export-v9", "cookbook-export-v8", 1)
	if _, _, err := decodePortableArchive(archive(v8WithName)); err == nil {
		t.Fatal("v8 course accepted v9 creator_name, including its empty value")
	}
	v8WithoutName := strings.Replace(v8WithName, `,"creator_name":""`, "", 1)
	v8AliasRoot := strings.Replace(v8WithName, `"courses"`, `"Courses"`, 1)
	v8AliasNested := strings.Replace(v8WithoutName, `"visibility"`, `"Creator_Name":"","visibility"`, 1)
	v8AliasNestedNull := strings.Replace(v8WithoutName, `"visibility"`, `"Creator_Name":null,"visibility"`, 1)
	v8DuplicateAlias := strings.Replace(v8WithName, `"creator_name":""`, `"creator_name":"","Creator_Name":null`, 1)
	for _, manifest := range []string{v8AliasRoot, v8AliasNested, v8AliasNestedNull, v8DuplicateAlias} {
		if _, _, err := decodePortableArchive(archive(manifest)); err == nil {
			t.Errorf("v8 accepted case-variant course name field: %s", manifest)
		}
	}
	v6NullCourses := strings.Replace(valid, "cookbook-export-v9", "cookbook-export-v6", 1)
	v6NullCourses = strings.Replace(v6NullCourses, `"courses":[{"id":"course","title":"Course","description":"","creator_name":"","visibility":"private","lessons":[]}],"omitted_course_recipe_links":0`, `"Courses":null`, 1)
	v6NullCourses = strings.Replace(v6NullCourses, `,"snapshot_entries":[]`, "", 1)
	if _, _, err := decodePortableArchive(archive(v6NullCourses)); err == nil {
		t.Fatal("v6 accepted a null v7+ courses container through a case-insensitive root alias")
	}
	v9CaseFolded := strings.Replace(strings.Replace(valid, `"courses"`, `"Courses"`, 1), `"creator_name"`, `"Creator_Name"`, 1)
	if bundle, _, err := decodePortableArchive(archive(v9CaseFolded)); err != nil || bundle.Courses == nil || len(*bundle.Courses) != 1 || (*bundle.Courses)[0].CreatorName == nil || *(*bundle.Courses)[0].CreatorName != "" {
		t.Fatalf("v9 accepted decoder aliases inconsistently: bundle=%+v err=%v", bundle.Courses, err)
	}
	v9DuplicateAliasLastNull := strings.Replace(valid, `"creator_name":""`, `"creator_name":"first","Creator_Name":null`, 1)
	if _, _, err := decodePortableArchive(archive(v9DuplicateAliasLastNull)); err == nil {
		t.Fatal("v9 accepted null in the last case-insensitive creator_name alias")
	}
	v9DuplicateAliasLastString := strings.Replace(valid, `"creator_name":""`, `"creator_name":null,"Creator_Name":"last"`, 1)
	if bundle, _, err := decodePortableArchive(archive(v9DuplicateAliasLastString)); err != nil || bundle.Courses == nil || (*bundle.Courses)[0].CreatorName == nil || *(*bundle.Courses)[0].CreatorName != "last" {
		t.Fatalf("v9 duplicate alias did not follow decoder last-value behavior: bundle=%+v err=%v", bundle.Courses, err)
	}
	rootDuplicate := strings.TrimSuffix(valid, "}") + `,"Courses":[{"id":"last-course","title":"Last","description":"","creator_name":"last","visibility":"private","lessons":[]}]} `
	if bundle, _, err := decodePortableArchive(archive(rootDuplicate)); err != nil || bundle.Courses == nil || len(*bundle.Courses) != 1 || (*bundle.Courses)[0].ID != "last-course" {
		t.Fatalf("v9 root alias duplicate did not follow decoder last-value behavior: bundle=%+v err=%v", bundle.Courses, err)
	}
	tooLong := strings.Replace(valid, `"creator_name":""`, `"creator_name":"`+strings.Repeat("x", 321)+`"`, 1)
	bundle, _, err := decodePortableArchive(archive(tooLong))
	if err == nil && validatePortableBundle(bundle) == nil {
		t.Fatal("v9 creator_name above byte limit accepted")
	}
	tooManyRunes := strings.Replace(valid, `"creator_name":""`, `"creator_name":"`+strings.Repeat("é", 81)+`"`, 1)
	bundle, _, err = decodePortableArchive(archive(tooManyRunes))
	if err == nil && validatePortableBundle(bundle) == nil {
		t.Fatal("v9 creator_name above Unicode scalar limit accepted")
	}
}

func TestPortableV9CourseAttributionValidationAndAtomicImport(t *testing.T) {
	name := ""
	courses := []portableCourse{{ID: "source-course", Title: "Course", Visibility: "private", CreatorName: &name, Lessons: []portableCourseLesson{}}}
	snapshots := []portableSnapshotReference{}
	base := portableBundle{
		Format: portableFormatV9, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{},
		Attributions:      []portableAttribution{{TargetKind: "course", TargetID: "source-course", SourceInstanceID: strings.Repeat("a", 32), CreatorID: strings.Repeat("b", 32), Role: "creator", Provenance: "claim"}},
		CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: json.RawMessage("[]"), Courses: &courses,
		OmittedCourseRecipeLinks: new(int), SnapshotEntries: &snapshots,
	}
	if err := validatePortableBundle(base); err != nil {
		t.Fatalf("valid v9 course attribution rejected: %v", err)
	}
	unknownCourse := base
	unknownCourse.Attributions = append([]portableAttribution(nil), base.Attributions...)
	unknownCourse.Attributions[0].TargetID = "not-exported"
	if err := validatePortableBundle(unknownCourse); err == nil {
		t.Fatal("course attribution target outside the exported course set accepted")
	}
	duplicate := base
	duplicate.Attributions = append(append([]portableAttribution(nil), base.Attributions...), base.Attributions[0])
	if err := validatePortableBundle(duplicate); err == nil {
		t.Fatal("duplicate course attribution accepted")
	}
	legacy := base
	legacy.Format = portableFormatV8
	legacyCourses := append([]portableCourse(nil), courses...)
	legacyCourses[0].CreatorName = nil
	legacy.Courses = &legacyCourses
	legacy.Attributions = append([]portableAttribution(nil), base.Attributions...)
	legacy.SnapshotEntries = &snapshots
	if err := validatePortableBundle(legacy); err == nil {
		t.Fatal("v8 archive accepted a v9 course attribution target")
	}

	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('importer','importer@example.test','Importer','now');
		CREATE TRIGGER reject_portable_course_claim BEFORE INSERT ON content_attributions WHEN NEW.course_id IS NOT NULL AND NEW.subject_kind='external_claim' BEGIN SELECT RAISE(ABORT,'injected course claim failure'); END`); err != nil {
		t.Fatal(err)
	}
	response := importPortableBookmarkArchive(t, apiHandler{db: db}, "importer", portableBookmarkArchive(t, base, nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("injected late course attribution failure status=%d body=%s", response.Code, response.Body)
	}
	var coursesAfterFailure, claimsAfterFailure int
	if err := db.QueryRow(`SELECT COUNT(*) FROM courses WHERE owner_id='importer'`).Scan(&coursesAfterFailure); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE course_id IN (SELECT id FROM courses WHERE owner_id='importer')`).Scan(&claimsAfterFailure); err != nil {
		t.Fatal(err)
	}
	if coursesAfterFailure != 0 || claimsAfterFailure != 0 {
		t.Fatalf("failed v9 course attribution left partial import: courses=%d claims=%d", coursesAfterFailure, claimsAfterFailure)
	}
}
