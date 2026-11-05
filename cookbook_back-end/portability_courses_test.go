package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPortableV7ExportsOwnedCoursesAndRemapsOnlyIncludedRecipes(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`UPDATE instance_settings SET courses_enabled=0,public_instance=1 WHERE id=1;
INSERT INTO users(id,email,display_name,created_at) VALUES
 ('owner','owner@example.test','Course Owner','now'),('other','other@example.test','Other Owner','now'),('importer','importer@example.test','Importer','now');
INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at,deleted_at) VALUES
 ('included-recipe','owner','Recipe Claim','Included recipe','','[]','[]',1,0,0,'[]','private','owner_only',1,'created','updated',NULL),
 ('other-private-recipe','other','Other','Private recipe','','[]','[]',1,0,0,'[]','private','owner_only',1,'created','updated',NULL),
 ('other-public-recipe','other','Other','Readable public recipe','','[]','[]',1,0,0,'[]','public','owner_only',1,'created','updated',NULL),
 ('deleted-recipe','owner','Owner','Deleted recipe','','[]','[]',1,0,0,'[]','private','owner_only',1,'created','updated','deleted');
INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) VALUES
 ('source-private','owner','Private course','UTF-8 detail: crème brûlée','private','created','updated'),
 ('source-public','owner','Public course','Public source input','public','created','updated'),
 ('other-course','other','Do not export this','Foreign metadata','private','created','updated');
INSERT INTO course_lessons(id,course_id,title,content,recipe_id,position,created_at,updated_at) VALUES
 ('z-first','source-private','First','Soup – crème 🥣','included-recipe',3,'created','updated'),
 ('b-second','source-private','Second','Private link omitted','other-private-recipe',3,'created','updated'),
 ('c-third','source-private','Third','Readable but not owned link omitted','other-public-recipe',4,'created','updated'),
 ('d-fourth','source-private','Fourth','Deleted target omitted','deleted-recipe',5,'created','updated'),
('e-fifth','source-private','Fifth','Already unlinked',NULL,6,'created','updated'),
('foreign-lesson','other-course','Foreign lesson','Do not export',NULL,0,'created','updated');`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	manifest, bundle := exportPortableBookmarkManifest(t, h, "owner")
	if bundle.Format != portableFormatV10 || bundle.Courses == nil || len(*bundle.Courses) != 2 || bundle.OmittedCourseRecipeLinks == nil || *bundle.OmittedCourseRecipeLinks != 3 {
		t.Fatalf("v9 export format/courses/omissions=%q/%d/%v", bundle.Format, len(*bundle.Courses), bundle.OmittedCourseRecipeLinks)
	}
	if (*bundle.Courses)[0].ID != "source-private" || (*bundle.Courses)[0].Visibility != "private" || (*bundle.Courses)[1].ID != "source-public" || (*bundle.Courses)[1].Visibility != "public" {
		t.Fatalf("owned course set/order/visibility=%+v", *bundle.Courses)
	}
	lessons := (*bundle.Courses)[0].Lessons
	if len(lessons) != 5 || lessons[0].ID != "b-second" || lessons[1].ID != "z-first" || lessons[1].Content != "Soup – crème 🥣" || lessons[1].RecipeID != "included-recipe" {
		t.Fatalf("ordered course lessons changed: %+v", lessons)
	}
	for _, lesson := range []portableCourseLesson{lessons[0], lessons[2], lessons[3]} {
		if lesson.RecipeID != "" {
			t.Fatalf("omitted lesson retained recipe identity: %+v", lesson)
		}
	}
	encoded := string(manifest)
	for _, secret := range []string{"other-private-recipe", "other-public-recipe", "deleted-recipe", "other-course", "Foreign metadata", "owner_id", "owner_name"} {
		if strings.Contains(encoded, secret) {
			t.Errorf("course export leaked excluded identity or authorship field %q", secret)
		}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &raw); err != nil {
		t.Fatal(err)
	}
	var rawCourses []map[string]json.RawMessage
	if err := json.Unmarshal(raw["courses"], &rawCourses); err != nil || len(rawCourses) != 2 {
		t.Fatalf("v7 courses field=%s err=%v", raw["courses"], err)
	}
	var rawLessons []map[string]json.RawMessage
	if err := json.Unmarshal(rawCourses[0]["lessons"], &rawLessons); err != nil || len(rawLessons) != 5 {
		t.Fatalf("course lessons field=%s err=%v", rawCourses[0]["lessons"], err)
	}

	// Reuse an archived recipe ID on the destination to prove links resolve only
	// through this import's generated recipe ID map, never by matching IDs.
	if _, err := db.Exec(`DELETE FROM recipes WHERE id='included-recipe';
INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES('included-recipe','importer','Destination','Unrelated same ID','','[]','[]',1,0,0,'[]','private','owner_only',1,'created','updated');`); err != nil {
		t.Fatal(err)
	}
	archive := portableBookmarkArchive(t, bundle, nil)
	response := importPortableBookmarkArchive(t, h, "importer", archive)
	var result map[string]int
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &result) != nil || result["courses"] != 2 || result["course_lessons"] != 5 || result["omitted_course_recipe_links"] != 3 {
		t.Fatalf("v7 import result=%d %s", response.Code, response.Body)
	}
	assertImportedPortableCourses(t, db, "importer", lessons, "included-recipe")
	second := importPortableBookmarkArchive(t, h, "importer", archive)
	if second.Code != http.StatusCreated {
		t.Fatalf("repeat v7 import: %d %s", second.Code, second.Body)
	}
	var copies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM courses WHERE owner_id='importer' AND title IN ('Private course','Public course')`).Scan(&copies); err != nil || copies != 4 {
		t.Fatalf("repeat import course count=%d err=%v", copies, err)
	}
}

func assertImportedPortableCourses(t *testing.T, db *sql.DB, userID string, expected []portableCourseLesson, sourceRecipeID string) {
	t.Helper()
	rows, err := db.Query(`SELECT c.id,c.owner_id,c.visibility,l.title,l.content,l.position,COALESCE(l.recipe_id,'')
		FROM courses c JOIN course_lessons l ON l.course_id=c.id WHERE c.owner_id=? AND c.title='Private course' ORDER BY c.created_at,c.id,l.position`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	index := 0
	var importedRecipeID string
	for rows.Next() {
		var courseID, ownerID, visibility, title, content, recipeID string
		var position int
		if err := rows.Scan(&courseID, &ownerID, &visibility, &title, &content, &position, &recipeID); err != nil {
			t.Fatal(err)
		}
		if ownerID != userID || visibility != "private" || courseID == "source-private" || index >= len(expected) {
			t.Fatalf("imported course custody/visibility/order invalid: %q %q %q", courseID, ownerID, visibility)
		}
		lessonIndex := index % len(expected)
		if title != expected[lessonIndex].Title || content != expected[lessonIndex].Content || position != lessonIndex {
			t.Fatalf("imported lesson order/text/normalized position index=%d: %q %q %d", lessonIndex, title, content, position)
		}
		if expected[lessonIndex].RecipeID != "" {
			if recipeID == "" || recipeID == sourceRecipeID {
				t.Fatalf("included recipe link was not remapped: %q", recipeID)
			}
			importedRecipeID = recipeID
		} else if recipeID != "" {
			t.Fatalf("omitted link rebound to target %q for lesson %q", recipeID, title)
		}
		index++
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if index != len(expected) {
		t.Fatalf("imported lessons=%d want %d", index, len(expected))
	}
	var owner, title string
	if err := db.QueryRow(`SELECT owner_id,title FROM recipes WHERE id=?`, importedRecipeID).Scan(&owner, &title); err != nil || owner != userID || title != "Included recipe" {
		t.Fatalf("course link did not point to imported archive recipe: owner=%q title=%q err=%v", owner, title, err)
	}
}

func TestPortableV7StrictValidationAndLegacyBoundaries(t *testing.T) {
	zero := 0
	baseCourses := []portableCourse{}
	base := portableBundle{Format: portableFormatV7, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: json.RawMessage("[]"), Courses: &baseCourses, OmittedCourseRecipeLinks: &zero}
	if err := validatePortableBundle(base); err != nil {
		t.Fatalf("valid empty v7 rejected: %v", err)
	}
	serialized, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var rawEmpty map[string]json.RawMessage
	if err := json.Unmarshal(serialized, &rawEmpty); err != nil || string(rawEmpty["courses"]) != "[]" || string(rawEmpty["omitted_course_recipe_links"]) != "0" {
		t.Fatalf("empty v7 fields missing: %s err=%v", serialized, err)
	}
	baseCourses = []portableCourse{{ID: "course", Title: "Course", Description: "Description", Visibility: "public", Lessons: []portableCourseLesson{{ID: "lesson", Title: "Lesson", Content: "UTF-8: jalapeño", Position: 2}}}}
	base.Courses = &baseCourses
	if err := validatePortableBundle(base); err != nil {
		t.Fatalf("valid v7 course rejected: %v", err)
	}
	for _, mutate := range []func(*portableBundle){
		func(candidate *portableBundle) { candidate.Courses = nil },
		func(candidate *portableBundle) { candidate.OmittedCourseRecipeLinks = nil },
		func(candidate *portableBundle) { value := -1; candidate.OmittedCourseRecipeLinks = &value },
		func(candidate *portableBundle) { value := 2; candidate.OmittedCourseRecipeLinks = &value },
		func(candidate *portableBundle) { (*candidate.Courses)[0].Lessons = nil },
		func(candidate *portableBundle) { (*candidate.Courses)[0].Lessons[0].ID = "" },
		func(candidate *portableBundle) { (*candidate.Courses)[0].Lessons[0].Position = 10001 },
		func(candidate *portableBundle) { (*candidate.Courses)[0].Visibility = "shared" },
		func(candidate *portableBundle) {
			*candidate.Courses = append(*candidate.Courses, (*candidate.Courses)[0])
		},
		func(candidate *portableBundle) {
			(*candidate.Courses)[0].Lessons = append((*candidate.Courses)[0].Lessons, (*candidate.Courses)[0].Lessons[0])
		},
		func(candidate *portableBundle) { (*candidate.Courses)[0].Lessons[0].RecipeID = "unknown" },
	} {
		candidate := base
		courses := append([]portableCourse(nil), (*base.Courses)...)
		courses[0].Lessons = append([]portableCourseLesson(nil), courses[0].Lessons...)
		candidate.Courses = &courses
		mutate(&candidate)
		if err := validatePortableBundle(candidate); err == nil {
			t.Errorf("invalid v7 archive accepted: %+v", candidate)
		}
	}
	v6 := portableBundle{Format: portableFormatV6, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: json.RawMessage("[]")}
	if err := validatePortableBundle(v6); err != nil {
		t.Fatalf("legacy v6 rejected: %v", err)
	}
	v6Courses := []portableCourse{}
	v6.Courses = &v6Courses
	if err := validatePortableBundle(v6); err == nil {
		t.Fatal("legacy v6 accepted a v7-only courses field")
	}
	unknown := []byte(`{"format":"cookbook-export-v7","cookbooks":[],"recipes":[],"bookmarks":[],"attributions":[],"cookbook_bookmarks":[],"course_bookmarks":[],"courses":[{"id":"course","title":"Course","description":"","visibility":"private","lessons":[],"owner_name":"Claim"}],"omitted_course_recipe_links":0}`)
	decoder := json.NewDecoder(strings.NewReader(string(unknown)))
	decoder.DisallowUnknownFields()
	var unknownBundle portableBundle
	if err := decoder.Decode(&unknownBundle); err == nil {
		t.Fatal("v7 archive accepted unknown course metadata")
	}
	missingCourses := base
	missingCourses.Courses = nil
	if err := validatePortableBundle(missingCourses); err == nil {
		t.Fatal("v7 archive without courses was accepted")
	}
}

func TestPortableV7CourseAndLessonCapsApplyToExportAndImport(t *testing.T) {
	for _, test := range []struct {
		name string
		seed string
	}{
		{name: "course cap", seed: `WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<1001) INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) SELECT printf('course-%04d',n),'owner','Course','','private','now','now' FROM seq;`},
		{name: "lesson cap", seed: `INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('course','owner','Course','','private','now','now'); WITH RECURSIVE seq(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM seq WHERE n<10000) INSERT INTO course_lessons(id,course_id,title,content,position,created_at,updated_at) SELECT printf('lesson-%05d',n),'course','Lesson','',n,'now','now' FROM seq;`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := testDatabase(t)
			if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.seed); err != nil {
				t.Fatal(err)
			}
			response := callCreatorProfileAPI(t, apiHandler{db: db}, http.MethodGet, "/api/export", "owner", nil)
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversized export status=%d body=%s", response.Code, response.Body)
			}
		})
	}
	base := portableBundle{Format: portableFormatV7, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: json.RawMessage("[]"), OmittedCourseRecipeLinks: new(int)}
	tooManyCourses := base
	tooManyCourseItems := make([]portableCourse, maxPortableCourses+1)
	tooManyCourses.Courses = &tooManyCourseItems
	if err := validatePortableBundle(tooManyCourses); err == nil {
		t.Fatal("import accepted more than 1,000 courses")
	}
	tooManyLessons := base
	tooManyLessonItems := []portableCourse{{ID: "course", Title: "Course", Visibility: "private", Lessons: make([]portableCourseLesson, maxPortableCourseLessons+1)}}
	tooManyLessons.Courses = &tooManyLessonItems
	for index := range (*tooManyLessons.Courses)[0].Lessons {
		(*tooManyLessons.Courses)[0].Lessons[index] = portableCourseLesson{ID: fmt.Sprintf("lesson-%05d", index), Title: "Lesson"}
	}
	if err := validatePortableBundle(tooManyLessons); err == nil {
		t.Fatal("import accepted more than 10,000 lessons")
	}
}

func TestPortableV7LateCourseFailureRollsBackAllCopiedContent(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('importer','i@example.test','Importer','now');
CREATE TRIGGER reject_course_lesson_import BEFORE INSERT ON course_lessons BEGIN SELECT RAISE(ABORT,'forced course lesson failure'); END`); err != nil {
		t.Fatal(err)
	}
	zero := 0
	courseItems := []portableCourse{{ID: "course", Title: "Course", Visibility: "private", Lessons: []portableCourseLesson{{ID: "lesson", Title: "Lesson", Content: "Body", Position: 0, RecipeID: "recipe"}}}}
	bundle := portableBundle{Format: portableFormatV7, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{{ID: "recipe", CreatorName: "Recipe creator", Title: "Recipe", Ingredients: []ingredient{}, Steps: []recipeStep{}, Servings: 1, Tags: []string{}, Visibility: "private", EditPolicy: "owner_only", CookbookIDs: []string{}}}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: json.RawMessage("[]"), Courses: &courseItems, OmittedCourseRecipeLinks: &zero}
	response := importPortableBookmarkArchive(t, apiHandler{db: db}, "importer", portableBookmarkArchive(t, bundle, nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("forced lesson failure status=%d body=%s", response.Code, response.Body)
	}
	for _, table := range []string{"recipes", "courses", "course_lessons"} {
		var count int
		if table == "course_lessons" {
			if err := db.QueryRow(`SELECT COUNT(*) FROM course_lessons l JOIN courses c ON c.id=l.course_id WHERE c.owner_id='importer'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed import retained lessons=%d err=%v", count, err)
			}
		} else {
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE owner_id='importer'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed import retained %s=%d err=%v", table, count, err)
			}
		}
	}
}
