package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/common-fate/httpsig"
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
	federationSignatureMiddleware(h.db, h.origin, h.trustedProxies, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		status, response, err := acceptFederationEvent(r.Context(), h.db, peerOrigin, body)
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
			writeError(w, http.StatusBadRequest, "invalid_federation_event")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(response)
	})).ServeHTTP(w, r)
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
	event, recipeJSON, err := decodeFederationEvent(body)
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
		var peerEnabled int
		if err := tx.QueryRowContext(ctx, `SELECT enabled FROM federation_peers WHERE origin=?`, keyOrigin).Scan(&peerEnabled); err != nil || peerEnabled != 1 {
			return errors.New("federation peer is disabled")
		}
		var latestSequence int64
		var priorDeleted int
		var priorStateKnown int
		var priorObjectBytes int64
		err = tx.QueryRowContext(ctx, `SELECT high_water_sequence,deleted,state_known,logical_bytes FROM federation_remote_catalog WHERE object_id=? AND source_origin=?`, event.ObjectID, keyOrigin).Scan(&latestSequence, &priorDeleted, &priorStateKnown, &priorObjectBytes)
		catalogExists := err == nil
		if errors.Is(err, sql.ErrNoRows) {
			if event.EventType != federationEventSnapshot {
				return errFederationConflict
			}
			latestSequence = 0
		} else if err != nil {
			return err
		}
		if event.Sequence <= latestSequence || (event.EventType == federationEventUpdate && (priorDeleted == 1 || priorStateKnown == 0)) {
			return errFederationConflict
		}
		now := timestamp()
		recipeID := strings.TrimPrefix(event.ObjectID, keyOrigin+"/federation/recipes/")
		creatorName, rawRecipe := "", ""
		deleted := 1
		if event.EventType != federationEventTombstone {
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
		if deleted == 1 {
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
		if err := closeFederationRetentionReceipt(ctx, tx, keyOrigin, event.ObjectID); err != nil {
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

func hasPendingFederationContentForRecipe(tx *sql.Tx, peer, recipeID string) (bool, error) {
	rows, err := tx.Query(`SELECT payload FROM federation_outbox WHERE peer_origin=? AND object_id LIKE ? ORDER BY sequence,id`, peer, "%/federation/recipes/"+recipeID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		event, _, err := decodeFederationEvent(payload)
		if err != nil {
			return false, err
		}
		if strings.HasSuffix(event.ObjectID, "/federation/recipes/"+recipeID) &&
			(event.EventType == federationEventSnapshot || event.EventType == federationEventUpdate) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func reconcilePendingFederationRows(tx *sql.Tx, peer, objectID string, now int64, prune bool) (int64, int64, error) {
	rows, err := tx.Query(`SELECT id,payload,lease_until FROM federation_outbox WHERE peer_origin=? AND object_id=? ORDER BY sequence,id`, peer, objectID)
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
		event, _, err := decodeFederationEvent(row.payload)
		if err != nil {
			rows.Close()
			return 0, 0, err
		}
		if event.EventType == federationEventTombstone {
			if event.Sequence > highestTombstoneSequence {
				highestTombstoneSequence = event.Sequence
			}
			continue
		}
		if event.EventType != federationEventSnapshot && event.EventType != federationEventUpdate {
			continue
		}
		if event.Sequence > highestContentSequence {
			highestContentSequence = event.Sequence
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
		if _, err := tx.Exec(`DELETE FROM federation_outbox WHERE id=?`, id); err != nil {
			return 0, 0, err
		}
	}
	return highestContentSequence, highestTombstoneSequence, nil
}

func enqueueFederationChangeForPeer(tx *sql.Tx, recipeID, targetPeer string, forceSnapshot bool) error {
	var federation, instancePublic int
	var origin string
	if err := tx.QueryRow(`SELECT federation_enabled,public_instance,federation_origin FROM instance_settings WHERE id=1`).Scan(&federation, &instancePublic, &origin); err != nil {
		return err
	}
	var source federationRecipeSource
	var ingredientsJSON, stepsJSON, tagsJSON, deletedAt string
	if err := tx.QueryRow(`SELECT id,owner_id,creator_name,source_recipe_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,image_path,COALESCE(deleted_at,'') FROM recipes WHERE id=?`, recipeID).Scan(&source.ID, &source.OwnerID, &source.CreatorName, &source.SourceRecipeID, &source.Title, &source.Description, &ingredientsJSON, &stepsJSON, &source.Servings, &source.PrepTime, &source.CookTime, &tagsJSON, &source.Visibility, &source.ImagePath, &deletedAt); err != nil {
		return err
	}
	source.Deleted = deletedAt != ""
	var sequence int64
	err := tx.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, recipeID).Scan(&sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	policyEligible, err := federationRecipePublicationEligible(context.Background(), tx, recipeID, origin)
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
	peerQuery := `SELECT origin,enabled FROM federation_peers ORDER BY origin`
	peerArgs := []any{}
	if targetPeer != "" {
		peerQuery = `SELECT origin,enabled FROM federation_peers WHERE origin=? ORDER BY origin`
		peerArgs = append(peerArgs, targetPeer)
	}
	rows, err := tx.Query(peerQuery, peerArgs...)
	if err != nil {
		return err
	}
	type federationPeer struct {
		origin  string
		enabled bool
	}
	var peers []federationPeer
	for rows.Next() {
		var peer federationPeer
		var enabled int
		if err := rows.Scan(&peer.origin, &enabled); err != nil {
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
		peerErr := tx.QueryRow(`SELECT published FROM federation_local_peer_objects WHERE peer_origin=? AND recipe_id=?`, peer.origin, recipeID).Scan(&peerPublished)
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
				pendingContentSequence, pendingTombstoneSequence, err := reconcilePendingFederationRows(tx, peer.origin, objectID, time.Now().Unix(), true)
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
				pendingContent, err := hasPendingFederationContentForRecipe(tx, peer.origin, recipeID)
				if err != nil {
					return err
				}
				if pendingContent {
					return errors.New("federation origin is required to reconcile pending content")
				}
			}
		}
		if eventType != "" {
			targets = append(targets, eventTarget{peer: peer.origin, eventType: eventType})
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
	if _, err := tx.Exec(`INSERT INTO federation_local_objects(recipe_id,sequence,published) VALUES(?,?,?) ON CONFLICT(recipe_id) DO UPDATE SET sequence=excluded.sequence,published=excluded.published`, recipeID, sequence, publishedNow); err != nil {
		return err
	}
	for _, target := range targets {
		body, err := serializeFederationEvent(origin, newID(), target.eventType, sequence, time.Now(), federation == 1, instancePublic == 1, source)
		if err != nil {
			return err
		}
		var event federationEvent
		if err := json.Unmarshal(body, &event); err != nil {
			return err
		}
		peerPublished := 0
		if eligible {
			peerPublished = 1
		}
		if _, err := tx.Exec(`INSERT INTO federation_local_peer_objects(peer_origin,recipe_id,published,updated_at) VALUES(?,?,?,?) ON CONFLICT(peer_origin,recipe_id) DO UPDATE SET published=excluded.published,updated_at=excluded.updated_at`, target.peer, recipeID, peerPublished, timestamp()); err != nil {
			return err
		}
		var peerCount, peerBytes, totalCount, totalBytes int
		if err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM federation_outbox WHERE peer_origin=?`, target.peer).Scan(&peerCount, &peerBytes); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM federation_outbox`).Scan(&totalCount, &totalBytes); err != nil {
			return err
		}
		if peerCount >= 1000 || int64(peerBytes)+int64(len(body)) > 16<<20 || totalCount >= 10000 || int64(totalBytes)+int64(len(body)) > 64<<20 {
			return errors.New("federation outbox capacity reached")
		}
		if _, err := tx.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at) VALUES(?,?,?,?,?,?,?,?)`, newID(), target.peer, event.EventID, event.ObjectID, event.Sequence, body, time.Now().Unix(), timestamp()); err != nil {
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
