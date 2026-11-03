package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const passkeyEnrollmentSchemaDDL = `
CREATE TABLE passkey_enrollment_grants (
  id TEXT PRIMARY KEY,
  token_hash BLOB NOT NULL UNIQUE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL CHECK (purpose='initial_passkey_registration'),
  issued_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  consumed_at TEXT,
  revoked_at TEXT,
  failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK (failed_attempts BETWEEN 0 AND 8)
);
ALTER TABLE webauthn_ceremonies RENAME TO webauthn_ceremonies_v3;
CREATE TABLE webauthn_ceremonies (
  id TEXT PRIMARY KEY,
  user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL CHECK (purpose IN ('registration','login','initial_registration')),
  session_data TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  enrollment_grant_id TEXT REFERENCES passkey_enrollment_grants(id) ON DELETE CASCADE
);
INSERT INTO webauthn_ceremonies(id,user_id,purpose,session_data,expires_at,created_at,enrollment_grant_id)
SELECT id,user_id,purpose,session_data,expires_at,created_at,NULL FROM webauthn_ceremonies_v3;
DROP TABLE webauthn_ceremonies_v3;
CREATE INDEX idx_webauthn_ceremonies_expiry ON webauthn_ceremonies(expires_at);
CREATE UNIQUE INDEX idx_passkey_enrollment_active_user ON passkey_enrollment_grants(user_id,purpose)
  WHERE consumed_at IS NULL AND revoked_at IS NULL;
CREATE UNIQUE INDEX idx_webauthn_ceremonies_enrollment_grant ON webauthn_ceremonies(enrollment_grant_id)
  WHERE enrollment_grant_id IS NOT NULL;
`

func ApplyPasskeyEnrollmentMigration(ctx context.Context, exec migrationExecutor) error {
	for _, statement := range strings.Split(passkeyEnrollmentSchemaDDL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := exec.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply passkey enrollment schema: %w", err)
		}
	}
	return nil
}

type enrollmentGrant struct {
	id, userID, purpose, expiresAt string
	tokenHash                      []byte
	failed                         int
}

func newEnrollmentToken() (id, token string, digest []byte, err error) {
	locator := make([]byte, 16)
	secret := make([]byte, 32)
	if _, err = rand.Read(locator); err != nil {
		return
	}
	if _, err = rand.Read(secret); err != nil {
		return
	}
	id = hex.EncodeToString(locator)
	secretText := hex.EncodeToString(secret)
	token = id + "." + secretText
	sum := sha256.Sum256([]byte(secretText))
	digest = sum[:]
	return
}

func parseEnrollmentToken(token string) (id string, digest []byte, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 64 || token != strings.ToLower(token) {
		return "", nil, false
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return "", nil, false
	}
	secret, err := hex.DecodeString(parts[1])
	if err != nil || len(secret) != 32 {
		return "", nil, false
	}
	sum := sha256.Sum256([]byte(parts[1]))
	return parts[0], sum[:], true
}

func validEnrollmentGrant(ctx context.Context, exec migrationExecutor, token string, now time.Time) (enrollmentGrant, bool, error) {
	id, digest, ok := parseEnrollmentToken(token)
	if !ok {
		return enrollmentGrant{}, false, nil
	}
	var grant enrollmentGrant
	err := exec.QueryRowContext(ctx, `SELECT id,user_id,purpose,expires_at,token_hash,failed_attempts FROM passkey_enrollment_grants WHERE id=? AND consumed_at IS NULL AND revoked_at IS NULL`, id).Scan(&grant.id, &grant.userID, &grant.purpose, &grant.expiresAt, &grant.tokenHash, &grant.failed)
	if errors.Is(err, sql.ErrNoRows) {
		return enrollmentGrant{}, false, nil
	}
	if err != nil {
		return enrollmentGrant{}, false, err
	}
	if subtle.ConstantTimeCompare(digest, grant.tokenHash) != 1 || grant.purpose != "initial_passkey_registration" || grant.failed >= 8 {
		return enrollmentGrant{}, false, nil
	}
	expires, err := time.Parse(time.RFC3339Nano, grant.expiresAt)
	if err != nil || !now.Before(expires) {
		return enrollmentGrant{}, false, nil
	}
	return grant, true, nil
}

