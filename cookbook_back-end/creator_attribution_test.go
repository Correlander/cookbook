package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreatorAttributionSchemaXORAndPartialUniqueIndexes(t *testing.T) {
	db := testDatabase(t)
	profileID := strings.Repeat("a", 32)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'owner','','private','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES('recipe','owner','','Dish','now','now');
		INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES('book','owner','Book','now','now')`, profileID); err != nil {
		t.Fatal(err)
	}
	localRecipe := `INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES(?,?,'local',?,'creator','self_asserted_at_creation','now')`
	if _, err := db.Exec(localRecipe, strings.Repeat("1", 32), "recipe", profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(localRecipe, strings.Repeat("2", 32), "recipe", profileID); err == nil {
		t.Fatal("partial index accepted a duplicate local recipe creator")
	}
	externalRecipe := `INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at) VALUES(?,?,'external_claim',?,?,?,'creator','portable_claim','now')`
	if _, err := db.Exec(externalRecipe, strings.Repeat("3", 32), "recipe", strings.Repeat("b", 32), profileID, strings.Repeat("🍲", 80)); err != nil {
		t.Fatalf("local ID equal to external claim ID collided: %v", err)
	}
	if _, err := db.Exec(externalRecipe, strings.Repeat("4", 32), "recipe", strings.Repeat("b", 32), profileID, "different claim name"); err == nil {
		t.Fatal("partial index accepted a duplicate external claim key")
	}
	if _, err := db.Exec(externalRecipe, strings.Repeat("5", 32), "recipe", strings.Repeat("c", 32), profileID, "same creator from another source"); err != nil {
		t.Fatalf("same creator ID from distinct source namespaces collided: %v", err)
	}
	invalidRows := []string{
		`INSERT INTO content_attributions(id,recipe_id,cookbook_id,subject_kind,creator_id,role,provenance,created_at) VALUES('dddddddddddddddddddddddddddddddd','recipe','book','local',?,'creator','self_asserted_at_creation','now')`,
		`INSERT INTO content_attributions(id,recipe_id,subject_kind,role,provenance,created_at) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','recipe','local','creator','self_asserted_at_creation','now')`,
		`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,claim_creator_id,role,provenance,created_at) VALUES('ffffffffffffffffffffffffffffffff','recipe','local',?,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','creator','self_asserted_at_creation','now')`,
		`INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at) VALUES('99999999999999999999999999999999','recipe','external_claim','BAD','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','creator','portable_claim','now')`,
		`INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at) VALUES('88888888888888888888888888888888','book','local',?,'adapter','adapter_source','now')`,
	}
	for index, statement := range invalidRows {
		var args []any
		if strings.Contains(statement, "?") {
			args = append(args, profileID)
		}
		if _, err := db.Exec(statement, args...); err == nil {
			t.Errorf("invalid attribution case %d was accepted", index)
		}
	}
}

