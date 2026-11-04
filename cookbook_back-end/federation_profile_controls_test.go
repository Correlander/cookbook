package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

func TestFederationPeerProfileMetadataAndCompareUpdate(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.org','Admin',1,'now'),('member','member@example.org','Member',0,'now')`); err != nil {
		t.Fatal(err)
	}
	peerOrigin := "https://peer.example.org"
	if err := registerFederationPeer(db, peerOrigin, false); err != nil {
		t.Fatal(err)
	}
	api := apiHandler{db: db}
	call := func(user, method, body string) *httptest.ResponseRecorder {
		req := requestForUser(method, "/api/admin/federation/peers", user)
		req.Body = io.NopCloser(strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec
	}
	get := httptest.NewRecorder()
	api.ServeHTTP(get, requestForUser(http.MethodGet, "/api/admin/federation/peers", "admin"))
	var peers []federationPeerAdminItem
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &peers) != nil || len(peers) != 1 || peers[0].ExchangeProfile != "v1" {
		t.Fatalf("peer metadata status=%d body=%s peers=%+v", get.Code, get.Body, peers)
	}
	if got := call("member", http.MethodPatch, `{"origin":"`+peerOrigin+`","expected_exchange_profile":"v1","exchange_profile":"exchange-v2-retention"}`); got.Code != http.StatusForbidden {
		t.Fatalf("non-admin profile update status=%d body=%s", got.Code, got.Body)
	}
	if got := call("admin", http.MethodPatch, `{"origin":"`+peerOrigin+`","expected_exchange_profile":"v1","exchange_profile":"exchange-v2-retention","grant":true}`); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown profile field status=%d body=%s", got.Code, got.Body)
	}
	if got := call("admin", http.MethodPatch, `{"origin":"https://peer.example.org/","expected_exchange_profile":"v1","exchange_profile":"exchange-v2-retention"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("noncanonical origin status=%d body=%s", got.Code, got.Body)
	}
	if got := call("admin", http.MethodPatch, `{"origin":"`+peerOrigin+`","expected_exchange_profile":"v1","exchange_profile":"exchange-v2-retention"}`); got.Code != http.StatusOK {
		t.Fatalf("profile update status=%d body=%s", got.Code, got.Body)
	}
	if got := call("admin", http.MethodPatch, `{"origin":"`+peerOrigin+`","expected_exchange_profile":"v1","exchange_profile":"v1"}`); got.Code != http.StatusConflict {
		t.Fatalf("stale compare status=%d body=%s", got.Code, got.Body)
	}
	if got := call("admin", http.MethodPatch, `{"origin":"`+peerOrigin+`","expected_exchange_profile":"exchange-v2-retention","exchange_profile":"unsupported"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("unsupported profile status=%d body=%s", got.Code, got.Body)
	}
}

