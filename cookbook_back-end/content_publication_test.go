package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

const publicationFixtureTime = "2026-11-03T12:00:00.000000000Z"

type publicationTestEnv struct {
	db        *sql.DB
	handler   http.Handler
	owner     *http.Cookie
	other     *http.Cookie
	recipient *http.Cookie
}

func newPublicationTestEnv(t *testing.T, visibilityValue visibility, state RecipePublicationState, mode RecipeFederationMode, federation bool) publicationTestEnv {
	t.Helper()
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('owner','owner@example.test','Owner',?),('other','other@example.test','Other',?),('recipient','recipient@example.test','Recipient',?)`,
		publicationFixtureTime, publicationFixtureTime, publicationFixtureTime); err != nil {
		t.Fatal(err)
	}
	origin := ""
	if federation {
		origin = testFederationOrigin
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=?,federation_origin=? WHERE id=1`, boolInt(federation), origin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES('0123456789abcdef0123456789abcdef','owner','Owner','Soup','Warm soup','[]','[{"text":"Cook"}]',2,5,10,'[]',?,'owner_only',1,?,?)`, visibilityValue, publicationFixtureTime, publicationFixtureTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,created_at)
		VALUES('revision-1','0123456789abcdef0123456789abcdef',1,'owner','Soup','Warm soup','[]','[{"text":"Cook"}]',2,5,10,'[]',?,?)`, visibilityValue, publicationFixtureTime); err != nil {
		t.Fatal(err)
	}
	var publishedAt any
	if state == RecipePublicationPublished {
		publishedAt = publicationFixtureTime
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_by,updated_at,published_at)
		VALUES('0123456789abcdef0123456789abcdef',?,?,0,'owner',?,?)`, state, mode, publicationFixtureTime, publishedAt); err != nil {
		t.Fatal(err)
	}
	if federation {
		if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
			t.Fatal(err)
		}
		if state == RecipePublicationPublished && mode == RecipeFederationFollowers {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := enqueueFederationChange(tx, testFederationID); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	ownerToken, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, err := issueSession(db, "other")
	if err != nil {
		t.Fatal(err)
	}
	recipientToken, _, err := issueSession(db, "recipient")
	if err != nil {
		t.Fatal(err)
	}
	return publicationTestEnv{
		db: db, handler: handlerWithAuth(db, testAuth(db)),
		owner:     &http.Cookie{Name: sessionCookie, Value: ownerToken},
		other:     &http.Cookie{Name: sessionCookie, Value: otherToken},
		recipient: &http.Cookie{Name: sessionCookie, Value: recipientToken},
	}
}

func callContentAPI(h http.Handler, method, path, body string, cookie *http.Cookie, ifMatch ...string) *httptest.ResponseRecorder {
	var requestBody *strings.Reader
	if body == "" {
		requestBody = strings.NewReader("")
	} else {
		requestBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, requestBody)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if method != http.MethodGet {
		req.Host = "localhost:5173"
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Content-Type", "application/json")
	}
	for _, value := range ifMatch {
		if value != "" {
			req.Header.Add("If-Match", value)
		}
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	return response
}

func publicationGET(t *testing.T, env publicationTestEnv) (recipePublicationView, string) {
	t.Helper()
	response := callContentAPI(env.handler, http.MethodGet, "/api/recipes/"+testFederationID+"/publication", "", env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("publication GET=%d %s", response.Code, response.Body)
	}
	var view recipePublicationView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view, response.Header().Get("ETag")
}

func publicationPUT(t *testing.T, env publicationTestEnv, cookie *http.Cookie, etag string, input recipePublicationInput) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return callContentAPI(env.handler, http.MethodPut, "/api/recipes/"+testFederationID+"/publication", string(body), cookie, etag)
}

func TestRecipePublicationOwnerRouteAndFailClosedReads(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPrivate, RecipePublicationUnpublished, RecipeFederationOff, false)
	if response := callContentAPI(env.handler, http.MethodGet, "/api/recipes/"+testFederationID+"/publication", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous publication GET=%d %s", response.Code, response.Body)
	}
	if response := callContentAPI(env.handler, http.MethodGet, "/api/recipes/"+testFederationID+"/publication", "", env.other); response.Code != http.StatusNotFound {
		t.Fatalf("non-owner publication GET=%d %s", response.Code, response.Body)
	}
	view, etag := publicationGET(t, env)
	if denied := publicationPUT(t, env, env.other, etag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff}); denied.Code != http.StatusNotFound {
		t.Fatalf("non-owner publication PUT=%d %s", denied.Code, denied.Body)
	}
	digest := sha256.Sum256(envPublicationBytes(t, env))
	if etag != `"sha256:`+hex.EncodeToString(digest[:])+`"` || len(etag) != len(`"sha256:`)+64+1 {
		t.Fatalf("unexpected canonical ETag %q", etag)
	}
	if view.RecipeID != testFederationID || view.PublicationState != RecipePublicationUnpublished || view.FederationMode != RecipeFederationOff || view.IndexEligible || view.UpdatedBy == nil || *view.UpdatedBy != "owner" || view.Visibility != visibilityPrivate || view.PublicInstance != true || view.FederationEnabled || responseCacheControl(t, env) != "private, no-store, no-transform" {
		t.Fatalf("unexpected owner publication view: %+v", view)
	}
	if _, err := env.db.Exec(`DELETE FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`); err != nil {
		t.Fatal(err)
	}
	if response := callContentAPI(env.handler, http.MethodGet, "/api/recipes/"+testFederationID+"/publication", "", env.other); response.Code != http.StatusNotFound {
		t.Fatalf("non-owner learned about missing policy: %d %s", response.Code, response.Body)
	}
	if response := callContentAPI(env.handler, http.MethodGet, "/api/recipes/"+testFederationID+"/publication", "", env.owner); response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "storage_error") {
		t.Fatalf("missing policy GET=%d %s", response.Code, response.Body)
	}
	var count int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("missing policy was repaired: count=%d err=%v", count, err)
	}
}

