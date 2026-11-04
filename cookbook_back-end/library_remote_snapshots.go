package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
	"github.com/common-fate/httpsig/contentdigest"
)

const (
	remoteSnapshotGrantPurpose     = "personal_private_retention"
	maxRemoteSnapshotCaptureBytes  = int64(8 << 20)
	maxRemoteSnapshotsPerAccount   = 1000
	maxRemoteSnapshotBytesPerUser  = int64(64 << 20)
	defaultRemoteSnapshotStoreSize = int64(256 << 20)
)

var (
	errRemoteSnapshotNotEligible   = errors.New("remote snapshot is not eligible")
	errRemoteSnapshotEvidence      = errors.New("remote snapshot evidence is invalid")
	errRemoteSnapshotTooLarge      = errors.New("remote snapshot capture is too large")
	errRemoteSnapshotAccountCap    = errors.New("remote snapshot account limit reached")
	errRemoteSnapshotStoreCap      = errors.New("remote snapshot storage limit reached")
	errRemoteSnapshotNotFound      = errors.New("remote snapshot not found")
	errRemoteSnapshotRestricted    = errors.New("remote snapshot is restricted")
	errRemoteSnapshotRemoved       = errors.New("remote snapshot was removed")
	errRemoteSnapshotAlreadyPinned = errors.New("remote snapshot pin already exists")
)

const libraryRemoteSnapshotsDDL = `ALTER TABLE instance_settings ADD COLUMN private_snapshot_storage_limit_bytes INTEGER NOT NULL DEFAULT 268435456 CHECK(private_snapshot_storage_limit_bytes>0);
CREATE TABLE library_remote_snapshot_versions (
	version_id TEXT PRIMARY KEY,
	object_uri TEXT NOT NULL,
	source_origin TEXT NOT NULL,
	source_recipe_id TEXT NOT NULL,
	sequence INTEGER CHECK(sequence IS NULL OR sequence>0),
	payload_json BLOB NOT NULL CHECK(length(payload_json) BETWEEN 1 AND 1500000),
	payload_sha256 BLOB NOT NULL CHECK(length(payload_sha256)=32),
	creator_name TEXT NOT NULL CHECK(length(trim(creator_name)) BETWEEN 1 AND 200),
	received_at TEXT NOT NULL,
	event_id TEXT,
	event_type TEXT,
	provenance_state TEXT NOT NULL CHECK(provenance_state IN ('publisher_verified','user_imported_unverified')),
	trusted_receipt_id TEXT,
	grant_purpose TEXT,
	grant_retention TEXT,
	grant_portability TEXT,
	event_body BLOB,
	event_body_sha256 BLOB,
	signature_input TEXT,
	signature_value TEXT,
	request_method TEXT,
	request_target TEXT,
	covered_components TEXT,
	covered_values TEXT,
	content_digest TEXT,
	authenticated_origin TEXT,
	key_id TEXT,
	public_key BLOB,
	key_status_at_receipt TEXT,
	key_created_at TEXT,
	profile_at_receipt TEXT,
	accepted_at TEXT,
	evidence_bytes INTEGER,
	encoded_capture_bytes INTEGER NOT NULL CHECK(encoded_capture_bytes>0 AND encoded_capture_bytes<=8388608),
	title TEXT NOT NULL,
	description TEXT NOT NULL,
	tags_json TEXT NOT NULL,
	untrusted_claim BLOB,
	CHECK((provenance_state='publisher_verified'
		AND sequence IS NOT NULL AND sequence>0
		AND event_id IS NOT NULL AND length(event_id)>0
		AND event_type IN ('recipe.snapshot','recipe.update')
		AND trusted_receipt_id IS NOT NULL AND length(trusted_receipt_id)>0
		AND grant_purpose='personal_private_retention'
		AND grant_retention='continues_after_ordinary_withdrawal'
		AND grant_portability='same_person_personal_account_only'
		AND event_body IS NOT NULL AND length(event_body) BETWEEN 1 AND 2097152
		AND length(event_body_sha256)=32
		AND signature_input IS NOT NULL AND length(signature_input)>0
		AND signature_value IS NOT NULL AND length(signature_value)>0
		AND request_method='POST' AND request_target='/api/federation/inbox'
		AND covered_components IS NOT NULL AND length(covered_components)>0
		AND covered_values IS NOT NULL AND length(covered_values)>0
		AND content_digest IS NOT NULL AND length(content_digest)>0
		AND authenticated_origin=source_origin AND key_id IS NOT NULL AND length(key_id)>0
		AND length(public_key)=32 AND key_status_at_receipt='active'
		AND key_created_at IS NOT NULL AND profile_at_receipt='exchange-v2-retention'
		AND accepted_at IS NOT NULL AND evidence_bytes BETWEEN 1 AND 65536
		AND untrusted_claim IS NULL)
		OR
		(provenance_state='user_imported_unverified'
		AND trusted_receipt_id IS NULL AND grant_purpose IS NULL AND grant_retention IS NULL
		AND grant_portability IS NULL AND event_body IS NULL AND event_body_sha256 IS NULL
		AND signature_input IS NULL AND signature_value IS NULL AND request_method IS NULL
		AND request_target IS NULL AND covered_components IS NULL AND covered_values IS NULL
		AND content_digest IS NULL AND authenticated_origin IS NULL AND key_id IS NULL
		AND public_key IS NULL AND key_status_at_receipt IS NULL AND key_created_at IS NULL
		AND profile_at_receipt IS NULL AND accepted_at IS NULL AND evidence_bytes IS NULL))
);
CREATE INDEX library_remote_snapshot_versions_source ON library_remote_snapshot_versions(object_uri,sequence,payload_sha256);
CREATE TABLE library_remote_snapshot_pins (
	bookmark_id TEXT PRIMARY KEY REFERENCES recipe_bookmarks(id) ON DELETE CASCADE,
	version_id TEXT UNIQUE REFERENCES library_remote_snapshot_versions(version_id) ON DELETE RESTRICT,
	state TEXT NOT NULL CHECK(state IN ('active','restricted','removed')),
	created_at TEXT NOT NULL,
	removed_version_id TEXT,
	moderated_by TEXT,
	moderated_at TEXT,
	moderation_reason TEXT CHECK(moderation_reason IS NULL OR moderation_reason IN ('legal','security','privacy','policy')),
	moderation_outcome TEXT CHECK(moderation_outcome IS NULL OR moderation_outcome IN ('restricted','removed')),
	CHECK((state IN ('active','restricted') AND version_id IS NOT NULL AND removed_version_id IS NULL)
		OR (state='removed' AND version_id IS NULL)),
	CHECK((moderation_outcome IS NULL AND moderated_by IS NULL AND moderated_at IS NULL AND moderation_reason IS NULL)
		OR (moderation_outcome=state AND moderated_by IS NOT NULL AND moderated_at IS NOT NULL AND moderation_reason IS NOT NULL))
);
CREATE TRIGGER library_remote_snapshot_pin_remote_bookmark_insert
BEFORE INSERT ON library_remote_snapshot_pins
WHEN NOT EXISTS(SELECT 1 FROM recipe_bookmarks WHERE id=NEW.bookmark_id AND target_kind='remote')
BEGIN SELECT RAISE(ABORT,'snapshot pin requires remote bookmark'); END;
CREATE TRIGGER library_remote_snapshot_pin_remote_bookmark_update
BEFORE UPDATE OF bookmark_id ON library_remote_snapshot_pins
WHEN NOT EXISTS(SELECT 1 FROM recipe_bookmarks WHERE id=NEW.bookmark_id AND target_kind='remote')
BEGIN SELECT RAISE(ABORT,'snapshot pin requires remote bookmark'); END;
CREATE TRIGGER library_remote_snapshot_pin_cleanup_after_delete
AFTER DELETE ON library_remote_snapshot_pins
WHEN OLD.version_id IS NOT NULL
BEGIN DELETE FROM library_remote_snapshot_versions WHERE version_id=OLD.version_id AND NOT EXISTS(SELECT 1 FROM library_remote_snapshot_pins WHERE version_id=OLD.version_id); END;
CREATE TRIGGER library_remote_snapshot_pin_cleanup_after_update
AFTER UPDATE OF version_id ON library_remote_snapshot_pins
WHEN OLD.version_id IS NOT NULL AND OLD.version_id IS NOT NEW.version_id
BEGIN DELETE FROM library_remote_snapshot_versions WHERE version_id=OLD.version_id AND NOT EXISTS(SELECT 1 FROM library_remote_snapshot_pins WHERE version_id=OLD.version_id); END`

func applyLibraryRemoteSnapshotsMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, libraryRemoteSnapshotsDDL); err != nil {
		return fmt.Errorf("create private remote snapshot schema: %w", err)
	}
	return nil
}

type remoteSnapshotReceipt struct {
	ReceiptID, ObjectURI, SourceOrigin, SourceRecipeID string
	EventID, EventType, CreatorName, ReceivedAt        string
	Payload, EventBody, EventBodyDigest, PayloadDigest []byte
	Sequence                                           int64
	GrantPurpose, GrantRetention, GrantPortability     string
	SignatureInput, SignatureValue, RequestMethod      string
	RequestTarget, CoveredComponents, CoveredValues    string
	ContentDigest, AuthenticatedOrigin, KeyID          string
	PublicKey                                          []byte
	KeyStatus, KeyCreatedAt, Profile, AcceptedAt       string
	EvidenceBytes                                      int64
}

type remoteSnapshotVersion struct {
	VersionID, ObjectURI, SourceOrigin, SourceRecipeID string
	CreatorName, ReceivedAt                            string
	EventID, EventType                                 sql.NullString
	Sequence                                           sql.NullInt64
	Payload, PayloadDigest                             []byte
	UntrustedClaim                                     []byte
	Title, Description, TagsJSON                       string
	Provenance                                         string
	SourceState                                        string
	GrantPurpose, GrantRetention, GrantPortability     string
	EncodedBytes                                       int64
	Evidence                                           remoteSnapshotEvidence
}

