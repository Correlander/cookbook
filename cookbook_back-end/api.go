package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

type apiHandler struct{ db *sql.DB }

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

type recipe struct {
	OwnerID     string          `json:"owner_id"`
	ID          string          `json:"id"`
	CreatorName string          `json:"creator_name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Ingredients json.RawMessage `json:"ingredients"`
	Steps       json.RawMessage `json:"steps"`
	Visibility  visibility      `json:"visibility"`
	EditPolicy  string          `json:"edit_policy"`
	Version     int             `json:"version"`
	CookbookIDs []string        `json:"cookbook_ids"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
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
	case len(parts) == 2 && parts[0] == "recipes":
		h.recipeByID(w, r, parts[1])
	default:
		writeError(w, http.StatusNotFound, "not_found")
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
		if input.Visibility != visibilityPrivate && !h.instancePublic() {
			writeError(w, 403, "instance_private")
			return
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
		tx, err := h.db.Begin()
		if err == nil {
			_, err = tx.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, in.OwnerID, creator, in.Title, in.Description, string(in.Ingredients), string(in.Steps), in.Visibility, edit, now, now)
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
		it.Ingredients, it.Steps = json.RawMessage(ingredients), json.RawMessage(steps)
		if q != "" && !strings.Contains(strings.ToLower(it.Title+" "+it.Description), strings.ToLower(q)) {
			continue
		}
		if canReadResource(uid, access{instancePublic: public == 1, visibility: it.Visibility, ownerID: it.OwnerID}) {
			it.CookbookIDs = h.recipeCookbooks(it.ID)
			items = append(items, it)
		}
	}
	writeJSON(w, 200, items)
}

type recipeInput struct {
	OwnerID     string          `json:"-"`
	UserID      string          `json:"-"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Ingredients json.RawMessage `json:"ingredients"`
	Steps       json.RawMessage `json:"steps"`
	Visibility  visibility      `json:"visibility"`
	EditPolicy  string          `json:"edit_policy"`
	CookbookIDs []string        `json:"cookbook_ids"`
	Version     int             `json:"version"`
}

func (in *recipeInput) validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return errors.New("title required")
	}
	if in.Visibility == "" {
		in.Visibility = visibilityPrivate
	}
	if !validVisibility(in.Visibility) {
		return errors.New("invalid visibility")
	}
	if len(in.Ingredients) == 0 {
		in.Ingredients = json.RawMessage("[]")
	}
	if len(in.Steps) == 0 {
		in.Steps = json.RawMessage("[]")
	}
	var a, b any
	if json.Unmarshal(in.Ingredients, &a) != nil || json.Unmarshal(in.Steps, &b) != nil {
		return errors.New("invalid structured content")
	}
	if a == nil || b == nil {
		return errors.New("content must be arrays")
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
		it.Ingredients, it.Steps = json.RawMessage(ingredients), json.RawMessage(steps)
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
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	res, err := tx.Exec(`UPDATE recipes SET title=?,description=?,ingredients_json=?,steps_json=?,visibility=?,version=version+1,updated_at=? WHERE id=? AND version=?`, in.Title, in.Description, string(in.Ingredients), string(in.Steps), in.Visibility, timestamp(), id, in.Version)
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
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	h.recipeByID(w, requestForUser(http.MethodGet, "/api/recipes/"+id, in.UserID), id)
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
