package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

type instanceAuthorityResponse struct {
	IsInstanceOwner           bool `json:"is_instance_owner"`
	IsAdmin                   bool `json:"is_admin"`
	OwnerClaimed              bool `json:"owner_claimed"`
	CanManageInstanceRoles    bool `json:"can_manage_instance_roles"`
	CanManageInstanceSettings bool `json:"can_manage_instance_settings"`
}

type instanceOwnerTransferResponse struct {
	ID        string `json:"id"`
	ToUserID  string `json:"to_user_id"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

func canManageInstanceRoles(ctx context.Context, db *sql.DB, actorID string) (bool, error) {
	capabilities, err := InstanceOwnerCapabilitiesFor(ctx, db, actorID)
	if err != nil {
		return false, err
	}
	if capabilities.OwnerClaimed {
		return capabilities.IsInstanceOwner, nil
	}
	return capabilities.IsAdmin, nil
}

func canManageInstanceSettings(ctx context.Context, db *sql.DB, actorID string) (bool, error) {
	capabilities, err := InstanceOwnerCapabilitiesFor(ctx, db, actorID)
	if err != nil {
		return false, err
	}
	return capabilities.CanManageInstanceSettings(), nil
}

func canManageInstanceRolesExec(ctx context.Context, exec sqliteExecutor, actorID string) (bool, error) {
	capabilities, err := instanceOwnerCapabilitiesForExec(ctx, exec, actorID)
	if err != nil {
		return false, err
	}
	if capabilities.OwnerClaimed {
		return capabilities.IsInstanceOwner, nil
	}
	return capabilities.IsAdmin, nil
}

func instanceOwnerCapabilitiesForExec(ctx context.Context, exec sqliteExecutor, accountID string) (InstanceOwnerCapabilities, error) {
	if strings.TrimSpace(accountID) == "" {
		return InstanceOwnerCapabilities{}, ErrOwnerAccountNotFound
	}
	var isAdmin int
	var ownerUserID sql.NullString
	var generation int64
	err := exec.QueryRowContext(ctx, `SELECT u.is_admin,a.owner_user_id,a.generation
		FROM users u CROSS JOIN instance_authority a
		WHERE u.id=? AND a.singleton_id=1`, accountID).Scan(&isAdmin, &ownerUserID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		var authorityExists int
		if checkErr := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_authority WHERE singleton_id=1`).Scan(&authorityExists); checkErr != nil {
			return InstanceOwnerCapabilities{}, checkErr
		}
		if authorityExists == 0 {
			return InstanceOwnerCapabilities{}, ErrInstanceAuthorityMissing
		}
		return InstanceOwnerCapabilities{}, ErrOwnerAccountNotFound
	}
	if err != nil {
		return InstanceOwnerCapabilities{}, err
	}
	return InstanceOwnerCapabilities{
		IsInstanceOwner: ownerUserID.Valid && ownerUserID.String == accountID,
		IsAdmin:         isAdmin == 1,
		OwnerClaimed:    ownerUserID.Valid,
		Generation:      generation,
	}, nil
}

func canViewAdminUsers(ctx context.Context, db *sql.DB, actorID string) (bool, error) {
	capabilities, err := InstanceOwnerCapabilitiesFor(ctx, db, actorID)
	if err != nil {
		return false, err
	}
	return capabilities.IsAdmin || capabilities.IsInstanceOwner, nil
}

func canManageFederationIdentity(ctx context.Context, db *sql.DB, actorID string) (bool, error) {
	return canManageInstanceRoles(ctx, db, actorID)
}

func (h apiHandler) instanceOwnerAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actorID := authenticatedUser(r)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/instance/authority":
		capabilities, err := InstanceOwnerCapabilitiesFor(ctx, h.db, actorID)
		if err != nil {
			writeOwnerAPIError(w, err)
			return
		}
		canManageRoles, err := canManageInstanceRoles(ctx, h.db, actorID)
		if err != nil {
			writeOwnerAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, instanceAuthorityResponse{
			IsInstanceOwner:           capabilities.IsInstanceOwner,
			IsAdmin:                   capabilities.IsAdmin,
			OwnerClaimed:              capabilities.OwnerClaimed,
			CanManageInstanceRoles:    canManageRoles,
			CanManageInstanceSettings: capabilities.CanManageInstanceSettings(),
		})
	case r.Method == http.MethodPost && r.URL.Path == "/api/instance/owner-transfers":
		var input struct {
			TargetUserID string `json:"target_user_id"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		transfer, err := CreateInstanceOwnerTransfer(ctx, h.db, actorID, input.TargetUserID)
		if err != nil {
			writeOwnerAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, ownerTransferResponse(transfer))
	case r.Method == http.MethodPost && r.PathValue("id") != "" && strings.HasSuffix(r.URL.Path, "/accept"):
		authority, err := AcceptInstanceOwnerTransfer(ctx, h.db, actorID, r.PathValue("id"))
		if err != nil {
			writeOwnerAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"owner_claimed": authority.Claimed(), "generation": authority.Generation})
	case r.Method == http.MethodDelete && r.PathValue("id") != "":
		if err := CancelInstanceOwnerTransfer(ctx, h.db, actorID, r.PathValue("id")); err != nil {
			writeOwnerAPIError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func ownerTransferResponse(transfer InstanceOwnerTransfer) instanceOwnerTransferResponse {
	return instanceOwnerTransferResponse{
		ID:        transfer.ID,
		ToUserID:  transfer.ToUserID,
		State:     string(transfer.State),
		CreatedAt: formatOwnerTime(transfer.CreatedAt),
		ExpiresAt: formatOwnerTime(transfer.ExpiresAt),
	}
}

func writeOwnerAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrOwnerAccountNotFound), errors.Is(err, ErrOwnerTransferNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrInstanceOwnerUnauthorized):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, ErrInstanceOwnerUnclaimed), errors.Is(err, ErrInstanceOwnerAlreadySet), errors.Is(err, ErrOwnerTransferPending), errors.Is(err, ErrOwnerTransferNotPending), errors.Is(err, ErrOwnerTransferStale):
		writeError(w, http.StatusConflict, "owner_state_conflict")
	case errors.Is(err, ErrOwnerTransferSelf):
		writeError(w, http.StatusBadRequest, "invalid_transfer_target")
	case errors.Is(err, ErrOwnerTransferExpired):
		writeError(w, http.StatusGone, "owner_transfer_expired")
	case errors.Is(err, ErrInstanceAuthorityMissing):
		writeError(w, http.StatusInternalServerError, "storage_error")
	default:
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
	}
}
