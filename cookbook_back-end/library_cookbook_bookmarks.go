package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const maxCookbookBookmarksPerAccount = 10000

const cookbookBookmarkSchemaDDL = `
CREATE TABLE cookbook_bookmarks (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  local_instance_id TEXT NOT NULL
    CHECK (length(local_instance_id)=32 AND local_instance_id NOT GLOB '*[^0-9a-f]*'),
  local_cookbook_id TEXT NOT NULL
    CHECK (length(trim(local_cookbook_id)) BETWEEN 1 AND 80 AND instr(local_cookbook_id,char(0))=0),
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX cookbook_bookmarks_identity_uq
  ON cookbook_bookmarks(user_id,local_instance_id,local_cookbook_id);
CREATE INDEX idx_cookbook_bookmarks_user_created
  ON cookbook_bookmarks(user_id,created_at DESC,id);
`

const cookbookBookmarkLimitTriggerDDL = `CREATE TRIGGER cookbook_bookmarks_account_limit
BEFORE INSERT ON cookbook_bookmarks
WHEN (SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id=NEW.user_id) >= 10000
BEGIN SELECT RAISE(ABORT, 'cookbook bookmark limit reached'); END`

// ApplyCookbookBookmarkMigration applies the additive v8 local reference table.
// The migration runner applies the DDL and ledger row in one transaction.
func ApplyCookbookBookmarkMigration(ctx context.Context, exec sqliteExecutor) error {
	for _, statement := range strings.Split(cookbookBookmarkSchemaDDL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := exec.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply cookbook bookmark schema: %w", err)
		}
	}
	if _, err := exec.ExecContext(ctx, cookbookBookmarkLimitTriggerDDL); err != nil {
		return fmt.Errorf("apply cookbook bookmark limit: %w", err)
	}
	return nil
}

type CookbookBookmarkTarget struct {
	LocalCookbookID string `json:"local_cookbook_id"`
}

type SaveCookbookBookmarkInput struct {
	LocalCookbookID string `json:"local_cookbook_id"`
}

type CookbookBookmarkView struct {
	ID           string                  `json:"id"`
	TargetKind   string                  `json:"target_kind"`
	Target       *CookbookBookmarkTarget `json:"target,omitempty"`
	Availability string                  `json:"availability"`
	Cookbook     *cookbook               `json:"cookbook,omitempty"`
	CreatedAt    string                  `json:"created_at"`
}

