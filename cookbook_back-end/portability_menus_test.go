package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"Cookbook_Database_Backend/internal/menus"
)

func TestPortableV10OwnedMenusPreserveSlotsAndImportAsPrivateCopies(t *testing.T) {
	db := testDatabase(t)
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	foreignInstance, foreignRecipeInstance, ownerProfile, foreignProfile, externalCreator := strings.Repeat("e", 32), strings.Repeat("9", 32), strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("f", 32)
	includedRecipeID, deletedRecipeID, foreignRecipeID := strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('owner','owner@example.test','Owner','now'),('other','other@example.test','Other','now'),('importer','importer@example.test','Importer','now'),('invalid','invalid@example.test','Invalid','now'),('rollback','rollback@example.test','Rollback','now'),('quota','quota@example.test','Quota','now')`)
	seed(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at,deleted_at) VALUES
		(?,'owner','Recipe Creator','Included recipe','','[]','[]',1,0,0,'[]','private','owner_only',1,'now','now',NULL),
		(?,'owner','Recipe Creator','Deleted recipe','','[]','[]',1,0,0,'[]','private','owner_only',1,'now','now','deleted'),
		(?,'other','Other','Foreign private recipe','','[]','[]',1,0,0,'[]','private','owner_only',1,'now','now',NULL)`, includedRecipeID, deletedRecipeID, foreignRecipeID)
	seed(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES
		(?,'owner','Owner private credit','private','now','now'),(?,'other','Hidden foreign credit','private','now','now')`, ownerProfile, foreignProfile)
	menuID := strings.Repeat("1", 32)
	seed(`INSERT INTO meal_menus(id,owner_id,title,note,creator_name,version,created_at,updated_at) VALUES(?, 'owner','Weekend','Keep these notes','Explicit menu claim',1,'now','now')`, menuID)
	hiddenMenuID := strings.Repeat("0", 32)
	seed(`INSERT INTO meal_menus(id,owner_id,title,note,creator_name,version,created_at,updated_at) VALUES(?, 'owner','Credit privacy','','',1,'now','now')`, hiddenMenuID)
	entries := []struct {
		id, instance, recipe, label, note string
	}{
		{strings.Repeat("2", 32), instanceID, includedRecipeID, "First", "first note"},
		{strings.Repeat("3", 32), instanceID, includedRecipeID, "Repeat", "repeat note"},
		{strings.Repeat("4", 32), foreignRecipeInstance, foreignRecipeID, "Foreign", "keep foreign slot"},
		{strings.Repeat("5", 32), instanceID, deletedRecipeID, "Deleted", "keep deleted slot"},
		{strings.Repeat("6", 32), "", "", "Unresolved", "already unresolved"},
	}
	for position, entry := range entries {
		var instance, recipe any
		if entry.instance != "" {
			instance, recipe = entry.instance, entry.recipe
		}
		seed(`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id,label,note,serving_note) VALUES(?,?,?,?,?,?,?,?)`, entry.id, menuID, position, instance, recipe, entry.label, entry.note, "serve plainly")
	}
	seed(`INSERT INTO content_attributions(id,menu_id,subject_kind,creator_id,role,provenance,created_at) VALUES(?,?,'local',?,'creator','self_asserted_at_creation','now')`, strings.Repeat("7", 32), menuID, ownerProfile)
	seed(`INSERT INTO content_attributions(id,menu_id,subject_kind,creator_id,role,provenance,created_at) VALUES(?,?,'local',?,'creator','self_asserted_at_creation','now')`, strings.Repeat("8", 32), hiddenMenuID, foreignProfile)
	seed(`INSERT INTO content_attributions(id,menu_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at) VALUES(?,?,'external_claim',?,?,'Imported source','adapter','imported_source','now')`, strings.Repeat("9", 32), menuID, foreignInstance, externalCreator)
	h := apiHandler{db: db}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, requestForUser(http.MethodGet, "/api/export", "owner"))
	if response.Code != http.StatusOK {
		t.Fatalf("v10 export status=%d body=%s", response.Code, response.Body)
	}
	bundle := portableManifestFromResponse(t, response.Body.Bytes())
	if bundle.Format != portableFormatV10 || bundle.Menus == nil || len(*bundle.Menus) != 2 || bundle.OmittedMenuRecipeLinks == nil || *bundle.OmittedMenuRecipeLinks != 2 {
		t.Fatalf("v10 menu export format/data=%q menus=%+v omitted=%v", bundle.Format, bundle.Menus, bundle.OmittedMenuRecipeLinks)
	}
	menu := (*bundle.Menus)[1]
	if menu.ID != menuID || menu.Title != "Weekend" || menu.Note != "Keep these notes" || menu.CreatorName != "Explicit menu claim" || len(menu.Entries) != len(entries) {
		t.Fatalf("menu fields/order lost: %+v", menu)
	}
	if menu.Entries[0].RecipeID != includedRecipeID || menu.Entries[1].RecipeID != includedRecipeID || menu.Entries[2].RecipeID != "" || !menu.Entries[2].RecipeLinkOmitted || menu.Entries[3].RecipeID != "" || !menu.Entries[3].RecipeLinkOmitted || menu.Entries[4].RecipeLinkOmitted {
		t.Fatalf("menu slot links were not safely represented: %+v", menu.Entries)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Foreign private recipe", "Deleted recipe", "Hidden foreign credit"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Errorf("menu export exposed excluded target or private claim %q", forbidden)
		}
	}
	ownerClaim, externalClaim, hiddenClaim := false, false, false
	for _, claim := range bundle.Attributions {
		if claim.TargetKind != "menu" || claim.TargetID != menuID {
			continue
		}
		switch claim.NameClaim {
		case "Owner private credit":
			ownerClaim = claim.SourceInstanceID == instanceID && claim.CreatorID == ownerProfile
		case "Imported source":
			externalClaim = claim.SourceInstanceID == foreignInstance && claim.CreatorID == externalCreator
		case "Hidden foreign credit":
			hiddenClaim = true
		}
	}
	if !ownerClaim || !externalClaim || hiddenClaim {
		t.Fatalf("menu creator claim privacy/remapping source failed: owner=%v external=%v hidden=%v attributions=%+v", ownerClaim, externalClaim, hiddenClaim, bundle.Attributions)
	}
	archive := append([]byte(nil), response.Body.Bytes()...)
	invalidBundle, _, err := decodePortableArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	(*invalidBundle.Menus)[1].Entries[4].Note = strings.Repeat("x", 8001)
	invalidManifest, err := json.Marshal(invalidBundle)
	if err != nil {
		t.Fatal(err)
	}
	invalidArchive, err := buildPortableExportArchive(invalidManifest, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	invalidResponse := importPortableBookmarkArchive(t, h, "invalid", invalidArchive)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid late menu slot unexpectedly imported: %d %s", invalidResponse.Code, invalidResponse.Body)
	}
	var invalidMenus int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menus WHERE owner_id='invalid'`).Scan(&invalidMenus); err != nil || invalidMenus != 0 {
		t.Fatalf("invalid late slot left imported menu state: count=%d err=%v", invalidMenus, err)
	}
	imported := importPortableBookmarkArchive(t, h, "importer", archive)
	if imported.Code != http.StatusCreated || !strings.Contains(imported.Body.String(), `"menus":2`) || !strings.Contains(imported.Body.String(), `"omitted_menu_recipe_links":2`) {
		t.Fatalf("v10 import status/counts=%d %s", imported.Code, imported.Body)
	}
	var importedMenuID, creatorName string
	if err := db.QueryRow(`SELECT id,creator_name FROM meal_menus WHERE owner_id='importer' AND title='Weekend'`).Scan(&importedMenuID, &creatorName); err != nil || importedMenuID == menuID || creatorName != "Explicit menu claim" {
		t.Fatalf("menu was not privately recreated with a fresh identity: id=%q name=%q err=%v", importedMenuID, creatorName, err)
	}
	var importedInstance string
	if err := db.QueryRow(`SELECT local_instance_id FROM meal_menu_entries WHERE menu_id=? AND position=0`, importedMenuID).Scan(&importedInstance); err != nil {
		t.Fatal(err)
	}
	if importedInstance != instanceID {
		t.Fatalf("imported included recipe did not bind to destination instance %q", instanceID)
	}
	var importedFirstRecipe, importedRepeatRecipe string
	if err := db.QueryRow(`SELECT local_recipe_id FROM meal_menu_entries WHERE menu_id=? AND position=0`, importedMenuID).Scan(&importedFirstRecipe); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT local_recipe_id FROM meal_menu_entries WHERE menu_id=? AND position=1`, importedMenuID).Scan(&importedRepeatRecipe); err != nil || importedFirstRecipe == "included-recipe" || importedFirstRecipe != importedRepeatRecipe {
		t.Fatalf("duplicate recipe order/reference was not remapped: %q %q err=%v", importedFirstRecipe, importedRepeatRecipe, err)
	}
	var unresolvedCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menu_entries WHERE menu_id=? AND local_recipe_id IS NULL`, importedMenuID).Scan(&unresolvedCount); err != nil || unresolvedCount != 3 {
		t.Fatalf("omitted and unresolved slots were not retained: count=%d err=%v", unresolvedCount, err)
	}
	var localClaims, externalClaims int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE menu_id=? AND subject_kind='local'`, importedMenuID).Scan(&localClaims); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE menu_id=? AND subject_kind='external_claim'`, importedMenuID).Scan(&externalClaims); err != nil || localClaims != 0 || externalClaims != 2 {
		t.Fatalf("importer received trusted/local authorship: local=%d external=%d err=%v", localClaims, externalClaims, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_late_menu_entry BEFORE INSERT ON meal_menu_entries WHEN NEW.position=1 BEGIN SELECT RAISE(ABORT,'test late entry failure'); END`); err != nil {
		t.Fatal(err)
	}
	rollback := importPortableBookmarkArchive(t, h, "rollback", archive)
	if rollback.Code != http.StatusInternalServerError {
		t.Fatalf("late menu failure unexpectedly succeeded: %d %s", rollback.Code, rollback.Body)
	}
	var rolledBackMenus int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menus WHERE owner_id='rollback'`).Scan(&rolledBackMenus); err != nil || rolledBackMenus != 0 {
		t.Fatalf("late entry failure left a partial menu: count=%d err=%v", rolledBackMenus, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_late_menu_entry`); err != nil {
		t.Fatal(err)
	}
	quotaTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	store := menus.Store{}
	for i := 0; i < menus.MaxMenusPerOwner; i++ {
		id := newID()
		if _, err := store.Create(context.Background(), quotaTx, menus.CreateInput{ID: id, OwnerID: "quota", Title: "Existing", Now: "now"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := quotaTx.Commit(); err != nil {
		t.Fatal(err)
	}
	quotaResponse := importPortableBookmarkArchive(t, h, "quota", archive)
	if quotaResponse.Code != http.StatusConflict || !strings.Contains(quotaResponse.Body.String(), "menu_limit_reached") {
		t.Fatalf("menu quota was not reported: %d %s", quotaResponse.Code, quotaResponse.Body)
	}
	var quotaMenus int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menus WHERE owner_id='quota'`).Scan(&quotaMenus); err != nil || quotaMenus != menus.MaxMenusPerOwner {
		t.Fatalf("quota failure partially changed account menus: count=%d err=%v", quotaMenus, err)
	}
	if _, _, _, err := exportPortableMenusWithLimit(context.Background(), db, "owner", map[string]bool{includedRecipeID: true}, new(int), 1); !errors.Is(err, errPortableExportTooLarge) {
		t.Fatalf("small injected menu manifest budget was not enforced: %v", err)
	}
}

func TestPortableMenuFieldPresenceAndAliasesFollowJSONDecoder(t *testing.T) {
	for _, raw := range []string{
		`{"menus":[]}`,
		`{"Menus":[]}`,
		`{"menus":null}`,
		`{"menus":[],"Menus":[]}`,
	} {
		if err := validatePortableMenuFieldVersion([]byte(raw), portableFormatV9); err == nil {
			t.Errorf("legacy archive accepted v10 menu field %s", raw)
		}
	}
	for _, raw := range []string{
		`{"menus":[{"creator_name":"first","CREATOR_NAME":"last"}]}`,
	} {
		if err := validatePortableMenuFieldVersion([]byte(raw), portableFormatV10); err != nil {
			t.Errorf("v10 creator alias/duplicate did not use decoder's final string: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"menus":[{}]}`,
		`{"menus":[{"creator_name":"ok","Creator_Name":null}]}`,
	} {
		if err := validatePortableMenuFieldVersion([]byte(raw), portableFormatV10); err == nil {
			t.Errorf("v10 menu accepted missing or final-null creator_name: %s", raw)
		}
	}
}

func TestPortableV9ArchiveWithoutMenusRemainsImportable(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('source','source@example.test','Source','now'),('destination','destination@example.test','Destination','now')`); err != nil {
		t.Fatal(err)
	}
	creatorName := "v9 unverified course claim"
	courses := []portableCourse{{ID: "course-source", Title: "Source course", Visibility: "private", CreatorName: &creatorName, Lessons: []portableCourseLesson{}}}
	omittedCourses := 0
	emptySnapshots := []portableSnapshotReference{}
	bundle := portableBundle{Format: portableFormatV9, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]"), CourseBookmarks: json.RawMessage("[]"), Courses: &courses, OmittedCourseRecipeLinks: &omittedCourses, SnapshotEntries: &emptySnapshots}
	manifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := buildPortableExportArchive(manifest, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := decodePortableArchive(archive)
	if err != nil || validatePortableBundle(decoded) != nil {
		t.Fatalf("genuine v9 archive failed decoding: %v", err)
	}
	response := importPortableBookmarkArchive(t, apiHandler{db: db}, "destination", archive)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"courses":1`) || strings.Contains(response.Body.String(), `"menus"`) {
		t.Fatalf("v9 course meanings were not preserved by importer: %d %s", response.Code, response.Body)
	}
	var importedName string
	if err := db.QueryRow(`SELECT creator_name FROM courses WHERE owner_id='destination'`).Scan(&importedName); err != nil || importedName != creatorName {
		t.Fatalf("v9 course claim changed: %q err=%v", importedName, err)
	}
}
