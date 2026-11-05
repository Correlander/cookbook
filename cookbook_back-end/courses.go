package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

type course struct {
	ID                  string                   `json:"id"`
	OwnerID             string                   `json:"owner_id,omitempty"`
	OwnerName           string                   `json:"owner_name"`
	Title               string                   `json:"title"`
	Description         string                   `json:"description"`
	CreatorName         string                   `json:"creator_name"`
	CreatorAttributions []creatorAttributionRead `json:"creator_attributions,omitempty"`
	Visibility          string                   `json:"visibility"`
	LessonCount         int                      `json:"lesson_count"`
	CreatedAt           string                   `json:"created_at"`
	UpdatedAt           string                   `json:"updated_at"`
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
			Title              string                   `json:"title"`
			Description        string                   `json:"description"`
			Visibility         string                   `json:"visibility"`
			CreatorAttribution *creatorAttributionInput `json:"creator_attribution,omitempty"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		if !validCourseFields(input.Title, input.Description, input.Visibility) || (input.Visibility == "public" && !h.instancePublic()) || validateCreatorAttributionInput(input.CreatorAttribution) != nil {
			writeError(w, http.StatusBadRequest, "invalid_course")
			return
		}
		uid := authenticatedUser(r)
		id, now := newID(), timestamp()
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		defer tx.Rollback()
		var publicInstance, coursesEnabled int
		if err := tx.QueryRowContext(r.Context(), `SELECT public_instance,courses_enabled FROM instance_settings WHERE id=1`).Scan(&publicInstance, &coursesEnabled); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if coursesEnabled != 1 {
			writeError(w, http.StatusNotFound, "courses_disabled")
			return
		}
		if input.Visibility == "public" && publicInstance == 1 {
			if profileErr := requireCompletedPublisherProfile(r.Context(), tx, uid); profileErr != nil {
				if !writePublisherProfileError(w, profileErr) {
					writeError(w, http.StatusInternalServerError, "storage_error")
				}
				return
			}
		}
		creatorName := ""
		if input.CreatorAttribution != nil && input.CreatorAttribution.Kind == "name_claim" {
			creatorName = input.CreatorAttribution.Name
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO courses(id,owner_id,title,description,creator_name,visibility,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, uid, input.Title, strings.TrimSpace(input.Description), creatorName, input.Visibility, now, now); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if input.CreatorAttribution != nil && input.CreatorAttribution.Kind == "local_profile" {
			profile, err := ensureCreatorProfile(r.Context(), tx, uid)
			if err == nil {
				err = insertLocalAttribution(r.Context(), tx, "course", id, profile.ID, "creator", "self_asserted_at_creation")
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
		}
		item, err := readCourseDetail(r.Context(), tx, uid, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusCreated, item)
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
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(r.Context(), `SELECT c.id FROM courses c JOIN instance_settings s ON s.id=1
		WHERE c.owner_id=? OR (s.public_instance=1 AND c.visibility='public')
		ORDER BY c.updated_at DESC,c.id LIMIT ? OFFSET ?`, uid, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		ids = append(ids, id)
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
	readable, err := readCourseDetails(r.Context(), tx, uid, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	items := make([]course, 0, len(ids))
	for _, id := range ids {
		item, ok := readable[id]
		if !ok {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if uid != item.OwnerID {
			item.OwnerID = ""
		}
		items = append(items, item)
	}
	if err := tx.Commit(); err != nil {
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
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		defer tx.Rollback()
		var current course
		var publicInstance, coursesEnabled int
		if err := tx.QueryRowContext(r.Context(), `SELECT c.id,c.owner_id,c.title,c.description,c.visibility,c.created_at,c.updated_at,s.public_instance,s.courses_enabled
			FROM courses c JOIN instance_settings s ON s.id=1 WHERE c.id=?`, id).Scan(&current.ID, &current.OwnerID, &current.Title, &current.Description, &current.Visibility, &current.CreatedAt, &current.UpdatedAt, &publicInstance, &coursesEnabled); err != nil || current.OwnerID != uid {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if coursesEnabled != 1 {
			writeError(w, http.StatusNotFound, "courses_disabled")
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
		if !validCourseFields(current.Title, current.Description, current.Visibility) || (current.Visibility == "public" && publicInstance != 1) {
			writeError(w, http.StatusBadRequest, "invalid_course")
			return
		}
		if current.Visibility == "public" {
			if profileErr := requireCompletedPublisherProfile(r.Context(), tx, uid); profileErr != nil {
				if !writePublisherProfileError(w, profileErr) {
					writeError(w, http.StatusInternalServerError, "storage_error")
				}
				return
			}
		}
		current.UpdatedAt = timestamp()
		if _, err := tx.ExecContext(r.Context(), `UPDATE courses SET title=?,description=?,visibility=?,updated_at=? WHERE id=? AND owner_id=?`, current.Title, current.Description, current.Visibility, current.UpdatedAt, id, uid); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		item, err := readCourseDetail(r.Context(), tx, uid, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, item)
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
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return course{}, false
	}
	defer tx.Rollback()
	actorID := authenticatedUser(r)
	item, err := readCourseDetail(r.Context(), tx, actorID, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return course{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return course{}, false
	}
	if actorID != item.OwnerID {
		item.OwnerID = ""
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return course{}, false
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
	if !validLessonFields(input.Title, input.Content, input.Position) {
		writeError(w, http.StatusBadRequest, "invalid_course_lesson")
		return
	}
	uid := authenticatedUser(r)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	var ownerID, courseVisibility string
	var publicInstance, coursesEnabled int
	if err := tx.QueryRowContext(r.Context(), `SELECT c.owner_id,c.visibility,s.public_instance,s.courses_enabled FROM courses c JOIN instance_settings s ON s.id=1 WHERE c.id=?`, courseID).Scan(&ownerID, &courseVisibility, &publicInstance, &coursesEnabled); err != nil || ownerID != uid {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if coursesEnabled != 1 {
		writeError(w, http.StatusNotFound, "courses_disabled")
		return
	}
	publiclyReadable := courseVisibility == "public" && publicInstance == 1
	if publiclyReadable {
		if profileErr := requireCompletedPublisherProfile(r.Context(), tx, uid); profileErr != nil {
			if !writePublisherProfileError(w, profileErr) {
				writeError(w, http.StatusInternalServerError, "storage_error")
			}
			return
		}
	}
	if input.RecipeID != "" && !canReadRecipeWithQueryer(r.Context(), tx, input.RecipeID, uid) {
		writeError(w, http.StatusBadRequest, "invalid_course_lesson")
		return
	}
	id, now := newID(), timestamp()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO course_lessons(id,course_id,title,content,recipe_id,position,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, courseID, input.Title, input.Content, nullableString(input.RecipeID), input.Position, now, now); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
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
		if items[index].RecipeID != "" && !h.canReadRecipe(r.Context(), items[index].RecipeID, uid) {
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
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	var ownerID, courseVisibility string
	var publicInstance, coursesEnabled int
	if err := tx.QueryRowContext(r.Context(), `SELECT c.owner_id,c.visibility,s.public_instance,s.courses_enabled FROM courses c JOIN instance_settings s ON s.id=1 WHERE c.id=?`, courseID).Scan(&ownerID, &courseVisibility, &publicInstance, &coursesEnabled); err != nil || ownerID != uid {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if coursesEnabled != 1 {
		writeError(w, http.StatusNotFound, "courses_disabled")
		return
	}
	var item courseLesson
	if err := tx.QueryRowContext(r.Context(), `SELECT id,course_id,title,content,COALESCE(recipe_id,''),position,created_at,updated_at FROM course_lessons WHERE id=? AND course_id=?`, lessonID, courseID).Scan(&item.ID, &item.CourseID, &item.Title, &item.Content, &item.RecipeID, &item.Position, &item.CreatedAt, &item.UpdatedAt); err != nil {
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
	if !validLessonFields(item.Title, item.Content, item.Position) {
		writeError(w, http.StatusBadRequest, "invalid_course_lesson")
		return
	}
	if courseVisibility == "public" && publicInstance == 1 {
		if profileErr := requireCompletedPublisherProfile(r.Context(), tx, uid); profileErr != nil {
			if !writePublisherProfileError(w, profileErr) {
				writeError(w, http.StatusInternalServerError, "storage_error")
			}
			return
		}
	}
	if item.RecipeID != "" && !canReadRecipeWithQueryer(r.Context(), tx, item.RecipeID, uid) {
		writeError(w, http.StatusBadRequest, "invalid_course_lesson")
		return
	}
	item.UpdatedAt = timestamp()
	if _, err := tx.ExecContext(r.Context(), `UPDATE course_lessons SET title=?,content=?,recipe_id=?,position=?,updated_at=? WHERE id=? AND course_id=?`, item.Title, item.Content, nullableString(item.RecipeID), item.Position, item.UpdatedAt, lessonID, courseID); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
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

func (h apiHandler) canReadRecipe(ctx context.Context, recipeID, uid string) bool {
	return canReadRecipeWithQueryer(ctx, h.db, recipeID, uid)
}

func canReadRecipeWithQueryer(ctx context.Context, queryer contentQueryer, recipeID, uid string) bool {
	var owner string
	var itemVisibility visibility
	var public int
	if err := queryer.QueryRowContext(ctx, `SELECT r.owner_id,r.visibility,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, recipeID).Scan(&owner, &itemVisibility, &public); err != nil {
		return false
	}
	member, err := recipeMemberRoleContext(ctx, queryer, recipeID, uid)
	if err != nil {
		return false
	}
	if !canReadResource(uid, access{instancePublic: public == 1, visibility: itemVisibility, ownerID: owner, memberRole: member}) {
		return false
	}
	if hasLocalRecipeRead(uid, owner, member) {
		return true
	}
	facts, err := readRecipePublicReadFacts(ctx, queryer, recipeID)
	return err == nil && facts.AnonymousEligible()
}
