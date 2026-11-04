package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

func retentionTestEvent(t *testing.T, origin, object string, sequence int64) []byte {
	t.Helper()
	payload := []byte(`{"title":"Private copy","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`)
	digest := exchangev2.PayloadDigest(payload)
	event := exchangev2.Event{
		Protocol: exchangev2.Protocol, SchemaVersion: 2, EventID: "00112233445566778899aabbccddeeff",
		EventType: exchangev2.RecipeSnapshot, SourceOrigin: origin, ObjectID: object,
		Sequence: sequence, OccurredAt: "2026-11-04T12:00:00Z",
		Attribution: &exchangev2.Attribution{CreatorName: "Author"}, Payload: payload, PayloadSHA256: digest,
		RetentionGrant: &exchangev2.RetentionGrant{Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention,
			Portability: exchangev2.GrantPortability, ObjectID: object, Sequence: sequence, PayloadSHA256: digest},
	}
	body, err := exchangev2.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func seedRetentionReceiptFacts(t *testing.T, db *sql.DB, origin, object string, sequence int64) ([]byte, []byte) {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'peer-created','peer-updated')`, origin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, origin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peer_keys(key_id,origin,public_key,status,created_at) VALUES('key-one',?,?, 'active','key-created')`, origin, publicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,logical_bytes,source_state) VALUES(?,?,?, ?,0,1,100,'published')`, origin, object, "00112233445566778899aabbccddeeff", sequence); err != nil {
		t.Fatal(err)
	}
	return retentionTestEvent(t, origin, object, sequence), publicKey
}

func TestFederationRetentionReceiptAdmissionAndDisableClosure(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	origin := "https://retention.example.net"
	object := origin + "/federation/recipes/00112233445566778899aabbccddeeff"
	body, publicKey := seedRetentionReceiptFacts(t, db, origin, object, 4)
	digest := sha256.Sum256(body)
	evidence := federationRetentionEvidence{ReceiptID: "receipt-one", EventBody: body, SignatureInput: "sig1=(\"@method\" \"@target-uri\" \"content-digest\")", SignatureValue: "sig1=:c2lnbmF0dXJl:", RequestMethod: "POST", RequestTarget: "/api/federation/inbox", CoveredComponents: "[\"@method\",\"@target-uri\",\"content-digest\"]", CoveredValues: "[\"POST\",\"https://retention.example.net/api/federation/inbox\",\"sha-256=:...:\"]", AuthenticatedOrigin: origin, KeyID: "key-one"}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return admitFederationRetentionReceipt(ctx, tx, evidence) }); err != nil {
		t.Fatal(err)
	}
	var storedBody, storedDigest, storedKey []byte
	var storedOrigin, storedProfile, acceptedAt, keyCreated string
	var evidenceBytes, encodedBytes int64
	if err := db.QueryRow(`SELECT event_body,body_digest,public_key,authenticated_origin,profile_at_receipt,key_created_at,accepted_at,evidence_bytes,encoded_bytes FROM federation_retention_receipts WHERE source_origin=? AND object_id=?`, origin, object).Scan(&storedBody, &storedDigest, &storedKey, &storedOrigin, &storedProfile, &keyCreated, &acceptedAt, &evidenceBytes, &encodedBytes); err != nil {
		t.Fatal(err)
	}
	wantEvidenceBytes := evidence.evidenceBytes() + int64(len(publicKey)+len("active")+len("key-created")+len(federationExchangeV2Retention))
	event, err := exchangev2.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	wantEncodedBytes := retentionReceiptEncodedBytes(evidence, event, acceptedAt, keyCreated, publicKey)
	if string(storedBody) != string(body) || string(storedDigest) != string(digest[:]) || string(storedKey) != string(publicKey) || storedOrigin != origin || storedProfile != federationExchangeV2Retention || evidenceBytes != wantEvidenceBytes || encodedBytes != wantEncodedBytes {
		t.Fatal("stored receipt lost exact body or authenticated provenance")
	}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return closeFederationOriginForDisable(ctx, tx, origin) }); err != nil {
		t.Fatal(err)
	}
	var receipts int
	var sourceState string
	var sequence int64
	var catalogBytes int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipt rows=%d err=%v", receipts, err)
	}
	if err := db.QueryRow(`SELECT source_state,high_water_sequence,logical_bytes FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, object).Scan(&sourceState, &sequence, &catalogBytes); err != nil || sourceState != "disconnected" || sequence != 4 || catalogBytes != 103 {
		t.Fatalf("catalog state=%q sequence=%d bytes=%d err=%v", sourceState, sequence, catalogBytes, err)
	}
}

