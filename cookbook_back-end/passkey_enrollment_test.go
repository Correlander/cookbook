package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/webauthn"
)

func TestPasskeyEnrollmentRouteBeginsWithoutSessionAndConsumesInvalidCeremony(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('bootstrap','bootstrap@example.test','Bootstrap',0,?)`, timestamp()); err != nil {
		t.Fatal(err)
	}
	id, token, digest, err := newEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO passkey_enrollment_grants(id,token_hash,user_id,purpose,issued_at,expires_at) VALUES(?,?,?,'initial_passkey_registration',?,?)`, id, digest, "bootstrap", timestamp(), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("COOKBOOK_RP_ID", "localhost")
	t.Setenv("COOKBOOK_RP_ORIGIN", "http://localhost:5173")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "/bin/true")
	auth, err := authConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, auth, cfg)

	badOrigin := httptest.NewRequest(http.MethodPost, "/api/auth/passkey/enrollment/begin", strings.NewReader(`{"token":"`+token+`"}`))
	badOrigin.Host = "localhost:5173"
	badOrigin.Header.Set("Origin", "http://evil.example")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, badOrigin)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin status=%d body=%s", denied.Code, denied.Body.String())
	}

	begin := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/begin", map[string]string{"token": token}, nil)
	if begin.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	var beginBody struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(begin.Body.Bytes(), &beginBody); err != nil || len(beginBody.CeremonyID) != 32 || len(beginBody.Options) == 0 {
		t.Fatalf("begin body=%s err=%v", begin.Body.String(), err)
	}
	var purpose, grant string
	if err := db.QueryRow(`SELECT purpose,enrollment_grant_id FROM webauthn_ceremonies WHERE id=?`, beginBody.CeremonyID).Scan(&purpose, &grant); err != nil || purpose != "initial_registration" || grant != id {
		t.Fatalf("ceremony purpose=%q grant=%q err=%v", purpose, grant, err)
	}
	var sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("enrollment issued a session: count=%d err=%v", sessions, err)
	}

	body := map[string]string{"token": token, "ceremony_id": beginBody.CeremonyID, "id": "invalid"}
	failed := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/finish", body, nil)
	if failed.Code != http.StatusBadRequest {
		t.Fatalf("invalid finish status=%d body=%s", failed.Code, failed.Body.String())
	}
	var ceremonies, attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_ceremonies WHERE id=?`, beginBody.CeremonyID).Scan(&ceremonies); err != nil || ceremonies != 0 {
		t.Fatalf("failed ceremony remains: count=%d err=%v", ceremonies, err)
	}
	if err := db.QueryRow(`SELECT failed_attempts FROM passkey_enrollment_grants WHERE id=?`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("failed attempts=%d err=%v", attempts, err)
	}
	replay := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/finish", body, nil)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestEnrollmentTokenFormatAndDigestAreOpaque(t *testing.T) {
	id, token, digest, err := newEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	gotID, gotDigest, ok := parseEnrollmentToken(token)
	if !ok || gotID != id || len(digest) != sha256.Size || !bytes.Equal(digest, gotDigest) {
		t.Fatalf("token parse ok=%v id=%q want=%q", ok, gotID, id)
	}
	for _, malformed := range []string{"", id + ".short", strings.ToUpper(token), id + "." + strings.Repeat("z", 64), id + "." + strings.Repeat("a", 64) + ".extra"} {
		if _, _, ok := parseEnrollmentToken(malformed); ok {
			t.Errorf("accepted malformed token %q", malformed)
		}
	}
}

func TestExpiredEnrollmentGrantIsDenied(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('expired','expired@example.test','Expired',0,?)`, timestamp()); err != nil {
		t.Fatal(err)
	}
	id, token, digest, err := newEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO passkey_enrollment_grants(id,token_hash,user_id,purpose,issued_at,expires_at) VALUES(?,?,?,'initial_passkey_registration',?,?)`, id, digest, "expired", timestamp(), time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	_, valid, err := validEnrollmentGrant(context.Background(), db, token, time.Now().UTC())
	if err != nil || valid {
		t.Fatalf("expired grant valid=%v err=%v", valid, err)
	}
}

func TestPasskeyEnrollmentFinishCommitsCredentialGrantCeremonyAndAuditTogether(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('bootstrap','bootstrap@example.test','Bootstrap',0,?)`, timestamp()); err != nil {
		t.Fatal(err)
	}
	id, token, digest, err := newEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO passkey_enrollment_grants(id,token_hash,user_id,purpose,issued_at,expires_at) VALUES(?,?,?,'initial_passkey_registration',?,?)`, id, digest, "bootstrap", timestamp(), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("COOKBOOK_RP_ID", "localhost")
	t.Setenv("COOKBOOK_RP_ORIGIN", "http://localhost:5173")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "/bin/true")
	auth, err := authConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, auth, cfg)
	begin := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/begin", map[string]string{"token": token}, nil)
	if begin.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	var beginBody struct {
		CeremonyID string `json:"ceremony_id"`
	}
	if err := json.Unmarshal(begin.Body.Bytes(), &beginBody); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err := db.QueryRow(`SELECT session_data FROM webauthn_ceremonies WHERE id=?`, beginBody.CeremonyID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(encoded), &session); err != nil {
		t.Fatal(err)
	}
	credential := validEnrollmentCredential(t, session.Challenge, "http://localhost:5173", "localhost", true)
	credential["token"] = token
	credential["ceremony_id"] = beginBody.CeremonyID
	_, _ = db.Exec(`CREATE TRIGGER reject_enrollment_audit BEFORE INSERT ON audit_events WHEN NEW.action='passkey_enrollment.complete' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`)
	failed := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/finish", credential, nil)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected audit failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	var credentials, ceremonies int
	var consumed sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_credentials`).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_ceremonies WHERE id=?`, beginBody.CeremonyID).Scan(&ceremonies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT consumed_at FROM passkey_enrollment_grants WHERE id=?`, id).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if credentials != 0 || ceremonies != 1 || consumed.Valid {
		t.Fatalf("audit failure left credentials=%d ceremonies=%d consumed=%v", credentials, ceremonies, consumed)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_enrollment_audit`); err != nil {
		t.Fatal(err)
	}
	success := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/finish", credential, nil)
	if success.Code != http.StatusCreated {
		t.Fatalf("finish status=%d body=%s", success.Code, success.Body.String())
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id='bootstrap'`).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_ceremonies WHERE id=?`, beginBody.CeremonyID).Scan(&ceremonies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT consumed_at FROM passkey_enrollment_grants WHERE id=?`, id).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	var audits, sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='passkey_enrollment.complete' AND resource_id='bootstrap'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if credentials != 1 || ceremonies != 0 || !consumed.Valid || audits != 1 || sessions != 0 {
		t.Fatalf("finish state credentials=%d ceremonies=%d consumed=%v audits=%d sessions=%d", credentials, ceremonies, consumed, audits, sessions)
	}
}

