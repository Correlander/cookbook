package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testFederationOrigin = "https://cookbook.example.org"
const testFederationID = "0123456789abcdef0123456789abcdef"

func TestFederationSnapshotSerializesOnlyPublicAllowlistedFields(t *testing.T) {
	source := federationRecipeSource{
		ID: testFederationID, OwnerID: "private-owner-id", CreatorName: "  Ada Cook  ",
		SourceRecipeID: "private-source-id", Title: " Tomato soup ", Description: " Family recipe ",
		Ingredients: []ingredient{{Name: "tomatoes", Quantity: "4", Notes: "ripe"}},
		Steps:       []recipeStep{{Text: "Simmer."}}, Servings: 4, PrepTime: 10,
		CookTime: 25, Tags: []string{" Soup ", "soup"}, Visibility: visibilityPublic,
		ImagePath: "private-image-path", CookbookIDs: []string{"private-cookbook-id"},
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), true, true, source)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	allowedEventFields := map[string]bool{"protocol": true, "schema_version": true, "event_id": true, "event_type": true, "source_origin": true, "object_id": true, "sequence": true, "occurred_at": true, "attribution": true, "recipe": true}
	for key := range event {
		if !allowedEventFields[key] {
			t.Errorf("unexpected event field %q", key)
		}
	}
	if event["object_id"] != testFederationOrigin+"/federation/recipes/"+testFederationID {
		t.Fatalf("object ID=%v", event["object_id"])
	}
	attribution := event["attribution"].(map[string]any)
	if len(attribution) != 1 || attribution["creator_name"] != "Ada Cook" {
		t.Fatalf("unexpected attribution: %#v", attribution)
	}
	recipe := event["recipe"].(map[string]any)
	allowedRecipeFields := map[string]bool{"title": true, "description": true, "ingredients": true, "steps": true, "servings": true, "prep_time_minutes": true, "cook_time_minutes": true, "tags": true}
	for key := range recipe {
		if !allowedRecipeFields[key] {
			t.Errorf("unexpected recipe field %q", key)
		}
	}
	for _, privateValue := range []string{"private-owner-id", "private-source-id", "private-image-path", "private-cookbook-id"} {
		if strings.Contains(string(payload), privateValue) {
			t.Errorf("payload leaked %q", privateValue)
		}
	}
	if recipe["title"] != "Tomato soup" || recipe["description"] != "Family recipe" || len(recipe["tags"].([]any)) != 1 {
		t.Fatalf("recipe normalization failed: %#v", recipe)
	}
}

func TestFederationSnapshotRejectsNonPublicOrDisabledContent(t *testing.T) {
	base := federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic}
	tests := []struct {
		name              string
		source            federationRecipeSource
		federationEnabled bool
		instancePublic    bool
	}{
		{name: "private", source: withVisibility(base, visibilityPrivate), federationEnabled: true, instancePublic: true},
		{name: "unlisted", source: withVisibility(base, visibilityUnlisted), federationEnabled: true, instancePublic: true},
		{name: "deleted", source: func() federationRecipeSource { recipe := base; recipe.Deleted = true; return recipe }(), federationEnabled: true, instancePublic: true},
		{name: "private instance", source: base, federationEnabled: true, instancePublic: false},
		{name: "disabled", source: base, federationEnabled: false, instancePublic: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), test.federationEnabled, test.instancePublic, test.source)
			if err == nil || payload != nil {
				t.Fatalf("nonpublic content serialized: payload=%s err=%v", payload, err)
			}
		})
	}
}

