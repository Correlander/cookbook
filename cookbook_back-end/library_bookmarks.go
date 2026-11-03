package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	maxRecipeBookmarksPerAccount = 10000
	maxRecipeBookmarkURIBytes    = 2048
)

const recipeBookmarkSchemaDDL = `
CREATE TABLE library_instance_identity (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id=1),
  instance_id TEXT NOT NULL UNIQUE
    CHECK (length(instance_id)=32 AND instance_id NOT GLOB '*[^0-9a-f]*')
);
INSERT INTO library_instance_identity(singleton_id,instance_id)
VALUES (1, lower(hex(randomblob(16))));
CREATE TABLE recipe_bookmarks (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('local','remote')),
  local_instance_id TEXT,
  local_recipe_id TEXT,
  remote_object_uri TEXT,
  created_at TEXT NOT NULL,
  CHECK (
    (target_kind='local'
      AND local_instance_id IS NOT NULL
      AND length(local_instance_id)=32
      AND local_instance_id NOT GLOB '*[^0-9a-f]*'
      AND local_recipe_id IS NOT NULL
      AND length(trim(local_recipe_id))>0
      AND remote_object_uri IS NULL)
    OR
    (target_kind='remote'
      AND local_instance_id IS NULL
      AND local_recipe_id IS NULL
      AND remote_object_uri IS NOT NULL
      AND length(trim(remote_object_uri))>0)
  )
);
CREATE UNIQUE INDEX recipe_bookmarks_local_unique
  ON recipe_bookmarks(user_id,local_instance_id,local_recipe_id)
  WHERE target_kind='local';
CREATE UNIQUE INDEX recipe_bookmarks_remote_unique
  ON recipe_bookmarks(user_id,remote_object_uri)
  WHERE target_kind='remote';
CREATE INDEX idx_recipe_bookmarks_user_created
  ON recipe_bookmarks(user_id,created_at DESC,id);
`

const recipeBookmarkLimitTriggerDDL = `CREATE TRIGGER recipe_bookmarks_account_limit
BEFORE INSERT ON recipe_bookmarks
WHEN (SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id=NEW.user_id) >= 10000
BEGIN SELECT RAISE(ABORT, 'recipe bookmark limit reached'); END`

// ApplyRecipeBookmarkMigration is migration 6's additive, local-only schema.
// Its caller applies the DDL and ledger row in the migration transaction.
func ApplyRecipeBookmarkMigration(ctx context.Context, exec migrationExecutor) error {
	for _, statement := range strings.Split(recipeBookmarkSchemaDDL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := exec.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply personal library bookmark schema: %w", err)
		}
	}
	if _, err := exec.ExecContext(ctx, recipeBookmarkLimitTriggerDDL); err != nil {
		return fmt.Errorf("apply personal library bookmark limit: %w", err)
	}
	return nil
}

type RecipeBookmarkTarget struct {
	LocalRecipeID   string `json:"local_recipe_id,omitempty"`
	RemoteObjectURI string `json:"remote_object_uri,omitempty"`
}

type SaveRecipeBookmarkInput struct {
	Target RecipeBookmarkTarget `json:"target"`
}

type RecipeBookmarkView struct {
	ID           string                `json:"id"`
	TargetKind   string                `json:"target_kind"`
	Target       *RecipeBookmarkTarget `json:"target,omitempty"`
	Availability string                `json:"availability"`
	LocalRecipe  *recipe               `json:"local_recipe,omitempty"`
	RemoteRecipe *RemoteRecipeReadView `json:"remote_recipe,omitempty"`
	CreatedAt    string                `json:"created_at"`
}