type PrivateSnapshotRecipeReadView struct {
	ObjectURI      string              `json:"object_uri"`
	SourceOrigin   string              `json:"source_origin"`
	SourceRecipeID string              `json:"source_recipe_id"`
	CreatorName    string              `json:"creator_name"`
	Recipe         RemoteRecipeContent `json:"recipe"`
	Sequence       *int64              `json:"sequence,omitempty"`
	UpdatedAt      string              `json:"updated_at"`
}

type remoteSnapshotEvidence struct {
	ReceiptID, EventBody, EventBodyDigest             []byte
	SignatureInput, SignatureValue                    string
	RequestMethod, RequestTarget, CoveredComponents   string
	CoveredValues, ContentDigest, AuthenticatedOrigin string
	KeyID, PublicKey, KeyStatus, KeyCreatedAt         string
	Profile, AcceptedAt                               string
	EvidenceBytes                                     int64
}

type remoteSnapshotPayloadView struct {
	Recipe         RemoteRecipeContent `json:"recipe"`
	ObjectURI      string              `json:"object_uri"`
	SourceOrigin   string              `json:"source_origin"`
	SourceRecipeID string              `json:"source_recipe_id"`
	CreatorName    string              `json:"creator_name"`
	Sequence       *int64              `json:"sequence,omitempty"`
	ReceivedAt     string              `json:"received_at"`
	VersionID      string              `json:"version_id"`
	Provenance     string              `json:"provenance_state"`
	RetentionClaim string              `json:"publisher_retention_claim,omitempty"`
	SourceState    string              `json:"source_state"`
}

func parseRemoteSnapshotPayload(raw []byte) (RemoteRecipeContent, error) {
	var content RemoteRecipeContent
	if len(raw) == 0 || len(raw) > exchangev2.MaxPayloadBytes || decodeRemoteRecipePayload(raw, &content) != nil {
		return RemoteRecipeContent{}, errRemoteSnapshotEvidence
	}
	return content, nil
}

func remoteSnapshotSequenceValue(sequence sql.NullInt64) *int64 {
	if !sequence.Valid {
		return nil
	}
	value := sequence.Int64
	return &value
}

func validateStoredRemoteSnapshot(item remoteSnapshotVersion) error {
	origin, recipeID, err := parseFederationRecipeObjectURI(item.ObjectURI)
	if err != nil || origin != item.SourceOrigin || recipeID != item.SourceRecipeID {
		return errRemoteSnapshotEvidence
	}
	if _, err := parseRemoteSnapshotPayload(item.Payload); err != nil || len(item.PayloadDigest) != sha256.Size || !bytesEqual(item.PayloadDigest, sha256Digest(item.Payload)) {
		return errRemoteSnapshotEvidence
	}
	if item.Sequence.Valid && item.Sequence.Int64 <= 0 {
		return errRemoteSnapshotEvidence
	}
	if item.Provenance == "user_imported_unverified" {
		return nil
	}
	if item.Provenance != "publisher_verified" || !item.Sequence.Valid || !item.EventID.Valid || !item.EventType.Valid ||
		item.EventType.String != exchangev2.RecipeSnapshot && item.EventType.String != exchangev2.RecipeUpdate {
		return errRemoteSnapshotEvidence
	}
	evidence := item.Evidence
	if len(evidence.ReceiptID) == 0 || len(evidence.EventBody) == 0 || len(evidence.EventBodyDigest) != sha256.Size ||
		!bytesEqual(evidence.EventBodyDigest, sha256Digest(evidence.EventBody)) || len(evidence.PublicKey) != 32 ||
		evidence.RequestMethod != "POST" || evidence.RequestTarget != "/api/federation/inbox" ||
		evidence.AuthenticatedOrigin != item.SourceOrigin || evidence.KeyID == "" || evidence.SignatureInput == "" || evidence.SignatureValue == "" {
		return errRemoteSnapshotEvidence
	}
	event, err := exchangev2.Decode(evidence.EventBody)
	if err != nil || event.EventID != item.EventID.String || event.EventType != item.EventType.String || event.SourceOrigin != item.SourceOrigin ||
		event.ObjectID != item.ObjectURI || event.Sequence != item.Sequence.Int64 || !bytesEqual(event.Payload, item.Payload) ||
		event.PayloadSHA256 != hex.EncodeToString(item.PayloadDigest) || event.Attribution == nil || strings.TrimSpace(event.Attribution.CreatorName) != strings.TrimSpace(item.CreatorName) ||
		event.RetentionGrant == nil || event.RetentionGrant.Purpose != item.GrantPurpose || event.RetentionGrant.Retention != item.GrantRetention ||
		event.RetentionGrant.Portability != item.GrantPortability || event.RetentionGrant.ObjectID != item.ObjectURI ||
		event.RetentionGrant.Sequence != item.Sequence.Int64 || event.RetentionGrant.PayloadSHA256 != event.PayloadSHA256 {
		return errRemoteSnapshotEvidence
	}
	components, values, err := decodeRetentionCoveredValues(evidence.CoveredComponents, evidence.CoveredValues)
	if err != nil {
		return errRemoteSnapshotEvidence
	}
	covered := make(map[string]string, len(components))
	for index, component := range components {
		covered[component] = values[index]
	}
	target, targetErr := url.Parse(covered["@target-uri"])
	if targetErr != nil || target == nil {
		return errRemoteSnapshotEvidence
	}
	targetOrigin, originErr := canonicalFederationOrigin(target.Scheme + "://" + target.Host)
	if originErr != nil || targetOrigin != target.Scheme+"://"+target.Host || target.Path != evidence.RequestTarget ||
		target.RawQuery != "" || target.Fragment != "" || covered["@method"] != evidence.RequestMethod ||
		covered["content-digest"] != evidence.ContentDigest || evidence.ContentDigest == "" {
		return errRemoteSnapshotEvidence
	}
	request, err := http.NewRequest(http.MethodPost, covered["@target-uri"], strings.NewReader(string(evidence.EventBody)))
	if err != nil {
		return errRemoteSnapshotEvidence
	}
	request.ContentLength = int64(len(evidence.EventBody))
	computedContentDigest, err := contentdigest.SHA256.HashRequest(nil, request)
	if err != nil || computedContentDigest != evidence.ContentDigest {
		return errRemoteSnapshotEvidence
	}
	return nil
}

