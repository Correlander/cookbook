package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

const (
	federationExchangeV1                       = "v1"
	federationExchangeV2Retention              = "exchange-v2-retention"
	federationRetentionReceiptMaxRows          = int64(256)
	federationRetentionReceiptMaxBytes         = int64(64 << 20)
	federationRetentionReceiptMaxEvidenceBytes = 64 << 10
)

var (
	errFederationRetentionCapacity = errors.New("federation retention receipt capacity exceeded")
	errFederationRetentionEvidence = errors.New("invalid federation retention evidence")
)

const federationRetentionReceiptsDDL = `ALTER TABLE federation_peers ADD COLUMN exchange_profile TEXT NOT NULL DEFAULT 'v1' CHECK(exchange_profile IN ('v1','exchange-v2-retention'));
ALTER TABLE federation_remote_catalog ADD COLUMN source_state TEXT NOT NULL DEFAULT 'unknown_legacy' CHECK(source_state IN ('published','withdrawn','disconnected','unknown_legacy'));
UPDATE federation_remote_catalog SET source_state=CASE WHEN state_known=1 AND deleted=0 THEN 'published' ELSE 'unknown_legacy' END;
UPDATE federation_remote_catalog SET logical_bytes=logical_bytes+length(CAST(source_state AS BLOB));
CREATE TABLE federation_retention_receipts (
 receipt_id TEXT NOT NULL UNIQUE,
 source_origin TEXT NOT NULL,
 object_id TEXT NOT NULL,
 event_id TEXT NOT NULL,
 sequence INTEGER NOT NULL CHECK(sequence>0),
 body_digest BLOB NOT NULL CHECK(length(body_digest)=32),
 event_body BLOB NOT NULL CHECK(length(event_body)<=2097152),
 signature_input TEXT NOT NULL,
 signature_value TEXT NOT NULL,
 request_method TEXT NOT NULL,
 request_target TEXT NOT NULL,
 covered_components TEXT NOT NULL,
 covered_values TEXT NOT NULL,
 authenticated_origin TEXT NOT NULL,
 key_id TEXT NOT NULL,
 public_key BLOB NOT NULL CHECK(length(public_key)=32),
 key_status_at_receipt TEXT NOT NULL CHECK(key_status_at_receipt='active'),
 key_created_at TEXT NOT NULL,
 profile_at_receipt TEXT NOT NULL CHECK(profile_at_receipt='exchange-v2-retention'),
 accepted_at TEXT NOT NULL,
 evidence_bytes INTEGER NOT NULL CHECK(evidence_bytes BETWEEN 1 AND 65536),
 encoded_bytes INTEGER NOT NULL CHECK(encoded_bytes>0),
	PRIMARY KEY(source_origin,object_id),
	UNIQUE(source_origin,event_id)
);
CREATE INDEX federation_retention_receipts_origin ON federation_retention_receipts(source_origin,object_id)`

func applyFederationRetentionReceiptsMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, federationRetentionReceiptsDDL); err != nil {
		return fmt.Errorf("create federation retention receipt schema: %w", err)
	}
	return nil
}

// federationRetentionEvidence is internal verified-request evidence. This persistence
// helper checks its binding to current database facts; it does not verify signatures.
type federationRetentionEvidence struct {
	ReceiptID           string
	EventBody           []byte
	SignatureInput      string
	SignatureValue      string
	RequestMethod       string
	RequestTarget       string
	CoveredComponents   string
	CoveredValues       string
	AuthenticatedOrigin string
	KeyID               string
}

func (e federationRetentionEvidence) evidenceBytes() int64 {
	return int64(len(e.SignatureInput) + len(e.SignatureValue) + len(e.RequestMethod) + len(e.RequestTarget) + len(e.CoveredComponents) + len(e.CoveredValues) + len(e.AuthenticatedOrigin) + len(e.KeyID))
}

