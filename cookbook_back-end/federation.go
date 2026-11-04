package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
	"github.com/common-fate/httpsig"
	"github.com/common-fate/httpsig/sigset"
)

type federationInboxHandler struct {
	db             *sql.DB
	origin         string
	trustedProxies []*net.IPNet
}

type publicFederationRecipesHandler struct{ db *sql.DB }

func (h publicFederationRecipesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var public int
	if err := h.db.QueryRow(`SELECT public_instance FROM instance_settings WHERE id=1`).Scan(&public); err != nil || public != 1 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	rows, err := h.db.Query(`SELECT object_id,source_origin,creator_name,recipe_json,sequence,updated_at FROM federation_remote_recipes WHERE deleted=0 ORDER BY updated_at DESC,object_id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	type item struct {
		ObjectID     string          `json:"object_id"`
		SourceOrigin string          `json:"source_origin"`
		CreatorName  string          `json:"creator_name"`
		Recipe       json.RawMessage `json:"recipe"`
		Sequence     int64           `json:"sequence"`
		UpdatedAt    string          `json:"updated_at"`
	}
	items := []item{}
	for rows.Next() {
		var current item
		var recipeJSON []byte
		if err := rows.Scan(&current.ObjectID, &current.SourceOrigin, &current.CreatorName, &recipeJSON, &current.Sequence, &current.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		current.Recipe = json.RawMessage(recipeJSON)
		items = append(items, current)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h federationInboxHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !federationEnabled(h.db) || h.origin == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	if r.ContentLength < 0 {
		writeError(w, http.StatusLengthRequired, "content_length_required")
		return
	}
	tag, err := federationRequestSignatureTag(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_signature_profile")
		return
	}
	if tag == federationSignatureV2Tag {
		meta, err := captureFederationV2RequestMetadata(r, h.origin)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_signature_profile")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), federationV2RequestMetadataKey, meta))
	}
	federationSignatureMiddlewareForTag(h.db, h.origin, h.trustedProxies, tag, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerOrigin, ok := httpsig.AttributesFromContext(r.Context()).(string)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxFederationEventBytes+1))
		if err != nil || int64(len(body)) > maxFederationEventBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "event_too_large")
			return
		}
		acceptCtx := r.Context()
		if tag == federationSignatureV2Tag {
			verified, err := verifyFederationRetentionEvidence(r, h.db, peerOrigin, body)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid_signature_profile")
				return
			}
			acceptCtx = context.WithValue(acceptCtx, federationVerifiedRequestKey{}, verified)
		}
		status, response, err := acceptFederationEvent(acceptCtx, h.db, peerOrigin, body)
		if err != nil {
			if errors.Is(err, errFederationConflict) {
				writeError(w, http.StatusConflict, "federation_conflict")
				return
			}
			if errors.Is(err, errFederationResourceCapacity) {
				w.Header().Set("Retry-After", "3600")
				writeError(w, http.StatusServiceUnavailable, "federation_resource_capacity")
				return
			}
			if errors.Is(err, errFederationRetentionCapacity) {
				w.Header().Set("Retry-After", "3600")
				writeError(w, http.StatusServiceUnavailable, "federation_retention_capacity")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_federation_event")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(response)
	})).ServeHTTP(w, r)
}

type federationV2RequestMetadataKeyType struct{}

var federationV2RequestMetadataKey = federationV2RequestMetadataKeyType{}

type federationV2RequestMetadata struct {
	components []string
	values     []string
	target     string
	keyID      string
}

func federationRequestSignatureTag(r *http.Request) (string, error) {
	set, err := sigset.Unmarshal(r)
	if err != nil {
		return "", errors.New("ambiguous federation signature")
	}
	_, v1Err := set.Find(federationSignatureTag)
	_, v2Err := set.Find(federationSignatureV2Tag)
	if v1Err == nil && v2Err == nil {
		return "", errors.New("multiple federation signature profiles")
	}
	if v2Err == nil {
		if len(set.Messages) != 1 {
			return "", errors.New("ambiguous Exchange v2 signatures")
		}
		return federationSignatureV2Tag, nil
	}
	if v1Err == nil {
		return federationSignatureTag, nil
	}
	return "", errors.New("unsupported federation signature tag")
}

func captureFederationV2RequestMetadata(r *http.Request, configuredOrigin string) (federationV2RequestMetadata, error) {
	if r.URL.RequestURI() != "/api/federation/inbox" {
		return federationV2RequestMetadata{}, errors.New("invalid federation inbox target")
	}
	canonicalOrigin, err := canonicalFederationOrigin(configuredOrigin)
	if err != nil || canonicalOrigin != configuredOrigin {
		return federationV2RequestMetadata{}, errors.New("invalid configured federation origin")
	}
	authority := strings.TrimPrefix(configuredOrigin, "https://")
	if r.Host != authority {
		return federationV2RequestMetadata{}, errors.New("request authority differs from configured verifier authority")
	}
	set, err := sigset.Unmarshal(r)
	if err != nil {
		return federationV2RequestMetadata{}, err
	}
	message, err := set.Find(federationSignatureV2Tag)
	if err != nil {
		return federationV2RequestMetadata{}, err
	}
	if len(r.Header.Values("Content-Digest")) != 1 || len(r.Header.Values("Content-Type")) != 1 {
		return federationV2RequestMetadata{}, errors.New("ambiguous signed headers")
	}
	components := append([]string(nil), message.Input.CoveredComponents...)
	values := make([]string, 0, len(components))
	for _, component := range components {
		switch component {
		case "@method":
			values = append(values, r.Method)
		case "@target-uri":
			target := r.URL.RequestURI()
			if target == "" {
				target = "/"
			}
			values = append(values, configuredOrigin+target)
		case "content-type":
			values = append(values, r.Header.Values("Content-Type")[0])
		case "content-length":
			values = append(values, strconv.FormatInt(r.ContentLength, 10))
		case "content-digest":
			values = append(values, r.Header.Values("Content-Digest")[0])
		default:
			return federationV2RequestMetadata{}, errors.New("unsupported signed component")
		}
	}
	target := r.URL.RequestURI()
	if target == "" {
		target = "/"
	}
	return federationV2RequestMetadata{components: components, values: values, target: r.URL.RequestURI(), keyID: message.Input.KeyID}, nil
}

func verifyFederationRetentionEvidence(r *http.Request, db *sql.DB, origin string, body []byte) (federationVerifiedRequest, error) {
	var result federationVerifiedRequest
	meta, ok := r.Context().Value(federationV2RequestMetadataKey).(federationV2RequestMetadata)
	if !ok {
		return result, errFederationRetentionEvidence
	}
	raw, ok := r.Context().Value(federationRawSignatureHeadersKey{}).(federationRawSignatureHeaders)
	if !ok {
		return result, errFederationRetentionEvidence
	}
	componentsJSON, err := json.Marshal(meta.components)
	if err != nil {
		return result, err
	}
	valuesJSON, err := json.Marshal(meta.values)
	if err != nil {
		return result, err
	}
	var publicKey []byte
	var foundOrigin string
	if err := db.QueryRowContext(r.Context(), `SELECT origin,public_key FROM federation_peer_keys WHERE key_id=? AND status='active'`, meta.keyID).Scan(&foundOrigin, &publicKey); err != nil || foundOrigin != origin || len(publicKey) != ed25519.PublicKeySize {
		return result, errFederationRetentionEvidence
	}
	result.publicKey = append([]byte(nil), publicKey...)
	result.Evidence = federationRetentionEvidence{ReceiptID: newID(), EventBody: append([]byte(nil), body...), SignatureInput: raw.input, SignatureValue: raw.signature, RequestMethod: r.Method, RequestTarget: meta.target, CoveredComponents: string(componentsJSON), CoveredValues: string(valuesJSON), AuthenticatedOrigin: origin, KeyID: meta.keyID}
	return result, nil
}

func federationEnabled(db *sql.DB) bool {
	var enabled int
	if err := db.QueryRow(`SELECT federation_enabled FROM instance_settings WHERE id=1`).Scan(&enabled); err != nil {
		return false
	}
	return enabled == 1
}

var errFederationConflict = errors.New("federation event conflicts with accepted state")

type federationRecipeSource struct {
	ID             string
	OwnerID        string
	CreatorName    string
	SourceRecipeID string
	Title          string
	Description    string
	Ingredients    []ingredient
	Steps          []recipeStep
	Servings       int
	PrepTime       int
	CookTime       int
	Tags           []string
	Visibility     visibility
	Deleted        bool
	ImagePath      string
	CookbookIDs    []string
}

func acceptFederationEvent(ctx context.Context, db *sql.DB, keyOrigin string, body []byte) (int, []byte, error) {
	v2Event, v2Err := exchangev2.Decode(body)
	v2 := v2Err == nil
	var event federationEvent
	var recipeJSON []byte
	var err error
	if v2 {
		if v2Event.SourceOrigin != keyOrigin {
			return 0, nil, errors.New("invalid or mismatched federation event")
		}
		event = federationEvent{EventID: v2Event.EventID, EventType: v2Event.EventType, SourceOrigin: v2Event.SourceOrigin, ObjectID: v2Event.ObjectID, Sequence: v2Event.Sequence}
		if v2Event.EventType != exchangev2.RecipeWithdrawal {
			var content RemoteRecipeContent
			if err := decodeRemoteRecipePayload(v2Event.Payload, &content); err != nil {
				return 0, nil, errors.New("invalid Exchange v2 recipe payload")
			}
			if v2Event.Attribution == nil {
				return 0, nil, errors.New("invalid Exchange v2 attribution")
			}
			event.Attribution = &federationEventAttribution{CreatorName: v2Event.Attribution.CreatorName}
			recipeJSON = append([]byte(nil), v2Event.Payload...)
		}
	} else {
		event, recipeJSON, err = decodeFederationEvent(body)
	}
	_, authenticatedV2 := verifiedFederationRetentionRequestFromContext(ctx)
	if v2 != authenticatedV2 {
		return 0, nil, errors.New("federation signature tag and body version mismatch")
	}
	if err != nil || event.SourceOrigin != keyOrigin {
		return 0, nil, errors.New("invalid or mismatched federation event")
	}
	digest := sha256.Sum256(body)
	response, err := json.Marshal(map[string]any{"status": "accepted", "event_id": event.EventID, "object_id": event.ObjectID, "sequence": event.Sequence})
	if err != nil {
		return 0, nil, err
	}
	acceptedStatus := 0
	acceptedResponse := []byte(nil)
	err = withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
		if v2 {
			var peerEnabled int
			var profile string
			var keyStatus, keyOriginStored string
			if err := tx.QueryRowContext(ctx, `SELECT enabled,exchange_profile FROM federation_peers WHERE origin=?`, keyOrigin).Scan(&peerEnabled, &profile); err != nil || peerEnabled != 1 {
				return errors.New("federation peer is disabled")
			}
			if profile != federationExchangeV2Retention {
				return errors.New("federation v2 profile is disabled")
			}
			verified, ok := verifiedFederationRetentionRequestFromContext(ctx)
			if !ok || verified.Evidence.AuthenticatedOrigin != keyOrigin {
				return errFederationRetentionEvidence
			}
			var publicKey []byte
			if err := tx.QueryRowContext(ctx, `SELECT origin,status,public_key FROM federation_peer_keys WHERE key_id=?`, verified.Evidence.KeyID).Scan(&keyOriginStored, &keyStatus, &publicKey); err != nil || keyOriginStored != keyOrigin || keyStatus != "active" || !bytes.Equal(publicKey, verified.publicKey) {
				return errFederationRetentionEvidence
			}
		}
		var previousDigest []byte
		var previousStatus int
		var previousResponse string
		err := tx.QueryRowContext(ctx, `SELECT body_digest,response_status,response_body FROM federation_received_events WHERE source_origin=? AND event_id=?`, keyOrigin, event.EventID).Scan(&previousDigest, &previousStatus, &previousResponse)
		if err == nil {
			if !bytes.Equal(previousDigest, digest[:]) {
				return errFederationConflict
			}
			acceptedStatus, acceptedResponse = previousStatus, []byte(previousResponse)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// V1 acceptance historically returns an exact duplicate receipt even after local peer removal.
		// New V1 events still require an enabled peer, while authenticated v2 requests recheck
		// peer/key/profile before duplicate lookup above.
		if !v2 {
			var peerEnabled int
			if err := tx.QueryRowContext(ctx, `SELECT enabled FROM federation_peers WHERE origin=?`, keyOrigin).Scan(&peerEnabled); err != nil || peerEnabled != 1 {
				return errors.New("federation peer is disabled")
			}
		}
		var latestSequence int64
		var priorDeleted int
		var priorStateKnown int
		var priorObjectBytes int64
		isUpdate := event.EventType == federationEventUpdate || event.EventType == exchangev2.RecipeUpdate
		isSnapshot := event.EventType == federationEventSnapshot || event.EventType == exchangev2.RecipeSnapshot
		isWithdrawal := event.EventType == federationEventTombstone || event.EventType == exchangev2.RecipeWithdrawal
		err = tx.QueryRowContext(ctx, `SELECT high_water_sequence,deleted,state_known,logical_bytes FROM federation_remote_catalog WHERE object_id=? AND source_origin=?`, event.ObjectID, keyOrigin).Scan(&latestSequence, &priorDeleted, &priorStateKnown, &priorObjectBytes)
		catalogExists := err == nil
		if errors.Is(err, sql.ErrNoRows) {
			if !isSnapshot {
				return errFederationConflict
			}
			latestSequence = 0
		} else if err != nil {
			return err
		}
		if event.Sequence <= latestSequence || (isUpdate && (priorDeleted == 1 || priorStateKnown == 0)) || (!catalogExists && !isSnapshot) {
			return errFederationConflict
		}
		now := timestamp()
		recipeID := strings.TrimPrefix(event.ObjectID, keyOrigin+"/federation/recipes/")
		creatorName, rawRecipe := "", ""
		deleted := 1
		if !isWithdrawal {
			creatorName, rawRecipe, deleted = event.Attribution.CreatorName, string(recipeJSON), 0
		}
		title, description, tagsJSON := "", "", "[]"
		if deleted == 0 {
			cached := RemoteRecipeReadView{ObjectURI: event.ObjectID, SourceOrigin: keyOrigin, SourceRecipeID: recipeID, CreatorName: creatorName, Sequence: event.Sequence, UpdatedAt: now}
			if view, decodeErr := decodeCachedPublicRemoteRecipe(event.ObjectID, keyOrigin, recipeID, cached, rawRecipe); decodeErr == nil {
				title, description = view.Recipe.Title, view.Recipe.Description
				if encoded, marshalErr := json.Marshal(view.Recipe.Tags); marshalErr != nil {
					return marshalErr
				} else {
					tagsJSON = string(encoded)
				}
			}
		}
		sourceState := "published"
		if deleted == 1 && v2 && isWithdrawal {
			sourceState = "withdrawn"
		} else if deleted == 1 {
			sourceState = "unknown_legacy"
		}
		nextObjectBytes := remoteCatalogLogicalBytes(keyOrigin, event.ObjectID, recipeID, creatorName, rawRecipe, title, description, tagsJSON, sourceState, event.Sequence, deleted)
		objectDelta := nextObjectBytes
		objectRows := int64(1)
		if catalogExists {
			objectDelta -= priorObjectBytes
			objectRows = 0
		}
		receivedAt := now
		receiptBytes := receivedEventLogicalBytes(keyOrigin, event.EventID, digest[:], event.ObjectID, event.Sequence, receivedAt, http.StatusAccepted, string(response))
		if err := checkFederationResourceLimits(ctx, tx, keyOrigin,
			federationResourceUsage{ObjectRows: objectRows, ObjectBytes: objectDelta},
			federationResourceUsage{EventRows: 1, EventBytes: receiptBytes}); err != nil {
			return err
		}
		if catalogExists {
			if _, err := tx.ExecContext(ctx, `UPDATE federation_remote_catalog SET source_recipe_id=?,high_water_sequence=?,deleted=?,state_known=1,source_state=?,logical_bytes=? WHERE source_origin=? AND object_id=?`, recipeID, event.Sequence, deleted, sourceState, nextObjectBytes, keyOrigin, event.ObjectID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,source_state,logical_bytes) VALUES(?,?,?,?,?,1,?,?)`, keyOrigin, event.ObjectID, recipeID, event.Sequence, deleted, sourceState, nextObjectBytes); err != nil {
			return err
		}
		if v2 && v2Event.RetentionGrant != nil {
			verified, _ := verifiedFederationRetentionRequestFromContext(ctx)
			if err := admitFederationRetentionReceipt(ctx, tx, verified.asRetentionEvidence()); err != nil {
				return err
			}
		} else if err := closeFederationRetentionReceipt(ctx, tx, keyOrigin, event.ObjectID); err != nil {
			return err
		}
		if deleted == 1 {
			if catalogExists {
				if _, err := tx.ExecContext(ctx, `UPDATE federation_remote_recipes SET sequence=?,creator_name='',recipe_json='',deleted=1,updated_at=? WHERE object_id=? AND source_origin=?`, event.Sequence, now, event.ObjectID, keyOrigin); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM federation_remote_recipe_search WHERE object_id=?`, event.ObjectID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?) ON CONFLICT(object_id) DO UPDATE SET sequence=excluded.sequence,creator_name=excluded.creator_name,recipe_json=excluded.recipe_json,deleted=0,updated_at=excluded.updated_at`, event.ObjectID, keyOrigin, recipeID, event.Sequence, creatorName, rawRecipe, now); err != nil {
				return err
			}
			if err := writeRemoteRecipeSearchProjectionExec(ctx, tx, RemoteRecipeReadView{ObjectURI: event.ObjectID, SourceOrigin: keyOrigin, SourceRecipeID: recipeID, CreatorName: creatorName, Sequence: event.Sequence, UpdatedAt: now}, rawRecipe); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body,logical_bytes) VALUES(?,?,?,?,?,?,?,?,?)`, keyOrigin, event.EventID, digest[:], event.ObjectID, event.Sequence, receivedAt, http.StatusAccepted, string(response), receiptBytes); err != nil {
			return err
		}
		acceptedStatus, acceptedResponse = http.StatusAccepted, response
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return acceptedStatus, acceptedResponse, nil
}

func writeRemoteRecipeSearchProjectionExec(ctx context.Context, tx sqliteExecutor, source RemoteRecipeReadView, rawBody string) error {
	view, decodeErr := decodeCachedPublicRemoteRecipe(source.ObjectURI, source.SourceOrigin, source.SourceRecipeID, source, rawBody)
	available, title, description, tagsJSON := 1, "", "", "[]"
	if decodeErr != nil {
		available = 0
	} else {
		encoded, err := json.Marshal(view.Recipe.Tags)
		if err != nil {
			return err
		}
		title, description, tagsJSON = view.Recipe.Title, view.Recipe.Description, string(encoded)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO federation_remote_recipe_search(object_id,projection_version,available,title,description,tags_json)
		VALUES(?,?,?,?,?,?) ON CONFLICT(object_id) DO UPDATE SET projection_version=excluded.projection_version,available=excluded.available,title=excluded.title,description=excluded.description,tags_json=excluded.tags_json
		WHERE federation_remote_recipe_search.projection_version<=excluded.projection_version`, source.ObjectURI, remoteRecipeSearchProjectionVersion, available, title, description, tagsJSON)
	return err
}

