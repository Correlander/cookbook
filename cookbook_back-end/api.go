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
	"reflect"
	"strconv"
	"strings"
	"time"
)

type apiHandler struct{ db *sql.DB }
type publicRecipeHandler struct{ db *sql.DB }

func listPage(r *http.Request) (int, int, error) {
	limit, offset := 24, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, errors.New("invalid limit")
		}
		limit = parsed
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 1000000 {
			return 0, 0, errors.New("invalid offset")
		}
		offset = parsed
	}
	return limit, offset, nil
}

type recipeRevision struct {
	ID          string       `json:"id"`
	RecipeID    string       `json:"recipe_id"`
	Version     int          `json:"version"`
	EditorName  string       `json:"editor_name"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Servings    int          `json:"servings"`
	PrepTime    int          `json:"prep_time_minutes"`
	CookTime    int          `json:"cook_time_minutes"`
	Tags        []string     `json:"tags"`
	Visibility  visibility   `json:"visibility"`
	CreatedAt   string       `json:"created_at"`
}

type revisionFieldChange struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

type revisionDiff struct {
	FromVersion int                            `json:"from_version"`
	ToVersion   int                            `json:"to_version"`
	Changes     map[string]revisionFieldChange `json:"changes"`
}

func (h publicRecipeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	var public int
	if err := h.db.QueryRow(`SELECT public_instance FROM instance_settings WHERE id=1`).Scan(&public); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if public != 1 {
		writeJSON(w, http.StatusOK, []recipe{})
		return
	}
	rows, err := h.db.Query(`SELECT id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at FROM recipes WHERE deleted_at IS NULL AND visibility='public' AND (?='' OR instr(lower(title||' '||description||' '||tags_json),lower(?))>0) ORDER BY updated_at DESC,id LIMIT ? OFFSET ?`, query, query, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipe{}
	for rows.Next() {
		it := recipe{}
		var ingredients, steps, tags string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Servings, &it.PrepTime, &it.CookTime, &tags, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt); err != nil {
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
		if err := json.Unmarshal([]byte(tags), &it.Tags); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
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
	Servings    int          `json:"servings"`
	PrepTime    int          `json:"prep_time_minutes"`
	CookTime    int          `json:"cook_time_minutes"`
	Tags        []string     `json:"tags"`
	Visibility  visibility   `json:"visibility"`
	EditPolicy  string       `json:"edit_policy"`
	CanEdit     bool         `json:"can_edit"`
	CanDelete   bool         `json:"can_delete"`
	Version     int          `json:"version"`
	CookbookIDs []string     `json:"cookbook_ids"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	DeletedAt   string       `json:"deleted_at,omitempty"`
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
	case len(parts) == 3 && parts[0] == "cookbooks" && parts[2] == "members":
		h.cookbookMembers(w, r, parts[1])
	case len(parts) == 4 && parts[0] == "cookbooks" && parts[2] == "members" && parts[3] == "invitations":
		h.cookbookInvitations(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "recipes" && parts[1] == "trash":
		h.recipeTrash(w, r)
	case len(parts) == 3 && parts[0] == "recipes" && parts[2] == "restore":
		h.restoreTrashedRecipe(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "recipes":
		h.recipes(w, r)
	case len(parts) == 2 && parts[0] == "admin" && parts[1] == "invitations":
		h.invitations(w, r)
	case len(parts) == 2 && parts[0] == "recipes":
		h.recipeByID(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "recipes" && parts[2] == "revisions":
		h.recipeRevisions(w, r, parts[1])
	case len(parts) == 4 && parts[0] == "recipes" && parts[2] == "revisions" && parts[3] == "diff":
		h.recipeRevisionDiff(w, r, parts[1])
	case len(parts) == 5 && parts[0] == "recipes" && parts[2] == "revisions" && parts[4] == "restore":
		h.restoreRecipeRevision(w, r, parts[1], parts[3])
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
		rows, err := h.db.Query(`SELECT i.id,i.email,i.expires_at,i.created_at,u.display_name FROM invitations i JOIN users u ON u.id=i.invited_by WHERE i.cookbook_id IS NULL AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>? ORDER BY i.created_at DESC`, timestamp())
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
		if _, err := h.db.Exec(`UPDATE invitations SET revoked_at=? WHERE cookbook_id IS NULL AND lower(email)=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at<=?`, timestamp(), email, timestamp()); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		var pending int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM invitations WHERE cookbook_id IS NULL AND lower(email)=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>?`, email, timestamp()).Scan(&pending); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if pending != 0 {
			writeError(w, 409, "invitation_already_pending")
			return
		}
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

func (h apiHandler) cookbookMembers(w http.ResponseWriter, r *http.Request, cookbookID string) {
	uid := authenticatedUser(r)
	var ownerID string
	if err := h.db.QueryRow(`SELECT owner_id FROM cookbooks WHERE id=?`, cookbookID).Scan(&ownerID); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	if r.Method == http.MethodGet {
		var member int
		_ = h.db.QueryRow(`SELECT COUNT(*) FROM cookbook_memberships WHERE cookbook_id=? AND user_id=?`, cookbookID, uid).Scan(&member)
		if uid == "" || (uid != ownerID && member != 1) {
			writeError(w, 404, "not_found")
			return
		}
		type item struct {
			UserID      string `json:"user_id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Role        role   `json:"role"`
		}
		items := []item{}
		var owner item
		if err := h.db.QueryRow(`SELECT id,email,display_name FROM users WHERE id=?`, ownerID).Scan(&owner.UserID, &owner.Email, &owner.DisplayName); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		owner.Role = roleOwner
		items = append(items, owner)
		rows, err := h.db.Query(`SELECT u.id,u.email,u.display_name,m.role FROM cookbook_memberships m JOIN users u ON u.id=m.user_id WHERE m.cookbook_id=? ORDER BY u.display_name`, cookbookID)
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var current item
			if rows.Scan(&current.UserID, &current.Email, &current.DisplayName, &current.Role) != nil {
				writeError(w, 500, "storage_error")
				return
			}
			items = append(items, current)
		}
		if rows.Err() != nil {
			writeError(w, 500, "storage_error")
			return
		}
		writeJSON(w, 200, items)
		return
	}
	if uid != ownerID {
		writeError(w, 403, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var input struct {
			UserID string `json:"user_id"`
			Role   role   `json:"role"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.UserID == ownerID || (input.Role != roleViewer && input.Role != roleEditor) {
			writeError(w, 400, "invalid_member_role")
			return
		}
		result, err := h.db.Exec(`UPDATE cookbook_memberships SET role=? WHERE cookbook_id=? AND user_id=?`, input.Role, cookbookID, input.UserID)
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			writeError(w, 404, "not_found")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "member_updated"})
	case http.MethodDelete:
		userID := r.URL.Query().Get("user_id")
		if userID == ownerID {
			writeError(w, 400, "owner_cannot_be_removed")
			return
		}
		result, err := h.db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id=? AND user_id=?`, cookbookID, userID)
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			writeError(w, 404, "not_found")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "member_removed"})
	default:
		writeError(w, 405, "method_not_allowed")
	}
}

