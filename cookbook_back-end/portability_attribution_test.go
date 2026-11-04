package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func portableV4Archive(t *testing.T, bundle portableBundle) []byte {
	t.Helper()
	manifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return portableV4ArchiveManifest(t, manifest)
}

func portableV4ArchiveManifest(t *testing.T, manifest []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	entry, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func portableManifestFromResponse(t *testing.T, data []byte) portableBundle {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		if entry.Name != "manifest.json" {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		manifest, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read portable manifest: read=%v close=%v", readErr, closeErr)
		}
		var bundle portableBundle
		if err := json.Unmarshal(manifest, &bundle); err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	t.Fatal("portable archive has no manifest")
	return portableBundle{}
}

func importPortableV4(t *testing.T, h http.Handler, archive []byte, userID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(archive))
	request = request.WithContext(context.WithValue(request.Context(), userContextKey{}, userID))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestPortableV4RedactsUnreadablePrivateRelationsAndRoundTripsClaims(t *testing.T) {
	db := testDatabase(t)
	ownerProfile := strings.Repeat("a", 32)
	hiddenProfile := strings.Repeat("b", 32)
	instanceProfile := strings.Repeat("c", 32)
	importerProfileID := strings.Repeat("d", 32)
	foreignSourceID := strings.Repeat("e", 32)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('owner','owner@example.test','Owner','now'),('hidden','hidden@example.test','Hidden','now'),
		('instance','instance@example.test','Instance','now'),('importer','importer@example.test','Importer','now')`)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'owner','Owner private name','private','now','now')`, ownerProfile)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'hidden','Hidden profile secret','private','now','now')`, hiddenProfile)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'instance','Household name','instance','now','now')`, instanceProfile)
	exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'importer','Importer identity','private','now','now')`, importerProfileID)
	exec(`INSERT INTO recipes(id,owner_id,creator_name,title,visibility,created_at,updated_at) VALUES
		('hidden-target','owner','Legacy hidden text','Hidden target','private','now','now'),
		('self-target','owner','','Self target','private','now','now'),
		('instance-target','owner','','Instance target','private','now','now'),
		('foreign-target','owner','Legacy foreign text','Foreign target','private','now','now')`)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('11111111111111111111111111111111','hidden-target','local',?,'creator','self_asserted_at_creation','now')`, hiddenProfile)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('22222222222222222222222222222222','self-target','local',?,'creator','self_asserted_at_creation','now')`, ownerProfile)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('33333333333333333333333333333333','instance-target','local',?,'adapter','adapter_source','now')`, instanceProfile)
	exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		VALUES('44444444444444444444444444444444','foreign-target','external_claim',?,?,'Imported claim','contributor','original_provenance','now')`, foreignSourceID, importerProfileID)
	h := apiHandler{db: db}
	export := httptest.NewRecorder()
	h.ServeHTTP(export, requestForUser(http.MethodGet, "/api/export", "owner"))
	if export.Code != http.StatusOK {
		t.Fatalf("v4 export status=%d body=%s", export.Code, export.Body)
	}
	bundle := portableManifestFromResponse(t, export.Body.Bytes())
	if bundle.Format != portableFormatV8 || len(bundle.Attributions) != 3 {
		t.Fatalf("v5 export format/attributions=%q/%+v", bundle.Format, bundle.Attributions)
	}
	claims := make(map[string]portableAttribution, len(bundle.Attributions))
	for _, item := range bundle.Attributions {
		claims[item.TargetID] = item
	}
	if _, found := claims["hidden-target"]; found {
		t.Fatal("export included typed attribution for another user's private profile")
	}
	manifestBytes, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifestBytes), hiddenProfile) || strings.Contains(string(manifestBytes), "Hidden profile secret") {
		t.Fatal("export leaked a private creator ID or name")
	}
	if got := claims["self-target"]; got.CreatorID != ownerProfile || got.NameClaim != "Owner private name" {
		t.Fatalf("self export lost owner claim: %+v", got)
	}
	if got := claims["instance-target"]; got.CreatorID != instanceProfile || got.NameClaim != "Household name" || got.Role != "adapter" {
		t.Fatalf("instance-visible claim not exported: %+v", got)
	}
	if got := claims["foreign-target"]; got.CreatorID != importerProfileID || got.SourceInstanceID != foreignSourceID || got.NameClaim != "Imported claim" || got.Role != "contributor" || got.Provenance != "original_provenance" {
		t.Fatalf("external claim changed on export: %+v", got)
	}
	var hiddenLegacy string
	if err := db.QueryRow(`SELECT creator_name FROM recipes WHERE id='hidden-target'`).Scan(&hiddenLegacy); err != nil || hiddenLegacy != "Legacy hidden text" {
		t.Fatalf("private relation omission changed legacy text: byline=%q err=%v", hiddenLegacy, err)
	}

	archive := portableV4Archive(t, bundle)
	imported := importPortableV4(t, h, archive, "importer")
	if imported.Code != http.StatusCreated {
		t.Fatalf("v4 import status=%d body=%s", imported.Code, imported.Body)
	}
	var importedClaims int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions a JOIN recipes r ON r.id=a.recipe_id WHERE r.owner_id='importer' AND a.subject_kind='external_claim'`).Scan(&importedClaims); err != nil || importedClaims != 3 {
		t.Fatalf("imported external claims=%d err=%v", importedClaims, err)
	}
	var localBindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions a JOIN recipes r ON r.id=a.recipe_id WHERE r.owner_id='importer' AND a.creator_id=?`, importerProfileID).Scan(&localBindings); err != nil || localBindings != 0 {
		t.Fatalf("foreign ID bound to equal local creator ID: count=%d err=%v", localBindings, err)
	}
	var importedLegacy string
	if err := db.QueryRow(`SELECT creator_name FROM recipes WHERE owner_id='importer' AND title='Hidden target'`).Scan(&importedLegacy); err != nil || importedLegacy != "Legacy hidden text" {
		t.Fatalf("independently allowed legacy text did not survive import: %q err=%v", importedLegacy, err)
	}
	reExport := httptest.NewRecorder()
	h.ServeHTTP(reExport, requestForUser(http.MethodGet, "/api/export", "importer"))
	if reExport.Code != http.StatusOK {
		t.Fatalf("re-export status=%d body=%s", reExport.Code, reExport.Body)
	}
	reExported := portableManifestFromResponse(t, reExport.Body.Bytes())
	if reExported.Format != portableFormatV8 || len(reExported.Attributions) != 3 {
		t.Fatalf("re-export format/count=%q/%d", reExported.Format, len(reExported.Attributions))
	}
	originalByTitle := make(map[string]portableAttribution, len(claims))
	for targetID, original := range claims {
		for _, recipe := range bundle.Recipes {
			if recipe.ID == targetID {
				originalByTitle[recipe.Title] = original
			}
		}
	}
	reExportManifest, err := json.Marshal(reExported)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range reExported.Attributions {
		var targetTitle string
		if err := db.QueryRow(`SELECT title FROM recipes WHERE id=? AND owner_id='importer'`, item.TargetID).Scan(&targetTitle); err != nil {
			t.Fatal(err)
		}
		original := originalByTitle[targetTitle]
		if original.SourceInstanceID != item.SourceInstanceID || original.CreatorID != item.CreatorID || original.NameClaim != item.NameClaim || original.Role != item.Role || original.Provenance != item.Provenance {
			t.Errorf("round-trip claim changed or recursively wrapped: original=%+v re-export=%+v", original, item)
		}
		if targetTitle == "Hidden target" || strings.Contains(string(reExportManifest), "importer@example.test") {
			t.Error("re-export exposed a previously omitted private profile or importer account")
		}
	}
}

func TestPortableV4ValidatesClaimBoundsTargetsAndCompatibility(t *testing.T) {
	base := portableBundle{
		Format: portableFormatV4, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{{ID: "recipe", Title: "Dish", Visibility: "private"}},
		Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{{TargetKind: "recipe", TargetID: "recipe", SourceInstanceID: strings.Repeat("a", 32), CreatorID: strings.Repeat("b", 32), Role: "creator", Provenance: "original"}},
	}
	if err := validatePortableBundle(base); err != nil {
		t.Fatalf("valid v4 claim rejected: %v", err)
	}
	maxName := base
	maxName.Attributions = append([]portableAttribution(nil), base.Attributions...)
	maxName.Attributions[0].NameClaim = strings.Repeat("🍲", 80)
	if err := validatePortableBundle(maxName); err != nil {
		t.Fatalf("maximum valid Unicode name claim rejected: %v", err)
	}
	invalid := []struct {
		name string
		edit func(*portableBundle)
	}{
		{"missing target", func(b *portableBundle) { b.Attributions[0].TargetID = "missing" }},
		{"bad source ID", func(b *portableBundle) { b.Attributions[0].SourceInstanceID = "BAD" }},
		{"bad creator ID", func(b *portableBundle) { b.Attributions[0].CreatorID = strings.Repeat("A", 32) }},
		{"unknown role", func(b *portableBundle) { b.Attributions[0].Role = "owner" }},
		{"empty provenance", func(b *portableBundle) { b.Attributions[0].Provenance = "" }},
		{"long provenance", func(b *portableBundle) { b.Attributions[0].Provenance = strings.Repeat("p", 129) }},
		{"name over 80 scalars", func(b *portableBundle) { b.Attributions[0].NameClaim = strings.Repeat("🍲", 81) }},
		{"name over 320 bytes", func(b *portableBundle) { b.Attributions[0].NameClaim = strings.Repeat("x", 321) }},
		{"unknown target kind", func(b *portableBundle) { b.Attributions[0].TargetKind = "profile" }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Attributions = append([]portableAttribution(nil), base.Attributions...)
			test.edit(&candidate)
			if err := validatePortableBundle(candidate); err == nil {
				t.Fatal("invalid v4 claim accepted")
			}
		})
	}
	duplicate := base
	duplicate.Attributions = append(append([]portableAttribution(nil), base.Attributions...), base.Attributions[0])
	if err := validatePortableBundle(duplicate); err == nil {
		t.Fatal("duplicate target/source/creator/role accepted")
	}
	missingClaims := base
	missingClaims.Attributions = nil
	if err := validatePortableBundle(missingClaims); err == nil {
		t.Fatal("v4 with missing attributions field accepted")
	}
	for _, old := range []portableBundle{
		{Format: legacyPortableFormat, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}},
		{Format: portableFormat, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}},
		{Format: portableFormatV3, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}},
	} {
		if err := validatePortableBundle(old); err != nil {
			t.Errorf("legacy format %q rejected: %v", old.Format, err)
		}
	}
	oldWithClaim := portableBundle{Format: portableFormatV3, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: base.Attributions}
	if err := validatePortableBundle(oldWithClaim); err == nil {
		t.Fatal("v3 archive accepted v4 attribution payload")
	}
	validManifest, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	unknownManifest := portableV4ArchiveManifest(t, bytes.Replace(validManifest, []byte(`"attributions":[`), []byte(`"future_claim":true,"attributions":[`), 1))
	if _, _, err := decodePortableArchive(unknownManifest); err == nil {
		t.Fatal("v4 unknown manifest field accepted")
	}
}

func TestPortableImportRejectsMalformedUTF8BeforeWritingClaims(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('importer','importer@example.test','Importer','now')`); err != nil {
		t.Fatal(err)
	}
	bundle := portableBundle{
		Format: portableFormatV4, Cookbooks: []portableCookbook{},
		Recipes:   []portableRecipe{{ID: "source-recipe", Title: "Dish", Visibility: "private"}},
		Bookmarks: []portableBookmark{},
		Attributions: []portableAttribution{{
			TargetKind: "recipe", TargetID: "source-recipe", SourceInstanceID: strings.Repeat("a", 32),
			CreatorID: strings.Repeat("b", 32), NameClaim: "claimed name", Role: "creator", Provenance: "original",
		}},
	}
	validManifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		find []byte
		bad  []byte
	}{
		{
			name: "name claim",
			find: []byte(`"name_claim":"claimed name"`),
			bad:  append([]byte(`"name_claim":"`), append([]byte{0xff}, '"')...),
		},
		{
			name: "provenance",
			find: []byte(`"provenance":"original"`),
			bad:  append([]byte(`"provenance":"`), append([]byte{0xfe}, '"')...),
		},
	}
	h := apiHandler{db: db}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			invalidManifest := bytes.Replace(validManifest, test.find, test.bad, 1)
			if bytes.Equal(invalidManifest, validManifest) {
				t.Fatal("malformed-byte fixture field was not replaced")
			}
			response := importPortableV4(t, h, portableV4ArchiveManifest(t, invalidManifest), "importer")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("malformed UTF-8 import status=%d body=%s", response.Code, response.Body)
			}
			var recipes, attributions int
			if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='importer'`).Scan(&recipes); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions`).Scan(&attributions); err != nil {
				t.Fatal(err)
			}
			if recipes != 0 || attributions != 0 {
				t.Fatalf("invalid manifest wrote partial import state: recipes=%d attributions=%d", recipes, attributions)
			}
		})
	}
	legacyJSON := append([]byte(`{"format":"cookbook-export-v1","cookbooks":[],"recipes":[],"claim":"`), append([]byte{0xff}, '"', '}')...)
	if _, _, err := decodePortableArchive(legacyJSON); err == nil {
		t.Fatal("legacy JSON path accepted malformed UTF-8")
	}
}

func TestPortableV4ImportAttributionFailureRollsBackResources(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('importer','importer@example.test','Importer','now');
		CREATE TRIGGER fail_external_claim BEFORE INSERT ON content_attributions WHEN NEW.subject_kind='external_claim' BEGIN SELECT RAISE(ABORT,'injected claim conflict'); END`); err != nil {
		t.Fatal(err)
	}
	bundle := portableBundle{Format: portableFormatV4, Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{{ID: "source-recipe", Title: "Imported dish", Visibility: "private"}}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{{TargetKind: "recipe", TargetID: "source-recipe", SourceInstanceID: strings.Repeat("a", 32), CreatorID: strings.Repeat("b", 32), Role: "creator", Provenance: "source"}}}
	archive := portableV4Archive(t, bundle)
	response := importPortableV4(t, apiHandler{db: db}, archive, "importer")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("injected attribution conflict status=%d body=%s", response.Code, response.Body)
	}
	var recipes, attributions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='importer'`).Scan(&recipes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions`).Scan(&attributions); err != nil || recipes != 0 || attributions != 0 {
		t.Fatalf("failed v4 import was not atomic: recipes=%d attributions=%d err=%v", recipes, attributions, err)
	}
}