func remoteSnapshotCaptureBytes(item remoteSnapshotReceipt, content RemoteRecipeContent) int64 {
	tagsJSON, _ := jsonMarshalCanonicalTags(content.Tags)
	textBytes := len(item.ReceiptID) + len(item.ObjectURI) + len(item.SourceOrigin) + len(item.SourceRecipeID) +
		len(item.EventID) + len(item.EventType) + len(item.CreatorName) + len(item.ReceivedAt) +
		len(item.GrantPurpose) + len(item.GrantRetention) + len(item.GrantPortability) +
		len(item.SignatureInput) + len(item.SignatureValue) + len(item.RequestMethod) + len(item.RequestTarget) +
		len(item.CoveredComponents) + len(item.CoveredValues) + len(item.ContentDigest) + len(item.AuthenticatedOrigin) +
		len(item.KeyID) + len(item.KeyStatus) + len(item.KeyCreatedAt) + len(item.Profile) + len(item.AcceptedAt) +
		len(content.Title) + len(content.Description) + len(tagsJSON)
	blobBytes := len(item.Payload) + len(item.PayloadDigest) + len(item.EventBody) + len(item.EventBodyDigest) + len(item.PublicKey)
	return int64(textBytes + blobBytes + 8*6)
}

func jsonMarshalCanonicalTags(tags []string) (string, error) {
	encoded, err := json.Marshal(tags)
	return string(encoded), err
}

