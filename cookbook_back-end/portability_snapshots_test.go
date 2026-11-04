package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

func createPortableSnapshotFixture(t *testing.T) (*sql.DB, apiHandler, string, string, remoteSnapshotVersion, []byte) {
	t.Helper()
	senderOrigin, receiverOrigin := "https://portable-source.example.org", "https://portable-local.example.org"
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := testDatabase(t)
	insertRecipeBookmarkFixture(t, db)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('importer','importer@example.test','Importer','now'),('importer2','importer2@example.test','Importer two','now'),('reference-only','reference@example.test','Reference only','now')`); err != nil {
		t.Fatal(err)
	}
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
	objectURI := senderOrigin + "/federation/recipes/" + testFederationID
	source := federationRecipeSource{ID: testFederationID, CreatorName: "Portable creator", Title: "Portable snapshot", Visibility: visibilityPublic}
	grant := &exchangev2.RetentionGrant{Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention,
		Portability: exchangev2.GrantPortability, ObjectID: objectURI, Sequence: 1}
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
	h := apiHandler{db: db}
	saved := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks", `{"target":{"remote_object_uri":"`+objectURI+`"}}`)
	var bookmark RecipeBookmarkView
	if saved.Code != http.StatusCreated || json.Unmarshal(saved.Body.Bytes(), &bookmark) != nil {
		t.Fatalf("create source bookmark status=%d body=%s", saved.Code, saved.Body)
	}
	captured := doBookmarkRequest(t, h, "viewer", http.MethodPost, "/api/library/bookmarks/"+bookmark.ID+"/capture", "")
	if captured.Code != http.StatusOK {
		t.Fatalf("capture source bookmark status=%d body=%s", captured.Code, captured.Body)
	}
	version, err := readOwnedActiveSnapshotForExport(context.Background(), db, "viewer", bookmark.ID)
	if err != nil {
		t.Fatalf("read captured fixture: %v", err)
	}
	return db, h, bookmark.ID, objectURI, version, version.Evidence.EventBody
}

func portableSnapshotArchive(t *testing.T, data []byte) (portableBundle, map[int]portableSnapshotEntry, map[string][]byte) {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var bundle portableBundle
	files := make(map[string][]byte)
	for _, item := range archive.File {
		reader, err := item.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read archive entry %q: %v/%v", item.Name, readErr, closeErr)
		}
		if item.Name == "manifest.json" {
			if err := json.Unmarshal(content, &bundle); err != nil {
				t.Fatal(err)
			}
		} else {
			files[item.Name] = content
		}
	}
	bundle.snapshotFiles = files
	entries, err := decodePortableSnapshotReferences(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return bundle, entries, files
}

func importPortableSnapshotArchive(t *testing.T, h http.Handler, userID string, data []byte, preserve string) *httptest.ResponseRecorder {
	t.Helper()
	request := requestForUser(http.MethodPost, "/api/import", userID)
	request.Body = io.NopCloser(bytes.NewReader(data))
	if preserve != "" {
		request.Header.Set(portableSnapshotPreserveHeader, preserve)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func exportPortableSnapshotArchive(t *testing.T, h http.Handler, userID string) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, requestForUser(http.MethodGet, "/api/export", userID))
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot archive export status=%d body=%s", response.Code, response.Body)
	}
	return append([]byte(nil), response.Body.Bytes()...)
}

func TestPortableSnapshotV8SamePersonImportPreservesOpaqueClaim(t *testing.T) {
	db, h, _, objectURI, original, eventBody := createPortableSnapshotFixture(t)
	sourceArchive := exportPortableSnapshotArchive(t, h, "viewer")
	bundle, entries, files := portableSnapshotArchive(t, sourceArchive)
	if bundle.Format != portableFormatV8 || len(entries) != 1 || len(files) != 1 {
		t.Fatalf("v8 snapshot export format/count=%q/%d files=%d", bundle.Format, len(entries), len(files))
	}
	entry := entries[0]
	if !strings.HasPrefix(string(entry.Claim), portableSnapshotClaimMagic) || !bytes.Contains(entry.Claim, eventBody) || !bytes.Equal(entry.Payload, original.Payload) || !bytes.Equal(entry.PayloadSHA256, original.PayloadDigest) {
		t.Fatal("first trusted export did not preserve exact payload and versioned raw evidence claim")
	}
	if entry.SourceSequence == nil || *entry.SourceSequence != original.Sequence.Int64 || entry.SourceEventID == nil || *entry.SourceEventID != original.EventID.String {
		t.Fatalf("source nullable facts were not retained: sequence=%v event=%v", entry.SourceSequence, entry.SourceEventID)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('importer-bookmark','importer','remote',?,'2026-11-04T00:00:00Z')`, objectURI); err != nil {
		t.Fatal(err)
	}
	withoutClaim := importPortableSnapshotArchive(t, h, "reference-only", sourceArchive, "")
	if withoutClaim.Code != http.StatusCreated || !strings.Contains(withoutClaim.Body.String(), `"snapshot_copies":0`) || !strings.Contains(withoutClaim.Body.String(), `"snapshot_references":1`) {
		t.Fatalf("default import did not report reference-only result: %d %s", withoutClaim.Code, withoutClaim.Body)
	}
	var referencePins int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins p JOIN recipe_bookmarks b ON b.id=p.bookmark_id WHERE b.user_id='reference-only'`).Scan(&referencePins); err != nil || referencePins != 0 {
		t.Fatalf("reference-only import created a pin: count=%d err=%v", referencePins, err)
	}
	response := importPortableSnapshotArchive(t, h, "importer", sourceArchive, "true")
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"snapshot_copies":1`) || !strings.Contains(response.Body.String(), `"snapshot_references":0`) {
		t.Fatalf("explicit same-person import failed: %d %s", response.Code, response.Body)
	}
	imported, err := readOwnedActiveSnapshotForExport(context.Background(), db, "importer", "importer-bookmark")
	if err != nil {
		t.Fatal(err)
	}
	if imported.Provenance != "user_imported_unverified" || !bytes.Equal(imported.Payload, original.Payload) || !bytes.Equal(imported.UntrustedClaim, entry.Claim) || len(imported.Evidence.ReceiptID) != 0 || len(imported.Evidence.EventBody) != 0 || imported.Evidence.SignatureInput != "" || imported.Evidence.KeyID != "" {
		t.Fatal("imported copy was promoted, modified, or lost its opaque evidence claim")
	}
	var eventID, eventType sql.NullString
	var trustedID, signatureInput, grantPurpose, untrustedClaim []byte
	if err := db.QueryRow(`SELECT event_id,event_type,trusted_receipt_id,signature_input,grant_purpose,untrusted_claim FROM library_remote_snapshot_versions WHERE version_id=?`, imported.VersionID).Scan(&eventID, &eventType, &trustedID, &signatureInput, &grantPurpose, &untrustedClaim); err != nil {
		t.Fatal(err)
	}
	if !eventID.Valid || !eventType.Valid || len(trustedID) != 0 || len(signatureInput) != 0 || len(grantPurpose) != 0 || !bytes.Equal(untrustedClaim, entry.Claim) {
		t.Fatalf("imported source claims or trust columns changed: event=%v/%v trusted=%x signature=%x grant=%x", eventID, eventType, trustedID, signatureInput, grantPurpose)
	}
	secondArchive := exportPortableSnapshotArchive(t, h, "importer")
	_, secondEntries, _ := portableSnapshotArchive(t, secondArchive)
	if !bytes.Equal(secondEntries[0].Claim, entry.Claim) {
		t.Fatal("re-export wrapped or changed the opaque imported claim")
	}
	response = importPortableSnapshotArchive(t, h, "importer2", secondArchive, "true")
	if response.Code != http.StatusCreated {
		t.Fatalf("second same-person import failed: %d %s", response.Code, response.Body)
	}
	var secondID string
	if err := db.QueryRow(`SELECT id FROM recipe_bookmarks WHERE user_id='importer2' AND remote_object_uri=?`, objectURI).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	second, err := readOwnedActiveSnapshotForExport(context.Background(), db, "importer2", secondID)
	if err != nil || second.Provenance != "user_imported_unverified" || !bytes.Equal(second.UntrustedClaim, entry.Claim) {
		t.Fatalf("second round trip promoted or rewrote claim: provenance=%q err=%v", second.Provenance, err)
	}
	for _, preserve := range []string{"true", "false"} {
		conflict := importPortableSnapshotArchive(t, h, "importer", sourceArchive, preserve)
		if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "snapshot_already_pinned") {
			t.Fatalf("existing pin was replaced/downgraded for preserve=%s: %d %s", preserve, conflict.Code, conflict.Body)
		}
	}
}

