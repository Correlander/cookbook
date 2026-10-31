package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"strings"
)

func (h apiHandler) transferRecipe(w http.ResponseWriter, r *http.Request, recipeID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	ownerID := authenticatedUser(r)
	if ownerID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(input.Email))
	if err != nil || parsed.Address != strings.TrimSpace(input.Email) || len(parsed.Address) > 254 {
		writeError(w, http.StatusBadRequest, "invalid_email")
		return
	}
	var recipientID string
	if err := h.db.QueryRow(`SELECT id FROM users WHERE email=?`, strings.ToLower(parsed.Address)).Scan(&recipientID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusBadRequest, "recipient_not_found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if recipientID == ownerID {
		writeError(w, http.StatusBadRequest, "recipient_is_current_owner")
		return
	}
	result, err := h.db.Exec(`UPDATE recipes SET owner_id=? WHERE id=? AND owner_id=? AND deleted_at IS NULL`, recipientID, recipeID, ownerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if changed != 1 {
		var owner string
		err := h.db.QueryRow(`SELECT owner_id FROM recipes WHERE id=? AND deleted_at IS NULL`, recipeID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) || owner != ownerID {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		writeError(w, http.StatusConflict, "ownership_unchanged")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ownership_transferred"})
}
