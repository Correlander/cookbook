package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

type publicCookbook struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	RecipeCount int    `json:"recipe_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type publicCookbooksHandler struct{ db *sql.DB }

const publicCookbookRowsQuery = `SELECT c.id,c.title,c.description,
	(SELECT COUNT(*) FROM cookbook_recipes cr JOIN recipes r ON r.id=cr.recipe_id JOIN recipe_publication_policy p ON p.recipe_id=r.id JOIN instance_settings s2 ON s2.id=1 WHERE cr.cookbook_id=c.id AND s2.public_instance=1 AND r.deleted_at IS NULL AND r.visibility='public' AND p.publication_state='published'),
	c.created_at,c.updated_at
	FROM cookbooks c JOIN instance_settings s ON s.id=1
	WHERE s.public_instance=1 AND c.visibility='public' AND (?='' OR instr(lower(c.title||' '||c.description),lower(?))>0)
	ORDER BY c.updated_at DESC,c.id LIMIT ? OFFSET ?`

const publicCookbookRecipeRowsQuery = `SELECT r.id,r.owner_id,r.creator_name,r.title,r.description,r.ingredients_json,r.steps_json,r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,r.image_path
	FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id AND c.visibility='public' JOIN recipes r ON r.id=cr.recipe_id JOIN recipe_publication_policy p ON p.recipe_id=r.id JOIN instance_settings s ON s.id=1
	WHERE s.public_instance=1 AND cr.cookbook_id=? AND r.deleted_at IS NULL AND r.visibility='public' AND p.publication_state='published'
	AND (?='' OR instr(lower(r.title||' '||r.description||' '||r.tags_json),lower(?))>0)
	AND (?='' OR instr(lower(r.tags_json),lower(?))>0)
	ORDER BY `

func (h publicCookbooksHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	rows, err := h.db.Query(publicCookbookRowsQuery, query, query, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []publicCookbook{}
	for rows.Next() {
		var item publicCookbook
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.RecipeCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
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

type publicCookbookRecipesHandler struct{ db *sql.DB }

func (h publicCookbookRecipesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	cookbookID := r.PathValue("id")
	var public int
	if err := h.db.QueryRow(`SELECT s.public_instance FROM cookbooks c JOIN instance_settings s ON s.id=1 WHERE c.id=? AND c.visibility='public'`, cookbookID).Scan(&public); err != nil || public != 1 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	order, tagJSON, err := recipeListOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_recipe_filter")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	rows, err := h.db.Query(publicCookbookRecipeRowsQuery+order+` LIMIT ? OFFSET ?`, cookbookID, query, query, tagJSON, tagJSON, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipe{}
	for rows.Next() {
		item := recipe{}
		var ingredients, steps, tags, imagePath string
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.CreatorName, &item.Title, &item.Description, &ingredients, &steps, &item.Servings, &item.PrepTime, &item.CookTime, &tags, &item.Visibility, &item.EditPolicy, &item.Version, &item.CreatedAt, &item.UpdatedAt, &imagePath); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &item.Ingredients) != nil || json.Unmarshal([]byte(steps), &item.Steps) != nil || json.Unmarshal([]byte(tags), &item.Tags) != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		item.OwnerID = ""
		item.CookbookIDs = []string{}
		item.EditPolicy = ""
		if imagePath != "" {
			item.Image = "/api/recipes/" + item.ID + "/image"
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