func envPublicationBytes(t *testing.T, env publicationTestEnv) []byte {
	t.Helper()
	response := callContentAPI(env.handler, http.MethodGet, "/api/recipes/"+testFederationID+"/publication", "", env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("publication GET=%d %s", response.Code, response.Body)
	}
	return response.Body.Bytes()
}

func responseCacheControl(t *testing.T, env publicationTestEnv) string {
	t.Helper()
	response := callContentAPI(env.handler, http.MethodGet, "/api/recipes/"+testFederationID+"/publication", "", env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("publication GET=%d %s", response.Code, response.Body)
	}
	return response.Header().Get("Cache-Control")
}

func TestRecipePublicationIfMatchNoOpAndConcurrentPrecondition(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationUnpublished, RecipeFederationOff, false)
	_, etag := publicationGET(t, env)
	input := recipePublicationInput{PublicationState: RecipePublicationUnpublished, FederationMode: RecipeFederationOff}
	if response := publicationPUT(t, env, env.owner, "", input); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match=%d %s", response.Code, response.Body)
	}
	for _, invalid := range []string{`*`, `W/` + etag, etag + ", " + etag, `"sha256:ABC"`} {
		if response := publicationPUT(t, env, env.owner, invalid, input); response.Code != http.StatusBadRequest {
			t.Errorf("invalid If-Match %q status=%d body=%s", invalid, response.Code, response.Body)
		}
	}
	missingField := callContentAPI(env.handler, http.MethodPut, "/api/recipes/"+testFederationID+"/publication", `{"publication_state":"unpublished","federation_mode":"off"}`, env.owner, etag)
	if missingField.Code != http.StatusBadRequest || !strings.Contains(missingField.Body.String(), "invalid_json") {
		t.Fatalf("incomplete policy tuple=%d %s", missingField.Code, missingField.Body)
	}
	if response := publicationPUT(t, env, env.owner, etag, input); response.Code != http.StatusOK || response.Header().Get("ETag") != etag || response.Header().Get("Cache-Control") != "private, no-store, no-transform" || response.Body.String() != string(envPublicationBytes(t, env)) {
		t.Fatalf("no-op PUT=%d cache-control=%q etag=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Header().Get("ETag"), response.Body)
	}
	var audits, outbox int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if audits != 0 || outbox != 0 {
		t.Fatalf("no-op wrote audit/outbox: audits=%d outbox=%d", audits, outbox)
	}
	input.PublicationState = RecipePublicationPublished
	responses := make([]*httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := range responses {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			responses[index] = publicationPUT(t, env, env.owner, etag, input)
		}(i)
	}
	close(start)
	wait.Wait()
	statuses := map[int]int{}
	for _, response := range responses {
		statuses[response.Code]++
	}
	if statuses[http.StatusOK] != 1 || statuses[http.StatusPreconditionFailed] != 1 {
		t.Fatalf("same-ETag concurrent PUT statuses=%v responses=%s / %s", statuses, responses[0].Body, responses[1].Body)
	}
	current, currentETag := publicationGET(t, env)
	if current.PublicationState != RecipePublicationPublished || current.PublishedAt == nil || currentETag == etag {
		t.Fatalf("publication transition did not advance state/ETag: view=%+v tag=%s", current, currentETag)
	}
	for _, response := range responses {
		if response.Code == http.StatusPreconditionFailed {
			if response.Header().Get("Cache-Control") != "private, no-store, no-transform" || response.Header().Get("ETag") != currentETag || response.Body.String() != "{\"error\":\"precondition_failed\"}\n" {
				t.Fatalf("stale concurrent PUT=%d cache-control=%q etag=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Header().Get("ETag"), response.Body)
			}
			continue
		}
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store, no-transform" || response.Header().Get("ETag") != currentETag || response.Body.String() != string(envPublicationBytes(t, env)) {
			t.Fatalf("successful concurrent PUT=%d cache-control=%q etag=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Header().Get("ETag"), response.Body)
		}
	}
	stale := publicationPUT(t, env, env.owner, etag, input)
	if stale.Code != http.StatusPreconditionFailed || stale.Header().Get("Cache-Control") != "private, no-store, no-transform" || stale.Header().Get("ETag") != currentETag || stale.Body.String() != "{\"error\":\"precondition_failed\"}\n" {
		t.Fatalf("stale PUT=%d cache-control=%q etag=%q body=%s", stale.Code, stale.Header().Get("Cache-Control"), stale.Header().Get("ETag"), stale.Body)
	}
	var version, revisions, actionCount int
	if err := env.db.QueryRow(`SELECT version FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='recipe.publication.publish'`).Scan(&actionCount); err != nil {
		t.Fatal(err)
	}
	if version != 1 || revisions != 1 || actionCount != 1 {
		t.Fatalf("policy PUT changed body or duplicate audit: version=%d revisions=%d publish_audits=%d", version, revisions, actionCount)
	}
	publishedAt := *current.PublishedAt
	withdraw := publicationPUT(t, env, env.owner, currentETag, recipePublicationInput{PublicationState: RecipePublicationWithdrawn, FederationMode: RecipeFederationOff})
	if withdraw.Code != http.StatusOK {
		t.Fatalf("withdrawal=%d %s", withdraw.Code, withdraw.Body)
	}
	withdrawn, withdrawnETag := publicationGET(t, env)
	if withdrawn.PublicationState != RecipePublicationWithdrawn || withdrawn.PublishedAt == nil || *withdrawn.PublishedAt != publishedAt || withdrawnETag == currentETag {
		t.Fatalf("withdraw did not preserve publication history/change ETag: %+v %s", withdrawn, withdrawnETag)
	}
	republish := publicationPUT(t, env, env.owner, withdrawnETag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff})
	if republish.Code != http.StatusOK {
		t.Fatalf("republish=%d %s", republish.Code, republish.Body)
	}
	republished, republishedETag := publicationGET(t, env)
	if republished.PublicationState != RecipePublicationPublished || republished.PublishedAt == nil || *republished.PublishedAt == publishedAt || republishedETag == withdrawnETag {
		t.Fatalf("republish failed to establish new publication time/tag: %+v %s", republished, republishedETag)
	}
}