func TestPortableSnapshotV8BlockedExportAndTamperingFailClosed(t *testing.T) {
	db, h, bookmarkID, _, _, _ := createPortableSnapshotFixture(t)
	for _, state := range []string{"restricted", "removed"} {
		if state == "restricted" {
			if _, err := db.Exec(`UPDATE library_remote_snapshot_pins SET state='restricted',moderated_by='admin',moderated_at='now',moderation_reason='policy',moderation_outcome='restricted' WHERE bookmark_id=?`, bookmarkID); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := db.Exec(`UPDATE library_remote_snapshot_pins SET state='removed',removed_version_id=version_id,version_id=NULL,moderated_by='admin',moderated_at='now',moderation_reason='privacy',moderation_outcome='removed' WHERE bookmark_id=?`, bookmarkID); err != nil {
				t.Fatal(err)
			}
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, requestForUser(http.MethodGet, "/api/export", "viewer"))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "snapshot_"+state) || strings.HasPrefix(response.Body.String(), "PK") {
			t.Fatalf("%s export did not fail before ZIP response: %d %s", state, response.Code, response.Body)
		}
	}
	_, h, _, _, _, _ = createPortableSnapshotFixture(t)
	archiveBytes := exportPortableSnapshotArchive(t, h, "viewer")
	archive, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatal(err)
	}
	var manifest portableBundle
	files := map[string][]byte{}
	for _, item := range archive.File {
		reader, err := item.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if item.Name == "manifest.json" {
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
		} else {
			files[item.Name] = data
		}
	}
	ref := (*manifest.SnapshotEntries)[0]
	files[ref.Entry] = append(files[ref.Entry], 'x')
	badArchive := mustZipPortableFiles(t, manifest, files)
	if decoded, _, err := decodePortableArchive(badArchive); err != nil {
		t.Fatal(err)
	} else if err := validatePortableBundle(decoded); err == nil {
		t.Fatal("tampered snapshot entry passed archive digest validation")
	}
	manifest.Format = portableFormatV7
	legacyWithSnapshot := mustZipPortableFiles(t, manifest, files)
	if decoded, _, err := decodePortableArchive(legacyWithSnapshot); err != nil {
		t.Fatal(err)
	} else if err := validatePortableBundle(decoded); err == nil {
		t.Fatal("v7 archive accepted v8 snapshot entries")
	}
}

