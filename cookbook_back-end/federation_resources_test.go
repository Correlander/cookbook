package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setTestFederationResources(t *testing.T, db *sql.DB, global federationResourceLimits, origin string, peer federationResourceLimits) {
	t.Helper()
	if err := setFederationResourceLimits(t.Context(), db, global, defaultFederationResourceLimits(), []federationResourceOrigin{{Origin: origin, Limits: peer}}); err != nil {
		t.Fatal(err)
	}
}

func resourceEvent(t *testing.T, eventID, eventType string, sequence int64, objectID string, title string) []byte {
	t.Helper()
	source := federationRecipeSource{ID: objectID, CreatorName: "Creator", Title: title, Visibility: visibilityPublic}
	body, err := serializeFederationEvent(testFederationOrigin, eventID, eventType, sequence, time.Now(), true, true, source)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestFederationResourceAdmissionDuplicateAtCapAndSafeIncrease(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	first := resourceEvent(t, "11111111111111111111111111111111", federationEventSnapshot, 1, testFederationID, "First")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, first); err != nil {
		t.Fatal(err)
	}
	global := defaultFederationGlobalResourceLimits()
	peer := defaultFederationResourceLimits()
	peer.ObjectRows, peer.ObjectBytes, peer.EventRows, peer.EventBytes = 10, 1<<20, 1, 1<<20
	setTestFederationResources(t, db, global, testFederationOrigin, peer)
	status, original, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, first)
	if err != nil || status != 202 || string(original) == "" {
		t.Fatalf("duplicate at cap status=%d err=%v", status, err)
	}
	update := resourceEvent(t, "22222222222222222222222222222222", federationEventUpdate, 2, testFederationID, "Second")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, update); !errors.Is(err, errFederationResourceCapacity) {
		t.Fatalf("new receipt at cap error=%v", err)
	}
	var sequence int64
	var title string
	var receipts int
	if err := db.QueryRow(`SELECT sequence,json_extract(recipe_json,'$.title') FROM federation_remote_recipes WHERE object_id=?`, testFederationOrigin+"/federation/recipes/"+testFederationID).Scan(&sequence, &title); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if sequence != 1 || title != "First" || receipts != 1 {
		t.Fatalf("capacity rejection mutated state: sequence=%d title=%q receipts=%d", sequence, title, receipts)
	}
	peer.EventRows = 2
	setTestFederationResources(t, db, global, testFederationOrigin, peer)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, update); err != nil {
		t.Fatalf("unchanged retry after increase: %v", err)
	}
	usage, err := readFederationResourceUsage(t.Context(), db, testFederationOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	if usage.EventRows != 2 || usage.ObjectRows != 1 {
		t.Fatalf("unexpected retained usage: %+v", usage)
	}
	peer.EventRows, peer.EventBytes = 10, usage.EventBytes
	setTestFederationResources(t, db, global, testFederationOrigin, peer)
	third := resourceEvent(t, "33333333333333333333333333333333", federationEventUpdate, 3, testFederationID, "Third")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, third); !errors.Is(err, errFederationResourceCapacity) {
		t.Fatalf("new receipt at exact byte cap error=%v", err)
	}
	if err := db.QueryRow(`SELECT high_water_sequence FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, testFederationOrigin, testFederationOrigin+"/federation/recipes/"+testFederationID).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatalf("receipt byte rejection changed high-water sequence=%d err=%v", sequence, err)
	}
	peer.EventBytes += 1 << 20
	setTestFederationResources(t, db, global, testFederationOrigin, peer)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, third); err != nil {
		t.Fatalf("unchanged retry after receipt byte increase: %v", err)
	}
}

func TestFederationResourceCatalogSurvivesPeerRemovalAndRejectsStaleReenrollment(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	first := resourceEvent(t, "11111111111111111111111111111111", federationEventSnapshot, 1, testFederationID, "First")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM federation_remote_recipes WHERE object_id=?`, testFederationOrigin+"/federation/recipes/"+testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM federation_peers WHERE origin=?`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	if status, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, first); err != nil || status != 202 {
		t.Fatalf("exact prior receipt after peer removal status=%d err=%v", status, err)
	}
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	stale := resourceEvent(t, "33333333333333333333333333333333", federationEventSnapshot, 1, testFederationID, "Resurrected")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, stale); !errors.Is(err, errFederationConflict) {
		t.Fatalf("stale event after re-enrollment error=%v", err)
	}
	usage, err := readFederationResourceUsage(t.Context(), db, testFederationOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ObjectRows != 1 || usage.EventRows != 1 {
		t.Fatalf("peer removal reclaimed durable history: %+v", usage)
	}
}

func TestFederationReceiptOnlyHighWaterRequiresSnapshotToRehydrate(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	object := testFederationOrigin + "/federation/recipes/" + testFederationID
	bytes := remoteCatalogLogicalBytes(testFederationOrigin, object, testFederationID, "", "", "", "", "[]", "unknown_legacy", 7, 0)
	if _, err := db.Exec(`INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,logical_bytes) VALUES(?,?,?,?,0,0,?)`, testFederationOrigin, object, testFederationID, 7, bytes); err != nil {
		t.Fatal(err)
	}
	update := resourceEvent(t, "11111111111111111111111111111111", federationEventUpdate, 8, testFederationID, "Update")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, update); !errors.Is(err, errFederationConflict) {
		t.Fatalf("update against receipt-only history error=%v", err)
	}
	snapshot := resourceEvent(t, "22222222222222222222222222222222", federationEventSnapshot, 8, testFederationID, "Resync")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, snapshot); err != nil {
		t.Fatalf("newer full snapshot failed to rehydrate catalog: %v", err)
	}
	var sequence int64
	var stateKnown int
	if err := db.QueryRow(`SELECT high_water_sequence,state_known FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, testFederationOrigin, object).Scan(&sequence, &stateKnown); err != nil || sequence != 8 || stateKnown != 1 {
		t.Fatalf("snapshot did not rehydrate durable state: sequence=%d known=%d err=%v", sequence, stateKnown, err)
	}
}