func TestRecipeAttributionCreateOnlyChoicesAndPublicPrivacy(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Account Display Name','now')`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	create := func(title string, attribution any) recipe {
		t.Helper()
		body := map[string]any{"title": title}
		if attribution != nil {
			body["creator_attribution"] = attribution
		}
		response := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes", "owner", body)
		if response.Code != http.StatusOK {
			t.Fatalf("recipe %q create status=%d body=%s", title, response.Code, response.Body)
		}
		var item recipe
		if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	local := create("Local", map[string]any{"kind": "local_profile"})
	claim := create("Claim", map[string]any{"kind": "name_claim", "name": "Grandma"})
	unknown := create("Unknown", map[string]any{"kind": "unknown"})
	legacy := create("Legacy", nil)
	if local.CreatorName != "" || unknown.CreatorName != "" || claim.CreatorName != "Grandma" || legacy.CreatorName != "Account Display Name" {
		t.Fatalf("byline choices local=%q claim=%q unknown=%q legacy=%q", local.CreatorName, claim.CreatorName, unknown.CreatorName, legacy.CreatorName)
	}
	var localRows, nameClaimRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE recipe_id=? AND subject_kind='local' AND role='creator' AND provenance='self_asserted_at_creation'`, local.ID).Scan(&localRows); err != nil || localRows != 1 {
		t.Fatalf("local creator relation count=%d err=%v", localRows, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE recipe_id IN (?,?)`, claim.ID, unknown.ID).Scan(&nameClaimRows); err != nil || nameClaimRows != 0 {
		t.Fatalf("text-only choice created typed row: count=%d err=%v", nameClaimRows, err)
	}
	var publicName string
	if err := db.QueryRow(`SELECT public_name FROM creator_profiles WHERE user_id='owner'`).Scan(&publicName); err != nil || publicName != "" {
		t.Fatalf("lazy private creator profile name=%q err=%v", publicName, err)
	}
	if _, err := db.Exec(`UPDATE creator_profiles SET public_name='Private Profile Secret' WHERE user_id='owner';
		UPDATE instance_settings SET public_instance=1 WHERE id=1;
		UPDATE recipes SET visibility='public' WHERE id=?;
		UPDATE recipe_publication_policy SET publication_state='published',published_at='now' WHERE recipe_id=?`, local.ID, local.ID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/public/recipes/"+local.ID, nil)
	request.SetPathValue("id", local.ID)
	response := httptest.NewRecorder()
	publicRecipeDetailHandler{db: db}.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Private Profile Secret") || strings.Contains(response.Body.String(), `"creator_id"`) || publicName != "" {
		t.Fatalf("public local-attributed recipe exposed profile: status=%d body=%s", response.Code, response.Body)
	}
	if _, err := db.Exec(`UPDATE recipes SET visibility='private' WHERE id=?`, local.ID); err != nil {
		t.Fatal(err)
	}
	update := callCreatorProfileAPI(t, h, http.MethodPut, "/api/recipes/"+local.ID, "owner", map[string]any{
		"title": "Local", "version": 1, "creator_attribution": map[string]any{"kind": "unknown"},
	})
	if update.Code != http.StatusBadRequest || !strings.Contains(update.Body.String(), "invalid_json") {
		t.Fatalf("recipe update accepted create-only attribution: status=%d body=%s", update.Code, update.Body)
	}
	restore := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes/"+local.ID+"/revisions/1/restore", "owner", map[string]any{
		"expected_version": 1, "creator_attribution": map[string]any{"kind": "unknown"},
	})
	if restore.Code != http.StatusBadRequest || !strings.Contains(restore.Body.String(), "invalid_json") {
		t.Fatalf("revision restore accepted creator attribution: status=%d body=%s", restore.Code, restore.Body)
	}
	bad := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes", "owner", map[string]any{
		"title": "Invalid", "creator_attribution": map[string]any{"kind": "name_claim", "name": strings.Repeat("🍲", 81)},
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("oversized name claim status=%d body=%s", bad.Code, bad.Body)
	}
}

