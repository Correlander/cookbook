package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

const maxCourseBookmarksPerAccount = 10000

const courseBookmarkSchemaDDL = `
CREATE TABLE course_bookmarks (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  local_instance_id TEXT NOT NULL CHECK (length(local_instance_id)=32 AND local_instance_id NOT GLOB '*[^0-9a-f]*'),
  local_course_id TEXT NOT NULL CHECK (length(trim(local_course_id)) BETWEEN 1 AND 80 AND instr(local_course_id,char(0))=0),
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX course_bookmarks_identity_uq ON course_bookmarks(user_id,local_instance_id,local_course_id);
CREATE INDEX idx_course_bookmarks_user_created ON course_bookmarks(user_id,created_at DESC,id);
`

const courseBookmarkLimitTriggerDDL = `CREATE TRIGGER course_bookmarks_account_limit
BEFORE INSERT ON course_bookmarks
WHEN (SELECT COUNT(*) FROM course_bookmarks WHERE user_id=NEW.user_id) >= 10000
BEGIN SELECT RAISE(ABORT, 'course bookmark limit reached'); END`

func ApplyCourseBookmarkMigration(ctx context.Context, exec sqliteExecutor) error {
	for _, statement := range strings.Split(courseBookmarkSchemaDDL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := exec.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	_, err := exec.ExecContext(ctx, courseBookmarkLimitTriggerDDL)
	return err
}

type CourseBookmarkInput struct {
	LocalCourseID string `json:"local_course_id"`
}
type CourseBookmarkView struct {
	BookmarkID string `json:"bookmark_id"`
	CourseID   string `json:"course_id"`
	CreatedAt  string `json:"created_at"`
}

func (h apiHandler) courseBookmarks(w http.ResponseWriter, r *http.Request, bookmarkID string) {
	w.Header().Set("Cache-Control", "private, no-store")
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if bookmarkID != "" {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		result, err := h.db.ExecContext(r.Context(), `DELETE FROM course_bookmarks WHERE id=? AND user_id=?`, bookmarkID, uid)
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
		return
	}
	if !coursesAreEnabled(h.db) {
		writeError(w, http.StatusNotFound, "courses_disabled")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	case http.MethodPost:
		h.saveCourseBookmark(w, r, uid)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) courseBookmarkStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !coursesAreEnabled(h.db) {
		writeError(w, http.StatusNotFound, "courses_disabled")
		return
	}
	values, ok := r.URL.Query()["local_course_id"]
	if !ok || len(values) != 1 || len(r.URL.Query()) != 1 || !validCourseBookmarkID(values[0]) {
		writeError(w, http.StatusBadRequest, "invalid_bookmark_target")
		return
	}
	var item course
	if err := readLibraryCourse(r.Context(), h.db, uid, values[0], &item); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	instanceID, err := currentLibraryInstanceID(r.Context(), h.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	var id string
	err = h.db.QueryRowContext(r.Context(), `SELECT id FROM course_bookmarks WHERE user_id=? AND local_instance_id=? AND local_course_id=?`, uid, instanceID, item.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"saved": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "bookmark_id": id})
}

func (h apiHandler) saveCourseBookmark(w http.ResponseWriter, r *http.Request, uid string) {
	var input CourseBookmarkInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validCourseBookmarkID(input.LocalCourseID) {
		writeError(w, http.StatusBadRequest, "invalid_bookmark_target")
		return
	}
	status := http.StatusCreated
	var response CourseBookmarkView
	err := withImmediateSQLiteWriteTx(r.Context(), h.db, func(tx sqliteExecutor) error {
		var item course
		if err := readLibraryCourse(r.Context(), tx, uid, input.LocalCourseID, &item); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
				return nil
			}
			return err
		}
		instanceID, err := currentLibraryInstanceID(r.Context(), tx)
		if err != nil {
			return err
		}
		response.CourseID = item.ID
		err = tx.QueryRowContext(r.Context(), `SELECT id,created_at FROM course_bookmarks WHERE user_id=? AND local_instance_id=? AND local_course_id=?`, uid, instanceID, item.ID).Scan(&response.BookmarkID, &response.CreatedAt)
		if err == nil {
			status = http.StatusOK
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		response.BookmarkID, response.CreatedAt = newID(), timestamp()
		_, err = tx.ExecContext(r.Context(), `INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES(?,?,?,?,?)`, response.BookmarkID, uid, instanceID, item.ID, response.CreatedAt)
		if err != nil {
			if strings.Contains(err.Error(), "course bookmark limit reached") {
				status = http.StatusConflict
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if status == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if status == http.StatusConflict {
		writeError(w, http.StatusConflict, "course_bookmark_limit_reached")
		return
	}
	writeJSON(w, status, response)
}

func validCourseBookmarkID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && len(id) <= 80 && !strings.ContainsRune(id, '\x00')
}

func readLibraryCourse(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, uid, id string, item *course) error {
	var public int
	err := queryer.QueryRowContext(ctx, `SELECT c.id,c.owner_id,u.display_name,c.title,c.description,c.visibility,
 (SELECT COUNT(*) FROM course_lessons l WHERE l.course_id=c.id),c.created_at,c.updated_at,s.public_instance
 FROM courses c JOIN users u ON u.id=c.owner_id JOIN instance_settings s ON s.id=1 WHERE c.id=? AND (c.owner_id=? OR (s.public_instance=1 AND c.visibility='public'))`, id, uid).
		Scan(&item.ID, &item.OwnerID, &item.OwnerName, &item.Title, &item.Description, &item.Visibility, &item.LessonCount, &item.CreatedAt, &item.UpdatedAt, &public)
	if err == nil && item.OwnerID != uid {
		item.OwnerID = ""
	}
	return err
}
