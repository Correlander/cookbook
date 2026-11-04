package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const maxImportBytes = 100 << 20
const maxImportManifestBytes = 40 << 20
const maxPortableMediaCount = 50
const maxPortableMediaBytes = 50 << 20
const maxPortableRelationships = 100_000
const maxPortableCookbookBookmarks = 10000
const portableFormatV5 = "cookbook-export-v5"
const portableFormatV4 = "cookbook-export-v4"
const portableFormatV3 = "cookbook-export-v3"
const portableFormat = "cookbook-export-v2"
const legacyPortableFormat = "cookbook-export-v1"

type portableBundle struct {
	Format            string                `json:"format"`
	Exported          string                `json:"exported_at"`
	Cookbooks         []portableCookbook    `json:"cookbooks"`
	Recipes           []portableRecipe      `json:"recipes"`
	Bookmarks         []portableBookmark    `json:"bookmarks"`
	Attributions      []portableAttribution `json:"attributions"`
	CookbookBookmarks json.RawMessage       `json:"cookbook_bookmarks,omitempty"`
}

// portableBookmark contains only the saved source identity and creation time.
// It never carries source content, media, authorization, or cached projections.
type portableBookmark struct {
	TargetKind      string `json:"target_kind"`
	LocalInstanceID string `json:"local_instance_id,omitempty"`
	LocalRecipeID   string `json:"local_recipe_id,omitempty"`
	RemoteObjectURI string `json:"remote_object_uri,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type portableBookmarkIdentity struct {
	kind, instanceID, recipeID, objectURI string
}

type portableCookbookBookmark struct {
	LocalInstanceID string `json:"local_instance_id"`
	LocalCookbookID string `json:"local_cookbook_id"`
	CreatedAt       string `json:"created_at"`
}

type portableCookbookBookmarkIdentity struct {
	instanceID, cookbookID string
}

var errPortableBookmarkLimitReached = errors.New("portable bookmark limit reached")
var errPortableCookbookBookmarkLimitReached = errors.New("portable cookbook bookmark limit reached")
var errPortableExportTooLarge = errors.New("portable export too large")

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
	bundle := portableBundle{Format: portableFormatV5, Exported: time.Now().UTC().Format(time.RFC3339Nano), Cookbooks: []portableCookbook{}, Recipes: []portableRecipe{}, Bookmarks: []portableBookmark{}, Attributions: []portableAttribution{}, CookbookBookmarks: json.RawMessage("[]")}
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
	if err := appendPortableAttributions(r.Context(), h.db, uid, &bundle, bookSet, recipeSet, &manifestSize); err != nil {
		if errors.Is(err, errPortableExportTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	bookmarkRows, err := h.db.Query(`SELECT target_kind,local_instance_id,local_recipe_id,remote_object_uri,created_at
		FROM recipe_bookmarks WHERE user_id=? ORDER BY created_at DESC,id`, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	for bookmarkRows.Next() {
		var bookmark portableBookmark
		var localInstanceID, localRecipeID, remoteObjectURI sql.NullString
		if err := bookmarkRows.Scan(&bookmark.TargetKind, &localInstanceID, &localRecipeID, &remoteObjectURI, &bookmark.CreatedAt); err != nil {
			bookmarkRows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if localInstanceID.Valid {
			bookmark.LocalInstanceID = localInstanceID.String
		}
		if localRecipeID.Valid {
			bookmark.LocalRecipeID = localRecipeID.String
		}
		if remoteObjectURI.Valid {
			bookmark.RemoteObjectURI = remoteObjectURI.String
		}
		if len(bundle.Bookmarks) >= maxRecipeBookmarksPerAccount {
			bookmarkRows.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		if err := validatePortableBookmark(bookmark); err != nil {
			bookmarkRows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		encodedBookmark, err := json.Marshal(bookmark)
		if err != nil || manifestSize+len(encodedBookmark)+1 > maxImportManifestBytes {
			bookmarkRows.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		manifestSize += len(encodedBookmark) + 1
		bundle.Bookmarks = append(bundle.Bookmarks, bookmark)
	}
	if err := bookmarkRows.Err(); err != nil {
		bookmarkRows.Close()
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := bookmarkRows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	cookbookBookmarkRows, err := h.db.Query(`SELECT local_instance_id,local_cookbook_id,created_at
		FROM cookbook_bookmarks WHERE user_id=? ORDER BY created_at DESC,id`, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	cookbookBookmarks := make([]portableCookbookBookmark, 0)
	for cookbookBookmarkRows.Next() {
		var bookmark portableCookbookBookmark
		if err := cookbookBookmarkRows.Scan(&bookmark.LocalInstanceID, &bookmark.LocalCookbookID, &bookmark.CreatedAt); err != nil {
			cookbookBookmarkRows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if len(cookbookBookmarks) >= maxPortableCookbookBookmarks {
			cookbookBookmarkRows.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		if validatePortableCookbookBookmark(bookmark) != nil {
			cookbookBookmarkRows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		encodedBookmark, err := json.Marshal(bookmark)
		if err != nil || manifestSize+len(encodedBookmark)+1 > maxImportManifestBytes {
			cookbookBookmarkRows.Close()
			writeError(w, http.StatusRequestEntityTooLarge, "export_too_large")
			return
		}
		manifestSize += len(encodedBookmark) + 1
		cookbookBookmarks = append(cookbookBookmarks, bookmark)
	}
	if err := cookbookBookmarkRows.Err(); err != nil {
		cookbookBookmarkRows.Close()
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := cookbookBookmarkRows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	bundle.CookbookBookmarks, err = json.Marshal(cookbookBookmarks)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
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

func appendPortableAttributions(ctx context.Context, db *sql.DB, userID string, bundle *portableBundle, books, recipes map[string]bool, manifestSize *int) error {
	instanceID, err := currentLibraryInstanceID(ctx, db)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT CASE WHEN a.recipe_id IS NOT NULL THEN 'recipe' ELSE 'cookbook' END,
		COALESCE(a.recipe_id,a.cookbook_id),a.subject_kind,a.creator_id,a.claim_source_instance_id,a.claim_creator_id,a.claim_name,a.role,a.provenance,
		p.user_id,p.visibility,p.public_name
		FROM content_attributions a
		LEFT JOIN recipes r ON r.id=a.recipe_id
		LEFT JOIN cookbooks c ON c.id=a.cookbook_id
		LEFT JOIN creator_profiles p ON p.creator_id=a.creator_id
		WHERE ((r.owner_id=? AND r.deleted_at IS NULL) OR c.owner_id=?)
		AND (a.subject_kind='external_claim' OR (a.subject_kind='local' AND (p.user_id=? OR p.visibility='instance')))
		ORDER BY CASE WHEN a.recipe_id IS NOT NULL THEN 0 ELSE 1 END,COALESCE(a.recipe_id,a.cookbook_id),a.id`, userID, userID, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var targetKind, targetID, subjectKind, roleValue, provenance string
		var creatorID, sourceInstanceID, claimCreatorID, claimName, profileUserID, profileVisibility, profileName sql.NullString
		if err := rows.Scan(&targetKind, &targetID, &subjectKind, &creatorID, &sourceInstanceID, &claimCreatorID, &claimName, &roleValue, &provenance, &profileUserID, &profileVisibility, &profileName); err != nil {
			return err
		}
		if targetKind == "recipe" && !recipes[targetID] || targetKind == "cookbook" && !books[targetID] {
			continue
		}
		item := portableAttribution{TargetKind: targetKind, TargetID: targetID, Role: roleValue, Provenance: provenance}
		if subjectKind == "local" {
			// The SQL visibility predicate prevents private foreign identity fields
			// from entering the export at all. The owner may self-export privately.
			if !profileUserID.Valid || (profileUserID.String != userID && profileVisibility.String != "instance") {
				continue
			}
			item.SourceInstanceID = instanceID
			item.CreatorID = creatorID.String
			if profileName.Valid {
				item.NameClaim = profileName.String
			}
		} else if subjectKind == "external_claim" {
			item.SourceInstanceID = sourceInstanceID.String
			item.CreatorID = claimCreatorID.String
			if claimName.Valid {
				item.NameClaim = claimName.String
			}
		} else {
			return errors.New("invalid stored attribution subject")
		}
		if len(bundle.Attributions) >= maxPortableRelationships {
			return errPortableExportTooLarge
		}
		encoded, err := json.Marshal(item)
		if err != nil || *manifestSize+len(encoded)+1 > maxImportManifestBytes {
			return errPortableExportTooLarge
		}
		*manifestSize += len(encoded) + 1
		bundle.Attributions = append(bundle.Attributions, item)
	}
	return rows.Err()
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
	cookbookBookmarkItems, err := portableCookbookBookmarksFromBundle(bundle)
	if err != nil {
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
	bookmarkPlan, err := planPortableBookmarkImport(r.Context(), tx, uid, bundle.Bookmarks)
	if errors.Is(err, errPortableBookmarkLimitReached) {
		writeError(w, http.StatusConflict, "bookmark_limit_reached")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	cookbookBookmarkPlan, err := planPortableCookbookBookmarkImport(r.Context(), tx, uid, cookbookBookmarkItems)
	if errors.Is(err, errPortableCookbookBookmarkLimitReached) {
		writeError(w, http.StatusConflict, "cookbook_bookmark_limit_reached")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
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
	if err := insertPortableBookmarkImport(r.Context(), tx, uid, bookmarkPlan); err != nil {
		if errors.Is(err, errPortableBookmarkLimitReached) {
			writeError(w, http.StatusConflict, "bookmark_limit_reached")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := insertPortableCookbookBookmarkImport(r.Context(), tx, uid, cookbookBookmarkPlan); err != nil {
		if errors.Is(err, errPortableCookbookBookmarkLimitReached) {
			writeError(w, http.StatusConflict, "cookbook_bookmark_limit_reached")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := insertPortableAttributionImport(r.Context(), tx, bundle.Attributions, recipeIDs, bookIDs); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	committed = true
	writeJSON(w, http.StatusCreated, map[string]int{"cookbooks": len(bundle.Cookbooks), "recipes": len(bundle.Recipes)})
}

func insertPortableAttributionImport(ctx context.Context, tx *sql.Tx, attributions []portableAttribution, recipeIDs, bookIDs map[string]string) error {
	for _, item := range attributions {
		var targetID string
		switch item.TargetKind {
		case "recipe":
			targetID = recipeIDs[item.TargetID]
		case "cookbook":
			targetID = bookIDs[item.TargetID]
		default:
			return errors.New("invalid portable attribution target")
		}
		if targetID == "" {
			return errors.New("portable attribution target is missing")
		}
		if err := insertExternalAttribution(ctx, tx, item.TargetKind, targetID, item); err != nil {
			return err
		}
	}
	return nil
}

func portableBookmarkKey(bookmark portableBookmark) portableBookmarkIdentity {
	return portableBookmarkIdentity{
		kind: bookmark.TargetKind, instanceID: bookmark.LocalInstanceID,
		recipeID: bookmark.LocalRecipeID, objectURI: bookmark.RemoteObjectURI,
	}
}

func planPortableBookmarkImport(ctx context.Context, tx *sql.Tx, userID string, bookmarks []portableBookmark) ([]portableBookmark, error) {
	if len(bookmarks) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT target_kind,local_instance_id,local_recipe_id,remote_object_uri
		FROM recipe_bookmarks WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	existing := make(map[portableBookmarkIdentity]struct{})
	count := 0
	for rows.Next() {
		var bookmark portableBookmark
		var instanceID, recipeID, objectURI sql.NullString
		if err := rows.Scan(&bookmark.TargetKind, &instanceID, &recipeID, &objectURI); err != nil {
			rows.Close()
			return nil, err
		}
		if instanceID.Valid {
			bookmark.LocalInstanceID = instanceID.String
		}
		if recipeID.Valid {
			bookmark.LocalRecipeID = recipeID.String
		}
		if objectURI.Valid {
			bookmark.RemoteObjectURI = objectURI.String
		}
		existing[portableBookmarkKey(bookmark)] = struct{}{}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if count > maxRecipeBookmarksPerAccount {
		return nil, errPortableBookmarkLimitReached
	}
	planned := make([]portableBookmark, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		if err := validatePortableBookmark(bookmark); err != nil {
			return nil, err
		}
		key := portableBookmarkKey(bookmark)
		if _, found := existing[key]; found {
			continue
		}
		existing[key] = struct{}{}
		planned = append(planned, bookmark)
	}
	if count+len(planned) > maxRecipeBookmarksPerAccount {
		return nil, errPortableBookmarkLimitReached
	}
	return planned, nil
}

func insertPortableBookmarkImport(ctx context.Context, tx *sql.Tx, userID string, bookmarks []portableBookmark) error {
	for _, bookmark := range bookmarks {
		_, err := tx.ExecContext(ctx, `INSERT INTO recipe_bookmarks
			(id,user_id,target_kind,local_instance_id,local_recipe_id,remote_object_uri,created_at)
			VALUES(?,?,?,?,?,?,?)`, newID(), userID, bookmark.TargetKind,
			portableBookmarkNullable(bookmark.LocalInstanceID), portableBookmarkNullable(bookmark.LocalRecipeID),
			portableBookmarkNullable(bookmark.RemoteObjectURI), bookmark.CreatedAt)
		if err != nil {
			if strings.Contains(err.Error(), "recipe bookmark limit reached") {
				return errPortableBookmarkLimitReached
			}
			return err
		}
	}
	return nil
}

func portableCookbookBookmarkKey(bookmark portableCookbookBookmark) portableCookbookBookmarkIdentity {
	return portableCookbookBookmarkIdentity{instanceID: bookmark.LocalInstanceID, cookbookID: bookmark.LocalCookbookID}
}

func planPortableCookbookBookmarkImport(ctx context.Context, tx *sql.Tx, userID string, bookmarks []portableCookbookBookmark) ([]portableCookbookBookmark, error) {
	if len(bookmarks) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT local_instance_id,local_cookbook_id FROM cookbook_bookmarks WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	existing := make(map[portableCookbookBookmarkIdentity]struct{})
	count := 0
	for rows.Next() {
		var bookmark portableCookbookBookmark
		if err := rows.Scan(&bookmark.LocalInstanceID, &bookmark.LocalCookbookID); err != nil {
			rows.Close()
			return nil, err
		}
		existing[portableCookbookBookmarkKey(bookmark)] = struct{}{}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if count > maxCookbookBookmarksPerAccount {
		return nil, errPortableCookbookBookmarkLimitReached
	}
	planned := make([]portableCookbookBookmark, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		if err := validatePortableCookbookBookmark(bookmark); err != nil {
			return nil, err
		}
		key := portableCookbookBookmarkKey(bookmark)
		if _, found := existing[key]; found {
			continue
		}
		existing[key] = struct{}{}
		planned = append(planned, bookmark)
	}
	if count+len(planned) > maxCookbookBookmarksPerAccount {
		return nil, errPortableCookbookBookmarkLimitReached
	}
	return planned, nil
}

func insertPortableCookbookBookmarkImport(ctx context.Context, tx *sql.Tx, userID string, bookmarks []portableCookbookBookmark) error {
	for _, bookmark := range bookmarks {
		_, err := tx.ExecContext(ctx, `INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at)
			VALUES(?,?,?,?,?)`, newID(), userID, bookmark.LocalInstanceID, bookmark.LocalCookbookID, bookmark.CreatedAt)
		if err != nil {
			if strings.Contains(err.Error(), "cookbook bookmark limit reached") {
				return errPortableCookbookBookmarkLimitReached
			}
			return err
		}
	}
	return nil
}

func portableBookmarkNullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func portableCookbookBookmarksFromBundle(bundle portableBundle) ([]portableCookbookBookmark, error) {
	if len(bundle.CookbookBookmarks) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(bundle.CookbookBookmarks), []byte("null")) {
		return nil, errors.New("cookbook bookmarks must be an array")
	}
	var bookmarks []portableCookbookBookmark
	decoder := json.NewDecoder(bytes.NewReader(bundle.CookbookBookmarks))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bookmarks); err != nil || bookmarks == nil {
		return nil, errors.New("invalid cookbook bookmarks")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing cookbook bookmark data")
	}
	if len(bookmarks) > maxPortableCookbookBookmarks {
		return nil, errors.New("too many cookbook bookmarks")
	}
	for _, bookmark := range bookmarks {
		if err := validatePortableCookbookBookmark(bookmark); err != nil {
			return nil, err
		}
	}
	return bookmarks, nil
}

func validatePortableBundle(bundle portableBundle) error {
	if (bundle.Format != portableFormatV5 && bundle.Format != portableFormatV4 && bundle.Format != portableFormatV3 && bundle.Format != portableFormat && bundle.Format != legacyPortableFormat) ||
		len(bundle.Cookbooks) > 1000 || len(bundle.Recipes) > 10000 || len(bundle.Bookmarks) > maxRecipeBookmarksPerAccount {
		return errors.New("unsupported or oversized import")
	}
	if (bundle.Format == portableFormatV3 || bundle.Format == portableFormatV4 || bundle.Format == portableFormatV5) && bundle.Bookmarks == nil {
		return errors.New("versioned archive is missing bookmarks")
	}
	if bundle.Format != portableFormatV3 && bundle.Format != portableFormatV4 && bundle.Format != portableFormatV5 && len(bundle.Bookmarks) != 0 {
		return errors.New("legacy archives cannot carry bookmarks")
	}
	if (bundle.Format == portableFormatV4 || bundle.Format == portableFormatV5) && bundle.Attributions == nil {
		return errors.New("versioned archive is missing attributions")
	}
	if bundle.Format != portableFormatV4 && bundle.Format != portableFormatV5 && len(bundle.Attributions) != 0 {
		return errors.New("legacy archives cannot carry attributions")
	}
	if _, err := portableCookbookBookmarksFromBundle(bundle); err != nil {
		return err
	}
	if bundle.Format == portableFormatV5 && bundle.CookbookBookmarks == nil {
		return errors.New("v5 archive is missing cookbook bookmarks")
	}
	if bundle.Format != portableFormatV5 && bundle.CookbookBookmarks != nil {
		return errors.New("cookbook bookmarks are only supported by v5 archives")
	}
	for _, bookmark := range bundle.Bookmarks {
		if err := validatePortableBookmark(bookmark); err != nil {
			return err
		}
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
	if bundle.Format == portableFormat || bundle.Format == portableFormatV3 || bundle.Format == portableFormatV4 || bundle.Format == portableFormatV5 {
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
	if bundle.Format == portableFormatV4 || bundle.Format == portableFormatV5 {
		if len(bundle.Attributions) > maxPortableRelationships {
			return errors.New("too many attributions")
		}
		seen := make(map[string]struct{}, len(bundle.Attributions))
		for _, item := range bundle.Attributions {
			if item.TargetKind != "recipe" && item.TargetKind != "cookbook" {
				return errors.New("invalid attribution target kind")
			}
			if item.TargetID == "" || len(item.TargetID) > 80 || (item.TargetKind == "recipe" && !recipes[item.TargetID]) || (item.TargetKind == "cookbook" && !books[item.TargetID]) {
				return errors.New("unknown attribution target")
			}
			if !validLibraryInstanceID(item.SourceInstanceID) || !validLibraryInstanceID(item.CreatorID) {
				return errors.New("invalid attribution identity")
			}
			if item.Role != "creator" && item.Role != "contributor" && item.Role != "adapter" {
				return errors.New("invalid attribution role")
			}
			if !utf8.ValidString(item.Provenance) || strings.ContainsRune(item.Provenance, '\x00') || len(item.Provenance) < 1 || len(item.Provenance) > 128 {
				return errors.New("invalid attribution provenance")
			}
			if item.NameClaim != "" && (!utf8.ValidString(item.NameClaim) || strings.ContainsRune(item.NameClaim, '\x00') || utf8.RuneCountInString(item.NameClaim) > 80 || len(item.NameClaim) > 320) {
				return errors.New("invalid attribution name claim")
			}
			key := strings.Join([]string{item.TargetKind, item.TargetID, item.SourceInstanceID, item.CreatorID, item.Role}, "\x00")
			if _, ok := seen[key]; ok {
				return errors.New("duplicate attribution")
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func validatePortableBookmark(bookmark portableBookmark) error {
	if _, err := time.Parse(time.RFC3339Nano, bookmark.CreatedAt); err != nil {
		return errors.New("invalid bookmark creation time")
	}
	switch bookmark.TargetKind {
	case "local":
		if !validLibraryInstanceID(bookmark.LocalInstanceID) || bookmark.LocalRecipeID == "" ||
			bookmark.LocalRecipeID != strings.TrimSpace(bookmark.LocalRecipeID) || len(bookmark.LocalRecipeID) > 200 || bookmark.RemoteObjectURI != "" {
			return errors.New("invalid local bookmark identity")
		}
	case "remote":
		if bookmark.LocalInstanceID != "" || bookmark.LocalRecipeID != "" {
			return errors.New("invalid remote bookmark identity")
		}
		if _, _, err := parseFederationRecipeObjectURI(bookmark.RemoteObjectURI); err != nil {
			return errors.New("invalid remote bookmark identity")
		}
	default:
		return errors.New("invalid bookmark target kind")
	}
	return nil
}

func validatePortableCookbookBookmark(bookmark portableCookbookBookmark) error {
	if _, err := time.Parse(time.RFC3339Nano, bookmark.CreatedAt); err != nil {
		return errors.New("invalid cookbook bookmark creation time")
	}
	if !validLibraryInstanceID(bookmark.LocalInstanceID) || bookmark.LocalCookbookID == "" ||
		bookmark.LocalCookbookID != strings.TrimSpace(bookmark.LocalCookbookID) || len(bookmark.LocalCookbookID) > 80 || strings.ContainsRune(bookmark.LocalCookbookID, '\x00') {
		return errors.New("invalid cookbook bookmark identity")
	}
	return nil
}

func decodePortableArchive(data []byte) (portableBundle, map[string][]byte, error) {
	media := map[string][]byte{}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if errors.Is(err, zip.ErrFormat) {
		if !utf8.Valid(data) {
			return portableBundle{}, nil, errors.New("manifest is not valid UTF-8")
		}
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
	if !utf8.Valid(manifest) {
		return portableBundle{}, nil, errors.New("manifest is not valid UTF-8")
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
	if bundle.Format != portableFormat && bundle.Format != portableFormatV3 && bundle.Format != portableFormatV4 && bundle.Format != portableFormatV5 {
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
