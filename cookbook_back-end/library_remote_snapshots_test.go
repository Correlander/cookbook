package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

func TestSignedRetentionReceiptCaptureIsOwnerScopedAndModerationCollectsBytes(t *testing.T) {
	senderOrigin, receiverOrigin := "https://snapshot-source.example.org", "https://snapshot-local.example.org"
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if err := registerFederationPeer(db, senderOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, senderOrigin, key.ID, key.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, senderOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET federation_enabled=1,federation_origin=? WHERE id=1`, receiverOrigin); err != nil {
		t.Fatal(err)
	}
	grant := &exchangev2.RetentionGrant{Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention,
		Portability: exchangev2.GrantPortability, ObjectID: senderOrigin + "/federation/recipes/" + testFederationID,
		Sequence: 1}
	source := federationRecipeSource{ID: testFederationID, CreatorName: "Source cook", Title: "Retained dish", Visibility: visibilityPublic}
	unsigned, err := serializeFederationV2Event(senderOrigin, newID(), exchangev2.RecipeSnapshot, 1, time.Now(), source, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant.PayloadSHA256 = exchangev2.PayloadDigest(mustDecodeV2Payload(t, unsigned))
	payload, err := serializeFederationV2Event(senderOrigin, newID(), exchangev2.RecipeSnapshot, 1, time.Now(), source, mustDecodeV2Payload(t, unsigned), grant)
	if err != nil {
		t.Fatal(err)
	}
	inbox := federationInboxHandler{db: db, origin: receiverOrigin}
	client := &http.Client{Transport: federationHandlerTransport{handler: inbox}}
	if err := deliverFederationEvent(context.Background(), client, key, key.ID, receiverOrigin, payload); err != nil {
		t.Fatal(err)
	}
	objectURI := senderOrigin + "/federation/recipes/" + testFederationID
	savedReference := doBookmarkRequest(t, apiHandler{db: db}, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"remote_object_uri":"`+objectURI+`"}}`)
	var referenceView RecipeBookmarkView
	if savedReference.Code != http.StatusCreated || json.Unmarshal(savedReference.Body.Bytes(), &referenceView) != nil || referenceView.SaveState != "reference" {
		t.Fatalf("default bookmark save did not remain a reference: status=%d body=%s", savedReference.Code, savedReference.Body)
	}
	bookmarkID := referenceView.ID
	h := apiHandler{db: db}
	for _, save := range []struct {
		actor, mode string
	}{
		{actor: "other", mode: "conditional"},
		{actor: "admin", mode: "snapshot"},
	} {
		response := doBookmarkRequest(t, h, save.actor, http.MethodPost, "/api/library/bookmarks", `{"save_mode":"`+save.mode+`","target":{"remote_object_uri":"`+objectURI+`"}}`)
		var view RecipeBookmarkView
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.SaveState != "snapshot" {
			t.Fatalf("new %s save did not return snapshot wire state: actor=%s status=%d body=%s", save.mode, save.actor, response.Code, response.Body)
		}
	}
	type captureResult struct {
		version remoteSnapshotVersion
		err     error
	}
	lockedReceipt := make(chan remoteSnapshotReceipt)
	allowCapture := make(chan struct{})
	captureDone := make(chan captureResult, 1)
	go func() {
		var version remoteSnapshotVersion
		err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
			receipt, err := readEligibleFederationRetentionReceipt(context.Background(), tx, objectURI)
			if err != nil {
				return err
			}
			lockedReceipt <- receipt
			<-allowCapture
			version, err = captureRemoteSnapshot(context.Background(), tx, "viewer", bookmarkID, referenceView.CreatedAt)
			return err
		})
		captureDone <- captureResult{version: version, err: err}
	}()
	originalReceipt := <-lockedReceipt
	replacementStarted := make(chan struct{})
	replacementDone := make(chan error, 1)
	go func() {
		close(replacementStarted)
		replacementDone <- withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
			return admitFederationRetentionReceipt(context.Background(), tx, retentionEvidenceFromSnapshotReceipt(originalReceipt, "replacement-receipt"))
		})
	}()
	<-replacementStarted
	close(allowCapture)
	racedCapture := <-captureDone
	if racedCapture.err != nil {
		t.Fatalf("capture during serialized receipt replacement: %v", racedCapture.err)
	}
	if err := <-replacementDone; err != nil {
		t.Fatalf("serialized receipt replacement: %v", err)
	}
	if string(racedCapture.version.Evidence.ReceiptID) != originalReceipt.ReceiptID {
		t.Fatalf("capture did not retain the exact pre-replacement receipt: %q", racedCapture.version.Evidence.ReceiptID)
	}
	var currentReceiptID string
	if err := db.QueryRow(`SELECT receipt_id FROM federation_retention_receipts WHERE object_id=?`, objectURI).Scan(&currentReceiptID); err != nil || currentReceiptID != "replacement-receipt" {
		t.Fatalf("receipt replacement did not complete after capture: id=%q err=%v", currentReceiptID, err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET private_snapshot_storage_limit_bytes=? WHERE id=1`, racedCapture.version.EncodedBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('snap-quota','owner','remote',?,'created')`, objectURI); err != nil {
		t.Fatal(err)
	}
	if err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
		_, err := captureRemoteSnapshot(context.Background(), tx, "owner", "snap-quota", "created")
		return err
	}); !errors.Is(err, errRemoteSnapshotStoreCap) {
		t.Fatalf("capture one byte beyond the exact instance boundary error=%v", err)
	}
	if _, err := db.Exec(`DELETE FROM recipe_bookmarks WHERE id='snap-quota'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET private_snapshot_storage_limit_bytes=? WHERE id=1`, defaultRemoteSnapshotStoreSize); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('snap-close','owner','remote',?,'created')`, objectURI); err != nil {
		t.Fatal(err)
	}
	closeLockHeld := make(chan struct{})
	allowCloseCapture := make(chan struct{})
	closeCaptureDone := make(chan error, 1)
	go func() {
		closeCaptureDone <- withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
			if _, err := readEligibleFederationRetentionReceipt(context.Background(), tx, objectURI); err != nil {
				return err
			}
			close(closeLockHeld)
			<-allowCloseCapture
			_, err := captureRemoteSnapshot(context.Background(), tx, "owner", "snap-close", "created")
			return err
		})
	}()
	<-closeLockHeld
	closerStarted := make(chan struct{})
	closerDone := make(chan error, 1)
	go func() {
		close(closerStarted)
		closerDone <- withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
			return closeFederationRetentionReceipt(context.Background(), tx, senderOrigin, objectURI)
		})
	}()
	<-closerStarted
	close(allowCloseCapture)
	if err := <-closeCaptureDone; err != nil {
		t.Fatalf("capture during serialized receipt closure: %v", err)
	}
	if err := <-closerDone; err != nil {
		t.Fatalf("receipt closure after capture: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM recipe_bookmarks WHERE id='snap-close'`); err != nil {
		t.Fatal(err)
	}
	capture := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks/"+bookmarkID+"/capture", "")
	if capture.Code != http.StatusOK || capture.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("capture status=%d cache=%q body=%s", capture.Code, capture.Header().Get("Cache-Control"), capture.Body)
	}
	var captureView RecipeBookmarkView
	if err := json.Unmarshal(capture.Body.Bytes(), &captureView); err != nil || captureView.SaveState != "snapshot" {
		t.Fatalf("new capture DTO did not use snapshot wire state: state=%q err=%v body=%s", captureView.SaveState, err, capture.Body)
	}
	repeatedSave := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", `{"save_mode":"conditional","target":{"remote_object_uri":"`+objectURI+`"}}`)
	var repeatedView RecipeBookmarkView
	if repeatedSave.Code != http.StatusOK || json.Unmarshal(repeatedSave.Body.Bytes(), &repeatedView) != nil || repeatedView.SaveState != "snapshot" {
		t.Fatalf("repeated active save leaked persisted state: status=%d body=%s", repeatedSave.Code, repeatedSave.Body)
	}
	var persistedState string
	if err := db.QueryRow(`SELECT state FROM library_remote_snapshot_pins WHERE bookmark_id=?`, bookmarkID).Scan(&persistedState); err != nil || persistedState != "active" {
		t.Fatalf("wire projection changed persisted pin state: state=%q err=%v", persistedState, err)
	}
	var fallback []libraryRecipeEntryRef
	fallbackTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err = libraryCanonicalRemoteRecipeEntries(context.Background(), fallbackTx, "viewer", "")
	_ = fallbackTx.Rollback()
	if err != nil {
		t.Fatalf("canonical library fallback: %v", err)
	}
	var fallbackJSON []byte
	for _, entry := range fallback {
		if entry.Item.BookmarkID == bookmarkID {
			fallbackJSON, err = json.Marshal(entry.Item)
			if err != nil || !strings.Contains(string(fallbackJSON), `"save_state":"snapshot"`) {
				t.Fatalf("canonical fallback DTO did not use snapshot wire state: %s err=%v", fallbackJSON, err)
			}
		}
	}
	if len(fallbackJSON) == 0 {
		t.Fatal("canonical fallback omitted active snapshot")
	}
	var publicProjection map[string]json.RawMessage
	if err := json.Unmarshal(capture.Body.Bytes(), &publicProjection); err != nil {
		t.Fatal(err)
	}
	for _, privateField := range []string{"event_body", "signature_input", "signature_value", "covered_values", "public_key", "payload"} {
		if _, exposed := publicProjection[privateField]; exposed {
			t.Fatalf("HTTP snapshot projection exposed raw evidence field %q", privateField)
		}
	}
	captureRecord, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmarkID)
	if err != nil {
		t.Fatalf("owner export seam did not read active capture: %v", err)
	}
	admittedBody := originalReceipt.EventBody
	if captureRecord.Provenance != "publisher_verified" || string(captureRecord.Evidence.EventBody) != string(admittedBody) || len(captureRecord.Evidence.PublicKey) != 32 {
		t.Fatal("snapshot capture did not preserve the exact authenticated receipt evidence")
	}
	if _, err := db.Exec(`UPDATE instance_settings SET private_snapshot_storage_limit_bytes=? WHERE id=1`, captureRecord.EncodedBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnedActiveSnapshotForExport(context.Background(), db, "other", bookmarkID); err != errRemoteSnapshotNotFound {
		t.Fatalf("cross-account export error=%v, want not found", err)
	}
	if _, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmarkID); err != nil {
		t.Fatalf("reducing the storage cap blocked an existing read: %v", err)
	}
	foreign := doBookmarkRequest(t, h, "other", http.MethodGet, "/api/library/bookmarks/"+bookmarkID+"/snapshot", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-account snapshot status=%d body=%s", foreign.Code, foreign.Body)
	}
	list := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/bookmarks?limit=20", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"snapshot_recipe"`) || !strings.Contains(list.Body.String(), `"title":"Retained dish"`) {
		t.Fatalf("owner bookmark list omitted the frozen snapshot: status=%d body=%s", list.Code, list.Body)
	}
	for _, privateField := range []string{"event_body", "signature_input", "signature_value", "covered_values", "public_key"} {
		if strings.Contains(list.Body.String(), `"`+privateField+`"`) {
			t.Fatalf("bookmark list exposed raw evidence field %q", privateField)
		}
	}
	search := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/recipes?q=Retained", "")
	var projectedPage LibraryRecipePage
	if search.Code != http.StatusOK || json.Unmarshal(search.Body.Bytes(), &projectedPage) != nil || !strings.Contains(search.Body.String(), `"snapshot_recipe"`) || !strings.Contains(search.Body.String(), `"total":1`) || len(projectedPage.Items) != 1 || projectedPage.Items[0].SaveState != "snapshot" {
		t.Fatalf("SQL-projected private snapshot search status=%d body=%s", search.Code, search.Body)
	}
	updatedSource := source
	updatedSource.Title = "Current source update"
	updateBody, err := serializeFederationV2Event(senderOrigin, newID(), exchangev2.RecipeUpdate, 2, time.Now(), updatedSource, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := deliverFederationEvent(context.Background(), client, key, key.ID, receiverOrigin, updateBody); err != nil {
		t.Fatalf("ordinary source update failed: %v", err)
	}
	withdrawalBody, err := serializeFederationV2Event(senderOrigin, newID(), exchangev2.RecipeWithdrawal, 3, time.Now(), source, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := deliverFederationEvent(context.Background(), client, key, key.ID, receiverOrigin, withdrawalBody); err != nil {
		t.Fatalf("ordinary source withdrawal failed: %v", err)
	}
	afterWithdrawal, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmarkID)
	if err != nil || string(afterWithdrawal.Payload) != string(captureRecord.Payload) || string(afterWithdrawal.Evidence.EventBody) != string(admittedBody) || afterWithdrawal.SourceState != "withdrawn" {
		t.Fatalf("frozen copy changed after update/withdrawal: source_state=%q err=%v", afterWithdrawal.SourceState, err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE federation_peers SET enabled=0,updated_at=? WHERE origin=?`, timestamp(), senderOrigin); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := closeFederationOriginForDisable(context.Background(), tx, senderOrigin); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := revokeFederationPeerKey(db, senderOrigin, key.ID); err != nil {
		t.Fatalf("remove now-disabled peer key: %v", err)
	}
	afterDisconnect, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmarkID)
	if err != nil || string(afterDisconnect.Payload) != string(captureRecord.Payload) || string(afterDisconnect.Evidence.EventBody) != string(admittedBody) || afterDisconnect.SourceState != "disconnected" {
		t.Fatalf("frozen copy changed after disconnect/key removal: source_state=%q err=%v", afterDisconnect.SourceState, err)
	}
	tamperedPayload := []byte(strings.Replace(string(captureRecord.Payload), "Retained dish", "Changed dish", 1))
	if _, err := db.Exec(`UPDATE library_remote_snapshot_versions SET payload_json=? WHERE version_id=?`, tamperedPayload, captureRecord.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmarkID); !errors.Is(err, errRemoteSnapshotEvidence) {
		t.Fatalf("publisher detail/export accepted payload whose stored digest mismatched: %v", err)
	}
	if _, err := readRemoteSnapshotRecipesByBookmark(context.Background(), db, "viewer", []string{bookmarkID}); !errors.Is(err, errRemoteSnapshotEvidence) {
		t.Fatalf("publisher list accepted payload whose stored digest mismatched: %v", err)
	}
	if _, err := db.Exec(`UPDATE library_remote_snapshot_versions SET payload_json=?,payload_sha256=? WHERE version_id=?`, captureRecord.Payload, captureRecord.PayloadDigest, captureRecord.VersionID); err != nil {
		t.Fatal(err)
	}
	tamperedEvent, err := exchangev2.Decode(captureRecord.Evidence.EventBody)
	if err != nil {
		t.Fatal(err)
	}
	tamperedEvent.EventID = newID()
	tamperedBody, err := exchangev2.Encode(tamperedEvent)
	if err != nil {
		t.Fatal(err)
	}
	tamperedDigest := sha256.Sum256(tamperedBody)
	if _, err := db.Exec(`UPDATE library_remote_snapshot_versions SET event_body=?,event_body_sha256=? WHERE version_id=?`, tamperedBody, tamperedDigest[:], captureRecord.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmarkID); !errors.Is(err, errRemoteSnapshotEvidence) {
		t.Fatalf("full capture accepted a self-consistent event body with a mismatched stored event ID: %v", err)
	}

	admin := doBookmarkRequest(t, h, "admin", http.MethodPatch, "/api/admin/library/snapshots/"+bookmarkID, `{"outcome":"removed","reason_category":"privacy"}`)
	if admin.Code != http.StatusOK || !strings.Contains(admin.Body.String(), `"save_state":"removed"`) {
		t.Fatalf("moderation removal status=%d body=%s", admin.Code, admin.Body)
	}
	removedSave := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"remote_object_uri":"`+objectURI+`"}}`)
	if removedSave.Code != http.StatusOK || !strings.Contains(removedSave.Body.String(), `"save_state":"removed"`) || strings.Contains(removedSave.Body.String(), `"save_state":"snapshot"`) {
		t.Fatalf("repeated save changed removed marker: status=%d body=%s", removedSave.Code, removedSave.Body)
	}
	var versions int
	var marker, state string
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_versions WHERE version_id=?`, captureRecord.VersionID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state,COALESCE(removed_version_id,'') FROM library_remote_snapshot_pins WHERE bookmark_id=?`, bookmarkID).Scan(&state, &marker); err != nil {
		t.Fatal(err)
	}
	if versions != 0 || state != "removed" || marker == "" {
		t.Fatalf("removed snapshot retained bytes or lost marker: versions=%d state=%q marker=%q", versions, state, marker)
	}
	if _, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmarkID); err != errRemoteSnapshotRemoved {
		t.Fatalf("removed snapshot export error=%v, want explicit blocked status", err)
	}
	status := doBookmarkRequest(t, h, "viewer", http.MethodGet, "/api/library/bookmarks/status?remote_object_uri="+objectURI, "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"save_state":"removed"`) {
		t.Fatalf("removed status=%d body=%s", status.Code, status.Body)
	}
	detail := httptest.NewRecorder()
	h.libraryRemoteRecipeDetailGuard(detail, requestForUser(http.MethodGet, "/api/library/recipes/remote?remote_object_uri="+objectURI, "viewer"))
	if detail.Code != http.StatusNotFound {
		t.Fatalf("removed remote detail fell back to cache: status=%d body=%s", detail.Code, detail.Body)
	}
	if _, err := db.Exec(`DELETE FROM recipe_bookmarks WHERE id=?`, bookmarkID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM recipe_bookmarks WHERE user_id IN ('other','admin') AND remote_object_uri=?`, objectURI); err != nil {
		t.Fatal(err)
	}
	var pins int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins`).Scan(&pins); err != nil || pins != 0 {
		t.Fatalf("ordinary bookmark deletion left pin rows=%d err=%v", pins, err)
	}
}

