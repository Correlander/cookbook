package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type emailSender interface{ SendLoginLink(to, link string) error }
type loginEmail struct {
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	SignInURL string `json:"sign_in_url"`
}
type authHandler struct {
	db             *sql.DB
	sender         emailSender
	baseURL        string
	origin         string
	secureCookies  bool
	webauthn       *webauthn.WebAuthn
	trustedProxies []*net.IPNet
}

func (h authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && !validRequestOrigin(r, h.origin) {
		writeError(w, http.StatusForbidden, "csrf_origin_mismatch")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/email":
		h.requestEmail(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/consume":
		h.consumeEmail(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/auth/me":
		h.me(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/auth/logout":
		h.logout(w, r)
	default:
		writeError(w, 404, "not_found")
	}
}

func validRequestOrigin(r *http.Request, expectedOrigin string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || expectedOrigin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") && origin == expectedOrigin && u.Host == r.Host
}

func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("invalid origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return strings.ToLower(u.Scheme) + "://" + host, nil
}

func (h authHandler) requestEmail(w http.ResponseWriter, r *http.Request) {
	allowed, err := allowAuthRequest(h.db, r, "email-link", 30, time.Hour, h.trustedProxies)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", "3600")
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var body struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		Invitation  string `json:"invitation"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(body.Email))
	if err != nil || parsed.Address != strings.TrimSpace(body.Email) || len(parsed.Address) > 254 {
		writeError(w, 400, "invalid_email")
		return
	}
	email := strings.ToLower(parsed.Address)
	var uid, registrationPolicy string
	var registrationName, registrationInvitation string
	var invitationValid bool
	invitationID := ""
	if invitation := strings.TrimSpace(body.Invitation); invitation != "" {
		sum := sha256.Sum256([]byte(invitation))
		var invitationExpiry string
		err := h.db.QueryRow(`SELECT id,expires_at FROM invitations WHERE token_hash=? AND lower(email)=? AND accepted_at IS NULL AND revoked_at IS NULL`, hex.EncodeToString(sum[:]), email).Scan(&invitationID, &invitationExpiry)
		if err == nil {
			if parsedExpiry, parseErr := time.Parse(time.RFC3339Nano, invitationExpiry); parseErr == nil && time.Now().Before(parsedExpiry) {
				invitationValid = true
			}
		}
		if !invitationValid {
			writeJSON(w, 200, map[string]string{"status": "if_account_can_sign_in_link_sent"})
			return
		}
	}
	err = h.db.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		if err := h.db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&registrationPolicy); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if registrationPolicy == "disabled" || (registrationPolicy == "invite_only" && !invitationValid) || (registrationPolicy != "open" && registrationPolicy != "invite_only" && registrationPolicy != "disabled") {
			writeJSON(w, 200, map[string]string{"status": "if_account_can_sign_in_link_sent"})
			return
		}
		registrationName = strings.TrimSpace(body.DisplayName)
		if len(registrationName) > 100 {
			writeError(w, http.StatusBadRequest, "invalid_display_name")
			return
		}
		if registrationName == "" {
			registrationName = strings.Split(email, "@")[0]
		}
	} else if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if invitationValid {
		registrationInvitation = invitationID
	}
	allowed, err = allowAuthSubject(h.db, "email-link-address", email, 5, time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if !allowed {
		writeJSON(w, http.StatusOK, map[string]string{"status": "if_account_can_sign_in_link_sent"})
		return
	}
	var count int
	err = h.db.QueryRow(`SELECT COUNT(*) FROM email_challenges WHERE email=? AND created_at>?`, email, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)).Scan(&count)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if count >= 5 {
		writeJSON(w, 200, map[string]string{"status": "if_account_can_sign_in_link_sent"})
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	token := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	_, err = h.db.Exec(`INSERT INTO email_challenges(id,email,display_name,invitation_id,token_hash,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`, newID(), email, registrationName, nullableID(registrationInvitation), hex.EncodeToString(sum[:]), now.Add(15*time.Minute).Format(time.RFC3339Nano), timestamp())
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	link := strings.TrimRight(h.baseURL, "/") + "/auth/confirm?token=" + url.QueryEscape(token)
	if err := h.sender.SendLoginLink(email, link); err != nil {
		writeError(w, 503, "email_unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "if_account_can_sign_in_link_sent"})
}

func (h authHandler) consumeEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Token) != 64 {
		writeError(w, 400, "invalid_or_expired_token")
		return
	}
	sum := sha256.Sum256([]byte(body.Token))
	hash := hex.EncodeToString(sum[:])
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	var email, expires, displayName string
	var invitationID sql.NullString
	err = tx.QueryRow(`SELECT email,expires_at,display_name,invitation_id FROM email_challenges WHERE token_hash=? AND consumed_at IS NULL`, hash).Scan(&email, &expires, &displayName, &invitationID)
	if err != nil {
		tx.Rollback()
		writeError(w, 400, "invalid_or_expired_token")
		return
	}
	expiry, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !time.Now().Before(expiry) {
		tx.Rollback()
		writeError(w, 400, "invalid_or_expired_token")
		return
	}
	res, err := tx.Exec(`UPDATE email_challenges SET consumed_at=? WHERE token_hash=? AND consumed_at IS NULL`, timestamp(), hash)
	if err != nil {
		tx.Rollback()
		writeError(w, 500, "storage_error")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		tx.Rollback()
		writeError(w, 400, "invalid_or_expired_token")
		return
	}
	var uid string
	if err := tx.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&uid); errors.Is(err, sql.ErrNoRows) {
		if displayName == "" {
			displayName = strings.Split(email, "@")[0]
		}
		uid = newID()
		if _, err := tx.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES(?,?,?,?)`, uid, email, displayName, timestamp()); err != nil {
			tx.Rollback()
			writeError(w, 400, "invalid_or_expired_token")
			return
		}
	} else if err != nil {
		tx.Rollback()
		writeError(w, 500, "storage_error")
		return
	}
	if invitationID.Valid {
		var cookbookID sql.NullString
		var invitedRole role
		if err := tx.QueryRow(`SELECT cookbook_id,role FROM invitations WHERE id=? AND lower(email)=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>?`, invitationID.String, email, timestamp()).Scan(&cookbookID, &invitedRole); err != nil {
			tx.Rollback()
			writeError(w, 400, "invalid_or_expired_token")
			return
		}
		if cookbookID.Valid {
			if _, err := tx.Exec(`INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES(?,?,?,?) ON CONFLICT(cookbook_id,user_id) DO UPDATE SET role=excluded.role`, cookbookID.String, uid, invitedRole, timestamp()); err != nil {
				tx.Rollback()
				writeError(w, 500, "storage_error")
				return
			}
		}
		result, err := tx.Exec(`UPDATE invitations SET accepted_at=? WHERE id=? AND accepted_at IS NULL AND revoked_at IS NULL`, timestamp(), invitationID.String)
		if err != nil {
			tx.Rollback()
			writeError(w, 500, "storage_error")
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			tx.Rollback()
			writeError(w, 400, "invalid_or_expired_token")
			return
		}
	}
	if err := insertAuditEvent(tx, uid, "POST /auth/consume", "auth", "", http.StatusOK); err != nil {
		tx.Rollback()
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	token, expiresAt, err := issueSessionFromRequest(h.db, uid, r)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	setSessionCookie(w, token, expiresAt, h.secureCookies)
	writeJSON(w, 200, map[string]string{"status": "signed_in"})
}

func (h authHandler) me(w http.ResponseWriter, r *http.Request) {
	uid, ok := sessionUser(h.db, r)
	if !ok {
		writeError(w, 401, "unauthorized")
		return
	}
	var u user
	if err := h.db.QueryRow(`SELECT id,email,display_name FROM users WHERE id=?`, uid).Scan(&u.ID, &u.Email, &u.DisplayName); err != nil {
		writeError(w, 401, "unauthorized")
		return
	}
	writeJSON(w, 200, u)
}
func (h authHandler) logout(w http.ResponseWriter, r *http.Request) {
	uid, ok := sessionUser(h.db, r)
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		sum := sha256.Sum256([]byte(cookie.Value))
		_, _ = h.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hex.EncodeToString(sum[:]))
	}
	if ok {
		recordAuditBestEffort(h.db, uid, "POST /auth/logout", "auth", "", http.StatusOK)
	}
	clearSessionCookie(w, h.secureCookies)
	writeJSON(w, 200, map[string]string{"status": "signed_out"})
}

func authConfig(db *sql.DB) (authHandler, error) {
	base := strings.TrimSpace(os.Getenv("COOKBOOK_PUBLIC_URL"))
	if base == "" {
		return authHandler{}, errors.New("COOKBOOK_PUBLIC_URL must be configured")
	}
	parsedBase, err := url.Parse(base)
	if err != nil || parsedBase.Host == "" || (parsedBase.Scheme != "https" && !(parsedBase.Scheme == "http" && (parsedBase.Hostname() == "localhost" || parsedBase.Hostname() == "127.0.0.1"))) {
		return authHandler{}, errors.New("COOKBOOK_PUBLIC_URL must use HTTPS outside local development")
	}
	driver := strings.TrimSpace(os.Getenv("COOKBOOK_EMAIL_COMMAND"))
	var sender emailSender
	if driver != "" {
		if _, err := exec.LookPath(driver); err != nil {
			return authHandler{}, errors.New("COOKBOOK_EMAIL_COMMAND executable was not found")
		}
		sender = commandEmailSender{command: driver}
	} else {
		host := strings.TrimSpace(os.Getenv("COOKBOOK_SMTP_HOST"))
		from := strings.TrimSpace(os.Getenv("COOKBOOK_SMTP_FROM"))
		username := os.Getenv("COOKBOOK_SMTP_USERNAME")
		password := os.Getenv("COOKBOOK_SMTP_PASSWORD")
		if host == "" || from == "" {
			return authHandler{}, errors.New("configure COOKBOOK_EMAIL_COMMAND or COOKBOOK_SMTP_HOST and COOKBOOK_SMTP_FROM")
		}
		parsedFrom, parseErr := mail.ParseAddress(from)
		if parseErr != nil || parsedFrom.Address != from {
			return authHandler{}, errors.New("COOKBOOK_SMTP_FROM must be a valid email address")
		}
		if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
			return authHandler{}, errors.New("COOKBOOK_SMTP_HOST must include a port")
		}
		if (username == "") != (password == "") {
			return authHandler{}, errors.New("COOKBOOK_SMTP_USERNAME and COOKBOOK_SMTP_PASSWORD must be set together")
		}
		sender = smtpEmailSender{address: host, from: from, username: username, password: password}
	}
	rpID := strings.TrimSpace(os.Getenv("COOKBOOK_RP_ID"))
	if rpID == "" {
		rpID = parsedBase.Hostname()
	}
	baseOrigin, err := normalizeOrigin(base)
	if err != nil {
		return authHandler{}, errors.New("COOKBOOK_PUBLIC_URL must contain a valid origin")
	}
	rpOrigin := strings.TrimSpace(os.Getenv("COOKBOOK_RP_ORIGIN"))
	if rpOrigin == "" {
		rpOrigin = baseOrigin
	} else if rpOrigin, err = normalizeOrigin(rpOrigin); err != nil {
		return authHandler{}, errors.New("COOKBOOK_RP_ORIGIN must be a valid origin")
	}
	if rpOrigin != baseOrigin {
		return authHandler{}, errors.New("COOKBOOK_RP_ORIGIN must match COOKBOOK_PUBLIC_URL origin")
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName:        "Cookbook",
		RPID:                 rpID,
		RPOrigins:            []string{rpOrigin},
		EncodeUserIDAsString: true,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: boolPointer(true),
			UserVerification:   protocol.VerificationRequired,
		},
	})
	if err != nil {
		return authHandler{}, errors.New("WebAuthn relying party configuration is invalid")
	}
	return authHandler{db: db, sender: sender, baseURL: base, origin: baseOrigin, secureCookies: parsedBase.Scheme == "https", webauthn: wa}, nil
}

