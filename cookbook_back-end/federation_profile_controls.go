package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

type federationPeerProfileInput struct {
	Origin                  string `json:"origin"`
	ExpectedExchangeProfile string `json:"expected_exchange_profile"`
	ExchangeProfile         string `json:"exchange_profile"`
}

func patchFederationPeerProfile(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var input federationPeerProfileInput
	if !decodeJSON(w, r, &input) {
		return
	}
	origin, err := canonicalFederationOrigin(input.Origin)
	if err != nil || origin != input.Origin {
		writeError(w, http.StatusBadRequest, "invalid_peer")
		return
	}
	if !validFederationExchangeProfile(input.ExpectedExchangeProfile) || !validFederationExchangeProfile(input.ExchangeProfile) {
		writeError(w, http.StatusBadRequest, "invalid_exchange_profile")
		return
	}
	err = withImmediateSQLiteWriteTx(r.Context(), db, func(tx sqliteExecutor) error {
		var isAdmin int
		if err := tx.QueryRowContext(r.Context(), `SELECT is_admin FROM users WHERE id=?`, authenticatedUser(r)).Scan(&isAdmin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errFederationPeerProfileForbidden
			}
			return err
		}
		if isAdmin != 1 {
			return errFederationPeerProfileForbidden
		}
		var current string
		if err := tx.QueryRowContext(r.Context(), `SELECT exchange_profile FROM federation_peers WHERE origin=?`, origin).Scan(&current); err != nil {
			return err
		}
		if current != input.ExpectedExchangeProfile {
			return errFederationPeerProfileConflict
		}
		if current == input.ExchangeProfile {
			return nil
		}
		var enabled int
		if err := tx.QueryRowContext(r.Context(), `SELECT enabled FROM federation_peers WHERE origin=?`, origin).Scan(&enabled); err != nil {
			return err
		}
		if current == federationExchangeV2Retention && input.ExchangeProfile == "v1" {
			if err := markFederationPeerOutboxObjectsForResync(r.Context(), tx, origin); err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM federation_outbox WHERE peer_origin=?`, origin); err != nil {
				return err
			}
			if err := resetFederationPeerExchangeProfileToV1(r.Context(), tx, origin); err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `UPDATE federation_peers SET updated_at=? WHERE origin=?`, timestamp(), origin); err != nil {
				return err
			}
		} else {
			if err := markFederationPeerOutboxObjectsForResync(r.Context(), tx, origin); err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `UPDATE federation_peers SET exchange_profile=?,updated_at=? WHERE origin=?`, input.ExchangeProfile, timestamp(), origin); err != nil {
				return err
			}
		}
		if enabled == 1 {
			if err := queueFederationSnapshotsForPeerExec(r.Context(), tx, origin); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "peer_not_found")
		return
	}
	if errors.Is(err, errFederationPeerProfileForbidden) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if errors.Is(err, errFederationPeerProfileConflict) {
		writeError(w, http.StatusConflict, "exchange_profile_conflict")
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "federation outbox capacity reached") {
			writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "peer_profile_updated", "exchange_profile": input.ExchangeProfile, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)})
}

var errFederationPeerProfileConflict = errors.New("federation peer profile changed")
var errFederationPeerProfileForbidden = errors.New("administrator permission changed")

func validFederationExchangeProfile(profile string) bool {
	return profile == "v1" || profile == federationExchangeV2Retention
}