func TestFederationTombstoneContainsNoRecipeOrAttribution(t *testing.T) {
	source := federationRecipeSource{
		ID: testFederationID, OwnerID: "private-owner-id", CreatorName: "Private creator",
		Title: "Private title", Visibility: visibilityPrivate, Deleted: true,
		ImagePath: "private-image-path", CookbookIDs: []string{"private-cookbook-id"},
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventTombstone, 2, time.Now(), true, false, source)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event["event_type"] != federationEventTombstone || event["sequence"] != float64(2) {
		t.Fatalf("unexpected tombstone: %#v", event)
	}
	if _, exists := event["recipe"]; exists {
		t.Fatal("tombstone included recipe data")
	}
	if _, exists := event["attribution"]; exists {
		t.Fatal("tombstone included attribution")
	}
	for _, privateValue := range []string{"private-owner-id", "Private creator", "Private title", "private-image-path", "private-cookbook-id"} {
		if strings.Contains(string(payload), privateValue) {
			t.Errorf("tombstone leaked %q", privateValue)
		}
	}
}

func withVisibility(source federationRecipeSource, visibility visibility) federationRecipeSource {
	source.Visibility = visibility
	return source
}

func TestFederationReceiverDeduplicatesAndAppliesSequenceAtomically(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	base := federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic}
	makeEvent := func(eventID, eventType string, sequence int64, source federationRecipeSource) []byte {
		t.Helper()
		payload, err := serializeFederationEvent(testFederationOrigin, eventID, eventType, sequence, time.Now(), true, true, source)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	first := makeEvent("11111111111111111111111111111111", federationEventSnapshot, 1, base)
	status, response, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, first)
	if err != nil || status != 202 || !strings.Contains(string(response), "accepted") {
		t.Fatalf("snapshot status=%d response=%s err=%v", status, response, err)
	}
	status, _, err = acceptFederationEvent(t.Context(), db, testFederationOrigin, first)
	if err != nil || status != 202 {
		t.Fatalf("duplicate event status=%d err=%v", status, err)
	}
	conflictingID := strings.Replace(string(first), "\"title\":\"Dish\"", "\"title\":\"Other dish\"", 1)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, []byte(conflictingID)); !errors.Is(err, errFederationConflict) {
		t.Fatalf("conflicting event ID error=%v", err)
	}
	update := base
	update.Title = "Updated dish"
	second := makeEvent("22222222222222222222222222222222", federationEventUpdate, 2, update)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, second); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	stale := makeEvent("33333333333333333333333333333333", federationEventUpdate, 2, base)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, stale); !errors.Is(err, errFederationConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	var sequence, deleted int
	var recipeJSON string
	if err := db.QueryRow(`SELECT sequence,deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, testFederationOrigin+"/federation/recipes/"+testFederationID).Scan(&sequence, &deleted, &recipeJSON); err != nil {
		t.Fatal(err)
	}
	if sequence != 2 || deleted != 0 || !strings.Contains(recipeJSON, "Updated dish") {
		t.Fatalf("remote recipe was not updated atomically: seq=%d deleted=%d payload=%s", sequence, deleted, recipeJSON)
	}
	tombstone, err := serializeFederationEvent(testFederationOrigin, "44444444444444444444444444444444", federationEventTombstone, 3, time.Now(), true, true, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, tombstone); err != nil {
		t.Fatalf("tombstone failed: %v", err)
	}
	if err := db.QueryRow(`SELECT sequence,deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, testFederationOrigin+"/federation/recipes/"+testFederationID).Scan(&sequence, &deleted, &recipeJSON); err != nil {
		t.Fatal(err)
	}
	if sequence != 3 || deleted != 1 || recipeJSON != "" {
		t.Fatalf("tombstone retained content: seq=%d deleted=%d payload=%q", sequence, deleted, recipeJSON)
	}
}

func TestDecodeFederationEventRejectsDuplicateUnknownAndMismatchedData(t *testing.T) {
	valid, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"duplicate key":       []byte(strings.Replace(string(valid), `"event_id":`, `"event_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event_id":`, 1)),
		"unknown field":       []byte(strings.TrimSuffix(string(valid), "}") + `,"secret":"value"}`),
		"incorrect casing":    []byte(strings.Replace(string(valid), `"protocol":`, `"Protocol":`, 1)),
		"wrong object origin": []byte(strings.Replace(string(valid), testFederationOrigin+"/federation/recipes/", "https://other.example.org/federation/recipes/", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeFederationEvent(body); err == nil {
				t.Fatal("accepted invalid event")
			}
		})
	}
}

