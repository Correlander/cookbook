package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"time"
)

type apiHandler struct{ db *sql.DB }
type publicRecipeHandler struct{ db *sql.DB }
type recipeRevision struct {
	ID          string       `json:"id"`
	RecipeID    string       `json:"recipe_id"`
	Version     int          `json:"version"`
	EditorName  string       `json:"editor_name"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Visibility  visibility   `json:"visibility"`
	CreatedAt   string       `json:"created_at"`
}

func (h publicRecipeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var public int
	if err := h.db.QueryRow(`SELECT public_instance FROM instance_settings WHERE id=1`).Scan(&public); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if public != 1 {
		writeJSON(w, http.StatusOK, []recipe{})
		return
	}
	rows, err := h.db.Query(`SELECT id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,version,created_at,updated_at FROM recipes WHERE visibility='public' ORDER BY updated_at DESC LIMIT 100`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipe{}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	for rows.Next() {
		it := recipe{}
		var ingredients, steps string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := json.Unmarshal([]byte(ingredients), &it.Ingredients); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := json.Unmarshal([]byte(steps), &it.Steps); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if query != "" && !strings.Contains(strings.ToLower(it.Title+" "+it.Description), query) {
			continue
		}
		it.OwnerID = ""
		it.CookbookIDs = []string{}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type user struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type cookbook struct {
	ID          string     `json:"id"`
	OwnerID     string     `json:"owner_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Visibility  visibility `json:"visibility"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

type ingredient struct {
	Name     string `json:"name"`
	Quantity string `json:"quantity,omitempty"`
	Notes    string `json:"notes,omitempty"`
}
type recipeStep struct {
	Text string `json:"text"`
}
type recipe struct {
	OwnerID     string       `json:"owner_id,omitempty"`
	ID          string       `json:"id"`
	CreatorName string       `json:"creator_name"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Visibility  visibility   `json:"visibility"`
	EditPolicy  string       `json:"edit_policy"`
	Version     int          `json:"version"`
	CookbookIDs []string     `json:"cookbook_ids"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 1 && parts[0] == "cookbooks":
		h.cookbooks(w, r)
	case len(parts) == 2 && parts[0] == "cookbooks":
		h.cookbookByID(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "recipes":
		h.recipes(w, r)
	case len(parts) == 2 && parts[0] == "admin" && parts[1] == "invitations":
		h.invitations(w, r)
	case len(parts) == 2 && parts[0] == "recipes":
		h.recipeByID(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "recipes" && parts[2] == "revisions":
		h.recipeRevisions(w, r, parts[1])
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

func (h apiHandler) invitations(w http.ResponseWriter, r *http.Request) {
	uid := authenticatedUser(r)
	var admin int
	if err := h.db.QueryRow(`SELECT is_admin FROM users WHERE id=?`, uid).Scan(&admin); err != nil || admin != 1 {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.Query(`SELECT i.id,i.email,i.expires_at,i.created_at,u.display_name FROM invitations i JOIN users u ON u.id=i.invited_by WHERE i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>? ORDER BY i.created_at DESC`, timestamp())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		defer rows.Close()
		type item struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			ExpiresAt string `json:"expires_at"`
			CreatedAt string `json:"created_at"`
			InvitedBy string `json:"invited_by"`
		}
		items := []item{}
		for rows.Next() {
			var current item
			if rows.Scan(&current.ID, &current.Email, &current.ExpiresAt, &current.CreatedAt, &current.InvitedBy) != nil {
				writeError(w, 500, "storage_error")
				return
			}
			items = append(items, current)
		}
		if rows.Err() != nil {
			writeError(w, 500, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var input struct {
			Email string `json:"email"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		parsed, err := mail.ParseAddress(strings.TrimSpace(input.Email))
		if err != nil || parsed.Address != strings.TrimSpace(input.Email) {
			writeError(w, 400, "invalid_email")
			return
		}
		email := strings.ToLower(parsed.Address)
		var existing string
		if h.db.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&existing) == nil {
			writeError(w, 409, "account_already_exists")
			return
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			writeError(w, 500, "internal_error")
			return
		}
		token := hex.EncodeToString(raw[:])
		sum := sha256.Sum256([]byte(token))
		now := time.Now().UTC()
		id := newID()
		_, err = h.db.Exec(`INSERT INTO invitations(id,token_hash,email,invited_by,expires_at,created_at) VALUES(?,?,?,?,?,?)`, id, hex.EncodeToString(sum[:]), email, uid, now.Add(7*24*time.Hour).Format(time.RFC3339Nano), timestamp())
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": id, "email": email, "token": token})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		result, err := h.db.Exec(`UPDATE invitations SET revoked_at=? WHERE id=? AND accepted_at IS NULL AND revoked_at IS NULL`, timestamp(), id)
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			writeError(w, 404, "not_found")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "invitation_revoked"})
	default:
		writeError(w, 405, "method_not_allowed")
	}
}

func (h apiHandler) cookbooks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var input struct {
			OwnerID     string     `json:"-"`
			Title       string     `json:"title"`
			Description string     `json:"description"`
			Visibility  visibility `json:"visibility"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		input.OwnerID = authenticatedUser(r)
		if input.Title == "" || input.OwnerID == "" {
			writeError(w, http.StatusBadRequest, "invalid_cookbook")
			return
		}
		if input.Visibility == "" {
			input.Visibility = visibilityPrivate
		}
		if !validVisibility(input.Visibility) {
			writeError(w, http.StatusBadRequest, "invalid_visibility")
			return
		}
		if input.Visibility != visibilityPrivate && !h.instancePublic() {
			writeError(w, 403, "instance_private")
			return
		}
		id, now := newID(), timestamp()
		_, err := h.db.Exec(`INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, input.OwnerID, input.Title, input.Description, input.Visibility, now, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusCreated, cookbook{ID: id, OwnerID: input.OwnerID, Title: input.Title, Description: input.Description, Visibility: input.Visibility, CreatedAt: now, UpdatedAt: now})
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	userID := authenticatedUser(r)
	rows, err := h.db.Query(`SELECT c.id,c.owner_id,c.title,c.description,c.visibility,c.created_at,c.updated_at FROM cookbooks c JOIN instance_settings s ON s.id=1 WHERE (c.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships m WHERE m.cookbook_id=c.id AND m.user_id=?)) OR (s.public_instance=1 AND c.visibility='public') ORDER BY c.updated_at DESC LIMIT 100`, userID, userID)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer rows.Close()
	items := []cookbook{}
	for rows.Next() {
		var c cookbook
		if err := rows.Scan(&c.ID, &c.OwnerID, &c.Title, &c.Description, &c.Visibility, &c.CreatedAt, &c.UpdatedAt); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		items = append(items, c)
	}
	writeJSON(w, 200, items)
}

func (h apiHandler) cookbookByID(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		var c cookbook
		var public int
		err := h.db.QueryRow(`SELECT c.id,c.owner_id,c.title,c.description,c.visibility,c.created_at,c.updated_at,s.public_instance FROM cookbooks c JOIN instance_settings s ON s.id=1 WHERE c.id=?`, id).Scan(&c.ID, &c.OwnerID, &c.Title, &c.Description, &c.Visibility, &c.CreatedAt, &c.UpdatedAt, &public)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "not_found")
			return
		}
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		uid := authenticatedUser(r)
		var member role
		_ = h.db.QueryRow(`SELECT role FROM cookbook_memberships WHERE cookbook_id=? AND user_id=?`, id, uid).Scan(&member)
		if !canReadResource(uid, access{instancePublic: public == 1, visibility: c.Visibility, ownerID: c.OwnerID, memberRole: member}) {
			writeError(w, 404, "not_found")
			return
		}
		writeJSON(w, 200, c)
		return
	}
	if r.Method != http.MethodPatch {
		writeError(w, 405, "method_not_allowed")
		return
	}
	var input struct {
		UserID      string     `json:"-"`
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Visibility  visibility `json:"visibility"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.UserID = authenticatedUser(r)
	var owner string
	if err := h.db.QueryRow(`SELECT owner_id FROM cookbooks WHERE id=?`, id).Scan(&owner); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	if owner != input.UserID || input.Title == "" || !validVisibility(input.Visibility) || (input.Visibility != visibilityPrivate && !h.instancePublic()) {
		writeError(w, 403, "forbidden")
		return
	}
	_, err := h.db.Exec(`UPDATE cookbooks SET title=?,description=?,visibility=?,updated_at=? WHERE id=?`, input.Title, input.Description, input.Visibility, timestamp(), id)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	h.cookbookByID(w, requestForUser(http.MethodGet, "/api/cookbooks/"+id, input.UserID), id)
}

func (h apiHandler) recipes(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var in recipeInput
		if !decodeJSON(w, r, &in) {
			return
		}
		if err := in.validate(); err != nil {
			writeError(w, 400, "invalid_recipe")
			return
		}
		if in.Visibility != visibilityPrivate && !h.instancePublic() {
			writeError(w, 403, "instance_private")
			return
		}
		var creator string
		in.OwnerID = authenticatedUser(r)
		if err := h.db.QueryRow(`SELECT display_name FROM users WHERE id=?`, in.OwnerID).Scan(&creator); err != nil {
			writeError(w, 400, "invalid_owner")
			return
		}
		id, now := newID(), timestamp()
		edit := in.EditPolicy
		if edit == "" {
			edit = "owner_only"
		}
		ingredientsJSON, _ := json.Marshal(in.Ingredients)
		stepsJSON, _ := json.Marshal(in.Steps)
		tx, err := h.db.Begin()
		if err == nil {
			_, err = tx.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, in.OwnerID, creator, in.Title, in.Description, string(ingredientsJSON), string(stepsJSON), in.Visibility, edit, now, now)
		}
		if err != nil {
			if tx != nil {
				_ = tx.Rollback()
			}
			writeError(w, 500, "storage_error")
			return
		}
		if err = replaceCookbookRefs(tx, id, in.OwnerID, in.CookbookIDs); err != nil {
			_ = tx.Rollback()
			writeError(w, 403, "cookbook_access_denied")
			return
		}
		if err = insertRecipeRevision(tx, id, 1, in.OwnerID, in.Title, in.Description, ingredientsJSON, stepsJSON, in.Visibility); err != nil {
			_ = tx.Rollback()
			writeError(w, 500, "storage_error")
			return
		}
		if err = tx.Commit(); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		h.recipeByID(w, requestForUser(http.MethodGet, "/api/recipes/"+id, in.OwnerID), id)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed")
		return
	}
	uid, q := authenticatedUser(r), strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := h.db.Query(`SELECT r.id,r.owner_id,r.creator_name,r.title,r.description,r.ingredients_json,r.steps_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE (r.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_recipes cr JOIN cookbook_memberships m ON m.cookbook_id=cr.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=?)) OR (s.public_instance=1 AND r.visibility='public') ORDER BY r.updated_at DESC LIMIT 100`, uid, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipe{}
	for rows.Next() {
		it, public := recipe{}, 0
		var ingredients, steps string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &public); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &it.Ingredients) != nil || json.Unmarshal([]byte(steps), &it.Steps) != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Title+" "+it.Description), strings.ToLower(q)) {
			continue
		}
		var member role
		_ = h.db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? LIMIT 1`, it.ID, uid).Scan(&member)
		if canReadResource(uid, access{instancePublic: public == 1, visibility: it.Visibility, ownerID: it.OwnerID, memberRole: member}) {
			it.CookbookIDs = h.recipeCookbooks(it.ID)
			items = append(items, it)
		}
	}
	writeJSON(w, 200, items)
}

type recipeInput struct {
	OwnerID     string       `json:"-"`
	UserID      string       `json:"-"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Visibility  visibility   `json:"visibility"`
	EditPolicy  string       `json:"edit_policy"`
	CookbookIDs []string     `json:"cookbook_ids"`
	Version     int          `json:"version"`
}

func (in *recipeInput) validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 200 {
		return errors.New("invalid title")
	}
	in.Description = strings.TrimSpace(in.Description)
	if len(in.Description) > 5000 {
		return errors.New("description too long")
	}
	if in.Visibility == "" {
		in.Visibility = visibilityPrivate
	}
	if !validVisibility(in.Visibility) {
		return errors.New("invalid visibility")
	}
	if in.EditPolicy == "" {
		in.EditPolicy = "owner_only"
	}
	if in.EditPolicy != "owner_only" && in.EditPolicy != "cookbook_editors" {
		return errors.New("invalid edit policy")
	}
	if len(in.Ingredients) > 200 || len(in.Steps) > 200 || len(in.CookbookIDs) > 50 {
		return errors.New("recipe too large")
	}
	for i := range in.Ingredients {
		in.Ingredients[i].Name = strings.TrimSpace(in.Ingredients[i].Name)
		if in.Ingredients[i].Name == "" || len(in.Ingredients[i].Name) > 300 || len(in.Ingredients[i].Quantity) > 100 || len(in.Ingredients[i].Notes) > 500 {
			return errors.New("invalid ingredient")
		}
	}
	for i := range in.Steps {
		in.Steps[i].Text = strings.TrimSpace(in.Steps[i].Text)
		if in.Steps[i].Text == "" || len(in.Steps[i].Text) > 5000 {
			return errors.New("invalid recipe step")
		}
	}
	return nil
}

func (h apiHandler) recipeByID(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		it := recipe{}
		var public int
		var ingredients, steps string
		err := h.db.QueryRow(`SELECT r.id,r.owner_id,r.creator_name,r.title,r.description,r.ingredients_json,r.steps_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=?`, id).Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &public)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "not_found")
			return
		}
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &it.Ingredients) != nil || json.Unmarshal([]byte(steps), &it.Steps) != nil {
			writeError(w, 500, "storage_error")
			return
		}
		uid := authenticatedUser(r)
		var member role
		_ = h.db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? LIMIT 1`, id, uid).Scan(&member)
		if !canReadResource(uid, access{instancePublic: public == 1, visibility: it.Visibility, ownerID: it.OwnerID, memberRole: member}) {
			writeError(w, 404, "not_found")
			return
		}
		it.CookbookIDs = h.recipeCookbooks(id)
		writeJSON(w, 200, it)
		return
	}
	if r.Method != http.MethodPut {
		writeError(w, 405, "method_not_allowed")
		return
	}
	var in recipeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, 400, "invalid_recipe")
		return
	}
	in.UserID = authenticatedUser(r)
	if in.Visibility != visibilityPrivate && !h.instancePublic() {
		writeError(w, 403, "instance_private")
		return
	}
	if in.UserID == "" || in.Version < 1 {
		writeError(w, 400, "invalid_update")
		return
	}
	var owner string
	var version int
	var edit string
	if err := h.db.QueryRow(`SELECT owner_id,version,edit_policy FROM recipes WHERE id=?`, id).Scan(&owner, &version, &edit); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	var memberRole role
	_ = h.db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END LIMIT 1`, id, in.UserID).Scan(&memberRole)
	if !canEditResource(in.UserID, access{ownerID: owner, memberRole: memberRole}) || (in.UserID != owner && (edit != "cookbook_editors" || memberRole != roleEditor)) {
		writeError(w, 403, "forbidden")
		return
	}
	if version != in.Version {
		writeError(w, 409, "version_conflict")
		return
	}
	ingredientsJSON, _ := json.Marshal(in.Ingredients)
	stepsJSON, _ := json.Marshal(in.Steps)
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	res, err := tx.Exec(`UPDATE recipes SET title=?,description=?,ingredients_json=?,steps_json=?,visibility=?,version=version+1,updated_at=? WHERE id=? AND version=?`, in.Title, in.Description, string(ingredientsJSON), string(stepsJSON), in.Visibility, timestamp(), id, in.Version)
	if err != nil {
		tx.Rollback()
		writeError(w, 500, "storage_error")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		tx.Rollback()
		writeError(w, 409, "version_conflict")
		return
	}
	if err := replaceCookbookRefs(tx, id, in.UserID, in.CookbookIDs); err != nil {
		tx.Rollback()
		writeError(w, 403, "cookbook_access_denied")
		return
	}
	if err := insertRecipeRevision(tx, id, in.Version+1, in.UserID, in.Title, in.Description, ingredientsJSON, stepsJSON, in.Visibility); err != nil {
		tx.Rollback()
		writeError(w, 500, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	h.recipeByID(w, requestForUser(http.MethodGet, "/api/recipes/"+id, in.UserID), id)
}

func (h apiHandler) recipeRevisions(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	uid := authenticatedUser(r)
	var owner string
	var visibilityValue visibility
	var public int
	if err := h.db.QueryRow(`SELECT r.owner_id,r.visibility,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=?`, id).Scan(&owner, &visibilityValue, &public); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	var member role
	_ = h.db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? LIMIT 1`, id, uid).Scan(&member)
	if !canReadResource(uid, access{instancePublic: public == 1, visibility: visibilityValue, ownerID: owner, memberRole: member}) {
		writeError(w, 404, "not_found")
		return
	}
	rows, err := h.db.Query(`SELECT rr.id,rr.recipe_id,rr.version,u.display_name,rr.title,rr.description,rr.ingredients_json,rr.steps_json,rr.visibility,rr.created_at FROM recipe_revisions rr JOIN users u ON u.id=rr.editor_id WHERE rr.recipe_id=? ORDER BY rr.version DESC`, id)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipeRevision{}
	for rows.Next() {
		item := recipeRevision{}
		var ingredients, steps string
		if err := rows.Scan(&item.ID, &item.RecipeID, &item.Version, &item.EditorName, &item.Title, &item.Description, &ingredients, &steps, &item.Visibility, &item.CreatedAt); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &item.Ingredients) != nil || json.Unmarshal([]byte(steps), &item.Steps) != nil {
			writeError(w, 500, "storage_error")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	writeJSON(w, 200, items)
}

func insertRecipeRevision(tx *sql.Tx, recipeID string, version int, editorID, title, description string, ingredients, steps []byte, visibilityValue visibility) error {
	_, err := tx.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, newID(), recipeID, version, editorID, title, description, string(ingredients), string(steps), visibilityValue, timestamp())
	return err
}

func (h apiHandler) userExists(id string) bool {
	var n int
	return h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, id).Scan(&n) == nil && n == 1
}
func (h apiHandler) setRecipeCookbooks(recipeID, userID string, ids []string) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	if err = replaceCookbookRefs(tx, recipeID, userID, ids); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
func replaceCookbookRefs(tx *sql.Tx, recipeID, userID string, ids []string) error {
	if _, err := tx.Exec(`DELETE FROM cookbook_recipes WHERE recipe_id=?`, recipeID); err != nil {
		return err
	}
	for _, id := range ids {
		var owner string
		if err := tx.QueryRow(`SELECT owner_id FROM cookbooks WHERE id=?`, id).Scan(&owner); err != nil || owner != userID {
			return errors.New("not cookbook owner")
		}
		if _, err := tx.Exec(`INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES(?,?,?,?)`, id, recipeID, userID, timestamp()); err != nil {
			return err
		}
	}
	return nil
}
func (h apiHandler) recipeCookbooks(id string) []string {
	rows, err := h.db.Query(`SELECT cookbook_id FROM cookbook_recipes WHERE recipe_id=? ORDER BY cookbook_id`, id)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			ids = append(ids, v)
		}
	}
	return ids
}
func validVisibility(v visibility) bool {
	return v == visibilityPrivate || v == visibilityPublic || v == visibilityUnlisted
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, 400, "invalid_json")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, 400, "invalid_json")
		return false
	}
	return true
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func requestForUser(method, path, userID string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	return r.WithContext(context.WithValue(r.Context(), userContextKey{}, userID))
}

func (h apiHandler) instancePublic() bool {
	var enabled int
	return h.db.QueryRow(`SELECT public_instance FROM instance_settings WHERE id=1`).Scan(&enabled) == nil && enabled == 1
}
