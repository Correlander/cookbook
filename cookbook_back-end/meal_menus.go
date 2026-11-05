package main

import (
	"errors"
	"net/http"
	"strconv"

	"Cookbook_Database_Backend/internal/menus"
)

type menuCreateInput struct {
	Title              string                   `json:"title"`
	Note               string                   `json:"note"`
	CreatorAttribution *creatorAttributionInput `json:"creator_attribution,omitempty"`
}

type menuUpdateInput struct {
	Title   string `json:"title"`
	Note    string `json:"note"`
	Version int    `json:"version"`
}

type menuEntryInput struct {
	RecipeID    string `json:"recipe_id"`
	Label       string `json:"label"`
	Note        string `json:"note"`
	ServingNote string `json:"serving_note"`
}

type menuOrderInput struct {
	EntryIDs []string `json:"entry_ids"`
	Version  int      `json:"version"`
}

func (h apiHandler) mealMenus(w http.ResponseWriter, r *http.Request, parts []string) {
	w.Header().Set("Cache-Control", "private, no-store")
	owner := authenticatedUser(r)
	if owner == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	u := mealMenuUsecase{db: h.db}
	switch {
	case len(parts) == 1:
		switch r.Method {
		case http.MethodGet:
			limit, offset, err := listPage(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_pagination")
				return
			}
			page, err := u.list(r.Context(), owner, limit, offset)
			if err != nil {
				writeMenuError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, page)
		case http.MethodPost:
			var input menuCreateInput
			if !decodeJSON(w, r, &input) {
				return
			}
			if validateCreatorAttributionInput(input.CreatorAttribution) != nil {
				writeError(w, http.StatusBadRequest, "invalid_menu")
				return
			}
			item, err := u.create(r.Context(), owner, input)
			if err != nil {
				writeMenuError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	case len(parts) == 2:
		id := parts[1]
		switch r.Method {
		case http.MethodGet:
			item, err := u.read(r.Context(), owner, id)
			if err != nil {
				writeMenuError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodPut:
			var input menuUpdateInput
			if !decodeJSON(w, r, &input) {
				return
			}
			if input.Version < 1 {
				writeError(w, http.StatusBadRequest, "invalid_menu")
				return
			}
			item, err := u.update(r.Context(), owner, id, input.Version, input)
			if err != nil {
				writeMenuError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodDelete:
			version, err := menuVersionQuery(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_menu")
				return
			}
			if err := u.delete(r.Context(), owner, id, version); err != nil {
				writeMenuError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "menu_deleted"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	case len(parts) == 3 && parts[2] == "entries":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var input struct {
			menuEntryInput
			Version int `json:"version"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Version < 1 {
			writeError(w, http.StatusBadRequest, "invalid_menu")
			return
		}
		item, err := u.addEntry(r.Context(), owner, parts[1], input.Version, input.menuEntryInput)
		if err != nil {
			writeMenuError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, item)
	case len(parts) == 4 && parts[2] == "entries" && parts[3] == "order":
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var input menuOrderInput
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Version < 1 {
			writeError(w, http.StatusBadRequest, "invalid_menu")
			return
		}
		item, err := u.reorder(r.Context(), owner, parts[1], input.Version, input.EntryIDs)
		if err != nil {
			writeMenuError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	case len(parts) == 4 && parts[2] == "entries":
		switch r.Method {
		case http.MethodPut:
			var input struct {
				menuEntryInput
				Version int `json:"version"`
			}
			if !decodeJSON(w, r, &input) {
				return
			}
			if input.Version < 1 {
				writeError(w, http.StatusBadRequest, "invalid_menu")
				return
			}
			item, err := u.updateEntry(r.Context(), owner, parts[1], parts[3], input.Version, input.menuEntryInput)
			if err != nil {
				writeMenuError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodDelete:
			version, err := menuVersionQuery(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_menu")
				return
			}
			item, err := u.removeEntry(r.Context(), owner, parts[1], parts[3], version)
			if err != nil {
				writeMenuError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

func (h apiHandler) libraryMenus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	h.mealMenus(w, r, []string{"menus"})
}

func menuVersionQuery(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("version")
	version, err := strconv.Atoi(raw)
	if raw == "" || err != nil || version < 1 {
		return 0, errors.New("invalid version")
	}
	return version, nil
}

func writeMenuError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, menus.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_menu")
	case errors.Is(err, menus.ErrTarget):
		writeError(w, http.StatusUnprocessableEntity, "recipe_unavailable")
	case errors.Is(err, menus.ErrNotFound), errors.Is(err, menus.ErrEntry):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, menus.ErrConflict):
		writeError(w, http.StatusConflict, "version_conflict")
	case errors.Is(err, menus.ErrLimit):
		writeError(w, http.StatusConflict, "menu_limit_reached")
	default:
		writeError(w, http.StatusInternalServerError, "storage_error")
	}
}