func TestRecipePublicationGatesAndTransactionalFailures(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationUnpublished, RecipeFederationOff, false)
	_, etag := publicationGET(t, env)
	if response := publicationPUT(t, env, env.owner, etag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff, IndexEligible: true}); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "index_submission_unavailable") {
		t.Fatalf("index opt-in status=%d %s", response.Code, response.Body)
	}
	unavailable := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationOff, false)
	_, unavailableETag := publicationGET(t, unavailable)
	response := publicationPUT(t, unavailable, unavailable.owner, unavailableETag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationFollowers})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "federation_unavailable") {
		t.Fatalf("follower opt-in without federation=%d %s", response.Code, response.Body)
	}
	if _, err := env.db.Exec(`CREATE TRIGGER reject_publication_audit BEFORE INSERT ON audit_events WHEN NEW.action='recipe.publication.publish' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	response = publicationPUT(t, env, env.owner, etag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("audit failure PUT=%d %s", response.Code, response.Body)
	}
	var state, mode, updatedAt string
	var published sql.NullString
	if err := env.db.QueryRow(`SELECT publication_state,federation_mode,updated_at,published_at FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&state, &mode, &updatedAt, &published); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationUnpublished) || mode != string(RecipeFederationOff) || updatedAt != publicationFixtureTime || published.Valid {
		t.Fatalf("audit failure partially committed policy: %s/%s/%s/%+v", state, mode, updatedAt, published)
	}
	if _, err := env.db.Exec(`DROP TRIGGER reject_publication_audit`); err != nil {
		t.Fatal(err)
	}

	fed := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationOff, true)
	_, fedETag := publicationGET(t, fed)
	if _, err := fed.db.Exec(`CREATE TRIGGER reject_publication_outbox BEFORE INSERT ON federation_outbox BEGIN SELECT RAISE(ABORT,'outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
	response = publicationPUT(t, fed, fed.owner, fedETag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationFollowers})
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "federation_queue_unavailable") {
		t.Fatalf("outbox failure PUT=%d %s", response.Code, response.Body)
	}
	if err := fed.db.QueryRow(`SELECT federation_mode FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&mode); err != nil || mode != string(RecipeFederationOff) {
		t.Fatalf("outbox failure committed consent=%q err=%v", mode, err)
	}
	var audits int
	if err := fed.db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("outbox failure committed audit=%d err=%v", audits, err)
	}
}

