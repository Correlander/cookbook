package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

type adminUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	IsAdmin     bool   `json:"is_admin"`
	CreatedAt   string `json:"created_at"`
}

var errLastAdministrator = errors.New("cannot remove the last administrator")
var errCannotChangeOwnRole = errors.New("cannot change own role")

func (h apiHandler) adminUsers(w http.ResponseWriter, r *http.Request) {
	canView, err := canViewAdminUsers(r.Context(), h.db, authenticatedUser(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if !canView {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	rows, err := h.db.Query(`SELECT id,email,display_name,is_admin,created_at FROM users ORDER BY created_at,id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []adminUser{}
	for rows.Next() {
		var item adminUser
		var isAdmin int
		if err := rows.Scan(&item.ID, &item.Email, &item.DisplayName, &isAdmin, &item.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		item.IsAdmin = isAdmin == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h apiHandler) adminUserByID(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var input struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.IsAdmin == nil {
		writeError(w, http.StatusBadRequest, "empty_user_update")
		return
	}
	current, err := updateUserAdministratorForActor(r.Context(), h.db, authenticatedUser(r), id, *input.IsAdmin)
	switch {
	case errors.Is(err, errLastAdministrator):
		writeError(w, http.StatusConflict, "last_administrator_required")
		return
	case errors.Is(err, errCannotChangeOwnRole):
		writeError(w, http.StatusConflict, "cannot_change_own_role")
		return
	case errors.Is(err, ErrOwnerAccountNotFound):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, ErrInstanceOwnerUnauthorized):
		writeError(w, http.StatusForbidden, "forbidden")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func updateUserAdministratorForActor(ctx context.Context, db *sql.DB, actorID, userID string, isAdmin bool) (adminUser, error) {
	return updateUserAdministratorForActorWithHook(ctx, db, actorID, userID, isAdmin, nil)
}

func updateUserAdministratorForActorWithHook(ctx context.Context, db *sql.DB, actorID, userID string, isAdmin bool, afterAuthorization func()) (current adminUser, err error) {
	err = withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		canManage, err := canManageInstanceRolesExec(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !canManage {
			return ErrInstanceOwnerUnauthorized
		}
		if afterAuthorization != nil {
			afterAuthorization()
		}
		if userID == actorID {
			return errCannotChangeOwnRole
		}
		var isAdminValue int
		if err := tx.QueryRowContext(ctx, `SELECT id,email,display_name,is_admin,created_at FROM users WHERE id=?`, userID).Scan(&current.ID, &current.Email, &current.DisplayName, &isAdminValue, &current.CreatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrOwnerAccountNotFound
			}
			return err
		}
		if err := setUserAdministratorTx(ctx, tx, userID, isAdmin); err != nil {
			return err
		}
		current.IsAdmin = isAdmin
		return nil
	})
	return current, err
}

func setUserAdministrator(db *sql.DB, userID string, isAdmin bool) error {
	return withInstanceOwnerWriteTx(context.Background(), db, func(tx migrationExecutor) error {
		return setUserAdministratorTx(context.Background(), tx, userID, isAdmin)
	})
}

func setUserAdministratorTx(ctx context.Context, tx migrationExecutor, userID string, isAdmin bool) error {
	if isAdmin {
		_, err := tx.ExecContext(ctx, `UPDATE users SET is_admin=1 WHERE id=?`, userID)
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET is_admin=0 WHERE id=? AND is_admin=1 AND (SELECT COUNT(*) FROM users WHERE is_admin=1)>1`, userID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed > 0 {
		return err
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id=?`, userID).Scan(&current); err != nil {
		return err
	}
	if current == 1 {
		return errLastAdministrator
	}
	return nil
}
