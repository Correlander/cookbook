package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
)

type sessionHandler struct {
	db            *sql.DB
	secureCookies bool
}

type sessionSummary struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	UserAgent string `json:"user_agent,omitempty"`
	Current   bool   `json:"current"`
}

func (h sessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/auth/sessions":
		h.list(w, r)
	case r.Method == http.MethodDelete && r.PathValue("id") != "":
		h.revoke(w, r, r.PathValue("id"))
	default:
		http.NotFound(w, r)
	}
}

func (h sessionHandler) list(w http.ResponseWriter, r *http.Request) {
	uid := authenticatedUser(r)
	cookie, err := r.Cookie(sessionCookie)
	if uid == "" || err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	currentHash := sessionTokenHash(cookie.Value)
	rows, err := h.db.Query(`SELECT id,created_at,expires_at,user_agent,token_hash FROM sessions WHERE user_id=? AND expires_at>? ORDER BY created_at DESC,id`, uid, timestamp())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []sessionSummary{}
	for rows.Next() {
		var item sessionSummary
		var tokenHash string
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.ExpiresAt, &item.UserAgent, &tokenHash); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		item.Current = subtle.ConstantTimeCompare([]byte(tokenHash), []byte(currentHash)) == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h sessionHandler) revoke(w http.ResponseWriter, r *http.Request, id string) {
	if len(id) > 64 {
		writeError(w, http.StatusNotFound, "session_not_found")
		return
	}
	uid := authenticatedUser(r)
	var revokedHash string
	err := h.db.QueryRow(`DELETE FROM sessions WHERE id=? AND user_id=? RETURNING token_hash`, id, uid).Scan(&revokedHash)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "session_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		currentHash := sessionTokenHash(cookie.Value)
		if subtle.ConstantTimeCompare([]byte(currentHash), []byte(revokedHash)) == 1 {
			clearSessionCookie(w, h.secureCookies)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