func TestFederationPeerProfileDowngradeResynchronizesCurrentStateAtomically(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('admin','admin@example.org','Admin',1,'now'),('owner','owner@example.org','Owner',0,'now')`); err != nil {
		t.Fatal(err)
	}
	peerOrigin := "https://peer.example.org"
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	publicID := "11111111111111111111111111111111"
	withdrawnID := "22222222222222222222222222222222"
	for _, id := range []string{publicID, withdrawnID} {
		if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?,'owner','Owner','Current title','[]','[]','[]','public','owner_only','now','now')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES(?,'published','followers',0,'now')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := registerFederationPeer(db, peerOrigin, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET enabled=1,exchange_profile='exchange-v2-retention' WHERE origin=?`, peerOrigin); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{publicID, withdrawnID} {
		if err := enqueueFederationChange(tx, id); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	publicObject := testFederationOrigin + "/federation/recipes/" + publicID
	grantBody := retentionTestEvent(t, testFederationOrigin, publicObject, 2)
	if _, err := db.Exec(`UPDATE federation_local_objects SET sequence=2 WHERE recipe_id=?`, publicID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at) VALUES('grant-row',?,'00112233445566778899aabbccddeeff',?,2,?,0,'now')`, peerOrigin, publicObject, grantBody); err != nil {
		t.Fatal(err)
	}
	peerPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peer_keys(key_id,origin,public_key,status,created_at) VALUES('peer-receipt-key',?,?,'active','key-created')`, peerOrigin, peerPublicKey); err != nil {
		t.Fatal(err)
	}
	remoteObject := peerOrigin + "/federation/recipes/33333333333333333333333333333333"
	if _, err := db.Exec(`INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,logical_bytes,source_state) VALUES(?,?,?,8,0,1,100,'published')`, peerOrigin, remoteObject, "33333333333333333333333333333333"); err != nil {
		t.Fatal(err)
	}
	receiptBody := retentionTestEvent(t, peerOrigin, remoteObject, 8)
	receiptEvidence := federationRetentionEvidence{ReceiptID: "profile-receipt", EventBody: receiptBody, SignatureInput: "input", SignatureValue: "value", RequestMethod: http.MethodPost, RequestTarget: "/api/federation/inbox", CoveredComponents: "components", CoveredValues: "values", AuthenticatedOrigin: peerOrigin, KeyID: "peer-receipt-key"}
	if err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
		return admitFederationRetentionReceipt(context.Background(), tx, receiptEvidence)
	}); err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBody)
	if _, err := db.Exec(`INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body,logical_bytes) VALUES(?,?,?,?,8,'received',202,'{}',1)`, peerOrigin, "00112233445566778899aabbccddeeff", receiptDigest[:], remoteObject); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('viewer','viewer@example.org','Viewer','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('retained-pin','viewer','remote',?,'now')`, remoteObject); err != nil {
		t.Fatal(err)
	}
	retainedBody := []byte(`{"title":"Previously captured","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`)
	if err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
		_, err := persistUserImportedUnverifiedSnapshot(context.Background(), tx, "viewer", "retained-pin", sql.NullInt64{}, sql.NullString{}, sql.NullString{}, "Author", retainedBody, nil, "now")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE recipes SET visibility='private' WHERE id=?`, withdrawnID); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, withdrawnID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var canceledWithdrawalSequence int64
	withdrawnObject := testFederationOrigin + "/federation/recipes/" + withdrawnID
	if err := db.QueryRow(`SELECT sequence FROM federation_outbox WHERE peer_origin=? AND object_id=?`, peerOrigin, withdrawnObject).Scan(&canceledWithdrawalSequence); err != nil {
		t.Fatal(err)
	}
	api := apiHandler{db: db}
	call := func(expected, next string) *httptest.ResponseRecorder {
		body := `{"origin":"` + peerOrigin + `","expected_exchange_profile":"` + expected + `","exchange_profile":"` + next + `"}`
		req := requestForUser(http.MethodPatch, "/api/admin/federation/peers", "admin")
		req.Body = io.NopCloser(strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_profile_resync BEFORE INSERT ON federation_outbox WHEN NEW.peer_origin='https://peer.example.org' BEGIN SELECT RAISE(ABORT,'injected profile resync capacity failure'); END`); err != nil {
		t.Fatal(err)
	}
	if got := call("exchange-v2-retention", "v1"); got.Code != http.StatusInternalServerError {
		t.Fatalf("failed resync status=%d body=%s", got.Code, got.Body)
	}
	var profile string
	if err := db.QueryRow(`SELECT exchange_profile FROM federation_peers WHERE origin=?`, peerOrigin).Scan(&profile); err != nil || profile != federationExchangeV2Retention {
		t.Fatalf("failed resync changed profile=%q err=%v", profile, err)
	}
	var oldQueue int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, peerOrigin).Scan(&oldQueue); err != nil || oldQueue != 3 {
		t.Fatalf("failed resync changed old queue=%d err=%v", oldQueue, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_profile_resync`); err != nil {
		t.Fatal(err)
	}
	if got := call("exchange-v2-retention", "v1"); got.Code != http.StatusOK {
		t.Fatalf("downgrade status=%d body=%s", got.Code, got.Body)
	}
	if err := db.QueryRow(`SELECT exchange_profile FROM federation_peers WHERE origin=?`, peerOrigin).Scan(&profile); err != nil || profile != "v1" {
		t.Fatalf("profile after downgrade=%q err=%v", profile, err)
	}
	var queueCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, peerOrigin).Scan(&queueCount); err != nil || queueCount != 2 {
		t.Fatalf("replacement queue rows=%d err=%v", queueCount, err)
	}
	rows, err := db.Query(`SELECT object_id,payload,sequence FROM federation_outbox WHERE peer_origin=? ORDER BY object_id`, peerOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]federationEvent{}
	for rows.Next() {
		var objectID string
		var body []byte
		var sequence int64
		if err := rows.Scan(&objectID, &body, &sequence); err != nil {
			t.Fatal(err)
		}
		var event federationEvent
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		if event.SchemaVersion != 1 {
			t.Fatalf("replacement profile event schema=%d", event.SchemaVersion)
		}
		if objectID == withdrawnObject && (event.EventType != federationEventTombstone || event.Sequence <= canceledWithdrawalSequence || event.Recipe != nil) {
			t.Fatalf("withdrawal replacement=%+v previous_sequence=%d", event, canceledWithdrawalSequence)
		}
		seen[objectID] = event
	}
	var receiptCount, historyCount int
	var remoteHighWater int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts WHERE source_origin=?`, peerOrigin).Scan(&receiptCount); err != nil || receiptCount != 0 {
		t.Fatalf("downgrade retained live receipts=%d err=%v", receiptCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events WHERE source_origin=?`, peerOrigin).Scan(&historyCount); err != nil || historyCount != 1 {
		t.Fatalf("downgrade changed replay history=%d err=%v", historyCount, err)
	}
	if err := db.QueryRow(`SELECT high_water_sequence FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, peerOrigin, remoteObject).Scan(&remoteHighWater); err != nil || remoteHighWater != 8 {
		t.Fatalf("downgrade changed remote high-water=%d err=%v", remoteHighWater, err)
	}
	var retainedPins, retainedVersions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins WHERE bookmark_id='retained-pin' AND state='active'`).Scan(&retainedPins); err != nil || retainedPins != 1 {
		t.Fatalf("downgrade changed retained private pin count=%d err=%v", retainedPins, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_versions WHERE object_uri=?`, remoteObject).Scan(&retainedVersions); err != nil || retainedVersions != 1 {
		t.Fatalf("downgrade changed retained private version count=%d err=%v", retainedVersions, err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if event, ok := seen[publicObject]; !ok || event.EventType != federationEventSnapshot || event.Recipe == nil || event.Recipe.Title != "Current title" {
		t.Fatalf("public replacement=%+v exists=%v", event, ok)
	}
	if got := call("v1", "exchange-v2-retention"); got.Code != http.StatusOK {
		t.Fatalf("upgrade status=%d body=%s", got.Code, got.Body)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, peerOrigin).Scan(&queueCount); err != nil || queueCount != 4 {
		t.Fatalf("upgrade retained v1 rows and queued current v2 state: rows=%d err=%v", queueCount, err)
	}
	rows, err = db.Query(`SELECT payload FROM federation_outbox WHERE peer_origin=?`, peerOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		var bodyMap map[string]any
		if err := json.Unmarshal(body, &bodyMap); err != nil {
			t.Fatal(err)
		}
		if _, hasGrant := bodyMap["retention_grant"]; hasGrant {
			t.Fatalf("profile upgrade recreated a grant: %s", body)
		}
	}
}

func TestFederationPeerProfileDowngradeSerializesWithOwnerAssertion(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	if _, err := env.db.Exec(`UPDATE users SET is_admin=1 WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin='https://peer.example.org'`, federationExchangeV2Retention); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	assertionDone := make(chan error, 1)
	profileDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		<-start
		assertionDone <- withImmediateSQLiteWriteTx(context.Background(), env.db, func(tx sqliteExecutor) error {
			return enqueueFederationOwnerAssertion(context.Background(), tx, testFederationID, "owner", 1)
		})
	}()
	go func() {
		<-start
		request := requestForUser(http.MethodPatch, "/api/admin/federation/peers", "owner")
		request.Body = io.NopCloser(strings.NewReader(`{"origin":"https://peer.example.org","expected_exchange_profile":"exchange-v2-retention","exchange_profile":"v1"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		apiHandler{db: env.db}.ServeHTTP(response, request)
		profileDone <- response
	}()
	close(start)
	assertionErr := <-assertionDone
	profileResponse := <-profileDone
	if profileResponse.Code != http.StatusOK {
		t.Fatalf("profile transition status=%d body=%s", profileResponse.Code, profileResponse.Body)
	}
	if assertionErr != nil && !errors.Is(assertionErr, errFederationOwnerAssertionConflict) {
		t.Fatalf("concurrent owner assertion error=%v", assertionErr)
	}
	var profile string
	if err := env.db.QueryRow(`SELECT exchange_profile FROM federation_peers WHERE origin='https://peer.example.org'`).Scan(&profile); err != nil || profile != "v1" {
		t.Fatalf("final profile=%q err=%v", profile, err)
	}
	rows, err := env.db.Query(`SELECT payload FROM federation_outbox WHERE peer_origin='https://peer.example.org'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		if _, err := exchangev2.Decode(body); err == nil {
			t.Fatalf("v2 event remained queued after downgrade: %s", body)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestFederationPeerProfileUpgradeRollsBackAtOutboxCapacity(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	if _, err := env.db.Exec(`UPDATE users SET is_admin=1 WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	peerOrigin := "https://peer.example.org"
	if _, err := env.db.Exec(`DELETE FROM federation_outbox WHERE peer_origin=?`, peerOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE federation_peers SET enabled=1,exchange_profile='v1' WHERE origin=?`, peerOrigin); err != nil {
		t.Fatal(err)
	}
	tx, err := env.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		if _, err := tx.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at) VALUES(?,?,?,?,?,?,0,'queued')`, newID(), peerOrigin, newID(), testFederationOrigin+"/federation/recipes/"+testFederationID, index+1, []byte(`{}`)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	request := requestForUser(http.MethodPatch, "/api/admin/federation/peers", "owner")
	request.Body = io.NopCloser(strings.NewReader(`{"origin":"https://peer.example.org","expected_exchange_profile":"v1","exchange_profile":"exchange-v2-retention"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	apiHandler{db: env.db}.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("capacity update status=%d body=%s", response.Code, response.Body)
	}
	var profile string
	var rows int
	if err := env.db.QueryRow(`SELECT exchange_profile FROM federation_peers WHERE origin=?`, peerOrigin).Scan(&profile); err != nil || profile != "v1" {
		t.Fatalf("capacity failure changed profile=%q err=%v", profile, err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, peerOrigin).Scan(&rows); err != nil || rows != 1000 {
		t.Fatalf("capacity failure changed queue rows=%d err=%v", rows, err)
	}
}

type controlledInflightFederationTransport struct {
	started chan []byte
	release chan struct{}
}

func (transport controlledInflightFederationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.started <- body
	<-transport.release
	return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"status":"accepted"}`)), Header: make(http.Header)}, nil
}

func TestFederationPeerDowngradeCannotRecallClaimedRequest(t *testing.T) {
	env := newPublicationTestEnv(t, visibilityPublic, RecipePublicationPublished, RecipeFederationFollowers, true)
	if _, err := env.db.Exec(`UPDATE users SET is_admin=1 WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	peerOrigin := "https://peer.example.org"
	if _, err := env.db.Exec(`DELETE FROM federation_outbox WHERE peer_origin=?`, peerOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, peerOrigin); err != nil {
		t.Fatal(err)
	}
	tx, err := env.db.Begin()
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
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transport := controlledInflightFederationTransport{started: make(chan []byte, 1), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseInflight := func() { releaseOnce.Do(func() { close(transport.release) }) }
	defer releaseInflight()
	deliveryDone := make(chan error, 1)
	go func() {
		processed, err := processNextFederationDelivery(context.Background(), env.db, key, &http.Client{Transport: transport})
		if err == nil && !processed {
			err = errors.New("delivery was not claimed")
		}
		deliveryDone <- err
	}()
	var claimedBody []byte
	select {
	case claimedBody = <-transport.started:
	case <-time.After(3 * time.Second):
		releaseInflight()
		t.Fatal("delivery did not reach the controlled in-flight transport")
	}
	if _, err := exchangev2.Decode(claimedBody); err != nil {
		t.Fatalf("claimed request was not a v2 event: %v", err)
	}
	request := requestForUser(http.MethodPatch, "/api/admin/federation/peers", "owner")
	request.Body = io.NopCloser(strings.NewReader(`{"origin":"https://peer.example.org","expected_exchange_profile":"exchange-v2-retention","exchange_profile":"v1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	apiHandler{db: env.db}.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		releaseInflight()
		t.Fatalf("profile downgrade status=%d body=%s", response.Code, response.Body)
	}
	rows, err := env.db.Query(`SELECT payload,sequence FROM federation_outbox WHERE peer_origin=?`, peerOrigin)
	if err != nil {
		releaseInflight()
		t.Fatal(err)
	}
	if !rows.Next() {
		rows.Close()
		releaseInflight()
		t.Fatal("downgrade did not queue current v1 state")
	}
	var replacement []byte
	var replacementSequence int64
	if err := rows.Scan(&replacement, &replacementSequence); err != nil {
		rows.Close()
		releaseInflight()
		t.Fatal(err)
	}
	rows.Close()
	var claimedEvent exchangev2.Event
	if claimedEvent, err = exchangev2.Decode(claimedBody); err != nil || replacementSequence <= claimedEvent.Sequence {
		releaseInflight()
		t.Fatalf("replacement sequence=%d claimed=%d err=%v", replacementSequence, claimedEvent.Sequence, err)
	}
	var replacementEvent federationEvent
	if err := json.Unmarshal(replacement, &replacementEvent); err != nil || replacementEvent.SchemaVersion != 1 {
		releaseInflight()
		t.Fatalf("replacement was not v1: schema=%d err=%v body=%s", replacementEvent.SchemaVersion, err, replacement)
	}
	releaseInflight()
	if err := <-deliveryDone; err != nil {
		t.Fatalf("already-claimed delivery result: %v", err)
	}
}