func TestDecodeFederationEventRejectsNestedDuplicateAndUnknownKeys(t *testing.T) {
	valid, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{
		ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic,
		Ingredients: []ingredient{{Name: "tomatoes", Quantity: "4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(valid)
	tests := map[string]string{
		"duplicate recipe title":    strings.Replace(body, `"title":"Dish"`, `"title":"Dish","title":"Dish"`, 1),
		"duplicate ingredient name": strings.Replace(body, `"name":"tomatoes"`, `"name":"tomatoes","name":"tomatoes"`, 1),
		"unknown recipe key":        strings.Replace(body, `"title":"Dish"`, `"title":"Dish","secret":"value"`, 1),
		"unknown ingredient key":    strings.Replace(body, `"name":"tomatoes"`, `"name":"tomatoes","secret":"value"`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeFederationEvent([]byte(body)); err == nil {
				t.Fatal("accepted nested invalid event field")
			}
		})
	}
}

func TestDecodeFederationEventRejectsInvalidUTF8TrailingAndOversizedBodies(t *testing.T) {
	valid, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"invalid UTF-8":       append(append([]byte(nil), valid...), 0xff),
		"trailing JSON value": append(append([]byte(nil), valid...), []byte(` {}`)...),
		"oversized body":      bytes.Repeat([]byte(" "), maxFederationEventBytes+1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeFederationEvent(body); err == nil {
				t.Fatal("accepted invalid event body")
			}
		})
	}
}

func TestFederationOutboxSharesRecipeTransactions(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?, 'owner','Owner','Dish','[]','[]','[]','private','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	assertQueue := func(want int) {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&count); err != nil || count != want {
			t.Fatalf("outbox count=%d err=%v want=%d", count, err, want)
		}
	}
	assertQueue(0)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE recipes SET visibility='public' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, testFederationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertQueue(1)
	var eventType string
	if err := db.QueryRow(`SELECT json_extract(CAST(payload AS TEXT),'$.event_type') FROM federation_outbox`).Scan(&eventType); err != nil || eventType != federationEventSnapshot {
		t.Fatalf("queued event=%q err=%v", eventType, err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE recipes SET visibility='private' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, testFederationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertQueue(2)
	if err := db.QueryRow(`SELECT json_extract(CAST(payload AS TEXT),'$.event_type') FROM federation_outbox ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&eventType); err != nil || eventType != federationEventTombstone {
		t.Fatalf("queued event=%q err=%v", eventType, err)
	}
}

func TestFederationSettingQueuesSnapshotAndDisableTombstone(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?, 'owner','Owner','Dish','[]','[]','[]','public','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	peerPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, "https://peer.example.org", "peer-key", peerPublicKey); err != nil {
		t.Fatal(err)
	}
	enabled := true
	settings, err := updateInstanceSettings(db, nil, nil, nil, nil, nil, false, &enabled)
	if err != nil || !settings.FederationEnabled {
		t.Fatalf("enable federation settings=%+v err=%v", settings, err)
	}
	var eventType string
	if err := db.QueryRow(`SELECT json_extract(CAST(payload AS TEXT),'$.event_type') FROM federation_outbox`).Scan(&eventType); err != nil || eventType != federationEventSnapshot {
		t.Fatalf("enable snapshot event=%q err=%v", eventType, err)
	}
	disabled := false
	settings, err = updateInstanceSettings(db, nil, nil, nil, nil, nil, false, &disabled)
	if err != nil || settings.FederationEnabled {
		t.Fatalf("disable federation settings=%+v err=%v", settings, err)
	}
	if err := db.QueryRow(`SELECT json_extract(CAST(payload AS TEXT),'$.event_type') FROM federation_outbox ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&eventType); err != nil || eventType != federationEventTombstone {
		t.Fatalf("disable tombstone event=%q err=%v", eventType, err)
	}
}

func TestFederationOutboxAdministrationCanInspectRetryAndDiscard(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.org','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	var event federationEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	queueID := newID()
	if _, err := db.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,attempts,next_attempt,created_at) VALUES(?,?,?,?,?,?,12,?,?)`, queueID, "https://peer.example.org", event.EventID, event.ObjectID, event.Sequence, payload, time.Now().Add(time.Hour).Unix(), timestamp()); err != nil {
		t.Fatal(err)
	}
	api := apiHandler{db: db}
	request := requestForUser(http.MethodGet, "/api/admin/federation/outbox?id="+queueID, "admin")
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Dish") {
		t.Fatalf("outbox detail status=%d body=%s", recorder.Code, recorder.Body)
	}
	request = requestForUser(http.MethodPost, "/api/admin/federation/outbox", "admin")
	request.Body = io.NopCloser(strings.NewReader(`{"id":"` + queueID + `"}`))
	recorder = httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	var attempts int
	if recorder.Code != http.StatusOK || db.QueryRow(`SELECT attempts FROM federation_outbox WHERE id=?`, queueID).Scan(&attempts) != nil || attempts != 0 {
		t.Fatalf("outbox retry status=%d body=%s attempts=%d", recorder.Code, recorder.Body, attempts)
	}
	request = requestForUser(http.MethodDelete, "/api/admin/federation/outbox", "admin")
	request.Body = io.NopCloser(strings.NewReader(`{"id":"` + queueID + `"}`))
	recorder = httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	var remaining int
	if recorder.Code != http.StatusOK || db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE id=?`, queueID).Scan(&remaining) != nil || remaining != 0 {
		t.Fatalf("outbox discard status=%d body=%s remaining=%d", recorder.Code, recorder.Body, remaining)
	}
}