type CookbookBookmarkPage struct {
	Items  []CookbookBookmarkView `json:"items"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

type cookbookBookmarkRecord struct {
	ID              string
	LocalInstanceID string
	LocalCookbookID string
	CreatedAt       string
}

func (h apiHandler) cookbookBookmarks(w http.ResponseWriter, r *http.Request, bookmarkID string) {
	w.Header().Set("Cache-Control", "private, no-store")
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
		h.deleteCookbookBookmark(w, r, userID, bookmarkID)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listCookbookBookmarks(w, r, ContentActor{AccountID: userID})
	case http.MethodPost:
		h.saveCookbookBookmark(w, r, ContentActor{AccountID: userID})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) listCookbookBookmarks(w http.ResponseWriter, r *http.Request, actor ContentActor) {
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
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id=?`, actor.AccountID).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,local_instance_id,local_cookbook_id,created_at
		FROM cookbook_bookmarks WHERE user_id=? ORDER BY created_at DESC,id LIMIT ? OFFSET ?`, actor.AccountID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	bookmarks := make([]cookbookBookmarkRecord, 0, limit)
	for rows.Next() {
		var item cookbookBookmarkRecord
		if err := rows.Scan(&item.ID, &item.LocalInstanceID, &item.LocalCookbookID, &item.CreatedAt); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if !validLibraryInstanceID(item.LocalInstanceID) || item.LocalCookbookID == "" || strings.TrimSpace(item.LocalCookbookID) != item.LocalCookbookID || len(item.LocalCookbookID) > 80 {
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
	localIDs := make([]string, 0, len(bookmarks))
	for _, item := range bookmarks {
		if item.LocalInstanceID == instanceID {
			localIDs = append(localIDs, item.LocalCookbookID)
		}
	}
	readable, err := readCookbookDetails(r.Context(), tx, actor.AccountID, localIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	page := CookbookBookmarkPage{Items: make([]CookbookBookmarkView, 0, len(bookmarks)), Total: total, Limit: limit, Offset: offset}
	for _, bookmark := range bookmarks {
		view := CookbookBookmarkView{ID: bookmark.ID, TargetKind: "local_cookbook", Availability: "unavailable", CreatedAt: bookmark.CreatedAt}
		if bookmark.LocalInstanceID == instanceID {
			if item, ok := readable[bookmark.LocalCookbookID]; ok {
				view.Availability = "available"
				view.Target = &CookbookBookmarkTarget{LocalCookbookID: bookmark.LocalCookbookID}
				view.Cookbook = &item
			}
		}
		page.Items = append(page.Items, view)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h apiHandler) saveCookbookBookmark(w http.ResponseWriter, r *http.Request, actor ContentActor) {
	var input SaveCookbookBookmarkInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.LocalCookbookID == "" || strings.TrimSpace(input.LocalCookbookID) != input.LocalCookbookID || len(input.LocalCookbookID) > 80 || strings.ContainsRune(input.LocalCookbookID, '\x00') {
		writeError(w, http.StatusBadRequest, "invalid_bookmark_target")
		return
	}
	var status int
	var response CookbookBookmarkView
	err := withImmediateSQLiteWriteTx(r.Context(), h.db, func(tx sqliteExecutor) error {
		item, err := readCookbookDetail(r.Context(), tx, actor.AccountID, input.LocalCookbookID)
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
			return nil
		}
		if err != nil {
			return err
		}
		instanceID, err := currentLibraryInstanceID(r.Context(), tx)
		if err != nil {
			return err
		}
		var bookmarkID, createdAt string
		err = tx.QueryRowContext(r.Context(), `SELECT id,created_at FROM cookbook_bookmarks
			WHERE user_id=? AND local_instance_id=? AND local_cookbook_id=?`, actor.AccountID, instanceID, input.LocalCookbookID).Scan(&bookmarkID, &createdAt)
		if err == nil {
			status = http.StatusOK
			response = availableCookbookBookmark(bookmarkID, input.LocalCookbookID, createdAt, item)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id=?`, actor.AccountID).Scan(&count); err != nil {
			return err
		}
		if count >= maxCookbookBookmarksPerAccount {
			status = http.StatusConflict
			return nil
		}
		bookmarkID, createdAt = newID(), timestamp()
		result, err := tx.ExecContext(r.Context(), `INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at)
			VALUES(?,?,?,?,?) ON CONFLICT(user_id,local_instance_id,local_cookbook_id) DO NOTHING`, bookmarkID, actor.AccountID, instanceID, input.LocalCookbookID, createdAt)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			if err := tx.QueryRowContext(r.Context(), `SELECT id,created_at FROM cookbook_bookmarks
				WHERE user_id=? AND local_instance_id=? AND local_cookbook_id=?`, actor.AccountID, instanceID, input.LocalCookbookID).Scan(&bookmarkID, &createdAt); err != nil {
				return err
			}
			status = http.StatusOK
		} else {
			status = http.StatusCreated
		}
		response = availableCookbookBookmark(bookmarkID, input.LocalCookbookID, createdAt, item)
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	switch status {
	case http.StatusNotFound:
		writeError(w, http.StatusNotFound, "not_found")
	case http.StatusConflict:
		writeError(w, http.StatusConflict, "cookbook_bookmark_limit_reached")
	default:
		writeJSON(w, status, response)
	}
}

func availableCookbookBookmark(bookmarkID, cookbookID, createdAt string, item cookbook) CookbookBookmarkView {
	return CookbookBookmarkView{
		ID:           bookmarkID,
		TargetKind:   "local_cookbook",
		Target:       &CookbookBookmarkTarget{LocalCookbookID: cookbookID},
		Availability: "available",
		Cookbook:     &item,
		CreatedAt:    createdAt,
	}
}

func (h apiHandler) deleteCookbookBookmark(w http.ResponseWriter, r *http.Request, userID, bookmarkID string) {
	result, err := h.db.ExecContext(r.Context(), `DELETE FROM cookbook_bookmarks WHERE id=? AND user_id=?`, bookmarkID, userID)
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
	w.WriteHeader(http.StatusNoContent)
}
