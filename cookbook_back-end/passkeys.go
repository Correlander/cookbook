package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type passkeyHandler struct {
	db            *sql.DB
	webauthn      *webauthn.WebAuthn
	secureCookies bool
}

type webAuthnUser struct {
	id          string
	email       string
	displayName string
	credentials []webauthn.Credential
}

func (u webAuthnUser) WebAuthnID() []byte          { return []byte(u.id) }
func (u webAuthnUser) WebAuthnName() string        { return u.email }
func (u webAuthnUser) WebAuthnDisplayName() string { return u.displayName }
func (u webAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

type storedWebAuthnCredential struct {
	Credential webauthn.Credential         `json:"credential"`
	Flags      protocol.AuthenticatorFlags `json:"flags"`
}

type passkeySummary struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	LastUsed  string `json:"last_used_at,omitempty"`
}

func (h passkeyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/passkey/register/begin":
		h.beginRegistration(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/passkey/register/finish":
		h.finishRegistration(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/passkey/login/begin":
		h.beginLogin(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/passkey/login/finish":
		h.finishLogin(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/auth/passkeys":
		h.list(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/auth/passkeys/"):
		h.remove(w, r, strings.TrimPrefix(r.URL.Path, "/api/auth/passkeys/"))
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

func (h passkeyHandler) beginRegistration(w http.ResponseWriter, r *http.Request) {
	user, ok := h.loadUser(authenticatedUser(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	creation, session, err := h.webauthn.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: boolPointer(true),
			UserVerification:   protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(webauthn.Credentials(user.credentials).CredentialDescriptors()),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "passkey_unavailable")
		return
	}
	id, err := h.saveCeremony(user.id, "registration", session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "options": creation})
}

func (h passkeyHandler) finishRegistration(w http.ResponseWriter, r *http.Request) {
	user, ok := h.loadUser(authenticatedUser(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	session, ok := h.consumeCeremony(w, r, user.id, "registration")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	credential, err := h.webauthn.FinishRegistration(user, session, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_passkey_response")
		return
	}
	if err := h.storeCredential(user.id, *credential); err != nil {
		if errors.Is(err, errPasskeyAlreadyRegistered) {
			writeError(w, http.StatusConflict, "passkey_already_registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "passkey_registered"})
}

func (h passkeyHandler) beginLogin(w http.ResponseWriter, r *http.Request) {
	options, session, err := h.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "passkey_unavailable")
		return
	}
	id, err := h.saveCeremony("", "login", session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony_id": id, "options": options})
}

func (h passkeyHandler) finishLogin(w http.ResponseWriter, r *http.Request) {
	session, ok := h.consumeCeremony(w, r, "", "login")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	user, credential, err := h.webauthn.FinishPasskeyLogin(h.loadDiscoverableUser, session, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "passkey_sign_in_failed")
		return
	}
	if err := h.updateCredential(user.WebAuthnID(), *credential); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	token, expires, err := issueSession(h.db, string(user.WebAuthnID()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	setSessionCookie(w, token, expires, h.secureCookies)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed_in"})
}

func (h passkeyHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT credential_id,created_at,COALESCE(last_used_at,'') FROM webauthn_credentials WHERE user_id=? ORDER BY created_at`, authenticatedUser(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []passkeySummary{}
	for rows.Next() {
		var id []byte
		var item passkeySummary
		if err := rows.Scan(&id, &item.CreatedAt, &item.LastUsed); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		item.ID = hex.EncodeToString(id)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h passkeyHandler) remove(w http.ResponseWriter, r *http.Request, id string) {
	credentialID, err := hex.DecodeString(id)
	if err != nil || len(credentialID) == 0 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	result, err := h.db.Exec(`DELETE FROM webauthn_credentials WHERE credential_id=? AND user_id=?`, credentialID, authenticatedUser(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "passkey_removed"})
}

func (h passkeyHandler) loadUser(id string) (webAuthnUser, bool) {
	var user webAuthnUser
	if id == "" || h.db.QueryRow(`SELECT id,email,display_name FROM users WHERE id=?`, id).Scan(&user.id, &user.email, &user.displayName) != nil {
		return webAuthnUser{}, false
	}
	rows, err := h.db.Query(`SELECT credential_json FROM webauthn_credentials WHERE user_id=?`, id)
	if err != nil {
		return webAuthnUser{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		var stored storedWebAuthnCredential
		if rows.Scan(&encoded) != nil || json.Unmarshal([]byte(encoded), &stored) != nil {
			return webAuthnUser{}, false
		}
		stored.Credential.Flags = webauthn.NewCredentialFlags(stored.Flags)
		user.credentials = append(user.credentials, stored.Credential)
	}
	return user, rows.Err() == nil
}

func (h passkeyHandler) loadDiscoverableUser(rawID, userHandle []byte) (webauthn.User, error) {
	if len(userHandle) == 0 {
		return nil, errors.New("missing discoverable user handle")
	}
	user, ok := h.loadUser(string(userHandle))
	if !ok {
		return nil, errors.New("unknown user")
	}
	for _, credential := range user.credentials {
		if subtle.ConstantTimeCompare(credential.ID, rawID) == 1 {
			return user, nil
		}
	}
	return nil, errors.New("credential does not belong to user handle")
}

func (h passkeyHandler) saveCeremony(userID, purpose string, session *webauthn.SessionData) (string, error) {
	if session == nil {
		return "", errors.New("missing ceremony session")
	}
	data, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	id := newID()
	if _, err := h.db.Exec(`DELETE FROM webauthn_ceremonies WHERE expires_at<=?`, timestamp()); err != nil {
		return "", err
	}
	_, err = h.db.Exec(`INSERT INTO webauthn_ceremonies(id,user_id,purpose,session_data,expires_at,created_at) VALUES(?,?,?,?,?,?)`, id, nullableID(userID), purpose, string(data), time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339Nano), timestamp())
	return id, err
}

func (h passkeyHandler) consumeCeremony(w http.ResponseWriter, r *http.Request, userID, purpose string) (webauthn.SessionData, bool) {
	id := r.Header.Get("X-Cookbook-Ceremony")
	if len(id) != 32 {
		writeError(w, http.StatusBadRequest, "invalid_or_expired_ceremony")
		return webauthn.SessionData{}, false
	}
	if _, err := hex.DecodeString(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_or_expired_ceremony")
		return webauthn.SessionData{}, false
	}
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return webauthn.SessionData{}, false
	}
	var owner sql.NullString
	var encoded string
	err = tx.QueryRow(`SELECT user_id,session_data FROM webauthn_ceremonies WHERE id=? AND purpose=? AND expires_at>?`, id, purpose, timestamp()).Scan(&owner, &encoded)
	if err != nil || owner.String != userID {
		_ = tx.Rollback()
		writeError(w, http.StatusBadRequest, "invalid_or_expired_ceremony")
		return webauthn.SessionData{}, false
	}
	if _, err := tx.Exec(`DELETE FROM webauthn_ceremonies WHERE id=?`, id); err != nil {
		_ = tx.Rollback()
		writeError(w, http.StatusInternalServerError, "storage_error")
		return webauthn.SessionData{}, false
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return webauthn.SessionData{}, false
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(encoded), &session); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return webauthn.SessionData{}, false
	}
	return session, true
}

func (h passkeyHandler) storeCredential(userID string, credential webauthn.Credential) error {
	stored := storedWebAuthnCredential{Credential: credential, Flags: credential.Flags.ProtocolValue()}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	result, err := h.db.Exec(`INSERT INTO webauthn_credentials(credential_id,user_id,credential_json,created_at) VALUES(?,?,?,?) ON CONFLICT(credential_id) DO NOTHING`, credential.ID, userID, string(encoded), timestamp())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errPasskeyAlreadyRegistered
	}
	return nil
}

var errPasskeyAlreadyRegistered = errors.New("passkey credential is already registered")

func (h passkeyHandler) updateCredential(userID []byte, credential webauthn.Credential) error {
	stored := storedWebAuthnCredential{Credential: credential, Flags: credential.Flags.ProtocolValue()}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	result, err := h.db.Exec(`UPDATE webauthn_credentials SET credential_json=?,last_used_at=? WHERE credential_id=? AND user_id=?`, string(encoded), timestamp(), credential.ID, string(userID))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("passkey credential is no longer registered")
	}
	return nil
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func boolPointer(value bool) *bool { return &value }
