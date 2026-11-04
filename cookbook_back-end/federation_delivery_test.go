package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

func TestFederationDeliveryAuthenticatesAndAppliesRemoteSnapshot(t *testing.T) {
	senderKey, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receiverDB := testDatabase(t)
	if err := registerFederationPeer(receiverDB, "https://sender.example.org", true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(receiverDB, "https://receiver.example.org", true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(receiverDB, "https://sender.example.org", senderKey.ID, senderKey.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := receiverDB.Exec(`UPDATE instance_settings SET federation_enabled=1,federation_origin='https://receiver.example.org' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationEvent("https://sender.example.org", testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Source cook", Title: "Snapshot", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	handler := federationInboxHandler{db: receiverDB, origin: "https://receiver.example.org"}
	client := &http.Client{Transport: federationHandlerTransport{handler: handler}}
	if err := deliverFederationEvent(context.Background(), client, senderKey, senderKey.ID, "https://receiver.example.org", payload); err != nil {
		t.Fatal(err)
	}
	var recipe string
	if err := receiverDB.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id=?`, "https://sender.example.org/federation/recipes/"+testFederationID).Scan(&recipe); err != nil || recipe == "" {
		t.Fatalf("remote snapshot missing: recipe=%q err=%v", recipe, err)
	}
	if _, err := receiverDB.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	listing := httptest.NewRecorder()
	publicFederationRecipesHandler{db: receiverDB}.ServeHTTP(listing, httptest.NewRequest(http.MethodGet, "/api/public/remote-recipes", nil))
	if listing.Code != http.StatusOK || !strings.Contains(listing.Body.String(), "Snapshot") {
		t.Fatalf("public remote recipe listing status=%d body=%s", listing.Code, listing.Body)
	}
}

func TestFederationInboxRejectsV2BodyUnderV1SignatureTag(t *testing.T) {
	senderKey, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receiver := testDatabase(t)
	if err := registerFederationPeer(receiver, "https://sender.example.org", true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(receiver, "https://sender.example.org", senderKey.ID, senderKey.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, "https://sender.example.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin='https://local.example' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationV2Event("https://sender.example.org", newID(), exchangev2.RecipeSnapshot, 1, time.Now(), federationRecipeSource{ID: testFederationID, CreatorName: "Sender", Title: "Dish", Visibility: visibilityPublic}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := signedFederationRequest(t, senderKey.PrivateKey, senderKey.ID, strings.Repeat("m", 40), string(payload))
	response := httptest.NewRecorder()
	federationInboxHandler{db: receiver, origin: "https://local.example"}.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("tag/body mismatch status=%d body=%s", response.Code, response.Body)
	}
	var events, objects int
	if err := receiver.QueryRow(`SELECT COUNT(*) FROM federation_received_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := receiver.QueryRow(`SELECT COUNT(*) FROM federation_remote_catalog`).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if events != 0 || objects != 0 {
		t.Fatalf("tag/body mismatch mutated receiver state events=%d objects=%d", events, objects)
	}
	mismatchedHost := signedFederationRequestWithTag(t, senderKey.PrivateKey, senderKey.ID, strings.Repeat("h", 40), string(payload), federationSignatureV2Tag)
	wrongAuthority := httptest.NewRecorder()
	federationInboxHandler{db: receiver, origin: "https://alternate.example.org"}.ServeHTTP(wrongAuthority, mismatchedHost)
	if wrongAuthority.Code != http.StatusUnauthorized {
		t.Fatalf("signed Host/configured-authority mismatch status=%d body=%s", wrongAuthority.Code, wrongAuthority.Body)
	}
	if err := receiver.QueryRow(`SELECT COUNT(*) FROM federation_received_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("authority mismatch stored %d events", events)
	}
}

func TestTwoDatabaseOwnerAssertionUsesSignedV2AndPersistsExactReceipt(t *testing.T) {
	senderOrigin, receiverOrigin := testFederationOrigin, "https://peer.example.org"
	senderKey, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sender := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	if _, err := sender.db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, receiverOrigin); err != nil {
		t.Fatal(err)
	}
	assertion := callContentAPI(sender.handler, http.MethodPost, "/api/recipes/"+testFederationID+"/federation-retention", `{"expected_recipe_version":1}`, sender.owner)
	if assertion.Code != http.StatusAccepted {
		t.Fatalf("owner assertion status=%d body=%s", assertion.Code, assertion.Body)
	}
	var exactV2 []byte
	if err := sender.db.QueryRow(`SELECT payload FROM federation_outbox WHERE peer_origin=? ORDER BY sequence DESC LIMIT 1`, receiverOrigin).Scan(&exactV2); err != nil {
		t.Fatal(err)
	}
	parsed, err := exchangev2.Decode(exactV2)
	if err != nil || parsed.RetentionGrant == nil {
		t.Fatalf("sender outbox v2 event=%+v err=%v", parsed, err)
	}

	receiver := testDatabase(t)
	if err := registerFederationPeer(receiver, senderOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(receiver, senderOrigin, senderKey.ID, senderKey.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, senderOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, receiverOrigin); err != nil {
		t.Fatal(err)
	}
	handler := federationInboxHandler{db: receiver, origin: receiverOrigin}
	var sentV2SignatureInput, sentV2Signature, sentV2Digest string
	client := &http.Client{Transport: federationHandlerTransport{handler: handler, onRequest: func(request *http.Request) {
		if strings.Contains(request.Header.Get("Signature-Input"), `tag="`+federationSignatureV2Tag+`"`) {
			sentV2SignatureInput = request.Header.Get("Signature-Input")
			sentV2Signature = request.Header.Get("Signature")
			sentV2Digest = request.Header.Get("Content-Digest")
		}
	}}}
	for index := 0; index < 2; index++ {
		processed, err := processNextFederationDelivery(context.Background(), sender.db, senderKey, client)
		if err != nil || !processed {
			t.Fatalf("delivery %d processed=%t err=%v", index+1, processed, err)
		}
	}
	var cached []byte
	if err := receiver.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id=?`, parsed.ObjectID).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, parsed.Payload) {
		t.Fatalf("cached body differs from exact signed event payload (%d != %d bytes)", len(cached), len(parsed.Payload))
	}
	var receiptBody []byte
	var signatureInput, signatureValue, components, values, authenticatedOrigin, requestMethod, requestTarget, receiptKeyID, keyStatus, receiptProfile string
	var receiptPublicKey []byte
	if err := receiver.QueryRow(`SELECT event_body,signature_input,signature_value,covered_components,covered_values,authenticated_origin,request_method,request_target,key_id,public_key,key_status_at_receipt,profile_at_receipt FROM federation_retention_receipts WHERE source_origin=? AND object_id=?`, senderOrigin, parsed.ObjectID).Scan(&receiptBody, &signatureInput, &signatureValue, &components, &values, &authenticatedOrigin, &requestMethod, &requestTarget, &receiptKeyID, &receiptPublicKey, &keyStatus, &receiptProfile); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receiptBody, exactV2) || signatureInput == "" || signatureValue == "" || signatureInput != sentV2SignatureInput || signatureValue != sentV2Signature || authenticatedOrigin != senderOrigin || requestMethod != http.MethodPost || requestTarget != "/api/federation/inbox" || receiptKeyID != senderKey.ID || !bytes.Equal(receiptPublicKey, senderKey.PublicKey) || keyStatus != "active" || receiptProfile != federationExchangeV2Retention {
		t.Fatal("receiver did not retain the exact signed receipt evidence")
	}
	var covered, coveredValues []string
	if err := json.Unmarshal([]byte(components), &covered); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(values), &coveredValues); err != nil {
		t.Fatal(err)
	}
	if len(covered) != 5 || len(coveredValues) != 5 || covered[1] != "@target-uri" || coveredValues[1] != receiverOrigin+"/api/federation/inbox" || covered[4] != "content-digest" || coveredValues[4] != sentV2Digest || !strings.Contains(coveredValues[4], "sha-256=:") {
		t.Fatalf("invalid positional signed evidence: %s / %s", components, values)
	}
	for index := int64(0); index < federationRetentionReceiptMaxRows-1; index++ {
		id := fmt.Sprintf("duplicate-capacity-%03d", index)
		if _, err := receiver.Exec(`INSERT INTO federation_retention_receipts(receipt_id,source_origin,object_id,event_id,sequence,body_digest,event_body,signature_input,signature_value,request_method,request_target,covered_components,covered_values,authenticated_origin,key_id,public_key,key_status_at_receipt,key_created_at,profile_at_receipt,accepted_at,evidence_bytes,encoded_bytes) VALUES(?,?,?,?,1,zeroblob(32),x'00','i','v','POST','/api/federation/inbox','[]','[]',?,'key',zeroblob(32),'active','created',?,'accepted',1,1)`, id, "https://prior.example.org", id, id, "https://prior.example.org", federationExchangeV2Retention); err != nil {
			t.Fatal(err)
		}
	}
	var receiptRows int64
	if err := receiver.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&receiptRows); err != nil || receiptRows != federationRetentionReceiptMaxRows {
		t.Fatalf("receipt table rows=%d err=%v, want full cap %d", receiptRows, err, federationRetentionReceiptMaxRows)
	}
	stale, err := serializeFederationV2Event(senderOrigin, newID(), exchangev2.RecipeUpdate, parsed.Sequence, time.Now(), federationRecipeSource{ID: testFederationID, CreatorName: "Owner", Title: "Stale", Visibility: visibilityPublic}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := deliverFederationEvent(context.Background(), client, senderKey, senderKey.ID, receiverOrigin, stale); err == nil || !strings.Contains(err.Error(), "status 409") {
		t.Fatalf("stale v2 sequence error=%v, want conflict", err)
	}
	if err := deliverFederationEvent(context.Background(), client, senderKey, senderKey.ID, receiverOrigin, exactV2); err != nil {
		t.Fatalf("same event/digest did not return stored response: %v", err)
	}
	if err := receiver.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&receiptRows); err != nil || receiptRows != federationRetentionReceiptMaxRows {
		t.Fatalf("duplicate changed full receipt table rows=%d err=%v", receiptRows, err)
	}
	tx, err := sender.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE recipes SET title='Later edit',version=version+1,updated_at=? WHERE id=?`, timestamp(), testFederationID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, testFederationID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var grantless []byte
	if err := sender.db.QueryRow(`SELECT payload FROM federation_outbox WHERE peer_origin=? ORDER BY sequence DESC LIMIT 1`, receiverOrigin).Scan(&grantless); err != nil {
		t.Fatal(err)
	}
	grantlessEvent, err := exchangev2.Decode(grantless)
	if err != nil || grantlessEvent.RetentionGrant != nil {
		t.Fatalf("ordinary edit carried retention grant: %+v err=%v", grantlessEvent.RetentionGrant, err)
	}
	processed, err := processNextFederationDelivery(context.Background(), sender.db, senderKey, client)
	if err != nil || !processed {
		t.Fatalf("ordinary v2 update processed=%t err=%v", processed, err)
	}
	var receiptCount int
	if err := receiver.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts WHERE source_origin=? AND object_id=?`, senderOrigin, parsed.ObjectID).Scan(&receiptCount); err != nil || receiptCount != 0 {
		t.Fatalf("ordinary grantless update left receipts=%d err=%v", receiptCount, err)
	}
	tx, err = sender.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE recipes SET visibility='private',version=version+1,updated_at=? WHERE id=?`, timestamp(), testFederationID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, testFederationID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var withdrawal []byte
	if err := sender.db.QueryRow(`SELECT payload FROM federation_outbox WHERE peer_origin=? ORDER BY sequence DESC LIMIT 1`, receiverOrigin).Scan(&withdrawal); err != nil {
		t.Fatal(err)
	}
	withdrawalEvent, err := exchangev2.Decode(withdrawal)
	if err != nil || withdrawalEvent.EventType != exchangev2.RecipeWithdrawal || withdrawalEvent.RetentionGrant != nil || len(withdrawalEvent.Payload) != 0 {
		t.Fatalf("invalid v2 withdrawal: %+v err=%v", withdrawalEvent, err)
	}
	processed, err = processNextFederationDelivery(context.Background(), sender.db, senderKey, client)
	if err != nil || !processed {
		t.Fatalf("v2 withdrawal processed=%t err=%v", processed, err)
	}
	var sourceState string
	var deleted int
	if err := receiver.QueryRow(`SELECT source_state,deleted FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, senderOrigin, parsed.ObjectID).Scan(&sourceState, &deleted); err != nil || sourceState != "withdrawn" || deleted != 1 {
		t.Fatalf("withdrawn source state=%q deleted=%d err=%v", sourceState, deleted, err)
	}
}

func TestSignedV2ReceiptCapacityRejectsAndRollsBackReceiverState(t *testing.T) {
	senderOrigin, receiverOrigin := "https://sender.example.org", "https://receiver.example.org"
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := testDatabase(t)
	if err := registerFederationPeer(db, senderOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, senderOrigin, key.ID, key.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, senderOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, receiverOrigin); err != nil {
		t.Fatal(err)
	}
	for index := int64(0); index < federationRetentionReceiptMaxRows; index++ {
		id := fmt.Sprintf("capacity-%03d", index)
		if _, err := db.Exec(`INSERT INTO federation_retention_receipts(receipt_id,source_origin,object_id,event_id,sequence,body_digest,event_body,signature_input,signature_value,request_method,request_target,covered_components,covered_values,authenticated_origin,key_id,public_key,key_status_at_receipt,key_created_at,profile_at_receipt,accepted_at,evidence_bytes,encoded_bytes) VALUES(?,?,?,?,1,zeroblob(32),x'00','i','v','POST','/api/federation/inbox','[]','[]',?,'key',zeroblob(32),'active','created',?,'accepted',1,1)`, id, "https://prior.example.org", id, id, "https://prior.example.org", federationExchangeV2Retention); err != nil {
			t.Fatal(err)
		}
	}
	body, err := serializeFederationV2Event(senderOrigin, newID(), exchangev2.RecipeSnapshot, 1, time.Now(), federationRecipeSource{ID: testFederationID, CreatorName: "Sender", Title: "Dish", Visibility: visibilityPublic}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	event, err := exchangev2.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	event.RetentionGrant = &exchangev2.RetentionGrant{Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention, Portability: exchangev2.GrantPortability, ObjectID: event.ObjectID, Sequence: event.Sequence, PayloadSHA256: event.PayloadSHA256}
	body, err = exchangev2.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: federationHandlerTransport{handler: federationInboxHandler{db: db, origin: receiverOrigin}}}
	err = deliverFederationEvent(context.Background(), client, key, key.ID, receiverOrigin, body)
	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("receipt-capacity delivery error=%v, want retryable 503", err)
	}
	var receipts, events, objects, caches int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_catalog WHERE object_id=?`, event.ObjectID).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipes WHERE object_id=?`, event.ObjectID).Scan(&caches); err != nil {
		t.Fatal(err)
	}
	if receipts != int(federationRetentionReceiptMaxRows) || events != 0 || objects != 0 || caches != 0 {
		t.Fatalf("capacity rejection mutated receiver state receipts=%d events=%d catalog=%d cache=%d", receipts, events, objects, caches)
	}
}

func TestSignedV2MaximumPayloadRemainsReadableAndExact(t *testing.T) {
	senderOrigin, receiverOrigin := "https://sender.example.org", "https://receiver.example.org"
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := testDatabase(t)
	if err := registerFederationPeer(db, senderOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, senderOrigin, key.ID, key.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, senderOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, receiverOrigin); err != nil {
		t.Fatal(err)
	}
	recipe := []byte(`{"title":"Dish","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`)
	recipe = append(recipe, bytes.Repeat([]byte(" "), 1_500_000-len(recipe))...)
	object, err := federationRecipeObjectID(senderOrigin, testFederationID)
	if err != nil {
		t.Fatal(err)
	}
	event := exchangev2.Event{Protocol: exchangev2.Protocol, SchemaVersion: exchangev2.SchemaVersion, EventID: newID(), EventType: exchangev2.RecipeSnapshot, SourceOrigin: senderOrigin, ObjectID: object, Sequence: 1, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Attribution: &exchangev2.Attribution{CreatorName: "Sender"}, Payload: recipe, PayloadSHA256: exchangev2.PayloadDigest(recipe)}
	event.RetentionGrant = &exchangev2.RetentionGrant{Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention, Portability: exchangev2.GrantPortability, ObjectID: object, Sequence: 1, PayloadSHA256: event.PayloadSHA256}
	body, err := exchangev2.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > exchangev2.MaxEventBytes {
		t.Fatalf("maximum payload event exceeded HTTP limit: %d", len(body))
	}
	client := &http.Client{Transport: federationHandlerTransport{handler: federationInboxHandler{db: db, origin: receiverOrigin}}}
	if err := deliverFederationEvent(context.Background(), client, key, key.ID, receiverOrigin, body); err != nil {
		t.Fatal(err)
	}
	var cached []byte
	if err := db.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id=?`, object).Scan(&cached); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, recipe) || len(cached) != 1_500_000 {
		t.Fatalf("cached v2 payload length=%d, exact=%t", len(cached), bytes.Equal(cached, recipe))
	}
}

func TestTwoDatabaseFederationPublishUpdateDuplicateAndUnpublish(t *testing.T) {
	senderOrigin := "https://sender.example.org"
	receiverOrigin := "https://receiver.example.org"
	senderKey, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receiverDB := testDatabase(t)
	if err := registerFederationPeer(receiverDB, senderOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(receiverDB, receiverOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(receiverDB, senderOrigin, senderKey.ID, senderKey.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := receiverDB.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, receiverOrigin); err != nil {
		t.Fatal(err)
	}
	senderDB := testDatabase(t)
	if _, err := senderDB.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := senderDB.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?, 'owner','Source cook','First title','[]','[]','[]','public','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := senderDB.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at)
		VALUES(?,'published','followers',0,'now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := senderDB.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, senderOrigin); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(senderDB, receiverOrigin, true); err != nil {
		t.Fatal(err)
	}
	handler := federationInboxHandler{db: receiverDB, origin: receiverOrigin}
	client := &http.Client{Transport: federationHandlerTransport{handler: handler}}
	queueAndDeliver := func() []byte {
		t.Helper()
		tx, err := senderDB.Begin()
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
		var payload []byte
		if err := senderDB.QueryRow(`SELECT payload FROM federation_outbox ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		processed, err := processNextFederationDelivery(context.Background(), senderDB, senderKey, client)
		if err != nil || !processed {
			t.Fatalf("delivery processed=%t err=%v", processed, err)
		}
		return payload
	}

	firstEvent := queueAndDeliver()
	firstObjectID := senderOrigin + "/federation/recipes/" + testFederationID
	var title string
	if err := receiverDB.QueryRow(`SELECT json_extract(recipe_json,'$.title') FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&title); err != nil || title != "First title" {
		t.Fatalf("first snapshot title=%q err=%v", title, err)
	}
	if err := deliverFederationEvent(context.Background(), client, senderKey, senderKey.ID, receiverOrigin, firstEvent); err != nil {
		t.Fatalf("duplicate delivery was not idempotent: %v", err)
	}
	if _, err := senderDB.Exec(`UPDATE recipes SET title='Updated title' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	if err := receiverDB.QueryRow(`SELECT json_extract(recipe_json,'$.title') FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&title); err != nil || title != "Updated title" {
		t.Fatalf("updated remote title=%q err=%v", title, err)
	}
	if _, err := senderDB.Exec(`UPDATE recipes SET visibility='private' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	var deleted int
	var recipeJSON string
	if err := receiverDB.QueryRow(`SELECT deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&deleted, &recipeJSON); err != nil || deleted != 1 || recipeJSON != "" {
		t.Fatalf("remote tombstone deleted=%d recipe=%q err=%v", deleted, recipeJSON, err)
	}
	listing := httptest.NewRecorder()
	publicFederationRecipesHandler{db: receiverDB}.ServeHTTP(listing, httptest.NewRequest(http.MethodGet, "/api/public/remote-recipes", nil))
	if listing.Code != http.StatusOK || listing.Body.String() != "[]\n" {
		t.Fatalf("unpublished remote recipe remained public: status=%d body=%s", listing.Code, listing.Body)
	}
	if _, err := senderDB.Exec(`UPDATE recipes SET visibility='public' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	if _, err := senderDB.Exec(`UPDATE recipes SET deleted_at='now' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	if err := receiverDB.QueryRow(`SELECT deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&deleted, &recipeJSON); err != nil || deleted != 1 || recipeJSON != "" {
		t.Fatalf("deleted remote recipe retained content: deleted=%d recipe=%q err=%v", deleted, recipeJSON, err)
	}
}

type federationHandlerTransport struct {
	handler   http.Handler
	onRequest func(*http.Request)
}

func (t federationHandlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.onRequest != nil {
		t.onRequest(request)
	}
	clone := request.Clone(request.Context())
	clone.Host = request.URL.Host
	clone.RequestURI = request.URL.RequestURI()
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, clone)
	return recorder.Result(), nil
}

func TestFederationEgressRejectsNonPublicDNSAnswers(t *testing.T) {
	for name, addresses := range map[string][]netip.Addr{
		"public IPv4":    {netip.MustParseAddr("1.1.1.1")},
		"public IPv6":    {netip.MustParseAddr("2606:4700:4700::1111")},
		"private":        {netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("10.0.0.1")},
		"loopback":       {netip.MustParseAddr("127.0.0.1")},
		"link local":     {netip.MustParseAddr("169.254.169.254")},
		"shared address": {netip.MustParseAddr("100.64.0.1")},
		"IPv6 ULA":       {netip.MustParseAddr("fd00::1")},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateFederationAddresses(addresses)
			if name == "public IPv4" || name == "public IPv6" {
				if err != nil {
					t.Fatalf("rejected public address: %v", err)
				}
			} else if err == nil {
				t.Fatal("accepted non-public DNS answer")
			}
		})
	}
}

func TestFederationDialRevalidatesDNSOnEveryConnection(t *testing.T) {
	resolver := &rebindingFederationResolver{}
	dialed := []string{}
	dial := federationDialContext(resolver, func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, io.ErrClosedPipe
	})
	if _, err := dial(context.Background(), "tcp", "peer.example.org:443"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("first dial error=%v", err)
	}
	if _, err := dial(context.Background(), "tcp", "peer.example.org:443"); err == nil {
		t.Fatal("accepted a private address after DNS changed")
	}
	if resolver.calls != 2 || len(dialed) != 1 || dialed[0] != "1.1.1.1:443" {
		t.Fatalf("DNS calls=%d dialed=%v", resolver.calls, dialed)
	}
}

type rebindingFederationResolver struct{ calls int }

func (r *rebindingFederationResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	if r.calls == 1 {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestFederationDeliveryDoesNotFollowRedirects(t *testing.T) {
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	transport := &federationRedirectTransport{}
	client := buildFederationDeliveryClient(nil)
	client.Transport = transport
	if err := deliverFederationEvent(context.Background(), client, key, key.ID, "https://peer.example.org", payload); err == nil {
		t.Fatal("accepted a redirect response")
	}
	if transport.calls != 1 {
		t.Fatalf("redirect request count=%d, want 1", transport.calls)
	}
}

type federationRedirectTransport struct{ calls int }

func (t *federationRedirectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://other.example.org/inbox"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func insertOptedInFederationDeliverySource(t *testing.T, db *sql.DB, origin string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('delivery-owner','delivery@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at)
		VALUES(?, 'delivery-owner','Creator','Dish','[]','[]','[]','public','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at)
		VALUES(?,'published','followers',0,'now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, origin); err != nil {
		t.Fatal(err)
	}
}

func TestFederationWorkerRetriesAndDeletesAcknowledgedItems(t *testing.T) {
	db := testDatabase(t)
	insertOptedInFederationDeliverySource(t, db, testFederationOrigin)
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
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
	if _, err := db.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at) VALUES(?,?,?,?,?,?,?,?)`, newID(), "https://peer.example.org", event.EventID, event.ObjectID, event.Sequence, payload, time.Now().Unix(), timestamp()); err != nil {
		t.Fatal(err)
	}
	failedClient := &http.Client{Transport: federationStatusTransport{status: http.StatusServiceUnavailable}}
	processed, err := processNextFederationDelivery(context.Background(), db, key, failedClient)
	if err != nil || !processed {
		t.Fatalf("failed attempt processed=%t err=%v", processed, err)
	}
	var attempts int
	var nextAttempt int64
	if err := db.QueryRow(`SELECT attempts,next_attempt FROM federation_outbox`).Scan(&attempts, &nextAttempt); err != nil || attempts != 1 || nextAttempt <= time.Now().Unix() {
		t.Fatalf("retry state attempts=%d next=%d err=%v", attempts, nextAttempt, err)
	}
	if _, err := db.Exec(`UPDATE federation_outbox SET next_attempt=?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	workingClient := &http.Client{Transport: federationStatusTransport{status: http.StatusAccepted}}
	processed, err = processNextFederationDelivery(context.Background(), db, key, workingClient)
	if err != nil || !processed {
		t.Fatalf("success processed=%t err=%v", processed, err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining queue=%d err=%v", remaining, err)
	}
}

func TestFederationOutboxSurvivesDatabaseRestartAndExpiredLease(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "federation.db")
	db, err := openDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	insertOptedInFederationDeliverySource(t, db, testFederationOrigin)
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
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
	if _, err := db.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,lease_until,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, newID(), "https://peer.example.org", event.EventID, event.ObjectID, event.Sequence, payload, time.Now().Unix(), time.Now().Add(-time.Minute).Unix(), timestamp()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	processed, err := processNextFederationDelivery(context.Background(), db, key, &http.Client{Transport: federationStatusTransport{status: http.StatusAccepted}})
	if err != nil || !processed {
		t.Fatalf("recovered delivery processed=%t err=%v", processed, err)
	}
	var queued int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("queue after recovery=%d err=%v", queued, err)
	}
}

type federationStatusTransport struct{ status int }

func (t federationStatusTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Body: io.NopCloser(strings.NewReader(`{"status":"accepted"}`)), Header: make(http.Header)}, nil
}
