package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
)

func (h apiHandler) libraryRemoteRecipeDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	userID := authenticatedUser(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_remote_recipe_target")
		return
	}
	values, ok := query["remote_object_uri"]
	if len(query) != 1 || !ok || len(values) != 1 || values[0] == "" {
		writeError(w, http.StatusBadRequest, "invalid_remote_recipe_target")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()

	item, err := readCachedPublicRemoteRecipe(r.Context(), tx, ContentActor{AccountID: userID}, values[0])
	switch {
	case errors.Is(err, errRemoteRecipeNotFound):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, errRemoteRecipeInvalidURI):
		writeError(w, http.StatusBadRequest, "invalid_remote_recipe_target")
		return
	case err != nil:
		// The canonical reader groups object/source mismatches with other
		// metadata validation failures. On that error only, distinguish a stale
		// source binding from corrupt metadata using the same exact row and
		// transaction snapshot.
		if err.Error() == "invalid cached remote recipe metadata" {
			origin, recipeID, parseErr := parseFederationRecipeObjectURI(values[0])
			if parseErr != nil {
				writeError(w, http.StatusBadRequest, "invalid_remote_recipe_target")
				return
			}
			var sourceOrigin, sourceRecipeID string
			metadataErr := tx.QueryRowContext(r.Context(), `SELECT source_origin,source_recipe_id
				FROM federation_remote_recipes WHERE object_id=? AND deleted=0`, values[0]).Scan(&sourceOrigin, &sourceRecipeID)
			if errors.Is(metadataErr, sql.ErrNoRows) || (metadataErr == nil && (sourceOrigin != origin || sourceRecipeID != recipeID)) {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
			if metadataErr != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, item)
}