func retentionReceiptEncodedBytes(e federationRetentionEvidence, event exchangev2.Event, acceptedAt, keyCreated string, publicKey []byte) int64 {
	return int64(len(e.ReceiptID) + len(event.SourceOrigin) + len(event.ObjectID) + len(event.EventID) + len(fmt.Sprint(event.Sequence)) + len(e.EventBody) + 32 + len(e.SignatureInput) + len(e.SignatureValue) + len(e.RequestMethod) + len(e.RequestTarget) + len(e.CoveredComponents) + len(e.CoveredValues) + len(e.AuthenticatedOrigin) + len(e.KeyID) + len(publicKey) + len("active") + len(keyCreated) + len(federationExchangeV2Retention) + len(acceptedAt) + len(fmt.Sprint(e.evidenceBytes()+int64(len(publicKey)+len("active")+len(keyCreated)+len(federationExchangeV2Retention)))))
}

func admitFederationRetentionReceipt(ctx context.Context, tx sqliteExecutor, evidence federationRetentionEvidence) error {
	if evidence.ReceiptID == "" || len(evidence.ReceiptID) > 200 || len(evidence.EventBody) == 0 || len(evidence.EventBody) > exchangev2.MaxEventBytes || evidence.RequestMethod != "POST" || evidence.RequestTarget != "/api/federation/inbox" || evidence.SignatureInput == "" || evidence.SignatureValue == "" || evidence.CoveredComponents == "" || evidence.CoveredValues == "" || evidence.KeyID == "" {
		return errFederationRetentionEvidence
	}
	event, err := exchangev2.Decode(evidence.EventBody)
	if err != nil || event.RetentionGrant == nil || event.SourceOrigin != evidence.AuthenticatedOrigin || event.ObjectID != event.RetentionGrant.ObjectID || event.Sequence != event.RetentionGrant.Sequence || event.PayloadSHA256 != event.RetentionGrant.PayloadSHA256 {
		return errFederationRetentionEvidence
	}
	digest := sha256.Sum256(evidence.EventBody)
	var profile, keyOrigin, keyStatus, keyCreated string
	var peerEnabled int
	var publicKey []byte
	if err := tx.QueryRowContext(ctx, `SELECT p.exchange_profile,p.enabled,k.origin,k.public_key,k.status,k.created_at FROM federation_peers p JOIN federation_peer_keys k ON k.origin=p.origin WHERE p.origin=? AND k.key_id=?`, event.SourceOrigin, evidence.KeyID).Scan(&profile, &peerEnabled, &keyOrigin, &publicKey, &keyStatus, &keyCreated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errFederationRetentionEvidence
		}
		return err
	}
	if profile != federationExchangeV2Retention || peerEnabled != 1 || keyOrigin != event.SourceOrigin || keyStatus != "active" || evidence.AuthenticatedOrigin != event.SourceOrigin {
		return errFederationRetentionEvidence
	}
	evidenceBytes := evidence.evidenceBytes() + int64(len(publicKey)+len(keyStatus)+len(keyCreated)+len(profile))
	if evidenceBytes > federationRetentionReceiptMaxEvidenceBytes {
		return errFederationRetentionEvidence
	}
	var catalogSequence int64
	var deleted int
	if err := tx.QueryRowContext(ctx, `SELECT high_water_sequence,deleted FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, event.SourceOrigin, event.ObjectID).Scan(&catalogSequence, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errFederationRetentionEvidence
		}
		return err
	}
	if catalogSequence != event.Sequence || deleted != 0 {
		return errFederationRetentionEvidence
	}
	accepted := time.Now().UTC().Format(time.RFC3339Nano)
	encoded := retentionReceiptEncodedBytes(evidence, event, accepted, keyCreated, publicKey)
	if err := checkFederationRetentionReceiptCapacity(ctx, tx, event.SourceOrigin, event.ObjectID, encoded); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO federation_retention_receipts(receipt_id,source_origin,object_id,event_id,sequence,body_digest,event_body,signature_input,signature_value,request_method,request_target,covered_components,covered_values,authenticated_origin,key_id,public_key,key_status_at_receipt,key_created_at,profile_at_receipt,accepted_at,evidence_bytes,encoded_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source_origin,object_id) DO UPDATE SET receipt_id=excluded.receipt_id,event_id=excluded.event_id,sequence=excluded.sequence,body_digest=excluded.body_digest,event_body=excluded.event_body,signature_input=excluded.signature_input,signature_value=excluded.signature_value,request_method=excluded.request_method,request_target=excluded.request_target,covered_components=excluded.covered_components,covered_values=excluded.covered_values,authenticated_origin=excluded.authenticated_origin,key_id=excluded.key_id,public_key=excluded.public_key,key_status_at_receipt=excluded.key_status_at_receipt,key_created_at=excluded.key_created_at,profile_at_receipt=excluded.profile_at_receipt,accepted_at=excluded.accepted_at,evidence_bytes=excluded.evidence_bytes,encoded_bytes=excluded.encoded_bytes`, evidence.ReceiptID, event.SourceOrigin, event.ObjectID, event.EventID, event.Sequence, digest[:], evidence.EventBody, evidence.SignatureInput, evidence.SignatureValue, evidence.RequestMethod, evidence.RequestTarget, evidence.CoveredComponents, evidence.CoveredValues, evidence.AuthenticatedOrigin, evidence.KeyID, publicKey, keyStatus, keyCreated, profile, accepted, evidenceBytes, encoded)
	return err
}