func TestFederationPeerAdministrationAndKeyRotation(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.org','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	api := apiHandler{db: db}
	postJSON := func(path, payload string) *httptest.ResponseRecorder {
		req := requestForUser(http.MethodPost, path, "admin")
		req.Body = io.NopCloser(strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec
	}
	if response := postJSON("/api/admin/federation/peers", `{"origin":"https://peer.example.org","enabled":false}`); response.Code != http.StatusOK {
		t.Fatalf("peer create status=%d body=%s", response.Code, response.Body)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyInput, _ := json.Marshal(map[string]string{"origin": "https://peer.example.org", "key_id": "peer-key", "public_key": base64.StdEncoding.EncodeToString(publicKey)})
	if response := postJSON("/api/admin/federation/keys", string(keyInput)); response.Code != http.StatusOK {
		t.Fatalf("key add status=%d body=%s", response.Code, response.Body)
	}
	if response := postJSON("/api/admin/federation/peers", `{"origin":"https://peer.example.org","enabled":true}`); response.Code != http.StatusOK {
		t.Fatalf("peer enable status=%d body=%s", response.Code, response.Body)
	}
	get := requestForUser(http.MethodGet, "/api/admin/federation/peers", "admin")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, get)
	var peers []federationPeerAdminItem
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &peers) != nil || len(peers) != 1 || len(peers[0].Keys) != 1 || peers[0].Keys[0].Status != "active" {
		t.Fatalf("peer list status=%d body=%s peers=%+v", response.Code, response.Body, peers)
	}
	if response := postJSON("/api/admin/federation/keys", `{"origin":"https://peer.example.org","key_id":"peer-key","public_key":"bad"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid key status=%d body=%s", response.Code, response.Body)
	}
	revoke := requestForUser(http.MethodDelete, "/api/admin/federation/keys", "admin")
	revoke.Body = io.NopCloser(bytes.NewBufferString(`{"origin":"https://peer.example.org","key_id":"peer-key"}`))
	revoke.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	api.ServeHTTP(response, revoke)
	if response.Code != http.StatusOK || federationPeerEnabled(db, "https://peer.example.org") {
		t.Fatalf("last peer key revocation status=%d body=%s", response.Code, response.Body)
	}
}

func TestDisabledPeerDropsQueueAndResynchronizesTombstonesAndSnapshots(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.org','Admin',1,'now'),('owner','owner@example.org','Owner',0,'now')`); err != nil {
		t.Fatal(err)
	}
	peerOrigin := "https://peer.example.org"
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	privateID := "11111111111111111111111111111111"
	publicID := "22222222222222222222222222222222"
	for _, id := range []string{privateID, publicID} {
		if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?,'owner','Owner','Dish','[]','[]','[]','public','owner_only','now','now')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := registerFederationPeer(db, peerOrigin, false); err != nil {
		t.Fatal(err)
	}
	peerKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, peerOrigin, "peer-key", peerKey); err != nil {
		t.Fatal(err)
	}
	api := apiHandler{db: db}
	call := func(method, body string) *httptest.ResponseRecorder {
		request := requestForUser(method, "/api/admin/federation/peers", "admin")
		request.Body = io.NopCloser(strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		return recorder
	}
	if response := call(http.MethodPost, `{"origin":"`+peerOrigin+`","enabled":true}`); response.Code != http.StatusOK {
		t.Fatalf("enable peer status=%d body=%s", response.Code, response.Body)
	}
	var queued int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, peerOrigin).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("initial snapshots=%d err=%v", queued, err)
	}
	if response := call(http.MethodDelete, `{"origin":"`+peerOrigin+`"}`); response.Code != http.StatusOK {
		t.Fatalf("disable peer status=%d body=%s", response.Code, response.Body)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, peerOrigin).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("disabled peer queue=%d err=%v", queued, err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE recipes SET visibility='private' WHERE id=?`, privateID); err != nil {
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, privateID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if response := call(http.MethodPost, `{"origin":"`+peerOrigin+`","enabled":true}`); response.Code != http.StatusOK {
		t.Fatalf("re-enable peer status=%d body=%s", response.Code, response.Body)
	}
	rows, err := db.Query(`SELECT json_extract(CAST(payload AS TEXT),'$.object_id'),json_extract(CAST(payload AS TEXT),'$.event_type') FROM federation_outbox WHERE peer_origin=?`, peerOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	events := map[string]string{}
	for rows.Next() {
		var objectID, eventType string
		if err := rows.Scan(&objectID, &eventType); err != nil {
			t.Fatal(err)
		}
		events[objectID] = eventType
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if events[testFederationOrigin+"/federation/recipes/"+privateID] != federationEventTombstone || events[testFederationOrigin+"/federation/recipes/"+publicID] != federationEventSnapshot {
		t.Fatalf("re-enabled peer was not resynchronized: %+v", events)
	}
}

func TestFederationSigningKeyPersistsAndRejectsBroadPermissions(t *testing.T) {
	dir := t.TempDir()
	key, err := loadOrCreateFederationSigningKey(dir)
	if err != nil || len(key.PrivateKey) != ed25519.PrivateKeySize || len(key.PublicKey) != ed25519.PublicKeySize || key.ID == "" {
		t.Fatalf("key=%+v err=%v", key, err)
	}
	reloaded, err := loadOrCreateFederationSigningKey(dir)
	if err != nil || reloaded.ID != key.ID || !bytes.Equal(reloaded.PrivateKey, key.PrivateKey) {
		t.Fatalf("reloaded key changed: first=%s second=%s err=%v", key.ID, reloaded.ID, err)
	}
	path := dir + string(os.PathSeparator) + federationPrivateKeyFile
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateFederationSigningKey(dir); err == nil {
		t.Fatal("accepted federation private key with broad permissions")
	}
}

func TestFederationIdentityKeyRotationStagesBeforeActivation(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.org','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	active, err := loadOrCreateFederationSigningKey(directory)
	if err != nil {
		t.Fatal(err)
	}
	api := apiHandler{db: db, federationKeyDir: directory}
	post := func(action string) *httptest.ResponseRecorder {
		request := requestForUser(http.MethodPost, "/api/admin/federation/identity/rotation", "admin")
		request.Body = io.NopCloser(strings.NewReader(`{"action":"` + action + `"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		return recorder
	}
	stage := post("stage")
	if stage.Code != http.StatusOK {
		t.Fatalf("stage status=%d body=%s", stage.Code, stage.Body)
	}
	var pending struct {
		KeyID string `json:"key_id"`
	}
	if err := json.Unmarshal(stage.Body.Bytes(), &pending); err != nil || pending.KeyID == active.ID {
		t.Fatalf("pending key=%+v err=%v", pending, err)
	}
	current, err := loadOrCreateFederationSigningKey(directory)
	if err != nil || current.ID != active.ID {
		t.Fatalf("staging changed active key: key=%s err=%v", current.ID, err)
	}
	duplicate := post("stage")
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("second stage status=%d body=%s", duplicate.Code, duplicate.Body)
	}
	activate := post("activate")
	if activate.Code != http.StatusOK {
		t.Fatalf("activate status=%d body=%s", activate.Code, activate.Body)
	}
	current, err = loadOrCreateFederationSigningKey(directory)
	if err != nil || current.ID != pending.KeyID {
		t.Fatalf("activation key=%s want=%s err=%v", current.ID, pending.KeyID, err)
	}
	if _, err := os.Stat(federationPendingKeyPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending key remains after activation: %v", err)
	}
}

