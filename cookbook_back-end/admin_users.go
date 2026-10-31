package main

import (
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

func (h apiHandler) adminUsers(w http.ResponseWriter, r *http.Request) {
	if !isAdministrator(h.db, authenticatedUser(r)) {
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
	if !isAdministrator(h.db, authenticatedUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
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
	actorID := authenticatedUser(r)
	if id == actorID {
		writeError(w, http.StatusConflict, "cannot_change_own_role")
		return
	}
	var current adminUser
	var isAdmin int
	if err := h.db.QueryRow(`SELECT id,email,display_name,is_admin,created_at FROM users WHERE id=?`, id).Scan(&current.ID, &current.Email, &current.DisplayName, &isAdmin, &current.CreatedAt); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err := setUserAdministrator(h.db, id, *input.IsAdmin); errors.Is(err, errLastAdministrator) {
		writeError(w, http.StatusConflict, "last_administrator_required")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	current.IsAdmin = *input.IsAdmin
	writeJSON(w, http.StatusOK, current)
}

func setUserAdministrator(db *sql.DB, userID string, isAdmin bool) error {
	if isAdmin {
		_, err := db.Exec(`UPDATE users SET is_admin=1 WHERE id=?`, userID)
		return err
	}
	result, err := db.Exec(`UPDATE users SET is_admin=0 WHERE id=? AND is_admin=1 AND (SELECT COUNT(*) FROM users WHERE is_admin=1)>1`, userID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed > 0 {
		return err
	}
	var current int
	if err := db.QueryRow(`SELECT is_admin FROM users WHERE id=?`, userID).Scan(&current); err != nil {
		return err
	}
	if current == 1 {
		return errLastAdministrator
	}
	return nil
}