func TestRecipePublicationConsentRevocationRequiresTombstoneIdentity(t *testing.T) {
	offline := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, false)
	_, offlineETag := publicationGET(t, offline)
	response := publicationPUT(t, offline, offline.owner, offlineETag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff})
	if response.Code != http.StatusOK {
		t.Fatalf("revocation blocked by disabled federation gate=%d %s", response.Code, response.Body)
	}
	var storedMode string
	if err := offline.db.QueryRow(`SELECT federation_mode FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&storedMode); err != nil || storedMode != string(RecipeFederationOff) {
		t.Fatalf("disabled gate rewrote or blocked owner revocation=%q err=%v", storedMode, err)
	}

	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	if _, err := env.db.Exec(`UPDATE instance_settings SET federation_origin='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	_, etag := publicationGET(t, env)
	response = publicationPUT(t, env, env.owner, etag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff})
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "federation_queue_unavailable") {
		t.Fatalf("revocation without stable object identity=%d %s", response.Code, response.Body)
	}
	var mode string
	if err := env.db.QueryRow(`SELECT federation_mode FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&mode); err != nil || mode != string(RecipeFederationFollowers) {
		t.Fatalf("failed tombstone changed owner consent=%q err=%v", mode, err)
	}
}

func TestRecipeVisibilityExpansionConfirmationAndAuditRollback(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPrivate, RecipePublicationUnpublished, RecipeFederationOff, false)
	body := `{"title":"Soup updated","description":"Warm soup","ingredients":[],"steps":[{"text":"Cook"}],"servings":2,"prep_time_minutes":5,"cook_time_minutes":10,"tags":[],"visibility":"public","version":1}`
	response := callContentAPI(env.handler, http.MethodPut, "/api/recipes/"+testFederationID, body, env.owner)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "publication_confirmation_required") {
		t.Fatalf("public visibility without confirmation=%d %s", response.Code, response.Body)
	}
	if _, err := env.db.Exec(`CREATE TRIGGER reject_visibility_audit BEFORE INSERT ON audit_events WHEN NEW.action='recipe.visibility.change' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	response = callContentAPI(env.handler, http.MethodPut, "/api/recipes/"+testFederationID, strings.TrimSuffix(body, `,"version":1}`)+`,"confirm_public_access":true,"version":1}`, env.owner)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("visibility audit failure=%d %s", response.Code, response.Body)
	}
	assertVisibilityRollbackState(t, env.db)
	if _, err := env.db.Exec(`DROP TRIGGER reject_visibility_audit`); err != nil {
		t.Fatal(err)
	}
	response = callContentAPI(env.handler, http.MethodPut, "/api/recipes/"+testFederationID, strings.TrimSuffix(body, `,"version":1}`)+`,"confirm_public_access":true,"version":1}`, env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("confirmed public visibility=%d %s", response.Code, response.Body)
	}
	var visibilityValue string
	if err := env.db.QueryRow(`SELECT visibility FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&visibilityValue); err != nil || visibilityValue != "public" {
		t.Fatalf("visibility=%s err=%v", visibilityValue, err)
	}
	var visibilityAudit, genericAudit, version int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='recipe.visibility.change'`).Scan(&visibilityAudit); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='PUT /recipes/{id}'`).Scan(&genericAudit); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT version FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if visibilityAudit != 1 || genericAudit != 0 || version != 2 {
		t.Fatalf("visibility change audit/body version = %d/%d/%d", visibilityAudit, genericAudit, version)
	}
	bodyOnly := `{"title":"Soup edited","description":"Warm soup","ingredients":[],"steps":[{"text":"Cook"}],"servings":2,"prep_time_minutes":5,"cook_time_minutes":10,"tags":[],"visibility":"public","version":2}`
	response = callContentAPI(env.handler, http.MethodPut, "/api/recipes/"+testFederationID, bodyOnly, env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("body-only update=%d %s", response.Code, response.Body)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='PUT /recipes/{id}'`).Scan(&genericAudit); err != nil {
		t.Fatal(err)
	}
	if genericAudit != 1 {
		t.Fatalf("body-only success audit suppressed or duplicated: %d", genericAudit)
	}
}