func (h apiHandler) cookbookInvitations(w http.ResponseWriter, r *http.Request, cookbookID string) {
	uid := authenticatedUser(r)
	var ownerID string
	if err := h.db.QueryRow(`SELECT owner_id FROM cookbooks WHERE id=?`, cookbookID).Scan(&ownerID); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	if uid != ownerID {
		writeError(w, 403, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		type item struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			Role      role   `json:"role"`
			ExpiresAt string `json:"expires_at"`
			CreatedAt string `json:"created_at"`
		}
		rows, err := h.db.Query(`SELECT id,email,role,expires_at,created_at FROM invitations WHERE cookbook_id=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>? ORDER BY created_at DESC`, cookbookID, timestamp())
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		defer rows.Close()
		items := []item{}
		for rows.Next() {
			var current item
			if rows.Scan(&current.ID, &current.Email, &current.Role, &current.ExpiresAt, &current.CreatedAt) != nil {
				writeError(w, 500, "storage_error")
				return
			}
			items = append(items, current)
		}
		if rows.Err() != nil {
			writeError(w, 500, "storage_error")
			return
		}
		writeJSON(w, 200, items)
	case http.MethodPost:
		var input struct {
			Email string `json:"email"`
			Role  role   `json:"role"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		parsed, err := mail.ParseAddress(strings.TrimSpace(input.Email))
		if err != nil || parsed.Address != strings.TrimSpace(input.Email) || len(parsed.Address) > 254 {
			writeError(w, 400, "invalid_email")
			return
		}
		if input.Role == "" {
			input.Role = roleViewer
		}
		if input.Role != roleViewer && input.Role != roleEditor {
			writeError(w, 400, "invalid_member_role")
			return
		}
		email := strings.ToLower(parsed.Address)
		if _, err := h.db.Exec(`UPDATE invitations SET revoked_at=? WHERE cookbook_id=? AND lower(email)=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at<=?`, timestamp(), cookbookID, email, timestamp()); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		var existing int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM users u LEFT JOIN cookbook_memberships m ON m.user_id=u.id AND m.cookbook_id=? WHERE u.email=? AND (u.id=? OR m.user_id IS NOT NULL)`, cookbookID, email, ownerID).Scan(&existing); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if existing > 0 {
			writeError(w, 409, "already_a_member")
			return
		}
		var pending int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM invitations WHERE cookbook_id=? AND lower(email)=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>?`, cookbookID, email, timestamp()).Scan(&pending); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if pending > 0 {
			writeError(w, 409, "invitation_already_pending")
			return
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			writeError(w, 500, "internal_error")
			return
		}
		token := hex.EncodeToString(raw[:])
		sum := sha256.Sum256([]byte(token))
		id := newID()
		expires := time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339Nano)
		if _, err := h.db.Exec(`INSERT INTO invitations(id,token_hash,email,cookbook_id,role,invited_by,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, hex.EncodeToString(sum[:]), email, cookbookID, input.Role, uid, expires, timestamp()); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		writeJSON(w, 201, map[string]string{"id": id, "email": email, "role": string(input.Role), "token": token, "expires_at": expires})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		result, err := h.db.Exec(`UPDATE invitations SET revoked_at=? WHERE id=? AND cookbook_id=? AND accepted_at IS NULL AND revoked_at IS NULL`, timestamp(), id, cookbookID)
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
		tagsJSON, _ := json.Marshal(in.Tags)
		tx, err := h.db.Begin()
		if err == nil {
			_, err = tx.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, in.OwnerID, creator, in.Title, in.Description, string(ingredientsJSON), string(stepsJSON), in.Servings, in.PrepTime, in.CookTime, string(tagsJSON), in.Visibility, edit, now, now)
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
		if err = insertRecipeRevision(tx, id, 1, in.OwnerID, in.Title, in.Description, ingredientsJSON, stepsJSON, in.Servings, in.PrepTime, in.CookTime, tagsJSON, in.Visibility); err != nil {
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
	limit, offset, err := listPage(r)
	if err != nil || len(q) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_pagination_or_query")
		return
	}
	rows, err := h.db.Query(`SELECT r.id,r.owner_id,r.creator_name,r.title,r.description,r.ingredients_json,r.steps_json,r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.deleted_at IS NULL AND ((r.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_recipes cr JOIN cookbook_memberships m ON m.cookbook_id=cr.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=?)) OR (s.public_instance=1 AND r.visibility='public')) AND (?='' OR instr(lower(r.title||' '||r.description||' '||r.tags_json),lower(?))>0) ORDER BY r.updated_at DESC,r.id LIMIT ? OFFSET ?`, uid, uid, q, q, limit, offset)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	items := []recipe{}
	publicFlags := []bool{}
	for rows.Next() {
		it, public := recipe{}, 0
		var ingredients, steps, tags string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Servings, &it.PrepTime, &it.CookTime, &tags, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &public); err != nil {
			rows.Close()
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &it.Ingredients) != nil || json.Unmarshal([]byte(steps), &it.Steps) != nil {
			rows.Close()
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(tags), &it.Tags) != nil {
			rows.Close()
			writeError(w, 500, "storage_error")
			return
		}
		items = append(items, it)
		publicFlags = append(publicFlags, public == 1)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "storage_error")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	visibleItems := make([]recipe, 0, len(items))
	for index := range items {
		it := items[index]
		var member role
		_ = h.db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? LIMIT 1`, it.ID, uid).Scan(&member)
		if !canReadResource(uid, access{instancePublic: publicFlags[index], visibility: it.Visibility, ownerID: it.OwnerID, memberRole: member}) {
			continue
		}
		it.CookbookIDs = h.recipeCookbooks(it.ID)
		it.CanEdit = canEditRecipe(uid, access{ownerID: it.OwnerID, memberRole: member}, it.EditPolicy)
		it.CanDelete = uid != "" && uid == it.OwnerID
		visibleItems = append(visibleItems, it)
	}
	writeJSON(w, 200, visibleItems)
}

type recipeInput struct {
	OwnerID     string       `json:"-"`
	UserID      string       `json:"-"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Servings    int          `json:"servings"`
	PrepTime    int          `json:"prep_time_minutes"`
	CookTime    int          `json:"cook_time_minutes"`
	Tags        []string     `json:"tags"`
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
	if in.Servings < 0 || in.Servings > 1000 || in.PrepTime < 0 || in.PrepTime > 1440 || in.CookTime < 0 || in.CookTime > 1440 {
		return errors.New("invalid recipe timing or servings")
	}
	if len(in.Tags) > 30 {
		return errors.New("too many recipe tags")
	}
	cleanTags := make([]string, 0, len(in.Tags))
	seenTags := make(map[string]struct{}, len(in.Tags))
	for _, tag := range in.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if len(tag) > 40 {
			return errors.New("recipe tag too long")
		}
		if _, exists := seenTags[tag]; exists {
			continue
		}
		seenTags[tag] = struct{}{}
		cleanTags = append(cleanTags, tag)
	}
	in.Tags = cleanTags
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
	if r.Method == http.MethodDelete {
		uid := authenticatedUser(r)
		if uid == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		result, err := h.db.Exec(`UPDATE recipes SET deleted_at=? WHERE id=? AND owner_id=? AND deleted_at IS NULL`, timestamp(), id, uid)
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "recipe_trashed"})
		return
	}
	if r.Method == http.MethodGet {
		it := recipe{}
		var public int
		var ingredients, steps, tags string
		err := h.db.QueryRow(`SELECT r.id,r.owner_id,r.creator_name,r.title,r.description,r.ingredients_json,r.steps_json,r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, id).Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Servings, &it.PrepTime, &it.CookTime, &tags, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &public)
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
		if json.Unmarshal([]byte(tags), &it.Tags) != nil {
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
		it.CanEdit = canEditRecipe(uid, access{ownerID: it.OwnerID, memberRole: member}, it.EditPolicy)
		it.CanDelete = uid != "" && uid == it.OwnerID
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
	if err := h.db.QueryRow(`SELECT owner_id,version,edit_policy FROM recipes WHERE id=? AND deleted_at IS NULL`, id).Scan(&owner, &version, &edit); err != nil {
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
	tagsJSON, _ := json.Marshal(in.Tags)
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	res, err := tx.Exec(`UPDATE recipes SET title=?,description=?,ingredients_json=?,steps_json=?,servings=?,prep_time_minutes=?,cook_time_minutes=?,tags_json=?,visibility=?,version=version+1,updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`, in.Title, in.Description, string(ingredientsJSON), string(stepsJSON), in.Servings, in.PrepTime, in.CookTime, string(tagsJSON), in.Visibility, timestamp(), id, in.Version)
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
	if err := insertRecipeRevision(tx, id, in.Version+1, in.UserID, in.Title, in.Description, ingredientsJSON, stepsJSON, in.Servings, in.PrepTime, in.CookTime, tagsJSON, in.Visibility); err != nil {
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

func (h apiHandler) recipeTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	rows, err := h.db.Query(`SELECT id,owner_id,creator_name,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at,deleted_at FROM recipes WHERE owner_id=? AND deleted_at IS NOT NULL ORDER BY deleted_at DESC,id LIMIT ? OFFSET ?`, uid, limit, offset)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipe{}
	for rows.Next() {
		it := recipe{}
		var ingredients, steps, tags string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Servings, &it.PrepTime, &it.CookTime, &tags, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &it.DeletedAt); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &it.Ingredients) != nil || json.Unmarshal([]byte(steps), &it.Steps) != nil || json.Unmarshal([]byte(tags), &it.Tags) != nil {
			writeError(w, 500, "storage_error")
			return
		}
		it.CanEdit = true
		it.CanDelete = true
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	writeJSON(w, 200, items)
}

func (h apiHandler) restoreTrashedRecipe(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	result, err := h.db.Exec(`UPDATE recipes SET deleted_at=NULL,updated_at=? WHERE id=? AND owner_id=? AND deleted_at IS NOT NULL`, timestamp(), id, uid)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	h.recipeByID(w, requestForUser(http.MethodGet, "/api/recipes/"+id, uid), id)
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
	if err := h.db.QueryRow(`SELECT r.owner_id,r.visibility,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, id).Scan(&owner, &visibilityValue, &public); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	var member role
	_ = h.db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? LIMIT 1`, id, uid).Scan(&member)
	if !canReadResource(uid, access{instancePublic: public == 1, visibility: visibilityValue, ownerID: owner, memberRole: member}) {
		writeError(w, 404, "not_found")
		return
	}
	rows, err := h.db.Query(`SELECT rr.id,rr.recipe_id,rr.version,u.display_name,rr.title,rr.description,rr.ingredients_json,rr.steps_json,rr.servings,rr.prep_time_minutes,rr.cook_time_minutes,rr.tags_json,rr.visibility,rr.created_at FROM recipe_revisions rr JOIN users u ON u.id=rr.editor_id WHERE rr.recipe_id=? ORDER BY rr.version DESC`, id)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipeRevision{}
	for rows.Next() {
		item := recipeRevision{}
		var ingredients, steps, tags string
		if err := rows.Scan(&item.ID, &item.RecipeID, &item.Version, &item.EditorName, &item.Title, &item.Description, &ingredients, &steps, &item.Servings, &item.PrepTime, &item.CookTime, &tags, &item.Visibility, &item.CreatedAt); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(ingredients), &item.Ingredients) != nil || json.Unmarshal([]byte(steps), &item.Steps) != nil {
			writeError(w, 500, "storage_error")
			return
		}
		if json.Unmarshal([]byte(tags), &item.Tags) != nil {
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

func (h apiHandler) recipeRevisionDiff(w http.ResponseWriter, r *http.Request, recipeID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	fromVersion, fromErr := strconv.Atoi(r.URL.Query().Get("from"))
	toVersion, toErr := strconv.Atoi(r.URL.Query().Get("to"))
	if fromErr != nil || toErr != nil || fromVersion < 1 || toVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_revision_versions")
		return
	}
	uid := authenticatedUser(r)
	var owner string
	var visibilityValue visibility
	var public int
	if err := h.db.QueryRow(`SELECT r.owner_id,r.visibility,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, recipeID).Scan(&owner, &visibilityValue, &public); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var member role
	_ = h.db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? LIMIT 1`, recipeID, uid).Scan(&member)
	if !canReadResource(uid, access{instancePublic: public == 1, visibility: visibilityValue, ownerID: owner, memberRole: member}) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	type snapshot struct {
		Title       string
		Description string
		Ingredients []ingredient
		Steps       []recipeStep
		Servings    int
		PrepTime    int
		CookTime    int
		Tags        []string
		Visibility  visibility
	}
	load := func(version int) (snapshot, error) {
		var item snapshot
		var ingredientsJSON, stepsJSON, tagsJSON string
		err := h.db.QueryRow(`SELECT title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility FROM recipe_revisions WHERE recipe_id=? AND version=?`, recipeID, version).Scan(&item.Title, &item.Description, &ingredientsJSON, &stepsJSON, &item.Servings, &item.PrepTime, &item.CookTime, &tagsJSON, &item.Visibility)
		if err != nil {
			return snapshot{}, err
		}
		if json.Unmarshal([]byte(ingredientsJSON), &item.Ingredients) != nil || json.Unmarshal([]byte(stepsJSON), &item.Steps) != nil || json.Unmarshal([]byte(tagsJSON), &item.Tags) != nil {
			return snapshot{}, errors.New("invalid revision data")
		}
		return item, nil
	}
	before, err := load(fromVersion)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	after, err := load(toVersion)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	changes := make(map[string]revisionFieldChange)
	add := func(field string, oldValue, newValue any) {
		if !reflect.DeepEqual(oldValue, newValue) {
			changes[field] = revisionFieldChange{Before: oldValue, After: newValue}
		}
	}
	add("title", before.Title, after.Title)
	add("description", before.Description, after.Description)
	add("ingredients", before.Ingredients, after.Ingredients)
	add("steps", before.Steps, after.Steps)
	add("servings", before.Servings, after.Servings)
	add("prep_time_minutes", before.PrepTime, after.PrepTime)
	add("cook_time_minutes", before.CookTime, after.CookTime)
	add("tags", before.Tags, after.Tags)
	add("visibility", before.Visibility, after.Visibility)
	writeJSON(w, http.StatusOK, revisionDiff{FromVersion: fromVersion, ToVersion: toVersion, Changes: changes})
}

func (h apiHandler) restoreRecipeRevision(w http.ResponseWriter, r *http.Request, recipeID, versionText string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	revisionVersion, err := strconv.Atoi(versionText)
	if err != nil || revisionVersion < 1 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var input struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	userID := authenticatedUser(r)
	var owner, editPolicy string
	var currentVersion int
	if err := h.db.QueryRow(`SELECT owner_id,edit_policy,version FROM recipes WHERE id=? AND deleted_at IS NULL`, recipeID).Scan(&owner, &editPolicy, &currentVersion); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	var memberRole role
	_ = h.db.QueryRow(`SELECT role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END LIMIT 1`, recipeID, userID).Scan(&memberRole)
	if !canEditResource(userID, access{ownerID: owner, memberRole: memberRole}) || (userID != owner && (editPolicy != "cookbook_editors" || memberRole != roleEditor)) {
		writeError(w, 403, "forbidden")
		return
	}
	if input.ExpectedVersion != currentVersion {
		writeError(w, 409, "version_conflict")
		return
	}
	var title, description, ingredients, steps, tags string
	var servings, prepTime, cookTime int
	var restoredVisibility visibility
	if err := h.db.QueryRow(`SELECT title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility FROM recipe_revisions WHERE recipe_id=? AND version=?`, recipeID, revisionVersion).Scan(&title, &description, &ingredients, &steps, &servings, &prepTime, &cookTime, &tags, &restoredVisibility); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	if restoredVisibility != visibilityPrivate && !h.instancePublic() {
		writeError(w, 403, "instance_private")
		return
	}
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	result, err := tx.Exec(`UPDATE recipes SET title=?,description=?,ingredients_json=?,steps_json=?,servings=?,prep_time_minutes=?,cook_time_minutes=?,tags_json=?,visibility=?,version=version+1,updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`, title, description, ingredients, steps, servings, prepTime, cookTime, tags, restoredVisibility, timestamp(), recipeID, currentVersion)
	if err != nil {
		_ = tx.Rollback()
		writeError(w, 500, "storage_error")
		return
	}
	updated, _ := result.RowsAffected()
	if updated != 1 {
		_ = tx.Rollback()
		writeError(w, 409, "version_conflict")
		return
	}
	if err := insertRecipeRevision(tx, recipeID, currentVersion+1, userID, title, description, []byte(ingredients), []byte(steps), servings, prepTime, cookTime, []byte(tags), restoredVisibility); err != nil {
		_ = tx.Rollback()
		writeError(w, 500, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	h.recipeByID(w, requestForUser(http.MethodGet, "/api/recipes/"+recipeID, userID), recipeID)
}

func insertRecipeRevision(tx *sql.Tx, recipeID string, version int, editorID, title, description string, ingredients, steps []byte, servings, prepTime, cookTime int, tags []byte, visibilityValue visibility) error {
	_, err := tx.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, newID(), recipeID, version, editorID, title, description, string(ingredients), string(steps), servings, prepTime, cookTime, string(tags), visibilityValue, timestamp())
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
	if _, err := tx.Exec(`DELETE FROM cookbook_recipes WHERE recipe_id=? AND cookbook_id IN (SELECT id FROM cookbooks WHERE owner_id=?)`, recipeID, userID); err != nil {
		return err
	}
	for _, id := range ids {
		var owner string
		if err := tx.QueryRow(`SELECT owner_id FROM cookbooks WHERE id=?`, id).Scan(&owner); err != nil {
			return errors.New("cookbook does not exist")
		}
		if owner != userID {
			var associated int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM cookbook_recipes WHERE cookbook_id=? AND recipe_id=?`, id, recipeID).Scan(&associated); err != nil || associated != 1 {
				return errors.New("not cookbook owner")
			}
			continue
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