type RecipeBookmarkPage struct {
	Items  []RecipeBookmarkView `json:"items"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
}

type recipeBookmarkRecord struct {
	ID              string
	TargetKind      string
	LocalInstanceID sql.NullString
	LocalRecipeID   sql.NullString
	RemoteObjectURI sql.NullString
	CreatedAt       string
}

func currentLibraryInstanceID(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (string, error) {
	var instanceID string
	if err := queryer.QueryRowContext(ctx, `SELECT instance_id FROM library_instance_identity WHERE singleton_id=1`).Scan(&instanceID); err != nil {
		return "", err
	}
	if !validLibraryInstanceID(instanceID) {
		return "", errors.New("invalid personal library instance identity")
	}
	return instanceID, nil
}

func validLibraryInstanceID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func (h apiHandler) recipeBookmarks(w http.ResponseWriter, r *http.Request, bookmarkID string) {
	userID := authenticatedUser(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if bookmarkID != "" {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		h.deleteRecipeBookmark(w, r, userID, bookmarkID)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listRecipeBookmarks(w, r, ContentActor{AccountID: userID})
	case http.MethodPost:
		h.saveRecipeBookmark(w, r, ContentActor{AccountID: userID})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) listRecipeBookmarks(w http.ResponseWriter, r *http.Request, actor ContentActor) {
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	instanceID, err := currentLibraryInstanceID(r.Context(), tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	var total int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id=?`, actor.AccountID).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,target_kind,local_instance_id,local_recipe_id,remote_object_uri,created_at
		FROM recipe_bookmarks WHERE user_id=? ORDER BY created_at DESC,id LIMIT ? OFFSET ?`, actor.AccountID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	bookmarks := make([]recipeBookmarkRecord, 0, limit)
	for rows.Next() {
		var item recipeBookmarkRecord
		if err := rows.Scan(&item.ID, &item.TargetKind, &item.LocalInstanceID, &item.LocalRecipeID, &item.RemoteObjectURI, &item.CreatedAt); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		bookmarks = append(bookmarks, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	page := RecipeBookmarkPage{Items: make([]RecipeBookmarkView, 0, len(bookmarks)), Total: total, Limit: limit, Offset: offset}
	for _, bookmark := range bookmarks {
		view := RecipeBookmarkView{ID: bookmark.ID, TargetKind: bookmark.TargetKind, Availability: "unavailable", CreatedAt: bookmark.CreatedAt}
		switch bookmark.TargetKind {
		case "local":
			if !bookmark.LocalInstanceID.Valid || !bookmark.LocalRecipeID.Valid || bookmark.RemoteObjectURI.Valid {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			if !validLibraryInstanceID(bookmark.LocalInstanceID.String) || bookmark.LocalInstanceID.String != instanceID {
				page.Items = append(page.Items, view)
				continue
			}
			item, readErr := readRecipeDetail(r.Context(), tx, actor, bookmark.LocalRecipeID.String)
			if errors.Is(readErr, errRecipeDetailNotFound) || errors.Is(readErr, errRecipeDetailDenied) {
				page.Items = append(page.Items, view)
				continue
			}
			if readErr != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			view.Availability = "available"
			view.Target = &RecipeBookmarkTarget{LocalRecipeID: bookmark.LocalRecipeID.String}
			view.LocalRecipe = &item
		case "remote":
			if bookmark.LocalInstanceID.Valid || bookmark.LocalRecipeID.Valid || !bookmark.RemoteObjectURI.Valid {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			item, readErr := readCachedPublicRemoteRecipe(r.Context(), tx, actor, bookmark.RemoteObjectURI.String)
			if errors.Is(readErr, errRemoteRecipeNotFound) {
				page.Items = append(page.Items, view)
				continue
			}
			if readErr != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			view.Availability = "available"
			view.Target = &RecipeBookmarkTarget{RemoteObjectURI: item.ObjectURI}
			view.RemoteRecipe = &item
		default:
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		page.Items = append(page.Items, view)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h apiHandler) saveRecipeBookmark(w http.ResponseWriter, r *http.Request, actor ContentActor) {
	var input SaveRecipeBookmarkInput
	if !decodeJSON(w, r, &input) {
		return
	}
	kind, err := validateRecipeBookmarkTarget(input.Target)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_bookmark_target")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	var instanceID string
	var localProjection *recipe
	var remoteProjection *RemoteRecipeReadView
	if kind == "local" {
		instanceID, err = currentLibraryInstanceID(r.Context(), tx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		item, readErr := readRecipeDetail(r.Context(), tx, actor, input.Target.LocalRecipeID)
		if errors.Is(readErr, errRecipeDetailNotFound) || errors.Is(readErr, errRecipeDetailDenied) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		localProjection = &item
	} else {
		item, readErr := readCachedPublicRemoteRecipe(r.Context(), tx, actor, input.Target.RemoteObjectURI)
		if errors.Is(readErr, errRemoteRecipeNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if errors.Is(readErr, errRemoteRecipeInvalidURI) {
			writeError(w, http.StatusBadRequest, "invalid_bookmark_target")
			return
		}
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		remoteProjection = &item
	}

	bookmarkID, createdAt, found, err := findRecipeBookmark(r.Context(), tx, actor.AccountID, kind, instanceID, input.Target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if found {
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, recipeBookmarkView(bookmarkID, createdAt, kind, input.Target, localProjection, remoteProjection))
		return
	}
	var count int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id=?`, actor.AccountID).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if count >= maxRecipeBookmarksPerAccount {
		writeError(w, http.StatusConflict, "bookmark_limit_reached")
		return
	}
	bookmarkID = newID()
	createdAt = timestamp()
	result, err := tx.ExecContext(r.Context(), `INSERT INTO recipe_bookmarks(id,user_id,target_kind,local_instance_id,local_recipe_id,remote_object_uri,created_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, bookmarkID, actor.AccountID, kind,
		nullableBookmarkValue(kind == "local", instanceID), nullableBookmarkValue(kind == "local", input.Target.LocalRecipeID),
		nullableBookmarkValue(kind == "remote", input.Target.RemoteObjectURI), createdAt)
	if err != nil {
		if strings.Contains(err.Error(), "recipe bookmark limit reached") {
			writeError(w, http.StatusConflict, "bookmark_limit_reached")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if changed == 0 {
		bookmarkID, createdAt, found, err = findRecipeBookmark(r.Context(), tx, actor.AccountID, kind, instanceID, input.Target)
		if err != nil || !found {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	status := http.StatusCreated
	if changed == 0 {
		status = http.StatusOK
	}
	writeJSON(w, status, recipeBookmarkView(bookmarkID, createdAt, kind, input.Target, localProjection, remoteProjection))
}

func validateRecipeBookmarkTarget(target RecipeBookmarkTarget) (string, error) {
	hasLocal := strings.TrimSpace(target.LocalRecipeID) != ""
	hasRemote := target.RemoteObjectURI != ""
	if hasLocal == hasRemote {
		return "", errors.New("exactly one recipe bookmark target is required")
	}
	if hasLocal {
		if len(target.LocalRecipeID) > 200 || target.LocalRecipeID != strings.TrimSpace(target.LocalRecipeID) {
			return "", errors.New("invalid local recipe bookmark identity")
		}
		return "local", nil
	}
	if _, _, err := parseFederationRecipeObjectURI(target.RemoteObjectURI); err != nil {
		return "", err
	}
	return "remote", nil
}

func findRecipeBookmark(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, kind, instanceID string, target RecipeBookmarkTarget) (string, string, bool, error) {
	var id, createdAt string
	var err error
	if kind == "local" {
		err = queryer.QueryRowContext(ctx, `SELECT id,created_at FROM recipe_bookmarks
			WHERE user_id=? AND target_kind='local' AND local_instance_id=? AND local_recipe_id=?`, userID, instanceID, target.LocalRecipeID).Scan(&id, &createdAt)
	} else {
		err = queryer.QueryRowContext(ctx, `SELECT id,created_at FROM recipe_bookmarks
			WHERE user_id=? AND target_kind='remote' AND remote_object_uri=?`, userID, target.RemoteObjectURI).Scan(&id, &createdAt)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return id, createdAt, err == nil, err
}

func recipeBookmarkView(id, createdAt, kind string, target RecipeBookmarkTarget, local *recipe, remote *RemoteRecipeReadView) RecipeBookmarkView {
	return RecipeBookmarkView{
		ID: id, TargetKind: kind, Target: &target, Availability: "available",
		LocalRecipe: local, RemoteRecipe: remote, CreatedAt: createdAt,
	}
}

func nullableBookmarkValue(valid bool, value string) any {
	if !valid {
		return nil
	}
	return value
}

func (h apiHandler) deleteRecipeBookmark(w http.ResponseWriter, r *http.Request, userID, bookmarkID string) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `DELETE FROM recipe_bookmarks WHERE id=? AND user_id=?`, bookmarkID, userID)
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
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