func assertVisibilityRollbackState(t *testing.T, db *sql.DB) {
	t.Helper()
	var visibilityValue string
	var version, revisions, audits int
	if err := db.QueryRow(`SELECT visibility,version FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&visibilityValue, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if visibilityValue != "private" || version != 1 || revisions != 1 || audits != 0 {
		t.Fatalf("visibility mutation partially committed: visibility=%s version=%d revisions=%d audits=%d", visibilityValue, version, revisions, audits)
	}
}

func TestRecipeCreatePublicationDefaultsAndExplicitChoice(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPrivate, RecipePublicationUnpublished, RecipeFederationOff, false)
	payload := `{"title":"New soup","description":"Created","ingredients":[],"steps":[{"text":"Cook"}],"servings":2,"prep_time_minutes":5,"cook_time_minutes":10,"tags":[],"visibility":"private"}`
	response := callContentAPI(env.handler, http.MethodPost, "/api/recipes", payload, env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("default create=%d %s", response.Code, response.Body)
	}
	var created recipe
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var state, mode, updatedBy, updatedAt string
	var published sql.NullString
	if err := env.db.QueryRow(`SELECT publication_state,federation_mode,updated_by,updated_at,published_at FROM recipe_publication_policy WHERE recipe_id=?`, created.ID).Scan(&state, &mode, &updatedBy, &updatedAt, &published); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationUnpublished) || mode != string(RecipeFederationOff) || updatedBy != "owner" || published.Valid || len(updatedAt) != len(publicationFixtureTime) {
		t.Fatalf("safe policy defaults=%s/%s/%s/%s/%+v", state, mode, updatedBy, updatedAt, published)
	}
	publicPayload := `{"title":"Public soup","ingredients":[],"steps":[{"text":"Cook"}],"visibility":"public"}`
	response = callContentAPI(env.handler, http.MethodPost, "/api/recipes", publicPayload, env.owner)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "publication_confirmation_required") {
		t.Fatalf("public create without confirmation=%d %s", response.Code, response.Body)
	}
	if _, err := env.db.Exec(`CREATE TRIGGER reject_create_audit BEFORE INSERT ON audit_events WHEN NEW.action='recipe.publication.initialize' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	confirmed := `{"title":"Public soup","ingredients":[],"steps":[{"text":"Cook"}],"visibility":"public","confirm_public_access":true,"initial_publication":{"publication_state":"published","federation_mode":"off","index_eligible":false}}`
	response = callContentAPI(env.handler, http.MethodPost, "/api/recipes", confirmed, env.owner)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("create audit failure=%d %s", response.Code, response.Body)
	}
	var publicRecipes, policies, revisions int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE title='Public soup'`).Scan(&publicRecipes); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM recipe_publication_policy p JOIN recipes r ON r.id=p.recipe_id WHERE r.title='Public soup'`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions rr JOIN recipes r ON r.id=rr.recipe_id WHERE r.title='Public soup'`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if publicRecipes != 0 || policies != 0 || revisions != 0 {
		t.Fatalf("failed create left partial state: recipes=%d policies=%d revisions=%d", publicRecipes, policies, revisions)
	}
	if _, err := env.db.Exec(`DROP TRIGGER reject_create_audit`); err != nil {
		t.Fatal(err)
	}
	response = callContentAPI(env.handler, http.MethodPost, "/api/recipes", confirmed, env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("confirmed create=%d %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT publication_state,federation_mode,updated_by,published_at FROM recipe_publication_policy WHERE recipe_id=?`, created.ID).Scan(&state, &mode, &updatedBy, &published); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationPublished) || mode != string(RecipeFederationOff) || updatedBy != "owner" || !published.Valid {
		t.Fatalf("explicit initial publication=%s/%s/%s/%+v", state, mode, updatedBy, published)
	}
}