func (h passkeyHandler) enrollmentBegin(w http.ResponseWriter, r *http.Request) {
	if h.enrollmentRateLimit(w, r, "passkey-enrollment-begin", 20) {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Token == "" {
		writeError(w, 400, "invalid_enrollment_grant")
		return
	}
	if h.enrollmentSubjectLimit(w, input.Token, "passkey-enrollment-begin") {
		return
	}
	var response any
	err := withInstanceOwnerWriteTx(r.Context(), h.db, func(exec migrationExecutor) error {
		grant, valid, err := validEnrollmentGrant(r.Context(), exec, input.Token, time.Now().UTC())
		if err != nil {
			return err
		}
		if !valid {
			return errInvalidEnrollmentGrant
		}
		user, ok, err := h.loadUserFrom(r.Context(), exec, grant.userID)
		if err != nil {
			return err
		}
		if !ok || len(user.credentials) != 0 {
			return errInvalidEnrollmentGrant
		}
		creation, session, err := h.webauthn.BeginRegistration(user,
			webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: boolPointer(true), UserVerification: protocol.VerificationRequired}),
		)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(session)
		if err != nil {
			return err
		}
		ceremony := newID()
		now := time.Now().UTC()
		if _, err := exec.ExecContext(r.Context(), `DELETE FROM webauthn_ceremonies WHERE enrollment_grant_id=? OR expires_at<=?`, grant.id, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := exec.ExecContext(r.Context(), `INSERT INTO webauthn_ceremonies(id,user_id,purpose,session_data,expires_at,created_at,enrollment_grant_id) VALUES(?,?,'initial_registration',?,?,?,?)`, ceremony, grant.userID, string(encoded), now.Add(5*time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), grant.id); err != nil {
			return err
		}
		response = map[string]any{"ceremony_id": ceremony, "options": creation}
		return nil
	})
	if errors.Is(err, errInvalidEnrollmentGrant) {
		writeError(w, http.StatusBadRequest, "invalid_or_expired_enrollment_grant")
		return
	}
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

var errInvalidEnrollmentGrant = errors.New("invalid enrollment grant")

func (h passkeyHandler) enrollmentFinish(w http.ResponseWriter, r *http.Request) {
	if h.enrollmentRateLimit(w, r, "passkey-enrollment-finish", 30) {
		return
	}
	var input struct {
		Token      string `json:"token"`
		CeremonyID string `json:"ceremony_id"`
	}
	// Read once into a bounded buffer because WebAuthn consumes the request body.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 400, "invalid_passkey_response")
		return
	}
	if err := json.Unmarshal(body, &input); err != nil || input.Token == "" || len(input.CeremonyID) != 32 {
		writeError(w, 400, "invalid_passkey_response")
		return
	}
	if h.enrollmentSubjectLimit(w, input.Token, "passkey-enrollment-finish") {
		return
	}
	name, err := normalizePasskeyName(r.Header.Get("X-Cookbook-Passkey-Name"))
	if err != nil {
		writeError(w, 400, "invalid_passkey_name")
		return
	}
	status := http.StatusBadRequest
	err = withInstanceOwnerWriteTx(r.Context(), h.db, func(exec migrationExecutor) error {
		grant, valid, err := validEnrollmentGrant(r.Context(), exec, input.Token, time.Now().UTC())
		if err != nil {
			return err
		}
		if !valid {
			return h.consumeBadEnrollmentAttempt(r.Context(), exec, input.Token, input.CeremonyID)
		}
		var encoded string
		var ceremonyGrant string
		if err := exec.QueryRowContext(r.Context(), `SELECT session_data,enrollment_grant_id FROM webauthn_ceremonies WHERE id=? AND user_id=? AND purpose='initial_registration' AND expires_at>?`, input.CeremonyID, grant.userID, timestamp()).Scan(&encoded, &ceremonyGrant); err != nil || ceremonyGrant != grant.id {
			return h.consumeBadEnrollmentAttempt(r.Context(), exec, input.Token, input.CeremonyID)
		}
		var session webauthn.SessionData
		if err := json.Unmarshal([]byte(encoded), &session); err != nil {
			return err
		}
		user, ok, err := h.loadUserFrom(r.Context(), exec, grant.userID)
		if err != nil {
			return err
		}
		if !ok || len(user.credentials) != 0 {
			return h.consumeBadEnrollmentAttempt(r.Context(), exec, input.Token, input.CeremonyID)
		}
		req := r.Clone(r.Context())
		req.Body = io.NopCloser(strings.NewReader(string(body)))
		credential, err := h.webauthn.FinishRegistration(user, session, req)
		if err != nil {
			status = http.StatusBadRequest
			return h.consumeBadEnrollmentAttempt(r.Context(), exec, input.Token, input.CeremonyID)
		}
		if err := storeCredentialWithExecutor(r.Context(), exec, grant.userID, *credential, name); err != nil {
			return err
		}
		now := timestamp()
		result, err := exec.ExecContext(r.Context(), `UPDATE passkey_enrollment_grants SET consumed_at=? WHERE id=? AND consumed_at IS NULL AND revoked_at IS NULL`, now, grant.id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errInvalidEnrollmentGrant
		}
		if _, err := exec.ExecContext(r.Context(), `DELETE FROM webauthn_ceremonies WHERE id=?`, input.CeremonyID); err != nil {
			return err
		}
		if _, err := exec.ExecContext(r.Context(), `INSERT INTO audit_events(id,actor_id,action,resource_type,resource_id,response_status,created_at) VALUES(?,NULL,'passkey_enrollment.complete','auth',?,201,?)`, newID(), grant.userID, now); err != nil {
			return err
		}
		status = http.StatusCreated
		return nil
	})
	if errors.Is(err, errInvalidEnrollmentGrant) {
		writeError(w, 400, "invalid_or_expired_enrollment_grant")
		return
	}
	if errors.Is(err, errPasskeyAlreadyRegistered) {
		writeError(w, http.StatusConflict, "passkey_already_registered")
		return
	}
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if status != http.StatusCreated {
		writeError(w, status, "invalid_passkey_response")
		return
	}
	writeJSON(w, status, map[string]string{"status": "passkey_registered"})
}

