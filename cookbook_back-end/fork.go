package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (h apiHandler) forkRecipe(w http.ResponseWriter, r *http.Request, recipeID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	userID := authenticatedUser(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var source recipe
	var ingredients, steps, tags, imagePath string
	var public int
	err := h.db.QueryRow(`SELECT r.id,r.owner_id,r.creator_name,r.title,r.description,r.ingredients_json,r.steps_json,r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.version,r.created_at,r.updated_at,r.image_path,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, recipeID).Scan(&source.ID, &source.OwnerID, &source.CreatorName, &source.Title, &source.Description, &ingredients, &steps, &source.Servings, &source.PrepTime, &source.CookTime, &tags, &source.Visibility, &source.Version, &source.CreatedAt, &source.UpdatedAt, &imagePath, &public)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	member := recipeMemberRole(h.db, recipeID, userID)
	if !canReadResource(userID, access{instancePublic: public == 1, visibility: source.Visibility, ownerID: source.OwnerID, memberRole: member}) {
		writeError(w, 404, "not_found")
		return
	}
	if json.Unmarshal([]byte(ingredients), &source.Ingredients) != nil || json.Unmarshal([]byte(steps), &source.Steps) != nil || json.Unmarshal([]byte(tags), &source.Tags) != nil {
		writeError(w, 500, "storage_error")
		return
	}
	var input struct {
		CookbookIDs []string `json:"cookbook_ids"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &input) {
		return
	}
	if len(input.CookbookIDs) > 50 {
		writeError(w, 400, "invalid_recipe")
		return
	}
	for _, bookID := range input.CookbookIDs {
		var owner string
		if err := h.db.QueryRow(`SELECT owner_id FROM cookbooks WHERE id=?`, bookID).Scan(&owner); err != nil || owner != userID {
			writeError(w, 403, "forbidden")
			return
		}
	}
	id, now := newID(), timestamp()
	sourceID := source.ID
	source.ID = id
	source.OwnerID = userID
	source.SourceRecipeID = sourceID
	source.Title = strings.TrimSpace(source.Title) + " (copy)"
	if len(source.Title) > 200 {
		source.Title = source.Title[:200]
	}
	source.Visibility = visibilityPrivate
	source.EditPolicy = "owner_only"
	source.Version = 1
	source.CreatedAt = now
	source.UpdatedAt = now
	source.DeletedAt = ""
	source.CanEdit = true
	source.CanDelete = true
	source.CookbookIDs = input.CookbookIDs
	source.Image = ""
	ingredientJSON, _ := json.Marshal(source.Ingredients)
	stepJSON, _ := json.Marshal(source.Steps)
	tagJSON, _ := json.Marshal(source.Tags)
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO recipes(id,owner_id,creator_name,source_recipe_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, userID, source.CreatorName, sourceID, source.Title, source.Description, string(ingredientJSON), string(stepJSON), source.Servings, source.PrepTime, source.CookTime, string(tagJSON), visibilityPrivate, "owner_only", now, now); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if err := insertDefaultRecipePublicationPolicy(r.Context(), tx, id); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if err := insertRecipeRevision(tx, id, 1, userID, source.Title, source.Description, ingredientJSON, stepJSON, source.Servings, source.PrepTime, source.CookTime, tagJSON, visibilityPrivate, ""); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	profile, err := ensureCreatorProfile(r.Context(), tx, userID)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if err := insertLocalAttribution(r.Context(), tx, "recipe", id, profile.ID, "adapter", "adapter_source"); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	for _, bookID := range input.CookbookIDs {
		if _, err := tx.Exec(`INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES(?,?,?,?)`, bookID, id, userID, now); err != nil {
			writeError(w, 400, "invalid_cookbook")
			return
		}
	}
	created, err := readRecipeDetail(r.Context(), tx, ContentActor{AccountID: userID}, id)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	recordAuditBestEffort(h.db, userID, "fork", "recipe", sourceID, http.StatusCreated)
	writeJSON(w, http.StatusCreated, created)
}