func TestFederationIdentityRotationSerializesWithOwnerTransfer(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.org','Owner',0,'now'),('target','target@example.org','Target',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner"); err != nil {
		t.Fatal(err)
	}
	transfer, err := CreateInstanceOwnerTransfer(context.Background(), db, "owner", "target")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	active, err := loadOrCreateFederationSigningKey(directory)
	if err != nil {
		t.Fatal(err)
	}
	authorized := make(chan struct{})
	continueRotation := make(chan struct{})
	releaseRotation := func() {
		select {
		case <-continueRotation:
		default:
			close(continueRotation)
		}
	}
	t.Cleanup(releaseRotation)
	rotationDone := make(chan struct {
		key federationSigningKey
		err error
	}, 1)
	go func() {
		key, err := rotateFederationSigningKeyForActor(context.Background(), db, "owner", directory, "stage", func() {
			close(authorized)
			<-continueRotation
		})
		rotationDone <- struct {
			key federationSigningKey
			err error
		}{key: key, err: err}
	}()
	select {
	case <-authorized:
	case <-time.After(2 * time.Second):
		t.Fatal("key rotation did not reach its authorized transaction")
	}
	acceptStarted := make(chan struct{})
	acceptDone := make(chan error, 1)
	go func() {
		close(acceptStarted)
		_, err := AcceptInstanceOwnerTransfer(context.Background(), db, "target", transfer.ID)
		acceptDone <- err
	}()
	<-acceptStarted
	releaseRotation()
	var staged federationSigningKey
	select {
	case result := <-rotationDone:
		staged = result.key
		if result.err != nil || staged.ID == active.ID {
			t.Fatalf("rotation ordered before transfer failed: key=%s err=%v", staged.ID, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("key rotation did not finish after release")
	}
	select {
	case err := <-acceptDone:
		if err != nil {
			t.Fatalf("transfer after key rotation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner transfer did not finish after key rotation")
	}
	current, err := loadOrCreateFederationSigningKey(directory)
	if err != nil || current.ID != active.ID {
		t.Fatalf("staging changed active key: key=%s err=%v", current.ID, err)
	}
	if pending, err := readFederationSigningKey(federationPendingKeyPath(directory)); err != nil || pending.ID != staged.ID {
		t.Fatalf("authorized in-flight stage was not preserved: pending=%s err=%v", pending.ID, err)
	}

	api := apiHandler{db: db, federationKeyDir: directory}
	request := requestForUser(http.MethodPost, "/api/admin/federation/identity/rotation", "owner")
	request.Body = io.NopCloser(strings.NewReader(`{"action":"activate"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("former owner activation status=%d body=%s", response.Code, response.Body)
	}
	current, err = loadOrCreateFederationSigningKey(directory)
	if err != nil || current.ID != active.ID {
		t.Fatalf("denied former-owner request changed active key: key=%s err=%v", current.ID, err)
	}
	if pending, err := readFederationSigningKey(federationPendingKeyPath(directory)); err != nil || pending.ID != staged.ID {
		t.Fatalf("denied former-owner request changed pending key: pending=%s err=%v", pending.ID, err)
	}
}

func TestClaimedInstanceOwnerAloneControlsLocalFederationIdentity(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.org','Owner',0,'now'),('admin','admin@example.org','Admin',1,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner"); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	api := apiHandler{db: db, federationKeyDir: directory}
	adminGet := httptest.NewRecorder()
	api.ServeHTTP(adminGet, requestForUser(http.MethodGet, "/api/admin/federation/identity", "admin"))
	if adminGet.Code != http.StatusForbidden {
		t.Fatalf("ordinary admin read local identity status=%d body=%s", adminGet.Code, adminGet.Body)
	}
	adminRotation := requestForUser(http.MethodPost, "/api/admin/federation/identity/rotation", "admin")
	adminRotation.Body = io.NopCloser(strings.NewReader(`{"action":"stage"}`))
	adminRotation.Header.Set("Content-Type", "application/json")
	adminResponse := httptest.NewRecorder()
	api.ServeHTTP(adminResponse, adminRotation)
	if adminResponse.Code != http.StatusForbidden {
		t.Fatalf("ordinary admin rotated local key status=%d body=%s", adminResponse.Code, adminResponse.Body)
	}
	ownerGet := httptest.NewRecorder()
	api.ServeHTTP(ownerGet, requestForUser(http.MethodGet, "/api/admin/federation/identity", "owner"))
	if ownerGet.Code != http.StatusOK {
		t.Fatalf("non-admin owner could not read local identity status=%d body=%s", ownerGet.Code, ownerGet.Body)
	}
	ownerRotation := requestForUser(http.MethodPost, "/api/admin/federation/identity/rotation", "owner")
	ownerRotation.Body = io.NopCloser(strings.NewReader(`{"action":"stage"}`))
	ownerRotation.Header.Set("Content-Type", "application/json")
	ownerResponse := httptest.NewRecorder()
	api.ServeHTTP(ownerResponse, ownerRotation)
	if ownerResponse.Code != http.StatusOK {
		t.Fatalf("non-admin owner could not stage key rotation status=%d body=%s", ownerResponse.Code, ownerResponse.Body)
	}
	peerRequest := requestForUser(http.MethodPost, "/api/admin/federation/peers", "admin")
	peerRequest.Body = io.NopCloser(strings.NewReader(`{"origin":"https://peer.example.org","enabled":false}`))
	peerRequest.Header.Set("Content-Type", "application/json")
	peerResponse := httptest.NewRecorder()
	api.ServeHTTP(peerResponse, peerRequest)
	if peerResponse.Code != http.StatusOK {
		t.Fatalf("ordinary admin lost peer administration after claim status=%d body=%s", peerResponse.Code, peerResponse.Body)
	}
}

func federationPeerEnabled(db *sql.DB, origin string) bool {
	var enabled int
	return db.QueryRowContext(context.Background(), `SELECT enabled FROM federation_peers WHERE origin=?`, origin).Scan(&enabled) == nil && enabled == 1
}
