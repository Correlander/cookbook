package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ownerTransferReadResponse struct {
	Transfers []ownerTransferReadItem `json:"transfers"`
}

type ownerTransferReadItem struct {
	ID                      string `json:"id"`
	Direction               string `json:"direction"`
	State                   string `json:"state"`
	CreatedAt               string `json:"created_at"`
	ExpiresAt               string `json:"expires_at"`
	CounterpartyDisplayName string `json:"counterparty_display_name"`
}

type ownerTransferReadHandler struct {
	db *sql.DB
}

func (h ownerTransferReadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	transfers, err := readPendingOwnerTransfersForActor(r.Context(), h.db, authenticatedUser(r), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, ownerTransferReadResponse{Transfers: transfers})
}

// readPendingOwnerTransfersForActor returns only the current owner's one
// actionable transfer as seen by its initiator or exact target. The authority
// join hides pending rows made stale by an owner change without mutating them.
func readPendingOwnerTransfersForActor(ctx context.Context, db *sql.DB, actorID string, now time.Time) ([]ownerTransferReadItem, error) {
	items := make([]ownerTransferReadItem, 0, 1)
	if strings.TrimSpace(actorID) == "" {
		return items, nil
	}
	var id, fromUserID, toUserID, state, createdAt, expiresAt, counterpartyName string
	err := db.QueryRowContext(ctx, `SELECT t.id,t.from_user_id,t.to_user_id,t.state,t.created_at,t.expires_at,
		CASE WHEN t.from_user_id=? THEN target.display_name ELSE source.display_name END
		FROM instance_authority AS a
		JOIN instance_owner_transfers AS t ON t.state='pending' AND t.from_user_id=a.owner_user_id
		JOIN users AS source ON source.id=t.from_user_id
		JOIN users AS target ON target.id=t.to_user_id
		WHERE a.singleton_id=1 AND (t.from_user_id=? OR t.to_user_id=?)
		ORDER BY t.created_at,t.id LIMIT 1`, actorID, actorID, actorID).
		Scan(&id, &fromUserID, &toUserID, &state, &createdAt, &expiresAt, &counterpartyName)
	if err == sql.ErrNoRows {
		var authorityExists int
		if checkErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_authority WHERE singleton_id=1`).Scan(&authorityExists); checkErr != nil {
			return nil, fmt.Errorf("check instance authority for pending transfer read: %w", checkErr)
		}
		if authorityExists == 0 {
			return nil, ErrInstanceAuthorityMissing
		}
		return items, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read actor's pending owner transfer: %w", err)
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse owner transfer creation time: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse owner transfer expiry time: %w", err)
	}
	if !now.Before(expires) {
		return items, nil
	}
	direction := "incoming"
	if fromUserID == actorID {
		direction = "outgoing"
	} else if toUserID != actorID {
		// The SQL predicate constrains this already; fail closed if it changes.
		return items, nil
	}
	items = append(items, ownerTransferReadItem{
		ID:                      id,
		Direction:               direction,
		State:                   state,
		CreatedAt:               formatOwnerTime(created),
		ExpiresAt:               formatOwnerTime(expires),
		CounterpartyDisplayName: counterpartyName,
	})
	return items, nil
}
