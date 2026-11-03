package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxImportBytes = 100 << 20
const maxImportManifestBytes = 40 << 20
const maxPortableMediaCount = 50
const maxPortableMediaBytes = 50 << 20
const maxPortableRelationships = 100_000
const portableFormat = "cookbook-export-v2"
const legacyPortableFormat = "cookbook-export-v1"

type portableBundle struct {
	Format    string             `json:"format"`
	Exported  string             `json:"exported_at"`
	Cookbooks []portableCookbook `json:"cookbooks"`
	Recipes   []portableRecipe   `json:"recipes"`
}

type portableCookbook struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Visibility  string   `json:"visibility"`
	RecipeIDs   []string `json:"recipe_ids"`
}

type portableRecipe struct {
	ID             string       `json:"id"`
	CreatorName    string       `json:"creator_name"`
	SourceRecipeID string       `json:"source_recipe_id,omitempty"`
	Title          string       `json:"title"`
	Description    string       `json:"description"`
	Ingredients    []ingredient `json:"ingredients"`
	Steps          []recipeStep `json:"steps"`
	Servings       int          `json:"servings"`
	PrepTime       int          `json:"prep_time_minutes"`
	CookTime       int          `json:"cook_time_minutes"`
	Tags           []string     `json:"tags"`
	Visibility     string       `json:"visibility"`
	EditPolicy     string       `json:"edit_policy"`
	CookbookIDs    []string     `json:"cookbook_ids"`
	CreatedAt      string       `json:"created_at"`
	UpdatedAt      string       `json:"updated_at"`
	ImageFile      string       `json:"image_file,omitempty"`
}

