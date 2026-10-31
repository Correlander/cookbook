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
	books, err := h.db.Query(`SELECT id,title,description,visibility FROM cookbooks WHERE owner_id=? ORDER BY id`, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	bookSet := map[string]bool{}
	for books.Next() {
		var book portableCookbook
		if err := books.Scan(&book.ID, &book.Title, &book.Description, &book.Visibility); err != nil {
			books.Close()
			writeError(w, 500, "storage_error")
			return
		}
		book.RecipeIDs = []string{}
		bundle.Cookbooks = append(bundle.Cookbooks, book)
		bookSet[book.ID] = true
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
		bundle.Recipes = append(bundle.Recipes, recipe)
		recipeSet[recipe.ID] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "storage_error")
		return
	}
	rows.Close()
	if len(bundle.Cookbooks) > 1000 || len(bundle.Recipes) > 10000 {
		writeError(w, 413, "export_too_large")
		return
	}
	links, err := h.db.Query(`SELECT cr.cookbook_id,cr.recipe_id FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id JOIN recipes r ON r.id=cr.recipe_id WHERE c.owner_id=? AND r.owner_id=? AND c.visibility!='unlisted' ORDER BY cr.cookbook_id,cr.recipe_id`, uid, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
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
		for i := range bundle.Cookbooks {
			if bundle.Cookbooks[i].ID == bookID {
				bundle.Cookbooks[i].RecipeIDs = append(bundle.Cookbooks[i].RecipeIDs, recipeID)
				break
			}
		}
		for i := range bundle.Recipes {
			if bundle.Recipes[i].ID == recipeID {
				bundle.Recipes[i].CookbookIDs = append(bundle.Recipes[i].CookbookIDs, bookID)
				break
			}
		}
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
	for _, b := range bundle.Cookbooks {
		if b.ID == "" || books[b.ID] || strings.TrimSpace(b.Title) == "" || len(b.Title) > 200 || len(b.Description) > 5000 || !validVisibility(visibility(b.Visibility)) {
			return errors.New("invalid cookbook")
		}
		books[b.ID] = true
	}
	imageRefs := map[string]bool{}
	for _, r := range bundle.Recipes {
		if r.ImageFile != "" {
			if !validStoredImageName(r.ImageFile) || imageRefs[r.ImageFile] {
				return errors.New("invalid image reference")
			}
			imageRefs[r.ImageFile] = true
		}
		if r.ID == "" || recipes[r.ID] || strings.TrimSpace(r.Title) == "" || len(r.Title) > 200 || len(r.Description) > 5000 || len(r.CreatorName) > 200 || !validVisibility(visibility(r.Visibility)) || r.Servings < 0 || r.Servings > 1000 || r.PrepTime < 0 || r.PrepTime > 1440 || r.CookTime < 0 || r.CookTime > 1440 || len(r.Ingredients) > 500 || len(r.Steps) > 500 || len(r.Tags) > 100 {
			return errors.New("invalid recipe")
		}
		for _, id := range r.CookbookIDs {
			if !books[id] {
				return errors.New("unknown cookbook reference")
			}
		}
		recipes[r.ID] = true
	}
	for _, b := range bundle.Cookbooks {
		for _, id := range b.RecipeIDs {
			if !recipes[id] {
				return errors.New("unknown recipe reference")
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