func TestRecipeLifecycleWithdrawsAndNeverResurrectsLegacyPublication(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	_, oldETag := publicationGET(t, env)
	response := callContentAPI(env.handler, http.MethodDelete, "/api/recipes/"+testFederationID, "", env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("trash=%d %s", response.Code, response.Body)
	}
	var state, mode, updatedBy string
	var sequence, published, deleted, version, revisionCount int
	if err := env.db.QueryRow(`SELECT publication_state,federation_mode,updated_by FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&state, &mode, &updatedBy); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT sequence,published FROM federation_local_objects WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&sequence, &published); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT deleted_at IS NOT NULL,version FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&deleted, &version); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationWithdrawn) || mode != string(RecipeFederationOff) || updatedBy != "owner" || sequence != 2 || published != 0 || deleted != 1 || version != 1 || revisionCount != 1 {
		t.Fatalf("trash lifecycle state=%s/%s actor=%s seq=%d published=%d deleted=%d version=%d revisions=%d", state, mode, updatedBy, sequence, published, deleted, version, revisionCount)
	}
	response = callContentAPI(env.handler, http.MethodPost, "/api/recipes/"+testFederationID+"/restore", "", env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("restore=%d %s", response.Code, response.Body)
	}
	view, newETag := publicationGET(t, env)
	if view.PublicationState != RecipePublicationWithdrawn || view.FederationMode != RecipeFederationOff || newETag == oldETag {
		t.Fatalf("restore re-published or failed to invalidate old ETag: %+v %s", view, newETag)
	}
	stale := publicationPUT(t, env, env.owner, oldETag, recipePublicationInput{PublicationState: RecipePublicationPublished, FederationMode: RecipeFederationOff})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("pre-trash ETag accepted after restore=%d %s", stale.Code, stale.Body)
	}

	legacy := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationOff, false)
	if _, err := legacy.db.Exec(`UPDATE recipes SET deleted_at=? WHERE id='0123456789abcdef0123456789abcdef'`, publicationFixtureTime); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`UPDATE recipe_publication_policy SET updated_by=NULL,published_at=NULL WHERE recipe_id='0123456789abcdef0123456789abcdef'`); err != nil {
		t.Fatal(err)
	}
	response = callContentAPI(legacy.handler, http.MethodPost, "/api/recipes/"+testFederationID+"/restore", "", legacy.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("legacy public restore=%d %s", response.Code, response.Body)
	}
	var legacyPublished sql.NullString
	if err := legacy.db.QueryRow(`SELECT publication_state,federation_mode,published_at FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&state, &mode, &legacyPublished); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationWithdrawn) || mode != string(RecipeFederationOff) || legacyPublished.Valid {
		t.Fatalf("legacy public recipe auto-published after restore=%s/%s/%+v", state, mode, legacyPublished)
	}
}

