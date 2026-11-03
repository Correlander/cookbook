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

type apiHandler struct {
	db               *sql.DB
	mediaDir         string
	federationKeyDir string
}
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

func recipeListOptions(r *http.Request) (string, string, error) {
	order := "r.updated_at DESC,r.id"
	switch r.URL.Query().Get("sort") {
	case "", "recent":
	case "oldest":
		order = "r.updated_at ASC,r.id"
	case "title":
		order = "lower(r.title) ASC,r.id"
	default:
		return "", "", errors.New("invalid sort")
	}
	tag := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag")))
	if len(tag) > 40 {
		return "", "", errors.New("invalid tag")
	}
	tagJSON := ""
	if tag != "" {
		encoded, err := json.Marshal(tag)
		if err != nil {
			return "", "", errors.New("invalid tag")
		}
		tagJSON = string(encoded)
	}
	return order, tagJSON, nil
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
	rows, err := h.db.Query(`SELECT r.id,r.owner_id,r.creator_name,r.title,r.description,r.ingredients_json,r.steps_json,r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,r.image_path
		FROM recipes r JOIN recipe_publication_policy p ON p.recipe_id=r.id JOIN instance_settings s ON s.id=1
		WHERE s.public_instance=1 AND r.deleted_at IS NULL AND r.visibility='public' AND p.publication_state='published'
		AND (?='' OR instr(lower(r.title||' '||r.description||' '||r.tags_json),lower(?))>0)
		ORDER BY r.updated_at DESC,r.id LIMIT ? OFFSET ?`, query, query, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipe{}
	for rows.Next() {
		it := recipe{}
		var ingredients, steps, tags string
		var imagePath string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.Title, &it.Description, &ingredients, &steps, &it.Servings, &it.PrepTime, &it.CookTime, &tags, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &imagePath); err != nil {
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
		it.SourceRecipeID = ""
		it.CookbookIDs = []string{}
		it.EditPolicy = ""
		if imagePath != "" {
			it.Image = "/api/recipes/" + it.ID + "/image"
		}
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
	OwnerID          string       `json:"owner_id,omitempty"`
	ID               string       `json:"id"`
	CreatorName      string       `json:"creator_name"`
	SourceRecipeID   string       `json:"source_recipe_id,omitempty"`
	Title            string       `json:"title"`
	Description      string       `json:"description"`
	Ingredients      []ingredient `json:"ingredients"`
	Steps            []recipeStep `json:"steps"`
	Servings         int          `json:"servings"`
	PrepTime         int          `json:"prep_time_minutes"`
	CookTime         int          `json:"cook_time_minutes"`
	Tags             []string     `json:"tags"`
	Visibility       visibility   `json:"visibility"`
	EditPolicy       string       `json:"edit_policy,omitempty"`
	CanEdit          bool         `json:"can_edit"`
	CanDelete        bool         `json:"can_delete"`
	CanManageSharing bool         `json:"can_manage_sharing"`
	Version          int          `json:"version"`
	CookbookIDs      []string     `json:"cookbook_ids,omitempty"`
	CreatedAt        string       `json:"created_at"`
	UpdatedAt        string       `json:"updated_at"`
	DeletedAt        string       `json:"deleted_at,omitempty"`
	Image            string       `json:"image,omitempty"`
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 2 && parts[0] == "account" && parts[1] == "creator-profile":
		h.creatorProfileSelf(w, r)
	case len(parts) == 2 && parts[0] == "creator-profiles":
		h.creatorProfileByID(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "creator-profiles" && parts[2] == "recipes":
		h.creatorProfileRecipes(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "creator-profiles" && parts[2] == "cookbooks":
		h.creatorProfileCookbooks(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "export":
		h.exportData(w, r)
	case len(parts) == 1 && parts[0] == "import":
		h.importData(w, r)
	case len(parts) == 2 && parts[0] == "library" && parts[1] == "bookmarks":
		h.recipeBookmarks(w, r, "")
	case len(parts) == 3 && parts[0] == "library" && parts[1] == "bookmarks":
		h.recipeBookmarks(w, r, parts[2])
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
	case len(parts) == 3 && parts[0] == "recipes" && parts[2] == "publication":
		h.recipePublication(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "recipes" && parts[2] == "restore":
		h.restoreTrashedRecipe(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "recipes":
		h.recipes(w, r)
	case len(parts) == 2 && parts[0] == "admin" && parts[1] == "invitations":
		h.invitations(w, r)
	case len(parts) == 2 && parts[0] == "admin" && parts[1] == "audit":
		h.auditEvents(w, r)
	case len(parts) == 2 && parts[0] == "admin" && parts[1] == "settings":
		h.instanceSettings(w, r)
	case len(parts) == 2 && parts[0] == "admin" && parts[1] == "users":
		h.adminUsers(w, r)
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "federation" && parts[2] == "peers":
		h.federationPeers(w, r)
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "federation" && parts[2] == "keys":
		h.federationPeerKeys(w, r)
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "federation" && parts[2] == "identity":
		h.federationIdentity(w, r)
	case len(parts) == 4 && parts[0] == "admin" && parts[1] == "federation" && parts[2] == "identity" && parts[3] == "rotation":
		h.federationIdentityRotation(w, r)
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "federation" && parts[2] == "outbox":
		h.federationOutbox(w, r)
	case len(parts) == 3 && parts[0] == "admin" && parts[1] == "users":
		h.adminUserByID(w, r, parts[2])
	case len(parts) >= 1 && parts[0] == "courses":
		h.courseRoutes(w, r, parts)
	case len(parts) >= 2 && parts[0] == "public" && parts[1] == "courses":
		h.publicCourseRoutes(w, r, parts[2:])
	case len(parts) == 3 && parts[0] == "recipes" && parts[2] == "fork":
		h.forkRecipe(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "recipes" && parts[2] == "transfer":
		h.transferRecipe(w, r, parts[1])
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
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		defer tx.Rollback()
		profile, err := ensureCreatorProfile(r.Context(), tx, input.OwnerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, input.OwnerID, input.Title, input.Description, input.Visibility, now, now); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := insertLocalAttribution(r.Context(), tx, "cookbook", id, profile.ID, "creator", "local_record_creation"); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := tx.Commit(); err != nil {
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
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	userID := authenticatedUser(r)
	rows, err := h.db.Query(`SELECT c.id,c.owner_id,c.title,c.description,c.visibility,c.created_at,c.updated_at FROM cookbooks c JOIN instance_settings s ON s.id=1 WHERE `+cookbookActorReadPredicate+` ORDER BY c.updated_at DESC,c.id LIMIT ? OFFSET ?`, userID, userID, limit, offset)
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
	if err := rows.Err(); err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	writeJSON(w, 200, items)
}

func (h apiHandler) cookbookByID(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodGet {
		var c cookbook
		uid := authenticatedUser(r)
		err := h.db.QueryRow(`SELECT c.id,c.owner_id,c.title,c.description,c.visibility,c.created_at,c.updated_at FROM cookbooks c JOIN instance_settings s ON s.id=1 WHERE c.id=? AND `+cookbookActorReadPredicate, id, uid, uid).Scan(&c.ID, &c.OwnerID, &c.Title, &c.Description, &c.Visibility, &c.CreatedAt, &c.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "not_found")
			return
		}
		if err != nil {
			writeError(w, 500, "storage_error")
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

const recipeRowsQuery = `SELECT r.id,r.owner_id,r.creator_name,r.source_recipe_id,r.title,r.description,r.ingredients_json,r.steps_json,r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,s.public_instance,r.image_path
	FROM recipes r JOIN instance_settings s ON s.id=1
	WHERE r.deleted_at IS NULL
	AND ` + recipeActorReadPredicate + `
	AND (?='' OR instr(lower(r.title||' '||r.description||' '||r.tags_json),lower(?))>0)
	AND (?='' OR EXISTS(SELECT 1 FROM cookbooks scope_book JOIN instance_settings scope_settings ON scope_settings.id=1 WHERE scope_book.id=? AND (scope_book.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships scope_member WHERE scope_member.cookbook_id=scope_book.id AND scope_member.user_id=?) OR (scope_settings.public_instance=1 AND scope_book.visibility='public'))))
	AND (?='' OR EXISTS(SELECT 1 FROM cookbook_recipes scope WHERE scope.recipe_id=r.id AND scope.cookbook_id=?))
	AND (?='' OR instr(lower(r.tags_json),lower(?))>0)
	ORDER BY `

func (h apiHandler) recipes(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var input recipeCreateRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		in := input.recipeInput
		if err := in.validate(); err != nil || validateCreatorAttributionInput(input.CreatorAttribution) != nil {
			writeError(w, 400, "invalid_recipe")
			return
		}
		if in.OwnerID = authenticatedUser(r); in.OwnerID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		initial, code := validateInitialPublication(initialRecipePublicationInput{}, input.InitialPublication != nil)
		if input.InitialPublication != nil {
			initial, code = validateInitialPublication(*input.InitialPublication, true)
		}
		if code != "" {
			writePublicationError(w, code)
			return
		}
		if in.Visibility == visibilityPublic && !input.ConfirmPublicAccess {
			writeError(w, http.StatusConflict, "publication_confirmation_required")
			return
		}
		var creator string
		if err := h.db.QueryRow(`SELECT display_name FROM users WHERE id=?`, in.OwnerID).Scan(&creator); err != nil {
			writeError(w, 400, "invalid_owner")
			return
		}
		if input.CreatorAttribution != nil {
			switch input.CreatorAttribution.Kind {
			case "local_profile", "unknown":
				creator = ""
			case "name_claim":
				creator = input.CreatorAttribution.Name
			}
		}
		id := newID()
		edit := in.EditPolicy
		if edit == "" {
			edit = "owner_only"
		}
		ingredientsJSON, _ := json.Marshal(in.Ingredients)
		stepsJSON, _ := json.Marshal(in.Steps)
		tagsJSON, _ := json.Marshal(in.Tags)
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err == nil {
			if code := validateRecipeCreationGates(r.Context(), tx, in.Visibility, input.ConfirmPublicAccess, initial); code != "" {
				_ = tx.Rollback()
				writePublicationError(w, code)
				return
			}
		}
		now := time.Now().UTC().Format(fixedPolicyTimeLayout)
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
		if err = insertInitialRecipePublicationPolicy(r.Context(), tx, id, in.OwnerID, initial, now); err != nil {
			_ = tx.Rollback()
			writePublicationError(w, "storage_error")
			return
		}
		if err = replaceCookbookRefs(tx, id, in.OwnerID, in.cookbookIDs()); err != nil {
			_ = tx.Rollback()
			writeError(w, 403, "cookbook_access_denied")
			return
		}
		if err = insertRecipeRevision(tx, id, 1, in.OwnerID, in.Title, in.Description, ingredientsJSON, stepsJSON, in.Servings, in.PrepTime, in.CookTime, tagsJSON, in.Visibility, ""); err != nil {
			_ = tx.Rollback()
			writeError(w, 500, "storage_error")
			return
		}
		if input.CreatorAttribution != nil && input.CreatorAttribution.Kind == "local_profile" {
			profile, profileErr := ensureCreatorProfile(r.Context(), tx, in.OwnerID)
			if profileErr != nil || insertLocalAttribution(r.Context(), tx, "recipe", id, profile.ID, "creator", "self_asserted_at_creation") != nil {
				_ = tx.Rollback()
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
		}
		if err = enqueueFederationChange(tx, id); err != nil {
			_ = tx.Rollback()
			writeError(w, 503, "federation_queue_unavailable")
			return
		}
		if err = insertAuditEvent(tx, in.OwnerID, "recipe.publication.initialize", "recipes", id, http.StatusOK); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err = tx.Commit(); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		markTransactionalAuditSuccess(r)
		h.writeRecipeDetail(w, r.Context(), ContentActor{AccountID: in.OwnerID}, id)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed")
		return
	}
	uid, q := authenticatedUser(r), strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	cookbookID := strings.TrimSpace(r.URL.Query().Get("cookbook_id"))
	if len(cookbookID) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_cookbook")
		return
	}
	if cookbookID != "" {
		var owner string
		var visibilityValue visibility
		var public int
		if err := h.db.QueryRow(`SELECT c.owner_id,c.visibility,s.public_instance FROM cookbooks c JOIN instance_settings s ON s.id=1 WHERE c.id=?`, cookbookID).Scan(&owner, &visibilityValue, &public); err != nil {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		var member role
		_ = h.db.QueryRow(`SELECT role FROM cookbook_memberships WHERE cookbook_id=? AND user_id=?`, cookbookID, uid).Scan(&member)
		if !canReadResource(uid, access{instancePublic: public == 1, visibility: visibilityValue, ownerID: owner, memberRole: member}) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
	}
	limit, offset, err := listPage(r)
	order, tagJSON, filterErr := recipeListOptions(r)
	if err != nil || len(q) > 200 || filterErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination_or_query")
		return
	}
	rows, err := h.db.Query(recipeRowsQuery+order+` LIMIT ? OFFSET ?`, uid, uid, q, q, cookbookID, cookbookID, uid, uid, cookbookID, cookbookID, tagJSON, tagJSON, limit, offset)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	items := []recipe{}
	publicFlags := []bool{}
	for rows.Next() {
		it, public := recipe{}, 0
		var ingredients, steps, tags string
		var imagePath string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.SourceRecipeID, &it.Title, &it.Description, &ingredients, &steps, &it.Servings, &it.PrepTime, &it.CookTime, &tags, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &public, &imagePath); err != nil {
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
		if imagePath != "" {
			items[len(items)-1].Image = "/api/recipes/" + it.ID + "/image"
		}
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
		member := recipeMemberRole(h.db, it.ID, uid)
		if !canReadResource(uid, access{instancePublic: publicFlags[index], visibility: it.Visibility, ownerID: it.OwnerID, memberRole: member}) {
			continue
		}
		if !hasLocalRecipeRead(uid, it.OwnerID, member) {
			it.SourceRecipeID = ""
		}
		it.CookbookIDs = h.recipeCookbooks(it.ID, uid)
		it.CanEdit = canEditRecipe(uid, access{ownerID: it.OwnerID, memberRole: member}, it.EditPolicy)
		it.CanDelete = uid != "" && uid == it.OwnerID
		it.CanManageSharing = canManageRecipeSharing(uid, it.OwnerID)
		visibleItems = append(visibleItems, projectRecipeForActor(it, uid))
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
	CookbookIDs *[]string    `json:"cookbook_ids"`
	Version     int          `json:"version"`
}

func (in recipeInput) cookbookIDs() []string {
	if in.CookbookIDs == nil {
		return nil
	}
	return *in.CookbookIDs
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
	if len(in.Ingredients) > 200 || len(in.Steps) > 200 || len(in.cookbookIDs()) > 50 {
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
		tx, err := h.db.Begin()
		if err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		record, err := readRecipePublicationRecord(r.Context(), tx, id)
		if errors.Is(err, sql.ErrNoRows) || (record.OwnerID != "" && (record.Deleted || record.OwnerID != uid)) {
			_ = tx.Rollback()
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		result, err := tx.Exec(`UPDATE recipes SET deleted_at=?,updated_at=? WHERE id=? AND owner_id=? AND deleted_at IS NULL`, timestamp(), timestamp(), id, uid)
		if err != nil {
			_ = tx.Rollback()
			writeError(w, 500, "storage_error")
			return
		}
		count, err := result.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if count != 1 {
			_ = tx.Rollback()
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err := advanceLifecyclePolicy(r.Context(), tx, record, uid); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := insertAuditEvent(tx, uid, "recipe.trash", "recipes", id, http.StatusOK); err != nil {
			_ = tx.Rollback()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := enqueueFederationChange(tx, id); err != nil {
			_ = tx.Rollback()
			writeError(w, 503, "federation_queue_unavailable")
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, 500, "storage_error")
			return
		}
		markTransactionalAuditSuccess(r)
		writeJSON(w, http.StatusOK, map[string]string{"status": "recipe_trashed"})
		return
	}
	if r.Method == http.MethodGet {
		h.writeRecipeDetail(w, r.Context(), ContentActor{AccountID: authenticatedUser(r)}, id)
		return
	}
	if r.Method != http.MethodPut {
		writeError(w, 405, "method_not_allowed")
		return
	}
	var input recipeUpdateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	in := input.recipeInput
	if err := in.validate(); err != nil {
		writeError(w, 400, "invalid_recipe")
		return
	}
	in.UserID = authenticatedUser(r)
	if in.UserID == "" || in.Version < 1 {
		writeError(w, 400, "invalid_update")
		return
	}
	var owner string
	var version int
	var edit string
	var currentVisibility visibility
	if err := h.db.QueryRow(`SELECT owner_id,version,edit_policy,visibility FROM recipes WHERE id=? AND deleted_at IS NULL`, id).Scan(&owner, &version, &edit, &currentVisibility); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	memberRole := recipeMemberRole(h.db, id, in.UserID)
	if !canEditResource(in.UserID, access{ownerID: owner, memberRole: memberRole}) || (in.UserID != owner && (edit != "cookbook_editors" || memberRole != roleEditor)) {
		writeError(w, 403, "forbidden")
		return
	}
	if in.UserID != owner && in.Visibility != currentVisibility {
		writeError(w, 403, "visibility_owner_only")
		return
	}
	if version != in.Version {
		writeError(w, 409, "version_conflict")
		return
	}
	ingredientsJSON, _ := json.Marshal(in.Ingredients)
	stepsJSON, _ := json.Marshal(in.Steps)
	tagsJSON, _ := json.Marshal(in.Tags)
	visibilityChanged := false
	_, err := commitRecipeMutation(h.db, recipeMutation{
		RecipeID: id, ActorID: in.UserID, ExpectedVersion: in.Version,
		Snapshot: recipeMutationSnapshot{
			Title: in.Title, Description: in.Description, IngredientsJSON: ingredientsJSON,
			StepsJSON: stepsJSON, Servings: in.Servings, PrepTime: in.PrepTime,
			CookTime: in.CookTime, TagsJSON: tagsJSON, Visibility: in.Visibility,
		},
		CookbookIDs: in.cookbookIDs(), ReplaceCookbookRefs: in.CookbookIDs != nil || in.UserID == owner,
		ConfirmPublicAccess: input.ConfirmPublicAccess, VisibilityChanged: &visibilityChanged,
	})
	if errors.Is(err, errRecipeMutationConflict) {
		writeError(w, http.StatusConflict, "version_conflict")
		return
	}
	if errors.Is(err, errRecipeMutationNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if errors.Is(err, errRecipeMutationVisibility) {
		writeError(w, http.StatusForbidden, "visibility_owner_only")
		return
	}
	if errors.Is(err, errRecipeMutationConfirmation) {
		writeError(w, http.StatusConflict, "publication_confirmation_required")
		return
	}
	if errors.Is(err, errRecipeMutationInstanceGate) {
		writeError(w, http.StatusForbidden, "instance_private")
		return
	}
	if errors.Is(err, errRecipeMutationDenied) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if errors.Is(err, errRecipeMutationCookbookRefs) {
		writeError(w, http.StatusForbidden, "cookbook_access_denied")
		return
	}
	if errors.Is(err, errRecipeMutationFederation) {
		writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if visibilityChanged {
		markTransactionalAuditSuccess(r)
	}
	h.writeRecipeDetail(w, r.Context(), ContentActor{AccountID: in.UserID}, id)
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
	rows, err := h.db.Query(`SELECT id,owner_id,creator_name,source_recipe_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,edit_policy,version,created_at,updated_at,deleted_at,source_recipe_id FROM recipes WHERE owner_id=? AND deleted_at IS NOT NULL ORDER BY deleted_at DESC,id LIMIT ? OFFSET ?`, uid, limit, offset)
	if err != nil {
		writeError(w, 500, "storage_error")
		return
	}
	defer rows.Close()
	items := []recipe{}
	for rows.Next() {
		it := recipe{}
		var ingredients, steps, tags string
		if err := rows.Scan(&it.ID, &it.OwnerID, &it.CreatorName, &it.SourceRecipeID, &it.Title, &it.Description, &ingredients, &steps, &it.Servings, &it.PrepTime, &it.CookTime, &tags, &it.Visibility, &it.EditPolicy, &it.Version, &it.CreatedAt, &it.UpdatedAt, &it.DeletedAt, &it.SourceRecipeID); err != nil {
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
	tx, err := h.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	record, err := readRecipePublicationRecord(r.Context(), tx, id)
	if errors.Is(err, sql.ErrNoRows) || (record.OwnerID != "" && (!record.Deleted || record.OwnerID != uid)) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := advanceLifecyclePolicy(r.Context(), tx, record, uid); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	result, err := tx.Exec(`UPDATE recipes SET deleted_at=NULL,updated_at=? WHERE id=? AND owner_id=? AND deleted_at IS NOT NULL`, timestamp(), id, uid)
	if err != nil {
		_ = tx.Rollback()
		writeError(w, 500, "storage_error")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if count != 1 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err := insertAuditEvent(tx, uid, "recipe.restore", "recipes", id, http.StatusOK); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := enqueueFederationChange(tx, id); err != nil {
		writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	markTransactionalAuditSuccess(r)
	h.writeRecipeDetail(w, r.Context(), ContentActor{AccountID: uid}, id)
}

func (h apiHandler) recipeRevisions(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	uid := authenticatedUser(r)
	var owner, editPolicy string
	if err := h.db.QueryRow(`SELECT owner_id,edit_policy FROM recipes WHERE id=? AND deleted_at IS NULL`, id).Scan(&owner, &editPolicy); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	member := recipeMemberRole(h.db, id, uid)
	if !canEditRecipe(uid, access{ownerID: owner, memberRole: member}, editPolicy) {
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
	var owner, editPolicy string
	if err := h.db.QueryRow(`SELECT owner_id,edit_policy FROM recipes WHERE id=? AND deleted_at IS NULL`, recipeID).Scan(&owner, &editPolicy); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	member := recipeMemberRole(h.db, recipeID, uid)
	if !canEditRecipe(uid, access{ownerID: owner, memberRole: member}, editPolicy) {
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
	var input revisionRestoreInput
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
	memberRole := recipeMemberRole(h.db, recipeID, userID)
	if !canEditResource(userID, access{ownerID: owner, memberRole: memberRole}) || (userID != owner && (editPolicy != "cookbook_editors" || memberRole != roleEditor)) {
		writeError(w, 403, "forbidden")
		return
	}
	if input.ExpectedVersion != currentVersion {
		writeError(w, 409, "version_conflict")
		return
	}
	var title, description, ingredients, steps, tags, restoredImage string
	var servings, prepTime, cookTime int
	var restoredVisibility visibility
	if err := h.db.QueryRow(`SELECT title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,visibility,image_path FROM recipe_revisions WHERE recipe_id=? AND version=?`, recipeID, revisionVersion).Scan(&title, &description, &ingredients, &steps, &servings, &prepTime, &cookTime, &tags, &restoredVisibility, &restoredImage); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	visibilityChanged := false
	_, err = commitRecipeMutation(h.db, recipeMutation{
		RecipeID: recipeID, ActorID: userID, ExpectedVersion: input.ExpectedVersion,
		Snapshot: recipeMutationSnapshot{
			Title: title, Description: description, IngredientsJSON: []byte(ingredients),
			StepsJSON: []byte(steps), Servings: servings, PrepTime: prepTime,
			CookTime: cookTime, TagsJSON: []byte(tags), Visibility: restoredVisibility,
		},
		ImagePath: &restoredImage, ConfirmPublicAccess: input.ConfirmPublicAccess,
		VisibilityChanged: &visibilityChanged,
	})
	if errors.Is(err, errRecipeMutationConflict) {
		writeError(w, http.StatusConflict, "version_conflict")
		return
	}
	if errors.Is(err, errRecipeMutationNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if errors.Is(err, errRecipeMutationVisibility) {
		writeError(w, http.StatusForbidden, "visibility_owner_only")
		return
	}
	if errors.Is(err, errRecipeMutationConfirmation) {
		writeError(w, http.StatusConflict, "publication_confirmation_required")
		return
	}
	if errors.Is(err, errRecipeMutationInstanceGate) {
		writeError(w, http.StatusForbidden, "instance_private")
		return
	}
	if errors.Is(err, errRecipeMutationDenied) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if errors.Is(err, errRecipeMutationFederation) {
		writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if visibilityChanged {
		markTransactionalAuditSuccess(r)
	}
	h.writeRecipeDetail(w, r.Context(), ContentActor{AccountID: userID}, recipeID)
}

func (h apiHandler) writeRecipeDetail(w http.ResponseWriter, ctx context.Context, actor ContentActor, recipeID string) {
	item, err := readRecipeDetail(ctx, h.db, actor, recipeID)
	if errors.Is(err, errRecipeDetailNotFound) || errors.Is(err, errRecipeDetailDenied) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func insertRecipeRevision(tx *sql.Tx, recipeID string, version int, editorID, title, description string, ingredients, steps []byte, servings, prepTime, cookTime int, tags []byte, visibilityValue visibility, imagePath string) error {
	_, err := tx.Exec(`INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,servings,prep_time_minutes,cook_time_minutes,tags_json,image_path,visibility,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, newID(), recipeID, version, editorID, title, description, string(ingredients), string(steps), servings, prepTime, cookTime, string(tags), imagePath, visibilityValue, timestamp())
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
	var recipeOwnerID string
	if err := tx.QueryRow(`SELECT owner_id FROM recipes WHERE id=? AND deleted_at IS NULL`, recipeID).Scan(&recipeOwnerID); err != nil {
		return err
	}
	if !canManageRecipeSharing(userID, recipeOwnerID) {
		visibleIDs, err := visibleRecipeCookbookIDsTx(tx, recipeID, userID)
		if err != nil {
			return err
		}
		if !sameCookbookIDs(ids, visibleIDs) {
			return errors.New("recipe sharing requires owner permission")
		}
		// Non-owners may echo the references visible to them for compatibility,
		// but must not replace the complete link set: other links may be hidden.
		return nil
	}
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

func visibleRecipeCookbookIDsTx(tx *sql.Tx, recipeID, userID string) ([]string, error) {
	rows, err := tx.Query(`SELECT cr.cookbook_id FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id JOIN instance_settings s ON s.id=1 WHERE cr.recipe_id=? AND (c.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships m WHERE m.cookbook_id=c.id AND m.user_id=?) OR (s.public_instance=1 AND c.visibility='public')) ORDER BY cr.cookbook_id`, recipeID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func sameCookbookIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, id := range left {
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	for _, id := range right {
		if _, exists := seen[id]; !exists {
			return false
		}
		delete(seen, id)
	}
	return len(seen) == 0
}
func (h apiHandler) recipeCookbooks(id, userID string) []string {
	rows, err := h.db.Query(`SELECT cr.cookbook_id FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id JOIN instance_settings s ON s.id=1 WHERE cr.recipe_id=? AND (c.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships m WHERE m.cookbook_id=c.id AND m.user_id=?) OR (s.public_instance=1 AND c.visibility='public')) ORDER BY cr.cookbook_id`, id, userID, userID)
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