type commandEmailSender struct {
	command string
	timeout time.Duration
}

type smtpEmailSender struct {
	address, from, username, password string
	timeout                           time.Duration
}

const emailDeliveryTimeout = 30 * time.Second

func configuredEmailTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return emailDeliveryTimeout
}

func (s smtpEmailSender) SendLoginLink(to, link string) error {
	toAddress, err := mail.ParseAddress(to)
	if err != nil || toAddress.Address != to {
		return errors.New("invalid recipient")
	}
	fromAddress, err := mail.ParseAddress(s.from)
	if err != nil {
		return errors.New("invalid sender")
	}
	host, _, err := net.SplitHostPort(s.address)
	if err != nil {
		return errors.New("invalid SMTP address")
	}
	timeout := configuredEmailTimeout(s.timeout)
	conn, err := net.DialTimeout("tcp", s.address, timeout)
	if err != nil {
		return errors.New("email delivery failed")
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return errors.New("email delivery failed")
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return errors.New("email delivery failed")
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP server does not support required STARTTLS")
	}
	if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		return errors.New("SMTP TLS negotiation failed")
	}
	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, host)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if err := client.Mail(fromAddress.Address); err != nil {
		return errors.New("SMTP sender rejected")
	}
	if err := client.Rcpt(toAddress.Address); err != nil {
		return errors.New("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message rejected")
	}
	message := fmt.Sprintf("To: %s\r\nFrom: %s\r\nSubject: Your Cookbook sign-in link\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nUse this one-time link to sign in. It expires in 15 minutes.\r\n\r\n%s\r\n", toAddress.String(), fromAddress.String(), link)
	if _, err = writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return errors.New("SMTP message write failed")
	}
	if err = writer.Close(); err != nil {
		return errors.New("SMTP message delivery failed")
	}
	if err = client.Quit(); err != nil {
		return errors.New("SMTP connection close failed")
	}
	return nil
}