func TestRecipeTrashAuditAndOutboxFailuresRollbackLifecycle(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	if _, err := env.db.Exec(`CREATE TRIGGER reject_trash_audit BEFORE INSERT ON audit_events WHEN NEW.action='recipe.trash' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	response := callContentAPI(env.handler, http.MethodDelete, "/api/recipes/"+testFederationID, "", env.owner)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("trash audit failure=%d %s", response.Code, response.Body)
	}
	assertActivePublishedFixture(t, env.db)
	if _, err := env.db.Exec(`DROP TRIGGER reject_trash_audit; CREATE TRIGGER reject_trash_outbox BEFORE INSERT ON federation_outbox BEGIN SELECT RAISE(ABORT,'outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
	response = callContentAPI(env.handler, http.MethodDelete, "/api/recipes/"+testFederationID, "", env.owner)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "federation_queue_unavailable") {
		t.Fatalf("trash outbox failure=%d %s", response.Code, response.Body)
	}
	assertActivePublishedFixture(t, env.db)
}

func assertActivePublishedFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	var deleted int
	var state, mode string
	var sequence, published int
	if err := db.QueryRow(`SELECT deleted_at IS NOT NULL FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT publication_state,federation_mode FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&state, &mode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sequence,published FROM federation_local_objects WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&sequence, &published); err != nil {
		t.Fatal(err)
	}
	var outbox, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || state != string(RecipePublicationPublished) || mode != string(RecipeFederationFollowers) || sequence != 1 || published != 1 || outbox != 1 || audits != 0 {
		t.Fatalf("failed trash partially committed: deleted=%d policy=%s/%s sequence=%d published=%d outbox=%d audits=%d", deleted, state, mode, sequence, published, outbox, audits)
	}
}

func TestRecipeOwnershipTransferWithdrawsPublicationAtomically(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	response := callContentAPI(env.handler, http.MethodPost, "/api/recipes/"+testFederationID+"/transfer", `{"email":"recipient@example.test"}`, env.owner)
	if response.Code != http.StatusOK {
		t.Fatalf("transfer=%d %s", response.Code, response.Body)
	}
	var owner, state, mode, actor string
	var version, sequence, published, revisionCount int
	if err := env.db.QueryRow(`SELECT owner_id,version FROM recipes WHERE id='0123456789abcdef0123456789abcdef'`).Scan(&owner, &version); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT publication_state,federation_mode,updated_by FROM recipe_publication_policy WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&state, &mode, &actor); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT sequence,published FROM federation_local_objects WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&sequence, &published); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='0123456789abcdef0123456789abcdef'`).Scan(&revisionCount); err != nil {
		t.Fatal(err)
	}
	if owner != "recipient" || state != string(RecipePublicationWithdrawn) || mode != string(RecipeFederationOff) || actor != "owner" || version != 1 || sequence != 2 || published != 0 || revisionCount != 1 {
		t.Fatalf("transfer outcome owner=%s policy=%s/%s actor=%s version=%d seq=%d published=%d revisions=%d", owner, state, mode, actor, version, sequence, published, revisionCount)
	}
	var auditCount int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='recipe.ownership.transfer'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("transfer audit count=%d err=%v", auditCount, err)
	}
}

func TestLocalCreationFieldsNeverEnterFederationWireSchema(t *testing.T) {
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, timeNowForPublicationTest(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Owner", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"top-level initial publication": func(body map[string]any) {
			body["initial_publication"] = map[string]any{"publication_state": "published"}
		},
		"top-level visibility confirmation": func(body map[string]any) { body["confirm_public_access"] = true },
		"recipe initial publication": func(body map[string]any) {
			body["recipe"].(map[string]any)["initial_publication"] = map[string]any{}
		},
		"recipe visibility confirmation": func(body map[string]any) { body["recipe"].(map[string]any)["confirm_public_access"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			copy := make(map[string]any, len(event))
			for key, value := range event {
				copy[key] = value
			}
			encoded, err := json.Marshal(copy)
			if err != nil {
				t.Fatal(err)
			}
			var fresh map[string]any
			if err := json.Unmarshal(encoded, &fresh); err != nil {
				t.Fatal(err)
			}
			mutate(fresh)
			encoded, err = json.Marshal(fresh)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := decodeFederationEvent(encoded); err == nil {
				t.Fatal("accepted local-only creation field on federation wire")
			}
		})
	}
}

func timeNowForPublicationTest() time.Time { return time.Now().UTC() }