func checkFederationRetentionReceiptCapacity(ctx context.Context, tx sqliteExecutor, origin, objectID string, nextBytes int64) error {
	var priorBytes int64
	priorErr := tx.QueryRowContext(ctx, `SELECT encoded_bytes FROM federation_retention_receipts WHERE source_origin=? AND object_id=?`, origin, objectID).Scan(&priorBytes)
	if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return priorErr
	}
	var rows, used int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(encoded_bytes),0) FROM federation_retention_receipts`).Scan(&rows, &used); err != nil {
		return err
	}
	rowDelta := int64(0)
	if errors.Is(priorErr, sql.ErrNoRows) {
		rowDelta = 1
	}
	if rows+rowDelta > federationRetentionReceiptMaxRows || used-priorBytes+nextBytes > federationRetentionReceiptMaxBytes {
		return errFederationRetentionCapacity
	}
	return nil
}

func closeFederationRetentionReceipt(ctx context.Context, tx sqliteExecutor, origin, objectID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM federation_retention_receipts WHERE source_origin=? AND object_id=?`, origin, objectID)
	return err
}

func setFederationCatalogSourceState(ctx context.Context, tx sqliteExecutor, origin, objectID, sourceState string) error {
	if sourceState != "published" && sourceState != "withdrawn" && sourceState != "disconnected" && sourceState != "unknown_legacy" {
		return errors.New("invalid federation source state")
	}
	_, err := tx.ExecContext(ctx, `UPDATE federation_remote_catalog SET logical_bytes=logical_bytes-length(CAST(source_state AS BLOB))+length(CAST(? AS BLOB)),source_state=? WHERE source_origin=? AND object_id=?`, sourceState, sourceState, origin, objectID)
	return err
}

// closeFederationOriginForDisable is called inside the same transaction as the
// peer disable. It preserves cursor rows and updates their byte accounting.
func closeFederationOriginForDisable(ctx context.Context, tx sqliteExecutor, origin string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE federation_remote_catalog SET source_state='disconnected',logical_bytes=logical_bytes-length(CAST(source_state AS BLOB))+length(CAST('disconnected' AS BLOB)) WHERE source_origin=?`, origin); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM federation_retention_receipts WHERE source_origin=?`, origin)
	return err
}

// resetFederationPeerExchangeProfileToV1 is used for an explicit downgrade and
// for last-key trust reset. It leaves durable replay and object history intact.
func resetFederationPeerExchangeProfileToV1(ctx context.Context, tx sqliteExecutor, origin string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE federation_peers SET exchange_profile='v1' WHERE origin=?`, origin); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM federation_retention_receipts WHERE source_origin=?`, origin)
	return err
}

