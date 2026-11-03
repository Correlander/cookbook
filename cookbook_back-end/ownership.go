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
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	record, err := readRecipePublicationRecord(r.Context(), tx, recipeID)
	if errors.Is(err, sql.ErrNoRows) || (record.OwnerID != "" && (record.Deleted || record.OwnerID != ownerID)) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE recipes SET owner_id=? WHERE id=? AND owner_id=? AND deleted_at IS NULL`, recipientID, recipeID, ownerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if changed != 1 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err := advanceLifecyclePolicy(r.Context(), tx, record, ownerID); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := insertAuditEvent(tx, ownerID, "recipe.ownership.transfer", "recipes", recipeID, http.StatusOK); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := enqueueFederationChange(tx, recipeID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	markTransactionalAuditSuccess(r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ownership_transferred"})
}
