package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
)

type instanceSettings struct {
	PublicInstance     bool   `json:"public_instance"`
	CoursesEnabled     bool   `json:"courses_enabled"`
	RegistrationPolicy string `json:"registration_policy"`
	RevisionRetention  int    `json:"revision_retention_days"`
	AuditRetention     int    `json:"audit_retention_days"`
	PublicCookbooks    int    `json:"public_cookbooks"`
	PublicRecipes      int    `json:"public_recipes"`
	PublicCourses      int    `json:"public_courses"`
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
			CoursesEnabled     *bool   `json:"courses_enabled"`
			RegistrationPolicy *string `json:"registration_policy"`
			RevisionRetention  *int    `json:"revision_retention_days"`
			AuditRetention     *int    `json:"audit_retention_days"`
			ConfirmPublication bool    `json:"confirm_publication"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.PublicInstance == nil && input.CoursesEnabled == nil && input.RegistrationPolicy == nil && input.RevisionRetention == nil && input.AuditRetention == nil {
			writeError(w, http.StatusBadRequest, "empty_settings_update")
			return
		}
		if input.RegistrationPolicy != nil && *input.RegistrationPolicy != "open" && *input.RegistrationPolicy != "invite_only" && *input.RegistrationPolicy != "disabled" {
			writeError(w, http.StatusBadRequest, "invalid_registration_policy")
			return
		}
		if input.RevisionRetention != nil && !validRetentionDays(*input.RevisionRetention) {
			writeError(w, http.StatusBadRequest, "invalid_revision_retention")
			return
		}
		if input.AuditRetention != nil && !validRetentionDays(*input.AuditRetention) {
			writeError(w, http.StatusBadRequest, "invalid_audit_retention")
			return
		}
		settings, err := updateInstanceSettings(h.db, input.PublicInstance, input.CoursesEnabled, input.RegistrationPolicy, input.RevisionRetention, input.AuditRetention, input.ConfirmPublication)
		if errors.Is(err, errPublicationConfirmationRequired) {
			writeError(w, http.StatusConflict, "publication_confirmation_required")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if input.RevisionRetention != nil || input.AuditRetention != nil {
			if err := purgeExpiredHistory(h.db); err != nil {
				log.Printf("history retention cleanup failed after settings update: %v", err)
			}
			if err := cleanupOrphanedMedia(h.db, h.mediaDir); err != nil {
				log.Printf("media cleanup failed after settings update: %v", err)
			}
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
	var courses int
	if err := db.QueryRow(`SELECT public_instance,courses_enabled,registration_policy,revision_retention_days,audit_retention_days FROM instance_settings WHERE id=1`).Scan(&public, &courses, &current.RegistrationPolicy, &current.RevisionRetention, &current.AuditRetention); err != nil {
		return current, err
	}
	current.PublicInstance = public == 1
	current.CoursesEnabled = courses == 1
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE visibility='public'`).Scan(&current.PublicCookbooks); err != nil {
		return current, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes WHERE visibility='public' AND deleted_at IS NULL`).Scan(&current.PublicRecipes); err != nil {
		return current, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM courses WHERE visibility='public'`).Scan(&current.PublicCourses); err != nil {
		return current, err
	}
	return current, nil
}

var errPublicationConfirmationRequired = errors.New("public content requires administrator confirmation")

func updateInstanceSettings(db *sql.DB, publicInstance, coursesEnabled *bool, registrationPolicy *string, revisionRetention, auditRetention *int, confirmPublication bool) (instanceSettings, error) {
	tx, err := db.Begin()
	if err != nil {
		return instanceSettings{}, err
	}
	defer tx.Rollback()

	var wasPublic int
	var courses int
	var currentPolicy string
	var currentRevisionRetention, currentAuditRetention int
	if err := tx.QueryRow(`SELECT public_instance,courses_enabled,registration_policy,revision_retention_days,audit_retention_days FROM instance_settings WHERE id=1`).Scan(&wasPublic, &courses, &currentPolicy, &currentRevisionRetention, &currentAuditRetention); err != nil {
		return instanceSettings{}, err
	}
	wasCoursesEnabled := courses == 1
	public := wasPublic == 1
	if coursesEnabled != nil {
		if *coursesEnabled {
			courses = 1
		} else {
			courses = 0
		}
	}
	if publicInstance != nil {
		public = *publicInstance
	}
	if wasPublic == 0 && public {
		var publicResources int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM cookbooks WHERE visibility='public') + (SELECT COUNT(*) FROM recipes WHERE visibility='public' AND deleted_at IS NULL) + (SELECT COUNT(*) FROM courses WHERE visibility='public')`).Scan(&publicResources); err != nil {
			return instanceSettings{}, err
		}
		if publicResources > 0 && !confirmPublication {
			return instanceSettings{}, errPublicationConfirmationRequired
		}
	}
	if !wasCoursesEnabled && courses == 1 {
		var publicCourses int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM courses WHERE visibility='public'`).Scan(&publicCourses); err != nil {
			return instanceSettings{}, err
		}
		if publicCourses > 0 && public && !confirmPublication {
			return instanceSettings{}, errPublicationConfirmationRequired
		}
	}
	if registrationPolicy != nil {
		currentPolicy = *registrationPolicy
	}
	if revisionRetention != nil {
		currentRevisionRetention = *revisionRetention
	}
	if auditRetention != nil {
		currentAuditRetention = *auditRetention
	}
	publicValue := 0
	if public {
		publicValue = 1
	}
	if _, err := tx.Exec(`UPDATE instance_settings SET public_instance=?,courses_enabled=?,registration_policy=?,registration_policy_configured=1,revision_retention_days=?,audit_retention_days=? WHERE id=1`, publicValue, courses, currentPolicy, currentRevisionRetention, currentAuditRetention); err != nil {
		return instanceSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return instanceSettings{}, err
	}
	return readInstanceSettings(db)
}

func validRetentionDays(days int) bool {
	switch days {
	case 0, 7, 30, 90, 365:
		return true
	default:
		return false
	}
}