func TestFederationRetentionReceiptRejectsStaleProfileAndOverCapEvidence(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	origin := "https://retention.example.net"
	object := origin + "/federation/recipes/00112233445566778899aabbccddeeff"
	body, _ := seedRetentionReceiptFacts(t, db, origin, object, 4)
	evidence := federationRetentionEvidence{ReceiptID: "receipt-one", EventBody: body, SignatureInput: "input", SignatureValue: "value", RequestMethod: "POST", RequestTarget: "/api/federation/inbox", CoveredComponents: "components", CoveredValues: "values", AuthenticatedOrigin: origin, KeyID: "key-one"}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile='v1' WHERE origin=?`, origin); err != nil {
		t.Fatal(err)
	}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return admitFederationRetentionReceipt(ctx, tx, evidence) }); !errors.Is(err, errFederationRetentionEvidence) {
		t.Fatalf("stale profile admission error=%v", err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, origin); err != nil {
		t.Fatal(err)
	}
	evidence.SignatureInput = string(make([]byte, federationRetentionReceiptMaxEvidenceBytes+1))
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return admitFederationRetentionReceipt(ctx, tx, evidence) }); !errors.Is(err, errFederationRetentionEvidence) {
		t.Fatalf("oversized evidence error=%v", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rejected evidence persisted: rows=%d err=%v", rows, err)
	}
}

func TestFederationRetentionCapacityUsesReplacementDelta(t *testing.T) {
	// The schema stores no mutable quota row: this verifies replacement preserves
	// one current row and keeps accounting tied to the replacement's exact bytes.
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	origin := "https://retention.example.net"
	object := origin + "/federation/recipes/00112233445566778899aabbccddeeff"
	body, _ := seedRetentionReceiptFacts(t, db, origin, object, 4)
	evidence := federationRetentionEvidence{ReceiptID: "receipt-one", EventBody: body, SignatureInput: "input", SignatureValue: "value", RequestMethod: "POST", RequestTarget: "/api/federation/inbox", CoveredComponents: "components", CoveredValues: "values", AuthenticatedOrigin: origin, KeyID: "key-one"}
	admit := func() error {
		return withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return admitFederationRetentionReceipt(ctx, tx, evidence) })
	}
	if err := admit(); err != nil {
		t.Fatal(err)
	}
	firstID := evidence.ReceiptID
	evidence.ReceiptID = "receipt-two"
	if err := admit(); err != nil {
		t.Fatal(err)
	}
	var rows int
	var receiptID string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(receipt_id) FROM federation_retention_receipts`).Scan(&rows, &receiptID); err != nil || rows != 1 || receiptID == firstID {
		t.Fatalf("replacement rows=%d id=%q err=%v", rows, receiptID, err)
	}
}