func (h apiHandler) exportData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	uid := authenticatedUser(r)
	bundle := portableBundle{Format: portableFormat, Exported: time.Now().UTC().Format(time.RFC3339Nano), Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}}
	manifestSize := 128
	books, err := h.db.Query(`SELECT id,title,description,visibility FROM cookbooks WHERE owner_id=? ORDER BY id`, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	bookSet := map[string]bool{}
	bookIndex := map[string]int{}
	for books.Next() {
		var book portableCookbook
		if err := books.Scan(&book.ID, &book.Title, &book.Description, &book.Visibility); err != nil {
			books.Close()
			writeError(w, 500, "storage_error")
			return
		}
		book.RecipeIDs = []string{}
		if len(bundle.Cookbooks) == 1000 {
			books.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		encodedBook, err := json.Marshal(book)
		if err != nil || manifestSize+len(encodedBook)+1 > maxImportManifestBytes {
			books.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		manifestSize += len(encodedBook) + 1
		bundle.Cookbooks = append(bundle.Cookbooks, book)
		bookSet[book.ID] = true
		bookIndex[book.ID] = len(bundle.Cookbooks) - 1
	}
	if err := books.Err(); err != nil {
		books.Close()
		writeError(w, 500, "storage_error")
		return
	}
	books.Close()
	rows, err := h.db.Query(`SELECT id,creator_name,source_recipe_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,created_at,updated_at,image_path FROM recipes WHERE owner_id=? AND deleted_at IS NULL ORDER BY id`, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	recipeSet := map[string]bool{}
	recipeIndex := map[string]int{}
	for rows.Next() {
		var recipe portableRecipe
		var ingredients, steps, tags, imagePath string
		if err := rows.Scan(&recipe.ID, &recipe.CreatorName, &recipe.SourceRecipeID, &recipe.Title, &recipe.Description, &ingredients, &steps, &recipe.Servings, &recipe.PrepTime, &recipe.CookTime, &tags, &recipe.Visibility, &recipe.EditPolicy, &recipe.CreatedAt, &recipe.UpdatedAt, &imagePath); err != nil {
			rows.Close()
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &recipe.Ingredients) != nil || json.Unmarshal([]byte(steps), &recipe.Steps) != nil || json.Unmarshal([]byte(tags), &recipe.Tags) != nil {
			rows.Close()
			writeError(w, 500, "storage_error")
			return
		}
		if recipe.Ingredients == nil {
			recipe.Ingredients = []ingredient{}
		}
		if recipe.Steps == nil {
			recipe.Steps = []recipeStep{}
		}
		if recipe.Tags == nil {
			recipe.Tags = []string{}
		}
		recipe.CookbookIDs = []string{}
		if imagePath != "" {
			if !validStoredImageName(imagePath) {
				rows.Close()
				writeError(w, 500, "storage_error")
				return
			}
			recipe.ImageFile = imagePath
		}
		if len(bundle.Recipes) == 10000 {
			rows.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		encodedRecipe, err := json.Marshal(recipe)
		if err != nil || manifestSize+len(encodedRecipe)+1 > maxImportManifestBytes {
			rows.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		manifestSize += len(encodedRecipe) + 1
		bundle.Recipes = append(bundle.Recipes, recipe)
		recipeSet[recipe.ID] = true
		recipeIndex[recipe.ID] = len(bundle.Recipes) - 1
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "storage_error")
		return
	}
	rows.Close()
	links, err := h.db.Query(`SELECT cr.cookbook_id,cr.recipe_id FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id JOIN recipes r ON r.id=cr.recipe_id WHERE c.owner_id=? AND r.owner_id=? ORDER BY cr.cookbook_id,cr.recipe_id`, uid, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	linkCount := 0
	for links.Next() {
		var bookID, recipeID string
		if err := links.Scan(&bookID, &recipeID); err != nil {
			links.Close()
			writeError(w, 500, "storage_error")
			return
		}
		if !bookSet[bookID] || !recipeSet[recipeID] {
			continue
		}
		linkCount++
		if linkCount > maxPortableRelationships {
			links.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		bookPosition := bookIndex[bookID]
		recipePosition := recipeIndex[recipeID]
		encodedBookID, err := json.Marshal(bookID)
		if err != nil {
			links.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		encodedRecipeID, err := json.Marshal(recipeID)
		if err != nil {
			links.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		relationshipSize := len(encodedRecipeID) + len(encodedBookID)
		if len(bundle.Cookbooks[bookPosition].RecipeIDs) > 0 {
			relationshipSize++
		}
		if len(bundle.Recipes[recipePosition].CookbookIDs) > 0 {
			relationshipSize++
		}
		if manifestSize+relationshipSize > maxImportManifestBytes {
			links.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		manifestSize += relationshipSize
		bundle.Cookbooks[bookPosition].RecipeIDs = append(bundle.Cookbooks[bookPosition].RecipeIDs, recipeID)
		bundle.Recipes[recipePosition].CookbookIDs = append(bundle.Recipes[recipePosition].CookbookIDs, bookID)
	}
	if err := links.Err(); err != nil {
		links.Close()
		writeError(w, 500, "storage_error")
		return
	}
	links.Close()
	mediaNames := map[string]bool{}
	for _, recipe := range bundle.Recipes {
		if recipe.ImageFile != "" {
			mediaNames[recipe.ImageFile] = true
		}
	}
	if len(mediaNames) > maxPortableMediaCount {
		writeError(w, 413, "export_too_large")
		return
	}
	totalMediaBytes := int64(0)
	for name := range mediaNames {
		if h.mediaDir == "" {
			writeError(w, 503, "media_unavailable")
			return
		}
		info, err := os.Stat(filepath.Join(h.mediaDir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxRecipeImageBytes {
			writeError(w, 500, "storage_error")
			return
		}
		totalMediaBytes += info.Size()
		if totalMediaBytes > maxPortableMediaBytes {
			writeError(w, 413, "export_too_large")
			return
		}
	}
	manifestBytes, err := json.Marshal(bundle)
	if err != nil || len(manifestBytes) > maxImportManifestBytes {
		writeError(w, 413, "export_too_large")
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=cookbook-export.zip")
	w.Header().Set("Content-Type", "application/zip")
	archive := zip.NewWriter(w)
	manifest, err := archive.Create("manifest.json")
	if err != nil {
		return
	}
	if _, err := manifest.Write(manifestBytes); err != nil {
		return
	}
	for name := range mediaNames {
		entry, err := archive.Create("media/" + name)
		if err != nil {
			return
		}
		file, err := os.Open(filepath.Join(h.mediaDir, name))
		if err != nil {
			return
		}
		_, copyErr := io.Copy(entry, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return
		}
	}
	_ = archive.Close()
}

func (h apiHandler) importData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import")
		return
	}
	bundle, media, err := decodePortableArchive(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import")
		return
	}
	if err := validatePortableBundle(bundle); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import")
		return
	}
	for _, recipe := range bundle.Recipes {
		if recipe.ImageFile != "" {
			if _, ok := media[recipe.ImageFile]; !ok {
				writeError(w, 400, "invalid_import")
				return
			}
		}
	}
	if len(media) > maxPortableMediaCount {
		writeError(w, 400, "invalid_import")
		return
	}
	if len(media) > 0 && h.mediaDir == "" {
		writeError(w, 503, "media_unavailable")
		return
	}
	storedMedia := map[string]string{}
	createdFiles := []string{}
	committed := false
	defer func() {
		if !committed {
			for _, path := range createdFiles {
				_ = os.Remove(path)
			}
		}
	}()
	if len(media) > 0 {
		if err := os.MkdirAll(h.mediaDir, 0700); err != nil {
			writeError(w, 500, "media_unavailable")
			return
		}
	}
	for original, content := range media {
		ext := filepath.Ext(original)
		name := newID() + ext
		path := filepath.Join(h.mediaDir, name)
		if err := os.WriteFile(path, content, 0600); err != nil {
			writeError(w, 500, "media_unavailable")
			return
		}
		createdFiles = append(createdFiles, path)
		storedMedia[original] = name
	}
	uid := authenticatedUser(r)
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer tx.Rollback()
	bookIDs := map[string]string{}
	for _, book := range bundle.Cookbooks {
		id := newID()
		bookIDs[book.ID] = id
		if _, err := tx.Exec(`INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, uid, book.Title, book.Description, "private", timestamp(), timestamp()); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
	}
	recipeIDs := map[string]string{}
	for _, recipe := range bundle.Recipes {
		id := newID()
		recipeIDs[recipe.ID] = id
		imagePath := storedMedia[recipe.ImageFile]
		ingredients, _ := json.Marshal(recipe.Ingredients)
		steps, _ := json.Marshal(recipe.Steps)
		tags, _ := json.Marshal(recipe.Tags)
		creator := strings.TrimSpace(recipe.CreatorName)
		if creator == "" {
			creator = "Unknown creator"
		}
		if _, err := tx.Exec(`INSERT INTO recipes(id,owner_id,creator_name,source_recipe_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,image_path,visibility,edit_policy,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, uid, creator, recipe.SourceRecipeID, recipe.Title, recipe.Description, string(ingredients), string(steps), recipe.Servings, recipe.PrepTime, recipe.CookTime, string(tags), imagePath, "private", "owner_only", timestamp(), timestamp()); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if err := insertDefaultRecipePublicationPolicy(r.Context(), tx, id); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if err := insertRecipeRevision(tx, id, 1, uid, recipe.Title, recipe.Description, ingredients, steps, recipe.Servings, recipe.PrepTime, recipe.CookTime, tags, "private", imagePath); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
	}
	for _, book := range bundle.Cookbooks {
		for _, oldRecipeID := range book.RecipeIDs {
			rid := recipeIDs[oldRecipeID]
			if rid == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES(?,?,?,?)`, bookIDs[book.ID], rid, uid, timestamp()); err != nil {
				writeError(w, 500, "storage_error")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	committed = true
	writeJSON(w, http.StatusCreated, map[string]int{"cookbooks": len(bundle.Cookbooks), "recipes": len(bundle.Recipes)})
}

func validatePortableBundle(bundle portableBundle) error {
	if (bundle.Format != portableFormat && bundle.Format != legacyPortableFormat) || len(bundle.Cookbooks) > 1000 || len(bundle.Recipes) > 10000 {
		return errors.New("unsupported or oversized import")
	}
	books, recipes := map[string]bool{}, map[string]bool{}
	for i := range bundle.Cookbooks {
		book := &bundle.Cookbooks[i]
		book.Title = strings.TrimSpace(book.Title)
		book.Description = strings.TrimSpace(book.Description)
		if book.ID == "" || len(book.ID) > 80 || books[book.ID] || book.Title == "" || len(book.Title) > 200 || len(book.Description) > 5000 || !validVisibility(visibility(book.Visibility)) {
			return errors.New("invalid cookbook")
		}
		books[book.ID] = true
	}
	imageRefs := map[string]bool{}
	recipeRelationshipCount := 0
	for i := range bundle.Recipes {
		r := &bundle.Recipes[i]
		if r.ImageFile != "" {
			if !validStoredImageName(r.ImageFile) || imageRefs[r.ImageFile] {
				return errors.New("invalid image reference")
			}
			imageRefs[r.ImageFile] = true
		}
		if r.ID == "" || len(r.ID) > 80 || recipes[r.ID] || len(r.CreatorName) > 200 || len(r.SourceRecipeID) > 80 {
			return errors.New("invalid recipe")
		}
		input := recipeInput{
			Title: r.Title, Description: r.Description, Ingredients: r.Ingredients,
			Steps: r.Steps, Servings: r.Servings, PrepTime: r.PrepTime,
			CookTime: r.CookTime, Tags: r.Tags, Visibility: visibility(r.Visibility),
			EditPolicy: r.EditPolicy,
		}
		if err := input.validate(); err != nil {
			return errors.New("invalid recipe")
		}
		r.Title = input.Title
		r.Description = input.Description
		r.Ingredients = input.Ingredients
		r.Steps = input.Steps
		r.Servings = input.Servings
		r.PrepTime = input.PrepTime
		r.CookTime = input.CookTime
		r.Tags = input.Tags
		r.Visibility = string(input.Visibility)
		r.EditPolicy = input.EditPolicy
		if len(r.CookbookIDs) > maxPortableRelationships-recipeRelationshipCount {
			return errors.New("too many recipe cookbook references")
		}
		recipeRelationshipCount += len(r.CookbookIDs)
		seenBooks := make(map[string]struct{}, len(r.CookbookIDs))
		for _, id := range r.CookbookIDs {
			if len(id) > 80 || !books[id] {
				return errors.New("unknown cookbook reference")
			}
			if _, exists := seenBooks[id]; exists {
				return errors.New("duplicate cookbook reference")
			}
			seenBooks[id] = struct{}{}
		}
		recipes[r.ID] = true
	}
	relationshipCount := 0
	recipeIDsByBook := make(map[string]map[string]struct{}, len(bundle.Cookbooks))
	for _, b := range bundle.Cookbooks {
		if len(b.RecipeIDs) > maxPortableRelationships-relationshipCount {
			return errors.New("too many cookbook recipe relationships")
		}
		seenRecipes := make(map[string]struct{}, len(b.RecipeIDs))
		for _, id := range b.RecipeIDs {
			if len(id) > 80 || !recipes[id] {
				return errors.New("unknown recipe reference")
			}
			if _, exists := seenRecipes[id]; exists {
				return errors.New("duplicate recipe reference")
			}
			seenRecipes[id] = struct{}{}
			relationshipCount++
		}
		recipeIDsByBook[b.ID] = seenRecipes
	}
	if bundle.Format == portableFormat {
		if relationshipCount != recipeRelationshipCount {
			return errors.New("inconsistent cookbook relationships")
		}
		for _, recipe := range bundle.Recipes {
			for _, bookID := range recipe.CookbookIDs {
				if _, exists := recipeIDsByBook[bookID][recipe.ID]; !exists {
					return errors.New("inconsistent cookbook relationships")
				}
			}
		}
	}
	return nil
}

func decodePortableArchive(data []byte) (portableBundle, map[string][]byte, error) {
	media := map[string][]byte{}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if errors.Is(err, zip.ErrFormat) {
		var bundle portableBundle
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&bundle); err != nil {
			return portableBundle{}, nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return portableBundle{}, nil, errors.New("trailing JSON data")
		}
		if bundle.Format != legacyPortableFormat {
			return portableBundle{}, nil, errors.New("expected a ZIP archive")
		}
		return bundle, media, nil
	}
	if err != nil {
		return portableBundle{}, nil, err
	}
	if len(archive.File) == 0 || len(archive.File) > maxPortableMediaCount+1 {
		return portableBundle{}, nil, errors.New("invalid archive entry count")
	}
	var manifest []byte
	totalMediaBytes := uint64(0)
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			return portableBundle{}, nil, errors.New("directories are not allowed")
		}
		limit := uint64(maxRecipeImageBytes)
		if entry.Name == "manifest.json" {
			if manifest != nil {
				return portableBundle{}, nil, errors.New("duplicate manifest")
			}
			limit = maxImportManifestBytes
		} else {
			if !strings.HasPrefix(entry.Name, "media/") {
				return portableBundle{}, nil, errors.New("unexpected archive path")
			}
			name := strings.TrimPrefix(entry.Name, "media/")
			if !validStoredImageName(name) || strings.Contains(name, "/") {
				return portableBundle{}, nil, errors.New("invalid media path")
			}
			if _, exists := media[name]; exists {
				return portableBundle{}, nil, errors.New("duplicate media file")
			}
		}
		if entry.UncompressedSize64 > limit {
			return portableBundle{}, nil, errors.New("archive entry too large")
		}
		reader, err := entry.Open()
		if err != nil {
			return portableBundle{}, nil, err
		}
		content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil || uint64(len(content)) != entry.UncompressedSize64 || uint64(len(content)) > limit {
			return portableBundle{}, nil, errors.New("invalid archive content")
		}
		if entry.Name == "manifest.json" {
			manifest = content
			continue
		}
		name := strings.TrimPrefix(entry.Name, "media/")
		config, format, err := image.DecodeConfig(bytes.NewReader(content))
		if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > maxRecipeImagePixels {
			return portableBundle{}, nil, errors.New("invalid media image")
		}
		expected := map[string]string{".jpg": "jpeg", ".png": "png", ".webp": "webp"}[filepath.Ext(name)]
		if format != expected {
			return portableBundle{}, nil, errors.New("media extension does not match image type")
		}
		totalMediaBytes += uint64(len(content))
		if totalMediaBytes > maxPortableMediaBytes {
			return portableBundle{}, nil, errors.New("archive media is too large")
		}
		media[name] = content
	}
	if manifest == nil {
		return portableBundle{}, nil, errors.New("manifest is missing")
	}
	var bundle portableBundle
	decoder := json.NewDecoder(bytes.NewReader(manifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return portableBundle{}, nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return portableBundle{}, nil, errors.New("trailing manifest data")
	}
	if bundle.Format != portableFormat {
		return portableBundle{}, nil, errors.New("unsupported archive version")
	}
	refs := map[string]bool{}
	for _, recipe := range bundle.Recipes {
		if recipe.ImageFile != "" {
			refs[recipe.ImageFile] = true
		}
	}
	if len(refs) != len(media) {
		return portableBundle{}, nil, errors.New("media reference mismatch")
	}
	for name := range media {
		if !refs[name] {
			return portableBundle{}, nil, errors.New("unreferenced media")
		}
	}
	return bundle, media, nil
}
