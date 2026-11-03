package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strings"
)

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

type auditSuccessMarkerKey struct{}

type auditSuccessMarker struct {
	transactional bool
}

func markTransactionalAuditSuccess(r *http.Request) {
	if marker, ok := r.Context().Value(auditSuccessMarkerKey{}).(*auditSuccessMarker); ok {
		marker.transactional = true
	}
}

type auditExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertAuditEvent(execer auditExecer, actorID, action, resourceType, resourceID string, status int) error {
	_, err := execer.Exec(`INSERT INTO audit_events(id,actor_id,action,resource_type,resource_id,response_status,created_at) VALUES(?,?,?,?,?,?,?)`, newID(), actorID, action, resourceType, resourceID, status, timestamp())
	return err
}

func recordAuditBestEffort(db *sql.DB, actorID, action, resourceType, resourceID string, status int) {
	if err := insertAuditEvent(db, actorID, action, resourceType, resourceID, status); err != nil {
		log.Printf("audit event write failed: %v", err)
	}
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func auditChanges(db *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &auditResponseWriter{ResponseWriter: w}
		marker := &auditSuccessMarker{}
		r = r.WithContext(context.WithValue(r.Context(), auditSuccessMarkerKey{}, marker))
		next.ServeHTTP(recorder, r)
		if ((recorder.status < http.StatusOK || recorder.status >= http.StatusMultipleChoices) && recorder.status != http.StatusForbidden) || (r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions) {
			return
		}
		if recorder.status >= http.StatusOK && recorder.status < http.StatusMultipleChoices && marker.transactional {
			return
		}
		actorID := authenticatedUser(r)
		if actorID == "" {
			return
		}
		resourceType, resourceID := auditResource(r.URL.Path)
		action := auditAction(r.Method, r.URL.Path)
		if err := insertAuditEvent(db, actorID, action, resourceType, resourceID, recorder.status); err != nil {
			log.Printf("audit event write failed: %v", err)
		}
	})
}

func auditAction(method, path string) string {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/"), "/"), "/")
	if len(parts) > 1 && (parts[0] == "recipes" || parts[0] == "cookbooks") {
		parts[1] = "{id}"
		if parts[0] == "recipes" && len(parts) > 3 && parts[2] == "revisions" {
			parts[3] = "{version}"
		}
	}
	if len(parts) > 2 && parts[0] == "admin" && parts[1] == "users" {
		parts[2] = "{id}"
	}
	if len(parts) > 2 && parts[0] == "auth" && (parts[1] == "passkeys" || parts[1] == "sessions") {
		parts[2] = "{id}"
	}
	return method + " /" + strings.Join(parts, "/")
}

func auditResource(path string) (string, string) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "api", ""
	}
	resourceType := parts[0]
	if resourceType == "admin" || resourceType == "auth" {
		return resourceType, ""
	}
	if len(parts) > 1 {
		return resourceType, parts[1]
	}
	return resourceType, ""
}

type auditEvent struct {
	ID             string `json:"id"`
	ActorID        string `json:"actor_id,omitempty"`
	ActorName      string `json:"actor_name"`
	Action         string `json:"action"`
	ResourceType   string `json:"resource_type"`
	ResourceID     string `json:"resource_id,omitempty"`
	ResponseStatus int    `json:"response_status"`
	CreatedAt      string `json:"created_at"`
}

func (h apiHandler) auditEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var admin int
	if err := h.db.QueryRow(`SELECT is_admin FROM users WHERE id=?`, authenticatedUser(r)).Scan(&admin); err != nil || admin != 1 {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	rows, err := h.db.Query(`SELECT e.id,COALESCE(e.actor_id,''),COALESCE(u.display_name,'Former user'),e.action,e.resource_type,e.resource_id,e.response_status,e.created_at FROM audit_events e LEFT JOIN users u ON u.id=e.actor_id ORDER BY e.created_at DESC,e.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []auditEvent{}
	for rows.Next() {
		var item auditEvent
		if err := rows.Scan(&item.ID, &item.ActorID, &item.ActorName, &item.Action, &item.ResourceType, &item.ResourceID, &item.ResponseStatus, &item.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