func nullableSnapshotSequence(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullableSnapshotFact(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func captureRemoteSnapshot(ctx context.Context, tx sqliteExecutor, accountID, bookmarkID string, bookmarkCreatedAt string) (remoteSnapshotVersion, error) {
	var objectURI string
	if err := tx.QueryRowContext(ctx, `SELECT remote_object_uri FROM recipe_bookmarks WHERE id=? AND user_id=? AND target_kind='remote'`, bookmarkID, accountID).Scan(&objectURI); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return remoteSnapshotVersion{}, errRemoteSnapshotNotFound
		}
		return remoteSnapshotVersion{}, err
	}
	var state string
	var priorVersion sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state,version_id FROM library_remote_snapshot_pins WHERE bookmark_id=?`, bookmarkID).Scan(&state, &priorVersion); err == nil {
		switch state {
		case "active":
			return readRemoteSnapshotForBookmark(ctx, tx, accountID, bookmarkID)
		case "restricted":
			return remoteSnapshotVersion{}, errRemoteSnapshotRestricted
		case "removed":
			return remoteSnapshotVersion{}, errRemoteSnapshotRemoved
		default:
			return remoteSnapshotVersion{}, errRemoteSnapshotEvidence
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return remoteSnapshotVersion{}, err
	}
	receipt, err := readEligibleFederationRetentionReceipt(ctx, tx, objectURI)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	content, err := parseRemoteSnapshotPayload(receipt.Payload)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	encodedBytes := remoteSnapshotCaptureBytes(receipt, content)
	if encodedBytes > maxRemoteSnapshotCaptureBytes {
		return remoteSnapshotVersion{}, errRemoteSnapshotTooLarge
	}
	if err := checkRemoteSnapshotQuota(ctx, tx, accountID, encodedBytes); err != nil {
		return remoteSnapshotVersion{}, err
	}
	versionID := newID()
	tagsJSON, err := jsonMarshalCanonicalTags(content.Tags)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO library_remote_snapshot_versions(
		version_id,object_uri,source_origin,source_recipe_id,sequence,payload_json,payload_sha256,creator_name,received_at,event_id,event_type,provenance_state,trusted_receipt_id,
		grant_purpose,grant_retention,grant_portability,event_body,event_body_sha256,signature_input,signature_value,request_method,request_target,covered_components,covered_values,content_digest,
		authenticated_origin,key_id,public_key,key_status_at_receipt,key_created_at,profile_at_receipt,accepted_at,evidence_bytes,encoded_capture_bytes,title,description,tags_json)
		VALUES(?,?,?,?,?,?,?,?,?,? ,?,'publisher_verified',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		versionID, receipt.ObjectURI, receipt.SourceOrigin, receipt.SourceRecipeID, receipt.Sequence, receipt.Payload, receipt.PayloadDigest,
		receipt.CreatorName, receipt.ReceivedAt, receipt.EventID, receipt.EventType, receipt.ReceiptID,
		receipt.GrantPurpose, receipt.GrantRetention, receipt.GrantPortability, receipt.EventBody, receipt.EventBodyDigest,
		receipt.SignatureInput, receipt.SignatureValue, receipt.RequestMethod, receipt.RequestTarget, receipt.CoveredComponents,
		receipt.CoveredValues, receipt.ContentDigest, receipt.AuthenticatedOrigin, receipt.KeyID, receipt.PublicKey,
		receipt.KeyStatus, receipt.KeyCreatedAt, receipt.Profile, receipt.AcceptedAt, receipt.EvidenceBytes,
		encodedBytes, content.Title, content.Description, tagsJSON)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO library_remote_snapshot_pins(bookmark_id,version_id,state,created_at) VALUES(?,?,'active',?)`, bookmarkID, versionID, bookmarkCreatedAt)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	return remoteSnapshotVersion{
		VersionID: versionID, ObjectURI: receipt.ObjectURI, SourceOrigin: receipt.SourceOrigin,
		SourceRecipeID: receipt.SourceRecipeID, EventID: sql.NullString{String: receipt.EventID, Valid: true}, EventType: sql.NullString{String: receipt.EventType, Valid: true},
		CreatorName: receipt.CreatorName, ReceivedAt: receipt.ReceivedAt, Sequence: sql.NullInt64{Int64: receipt.Sequence, Valid: true},
		Payload: append([]byte(nil), receipt.Payload...), PayloadDigest: append([]byte(nil), receipt.PayloadDigest...),
		Title: content.Title, Description: content.Description, TagsJSON: tagsJSON, Provenance: "publisher_verified",
		GrantPurpose: receipt.GrantPurpose, GrantRetention: receipt.GrantRetention,
		GrantPortability: receipt.GrantPortability, EncodedBytes: encodedBytes,
		Evidence: snapshotEvidenceFromReceipt(receipt),
	}, nil
}

