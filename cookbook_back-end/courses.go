package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

type course struct {
	ID          string `json:"id"`
	OwnerID     string `json:"owner_id,omitempty"`
	OwnerName   string `json:"owner_name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	LessonCount int    `json:"lesson_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type courseLesson struct {
	ID        string `json:"id"`
	CourseID  string `json:"course_id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	RecipeID  string `json:"recipe_id,omitempty"`
	Position  int    `json:"position"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (h apiHandler) courseRoutes(w http.ResponseWriter, r *http.Request, parts []string) {
	if !coursesAreEnabled(h.db) {
		writeError(w, http.StatusNotFound, "courses_disabled")
		return
	}
	switch {
	case len(parts) == 1:
		h.courseCollection(w, r)
	case len(parts) == 2:
		h.courseItem(w, r, parts[1])
	case len(parts) == 3 && parts[2] == "lessons":
		h.courseLessons(w, r, parts[1])
	case len(parts) == 4 && parts[2] == "lessons":
		h.courseLessonItem(w, r, parts[1], parts[3])
	default:
		http.NotFound(w, r)
	}
}

func (h apiHandler) publicCourseRoutes(w http.ResponseWriter, r *http.Request, parts []string) {
	if !coursesAreEnabled(h.db) {
		writeError(w, http.StatusNotFound, "courses_disabled")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	switch {
	case len(parts) == 0:
		h.listCourses(w, r, "")
	case len(parts) == 1:
		h.readCourse(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "lessons":
		if _, ok := h.readCourseRecord(w, r, parts[0]); ok {
			h.listCourseLessons(w, r, parts[0])
		}
	default:
		http.NotFound(w, r)
	}
}

func coursesAreEnabled(db *sql.DB) bool {
	var enabled int
	return db.QueryRow(`SELECT courses_enabled FROM instance_settings WHERE id=1`).Scan(&enabled) == nil && enabled == 1
}

func (h apiHandler) courseCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listCourses(w, r, authenticatedUser(r))
	case http.MethodPost:
		var input struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Visibility  string `json:"visibility"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		if !validCourseFields(input.Title, input.Description, input.Visibility) || (input.Visibility == "public" && !h.instancePublic()) {
			writeError(w, http.StatusBadRequest, "invalid_course")
			return
		}
		uid := authenticatedUser(r)
		id, now := newID(), timestamp()
		if _, err := h.db.Exec(`INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, uid, input.Title, strings.TrimSpace(input.Description), input.Visibility, now, now); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		item, ok := h.readCourseRecord(w, requestForUser(http.MethodGet, "/api/courses/"+id, uid), id)
		if ok {
			writeJSON(w, http.StatusCreated, item)
		}
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) listCourses(w http.ResponseWriter, r *http.Request, uid string) {
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	rows, err := h.db.Query(`SELECT c.id,c.owner_id,u.display_name,c.title,c.description,c.visibility,COUNT(l.id),c.created_at,c.updated_at
		FROM courses c JOIN users u ON u.id=c.owner_id JOIN instance_settings s ON s.id=1
		LEFT JOIN course_lessons l ON l.course_id=c.id
		WHERE c.owner_id=? OR (s.public_instance=1 AND c.visibility='public')
		GROUP BY c.id ORDER BY c.updated_at DESC,c.id LIMIT ? OFFSET ?`, uid, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []course{}
	for rows.Next() {
		var item course
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.OwnerName, &item.Title, &item.Description, &item.Visibility, &item.LessonCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if uid != item.OwnerID {
			item.OwnerID = ""
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h apiHandler) courseItem(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		h.readCourse(w, r, id)
	case http.MethodPatch:
		var input struct {
			Title       *string `json:"title"`
			Description *string `json:"description"`
			Visibility  *string `json:"visibility"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		uid := authenticatedUser(r)
		var current course
		if err := h.db.QueryRow(`SELECT id,owner_id,title,description,visibility,created_at,updated_at FROM courses WHERE id=?`, id).Scan(&current.ID, &current.OwnerID, &current.Title, &current.Description, &current.Visibility, &current.CreatedAt, &current.UpdatedAt); err != nil || current.OwnerID != uid {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if input.Title != nil {
			current.Title = strings.TrimSpace(*input.Title)
		}
		if input.Description != nil {
			current.Description = strings.TrimSpace(*input.Description)
		}
		if input.Visibility != nil {
			current.Visibility = *input.Visibility
		}
		if !validCourseFields(current.Title, current.Description, current.Visibility) || (current.Visibility == "public" && !h.instancePublic()) {
			writeError(w, http.StatusBadRequest, "invalid_course")
			return
		}
		if _, err := h.db.Exec(`UPDATE courses SET title=?,description=?,visibility=?,updated_at=? WHERE id=? AND owner_id=?`, current.Title, current.Description, current.Visibility, timestamp(), id, uid); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		h.readCourse(w, requestForUser(http.MethodGet, "/api/courses/"+id, uid), id)
	case http.MethodDelete:
		result, err := h.db.Exec(`DELETE FROM courses WHERE id=? AND owner_id=?`, id, authenticatedUser(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func validCourseFields(title, description, visibility string) bool {
	return title != "" && len(title) <= 200 && len(description) <= 5000 && (visibility == "private" || visibility == "public")
}

func (h apiHandler) readCourse(w http.ResponseWriter, r *http.Request, id string) {
	item, ok := h.readCourseRecord(w, r, id)
	if ok {
		writeJSON(w, http.StatusOK, item)
	}
}

func (h apiHandler) readCourseRecord(w http.ResponseWriter, r *http.Request, id string) (course, bool) {
	var item course
	var public int
	err := h.db.QueryRow(`SELECT c.id,c.owner_id,u.display_name,c.title,c.description,c.visibility,COUNT(l.id),c.created_at,c.updated_at,s.public_instance
		FROM courses c JOIN users u ON u.id=c.owner_id JOIN instance_settings s ON s.id=1
		LEFT JOIN course_lessons l ON l.course_id=c.id WHERE c.id=? GROUP BY c.id`, id).Scan(&item.ID, &item.OwnerID, &item.OwnerName, &item.Title, &item.Description, &item.Visibility, &item.LessonCount, &item.CreatedAt, &item.UpdatedAt, &public)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return course{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return course{}, false
	}
	if !canReadResource(authenticatedUser(r), access{instancePublic: public == 1, visibility: visibility(item.Visibility), ownerID: item.OwnerID}) {
		writeError(w, http.StatusNotFound, "not_found")
		return course{}, false
	}
	if authenticatedUser(r) != item.OwnerID {
		item.OwnerID = ""
	}
	return item, true
}

func (h apiHandler) courseLessons(w http.ResponseWriter, r *http.Request, courseID string) {
	if r.Method == http.MethodGet {
		if _, ok := h.readCourseRecord(w, r, courseID); ok {
			h.listCourseLessons(w, r, courseID)
		}
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !h.ownsCourse(courseID, authenticatedUser(r)) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var input struct {
		Title    string `json:"title"`
		Content  string `json:"content"`
		RecipeID string `json:"recipe_id"`
		Position int    `json:"position"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	if !validLessonFields(input.Title, input.Content, input.Position) || (input.RecipeID != "" && !h.canReadRecipe(input.RecipeID, authenticatedUser(r))) {
		writeError(w, http.StatusBadRequest, "invalid_course_lesson")
		return
	}
	id, now := newID(), timestamp()
	if _, err := h.db.Exec(`INSERT INTO course_lessons(id,course_id,title,content,recipe_id,position,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, courseID, input.Title, input.Content, nullableString(input.RecipeID), input.Position, now, now); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusCreated, courseLesson{ID: id, CourseID: courseID, Title: input.Title, Content: input.Content, RecipeID: input.RecipeID, Position: input.Position, CreatedAt: now, UpdatedAt: now})
}

func (h apiHandler) listCourseLessons(w http.ResponseWriter, r *http.Request, courseID string) {
	rows, err := h.db.Query(`SELECT id,course_id,title,content,COALESCE(recipe_id,''),position,created_at,updated_at FROM course_lessons WHERE course_id=? ORDER BY position,id`, courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []courseLesson{}
	uid := authenticatedUser(r)
	for rows.Next() {
		var item courseLesson
		if err := rows.Scan(&item.ID, &item.CourseID, &item.Title, &item.Content, &item.RecipeID, &item.Position, &item.CreatedAt, &item.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	for index := range items {
		if items[index].RecipeID != "" && !h.canReadRecipe(items[index].RecipeID, uid) {
			items[index].RecipeID = ""
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h apiHandler) courseLessonItem(w http.ResponseWriter, r *http.Request, courseID, lessonID string) {
	uid := authenticatedUser(r)
	if !h.ownsCourse(courseID, uid) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method == http.MethodDelete {
		result, err := h.db.Exec(`DELETE FROM course_lessons WHERE id=? AND course_id=?`, lessonID, courseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var input struct {
		Title    *string `json:"title"`
		Content  *string `json:"content"`
		RecipeID *string `json:"recipe_id"`
		Position *int    `json:"position"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var item courseLesson
	if err := h.db.QueryRow(`SELECT id,course_id,title,content,COALESCE(recipe_id,''),position,created_at,updated_at FROM course_lessons WHERE id=? AND course_id=?`, lessonID, courseID).Scan(&item.ID, &item.CourseID, &item.Title, &item.Content, &item.RecipeID, &item.Position, &item.CreatedAt, &item.UpdatedAt); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if input.Title != nil {
		item.Title = strings.TrimSpace(*input.Title)
	}
	if input.Content != nil {
		item.Content = strings.TrimSpace(*input.Content)
	}
	if input.RecipeID != nil {
		item.RecipeID = strings.TrimSpace(*input.RecipeID)
	}
	if input.Position != nil {
		item.Position = *input.Position
	}
	if !validLessonFields(item.Title, item.Content, item.Position) || (item.RecipeID != "" && !h.canReadRecipe(item.RecipeID, uid)) {
		writeError(w, http.StatusBadRequest, "invalid_course_lesson")
		return
	}
	item.UpdatedAt = timestamp()
	if _, err := h.db.Exec(`UPDATE course_lessons SET title=?,content=?,recipe_id=?,position=?,updated_at=? WHERE id=? AND course_id=?`, item.Title, item.Content, nullableString(item.RecipeID), item.Position, item.UpdatedAt, lessonID, courseID); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func validLessonFields(title, content string, position int) bool {
	return title != "" && len(title) <= 200 && len(content) <= 20000 && position >= 0 && position <= 10000
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (h apiHandler) ownsCourse(courseID, uid string) bool {
	var count int
	return uid != "" && h.db.QueryRow(`SELECT COUNT(*) FROM courses WHERE id=? AND owner_id=?`, courseID, uid).Scan(&count) == nil && count == 1
}

func (h apiHandler) canReadRecipe(recipeID, uid string) bool {
	var owner string
	var itemVisibility visibility
	var public int
	if err := h.db.QueryRow(`SELECT r.owner_id,r.visibility,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, recipeID).Scan(&owner, &itemVisibility, &public); err != nil {
		return false
	}
	member := recipeMemberRole(h.db, recipeID, uid)
	return canReadResource(uid, access{instancePublic: public == 1, visibility: itemVisibility, ownerID: owner, memberRole: member})
}