func validEnrollmentCredential(t *testing.T, challenge, origin, rpID string, userVerified bool) map[string]any {
	t.Helper()
	const credentialID = "6xrtBhJQW6QU4tOaB4rrHaS2Ks0yDDL_q8jDC16DEjZ-VLVf4kCRkvl2xp2D71sTPYns-exsHQHTy3G-zJRK8g"
	const attestation = "o2NmbXRkbm9uZWdhdHRTdG10oGhhdXRoRGF0YVjEdKbqkhPJnC90siSSsyDPQCYqlMGpUKA5fyklC2CEHvBBAAAAAAAAAAAAAAAAAAAAAAAAAAAAQOsa7QYSUFukFOLTmgeK6x2ktirNMgwy_6vIwwtegxI2flS1X-JAkZL5dsadg-9bEz2J7PnsbB0B08txvsyUSvKlAQIDJiABIVggLKF5xS0_BntttUIrm2Z2tgZ4uQDwllbdIfrrBMABCNciWCDHwin8Zdkr56iSIh0MrB5qZiEzYLQpEOREhMUkY6q4Vw"
	raw, err := base64.RawURLEncoding.DecodeString(attestation)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := webauthncbor.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	authData, ok := object["authData"].([]byte)
	if !ok || len(authData) < 37 {
		t.Fatalf("fixture authData type=%T length=%d", object["authData"], len(authData))
	}
	rpHash := sha256.Sum256([]byte(rpID))
	copy(authData[:32], rpHash[:])
	if userVerified {
		authData[32] |= 0x04
	} else {
		authData[32] &^= 0x04
	}
	object["authData"] = authData
	attestationBytes, err := webauthncbor.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	clientData, err := json.Marshal(map[string]string{"type": "webauthn.create", "challenge": challenge, "origin": origin})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": credentialID, "rawId": credentialID, "type": "public-key", "authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{}, "response": map[string]any{"attestationObject": base64.RawURLEncoding.EncodeToString(attestationBytes), "clientDataJSON": base64.RawURLEncoding.EncodeToString(clientData), "transports": []string{"internal"}}}
}

