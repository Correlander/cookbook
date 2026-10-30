package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type emailSender interface{ SendLoginLink(to, link string) error }
type loginEmail struct {
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	SignInURL string `json:"sign_in_url"`
}
type authHandler struct {
	db            *sql.DB
	sender        emailSender
	baseURL       string
	secureCookies bool
}

func (h authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func (h authHandler) requestEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(body.Email))
	if err != nil || parsed.Address != strings.TrimSpace(body.Email) {
		writeError(w, 400, "invalid_email")
		return
	}
	email := strings.ToLower(parsed.Address)
	var uid, registrationPolicy string
	err = h.db.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		if err := h.db.QueryRow(`SELECT registration_policy FROM instance_settings WHERE id=1`).Scan(&registrationPolicy); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if registrationPolicy != "open" {
			writeJSON(w, 200, map[string]string{"status": "if_account_can_sign_in_link_sent"})
			return
		}
		name := strings.TrimSpace(body.DisplayName)
		if name == "" {
			name = strings.Split(email, "@")[0]
		}
		uid = newID()
		_, err = h.db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES(?,?,?,?)`, uid, email, name, timestamp())
		if err != nil {
			writeJSON(w, 200, map[string]string{"status": "if_account_can_sign_in_link_sent"})
			return
		}
	} else if err != nil {
		writeError(w, 500, "storage_error")
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
	_, err = h.db.Exec(`INSERT INTO email_challenges(id,email,token_hash,expires_at,created_at) VALUES(?,?,?,?,?)`, newID(), email, hex.EncodeToString(sum[:]), now.Add(15*time.Minute).Format(time.RFC3339Nano), timestamp())
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
	var email, expires string
	err = tx.QueryRow(`SELECT email,expires_at FROM email_challenges WHERE token_hash=? AND consumed_at IS NULL`, hash).Scan(&email, &expires)
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
	if err := tx.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&uid); err != nil {
		tx.Rollback()
		writeError(w, 400, "invalid_or_expired_token")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	token, expiresAt, err := issueSession(h.db, uid)
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
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		sum := sha256.Sum256([]byte(cookie.Value))
		_, _ = h.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hex.EncodeToString(sum[:]))
	}
	clearSessionCookie(w, h.secureCookies)
	writeJSON(w, 200, map[string]string{"status": "signed_out"})
}

func authConfig(db *sql.DB) (authHandler, error) {
	base := strings.TrimSpace(os.Getenv("COOKBOOK_PUBLIC_URL"))
	if base == "" {
		return authHandler{}, errors.New("COOKBOOK_PUBLIC_URL must be configured")
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://localhost") && !strings.HasPrefix(base, "http://127.0.0.1") {
		return authHandler{}, errors.New("COOKBOOK_PUBLIC_URL must use HTTPS outside local development")
	}
	driver := strings.TrimSpace(os.Getenv("COOKBOOK_EMAIL_COMMAND"))
	if driver == "" {
		return authHandler{}, errors.New("COOKBOOK_EMAIL_COMMAND is required for email login")
	}
	if _, err := exec.LookPath(driver); err != nil {
		return authHandler{}, errors.New("COOKBOOK_EMAIL_COMMAND executable was not found")
	}
	return authHandler{db: db, sender: commandEmailSender{command: driver}, baseURL: base, secureCookies: strings.HasPrefix(base, "https://")}, nil
}

type commandEmailSender struct{ command string }

func (s commandEmailSender) SendLoginLink(to, link string) error {
	payload, err := json.Marshal(loginEmail{To: to, Subject: "Your Cookbook sign-in link", Text: "Use this one-time link to sign in. It expires in 15 minutes.", SignInURL: link})
	if err != nil {
		return errors.New("email payload encoding failed")
	}
	cmd := exec.Command(s.command)
	cmd.Stdin = strings.NewReader(string(payload))
	if err := cmd.Run(); err != nil {
		return errors.New("email delivery failed")
	}
	return nil
}

type testEmailSender struct{ sent chan string }

func (s testEmailSender) SendLoginLink(to, link string) error { s.sent <- link; return nil }
func testAuth(db *sql.DB) authHandler {
	return authHandler{db: db, sender: testEmailSender{sent: make(chan string, 8)}, baseURL: "http://localhost:8000"}
}