func TestFederationRetentionAssertionIsCurrentOwnerExactVersionAndAtomic(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	if err := registerFederationPeer(env.db, "https://legacy-v1.example.org", true); err != nil {
		t.Fatal(err)
	}
	path := "/api/recipes/" + testFederationID + "/federation-retention"
	noV2Peer := callContentAPI(env.handler, http.MethodPost, path, `{"expected_recipe_version":1}`, env.owner)
	if noV2Peer.Code != http.StatusConflict {
		t.Fatalf("assertion without an opted-in peer status=%d body=%s", noV2Peer.Code, noV2Peer.Body)
	}
	var sequenceBeforeOptIn int64
	if err := env.db.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, testFederationID).Scan(&sequenceBeforeOptIn); err != nil || sequenceBeforeOptIn != 1 {
		t.Fatalf("no-peer assertion advanced sequence=%d err=%v", sequenceBeforeOptIn, err)
	}
	if _, err := env.db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin='https://peer.example.org'`, federationExchangeV2Retention); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		cookie     *http.Cookie
		want       int
	}{
		{name: "non-owner", body: `{"expected_recipe_version":1}`, cookie: env.other, want: http.StatusConflict},
		{name: "stale version", body: `{"expected_recipe_version":2}`, cookie: env.owner, want: http.StatusConflict},
		{name: "unexpected caller field", body: `{"expected_recipe_version":1,"owner_id":"owner"}`, cookie: env.owner, want: http.StatusBadRequest},
	} {
		rec := callContentAPI(env.handler, http.MethodPost, path, tc.body, tc.cookie)
		if rec.Code != tc.want {
			t.Fatalf("%s status=%d body=%s want=%d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
	var before int64
	if err := env.db.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, testFederationID).Scan(&before); err != nil || before != 1 {
		t.Fatalf("denied sequence=%d err=%v", before, err)
	}
	response := callContentAPI(env.handler, http.MethodPost, path, `{"expected_recipe_version":1}`, env.owner)
	if response.Code != http.StatusAccepted {
		t.Fatalf("owner assertion status=%d body=%s", response.Code, response.Body)
	}
	var sequence int64
	if err := env.db.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, testFederationID).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatalf("accepted sequence=%d err=%v", sequence, err)
	}
	var payload []byte
	if err := env.db.QueryRow(`SELECT payload FROM federation_outbox WHERE peer_origin='https://peer.example.org' ORDER BY sequence DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	event, err := exchangev2.Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.RetentionGrant == nil || event.RetentionGrant.ObjectID != event.ObjectID || event.RetentionGrant.Sequence != event.Sequence || event.RetentionGrant.PayloadSHA256 != exchangev2.PayloadDigest(event.Payload) || event.Sequence != 2 {
		t.Fatalf("grant is not exactly bound: %+v", event.RetentionGrant)
	}
	var legacyRows int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin='https://legacy-v1.example.org'`).Scan(&legacyRows); err != nil || legacyRows != 0 {
		t.Fatalf("owner assertion reached default-v1 peer rows=%d err=%v", legacyRows, err)
	}
	var auditCount int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='recipe.federation.retention.assert' AND response_status=?`, http.StatusAccepted).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("transactional audit count=%d err=%v", auditCount, err)
	}
	var outboxBefore int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outboxBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`CREATE TRIGGER reject_retention_assertion BEFORE INSERT ON federation_outbox WHEN json_extract(CAST(NEW.payload AS TEXT),'$.retention_grant') IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected assertion failure'); END`); err != nil {
		t.Fatal(err)
	}
	rejected := callContentAPI(env.handler, http.MethodPost, path, `{"expected_recipe_version":1}`, env.owner)
	if rejected.Code != http.StatusInternalServerError {
		t.Fatalf("outbox failure status=%d body=%s", rejected.Code, rejected.Body)
	}
	var afterSequence int64
	var outboxAfter, auditAfter int
	if err := env.db.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, testFederationID).Scan(&afterSequence); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&outboxAfter); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='recipe.federation.retention.assert' AND response_status=?`, http.StatusAccepted).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if afterSequence != 2 || outboxAfter != outboxBefore || auditAfter != 1 {
		t.Fatalf("failed assertion committed state sequence=%d outbox=%d/%d audit=%d", afterSequence, outboxAfter, outboxBefore, auditAfter)
	}
}