func mustDecodeV2Payload(t *testing.T, eventBody []byte) []byte {
	t.Helper()
	event, err := exchangev2.Decode(eventBody)
	if err != nil {
		t.Fatal(err)
	}
	return event.Payload
}

func retentionEvidenceFromSnapshotReceipt(receipt remoteSnapshotReceipt, receiptID string) federationRetentionEvidence {
	return federationRetentionEvidence{ReceiptID: receiptID, EventBody: append([]byte(nil), receipt.EventBody...),
		SignatureInput: receipt.SignatureInput, SignatureValue: receipt.SignatureValue, RequestMethod: receipt.RequestMethod,
		RequestTarget: receipt.RequestTarget, CoveredComponents: receipt.CoveredComponents, CoveredValues: receipt.CoveredValues,
		AuthenticatedOrigin: receipt.AuthenticatedOrigin, KeyID: receipt.KeyID}
}

func TestDirectImportedUnverifiedPersistenceKeepsPinsAndClaimsUntrusted(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if err := checkRemoteSnapshotQuota(context.Background(), db, "viewer", maxRemoteSnapshotBytesPerUser); err != nil {
		t.Fatalf("exact per-account byte boundary rejected: %v", err)
	}
	if err := checkRemoteSnapshotQuota(context.Background(), db, "viewer", maxRemoteSnapshotBytesPerUser+1); !errors.Is(err, errRemoteSnapshotAccountCap) {
		t.Fatalf("per-account byte boundary plus one error=%v", err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET private_snapshot_storage_limit_bytes=100 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := checkRemoteSnapshotQuota(context.Background(), db, "viewer", 100); err != nil {
		t.Fatalf("exact instance byte boundary rejected: %v", err)
	}
	if err := checkRemoteSnapshotQuota(context.Background(), db, "viewer", 101); !errors.Is(err, errRemoteSnapshotStoreCap) {
		t.Fatalf("instance byte boundary plus one error=%v", err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET private_snapshot_storage_limit_bytes=? WHERE id=1`, defaultRemoteSnapshotStoreSize); err != nil {
		t.Fatal(err)
	}
	objectURI := testFederationOrigin + "/federation/recipes/1123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('imported-pin','viewer','remote',?,'created')`, objectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('reference-pin','viewer','remote',?,'created')`, testFederationOrigin+"/federation/recipes/2123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"title":"Imported copy","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`)
	claim := []byte(strings.Repeat("archive-evidence-byte-", 6000))
	var captured remoteSnapshotVersion
	if err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
		var err error
		captured, err = persistUserImportedUnverifiedSnapshot(context.Background(), tx, "viewer", "imported-pin", sql.NullInt64{}, sql.NullString{}, sql.NullString{}, "Imported author", payload, claim, "import-time")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if captured.Provenance != "user_imported_unverified" || captured.Evidence.EventBody != nil || captured.GrantPurpose != "" || captured.Sequence.Valid || captured.EventID.Valid || captured.EventType.Valid {
		t.Fatalf("archive claims gained trust or evidence: %+v", captured)
	}
	var storedClaim []byte
	if err := db.QueryRow(`SELECT untrusted_claim FROM library_remote_snapshot_versions WHERE version_id=?`, captured.VersionID).Scan(&storedClaim); err != nil || string(storedClaim) != string(claim) {
		t.Fatalf("bounded original archive evidence claim was not preserved exactly: bytes=%d err=%v", len(storedClaim), err)
	}
	read, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", "imported-pin")
	if err != nil || read.Provenance != "user_imported_unverified" || read.Evidence.EventBody != nil || string(read.UntrustedClaim) != string(claim) || read.Sequence.Valid || read.EventType.Valid {
		t.Fatalf("imported owner read=%+v err=%v", read, err)
	}
	var storedSequence sql.NullInt64
	var storedEventID, storedEventType sql.NullString
	if err := db.QueryRow(`SELECT sequence,event_id,event_type FROM library_remote_snapshot_versions WHERE version_id=?`, captured.VersionID).Scan(&storedSequence, &storedEventID, &storedEventType); err != nil || storedSequence.Valid || storedEventID.Valid || storedEventType.Valid {
		t.Fatalf("absent source facts were synthesized: sequence=%+v eventID=%+v eventType=%+v err=%v", storedSequence, storedEventID, storedEventType, err)
	}
	listed, err := readRemoteSnapshotRecipesByBookmark(context.Background(), db, "viewer", []string{"imported-pin"})
	if err != nil || listed["imported-pin"].Sequence != nil {
		t.Fatalf("unverified list synthesized an absent source sequence: view=%+v err=%v", listed["imported-pin"], err)
	}
	detail := doBookmarkRequest(t, apiHandler{db: db}, "viewer", http.MethodGet, "/api/library/bookmarks/imported-pin/snapshot", "")
	if detail.Code != http.StatusOK || detail.Header().Get("Cache-Control") != "private, no-store" || detail.Header().Get("Vary") != "Cookie" || detail.Header().Get("Referrer-Policy") != "no-referrer" || strings.Contains(detail.Body.String(), `"sequence"`) || strings.Contains(detail.Body.String(), `"untrusted_claim"`) || strings.Contains(detail.Body.String(), `"event_body"`) {
		t.Fatalf("unverified detail exposed absent or private facts: status=%d body=%s", detail.Code, detail.Body)
	}
	assertDetailError := func(actor, bookmarkID, wantCode string) {
		t.Helper()
		response := doBookmarkRequest(t, apiHandler{db: db}, actor, http.MethodGet, "/api/library/bookmarks/"+bookmarkID+"/snapshot", "")
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusNotFound || len(body) != 1 || body["error"] != wantCode {
			t.Fatalf("snapshot detail actor=%q bookmark=%q: status=%d body=%s, want 404 %q", actor, bookmarkID, response.Code, response.Body, wantCode)
		}
		if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Vary") != "Cookie" || response.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("snapshot detail actor=%q bookmark=%q lost private headers: %v", actor, bookmarkID, response.Header())
		}
		for _, privateField := range []string{"title", "untrusted_claim", "event_body", "signature_input", "signature_value", "public_key", "moderation_reason", "moderation_outcome"} {
			if strings.Contains(response.Body.String(), `"`+privateField+`"`) {
				t.Fatalf("snapshot detail error exposed field %q: %s", privateField, response.Body)
			}
		}
	}
	assertDetailError("other", "imported-pin", "not_found")
	assertDetailError("admin", "imported-pin", "not_found")
	assertDetailError("viewer", "missing-pin", "not_found")
	assertDetailError("viewer", "reference-pin", "not_found")
	if _, err := readEligibleFederationRetentionReceipt(context.Background(), db, objectURI); !errors.Is(err, errRemoteSnapshotNotEligible) {
		t.Fatalf("untrusted import became live capture eligibility: %v", err)
	}
	tamperedPayload := []byte(strings.Replace(string(payload), "Imported copy", "Tampered copy", 1))
	if _, err := db.Exec(`UPDATE library_remote_snapshot_versions SET payload_json=? WHERE version_id=?`, tamperedPayload, captured.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", "imported-pin"); !errors.Is(err, errRemoteSnapshotEvidence) {
		t.Fatalf("detail/export accepted imported payload whose stored hash mismatched: %v", err)
	}
	if _, err := readRemoteSnapshotRecipesByBookmark(context.Background(), db, "viewer", []string{"imported-pin"}); !errors.Is(err, errRemoteSnapshotEvidence) {
		t.Fatalf("list accepted imported payload whose stored hash mismatched: %v", err)
	}
	if err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
		_, err := persistUserImportedUnverifiedSnapshot(context.Background(), tx, "viewer", "imported-pin", sql.NullInt64{}, sql.NullString{}, sql.NullString{}, "Replacement", payload, nil, "later")
		return err
	}); !errors.Is(err, errRemoteSnapshotAlreadyPinned) {
		t.Fatalf("second import replaced an existing frozen pin: %v", err)
	}
	var state, versionID string
	if err := db.QueryRow(`SELECT p.state,p.version_id FROM library_remote_snapshot_pins p WHERE bookmark_id='imported-pin'`).Scan(&state, &versionID); err != nil || state != "active" || versionID != captured.VersionID {
		t.Fatalf("existing pin changed after attempted replacement: state=%q version=%q err=%v", state, versionID, err)
	}
	if _, err := db.Exec(`UPDATE library_remote_snapshot_pins SET state='restricted',moderated_by='admin',moderated_at='now',moderation_reason='policy',moderation_outcome='restricted' WHERE bookmark_id='imported-pin'`); err != nil {
		t.Fatal(err)
	}
	if err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
		_, err := persistUserImportedUnverifiedSnapshot(context.Background(), tx, "viewer", "imported-pin", sql.NullInt64{}, sql.NullString{}, sql.NullString{}, "Resurrect", payload, nil, "later")
		return err
	}); !errors.Is(err, errRemoteSnapshotAlreadyPinned) {
		t.Fatalf("import erased a blocked marker: %v", err)
	}
	if _, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", "imported-pin"); !errors.Is(err, errRemoteSnapshotRestricted) {
		t.Fatalf("restricted imported copy lost blocked status: %v", err)
	}
	assertDetailError("viewer", "imported-pin", "snapshot_restricted")
	assertDetailError("other", "imported-pin", "not_found")
	assertDetailError("admin", "imported-pin", "not_found")
	if _, err := db.Exec(`UPDATE library_remote_snapshot_pins SET state='removed',removed_version_id=version_id,version_id=NULL,moderated_by='admin',moderated_at='later',moderation_reason='privacy',moderation_outcome='removed' WHERE bookmark_id='imported-pin'`); err != nil {
		t.Fatal(err)
	}
	assertDetailError("viewer", "imported-pin", "snapshot_removed")
	assertDetailError("other", "imported-pin", "not_found")
	assertDetailError("admin", "imported-pin", "not_found")
	if _, err := db.Exec(`DELETE FROM users WHERE id='viewer'`); err != nil {
		t.Fatal(err)
	}
	var versions, pins int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins`).Scan(&pins); err != nil || versions != 0 || pins != 0 {
		t.Fatalf("account deletion did not synchronously collect snapshot bytes: versions=%d pins=%d err=%v", versions, pins, err)
	}
}

func TestDirectPersistenceSerializesTheFinalInstanceQuotaSlot(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	ids := []string{"1123456789abcdef0123456789abcdef", "2123456789abcdef0123456789abcdef", "3123456789abcdef0123456789abcdef"}
	objectURI := func(id string) string { return testFederationOrigin + "/federation/recipes/" + id }
	payload := []byte(`{"title":"Quota copy","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`)
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('quota-seed','viewer','remote',?,'created')`, objectURI(ids[0])); err != nil {
		t.Fatal(err)
	}
	var seeded remoteSnapshotVersion
	if err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
		var err error
		seeded, err = persistUserImportedUnverifiedSnapshot(context.Background(), tx, "viewer", "quota-seed", sql.NullInt64{}, sql.NullString{}, sql.NullString{}, "Author", payload, nil, "same-time")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM recipe_bookmarks WHERE id='quota-seed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET private_snapshot_storage_limit_bytes=? WHERE id=1`, seeded.EncodedBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES
		('quota-a','viewer','remote',?,'created'),('quota-b','viewer','remote',?,'created')`, objectURI(ids[1]), objectURI(ids[2])); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, bookmarkID := range []string{"quota-a", "quota-b"} {
		bookmarkID := bookmarkID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := withImmediateSQLiteWriteTx(context.Background(), db, func(tx sqliteExecutor) error {
				_, err := persistUserImportedUnverifiedSnapshot(context.Background(), tx, "viewer", bookmarkID, sql.NullInt64{}, sql.NullString{}, sql.NullString{}, "Author", payload, nil, "same-time")
				return err
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	accepted, rejected := 0, 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if errors.Is(err, errRemoteSnapshotStoreCap) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent admission error: %v", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("final quota admission accepted=%d rejected=%d, want one of each", accepted, rejected)
	}
	var pins, versions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins WHERE state='active'`).Scan(&pins); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_versions`).Scan(&versions); err != nil || pins != 1 || versions != 1 {
		t.Fatalf("concurrent quota outcome pins=%d versions=%d err=%v", pins, versions, err)
	}
}

func TestDirectSnapshotPersistenceHonorsTheExactThousandPinAccountLimit(t *testing.T) {
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	payload := []byte(`{"title":"Quota pin","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`)
	digest := sha256Digest(payload)
	insertBookmark, err := db.Prepare(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES(?, 'viewer','remote',?,'created')`)
	if err != nil {
		t.Fatal(err)
	}
	defer insertBookmark.Close()
	insertVersion, err := db.Prepare(`INSERT INTO library_remote_snapshot_versions(version_id,object_uri,source_origin,source_recipe_id,sequence,payload_json,payload_sha256,creator_name,received_at,event_id,event_type,provenance_state,encoded_capture_bytes,title,description,tags_json)
		VALUES(?,?,?,?,NULL,?,?,?,'created',NULL,NULL,'user_imported_unverified',1,'Quota pin','','[]')`)
	if err != nil {
		t.Fatal(err)
	}
	defer insertVersion.Close()
	insertPin, err := db.Prepare(`INSERT INTO library_remote_snapshot_pins(bookmark_id,version_id,state,created_at) VALUES(?,?,'active','created')`)
	if err != nil {
		t.Fatal(err)
	}
	defer insertPin.Close()
	for index := 0; index < maxRemoteSnapshotsPerAccount-1; index++ {
		recipeID := fmt.Sprintf("%032x", index+1)
		bookmarkID := fmt.Sprintf("pin-%04d", index)
		versionID := fmt.Sprintf("version-%04d", index)
		objectURI := testFederationOrigin + "/federation/recipes/" + recipeID
		if _, err := insertBookmark.Exec(bookmarkID, objectURI); err != nil {
			t.Fatal(err)
		}
		if _, err := insertVersion.Exec(versionID, objectURI, testFederationOrigin, recipeID, payload, digest, "Archive author"); err != nil {
			t.Fatal(err)
		}
		if _, err := insertPin.Exec(bookmarkID, versionID); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRemoteSnapshotQuota(context.Background(), db, "viewer", 1); err != nil {
		t.Fatalf("exact thousandth pin was rejected: %v", err)
	}
	recipeID := fmt.Sprintf("%032x", maxRemoteSnapshotsPerAccount)
	bookmarkID, versionID := "pin-1000", "version-1000"
	objectURI := testFederationOrigin + "/federation/recipes/" + recipeID
	if _, err := insertBookmark.Exec(bookmarkID, objectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := insertVersion.Exec(versionID, objectURI, testFederationOrigin, recipeID, payload, digest, "Archive author"); err != nil {
		t.Fatal(err)
	}
	if _, err := insertPin.Exec(bookmarkID, versionID); err != nil {
		t.Fatal(err)
	}
	if err := checkRemoteSnapshotQuota(context.Background(), db, "viewer", 1); !errors.Is(err, errRemoteSnapshotAccountCap) {
		t.Fatalf("thousand-and-first pin quota error=%v", err)
	}
}