func mustZipPortableFiles(t *testing.T, bundle portableBundle, files map[string][]byte) []byte {
	t.Helper()
	manifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	entry, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(manifest); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestPortableSnapshotArchiveRejectsUnsafeAndOversizedStaging(t *testing.T) {
	if validPortableSnapshotPath("snapshots/../00000001.json") || validPortableSnapshotPath("snapshots/00000001.json/../x") || validPortableSnapshotPath("snapshots/0000000x.json") {
		t.Fatal("unsafe snapshot path accepted")
	}
	if _, err := buildPortableExportArchive([]byte(`{}`), t.TempDir(), nil, map[string][]byte{"snapshots/00000001.json": bytes.Repeat([]byte("x"), maxImportBytes)}); !errors.Is(err, errPortableExportTooLarge) {
		t.Fatalf("oversized uncompressed staged archive error=%v", err)
	}
	buffer := &boundedArchiveBuffer{limit: 4}
	if n, err := buffer.Write([]byte("12345")); n != 0 || !errors.Is(err, errPortableExportTooLarge) || buffer.buffer.Len() != 0 {
		t.Fatalf("bounded archive writer partially wrote over-limit bytes: n=%d err=%v len=%d", n, err, buffer.buffer.Len())
	}
	manifest, files := []byte("1234"), map[string][]byte{"snapshots/00000001.json": []byte("x")}
	archive, err := buildPortableExportArchiveWithLimits(manifest, "", nil, files, 4096, 4, 5)
	if err != nil {
		t.Fatalf("exact manifest/decoded limits rejected: %v", err)
	}
	if _, err := buildPortableExportArchiveWithLimits(manifest, "", nil, files, 4096, 3, 5); !errors.Is(err, errPortableExportTooLarge) {
		t.Fatalf("one-over manifest limit error=%v", err)
	}
	if _, err := buildPortableExportArchiveWithLimits(manifest, "", nil, files, 4096, 4, 4); !errors.Is(err, errPortableExportTooLarge) {
		t.Fatalf("one-over decoded total error=%v", err)
	}
	if _, err := buildPortableExportArchiveWithLimits(manifest, "", nil, files, int64(len(archive)), 4, 5); err != nil {
		t.Fatalf("exact complete ZIP output limit rejected: %v", err)
	}
	if _, err := buildPortableExportArchiveWithLimits(manifest, "", nil, files, int64(len(archive))-1, 4, 5); !errors.Is(err, errPortableExportTooLarge) {
		t.Fatalf("one-over complete ZIP output limit error=%v", err)
	}
	if total, ok := accumulatePortableArchiveSize(4, 1, 5); !ok || total != 5 {
		t.Fatalf("exact advertised ZIP decoded total rejected: total=%d ok=%t", total, ok)
	}
	if _, ok := accumulatePortableArchiveSize(4, 2, 5); ok {
		t.Fatal("one-over advertised ZIP decoded total accepted")
	}
}

func TestPortableSnapshotCaptureAccountingExactAndOneOver(t *testing.T) {
	_, _, _, objectURI, snapshot, _ := createPortableSnapshotFixture(t)
	bookmark := portableBookmark{TargetKind: "remote", RemoteObjectURI: objectURI}
	entry, err := portableSnapshotEntryFromVersion(snapshot, bookmark)
	if err != nil {
		t.Fatal(err)
	}
	content, err := parseRemoteSnapshotPayload(entry.Payload)
	if err != nil {
		t.Fatal(err)
	}
	current, err := portableSnapshotCaptureBytes(bookmark, entry, content)
	if err != nil || current >= maxRemoteSnapshotCaptureBytes {
		t.Fatalf("fixture capture size=%d err=%v", current, err)
	}
	fixedFields := current - int64(len(entry.Claim))
	exact := entry
	exact.Claim = bytes.Repeat([]byte{'c'}, int(maxRemoteSnapshotCaptureBytes-fixedFields))
	if encoded, err := validatePortableSnapshotEntry(exact, bookmark); err != nil || encoded != maxRemoteSnapshotCaptureBytes {
		t.Fatalf("exact Core capture accounting boundary rejected: bytes=%d err=%v", encoded, err)
	}
	over := exact
	over.Claim = append(append([]byte(nil), exact.Claim...), 'x')
	if _, err := validatePortableSnapshotEntry(over, bookmark); !errors.Is(err, errRemoteSnapshotTooLarge) {
		t.Fatalf("one-over Core capture accounting boundary error=%v", err)
	}
}

func TestPortableSnapshotImportQuotaFailureRollsBackBookmarks(t *testing.T) {
	db, h, _, _, _, _ := createPortableSnapshotFixture(t)
	if _, err := db.Exec(`UPDATE instance_settings SET private_snapshot_storage_limit_bytes=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	archive := exportPortableSnapshotArchive(t, h, "viewer")
	response := importPortableSnapshotArchive(t, h, "importer", archive, "true")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "snapshot_storage_limit_reached") {
		t.Fatalf("storage quota failure was not explicit: %d %s", response.Code, response.Body)
	}
	var bookmarks, pins int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id='importer'`).Scan(&bookmarks); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins p JOIN recipe_bookmarks b ON b.id=p.bookmark_id WHERE b.user_id='importer'`).Scan(&pins); err != nil {
		t.Fatal(err)
	}
	if bookmarks != 0 || pins != 0 {
		t.Fatalf("quota failure left partial bookmark/snapshot rows: bookmarks=%d pins=%d", bookmarks, pins)
	}
}