// persistUserImportedUnverifiedSnapshot is the narrow transaction seam for a
// later Operations archive adapter. The caller must have already established
// same-person import eligibility. This function binds data to the owner's
// existing remote bookmark, never reads or upgrades archive evidence, and
// leaves any existing frozen or moderated pin untouched.
func persistUserImportedUnverifiedSnapshot(ctx context.Context, tx sqliteExecutor, accountID, bookmarkID string, sourceSequence sql.NullInt64, sourceEventID, sourceEventType sql.NullString, creatorName string, payload, untrustedClaim []byte, importedAt string) (remoteSnapshotVersion, error) {
	var objectURI, createdAt string
	if err := tx.QueryRowContext(ctx, `SELECT remote_object_uri,created_at FROM recipe_bookmarks WHERE id=? AND user_id=? AND target_kind='remote'`, bookmarkID, accountID).Scan(&objectURI, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return remoteSnapshotVersion{}, errRemoteSnapshotNotFound
		}
		return remoteSnapshotVersion{}, err
	}
	origin, recipeID, err := parseFederationRecipeObjectURI(objectURI)
	if err != nil || (sourceSequence.Valid && sourceSequence.Int64 <= 0) || strings.TrimSpace(creatorName) == "" || len(creatorName) > 200 ||
		(sourceEventID.Valid && (sourceEventID.String == "" || len(sourceEventID.String) > 200)) ||
		(sourceEventType.Valid && (sourceEventType.String == "" || len(sourceEventType.String) > 200)) {
		return remoteSnapshotVersion{}, errRemoteSnapshotEvidence
	}
	if len(untrustedClaim) > int(maxRemoteSnapshotCaptureBytes) {
		return remoteSnapshotVersion{}, errRemoteSnapshotTooLarge
	}
	if _, err := parseRemoteSnapshotPayload(payload); err != nil {
		return remoteSnapshotVersion{}, err
	}
	var priorState string
	err = tx.QueryRowContext(ctx, `SELECT state FROM library_remote_snapshot_pins WHERE bookmark_id=?`, bookmarkID).Scan(&priorState)
	if err == nil {
		return remoteSnapshotVersion{}, errRemoteSnapshotAlreadyPinned
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return remoteSnapshotVersion{}, err
	}
	content, err := parseRemoteSnapshotPayload(payload)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	tagsJSON, err := jsonMarshalCanonicalTags(content.Tags)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	versionID := newID()
	if importedAt == "" {
		importedAt = timestamp()
	}
	encodedBytes := int64(len(objectURI) + len(origin) + len(recipeID) + len(creatorName) + len(payload) + len(untrustedClaim) + len(importedAt) + len(content.Title) + len(content.Description) + len(tagsJSON) + 8*3)
	if encodedBytes > maxRemoteSnapshotCaptureBytes {
		return remoteSnapshotVersion{}, errRemoteSnapshotTooLarge
	}
	if err := checkRemoteSnapshotQuota(ctx, tx, accountID, encodedBytes); err != nil {
		return remoteSnapshotVersion{}, err
	}
	digest := sha256Digest(payload)
	_, err = tx.ExecContext(ctx, `INSERT INTO library_remote_snapshot_versions(
		version_id,object_uri,source_origin,source_recipe_id,sequence,payload_json,payload_sha256,creator_name,received_at,event_id,event_type,provenance_state,
		encoded_capture_bytes,title,description,tags_json,untrusted_claim)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,'user_imported_unverified',?,?,?,?,?)`,
		versionID, objectURI, origin, recipeID, nullableSnapshotSequence(sourceSequence), payload, digest, creatorName, importedAt, nullableSnapshotFact(sourceEventID), nullableSnapshotFact(sourceEventType),
		encodedBytes, content.Title, content.Description, tagsJSON, untrustedClaim)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO library_remote_snapshot_pins(bookmark_id,version_id,state,created_at) VALUES(?,?,'active',?)`, bookmarkID, versionID, createdAt); err != nil {
		return remoteSnapshotVersion{}, err
	}
	return remoteSnapshotVersion{VersionID: versionID, ObjectURI: objectURI, SourceOrigin: origin, SourceRecipeID: recipeID,
		CreatorName: creatorName, Sequence: sourceSequence, EventID: sourceEventID, EventType: sourceEventType, Payload: append([]byte(nil), payload...), PayloadDigest: digest,
		ReceivedAt: importedAt, Title: content.Title, Description: content.Description, TagsJSON: tagsJSON,
		Provenance: "user_imported_unverified", SourceState: "unknown_legacy", EncodedBytes: encodedBytes}, nil
}

func snapshotEvidenceFromReceipt(receipt remoteSnapshotReceipt) remoteSnapshotEvidence {
	return remoteSnapshotEvidence{
		ReceiptID: []byte(receipt.ReceiptID), EventBody: append([]byte(nil), receipt.EventBody...),
		EventBodyDigest: append([]byte(nil), receipt.EventBodyDigest...), SignatureInput: receipt.SignatureInput,
		SignatureValue: receipt.SignatureValue, RequestMethod: receipt.RequestMethod, RequestTarget: receipt.RequestTarget,
		CoveredComponents: receipt.CoveredComponents, CoveredValues: receipt.CoveredValues, ContentDigest: receipt.ContentDigest,
		AuthenticatedOrigin: receipt.AuthenticatedOrigin, KeyID: receipt.KeyID, PublicKey: string(receipt.PublicKey),
		KeyStatus: receipt.KeyStatus, KeyCreatedAt: receipt.KeyCreatedAt, Profile: receipt.Profile,
		AcceptedAt: receipt.AcceptedAt, EvidenceBytes: receipt.EvidenceBytes,
	}
}

func checkRemoteSnapshotQuota(ctx context.Context, tx sqliteExecutor, accountID string, nextBytes int64) error {
	var accountCount int64
	var accountBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(v.encoded_capture_bytes),0)
		FROM library_remote_snapshot_pins p JOIN library_remote_snapshot_versions v ON v.version_id=p.version_id
		JOIN recipe_bookmarks b ON b.id=p.bookmark_id WHERE b.user_id=? AND p.state IN ('active','restricted')`, accountID).Scan(&accountCount, &accountBytes); err != nil {
		return err
	}
	if accountCount+1 > maxRemoteSnapshotsPerAccount || accountBytes+nextBytes > maxRemoteSnapshotBytesPerUser {
		return errRemoteSnapshotAccountCap
	}
	var instanceQuota int64
	if err := tx.QueryRowContext(ctx, `SELECT private_snapshot_storage_limit_bytes FROM instance_settings WHERE id=1`).Scan(&instanceQuota); err != nil {
		return err
	}
	var instanceBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(v.encoded_capture_bytes),0) FROM library_remote_snapshot_pins p
		JOIN library_remote_snapshot_versions v ON v.version_id=p.version_id WHERE p.state IN ('active','restricted')`).Scan(&instanceBytes); err != nil {
		return err
	}
	if instanceQuota <= 0 || nextBytes > instanceQuota-instanceBytes {
		return errRemoteSnapshotStoreCap
	}
	return nil
}

func readRemoteSnapshotForBookmark(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, accountID, bookmarkID string) (remoteSnapshotVersion, error) {
	var item remoteSnapshotVersion
	var payloadDigest []byte
	var sourceState string
	var eventBody, eventBodyDigest, receiptID, publicKey []byte
	var signatureInput, signatureValue, requestMethod, requestTarget, coveredComponents, coveredValues sql.NullString
	var contentDigest, authenticatedOrigin, keyID, keyStatus, keyCreatedAt, profile, acceptedAt sql.NullString
	var evidenceBytes sql.NullInt64
	var grantPurpose, grantRetention, grantPortability sql.NullString
	err := queryer.QueryRowContext(ctx, `SELECT v.version_id,v.object_uri,v.source_origin,v.source_recipe_id,v.sequence,v.payload_json,v.payload_sha256,v.creator_name,v.received_at,v.event_id,v.event_type,v.provenance_state,v.title,v.description,v.tags_json,v.encoded_capture_bytes,COALESCE(c.source_state,'unknown_legacy'),
		v.trusted_receipt_id,v.event_body,v.event_body_sha256,v.signature_input,v.signature_value,v.request_method,v.request_target,v.covered_components,v.covered_values,v.content_digest,v.authenticated_origin,v.key_id,v.public_key,v.key_status_at_receipt,v.key_created_at,v.profile_at_receipt,v.accepted_at,v.evidence_bytes,v.grant_purpose,v.grant_retention,v.grant_portability
		FROM recipe_bookmarks b JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id AND p.state='active'
		JOIN library_remote_snapshot_versions v ON v.version_id=p.version_id LEFT JOIN federation_remote_catalog c ON c.object_id=b.remote_object_uri
		WHERE b.id=? AND b.user_id=? AND b.target_kind='remote'`, bookmarkID, accountID).Scan(
		&item.VersionID, &item.ObjectURI, &item.SourceOrigin, &item.SourceRecipeID, &item.Sequence, &item.Payload, &payloadDigest, &item.CreatorName, &item.ReceivedAt, &item.EventID, &item.EventType, &item.Provenance, &item.Title, &item.Description, &item.TagsJSON, &item.EncodedBytes, &sourceState,
		&receiptID, &eventBody, &eventBodyDigest, &signatureInput, &signatureValue, &requestMethod, &requestTarget, &coveredComponents, &coveredValues, &contentDigest, &authenticatedOrigin, &keyID, &publicKey, &keyStatus, &keyCreatedAt, &profile, &acceptedAt, &evidenceBytes, &grantPurpose, &grantRetention, &grantPortability)
	if errors.Is(err, sql.ErrNoRows) {
		return remoteSnapshotVersion{}, errRemoteSnapshotNotFound
	}
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	if sourceState == "" {
		sourceState = "unknown_legacy"
	}
	item.SourceState = sourceState
	item.PayloadDigest = payloadDigest
	if item.Provenance == "publisher_verified" {
		if len(receiptID) == 0 || len(eventBody) == 0 || len(eventBodyDigest) != sha256.Size || len(publicKey) != 32 || !evidenceBytes.Valid {
			return remoteSnapshotVersion{}, errRemoteSnapshotEvidence
		}
		item.Evidence = remoteSnapshotEvidence{ReceiptID: receiptID, EventBody: eventBody, EventBodyDigest: eventBodyDigest,
			SignatureInput: signatureInput.String, SignatureValue: signatureValue.String, RequestMethod: requestMethod.String,
			RequestTarget: requestTarget.String, CoveredComponents: coveredComponents.String, CoveredValues: coveredValues.String,
			ContentDigest: contentDigest.String, AuthenticatedOrigin: authenticatedOrigin.String, KeyID: keyID.String,
			PublicKey: string(publicKey), KeyStatus: keyStatus.String, KeyCreatedAt: keyCreatedAt.String,
			Profile: profile.String, AcceptedAt: acceptedAt.String, EvidenceBytes: evidenceBytes.Int64}
		if grantPurpose.Valid {
			item.GrantPurpose = grantPurpose.String
		}
		if grantRetention.Valid {
			item.GrantRetention = grantRetention.String
		}
		if grantPortability.Valid {
			item.GrantPortability = grantPortability.String
		}
	} else if item.Provenance != "user_imported_unverified" {
		return remoteSnapshotVersion{}, errRemoteSnapshotEvidence
	}
	if err := validateStoredRemoteSnapshot(item); err != nil {
		return remoteSnapshotVersion{}, errRemoteSnapshotEvidence
	}
	return item, nil
}

type remoteSnapshotBookmarkStatus struct {
	State, VersionID, SourceState, Provenance string
	ModeratedAt, Reason, Outcome              string
}

func readRemoteSnapshotBookmarkStates(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, accountID string, bookmarkIDs []string) (map[string]remoteSnapshotBookmarkStatus, error) {
	result := make(map[string]remoteSnapshotBookmarkStatus, len(bookmarkIDs))
	if len(bookmarkIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(bookmarkIDs))
	args := make([]any, 0, len(bookmarkIDs)+1)
	args = append(args, accountID)
	for i, id := range bookmarkIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := queryer.QueryContext(ctx, `SELECT b.id,COALESCE(p.state,''),COALESCE(p.version_id,p.removed_version_id,''),COALESCE(c.source_state,'unknown_legacy'),COALESCE(v.provenance_state,''),COALESCE(p.moderated_at,''),COALESCE(p.moderation_reason,''),COALESCE(p.moderation_outcome,'')
		FROM recipe_bookmarks b LEFT JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id
		LEFT JOIN library_remote_snapshot_versions v ON v.version_id=p.version_id
		LEFT JOIN federation_remote_catalog c ON c.object_id=b.remote_object_uri
		WHERE b.user_id=? AND b.target_kind='remote' AND b.id IN (`+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var state remoteSnapshotBookmarkStatus
		if err := rows.Scan(&id, &state.State, &state.VersionID, &state.SourceState, &state.Provenance, &state.ModeratedAt, &state.Reason, &state.Outcome); err != nil {
			return nil, err
		}
		result[id] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readRemoteSnapshotRecipesByBookmark(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, accountID string, bookmarkIDs []string) (map[string]PrivateSnapshotRecipeReadView, error) {
	result := make(map[string]PrivateSnapshotRecipeReadView, len(bookmarkIDs))
	if len(bookmarkIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(bookmarkIDs))
	args := make([]any, 0, len(bookmarkIDs)+1)
	args = append(args, accountID)
	for i, id := range bookmarkIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := queryer.QueryContext(ctx, `SELECT b.id,v.version_id,v.object_uri,v.source_origin,v.source_recipe_id,v.sequence,v.payload_json,v.payload_sha256,v.creator_name,v.received_at,v.provenance_state
		FROM recipe_bookmarks b JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id AND p.state='active'
		JOIN library_remote_snapshot_versions v ON v.version_id=p.version_id
		WHERE b.user_id=? AND b.target_kind='remote' AND b.id IN (`+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bookmarkID, versionID, objectURI, origin, recipeID, creator, receivedAt, provenance string
		var sequence sql.NullInt64
		var payload, payloadDigest []byte
		if err := rows.Scan(&bookmarkID, &versionID, &objectURI, &origin, &recipeID, &sequence, &payload, &payloadDigest, &creator, &receivedAt, &provenance); err != nil {
			return nil, err
		}
		parsedOrigin, parsedRecipeID, parseErr := parseFederationRecipeObjectURI(objectURI)
		content, decodeErr := parseRemoteSnapshotPayload(payload)
		if parseErr != nil || decodeErr != nil || parsedOrigin != origin || parsedRecipeID != recipeID || (sequence.Valid && sequence.Int64 <= 0) || creator == "" ||
			(provenance != "publisher_verified" && provenance != "user_imported_unverified") || !bytesEqual(payloadDigest, sha256Digest(payload)) {
			return nil, errRemoteSnapshotEvidence
		}
		result[bookmarkID] = PrivateSnapshotRecipeReadView{
			ObjectURI: objectURI, SourceOrigin: origin, SourceRecipeID: recipeID, CreatorName: creator,
			Recipe: content, Sequence: remoteSnapshotSequenceValue(sequence), UpdatedAt: receivedAt,
		}
		_ = versionID // version identity is projected separately on the bookmark item.
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// readOwnedActiveSnapshotForExport is the internal owner-scoped full capture
// seam for the separately leased portability implementation. It is not an HTTP
// DTO and must only be called after owner/export authorization.
func readOwnedActiveSnapshotForExport(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, accountID, bookmarkID string) (remoteSnapshotVersion, error) {
	var state string
	err := queryer.QueryRowContext(ctx, `SELECT COALESCE(p.state,'') FROM recipe_bookmarks b
		LEFT JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id
		WHERE b.id=? AND b.user_id=? AND b.target_kind='remote'`, bookmarkID, accountID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return remoteSnapshotVersion{}, errRemoteSnapshotNotFound
	}
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	if state == "restricted" {
		return remoteSnapshotVersion{}, errRemoteSnapshotRestricted
	}
	if state == "removed" {
		return remoteSnapshotVersion{}, errRemoteSnapshotRemoved
	}
	if state != "active" {
		return remoteSnapshotVersion{}, errRemoteSnapshotNotFound
	}
	item, err := readRemoteSnapshotForBookmark(ctx, queryer, accountID, bookmarkID)
	if err != nil {
		return remoteSnapshotVersion{}, err
	}
	var claim []byte
	if err := queryer.QueryRowContext(ctx, `SELECT v.untrusted_claim FROM recipe_bookmarks b
		JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id AND p.state='active'
		JOIN library_remote_snapshot_versions v ON v.version_id=p.version_id
		WHERE b.id=? AND b.user_id=? AND b.target_kind='remote'`, bookmarkID, accountID).Scan(&claim); err != nil {
		return remoteSnapshotVersion{}, err
	}
	item.UntrustedClaim = append([]byte(nil), claim...)
	return item, nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sha256Digest(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}

func remoteSnapshotSearchMatch(query string, title, description, tags string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(sqliteASCIILower(title+" "+description+" "+tags), sqliteASCIILower(query))
}
