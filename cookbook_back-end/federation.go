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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var previousDigest []byte
	var previousStatus int
	var previousResponse string
	err = tx.QueryRowContext(ctx, `SELECT body_digest,response_status,response_body FROM federation_received_events WHERE source_origin=? AND event_id=?`, keyOrigin, event.EventID).Scan(&previousDigest, &previousStatus, &previousResponse)
	if err == nil {
		if !bytes.Equal(previousDigest, digest[:]) {
			return 0, nil, errFederationConflict
		}
		if err := tx.Commit(); err != nil {
			return 0, nil, err
		}
		return previousStatus, []byte(previousResponse), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	var peerEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM federation_peers WHERE origin=?`, keyOrigin).Scan(&peerEnabled); err != nil || peerEnabled != 1 {
		return 0, nil, errors.New("federation peer is disabled")
	}
	var latestSequence int64
	var deleted int
	err = tx.QueryRowContext(ctx, `SELECT sequence,deleted FROM federation_remote_recipes WHERE object_id=? AND source_origin=?`, event.ObjectID, keyOrigin).Scan(&latestSequence, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		if event.EventType != federationEventSnapshot {
			return 0, nil, errFederationConflict
		}
		latestSequence = 0
	} else if err != nil {
		return 0, nil, err
	}
	if event.Sequence <= latestSequence || (event.EventType == federationEventUpdate && deleted == 1) {
		return 0, nil, errFederationConflict
	}
	if event.EventType == federationEventTombstone {
		_, err = tx.ExecContext(ctx, `UPDATE federation_remote_recipes SET sequence=?,creator_name='',recipe_json='',deleted=1,updated_at=? WHERE object_id=? AND source_origin=?`, event.Sequence, timestamp(), event.ObjectID, keyOrigin)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?) ON CONFLICT(object_id) DO UPDATE SET sequence=excluded.sequence,creator_name=excluded.creator_name,recipe_json=excluded.recipe_json,deleted=0,updated_at=excluded.updated_at`, event.ObjectID, keyOrigin, strings.Split(event.ObjectID, "/federation/recipes/")[1], event.Sequence, event.Attribution.CreatorName, string(recipeJSON), timestamp())
	}
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body) VALUES(?,?,?,?,?,?,?,?)`, keyOrigin, event.EventID, digest[:], event.ObjectID, event.Sequence, timestamp(), http.StatusAccepted, string(response))
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, response, nil
}

func enqueueFederationChange(tx *sql.Tx, recipeID string) error {
	return enqueueFederationChangeForPeer(tx, recipeID, "", false)
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
	if err := json.Unmarshal([]byte(ingredientsJSON), &source.Ingredients); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(stepsJSON), &source.Steps); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &source.Tags); err != nil {
		return err
	}
	source.Deleted = deletedAt != ""
	var sequence int64
	err := tx.QueryRow(`SELECT sequence FROM federation_local_objects WHERE recipe_id=?`, recipeID).Scan(&sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	eligible := federation == 1 && instancePublic == 1 && origin != "" && source.Visibility == visibilityPublic && !source.Deleted
	peerQuery := `SELECT origin FROM federation_peers WHERE enabled=1 ORDER BY origin`
	peerArgs := []any{}
	if targetPeer != "" {
		peerQuery = `SELECT origin FROM federation_peers WHERE enabled=1 AND origin=? ORDER BY origin`
		peerArgs = append(peerArgs, targetPeer)
	}
	rows, err := tx.Query(peerQuery, peerArgs...)
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
	type eventTarget struct {
		peer      string
		eventType string
	}
	targets := make([]eventTarget, 0, len(peers))
	for _, peer := range peers {
		var peerPublished int
		peerErr := tx.QueryRow(`SELECT published FROM federation_local_peer_objects WHERE peer_origin=? AND recipe_id=?`, peer, recipeID).Scan(&peerPublished)
		if peerErr != nil && !errors.Is(peerErr, sql.ErrNoRows) {
			return peerErr
		}
		eventType := ""
		if eligible {
			if (targetPeer == peer && forceSnapshot) || peerPublished == 0 {
				eventType = federationEventSnapshot
			} else {
				eventType = federationEventUpdate
			}
		} else if peerPublished == 1 {
			eventType = federationEventTombstone
		}
		if eventType != "" {
			targets = append(targets, eventTarget{peer: peer, eventType: eventType})
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
