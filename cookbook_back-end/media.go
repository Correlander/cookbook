package main

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"
)

const maxRecipeImageBytes = 5 << 20
const maxRecipeImagePixels = 40_000_000

type mediaHandler struct {
	db      *sql.DB
	rootDir string
}

func (h mediaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, recipeID string) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		h.serveImage(w, r, recipeID)
		return
	}
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var owner string
	if err := h.db.QueryRow(`SELECT owner_id FROM recipes WHERE id=? AND deleted_at IS NULL`, recipeID).Scan(&owner); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if uid != owner {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.saveImagePath(recipeID, uid, ""); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if h.rootDir == "" {
		writeError(w, http.StatusServiceUnavailable, "media_unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRecipeImageBytes+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_image")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRecipeImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxRecipeImageBytes {
		writeError(w, http.StatusBadRequest, "invalid_image")
		return
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png" && format != "webp") {
		writeError(w, http.StatusBadRequest, "unsupported_image_type")
		return
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > maxRecipeImagePixels {
		writeError(w, http.StatusBadRequest, "invalid_image_dimensions")
		return
	}
	extension := map[string]string{"jpeg": ".jpg", "png": ".png", "webp": ".webp"}[format]
	if err := os.MkdirAll(h.rootDir, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, "media_unavailable")
		return
	}
	name := newID() + extension
	path := filepath.Join(h.rootDir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		writeError(w, http.StatusInternalServerError, "media_unavailable")
		return
	}
	if err := h.saveImagePath(recipeID, uid, name); err != nil {
		_ = os.Remove(path)
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	var version int
	if err := h.db.QueryRow(`SELECT version FROM recipes WHERE id=?`, recipeID).Scan(&version); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"image": "/api/recipes/" + recipeID + "/image", "version": version})
}

func (h mediaHandler) saveImagePath(recipeID, userID, imagePath string) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	var version int
	var title, description, ingredients, steps, tags string
	var servings, prepTime, cookTime int
	var recipeVisibility visibility
	if err := tx.QueryRow(`SELECT version,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility FROM recipes WHERE id=? AND owner_id=? AND deleted_at IS NULL`, recipeID, userID).Scan(&version, &title, &description, &ingredients, &steps, &servings, &prepTime, &cookTime, &tags, &recipeVisibility); err != nil {
		_ = tx.Rollback()
		return err
	}
	result, err := tx.Exec(`UPDATE recipes SET image_path=?,version=version+1,updated_at=? WHERE id=? AND owner_id=? AND version=? AND deleted_at IS NULL`, imagePath, timestamp(), recipeID, userID, version)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		_ = tx.Rollback()
		return errors.New("recipe changed concurrently")
	}
	if err := insertRecipeRevision(tx, recipeID, version+1, userID, title, description, []byte(ingredients), []byte(steps), servings, prepTime, cookTime, []byte(tags), recipeVisibility, imagePath); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (h mediaHandler) serveImage(w http.ResponseWriter, r *http.Request, recipeID string) {
	var owner, imagePath string
	var recipeVisibility visibility
	var public int
	if err := h.db.QueryRow(`SELECT r.owner_id,r.visibility,r.image_path,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, recipeID).Scan(&owner, &recipeVisibility, &imagePath, &public); err != nil || imagePath == "" {
		http.NotFound(w, r)
		return
	}
	uid := authenticatedUser(r)
	member := recipeMemberRole(h.db, recipeID, uid)
	if !canReadResource(uid, access{instancePublic: public == 1, visibility: recipeVisibility, ownerID: owner, memberRole: member}) {
		http.NotFound(w, r)
		return
	}
	if !hasLocalRecipeRead(uid, owner, member) {
		facts, err := readRecipePublicReadFacts(r.Context(), h.db, recipeID)
		if err != nil || !facts.AnonymousEligible() {
			http.NotFound(w, r)
			return
		}
	}
	cleanName := filepath.Base(imagePath)
	if cleanName != imagePath || strings.Contains(cleanName, "..") || !validStoredImageName(cleanName) {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(filepath.Join(h.rootDir, cleanName))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRecipeImageBytes {
		http.NotFound(w, r)
		return
	}
	contentType := "image/jpeg"
	switch filepath.Ext(cleanName) {
	case ".png":
		contentType = "image/png"
	case ".webp":
		contentType = "image/webp"
	}
	if _, format, err := image.DecodeConfig(file); err != nil || (format != "jpeg" && format != "png" && format != "webp") {
		http.NotFound(w, r)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", "inline; filename=recipe-image")
	http.ServeContent(w, r, "recipe-image", info.ModTime(), file)
}

func validStoredImageName(name string) bool {
	extension := filepath.Ext(name)
	if extension != ".jpg" && extension != ".png" && extension != ".webp" {
		return false
	}
	id := strings.TrimSuffix(name, extension)
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
