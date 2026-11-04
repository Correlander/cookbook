package main

import (
	"database/sql"
	"net/http"
)

type LibraryCourseItem struct {
	Availability  string   `json:"availability"`
	Relationships []string `json:"relationships"`
	BookmarkID    string   `json:"bookmark_id,omitempty"`
	CreatedAt     string   `json:"created_at,omitempty"`
	Course        *course  `json:"course,omitempty"`
}

type LibraryCoursePage struct {
	Items  []LibraryCourseItem `json:"items"`
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

func (h apiHandler) libraryCourses(w http.ResponseWriter, r *http.Request) {
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
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer tx.Rollback()
	instanceID, err := currentLibraryInstanceID(r.Context(), tx)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	var coursesEnabled int
	if err := tx.QueryRowContext(r.Context(), `SELECT courses_enabled FROM instance_settings WHERE id=1`).Scan(&coursesEnabled); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	enabled := coursesEnabled == 1
	available := `SELECT c.id,c.owner_id,u.display_name,c.title,c.description,c.visibility,
 (SELECT COUNT(*) FROM course_lessons l WHERE l.course_id=c.id) AS lesson_count,c.created_at AS course_created,c.updated_at,
 COALESCE(b.id,'') AS bookmark_id,'' AS saved_at,c.updated_at AS sort_at,(c.owner_id=?) AS owned,(b.id IS NOT NULL) AS saved
 FROM courses c JOIN users u ON u.id=c.owner_id JOIN instance_settings s ON s.id=1
 LEFT JOIN course_bookmarks b ON b.user_id=? AND b.local_instance_id=? AND b.local_course_id=c.id
 WHERE ?=1 AND (c.owner_id=? OR b.id IS NOT NULL)
 AND (c.owner_id=? OR (s.public_instance=1 AND c.visibility='public'))`
	entries := `WITH entries AS (` + available + ` UNION ALL
 SELECT '' AS id,'' AS owner_id,'' AS display_name,'' AS title,'' AS description,'' AS visibility,0 AS lesson_count,'' AS course_created,'' AS updated_at,b.id AS bookmark_id,b.created_at AS saved_at,b.created_at AS sort_at,0 AS owned,1 AS saved
 FROM course_bookmarks b LEFT JOIN courses c ON c.id=b.local_course_id AND b.local_instance_id=? JOIN instance_settings s ON s.id=1
 WHERE b.user_id=? AND (?=0 OR b.local_instance_id<>? OR c.id IS NULL OR NOT (c.owner_id=? OR (s.public_instance=1 AND c.visibility='public')))
 )`
	args := []any{uid, uid, instanceID, boolInt(enabled), uid, uid, instanceID, uid, boolInt(enabled), instanceID, uid}
	var total int
	if err := tx.QueryRowContext(r.Context(), entries+` SELECT COUNT(*) FROM entries`, args...).Scan(&total); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	rows, err := tx.QueryContext(r.Context(), entries+` SELECT id,owner_id,display_name,title,description,visibility,lesson_count,course_created,updated_at,bookmark_id,saved_at,sort_at,owned,saved FROM entries ORDER BY sort_at DESC,CASE WHEN id<>'' THEN 'c:'||id ELSE 'b:'||bookmark_id END LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	page := LibraryCoursePage{Items: []LibraryCourseItem{}, Total: total, Limit: limit, Offset: offset}
	availableIDs := []string{}
	for rows.Next() {
		var item LibraryCourseItem
		var id, ownerID, ownerName, title, description, visibility, createdAt, updatedAt, savedAt, sortAt string
		var lessonCount, owned, saved int
		if err := rows.Scan(&id, &ownerID, &ownerName, &title, &description, &visibility, &lessonCount, &createdAt, &updatedAt, &item.BookmarkID, &savedAt, &sortAt, &owned, &saved); err != nil {
			rows.Close()
			writeError(w, 500, "storage_error")
			return
		}
		if id == "" {
			item.Availability = "unavailable"
			item.Relationships = []string{"saved"}
			item.CreatedAt = savedAt
		} else {
			availableIDs = append(availableIDs, id)
			item.Availability = "available"
			item.Relationships = []string{}
			if owned != 0 {
				item.Relationships = append(item.Relationships, "owned")
			}
			if saved != 0 {
				item.Relationships = append(item.Relationships, "saved")
			}
			if ownerID != uid {
				ownerID = ""
			}
			item.Course = &course{ID: id, OwnerID: ownerID, OwnerName: ownerName, Title: title, Description: description, Visibility: visibility, LessonCount: lessonCount, CreatedAt: createdAt, UpdatedAt: updatedAt}
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "storage_error")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	readableCourses, err := readCourseDetails(r.Context(), tx, uid, availableIDs)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	for i := range page.Items {
		if page.Items[i].Availability != "available" {
			continue
		}
		id := page.Items[i].Course.ID
		item, ok := readableCourses[id]
		if !ok {
			writeError(w, 500, "storage_error")
			return
		}
		if item.OwnerID != uid {
			item.OwnerID = ""
		}
		page.Items[i].Course = &item
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