func TestAcceptedV1TombstoneClosesCurrentReceiptAndKeepsLegacyState(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	origin := "https://retention.example.net"
	object := origin + "/federation/recipes/00112233445566778899aabbccddeeff"
	body, _ := seedRetentionReceiptFacts(t, db, origin, object, 4)
	evidence := federationRetentionEvidence{ReceiptID: "receipt-one", EventBody: body, SignatureInput: "input", SignatureValue: "value", RequestMethod: "POST", RequestTarget: "/api/federation/inbox", CoveredComponents: "components", CoveredValues: "values", AuthenticatedOrigin: origin, KeyID: "key-one"}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return admitFederationRetentionReceipt(ctx, tx, evidence) }); err != nil {
		t.Fatal(err)
	}
	source := federationRecipeSource{ID: "00112233445566778899aabbccddeeff", CreatorName: "Author", Title: "Withdrawn", Visibility: visibilityPublic}
	tombstone, err := serializeFederationEvent(origin, "ffeeddccbbaa99887766554433221100", federationEventTombstone, 5, time.Now(), true, true, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acceptFederationEvent(ctx, db, origin, tombstone); err != nil {
		t.Fatal(err)
	}
	var rows int
	var state string
	var sequence int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("receipt rows=%d err=%v", rows, err)
	}
	if err := db.QueryRow(`SELECT source_state,high_water_sequence FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, object).Scan(&state, &sequence); err != nil || state != "unknown_legacy" || sequence != 5 {
		t.Fatalf("v1 tombstone state=%q sequence=%d err=%v", state, sequence, err)
	}
}

func TestFederationRetentionReceiptCapacityRowsAndBytes(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	insertReceipt := func(tx sqliteExecutor, id, origin, object string, encodedBytes int64) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO federation_retention_receipts(receipt_id,source_origin,object_id,event_id,sequence,body_digest,event_body,signature_input,signature_value,request_method,request_target,covered_components,covered_values,authenticated_origin,key_id,public_key,key_status_at_receipt,key_created_at,profile_at_receipt,accepted_at,evidence_bytes,encoded_bytes) VALUES(?,?,?,?,1,zeroblob(32),x'00','i','v','POST','/api/federation/inbox','c','v',?,'k',zeroblob(32),'active','created','exchange-v2-retention','accepted',1,?)`, id, origin, object, id, origin, encodedBytes)
		return err
	}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
		for i := int64(0); i < federationRetentionReceiptMaxRows; i++ {
			if err := insertReceipt(tx, fmt.Sprintf("row-%03d", i), "https://capacity.example.org", fmt.Sprintf("object-%03d", i), 1); err != nil {
				return err
			}
		}
		if !errors.Is(checkFederationRetentionReceiptCapacity(ctx, tx, "https://capacity.example.org", "new-object", 1), errFederationRetentionCapacity) {
			return errors.New("row limit did not reject the next receipt")
		}
		return checkFederationRetentionReceiptCapacity(ctx, tx, "https://capacity.example.org", "object-000", 1)
	}); err != nil {
		t.Fatal(err)
	}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM federation_retention_receipts`); err != nil {
			return err
		}
		if err := insertReceipt(tx, "byte-cap", "https://capacity.example.org", "byte-object", federationRetentionReceiptMaxBytes); err != nil {
			return err
		}
		if !errors.Is(checkFederationRetentionReceiptCapacity(ctx, tx, "https://capacity.example.org", "new-object", 1), errFederationRetentionCapacity) {
			return errors.New("byte limit did not reject the next receipt")
		}
		return checkFederationRetentionReceiptCapacity(ctx, tx, "https://capacity.example.org", "byte-object", 1)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFederationRetentionReceiptAdmissionSerializesFinalRow(t *testing.T) {
	db, err := openDatabase(filepath.Join(t.TempDir(), "retention-capacity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	origin := "https://parallel-retention.example.org"
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at,exchange_profile) VALUES(?,1,'created','updated',?)`, origin, federationExchangeV2Retention); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peer_keys(key_id,origin,public_key,status,created_at) VALUES('parallel-key',?,?,'active','created')`, origin, publicKey); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"11111111111111111111111111111111", "22222222222222222222222222222222"} {
		object := origin + "/federation/recipes/" + suffix
		if _, err := db.Exec(`INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,source_state,logical_bytes) VALUES(?,?,?,1,0,1,'published',100)`, origin, object, suffix); err != nil {
			t.Fatal(err)
		}
	}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
		for i := 0; i < int(federationRetentionReceiptMaxRows)-1; i++ {
			_, err := tx.ExecContext(ctx, `INSERT INTO federation_retention_receipts(receipt_id,source_origin,object_id,event_id,sequence,body_digest,event_body,signature_input,signature_value,request_method,request_target,covered_components,covered_values,authenticated_origin,key_id,public_key,key_status_at_receipt,key_created_at,profile_at_receipt,accepted_at,evidence_bytes,encoded_bytes) VALUES(?,?,?,?,1,zeroblob(32),x'00','i','v','POST','/api/federation/inbox','c','v',?,'parallel-key',?,'active','created',?,'accepted',1,1)`, fmt.Sprintf("existing-%03d", i), origin, fmt.Sprintf("existing-%03d", i), fmt.Sprintf("existing-%03d", i), origin, publicKey, federationExchangeV2Retention)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for _, suffix := range []string{"11111111111111111111111111111111", "22222222222222222222222222222222"} {
		go func(suffix string) {
			object := origin + "/federation/recipes/" + suffix
			body := retentionTestEvent(t, origin, object, 1)
			evidence := federationRetentionEvidence{ReceiptID: "receipt-" + suffix, EventBody: body, SignatureInput: "input", SignatureValue: "value", RequestMethod: "POST", RequestTarget: "/api/federation/inbox", CoveredComponents: "components", CoveredValues: "values", AuthenticatedOrigin: origin, KeyID: "parallel-key"}
			ready.Done()
			<-start
			results <- withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return admitFederationRetentionReceipt(ctx, tx, evidence) })
		}(suffix)
	}
	ready.Wait()
	close(start)
	var accepted, capacity int
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			accepted++
		} else if errors.Is(err, errFederationRetentionCapacity) {
			capacity++
		} else {
			t.Fatalf("concurrent admission error=%v", err)
		}
	}
	if accepted != 1 || capacity != 1 {
		t.Fatalf("final-row admission accepted=%d capacity=%d", accepted, capacity)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&rows); err != nil || rows != int(federationRetentionReceiptMaxRows) {
		t.Fatalf("receipt rows=%d err=%v", rows, err)
	}
}

func TestResetFederationPeerProfileToV1ClosesReceiptWithoutEnablingV2(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	origin := "https://reset-profile.example.net"
	object := origin + "/federation/recipes/00112233445566778899aabbccddeeff"
	body, _ := seedRetentionReceiptFacts(t, db, origin, object, 4)
	evidence := federationRetentionEvidence{ReceiptID: "receipt-one", EventBody: body, SignatureInput: "input", SignatureValue: "value", RequestMethod: "POST", RequestTarget: "/api/federation/inbox", CoveredComponents: "components", CoveredValues: "values", AuthenticatedOrigin: origin, KeyID: "key-one"}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return admitFederationRetentionReceipt(ctx, tx, evidence) }); err != nil {
		t.Fatal(err)
	}
	if err := withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error { return resetFederationPeerExchangeProfileToV1(ctx, tx, origin) }); err != nil {
		t.Fatal(err)
	}
	var profile string
	var rows int
	if err := db.QueryRow(`SELECT exchange_profile FROM federation_peers WHERE origin=?`, origin).Scan(&profile); err != nil || profile != federationExchangeV1 {
		t.Fatalf("reset profile=%q err=%v", profile, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts WHERE source_origin=?`, origin).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("reset receipts=%d err=%v", rows, err)
	}
}