// readEligibleFederationRetentionReceipt is called only while holding the
// instance's immediate SQLite write lock. Receipt admission, replacement,
// closure, and snapshot capture therefore share one serialization boundary.
// Its presence represents a receipt accepted by the authenticated inbox; this
// helper rechecks that receipt's current peer, key, profile, catalog, and cache
// fences but does not perform HTTP signature verification itself.
func readEligibleFederationRetentionReceipt(ctx context.Context, tx sqliteExecutor, objectURI string) (remoteSnapshotReceipt, error) {
	origin, recipeID, err := parseFederationRecipeObjectURI(objectURI)
	if err != nil {
		return remoteSnapshotReceipt{}, errRemoteSnapshotNotEligible
	}
	var item remoteSnapshotReceipt
	var bodyDigest []byte
	var catalogSequence, cacheSequence int64
	var deleted, peerEnabled int
	var sourceState, profile, keyOrigin, keyStatus string
	var currentPublicKey []byte
	var currentKeyCreated string
	err = tx.QueryRowContext(ctx, `SELECT rr.receipt_id,rr.object_id,rr.source_origin,rr.event_id,rr.sequence,rr.body_digest,rr.event_body,
		rr.signature_input,rr.signature_value,rr.request_method,rr.request_target,rr.covered_components,rr.covered_values,
		rr.authenticated_origin,rr.key_id,rr.public_key,rr.key_status_at_receipt,rr.key_created_at,rr.profile_at_receipt,rr.accepted_at,rr.evidence_bytes,
		c.high_water_sequence,c.deleted,c.source_state,p.enabled,p.exchange_profile,k.origin,k.status,k.public_key,k.created_at,
		r.source_recipe_id,r.creator_name,r.sequence
		FROM federation_retention_receipts rr
		JOIN federation_remote_catalog c ON c.source_origin=rr.source_origin AND c.object_id=rr.object_id
		JOIN federation_peers p ON p.origin=rr.source_origin
		JOIN federation_peer_keys k ON k.origin=p.origin AND k.key_id=rr.key_id
		JOIN federation_remote_recipes r ON r.object_id=rr.object_id AND r.deleted=0
		WHERE rr.object_id=? AND rr.source_origin=?`, objectURI, origin).Scan(
		&item.ReceiptID, &item.ObjectURI, &item.SourceOrigin, &item.EventID, &item.Sequence, &bodyDigest, &item.EventBody,
		&item.SignatureInput, &item.SignatureValue, &item.RequestMethod, &item.RequestTarget, &item.CoveredComponents, &item.CoveredValues,
		&item.AuthenticatedOrigin, &item.KeyID, &item.PublicKey, &item.KeyStatus, &item.KeyCreatedAt, &item.Profile, &item.AcceptedAt, &item.EvidenceBytes,
		&catalogSequence, &deleted, &sourceState, &peerEnabled, &profile, &keyOrigin, &keyStatus, &currentPublicKey, &currentKeyCreated,
		&item.SourceRecipeID, &item.CreatorName, &cacheSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return remoteSnapshotReceipt{}, errRemoteSnapshotNotEligible
	}
	if err != nil {
		return remoteSnapshotReceipt{}, err
	}
	if item.ObjectURI != objectURI || item.SourceOrigin != origin || item.AuthenticatedOrigin != origin ||
		item.SourceRecipeID != recipeID || item.RequestMethod != http.MethodPost || item.RequestTarget != "/api/federation/inbox" ||
		item.KeyStatus != "active" || item.Profile != federationExchangeV2Retention || profile != federationExchangeV2Retention ||
		peerEnabled != 1 || keyOrigin != origin || keyStatus != "active" || item.KeyCreatedAt != currentKeyCreated ||
		string(item.PublicKey) != string(currentPublicKey) || catalogSequence != item.Sequence || cacheSequence != item.Sequence ||
		deleted != 0 || sourceState != "published" || item.EvidenceBytes < 1 || item.EvidenceBytes > federationRetentionReceiptMaxEvidenceBytes {
		return remoteSnapshotReceipt{}, errRemoteSnapshotNotEligible
	}
	verifiedEvidenceBytes := int64(len(item.SignatureInput) + len(item.SignatureValue) + len(item.RequestMethod) + len(item.RequestTarget) + len(item.CoveredComponents) + len(item.CoveredValues) + len(item.AuthenticatedOrigin) + len(item.KeyID) + len(item.PublicKey) + len(item.KeyStatus) + len(item.KeyCreatedAt) + len(item.Profile))
	if item.EvidenceBytes != verifiedEvidenceBytes {
		return remoteSnapshotReceipt{}, errRemoteSnapshotEvidence
	}
	event, err := exchangev2.Decode(item.EventBody)
	if err != nil || event.EventID != item.EventID || event.EventType == exchangev2.RecipeWithdrawal ||
		event.SourceOrigin != origin || event.ObjectID != objectURI || event.Sequence != item.Sequence ||
		event.Attribution == nil || strings.TrimSpace(event.Attribution.CreatorName) != strings.TrimSpace(item.CreatorName) ||
		event.RetentionGrant == nil || event.RetentionGrant.Purpose != exchangev2.GrantPurpose ||
		event.RetentionGrant.Retention != exchangev2.GrantRetention || event.RetentionGrant.Portability != exchangev2.GrantPortability ||
		event.RetentionGrant.ObjectID != objectURI || event.RetentionGrant.Sequence != item.Sequence {
		return remoteSnapshotReceipt{}, errRemoteSnapshotEvidence
	}
	actualBodyDigest := sha256.Sum256(item.EventBody)
	if len(bodyDigest) != sha256.Size || !bytes.Equal(bodyDigest, actualBodyDigest[:]) {
		return remoteSnapshotReceipt{}, errRemoteSnapshotEvidence
	}
	item.Payload = append([]byte(nil), event.Payload...)
	item.PayloadDigest = make([]byte, sha256.Size)
	decodedDigest, err := hex.DecodeString(event.PayloadSHA256)
	actualPayloadDigest := sha256.Sum256(item.Payload)
	if err != nil || len(decodedDigest) != sha256.Size || !bytes.Equal(decodedDigest, actualPayloadDigest[:]) ||
		event.RetentionGrant.PayloadSHA256 != event.PayloadSHA256 {
		return remoteSnapshotReceipt{}, errRemoteSnapshotEvidence
	}
	item.PayloadDigest = decodedDigest
	item.GrantPurpose = event.RetentionGrant.Purpose
	item.GrantRetention = event.RetentionGrant.Retention
	item.GrantPortability = event.RetentionGrant.Portability
	item.EventType = event.EventType
	item.EventBodyDigest = append([]byte(nil), bodyDigest...)
	item.ReceivedAt = item.AcceptedAt
	components, values, err := decodeRetentionCoveredValues(item.CoveredComponents, item.CoveredValues)
	if err != nil {
		return remoteSnapshotReceipt{}, errRemoteSnapshotEvidence
	}
	for index, component := range components {
		if component == "content-digest" {
			item.ContentDigest = values[index]
		}
	}
	if item.ContentDigest == "" || len(item.SignatureInput) == 0 || len(item.SignatureValue) == 0 || len(item.PublicKey) != 32 {
		return remoteSnapshotReceipt{}, errRemoteSnapshotEvidence
	}
	return item, nil
}

func decodeRetentionCoveredValues(componentsJSON, valuesJSON string) ([]string, []string, error) {
	var components, values []string
	if json.Unmarshal([]byte(componentsJSON), &components) != nil || json.Unmarshal([]byte(valuesJSON), &values) != nil || len(components) == 0 || len(components) != len(values) {
		return nil, nil, errFederationRetentionEvidence
	}
	seen := make(map[string]struct{}, len(components))
	for _, component := range components {
		if component == "" {
			return nil, nil, errFederationRetentionEvidence
		}
		if _, duplicate := seen[component]; duplicate {
			return nil, nil, errFederationRetentionEvidence
		}
		seen[component] = struct{}{}
	}
	return components, values, nil
}