func enqueueFederationChange(tx *sql.Tx, recipeID string) error {
	return enqueueFederationChangeForPeer(tx, recipeID, "", false)
}

func federationRecipePublicationEligible(ctx context.Context, queryer recipePolicyQueryer, recipeID, expectedOrigin string) (bool, error) {
	readFacts, err := readRecipePublicReadFacts(ctx, queryer, recipeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var federation int
	var origin, mode string
	err = queryer.QueryRowContext(ctx, `SELECT s.federation_enabled,s.federation_origin,p.federation_mode
		FROM instance_settings s JOIN recipe_publication_policy p ON p.recipe_id=? WHERE s.id=1`, recipeID).
		Scan(&federation, &origin, &mode)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	canonicalOrigin, err := canonicalFederationOrigin(origin)
	if err != nil || canonicalOrigin != origin || (expectedOrigin != "" && expectedOrigin != origin) {
		return false, nil
	}
	return readFacts.AnonymousEligible() && federation == 1 && mode == string(RecipeFederationFollowers), nil
}

func federationObjectRecipeID(origin, objectID string) (string, bool) {
	const marker = "/federation/recipes/"
	if !strings.HasPrefix(objectID, origin+marker) {
		return "", false
	}
	recipeID := strings.TrimPrefix(objectID, origin+marker)
	if strings.Contains(recipeID, "/") || !isFederationID(recipeID) {
		return "", false
	}
	return recipeID, true
}

func validFederationEgressOrigin(origin string) bool {
	canonical, err := canonicalFederationOrigin(origin)
	return err == nil && canonical == origin
}

func federationOutboxEventSummary(payload []byte) (sourceOrigin, objectID, eventType string, sequence int64, err error) {
	if event, decodeErr := exchangev2.Decode(payload); decodeErr == nil {
		return event.SourceOrigin, event.ObjectID, event.EventType, event.Sequence, nil
	}
	event, _, err := decodeFederationEvent(payload)
	if err != nil {
		return "", "", "", 0, err
	}
	return event.SourceOrigin, event.ObjectID, event.EventType, event.Sequence, nil
}

func hasPendingFederationContentForRecipe(ctx context.Context, tx sqliteExecutor, peer, recipeID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM federation_outbox WHERE peer_origin=? AND object_id LIKE ? ORDER BY sequence,id`, peer, "%/federation/recipes/"+recipeID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		sourceOrigin, objectID, eventType, _, err := federationOutboxEventSummary(payload)
		if err != nil {
			return false, err
		}
		if sourceOrigin != "" && strings.HasSuffix(objectID, "/federation/recipes/"+recipeID) &&
			(eventType == federationEventSnapshot || eventType == federationEventUpdate || eventType == exchangev2.RecipeSnapshot || eventType == exchangev2.RecipeUpdate) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func reconcilePendingFederationRows(ctx context.Context, tx sqliteExecutor, peer, objectID string, now int64, prune bool) (int64, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,payload,lease_until FROM federation_outbox WHERE peer_origin=? AND object_id=? ORDER BY sequence,id`, peer, objectID)
	if err != nil {
		return 0, 0, err
	}
	type staleRow struct {
		id       string
		payload  []byte
		leaseEnd sql.NullInt64
	}
	var highestContentSequence, highestTombstoneSequence int64
	var remove []string
	for rows.Next() {
		var row staleRow
		if err := rows.Scan(&row.id, &row.payload, &row.leaseEnd); err != nil {
			rows.Close()
			return 0, 0, err
		}
		_, _, eventType, sequence, err := federationOutboxEventSummary(row.payload)
		if err != nil {
			rows.Close()
			return 0, 0, err
		}
		if eventType == federationEventTombstone || eventType == exchangev2.RecipeWithdrawal {
			if sequence > highestTombstoneSequence {
				highestTombstoneSequence = sequence
			}
			continue
		}
		if eventType != federationEventSnapshot && eventType != federationEventUpdate && eventType != exchangev2.RecipeSnapshot && eventType != exchangev2.RecipeUpdate {
			continue
		}
		if sequence > highestContentSequence {
			highestContentSequence = sequence
		}
		if prune && (!row.leaseEnd.Valid || row.leaseEnd.Int64 < now) {
			remove = append(remove, row.id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	for _, id := range remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM federation_outbox WHERE id=?`, id); err != nil {
			return 0, 0, err
		}
	}
	return highestContentSequence, highestTombstoneSequence, nil
}

func enqueueFederationChangeForPeer(tx *sql.Tx, recipeID, targetPeer string, forceSnapshot bool) error {
	return enqueueFederationChangeForPeerExec(context.Background(), tx, recipeID, targetPeer, forceSnapshot)
}

func enqueueFederationChangeForPeerExec(ctx context.Context, tx sqliteExecutor, recipeID, targetPeer string, forceSnapshot bool) error {
	var federation, instancePublic int
	var origin string
	if err := tx.QueryRowContext(ctx, `SELECT federation_enabled,public_instance,federation_origin FROM instance_settings WHERE id=1`).Scan(&federation, &instancePublic, &origin); err != nil {
		return err
	}
	var source federationRecipeSource
	var ingredientsJSON, stepsJSON, tagsJSON, deletedAt string
	if err := tx.QueryRowContext(ctx, `SELECT id,owner_id,creator_name,source_recipe_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,image_path,COALESCE(deleted_at,'') FROM recipes WHERE id=?`, recipeID).Scan(&source.ID, &source.OwnerID, &source.CreatorName, &source.SourceRecipeID, &source.Title, &source.Description, &ingredientsJSON, &stepsJSON, &source.Servings, &source.PrepTime, &source.CookTime, &tagsJSON, &source.Visibility, &source.ImagePath, &deletedAt); err != nil {
		return err
	}
	source.Deleted = deletedAt != ""
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, recipeID).Scan(&sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	policyEligible, err := federationRecipePublicationEligible(ctx, tx, recipeID, origin)
	if err != nil {
		return err
	}
	eligible := policyEligible && federation == 1 && instancePublic == 1 && origin != "" && source.Visibility == visibilityPublic && !source.Deleted
	if eligible {
		if err := json.Unmarshal([]byte(ingredientsJSON), &source.Ingredients); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(stepsJSON), &source.Steps); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(tagsJSON), &source.Tags); err != nil {
			return err
		}
	}
	peerQuery := `SELECT origin,enabled,exchange_profile FROM federation_peers ORDER BY origin`
	peerArgs := []any{}
	if targetPeer != "" {
		peerQuery = `SELECT origin,enabled,exchange_profile FROM federation_peers WHERE origin=? ORDER BY origin`
		peerArgs = append(peerArgs, targetPeer)
	}
	rows, err := tx.QueryContext(ctx, peerQuery, peerArgs...)
	if err != nil {
		return err
	}
	type federationPeer struct {
		origin  string
		enabled bool
		profile string
	}
	var peers []federationPeer
	for rows.Next() {
		var peer federationPeer
		var enabled int
		if err := rows.Scan(&peer.origin, &enabled, &peer.profile); err != nil {
			rows.Close()
			return err
		}
		peer.enabled = enabled == 1
		peers = append(peers, peer)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if targetPeer != "" && (len(peers) != 1 || !peers[0].enabled) {
		return errors.New("federation peer is not enabled")
	}
	type eventTarget struct {
		peer      string
		eventType string
		profile   string
	}
	targets := make([]eventTarget, 0, len(peers))
	var objectID string
	objectIDReady := false
	ensureObjectID := func() error {
		if objectIDReady {
			return nil
		}
		if !validFederationEgressOrigin(origin) {
			return errors.New("federation origin is required for pending publication state")
		}
		var err error
		objectID, err = federationRecipeObjectID(origin, recipeID)
		if err != nil {
			return err
		}
		objectIDReady = true
		return nil
	}
	for _, peer := range peers {
		var peerPublished int
		peerErr := tx.QueryRowContext(ctx, `SELECT published FROM federation_local_peer_objects WHERE peer_origin=? AND recipe_id=?`, peer.origin, recipeID).Scan(&peerPublished)
		if peerErr != nil && !errors.Is(peerErr, sql.ErrNoRows) {
			return peerErr
		}
		eventType := ""
		if eligible {
			if peer.enabled {
				if err := ensureObjectID(); err != nil {
					return err
				}
				if (targetPeer == peer.origin && forceSnapshot) || peerPublished == 0 {
					eventType = federationEventSnapshot
				} else {
					eventType = federationEventUpdate
				}
			}
		} else {
			if validFederationEgressOrigin(origin) {
				if err := ensureObjectID(); err != nil {
					return err
				}
				pendingContentSequence, pendingTombstoneSequence, err := reconcilePendingFederationRows(ctx, tx, peer.origin, objectID, time.Now().Unix(), true)
				if err != nil {
					return err
				}
				if peerPublished == 1 || pendingContentSequence > pendingTombstoneSequence {
					eventType = federationEventTombstone
				}
			} else {
				if peerPublished == 1 {
					return errors.New("federation origin is required to withdraw a published object")
				}
				pendingContent, err := hasPendingFederationContentForRecipe(ctx, tx, peer.origin, recipeID)
				if err != nil {
					return err
				}
				if pendingContent {
					return errors.New("federation origin is required to reconcile pending content")
				}
			}
		}
		if eventType != "" {
			if peer.profile == federationExchangeV2Retention {
				if eventType == federationEventTombstone {
					eventType = exchangev2.RecipeWithdrawal
				} else if eventType == federationEventSnapshot {
					eventType = exchangev2.RecipeSnapshot
				} else {
					eventType = exchangev2.RecipeUpdate
				}
			}
			targets = append(targets, eventTarget{peer: peer.origin, eventType: eventType, profile: peer.profile})
		}
	}
	if len(targets) == 0 {
		return nil
	}
	sequence++
	publishedNow := 0
	if eligible {
		publishedNow = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO federation_local_objects(recipe_id,sequence,published) VALUES(?,?,?) ON CONFLICT(recipe_id) DO UPDATE SET sequence=excluded.sequence,published=excluded.published`, recipeID, sequence, publishedNow); err != nil {
		return err
	}
	for _, target := range targets {
		var body []byte
		var err error
		if target.profile == federationExchangeV2Retention {
			body, err = serializeFederationV2Event(origin, newID(), target.eventType, sequence, time.Now(), source, nil, nil)
		} else {
			body, err = serializeFederationEvent(origin, newID(), target.eventType, sequence, time.Now(), federation == 1, instancePublic == 1, source)
		}
		if err != nil {
			return err
		}
		var eventID, eventObject string
		var eventSequence int64
		if target.profile == federationExchangeV2Retention {
			parsed, err := exchangev2.Decode(body)
			if err != nil {
				return err
			}
			eventID, eventObject, eventSequence = parsed.EventID, parsed.ObjectID, parsed.Sequence
		} else {
			var event federationEvent
			if err := json.Unmarshal(body, &event); err != nil {
				return err
			}
			eventID, eventObject, eventSequence = event.EventID, event.ObjectID, event.Sequence
		}
		peerPublished := 0
		if eligible {
			peerPublished = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO federation_local_peer_objects(peer_origin,recipe_id,published,updated_at) VALUES(?,?,?,?) ON CONFLICT(peer_origin,recipe_id) DO UPDATE SET published=excluded.published,updated_at=excluded.updated_at`, target.peer, recipeID, peerPublished, timestamp()); err != nil {
			return err
		}
		var peerCount, peerBytes, totalCount, totalBytes int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM federation_outbox WHERE peer_origin=?`, target.peer).Scan(&peerCount, &peerBytes); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM federation_outbox`).Scan(&totalCount, &totalBytes); err != nil {
			return err
		}
		if peerCount >= 1000 || int64(peerBytes)+int64(len(body)) > 16<<20 || totalCount >= 10000 || int64(totalBytes)+int64(len(body)) > 64<<20 {
			return errors.New("federation outbox capacity reached")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at) VALUES(?,?,?,?,?,?,?,?)`, newID(), target.peer, eventID, eventObject, eventSequence, body, time.Now().Unix(), timestamp()); err != nil {
			return err
		}
	}
	return nil
}

func serializeFederationV2Event(origin, eventID, eventType string, sequence int64, occurredAt time.Time, source federationRecipeSource, exactPayload []byte, grant *exchangev2.RetentionGrant) ([]byte, error) {
	if sequence < 1 || occurredAt.IsZero() || !isFederationID(eventID) {
		return nil, errors.New("invalid federation v2 event metadata")
	}
	objectID, err := federationRecipeObjectID(origin, source.ID)
	if err != nil {
		return nil, err
	}
	event := exchangev2.Event{Protocol: exchangev2.Protocol, SchemaVersion: exchangev2.SchemaVersion, EventID: eventID, EventType: eventType, SourceOrigin: origin, ObjectID: objectID, Sequence: sequence, OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano)}
	event.RetentionGrant = grant
	switch eventType {
	case exchangev2.RecipeSnapshot, exchangev2.RecipeUpdate:
		if source.Deleted || source.Visibility != visibilityPublic {
			return nil, errors.New("recipe is not eligible for federation")
		}
		creator := strings.TrimSpace(source.CreatorName)
		if creator == "" {
			creator = "Unknown creator"
		}
		if len(creator) > 200 {
			return nil, errors.New("invalid federation attribution")
		}
		event.Attribution = &exchangev2.Attribution{CreatorName: creator}
		if len(exactPayload) == 0 {
			input := recipeInput{Title: source.Title, Description: source.Description, Ingredients: source.Ingredients, Steps: source.Steps, Servings: source.Servings, PrepTime: source.PrepTime, CookTime: source.CookTime, Tags: source.Tags, Visibility: visibilityPublic, EditPolicy: "owner_only"}
			if err := input.validate(); err != nil {
				return nil, errors.New("invalid public recipe")
			}
			if input.Ingredients == nil {
				input.Ingredients = []ingredient{}
			}
			if input.Steps == nil {
				input.Steps = []recipeStep{}
			}
			content := RemoteRecipeContent{Title: input.Title, Description: input.Description, Ingredients: input.Ingredients, Steps: input.Steps, Servings: input.Servings, PrepTime: input.PrepTime, CookTime: input.CookTime, Tags: input.Tags}
			exactPayload, err = json.Marshal(content)
			if err != nil {
				return nil, err
			}
		}
		event.Payload = append([]byte(nil), exactPayload...)
		event.PayloadSHA256 = exchangev2.PayloadDigest(event.Payload)
	case exchangev2.RecipeWithdrawal:
		if len(exactPayload) != 0 {
			return nil, errors.New("withdrawals cannot contain recipe content")
		}
	default:
		return nil, errors.New("unsupported federation v2 event type")
	}
	return exchangev2.Encode(event)
}

