package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode"
)

const maxPublicRecipeDetailIDBytes = 200

type publicRecipeDetailHandler struct {
	db *sql.DB
}

type publicRecipeDetail struct {
	ID          string       `json:"id"`
	CreatorName string       `json:"creator_name"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Servings    int          `json:"servings"`
	PrepTime    int          `json:"prep_time_minutes"`
	CookTime    int          `json:"cook_time_minutes"`
	Tags        []string     `json:"tags"`
	Version     int          `json:"version"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	Image       string       `json:"image,omitempty"`
}

func (h publicRecipeDetailHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writePublicRecipeDetailError(w, r, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	recipeID := r.PathValue("id")
	if !validPublicRecipeDetailID(recipeID) {
		writePublicRecipeDetailError(w, r, http.StatusNotFound, "not_found")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writePublicRecipeDetailError(w, r, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()

	// Empty account identity deliberately makes the shared query enforce the
	// anonymous public policy, even when the request carries a valid cookie.
	item, err := readRecipeDetail(r.Context(), tx, ContentActor{}, recipeID)
	if errors.Is(err, errRecipeDetailNotFound) || errors.Is(err, errRecipeDetailDenied) {
		writePublicRecipeDetailError(w, r, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writePublicRecipeDetailError(w, r, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writePublicRecipeDetailError(w, r, http.StatusInternalServerError, "storage_error")
		return
	}
	view := publicRecipeDetail{
		ID: item.ID, CreatorName: item.CreatorName, Title: item.Title,
		Description: item.Description, Ingredients: item.Ingredients, Steps: item.Steps,
		Servings: item.Servings, PrepTime: item.PrepTime, CookTime: item.CookTime,
		Tags: item.Tags, Version: item.Version, CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt, Image: item.Image,
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func validPublicRecipeDetailID(value string) bool {
	if value == "" || len(value) > maxPublicRecipeDetailIDBytes || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func writePublicRecipeDetailError(w http.ResponseWriter, r *http.Request, status int, code string) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		return
	}
	writeError(w, status, code)
}