func TestFederationResourceSerializedConcurrentAdmissionRespectsObjectCap(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	global := defaultFederationGlobalResourceLimits()
	global.ObjectRows, global.ObjectBytes = 1, 1<<20
	peer := defaultFederationResourceLimits()
	peer.ObjectRows, peer.ObjectBytes = 1, 1<<20
	setTestFederationResources(t, db, global, testFederationOrigin, peer)
	bodies := [][]byte{resourceEvent(t, fmt.Sprintf("%032d", 1), federationEventSnapshot, 1, testFederationID, "Concurrent"), resourceEvent(t, fmt.Sprintf("%032d", 2), federationEventSnapshot, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Concurrent")}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, body := range bodies {
		wg.Add(1)
		go func(body []byte) {
			defer wg.Done()
			_, _, err := acceptFederationEvent(context.Background(), db, testFederationOrigin, body)
			results <- err
		}(body)
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, errFederationResourceCapacity):
			rejected++
		default:
			t.Fatalf("concurrent admission error=%v", err)
		}
	}
	usage, err := readFederationResourceUsage(t.Context(), db, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || rejected != 1 || usage.ObjectRows != 1 {
		t.Fatalf("accepted=%d rejected=%d usage=%+v", accepted, rejected, usage)
	}
}

func TestFederationResourceUTF8AccountingAndReplacementDeltas(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	first := resourceEvent(t, "11111111111111111111111111111111", federationEventSnapshot, 1, testFederationID, "Soupe 🥣")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, first); err != nil {
		t.Fatal(err)
	}
	var recipeID, creator, raw, title, description, tags string
	var sequence int64
	var deleted int
	object := testFederationOrigin + "/federation/recipes/" + testFederationID
	if err := db.QueryRow(`SELECT source_recipe_id,sequence,creator_name,recipe_json,deleted FROM federation_remote_recipes WHERE object_id=?`, object).Scan(&recipeID, &sequence, &creator, &raw, &deleted); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT title,description,tags_json FROM federation_remote_recipe_search WHERE object_id=?`, object).Scan(&title, &description, &tags); err != nil {
		t.Fatal(err)
	}
	wantObjectBytes := remoteCatalogLogicalBytes(testFederationOrigin, object, recipeID, creator, raw, title, description, tags, "published", sequence, deleted)
	var objectBytes, receiptBytes int64
	if err := db.QueryRow(`SELECT logical_bytes FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, testFederationOrigin, object).Scan(&objectBytes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT logical_bytes FROM federation_received_events WHERE source_origin=? AND event_id=?`, testFederationOrigin, "11111111111111111111111111111111").Scan(&receiptBytes); err != nil {
		t.Fatal(err)
	}
	if objectBytes != wantObjectBytes || objectBytes <= int64(len(raw)) {
		t.Fatalf("UTF-8 catalog accounting bytes=%d want=%d raw=%d", objectBytes, wantObjectBytes, len(raw))
	}
	var digest []byte
	var response, received string
	var status int
	if err := db.QueryRow(`SELECT body_digest,response_body,received_at,response_status FROM federation_received_events WHERE source_origin=? AND event_id=?`, testFederationOrigin, "11111111111111111111111111111111").Scan(&digest, &response, &received, &status); err != nil {
		t.Fatal(err)
	}
	if want := receivedEventLogicalBytes(testFederationOrigin, "11111111111111111111111111111111", digest, object, 1, received, status, response); receiptBytes != want {
		t.Fatalf("receipt logical bytes=%d want=%d", receiptBytes, want)
	}
	global := defaultFederationGlobalResourceLimits()
	peer := defaultFederationResourceLimits()
	peer.ObjectBytes = objectBytes
	setTestFederationResources(t, db, global, testFederationOrigin, peer)
	shrink := resourceEvent(t, "22222222222222222222222222222222", federationEventUpdate, 2, testFederationID, "A")
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, shrink); err != nil {
		t.Fatalf("shrinking replacement at exact cap: %v", err)
	}
	usage, err := readFederationResourceUsage(t.Context(), db, testFederationOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ObjectRows != 1 || usage.ObjectBytes >= objectBytes {
		t.Fatalf("replacement did not apply byte delta: initial=%d usage=%+v", objectBytes, usage)
	}
	grow := resourceEvent(t, "33333333333333333333333333333333", federationEventUpdate, 3, testFederationID, strings.Repeat("x", 100))
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, grow); !errors.Is(err, errFederationResourceCapacity) {
		t.Fatalf("oversized replacement error=%v", err)
	}
	var gotTitle string
	var gotSequence int64
	if err := db.QueryRow(`SELECT sequence,json_extract(recipe_json,'$.title') FROM federation_remote_recipes WHERE object_id=?`, object).Scan(&gotSequence, &gotTitle); err != nil {
		t.Fatal(err)
	}
	if gotSequence != 2 || gotTitle != "A" {
		t.Fatalf("rejected replacement mutated cache: sequence=%d title=%q", gotSequence, gotTitle)
	}
}

func TestFederationResourceLimitsPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation-limits.db")
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	global := defaultFederationGlobalResourceLimits()
	defaults := defaultFederationResourceLimits()
	defaults.ObjectRows, defaults.ObjectBytes, defaults.EventRows, defaults.EventBytes = 500, 8<<20, 5000, 32<<20
	origin := defaultFederationResourceLimits()
	origin.ObjectRows, origin.ObjectBytes, origin.EventRows, origin.EventBytes = 25, 1<<20, 250, 8<<20
	if err := setFederationResourceLimits(t.Context(), db, global, defaults, []federationResourceOrigin{{Origin: testFederationOrigin, Limits: origin}}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gotDefaults, err := readFederationDefaultPeerLimits(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	gotOrigin, err := readFederationResourceLimits(t.Context(), db, testFederationOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if gotDefaults != defaults || gotOrigin != origin {
		t.Fatalf("resource limits lost on restart: defaults=%+v origin=%+v", gotDefaults, gotOrigin)
	}
}

func TestFederationPeerReenrollmentUsesPersistedSenderSequence(t *testing.T) {
	db := testDatabase(t)
	peerOrigin := "https://peer.example.org"
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('owner','owner@example.test','Owner',0,'now'),('admin','admin@example.test','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?,'owner','Owner','Dish','[]','[]','[]','public','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES(?,'published','followers',0,'now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, peerOrigin, true); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, testFederationID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var firstSequence int64
	if err := db.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, testFederationID).Scan(&firstSequence); err != nil || firstSequence != 1 {
		t.Fatalf("initial sender sequence=%d err=%v", firstSequence, err)
	}
	if _, err := db.Exec(`DELETE FROM federation_peers WHERE origin=?`, peerOrigin); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, peerOrigin, false); err != nil {
		t.Fatal(err)
	}
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, peerOrigin, "re-enrolled-key", key); err != nil {
		t.Fatal(err)
	}
	api := apiHandler{db: db}
	request := requestForUser(http.MethodPost, "/api/admin/federation/peers", "admin")
	request.Body = io.NopCloser(strings.NewReader(`{"origin":"` + peerOrigin + `","enabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.federationPeers(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("peer re-enable status=%d body=%s", response.Code, response.Body.String())
	}
	var sequence int64
	var payload []byte
	if err := db.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, testFederationID).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT payload FROM federation_outbox WHERE peer_origin=? ORDER BY sequence DESC LIMIT 1`, peerOrigin).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	event, _, err := decodeFederationEvent(payload)
	if err != nil || sequence != 2 || event.Sequence != 2 || event.EventType != federationEventSnapshot {
		t.Fatalf("re-enrollment reset/omitted sender sequence: local=%d event=%+v err=%v", sequence, event, err)
	}
	var eventJSON map[string]any
	if err := json.Unmarshal(payload, &eventJSON); err != nil || eventJSON["event_id"] == nil {
		t.Fatalf("re-enrolled event is not an immutable v1 payload: %s err=%v", payload, err)
	}
}
