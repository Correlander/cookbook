package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

const creatorProfilesSchemaDDL = `
CREATE TABLE creator_profiles (
  creator_id TEXT NOT NULL PRIMARY KEY
    CHECK (length(creator_id)=32 AND creator_id NOT GLOB '*[^0-9a-f]*'),
  user_id TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  public_name TEXT NOT NULL
    CHECK (length(CAST(public_name AS BLOB))<=320
      AND length(public_name)<=80
      AND instr(public_name,char(0))=0),
  bio TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(bio AS BLOB))<=1000 AND instr(bio,char(0))=0),
  visibility TEXT NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('private','instance')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (visibility='private' OR length(public_name) BETWEEN 1 AND 80)
);
CREATE INDEX creator_profiles_visibility_idx ON creator_profiles(visibility,creator_id);
`

type creatorProfile struct {
	ID         string `json:"id"`
	PublicName string `json:"public_name"`
	Bio        string `json:"bio"`
	Visibility string `json:"visibility"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type creatorProfileEnvelope struct {
	Profile *creatorProfile `json:"profile"`
}

func normalizeCreatorProfileInput(publicName, bio, visibility string) (string, string, string, error) {
	publicName = strings.TrimSpace(publicName)
	if (visibility != string(visibilityPrivate) && visibility != "instance") || !utf8.ValidString(publicName) || strings.ContainsRune(publicName, '\x00') || utf8.RuneCountInString(publicName) > 80 || len(publicName) > 320 {
		return "", "", "", errors.New("invalid public name")
	}
	if visibility == "instance" && utf8.RuneCountInString(publicName) < 1 {
		return "", "", "", errors.New("instance profiles require a public name")
	}
	if !utf8.ValidString(bio) || strings.ContainsRune(bio, '\x00') || len(bio) > 1000 {
		return "", "", "", errors.New("invalid bio")
	}
	return publicName, bio, visibility, nil
}

func readCreatorProfile(ctx context.Context, queryer contentQueryer, creatorID string) (creatorProfile, error) {
	var profile creatorProfile
	err := queryer.QueryRowContext(ctx, `SELECT creator_id,public_name,bio,visibility,created_at,updated_at FROM creator_profiles WHERE creator_id=?`, creatorID).
		Scan(&profile.ID, &profile.PublicName, &profile.Bio, &profile.Visibility, &profile.CreatedAt, &profile.UpdatedAt)
	return profile, err
}

func (h apiHandler) creatorProfileSelf(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		profile, err := readCreatorProfileForUser(r.Context(), h.db, uid)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, creatorProfileEnvelope{})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, creatorProfileEnvelope{Profile: &profile})
	case http.MethodPut:
		var input struct {
			PublicName string `json:"public_name"`
			Bio        string `json:"bio"`
			Visibility string `json:"visibility"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		name, bio, visibility, err := normalizeCreatorProfileInput(input.PublicName, input.Bio, input.Visibility)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_creator_profile")
			return
		}
		now := timestamp()
		_, err = h.db.ExecContext(r.Context(), `INSERT INTO creator_profiles(creator_id,user_id,public_name,bio,visibility,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET public_name=excluded.public_name,bio=excluded.bio,visibility=excluded.visibility,updated_at=excluded.updated_at`, newID(), uid, name, bio, visibility, now, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		profile, err := readCreatorProfileForUser(r.Context(), h.db, uid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, creatorProfileEnvelope{Profile: &profile})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) creatorProfileByID(w http.ResponseWriter, r *http.Request, creatorID string) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var profile creatorProfile
	err := h.db.QueryRowContext(r.Context(), `SELECT creator_id,public_name,bio,visibility,created_at,updated_at
		FROM creator_profiles WHERE creator_id=? AND (user_id=? OR visibility='instance')`, creatorID, uid).
		Scan(&profile.ID, &profile.PublicName, &profile.Bio, &profile.Visibility, &profile.CreatedAt, &profile.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, creatorProfileEnvelope{Profile: &profile})
}

func readCreatorProfileForUser(ctx context.Context, queryer contentQueryer, userID string) (creatorProfile, error) {
	var profile creatorProfile
	err := queryer.QueryRowContext(ctx, `SELECT creator_id,public_name,bio,visibility,created_at,updated_at FROM creator_profiles WHERE user_id=?`, userID).
		Scan(&profile.ID, &profile.PublicName, &profile.Bio, &profile.Visibility, &profile.CreatedAt, &profile.UpdatedAt)
	return profile, err
}

// ensureCreatorProfile creates an account's local identity lazily for a typed
// creation event. Existing profile identity and visibility remain unchanged.
func ensureCreatorProfile(ctx context.Context, tx *sql.Tx, userID string) (creatorProfile, error) {
	var existing creatorProfile
	err := tx.QueryRowContext(ctx, `SELECT creator_id,public_name,bio,visibility,created_at,updated_at FROM creator_profiles WHERE user_id=?`, userID).
		Scan(&existing.ID, &existing.PublicName, &existing.Bio, &existing.Visibility, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return creatorProfile{}, err
	}
	now := timestamp()
	profile := creatorProfile{ID: newID(), PublicName: "", Visibility: string(visibilityPrivate), CreatedAt: now, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO creator_profiles(creator_id,user_id,public_name,bio,visibility,created_at,updated_at) VALUES(?,?,?,'','private',?,?)`, profile.ID, userID, profile.PublicName, now, now)
	return profile, err
}
