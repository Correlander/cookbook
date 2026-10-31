package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

type federationPeerAdminItem struct {
	Origin    string                   `json:"origin"`
	Enabled   bool                     `json:"enabled"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
	Keys      []federationPeerAdminKey `json:"keys"`
}

type federationPeerAdminKey struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

func (h apiHandler) federationIdentity(w http.ResponseWriter, r *http.Request) {
	if !isAdministrator(h.db, authenticatedUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var origin string
	if err := h.db.QueryRow(`SELECT federation_origin FROM instance_settings WHERE id=1`).Scan(&origin); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	key, err := loadOrCreateFederationSigningKey(h.federationKeyDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "federation_key_unavailable")
		return
	}
	identity := map[string]string{"origin": origin, "key_id": key.ID, "public_key": key.publicKeyBase64()}
	pending, pendingErr := readFederationSigningKey(federationPendingKeyPath(h.federationKeyDir))
	if pendingErr == nil {
		identity["pending_key_id"] = pending.ID
		identity["pending_public_key"] = pending.publicKeyBase64()
	} else if !errors.Is(pendingErr, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, "federation_key_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, identity)
}

func (h apiHandler) federationIdentityRotation(w http.ResponseWriter, r *http.Request) {
	if !isAdministrator(h.db, authenticatedUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var input struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var key federationSigningKey
	var err error
	switch input.Action {
	case "stage":
		key, err = stageFederationKeyRotation(h.federationKeyDir)
		if err != nil {
			if errors.Is(err, errFederationRotationPending) {
				writeError(w, http.StatusConflict, "federation_rotation_already_pending")
			} else {
				writeError(w, http.StatusInternalServerError, "federation_key_unavailable")
			}
			return
		}
	case "activate":
		key, err = activateFederationKeyRotation(h.federationKeyDir)
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusConflict, "pending_federation_key_required")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "federation_key_unavailable")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_rotation_action")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key_id": key.ID, "public_key": key.publicKeyBase64()})
}

func (h apiHandler) federationPeers(w http.ResponseWriter, r *http.Request) {
	if !isAdministrator(h.db, authenticatedUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query(`SELECT origin,enabled,created_at,updated_at FROM federation_peers ORDER BY origin`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		peers := []federationPeerAdminItem{}
		for rows.Next() {
			var peer federationPeerAdminItem
			var enabled int
			if err := rows.Scan(&peer.Origin, &enabled, &peer.CreatedAt, &peer.UpdatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			peer.Enabled = enabled == 1
			peer.Keys = []federationPeerAdminKey{}
			peers = append(peers, peer)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := rows.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		for i := range peers {
			keyRows, err := h.db.Query(`SELECT key_id,public_key,status,created_at,COALESCE(revoked_at,'') FROM federation_peer_keys WHERE origin=? ORDER BY created_at,key_id`, peers[i].Origin)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			for keyRows.Next() {
				var key federationPeerAdminKey
				var publicKey []byte
				if err := keyRows.Scan(&key.KeyID, &publicKey, &key.Status, &key.CreatedAt, &key.RevokedAt); err != nil {
					keyRows.Close()
					writeError(w, http.StatusInternalServerError, "storage_error")
					return
				}
				key.PublicKey = base64.StdEncoding.EncodeToString(publicKey)
				peers[i].Keys = append(peers[i].Keys, key)
			}
			if err := keyRows.Err(); err != nil {
				keyRows.Close()
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			keyRows.Close()
		}
		writeJSON(w, http.StatusOK, peers)
	case http.MethodPost:
		var input struct {
			Origin  string `json:"origin"`
			Enabled *bool  `json:"enabled"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled_required")
			return
		}
		origin, err := canonicalFederationOrigin(input.Origin)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_peer")
			return
		}
		tx, err := h.db.Begin()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		defer tx.Rollback()
		var priorEnabled int
		priorErr := tx.QueryRow(`SELECT enabled FROM federation_peers WHERE origin=?`, origin).Scan(&priorEnabled)
		if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if *input.Enabled && priorEnabled != 1 {
			var activeKeys int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM federation_peer_keys WHERE origin=? AND status='active'`, origin).Scan(&activeKeys); err != nil || activeKeys == 0 {
				writeError(w, http.StatusConflict, "active_peer_key_required")
				return
			}
		}
		state := priorEnabled
		if *input.Enabled {
			state = 1
		}
		if _, err := tx.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(origin) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`, origin, state, timestamp(), timestamp()); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if *input.Enabled && priorEnabled != 1 {
			if err := queueFederationSnapshotsForAll(tx, origin); err != nil {
				_ = tx.Rollback()
				writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
				return
			}
		}
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "peer_saved"})
	case http.MethodDelete:
		var input struct {
			Origin string `json:"origin"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		origin, err := canonicalFederationOrigin(input.Origin)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_peer")
			return
		}
		tx, err := h.db.Begin()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		result, err := tx.Exec(`UPDATE federation_peers SET enabled=0,updated_at=? WHERE origin=?`, timestamp(), origin)
		if err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			_ = tx.Rollback()
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if _, err := tx.Exec(`DELETE FROM federation_outbox WHERE peer_origin=?`, origin); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "peer_disabled"})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) federationPeerKeys(w http.ResponseWriter, r *http.Request) {
	if !isAdministrator(h.db, authenticatedUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	var input struct {
		Origin    string `json:"origin"`
		KeyID     string `json:"key_id"`
		PublicKey string `json:"public_key"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	origin, err := canonicalFederationOrigin(input.Origin)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_peer")
		return
	}
	switch r.Method {
	case http.MethodPost:
		publicKey, err := base64.StdEncoding.DecodeString(input.PublicKey)
		if err != nil || len(publicKey) != 32 {
			writeError(w, http.StatusBadRequest, "invalid_public_key")
			return
		}
		if err := registerFederationPeerKey(h.db, origin, input.KeyID, publicKey); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "peer_not_found")
			} else {
				writeError(w, http.StatusBadRequest, "invalid_peer_key")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "peer_key_activated"})
	case http.MethodDelete:
		if err := revokeFederationPeerKey(h.db, origin, input.KeyID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found")
			} else {
				writeError(w, http.StatusInternalServerError, "storage_error")
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "peer_key_revoked", "updated_at": time.Now().UTC().Format(time.RFC3339Nano)})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) federationOutbox(w http.ResponseWriter, r *http.Request) {
	if !isAdministrator(h.db, authenticatedUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if id != "" {
			var item struct {
				ID         string          `json:"id"`
				PeerOrigin string          `json:"peer_origin"`
				EventID    string          `json:"event_id"`
				ObjectID   string          `json:"object_id"`
				Sequence   int64           `json:"sequence"`
				Attempts   int             `json:"attempts"`
				NextTry    int64           `json:"next_attempt"`
				LastError  string          `json:"last_error"`
				CreatedAt  string          `json:"created_at"`
				Payload    json.RawMessage `json:"payload"`
			}
			if err := h.db.QueryRow(`SELECT id,peer_origin,event_id,object_id,sequence,attempts,next_attempt,last_error,created_at,payload FROM federation_outbox WHERE id=?`, id).Scan(&item.ID, &item.PeerOrigin, &item.EventID, &item.ObjectID, &item.Sequence, &item.Attempts, &item.NextTry, &item.LastError, &item.CreatedAt, &item.Payload); err != nil {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
			writeJSON(w, http.StatusOK, item)
			return
		}
		rows, err := h.db.Query(`SELECT id,peer_origin,event_id,object_id,sequence,attempts,next_attempt,last_error,created_at FROM federation_outbox ORDER BY created_at DESC,id LIMIT 100`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		defer rows.Close()
		type item struct {
			ID         string `json:"id"`
			PeerOrigin string `json:"peer_origin"`
			EventID    string `json:"event_id"`
			ObjectID   string `json:"object_id"`
			Sequence   int64  `json:"sequence"`
			Attempts   int    `json:"attempts"`
			NextTry    int64  `json:"next_attempt"`
			LastError  string `json:"last_error"`
			CreatedAt  string `json:"created_at"`
		}
		items := []item{}
		for rows.Next() {
			var current item
			if err := rows.Scan(&current.ID, &current.PeerOrigin, &current.EventID, &current.ObjectID, &current.Sequence, &current.Attempts, &current.NextTry, &current.LastError, &current.CreatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			items = append(items, current)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var input struct {
			ID string `json:"id"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		result, err := h.db.Exec(`UPDATE federation_outbox SET attempts=0,next_attempt=?,lease_until=NULL,last_error='' WHERE id=?`, time.Now().Unix(), input.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "delivery_retried"})
	case http.MethodDelete:
		var input struct {
			ID string `json:"id"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		tx, err := h.db.Begin()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		defer tx.Rollback()
		var objectID string
		if err := tx.QueryRow(`SELECT object_id FROM federation_outbox WHERE id=?`, input.ID).Scan(&objectID); err != nil {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		result, err := tx.Exec(`DELETE FROM federation_outbox WHERE object_id=?`, objectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		parts := strings.Split(objectID, "/federation/recipes/")
		if len(parts) == 2 && isFederationID(parts[1]) {
			if _, err := tx.Exec(`UPDATE federation_local_objects SET published=0 WHERE recipe_id=?`, parts[1]); err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			if _, err := tx.Exec(`UPDATE federation_local_peer_objects SET published=0,updated_at=? WHERE recipe_id=?`, timestamp(), parts[1]); err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
		}
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "delivery_discarded"})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}
