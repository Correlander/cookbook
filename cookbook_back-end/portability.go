package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxImportBytes = 10 << 20
const portableFormat = "cookbook-export-v1"

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
	Visibility  string       `json:"visibility"`
	EditPolicy  string       `json:"edit_policy"`
	CookbookIDs []string     `json:"cookbook_ids"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
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
	rows, err := h.db.Query(`SELECT id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,created_at,updated_at FROM recipes WHERE owner_id=? AND deleted_at IS NULL ORDER BY id`, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	recipeSet := map[string]bool{}
	for rows.Next() {
		var recipe portableRecipe
		var ingredients, steps, tags string
		if err := rows.Scan(&recipe.ID, &recipe.CreatorName, &recipe.Title, &recipe.Description, &ingredients, &steps, &recipe.Servings, &recipe.PrepTime, &recipe.CookTime, &tags, &recipe.Visibility, &recipe.EditPolicy, &recipe.CreatedAt, &recipe.UpdatedAt); err != nil {
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
		bundle.Recipes = append(bundle.Recipes, recipe)
		recipeSet[recipe.ID] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "storage_error")
		return
	}
	rows.Close()
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
	w.Header().Set("Content-Disposition", "attachment; filename=cookbook-export.json")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(bundle)
}

func (h apiHandler) importData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	var bundle portableBundle
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_import")
		return
	}
	if err := validatePortableBundle(bundle); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_import")
		return
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
		ingredients, _ := json.Marshal(recipe.Ingredients)
		steps, _ := json.Marshal(recipe.Steps)
		tags, _ := json.Marshal(recipe.Tags)
		creator := strings.TrimSpace(recipe.CreatorName)
		if creator == "" {
			creator = "Unknown creator"
		}
		if _, err := tx.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, uid, creator, recipe.Title, recipe.Description, string(ingredients), string(steps), recipe.Servings, recipe.PrepTime, recipe.CookTime, string(tags), "private", "owner_only", timestamp(), timestamp()); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if err := insertRecipeRevision(tx, id, 1, uid, recipe.Title, recipe.Description, ingredients, steps, recipe.Servings, recipe.PrepTime, recipe.CookTime, tags, "private", ""); err != nil {
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
	writeJSON(w, http.StatusCreated, map[string]int{"cookbooks": len(bundle.Cookbooks), "recipes": len(bundle.Recipes)})
}

func validatePortableBundle(bundle portableBundle) error {
	if bundle.Format != portableFormat || len(bundle.Cookbooks) > 1000 || len(bundle.Recipes) > 10000 {
		return errors.New("unsupported or oversized import")
	}
	books, recipes := map[string]bool{}, map[string]bool{}
	for _, b := range bundle.Cookbooks {
		if b.ID == "" || books[b.ID] || strings.TrimSpace(b.Title) == "" || len(b.Title) > 200 || len(b.Description) > 5000 || !validVisibility(visibility(b.Visibility)) {
			return errors.New("invalid cookbook")
		}
		books[b.ID] = true
	}
	for _, r := range bundle.Recipes {
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
