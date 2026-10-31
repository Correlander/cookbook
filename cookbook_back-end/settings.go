package main

import (
	"database/sql"
	"errors"
	"net/http"
)

type instanceSettings struct {
	PublicInstance     bool   `json:"public_instance"`
	RegistrationPolicy string `json:"registration_policy"`
	PublicCookbooks    int    `json:"public_cookbooks"`
	PublicRecipes      int    `json:"public_recipes"`
}

func (h apiHandler) instanceSettings(w http.ResponseWriter, r *http.Request) {
	if !isAdministrator(h.db, authenticatedUser(r)) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := readInstanceSettings(h.db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPatch:
		var input struct {
			PublicInstance     *bool   `json:"public_instance"`
			RegistrationPolicy *string `json:"registration_policy"`
			ConfirmPublication bool    `json:"confirm_publication"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.PublicInstance == nil && input.RegistrationPolicy == nil {
			writeError(w, http.StatusBadRequest, "empty_settings_update")
			return
		}
		if input.RegistrationPolicy != nil && *input.RegistrationPolicy != "open" && *input.RegistrationPolicy != "invite_only" && *input.RegistrationPolicy != "disabled" {
			writeError(w, http.StatusBadRequest, "invalid_registration_policy")
			return
		}
		settings, err := updateInstanceSettings(h.db, input.PublicInstance, input.RegistrationPolicy, input.ConfirmPublication)
		if errors.Is(err, errPublicationConfirmationRequired) {
			writeError(w, http.StatusConflict, "publication_confirmation_required")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, settings)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func isAdministrator(db *sql.DB, userID string) bool {
	if userID == "" {
		return false
	}
	var admin int
	return db.QueryRow(`SELECT is_admin FROM users WHERE id=?`, userID).Scan(&admin) == nil && admin == 1
}

func readInstanceSettings(db *sql.DB) (instanceSettings, error) {
	var current instanceSettings
	var public int
	if err := db.QueryRow(`SELECT public_instance,registration_policy FROM instance_settings WHERE id=1`).Scan(&public, &current.RegistrationPolicy); err != nil {
		return current, err
	}
	current.PublicInstance = public == 1
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE visibility='public'`).Scan(&current.PublicCookbooks); err != nil {
		return current, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE visibility='public' AND deleted_at IS NULL`).Scan(&current.PublicRecipes); err != nil {
		return current, err
	}
	return current, nil
}

var errPublicationConfirmationRequired = errors.New("public content requires administrator confirmation")

func updateInstanceSettings(db *sql.DB, publicInstance *bool, registrationPolicy *string, confirmPublication bool) (instanceSettings, error) {
	tx, err := db.Begin()
	if err != nil {
		return instanceSettings{}, err
	}
	defer tx.Rollback()

	var wasPublic int
	var currentPolicy string
	if err := tx.QueryRow(`SELECT public_instance,registration_policy FROM instance_settings WHERE id=1`).Scan(&wasPublic, &currentPolicy); err != nil {
		return instanceSettings{}, err
	}
	public := wasPublic == 1
	if publicInstance != nil {
		public = *publicInstance
	}
	if wasPublic == 0 && public {
		var publicResources int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM cookbooks WHERE visibility='public') + (SELECT COUNT(*) FROM recipes WHERE visibility='public' AND deleted_at IS NULL)`).Scan(&publicResources); err != nil {
			return instanceSettings{}, err
		}
		if publicResources > 0 && !confirmPublication {
			return instanceSettings{}, errPublicationConfirmationRequired
		}
	}
	if registrationPolicy != nil {
		currentPolicy = *registrationPolicy
	}
	publicValue := 0
	if public {
		publicValue = 1
	}
	if _, err := tx.Exec(`UPDATE instance_settings SET public_instance=?,registration_policy=?,registration_policy_configured=1 WHERE id=1`, publicValue, currentPolicy); err != nil {
		return instanceSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return instanceSettings{}, err
	}
	return readInstanceSettings(db)
}