func TestLocalProfileRecipeAttributionRollsBackWholeCreateOnLateFailure(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_local_profile_create_audit
		BEFORE INSERT ON audit_events WHEN NEW.action='recipe.publication.initialize'
		BEGIN SELECT RAISE(ABORT,'injected late create failure'); END`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	input := map[string]any{
		"title": "Atomic profile recipe", "ingredients": []ingredient{}, "steps": []recipeStep{{Text: "Cook"}},
		"visibility": "private",
		"initial_publication": map[string]any{
			"publication_state": "unpublished", "federation_mode": "off", "index_eligible": false,
		},
		"creator_attribution": map[string]any{"kind": "local_profile"},
	}
	failed := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes", "owner", input)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected late failure status=%d body=%s", failed.Code, failed.Body)
	}
	checks := map[string]string{
		"recipe":             `SELECT COUNT(*) FROM recipes WHERE owner_id='owner'`,
		"profile":            `SELECT COUNT(*) FROM creator_profiles WHERE user_id='owner'`,
		"publication policy": `SELECT COUNT(*) FROM recipe_publication_policy p JOIN recipes r ON r.id=p.recipe_id WHERE r.owner_id='owner'`,
		"revision":           `SELECT COUNT(*) FROM recipe_revisions v JOIN recipes r ON r.id=v.recipe_id WHERE r.owner_id='owner'`,
		"attribution":        `SELECT COUNT(*) FROM content_attributions a JOIN recipes r ON r.id=a.recipe_id WHERE r.owner_id='owner'`,
		"federation object":  `SELECT COUNT(*) FROM federation_local_objects o JOIN recipes r ON r.id=o.recipe_id WHERE r.owner_id='owner'`,
		"peer object":        `SELECT COUNT(*) FROM federation_local_peer_objects o JOIN recipes r ON r.id=o.recipe_id WHERE r.owner_id='owner'`,
		"outbox":             `SELECT COUNT(*) FROM federation_outbox`,
		"audit":              `SELECT COUNT(*) FROM audit_events WHERE action='recipe.publication.initialize'`,
	}
	for name, query := range checks {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatalf("count %s after rollback: %v", name, err)
		}
		if count != 0 {
			t.Errorf("late create failure left %s rows: %d", name, count)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER fail_local_profile_create_audit`); err != nil {
		t.Fatal(err)
	}
	created := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes", "owner", input)
	if created.Code != http.StatusOK {
		t.Fatalf("control recipe create status=%d body=%s", created.Code, created.Body)
	}
	var recipeRows, profileRows, policyRows, revisionRows, outboxRows, attributionRows, auditRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='owner'`).Scan(&recipeRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM creator_profiles WHERE user_id='owner' AND public_name='' AND visibility='private'`).Scan(&profileRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_publication_policy p JOIN recipes r ON r.id=p.recipe_id WHERE r.owner_id='owner' AND p.publication_state='unpublished' AND p.federation_mode='off'`).Scan(&policyRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions v JOIN recipes r ON r.id=v.recipe_id WHERE r.owner_id='owner'`).Scan(&revisionRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions a JOIN recipes r ON r.id=a.recipe_id WHERE r.owner_id='owner' AND a.subject_kind='local'`).Scan(&attributionRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='recipe.publication.initialize'`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if recipeRows != 1 || profileRows != 1 || policyRows != 1 || revisionRows != 1 || outboxRows != 0 || attributionRows != 1 || auditRows != 1 {
		t.Fatalf("success control did not exercise complete private-create transaction: recipes=%d profiles=%d policies=%d revisions=%d outbox=%d attributions=%d audit=%d", recipeRows, profileRows, policyRows, revisionRows, outboxRows, attributionRows, auditRows)
	}
}

