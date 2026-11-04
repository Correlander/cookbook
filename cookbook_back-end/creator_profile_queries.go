package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

type creatorProfileRecipeItem struct {
	Recipe     recipe `json:"recipe"`
	Role       string `json:"role"`
	Provenance string `json:"provenance"`
}

type creatorProfileCookbook struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Visibility  visibility `json:"visibility"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

type creatorProfileCookbookItem struct {
	Cookbook   creatorProfileCookbook `json:"cookbook"`
	Role       string                 `json:"role"`
	Provenance string                 `json:"provenance"`
}

type creatorProfileRecipeList struct {
	Profile creatorProfile             `json:"profile"`
	Items   []creatorProfileRecipeItem `json:"items"`
	Limit   int                        `json:"limit"`
	Offset  int                        `json:"offset"`
}

type creatorProfileCookbookList struct {
	Profile creatorProfile               `json:"profile"`
	Items   []creatorProfileCookbookItem `json:"items"`
	Limit   int                          `json:"limit"`
	Offset  int                          `json:"offset"`
}

type creatorProfileCourse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type creatorProfileCourseItem struct {
	Course     creatorProfileCourse `json:"course"`
	Role       string               `json:"role"`
	Provenance string               `json:"provenance"`
}

type creatorProfileCourseList struct {
	Profile creatorProfile             `json:"profile"`
	Items   []creatorProfileCourseItem `json:"items"`
	Total   int                        `json:"total"`
	Limit   int                        `json:"limit"`
	Offset  int                        `json:"offset"`
}

func recipeCookbookIDsForActor(ctx context.Context, tx *sql.Tx, actorID string, recipeIDs []string) (map[string][]string, error) {
	if len(recipeIDs) == 0 {
		return map[string][]string{}, nil
	}
	var placeholders strings.Builder
	args := make([]any, 0, len(recipeIDs)+2)
	for index, id := range recipeIDs {
		if index > 0 {
			placeholders.WriteByte(',')
		}
		placeholders.WriteByte('?')
		args = append(args, id)
	}
	args = append(args, actorID, actorID)
	query := `SELECT cr.recipe_id,cr.cookbook_id FROM cookbook_recipes cr
		JOIN cookbooks c ON c.id=cr.cookbook_id JOIN instance_settings s ON s.id=1
		WHERE cr.recipe_id IN (` + placeholders.String() + `) AND ` + cookbookActorReadPredicate + `
		ORDER BY cr.recipe_id,cr.cookbook_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]string, len(recipeIDs))
	for rows.Next() {
		var recipeID, cookbookID string
		if err := rows.Scan(&recipeID, &cookbookID); err != nil {
			return nil, err
		}
		result[recipeID] = append(result[recipeID], cookbookID)
	}
	return result, rows.Err()
}