func (h passkeyHandler) consumeBadEnrollmentAttempt(ctx context.Context, exec migrationExecutor, token, ceremonyID string) error {
	id, _, ok := parseEnrollmentToken(token)
	if ok {
		if _, err := exec.ExecContext(ctx, `UPDATE passkey_enrollment_grants SET failed_attempts=MIN(failed_attempts+1,8) WHERE id=? AND consumed_at IS NULL AND revoked_at IS NULL`, id); err != nil {
			return err
		}
	}
	if len(ceremonyID) == 32 {
		if _, err := exec.ExecContext(ctx, `DELETE FROM webauthn_ceremonies WHERE id=? AND purpose='initial_registration'`, ceremonyID); err != nil {
			return err
		}
	}
	return nil
}

func (h passkeyHandler) enrollmentRateLimit(w http.ResponseWriter, r *http.Request, action string, limit int) bool {
	allowed, err := allowAuthRequest(h.db, r, action+"-ip", limit, time.Hour, h.trustedProxies)
	if err != nil {
		writeError(w, 500, "storage_error")
		return true
	}
	if !allowed {
		w.Header().Set("Retry-After", "3600")
		writeError(w, 429, "rate_limited")
		return true
	}
	return false
}

func (h passkeyHandler) enrollmentSubjectLimit(w http.ResponseWriter, token, action string) bool {
	id, _, ok := parseEnrollmentToken(token)
	if !ok {
		return false
	}
	allowed, err := allowAuthSubject(h.db, action+"-grant", id, 10, time.Hour)
	if err != nil {
		writeError(w, 500, "storage_error")
		return true
	}
	if !allowed {
		w.Header().Set("Retry-After", "3600")
		writeError(w, 429, "rate_limited")
		return true
	}
	return false
}