func TestPublishedFederatedRecipeAttributionRollsBackOnLateFailure(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	insertCompletedPublisherProfile(t, db, "owner", "Private publisher")
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	var profileID, profileName, profileVisibility string
	if err := db.QueryRow(`SELECT creator_id,public_name,visibility FROM creator_profiles WHERE user_id='owner'`).Scan(&profileID, &profileName, &profileVisibility); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_federated_profile_recipe_audit
		BEFORE INSERT ON audit_events WHEN NEW.action='recipe.publication.initialize'
		BEGIN SELECT RAISE(ABORT,'injected late create failure'); END`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	input := map[string]any{
		"title": "Atomic federated profile recipe", "ingredients": []ingredient{}, "steps": []recipeStep{{Text: "Cook"}},
		"visibility": "public", "confirm_public_access": true,
		"initial_publication": map[string]any{
			"publication_state": "published", "federation_mode": "followers", "index_eligible": false,
		},
		"creator_attribution": map[string]any{"kind": "local_profile"},
	}
	failed := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes", "owner", input)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected late failure status=%d body=%s", failed.Code, failed.Body)
	}
	for _, check := range []struct {
		name  string
		query string
	}{
		{"recipe", `SELECT COUNT(*) FROM recipes WHERE owner_id='owner'`},
		{"policy", `SELECT COUNT(*) FROM recipe_publication_policy p JOIN recipes r ON r.id=p.recipe_id WHERE r.owner_id='owner'`},
		{"revision", `SELECT COUNT(*) FROM recipe_revisions v JOIN recipes r ON r.id=v.recipe_id WHERE r.owner_id='owner'`},
		{"attribution", `SELECT COUNT(*) FROM content_attributions a JOIN recipes r ON r.id=a.recipe_id WHERE r.owner_id='owner'`},
		{"local federation object", `SELECT COUNT(*) FROM federation_local_objects`},
		{"peer federation object", `SELECT COUNT(*) FROM federation_local_peer_objects`},
		{"outbox", `SELECT COUNT(*) FROM federation_outbox`},
		{"audit", `SELECT COUNT(*) FROM audit_events WHERE action='recipe.publication.initialize'`},
	} {
		var count int
		if err := db.QueryRow(check.query).Scan(&count); err != nil {
			t.Fatalf("count %s after injected failure: %v", check.name, err)
		}
		if count != 0 {
			t.Errorf("late federated create failure left %s rows: %d", check.name, count)
		}
	}
	var preservedID, preservedName, preservedVisibility string
	if err := db.QueryRow(`SELECT creator_id,public_name,visibility FROM creator_profiles WHERE user_id='owner'`).Scan(&preservedID, &preservedName, &preservedVisibility); err != nil {
		t.Fatalf("completed profile was not preserved after rollback: %v", err)
	}
	if preservedID != profileID || preservedName != profileName || preservedVisibility != profileVisibility {
		t.Fatalf("completed profile changed after rollback: before=%q/%q/%q after=%q/%q/%q", profileID, profileName, profileVisibility, preservedID, preservedName, preservedVisibility)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_federated_profile_recipe_audit`); err != nil {
		t.Fatal(err)
	}
	created := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes", "owner", input)
	if created.Code != http.StatusOK {
		t.Fatalf("eligible federated create control status=%d body=%s", created.Code, created.Body)
	}
	var recipes, policies, revisions, attributions, localObjects, peerObjects, outbox, audits int
	for _, check := range []struct {
		name  string
		query string
		total *int
	}{
		{"recipe", `SELECT COUNT(*) FROM recipes WHERE owner_id='owner'`, &recipes},
		{"policy", `SELECT COUNT(*) FROM recipe_publication_policy p JOIN recipes r ON r.id=p.recipe_id WHERE r.owner_id='owner' AND p.publication_state='published' AND p.federation_mode='followers'`, &policies},
		{"revision", `SELECT COUNT(*) FROM recipe_revisions v JOIN recipes r ON r.id=v.recipe_id WHERE r.owner_id='owner'`, &revisions},
		{"attribution", `SELECT COUNT(*) FROM content_attributions a JOIN recipes r ON r.id=a.recipe_id WHERE r.owner_id='owner' AND a.subject_kind='local'`, &attributions},
		{"local federation object", `SELECT COUNT(*) FROM federation_local_objects`, &localObjects},
		{"peer federation object", `SELECT COUNT(*) FROM federation_local_peer_objects`, &peerObjects},
		{"outbox", `SELECT COUNT(*) FROM federation_outbox`, &outbox},
		{"audit", `SELECT COUNT(*) FROM audit_events WHERE action='recipe.publication.initialize'`, &audits},
	} {
		if err := db.QueryRow(check.query).Scan(check.total); err != nil {
			t.Fatalf("count %s after successful federated create: %v", check.name, err)
		}
	}
	var controlProfileID, controlName, controlVisibility string
	if err := db.QueryRow(`SELECT creator_id,public_name,visibility FROM creator_profiles WHERE user_id='owner'`).Scan(&controlProfileID, &controlName, &controlVisibility); err != nil {
		t.Fatal(err)
	}
	if recipes != 1 || policies != 1 || revisions != 1 || attributions != 1 || localObjects != 1 || peerObjects != 1 || outbox != 1 || audits != 1 {
		t.Fatalf("successful federated create did not exercise the full transaction: recipes=%d policies=%d revisions=%d attributions=%d local=%d peer=%d outbox=%d audits=%d", recipes, policies, revisions, attributions, localObjects, peerObjects, outbox, audits)
	}
	if controlProfileID != profileID || controlName != profileName || controlVisibility != profileVisibility {
		t.Fatalf("successful publication changed existing publisher profile: before=%q/%q/%q after=%q/%q/%q", profileID, profileName, profileVisibility, controlProfileID, controlName, controlVisibility)
	}
}

