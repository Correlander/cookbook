package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const sessionCookie = "cookbook_session"
const recentAuthenticationWindow = 10 * time.Minute

func issueSession(db *sql.DB, userID string) (string, time.Time, error) {
	return issueSessionWithUserAgent(db, userID, "")
}

func issueSessionFromRequest(db *sql.DB, userID string, r *http.Request) (string, time.Time, error) {
	return issueSessionWithUserAgent(db, userID, normalizedUserAgent(r.UserAgent()))
}

func issueSessionWithUserAgent(db *sql.DB, userID, userAgent string) (string, time.Time, error) {
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
	if _, err := db.Exec(`INSERT INTO sessions(id,token_hash,user_id,expires_at,created_at,user_agent) VALUES(?,?,?,?,?,?)`, newID(), sessionTokenHash(token), userID, expires.Format(time.RFC3339Nano), timestamp(), userAgent); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func normalizedUserAgent(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 255 {
		return string(runes[:255])
	}
	return value
}

func sessionUser(db *sql.DB, r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	var userID, expires string
	err = db.QueryRow(`SELECT user_id,expires_at FROM sessions WHERE token_hash=?`, sessionTokenHash(cookie.Value)).Scan(&userID, &expires)
	if err != nil {
		return "", false
	}
	timeValue, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !time.Now().Before(timeValue) {
		_, _ = db.Exec(`DELETE FROM sessions WHERE token_hash=?`, sessionTokenHash(cookie.Value))
		return "", false
	}
	return userID, true
}

func requireRecentAuthentication(db *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := authenticatedUser(r)
		cookie, err := r.Cookie(sessionCookie)
		if uid == "" || err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var createdAt string
		err = db.QueryRow(`SELECT created_at FROM sessions WHERE token_hash=? AND user_id=?`, sessionTokenHash(cookie.Value), uid).Scan(&createdAt)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
			} else {
				writeError(w, http.StatusInternalServerError, "storage_error")
			}
			return
		}
		issuedAt, err := time.Parse(time.RFC3339Nano, createdAt)
		now := time.Now().UTC()
		if err != nil || issuedAt.After(now) || now.Sub(issuedAt) > recentAuthenticationWindow {
			writeError(w, http.StatusForbidden, "reauthentication_required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expires, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}