func (h apiHandler) creatorProfileRecipes(w http.ResponseWriter, r *http.Request, creatorID string) {
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
	query := `WITH visible_profile AS (
		SELECT creator_id,user_id,public_name,bio,visibility,created_at,updated_at
		FROM creator_profiles WHERE creator_id=? AND (user_id=? OR visibility='instance')
	), readable AS (
		SELECT r.id,r.owner_id,r.creator_name,r.source_recipe_id,r.title,r.description,r.ingredients_json,r.steps_json,
			r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.edit_policy,r.version,
			r.created_at,r.updated_at,s.public_instance,r.image_path,
			(SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id
			 WHERE cr.recipe_id=r.id AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END LIMIT 1) AS member_role,
			a.role,a.provenance
		FROM visible_profile p JOIN content_attributions a ON a.creator_id=p.creator_id AND a.subject_kind='local'
		JOIN recipes r ON r.id=a.recipe_id JOIN instance_settings s ON s.id=1
		WHERE r.deleted_at IS NULL
		AND ` + recipeActorReadPredicate + `
	), page AS (
		SELECT * FROM readable ORDER BY updated_at DESC,id,role LIMIT ? OFFSET ?
	)
	SELECT p.creator_id,p.public_name,p.bio,p.visibility,p.created_at,p.updated_at,
		page.id,page.owner_id,page.creator_name,page.source_recipe_id,page.title,page.description,page.ingredients_json,page.steps_json,
		page.servings,page.prep_time_minutes,page.cook_time_minutes,page.tags_json,page.visibility,page.edit_policy,page.version,
		page.created_at,page.updated_at,page.public_instance,page.image_path,page.member_role,page.role,page.provenance
	FROM visible_profile p LEFT JOIN page ON 1=1 ORDER BY page.updated_at DESC,page.id,page.role`
	rows, err := tx.QueryContext(r.Context(), query, creatorID, uid, uid, uid, uid, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	result := creatorProfileRecipeList{Items: []creatorProfileRecipeItem{}, Limit: limit, Offset: offset}
	items := make([]creatorProfileRecipeItem, 0, limit)
	recipeIDs := make([]string, 0, limit)
	var visible bool
	for rows.Next() {
		visible = true
		var item recipe
		var itemID, ownerID, creatorName, sourceID, title, description, ingredients, steps, tags, visibilityValue, editPolicy, createdAt, updatedAt, imagePath, member, roleValue, provenance sql.NullString
		var public, servings, prepTime, cookTime, version sql.NullInt64
		if err := rows.Scan(&result.Profile.ID, &result.Profile.PublicName, &result.Profile.Bio, &result.Profile.Visibility, &result.Profile.CreatedAt, &result.Profile.UpdatedAt,
			&itemID, &ownerID, &creatorName, &sourceID, &title, &description, &ingredients, &steps, &servings, &prepTime, &cookTime, &tags,
			&visibilityValue, &editPolicy, &version, &createdAt, &updatedAt, &public, &imagePath, &member, &roleValue, &provenance); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if !itemID.Valid {
			continue
		}
		item.ID, item.OwnerID, item.CreatorName, item.SourceRecipeID = itemID.String, ownerID.String, creatorName.String, sourceID.String
		item.Title, item.Description, item.Visibility, item.EditPolicy = title.String, description.String, visibility(visibilityValue.String), editPolicy.String
		item.Servings, item.PrepTime, item.CookTime, item.Version = int(servings.Int64), int(prepTime.Int64), int(cookTime.Int64), int(version.Int64)
		item.CreatedAt, item.UpdatedAt = createdAt.String, updatedAt.String
		if member.Valid {
			item.CanEdit = canEditRecipe(uid, access{ownerID: item.OwnerID, memberRole: role(member.String)}, item.EditPolicy)
			item.CanDelete = uid == item.OwnerID
			item.CanManageSharing = canManageRecipeSharing(uid, item.OwnerID)
		} else {
			item.CanEdit = canEditRecipe(uid, access{ownerID: item.OwnerID}, item.EditPolicy)
			item.CanDelete = uid == item.OwnerID
			item.CanManageSharing = canManageRecipeSharing(uid, item.OwnerID)
		}
		if !hasLocalRecipeRead(uid, item.OwnerID, role(member.String)) {
			item.SourceRecipeID = ""
		}
		if err := json.Unmarshal([]byte(ingredients.String), &item.Ingredients); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := json.Unmarshal([]byte(steps.String), &item.Steps); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := json.Unmarshal([]byte(tags.String), &item.Tags); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if imagePath.Valid && imagePath.String != "" {
			item.Image = "/api/recipes/" + item.ID + "/image"
		}
		items = append(items, creatorProfileRecipeItem{Recipe: projectRecipeForActor(item, uid), Role: roleValue.String, Provenance: provenance.String})
		recipeIDs = append(recipeIDs, item.ID)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if len(recipeIDs) > 0 {
		cookbookIDs, err := recipeCookbookIDsForActor(r.Context(), tx, uid, recipeIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		for index := range items {
			items[index].Recipe.CookbookIDs = cookbookIDs[items[index].Recipe.ID]
			if items[index].Recipe.CookbookIDs == nil {
				items[index].Recipe.CookbookIDs = []string{}
			}
		}
	}
	result.Items = items
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h apiHandler) creatorProfileCookbooks(w http.ResponseWriter, r *http.Request, creatorID string) {
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
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	query := `WITH visible_profile AS (
		SELECT creator_id,user_id,public_name,bio,visibility,created_at,updated_at
		FROM creator_profiles WHERE creator_id=? AND (user_id=? OR visibility='instance')
	), readable AS (
		SELECT c.id,c.title,c.description,c.visibility,c.created_at,c.updated_at,a.role,a.provenance
		FROM content_attributions a JOIN cookbooks c ON c.id=a.cookbook_id JOIN instance_settings s ON s.id=1
		WHERE a.subject_kind='local' AND a.creator_id=? AND ` + cookbookActorReadPredicate + `
	), page AS (
		SELECT id,title,description,visibility,created_at,updated_at,role,provenance FROM readable
		ORDER BY updated_at DESC,id LIMIT ? OFFSET ?
	)
	SELECT p.creator_id,p.public_name,p.bio,p.visibility,p.created_at,p.updated_at,
		page.id,page.title,page.description,page.visibility,page.created_at,page.updated_at,page.role,page.provenance
	FROM visible_profile p LEFT JOIN page ON 1=1 ORDER BY page.id`
	rows, err := h.db.QueryContext(r.Context(), query, creatorID, uid, creatorID, uid, uid, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	result := creatorProfileCookbookList{Items: []creatorProfileCookbookItem{}, Limit: limit, Offset: offset}
	var visible bool
	for rows.Next() {
		visible = true
		var id, title, description, vis, created, updated, roleValue, provenance sql.NullString
		if err := rows.Scan(&result.Profile.ID, &result.Profile.PublicName, &result.Profile.Bio, &result.Profile.Visibility, &result.Profile.CreatedAt, &result.Profile.UpdatedAt,
			&id, &title, &description, &vis, &created, &updated, &roleValue, &provenance); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if !id.Valid {
			continue
		}
		result.Items = append(result.Items, creatorProfileCookbookItem{
			Cookbook: creatorProfileCookbook{ID: id.String, Title: title.String, Description: description.String, Visibility: visibility(vis.String), CreatedAt: created.String, UpdatedAt: updated.String},
			Role:     roleValue.String, Provenance: provenance.String,
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if !visible {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h apiHandler) creatorProfileCourses(w http.ResponseWriter, r *http.Request, creatorID string) {
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
	query := `WITH visible_profile AS (
		SELECT p.creator_id,p.user_id,p.public_name,p.bio,p.visibility,p.created_at,p.updated_at
		FROM creator_profiles p JOIN instance_settings s ON s.id=1 AND s.courses_enabled=1
		WHERE p.creator_id=? AND (p.user_id=? OR p.visibility='instance')
	), readable AS (
		SELECT c.id,c.title,c.updated_at,a.role,a.provenance
		FROM visible_profile p JOIN content_attributions a ON a.creator_id=p.creator_id AND a.subject_kind='local'
		JOIN courses c ON c.id=a.course_id JOIN instance_settings s ON s.id=1
		WHERE c.owner_id=? OR (s.public_instance=1 AND c.visibility='public')
	), page AS (
		SELECT id,title,updated_at,role,provenance FROM readable
		ORDER BY updated_at DESC,id,role LIMIT ? OFFSET ?
	), total AS (SELECT COUNT(*) AS count FROM readable)
	SELECT p.creator_id,p.public_name,p.bio,p.visibility,p.created_at,p.updated_at,total.count,
		page.id,page.title,page.role,page.provenance
	FROM visible_profile p CROSS JOIN total LEFT JOIN page ON 1=1
	ORDER BY page.updated_at DESC,page.id,page.role`
	rows, err := tx.QueryContext(r.Context(), query, creatorID, uid, uid, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	result := creatorProfileCourseList{Items: []creatorProfileCourseItem{}, Limit: limit, Offset: offset}
	var visible bool
	for rows.Next() {
		visible = true
		var id, publicName, bio, profileVisibility, createdAt, updatedAt string
		var total sql.NullInt64
		var courseID, courseTitle, roleValue, provenance sql.NullString
		if err := rows.Scan(&id, &publicName, &bio, &profileVisibility, &createdAt, &updatedAt, &total,
			&courseID, &courseTitle, &roleValue, &provenance); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		result.Profile = creatorProfile{ID: id, PublicName: publicName, Bio: bio, Visibility: profileVisibility, CreatedAt: createdAt, UpdatedAt: updatedAt}
		result.Total = int(total.Int64)
		if !courseID.Valid {
			continue
		}
		result.Items = append(result.Items, creatorProfileCourseItem{
			Course: creatorProfileCourse{ID: courseID.String, Title: courseTitle.String},
			Role:   roleValue.String, Provenance: provenance.String,
		})
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
	if !visible {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