func TestCookbookAndForkAttributionsAreAtomic(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('cookbook-owner','cookbook@example.test','Book owner','now'),
		('source-owner','source@example.test','Source owner','now'),
		('forker','forker@example.test','Forker account name','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,visibility,created_at,updated_at) VALUES('source','source-owner','Source byline','Source','public','now','now');
		UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	if _, err := db.Exec(`CREATE TRIGGER fail_book_attribution BEFORE INSERT ON content_attributions WHEN NEW.cookbook_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	failedBook := callCreatorProfileAPI(t, h, http.MethodPost, "/api/cookbooks", "cookbook-owner", map[string]string{"title": "Atomic book"})
	var books, ownerProfiles int
	if failedBook.Code != http.StatusInternalServerError {
		t.Fatalf("injected cookbook attribution failure status=%d body=%s", failedBook.Code, failedBook.Body)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE owner_id='cookbook-owner'`).Scan(&books); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM creator_profiles WHERE user_id='cookbook-owner'`).Scan(&ownerProfiles); err != nil || books != 0 || ownerProfiles != 0 {
		t.Fatalf("cookbook failure was not atomic: books=%d profiles=%d err=%v", books, ownerProfiles, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_book_attribution`); err != nil {
		t.Fatal(err)
	}
	createdBook := callCreatorProfileAPI(t, h, http.MethodPost, "/api/cookbooks", "cookbook-owner", map[string]string{"title": "Atomic book"})
	if createdBook.Code != http.StatusCreated {
		t.Fatalf("cookbook create status=%d body=%s", createdBook.Code, createdBook.Body)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions a JOIN cookbooks c ON c.id=a.cookbook_id WHERE c.owner_id='cookbook-owner' AND a.role='creator' AND a.provenance='local_record_creation'`).Scan(&books); err != nil || books != 1 {
		t.Fatalf("cookbook creator relation count=%d err=%v", books, err)
	}
	if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'source-owner','','private','now','now')`, strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES(?, 'source','local',?,'creator','self_asserted_at_creation','now')`, strings.Repeat("1", 32), strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_adapter_attribution BEFORE INSERT ON content_attributions WHEN NEW.role='adapter' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	forkFailure := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes/source/fork", "forker", map[string]any{})
	var forkRows, forkerProfiles int
	if forkFailure.Code != http.StatusInternalServerError {
		t.Fatalf("injected fork attribution failure status=%d body=%s", forkFailure.Code, forkFailure.Body)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE owner_id='forker'`).Scan(&forkRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM creator_profiles WHERE user_id='forker'`).Scan(&forkerProfiles); err != nil || forkRows != 0 || forkerProfiles != 0 {
		t.Fatalf("fork failure was not atomic: forks=%d profiles=%d err=%v", forkRows, forkerProfiles, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_adapter_attribution`); err != nil {
		t.Fatal(err)
	}
	fork := callCreatorProfileAPI(t, h, http.MethodPost, "/api/recipes/source/fork", "forker", map[string]any{})
	if fork.Code != http.StatusCreated {
		t.Fatalf("fork status=%d body=%s", fork.Code, fork.Body)
	}
	var result recipe
	if err := json.Unmarshal(fork.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SourceRecipeID != "source" || result.CreatorName != "Source byline" || result.OwnerID != "forker" {
		t.Fatalf("fork custody/source was changed: %+v", result)
	}
	var adapterCount, sourceCreatorCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE recipe_id=? AND subject_kind='local' AND role='adapter' AND provenance='adapter_source'`, result.ID).Scan(&adapterCount); err != nil || adapterCount != 1 {
		t.Fatalf("fork adapter count=%d err=%v", adapterCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE recipe_id='source' AND subject_kind='local' AND role='creator'`).Scan(&sourceCreatorCount); err != nil || sourceCreatorCount != 1 {
		t.Fatalf("source creator relation changed: count=%d err=%v", sourceCreatorCount, err)
	}
	var forkerName string
	if err := db.QueryRow(`SELECT public_name FROM creator_profiles WHERE user_id='forker'`).Scan(&forkerName); err != nil || forkerName != "" {
		t.Fatalf("fork auto-profile leaked display name: %q err=%v", forkerName, err)
	}
}