var errFederationOwnerAssertionConflict = errors.New("federation owner assertion conflict")

func enqueueFederationOwnerAssertion(ctx context.Context, tx sqliteExecutor, recipeID, actorID string, expectedVersion int) error {
	if expectedVersion < 1 || actorID == "" {
		return errFederationOwnerAssertionConflict
	}
	var federation, publicInstance int
	var origin string
	if err := tx.QueryRowContext(ctx, `SELECT federation_enabled,public_instance,federation_origin FROM instance_settings WHERE id=1`).Scan(&federation, &publicInstance, &origin); err != nil {
		return err
	}
	var source federationRecipeSource
	var version int
	var ingredientsJSON, stepsJSON, tagsJSON, deletedAt string
	if err := tx.QueryRowContext(ctx, `SELECT id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,image_path,COALESCE(deleted_at,''),version FROM recipes WHERE id=?`, recipeID).Scan(&source.ID, &source.OwnerID, &source.CreatorName, &source.Title, &source.Description, &ingredientsJSON, &stepsJSON, &source.Servings, &source.PrepTime, &source.CookTime, &tagsJSON, &source.Visibility, &source.ImagePath, &deletedAt, &version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errFederationOwnerAssertionConflict
		}
		return err
	}
	if source.OwnerID != actorID || deletedAt != "" || version != expectedVersion || source.Visibility != visibilityPublic || federation != 1 || publicInstance != 1 || !validFederationEgressOrigin(origin) {
		return errFederationOwnerAssertionConflict
	}
	source.Deleted = deletedAt != ""
	if err := json.Unmarshal([]byte(ingredientsJSON), &source.Ingredients); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(stepsJSON), &source.Steps); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &source.Tags); err != nil {
		return err
	}
	eligible, err := federationRecipePublicationEligible(ctx, tx, recipeID, origin)
	if err != nil {
		return err
	}
	if !eligible {
		return errFederationOwnerAssertionConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT origin FROM federation_peers WHERE enabled=1 AND exchange_profile=? ORDER BY origin`, federationExchangeV2Retention)
	if err != nil {
		return err
	}
	var peers []string
	for rows.Next() {
		var peer string
		if err := rows.Scan(&peer); err != nil {
			rows.Close()
			return err
		}
		peers = append(peers, peer)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(peers) == 0 {
		return errFederationOwnerAssertionConflict
	}
	var sequence int64
	err = tx.QueryRowContext(ctx, `SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, recipeID).Scan(&sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sequence++
	objectID, err := federationRecipeObjectID(origin, recipeID)
	if err != nil {
		return err
	}
	content := recipeInput{Title: source.Title, Description: source.Description, Ingredients: source.Ingredients, Steps: source.Steps, Servings: source.Servings, PrepTime: source.PrepTime, CookTime: source.CookTime, Tags: source.Tags, Visibility: visibilityPublic, EditPolicy: "owner_only"}
	if err := content.validate(); err != nil {
		return err
	}
	if content.Ingredients == nil {
		content.Ingredients = []ingredient{}
	}
	if content.Steps == nil {
		content.Steps = []recipeStep{}
	}
	serialized, err := json.Marshal(RemoteRecipeContent{Title: content.Title, Description: content.Description, Ingredients: content.Ingredients, Steps: content.Steps, Servings: content.Servings, PrepTime: content.PrepTime, CookTime: content.CookTime, Tags: content.Tags})
	if err != nil {
		return err
	}
	digest := exchangev2.PayloadDigest(serialized)
	if _, err := tx.ExecContext(ctx, `INSERT INTO federation_local_objects(recipe_id,sequence,published) VALUES(?,?,1) ON CONFLICT(recipe_id) DO UPDATE SET sequence=excluded.sequence,published=1`, recipeID, sequence); err != nil {
		return err
	}
	for _, peer := range peers {
		var published int
		peerStateErr := tx.QueryRowContext(ctx, `SELECT published FROM federation_local_peer_objects WHERE peer_origin=? AND recipe_id=?`, peer, recipeID).Scan(&published)
		if peerStateErr != nil && !errors.Is(peerStateErr, sql.ErrNoRows) {
			return peerStateErr
		}
		eventType := exchangev2.RecipeUpdate
		if published == 0 {
			eventType = exchangev2.RecipeSnapshot
		}
		grant := &exchangev2.RetentionGrant{Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention, Portability: exchangev2.GrantPortability, ObjectID: objectID, Sequence: sequence, PayloadSHA256: digest}
		body, err := serializeFederationV2Event(origin, newID(), eventType, sequence, time.Now(), source, serialized, grant)
		if err != nil {
			return err
		}
		var event exchangev2.Event
		event, err = exchangev2.Decode(body)
		if err != nil {
			return err
		}
		var count, bytesUsed, totalCount, totalBytes int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM federation_outbox WHERE peer_origin=?`, peer).Scan(&count, &bytesUsed); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM federation_outbox`).Scan(&totalCount, &totalBytes); err != nil {
			return err
		}
		if count >= 1000 || int64(bytesUsed)+int64(len(body)) > 16<<20 || totalCount >= 10000 || int64(totalBytes)+int64(len(body)) > 64<<20 {
			return errors.New("federation outbox capacity reached")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at) VALUES(?,?,?,?,?,?,?,?)`, newID(), peer, event.EventID, event.ObjectID, event.Sequence, body, time.Now().Unix(), timestamp()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO federation_local_peer_objects(peer_origin,recipe_id,published,updated_at) VALUES(?,?,1,?) ON CONFLICT(peer_origin,recipe_id) DO UPDATE SET published=1,updated_at=excluded.updated_at`, peer, recipeID, timestamp()); err != nil {
			return err
		}
	}
	return nil
}

