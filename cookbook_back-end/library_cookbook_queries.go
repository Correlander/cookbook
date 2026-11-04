package main

import (
	"database/sql"
	"net/http"
)

type LibraryCookbookItem struct {
	Availability   string    `json:"availability"`
	BookmarkID     string    `json:"bookmark_id,omitempty"`
	Relationships  []string  `json:"relationships"`
	MembershipRole string    `json:"membership_role,omitempty"`
	Cookbook       *cookbook `json:"cookbook,omitempty"`
	CreatedAt      string    `json:"created_at,omitempty"`
}

type LibraryCookbookPage struct {
	Items  []LibraryCookbookItem `json:"items"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

func (h apiHandler) libraryCookbooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	userID := authenticatedUser(r)
	if userID == "" {
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
	const candidates = `WITH candidate AS (
 SELECT c.id,c.updated_at,c.owner_id,
 EXISTS(SELECT 1 FROM cookbook_memberships m WHERE m.cookbook_id=c.id AND m.user_id=?) AS is_shared,
 COALESCE((SELECT m.role FROM cookbook_memberships m WHERE m.cookbook_id=c.id AND m.user_id=? LIMIT 1),'') AS member_role,
 (SELECT b.id FROM cookbook_bookmarks b WHERE b.user_id=? AND b.local_instance_id=? AND b.local_cookbook_id=c.id) AS bookmark_id
 FROM cookbooks c JOIN instance_settings s ON s.id=1
 WHERE c.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships m WHERE m.cookbook_id=c.id AND m.user_id=?)
 OR (EXISTS(SELECT 1 FROM cookbook_bookmarks b WHERE b.user_id=? AND b.local_instance_id=? AND b.local_cookbook_id=c.id) AND ` + cookbookActorReadPredicate + `)
), entries AS (
 SELECT id AS entry_id, id AS cookbook_id, COALESCE(bookmark_id,'') AS bookmark_id, 'available' AS availability, updated_at AS sort_at, '' AS created_at, is_shared,member_role,(owner_id=? ) AS is_owned,(bookmark_id IS NOT NULL) AS is_saved FROM candidate
 UNION ALL
 SELECT '' AS entry_id,'' AS cookbook_id,b.id AS bookmark_id,'unavailable' AS availability,b.created_at AS sort_at,b.created_at,0 AS is_shared,'' AS member_role,0 AS is_owned,1 AS is_saved
 FROM cookbook_bookmarks b LEFT JOIN cookbooks c ON c.id=b.local_cookbook_id JOIN instance_settings s ON s.id=1
 WHERE b.user_id=? AND (b.local_instance_id<>? OR c.id IS NULL OR NOT ` + cookbookActorReadPredicate + `)
)`
	args := []any{userID, userID, userID, instanceID, userID, userID, userID, instanceID, userID, userID, userID, userID, instanceID, userID, userID}
	var total int
	if err := tx.QueryRowContext(r.Context(), candidates+` SELECT COUNT(*) FROM entries`, args...).Scan(&total); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	rows, err := tx.QueryContext(r.Context(), candidates+` SELECT entry_id,cookbook_id,bookmark_id,availability,sort_at,created_at,is_shared,member_role,is_owned,is_saved FROM entries ORDER BY sort_at DESC,CASE availability WHEN 'available' THEN 'c:'||entry_id ELSE 'b:'||bookmark_id END LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	page := LibraryCookbookPage{Items: []LibraryCookbookItem{}, Total: total, Limit: limit, Offset: offset}
	ids := []string{}
	for rows.Next() {
		var item LibraryCookbookItem
		var entryID, cookbookID, sortAt string
		var shared, owned, saved int
		if err := rows.Scan(&entryID, &cookbookID, &item.BookmarkID, &item.Availability, &sortAt, &item.CreatedAt, &shared, &item.MembershipRole, &owned, &saved); err != nil {
			rows.Close()
			writeError(w, 500, "storage_error")
			return
		}
		if item.Availability == "available" {
			ids = append(ids, cookbookID)
			if owned != 0 {
				item.Relationships = append(item.Relationships, "owned")
			}
			if shared != 0 {
				item.Relationships = append(item.Relationships, "shared")
			}
			if saved != 0 {
				item.Relationships = append(item.Relationships, "saved")
			}
		} else {
			item.Relationships = []string{"saved"}
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
	readable, err := readCookbookDetails(r.Context(), tx, userID, ids)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	for i := range page.Items {
		if page.Items[i].Availability == "available" {
			id := ids[0]
			ids = ids[1:]
			book, ok := readable[id]
			if !ok {
				writeError(w, 500, "storage_error")
				return
			}
			page.Items[i].Cookbook = &book
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	writeJSON(w, 200, page)
}
