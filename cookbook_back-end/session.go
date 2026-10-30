package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

const sessionCookie = "cookbook_session"

func issueSession(db *sql.DB, userID string) (string, time.Time, error) {
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, userID).Scan(&exists); err != nil || exists != 1 {
		return "", time.Time{}, errors.New("account not found")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(raw[:])
	expires := time.Now().UTC().Add(30 * 24 * time.Hour)
	sum := sha256.Sum256([]byte(token))
	if _, err := db.Exec(`INSERT INTO sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`, hex.EncodeToString(sum[:]), userID, expires.Format(time.RFC3339Nano), timestamp()); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func sessionUser(db *sql.DB, r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(cookie.Value))
	var userID, expires string
	err = db.QueryRow(`SELECT user_id,expires_at FROM sessions WHERE token_hash=?`, hex.EncodeToString(sum[:])).Scan(&userID, &expires)
	if err != nil {
		return "", false
	}
	timeValue, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !time.Now().Before(timeValue) {
		_, _ = db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hex.EncodeToString(sum[:]))
		return "", false
	}
	return userID, true
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expires, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}