func queueFederationSnapshotsForAll(tx *sql.Tx, newlyEnabledPeer string) error {
	var federation int
	if err := tx.QueryRow(`SELECT federation_enabled FROM instance_settings WHERE id=1`).Scan(&federation); err != nil {
		return err
	}
	if federation != 1 {
		return nil
	}
	var enabled int
	if err := tx.QueryRow(`SELECT enabled FROM federation_peers WHERE origin=?`, newlyEnabledPeer).Scan(&enabled); err != nil || enabled != 1 {
		return errors.New("federation peer is not enabled")
	}
	rows, err := tx.Query(`SELECT id FROM recipes WHERE (visibility='public' AND deleted_at IS NULL) OR id IN (SELECT recipe_id FROM federation_local_peer_objects WHERE peer_origin=? AND published=1) ORDER BY id`, newlyEnabledPeer)
	if err != nil {
		return err
	}
	var recipeIDs []string
	for rows.Next() {
		var recipeID string
		if err := rows.Scan(&recipeID); err != nil {
			rows.Close()
			return err
		}
		recipeIDs = append(recipeIDs, recipeID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, recipeID := range recipeIDs {
		if err := enqueueFederationChangeForPeer(tx, recipeID, newlyEnabledPeer, true); err != nil {
			return err
		}
	}
	return nil
}