func TestConcurrentPasskeyEnrollmentFinishHasSingleWinner(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('bootstrap','bootstrap@example.test','Bootstrap',0,?)`, timestamp()); err != nil {
		t.Fatal(err)
	}
	id, token, digest, err := newEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO passkey_enrollment_grants(id,token_hash,user_id,purpose,issued_at,expires_at) VALUES(?,?,?,'initial_passkey_registration',?,?)`, id, digest, "bootstrap", timestamp(), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("COOKBOOK_RP_ID", "localhost")
	t.Setenv("COOKBOOK_RP_ORIGIN", "http://localhost:5173")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "/bin/true")
	auth, err := authConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, auth, cfg)
	begin := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/begin", map[string]string{"token": token}, nil)
	if begin.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	var beginBody struct {
		CeremonyID string `json:"ceremony_id"`
	}
	if err := json.Unmarshal(begin.Body.Bytes(), &beginBody); err != nil {
		t.Fatal(err)
	}
	var sessionJSON string
	if err := db.QueryRow(`SELECT session_data FROM webauthn_ceremonies WHERE id=?`, beginBody.CeremonyID).Scan(&sessionJSON); err != nil {
		t.Fatal(err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(sessionJSON), &session); err != nil {
		t.Fatal(err)
	}
	body := validEnrollmentCredential(t, session.Challenge, "http://localhost:5173", "localhost", true)
	body["token"] = token
	body["ceremony_id"] = beginBody.CeremonyID
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			recorder := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/finish", body, nil)
			codes <- recorder.Code
		}()
	}
	close(start)
	wait.Wait()
	close(codes)
	successes, denials := 0, 0
	for code := range codes {
		if code == http.StatusCreated {
			successes++
		} else if code == http.StatusBadRequest {
			denials++
		} else {
			t.Errorf("unexpected concurrent finish status=%d", code)
		}
	}
	if successes != 1 || denials != 1 {
		t.Fatalf("concurrent finish successes=%d denials=%d", successes, denials)
	}
	var credentials, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id='bootstrap'`).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='passkey_enrollment.complete' AND resource_id='bootstrap'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if credentials != 1 || audits != 1 {
		t.Fatalf("concurrent final state credentials=%d audits=%d", credentials, audits)
	}
}

func TestPasskeyEnrollmentFinishRejectsWrongChallengeRPAndMissingUV(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('bootstrap','bootstrap@example.test','Bootstrap',0,?)`, timestamp()); err != nil {
		t.Fatal(err)
	}
	id, token, digest, err := newEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO passkey_enrollment_grants(id,token_hash,user_id,purpose,issued_at,expires_at) VALUES(?,?,?,'initial_passkey_registration',?,?)`, id, digest, "bootstrap", timestamp(), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("COOKBOOK_RP_ID", "localhost")
	t.Setenv("COOKBOOK_RP_ORIGIN", "http://localhost:5173")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "/bin/true")
	auth, err := authConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuthConfig(db, auth, cfg)
	for _, tc := range []struct {
		name            string
		challenge, rpID string
		uv              bool
	}{{"challenge", "wrong-challenge", "localhost", true}, {"rp", "", "wrong.example", true}, {"user-verification", "", "localhost", false}} {
		t.Run(tc.name, func(t *testing.T) {
			begin := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/begin", map[string]string{"token": token}, nil)
			if begin.Code != http.StatusOK {
				t.Fatalf("begin status=%d body=%s", begin.Code, begin.Body.String())
			}
			var envelope struct {
				CeremonyID string `json:"ceremony_id"`
			}
			if err := json.Unmarshal(begin.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			var sessionJSON string
			if err := db.QueryRow(`SELECT session_data FROM webauthn_ceremonies WHERE id=?`, envelope.CeremonyID).Scan(&sessionJSON); err != nil {
				t.Fatal(err)
			}
			var session webauthn.SessionData
			if err := json.Unmarshal([]byte(sessionJSON), &session); err != nil {
				t.Fatal(err)
			}
			challenge := session.Challenge
			if tc.challenge != "" {
				challenge = tc.challenge
			}
			body := validEnrollmentCredential(t, challenge, "http://localhost:5173", tc.rpID, tc.uv)
			body["token"] = token
			body["ceremony_id"] = envelope.CeremonyID
			finish := callAPI(handler, http.MethodPost, "/api/auth/passkey/enrollment/finish", body, nil)
			if finish.Code != http.StatusBadRequest {
				t.Fatalf("invalid %s finish status=%d body=%s", tc.name, finish.Code, finish.Body.String())
			}
		})
	}
	var attempts int
	if err := db.QueryRow(`SELECT failed_attempts FROM passkey_enrollment_grants WHERE id=?`, id).Scan(&attempts); err != nil || attempts != 3 {
		t.Fatalf("denied ceremony attempts=%d err=%v", attempts, err)
	}
}