func (s commandEmailSender) SendLoginLink(to, link string) error {
	payload, err := json.Marshal(loginEmail{To: to, Subject: "Your Cookbook sign-in link", Text: "Use this one-time link to sign in. It expires in 15 minutes.", SignInURL: link})
	if err != nil {
		return errors.New("email payload encoding failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), configuredEmailTimeout(s.timeout))
	defer cancel()
	cmd := exec.CommandContext(ctx, s.command)
	cmd.Stdin = strings.NewReader(string(payload))
	if err := cmd.Run(); err != nil {
		return errors.New("email delivery failed")
	}
	return nil
}

type testEmailSender struct{ sent chan string }

func (s testEmailSender) SendLoginLink(to, link string) error { s.sent <- link; return nil }
func testAuth(db *sql.DB) authHandler {
	wa, err := webauthn.New(&webauthn.Config{RPDisplayName: "Cookbook", RPID: "localhost", RPOrigins: []string{"http://localhost:5173"}, EncodeUserIDAsString: true, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: boolPointer(true), UserVerification: protocol.VerificationRequired}})
	if err != nil {
		panic(err)
	}
	return authHandler{db: db, sender: testEmailSender{sent: make(chan string, 8)}, baseURL: "http://localhost:5173", origin: "http://localhost:5173", webauthn: wa}
}
