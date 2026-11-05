package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type instanceSettings struct {
	PublicInstance                   bool   `json:"public_instance"`
	CoursesEnabled                   bool   `json:"courses_enabled"`
	RegistrationPolicy               string `json:"registration_policy"`
	RevisionRetention                int    `json:"revision_retention_days"`
	AuditRetention                   int    `json:"audit_retention_days"`
	PrivateSnapshotStorageLimitBytes int64  `json:"private_snapshot_storage_limit_bytes"`
	AnonymousHomePreset              string `json:"anonymous_home_preset"`
	SiteName                         string `json:"site_name"`
	Palette                          string `json:"palette"`
	FederationEnabled                bool   `json:"federation_enabled"`
	FederationOrigin                 string `json:"federation_origin"`
	PublicCookbooks                  int    `json:"public_cookbooks"`
	PublicRecipes                    int    `json:"public_recipes"`
	PublicCourses                    int    `json:"public_courses"`
}

func (h apiHandler) instanceSettings(w http.ResponseWriter, r *http.Request) {
	canManage, err := canManageInstanceSettings(r.Context(), h.db, authenticatedUser(r))
	if errors.Is(err, ErrOwnerAccountNotFound) || (err == nil && !canManage) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
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
			PublicInstance                   *bool           `json:"public_instance"`
			CoursesEnabled                   *bool           `json:"courses_enabled"`
			RegistrationPolicy               *string         `json:"registration_policy"`
			RevisionRetention                *int            `json:"revision_retention_days"`
			AuditRetention                   *int            `json:"audit_retention_days"`
			FederationEnabled                *bool           `json:"federation_enabled"`
			PrivateSnapshotStorageLimitBytes *int64          `json:"private_snapshot_storage_limit_bytes"`
			AnonymousHomePreset              json.RawMessage `json:"anonymous_home_preset"`
			SiteName                         json.RawMessage `json:"site_name"`
			Palette                          json.RawMessage `json:"palette"`
			ConfirmPublication               bool            `json:"confirm_publication"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		var anonymousHomePreset *string
		if input.AnonymousHomePreset != nil {
			var value any
			if err := json.Unmarshal(input.AnonymousHomePreset, &value); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_anonymous_home_preset")
				return
			}
			preset, ok := value.(string)
			if !ok || !validAnonymousHomePreset(preset) {
				writeError(w, http.StatusBadRequest, "invalid_anonymous_home_preset")
				return
			}
			anonymousHomePreset = &preset
		}
		siteName, err := parseOptionalSiteName(input.SiteName)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_site_name")
			return
		}
		palette, err := parseOptionalPalette(input.Palette)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_palette")
			return
		}
		if input.PublicInstance == nil && input.CoursesEnabled == nil && input.RegistrationPolicy == nil && input.RevisionRetention == nil && input.AuditRetention == nil && input.FederationEnabled == nil && input.PrivateSnapshotStorageLimitBytes == nil && input.AnonymousHomePreset == nil && input.SiteName == nil && input.Palette == nil {
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
		if input.PrivateSnapshotStorageLimitBytes != nil && *input.PrivateSnapshotStorageLimitBytes <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_private_snapshot_storage_limit")
			return
		}
		if input.FederationEnabled != nil && *input.FederationEnabled {
			if _, err := loadOrCreateFederationSigningKey(h.federationKeyDir); err != nil {
				writeError(w, http.StatusConflict, "federation_identity_unavailable")
				return
			}
			var origin string
			if err := h.db.QueryRow(`SELECT federation_origin FROM instance_settings WHERE id=1`).Scan(&origin); err != nil || origin == "" {
				writeError(w, http.StatusConflict, "federation_origin_required")
				return
			}
			var enabledPeers int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM federation_peers WHERE enabled=1 AND EXISTS(SELECT 1 FROM federation_peer_keys k WHERE k.origin=federation_peers.origin AND k.status='active')`).Scan(&enabledPeers); err != nil || enabledPeers == 0 {
				writeError(w, http.StatusConflict, "enabled_peer_required")
				return
			}
		}
		settings, err := updateInstanceSettingsWithSnapshotLimitAndPreset(h.db, input.PublicInstance, input.CoursesEnabled, input.RegistrationPolicy, input.RevisionRetention, input.AuditRetention, input.ConfirmPublication, input.PrivateSnapshotStorageLimitBytes, input.FederationEnabled, anonymousHomePreset, siteName, palette)
		if errors.Is(err, errPublicationConfirmationRequired) {
			writeError(w, http.StatusConflict, "publication_confirmation_required")
			return
		}
		if writePublisherProfileError(w, err) {
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
	var federation int
	if err := db.QueryRow(`SELECT public_instance,courses_enabled,registration_policy,revision_retention_days,audit_retention_days,federation_enabled,federation_origin,private_snapshot_storage_limit_bytes,anonymous_home_preset,site_name,palette FROM instance_settings WHERE id=1`).Scan(&public, &courses, &current.RegistrationPolicy, &current.RevisionRetention, &current.AuditRetention, &federation, &current.FederationOrigin, &current.PrivateSnapshotStorageLimitBytes, &current.AnonymousHomePreset, &current.SiteName, &current.Palette); err != nil {
		return current, err
	}
	current.PublicInstance = public == 1
	current.CoursesEnabled = courses == 1
	current.FederationEnabled = federation == 1
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

func updateInstanceSettings(db *sql.DB, publicInstance, coursesEnabled *bool, registrationPolicy *string, revisionRetention, auditRetention *int, confirmPublication bool, federationSwitch ...*bool) (instanceSettings, error) {
	return updateInstanceSettingsWithSnapshotLimit(db, publicInstance, coursesEnabled, registrationPolicy, revisionRetention, auditRetention, confirmPublication, nil, federationSwitch...)
}

func updateInstanceSettingsWithSnapshotLimit(db *sql.DB, publicInstance, coursesEnabled *bool, registrationPolicy *string, revisionRetention, auditRetention *int, confirmPublication bool, privateSnapshotStorageLimitBytes *int64, federationSwitch ...*bool) (instanceSettings, error) {
	var federation *bool
	if len(federationSwitch) > 0 {
		federation = federationSwitch[0]
	}
	return updateInstanceSettingsWithSnapshotLimitAndPreset(db, publicInstance, coursesEnabled, registrationPolicy, revisionRetention, auditRetention, confirmPublication, privateSnapshotStorageLimitBytes, federation, nil, nil, nil)
}

func updateInstanceSettingsWithSnapshotLimitAndPreset(db *sql.DB, publicInstance, coursesEnabled *bool, registrationPolicy *string, revisionRetention, auditRetention *int, confirmPublication bool, privateSnapshotStorageLimitBytes *int64, federationSwitch *bool, anonymousHomePreset, siteName, palette *string) (instanceSettings, error) {
	if anonymousHomePreset != nil && !validAnonymousHomePreset(*anonymousHomePreset) {
		return instanceSettings{}, errors.New("invalid anonymous homepage preset")
	}
	if siteName != nil && !validSiteName(*siteName) {
		return instanceSettings{}, errors.New("invalid site name")
	}
	if palette != nil && !validPalette(*palette) {
		return instanceSettings{}, errors.New("invalid palette")
	}
	tx, err := db.Begin()
	if err != nil {
		return instanceSettings{}, err
	}
	defer tx.Rollback()

	var wasPublic int
	var courses int
	var registrationPolicyConfigured int
	var currentPolicy string
	var currentRevisionRetention, currentAuditRetention int
	var federation int
	var origin string
	var currentPrivateSnapshotStorageLimit int64
	var currentAnonymousHomePreset string
	var currentSiteName, currentPalette string
	if err := tx.QueryRow(`SELECT public_instance,courses_enabled,registration_policy,registration_policy_configured,revision_retention_days,audit_retention_days,federation_enabled,federation_origin,private_snapshot_storage_limit_bytes,anonymous_home_preset,site_name,palette FROM instance_settings WHERE id=1`).Scan(&wasPublic, &courses, &currentPolicy, &registrationPolicyConfigured, &currentRevisionRetention, &currentAuditRetention, &federation, &origin, &currentPrivateSnapshotStorageLimit, &currentAnonymousHomePreset, &currentSiteName, &currentPalette); err != nil {
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
	oldFederation := federation
	if federationSwitch != nil {
		if *federationSwitch {
			if origin == "" {
				return instanceSettings{}, errors.New("federation origin is required")
			}
			var enabledPeers int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM federation_peers WHERE enabled=1 AND EXISTS(SELECT 1 FROM federation_peer_keys k WHERE k.origin=federation_peers.origin AND k.status='active')`).Scan(&enabledPeers); err != nil {
				return instanceSettings{}, err
			} else if enabledPeers == 0 {
				return instanceSettings{}, errors.New("at least one enabled federation peer is required")
			}
			federation = 1
		} else {
			federation = 0
		}
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
	if public {
		ownerQueries := make([]string, 0, 3)
		if wasPublic == 0 {
			ownerQueries = append(ownerQueries,
				`SELECT DISTINCT owner_id FROM cookbooks WHERE visibility='public'`,
				`SELECT DISTINCT r.owner_id FROM recipes r JOIN recipe_publication_policy p ON p.recipe_id=r.id
					WHERE r.visibility='public' AND r.deleted_at IS NULL AND p.publication_state='published'`,
			)
		}
		if courses == 1 && (wasPublic == 0 || !wasCoursesEnabled) {
			ownerQueries = append(ownerQueries, `SELECT DISTINCT owner_id FROM courses WHERE visibility='public'`)
		}
		for _, ownerQuery := range ownerQueries {
			if err := requirePublisherProfilesForOwners(context.Background(), tx, ownerQuery); err != nil {
				return instanceSettings{}, err
			}
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
	if privateSnapshotStorageLimitBytes != nil {
		if *privateSnapshotStorageLimitBytes <= 0 {
			return instanceSettings{}, errors.New("private snapshot storage limit must be positive")
		}
		currentPrivateSnapshotStorageLimit = *privateSnapshotStorageLimitBytes
	}
	if anonymousHomePreset != nil {
		currentAnonymousHomePreset = *anonymousHomePreset
	}
	if siteName != nil {
		currentSiteName = *siteName
	}
	if palette != nil {
		currentPalette = *palette
	}
	if publicInstance != nil || coursesEnabled != nil || registrationPolicy != nil || revisionRetention != nil || auditRetention != nil || federationSwitch != nil || privateSnapshotStorageLimitBytes != nil {
		registrationPolicyConfigured = 1
	}
	publicValue := 0
	if public {
		publicValue = 1
	}
	if _, err := tx.Exec(`UPDATE instance_settings SET public_instance=?,courses_enabled=?,registration_policy=?,registration_policy_configured=?,revision_retention_days=?,audit_retention_days=?,federation_enabled=?,federation_origin_locked=CASE WHEN ?=1 THEN 1 ELSE federation_origin_locked END,private_snapshot_storage_limit_bytes=?,anonymous_home_preset=?,site_name=?,palette=? WHERE id=1`, publicValue, courses, currentPolicy, registrationPolicyConfigured, currentRevisionRetention, currentAuditRetention, federation, federation, currentPrivateSnapshotStorageLimit, currentAnonymousHomePreset, currentSiteName, currentPalette); err != nil {
		return instanceSettings{}, err
	}
	if oldFederation != federation || (publicInstance != nil && (wasPublic == 1) != public) {
		rows, err := tx.Query(`SELECT id FROM recipes WHERE visibility='public' OR EXISTS(SELECT 1 FROM federation_local_objects WHERE recipe_id=recipes.id AND published=1) ORDER BY id`)
		if err != nil {
			return instanceSettings{}, err
		}
		var recipeIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return instanceSettings{}, err
			}
			recipeIDs = append(recipeIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return instanceSettings{}, err
		}
		if err := rows.Close(); err != nil {
			return instanceSettings{}, err
		}
		for _, id := range recipeIDs {
			if err := enqueueFederationChange(tx, id); err != nil {
				return instanceSettings{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return instanceSettings{}, err
	}
	return readInstanceSettings(db)
}

func parseOptionalSiteName(raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	value = strings.TrimSpace(value)
	if !validSiteName(value) {
		return nil, errors.New("invalid site name")
	}
	return &value, nil
}

func parseOptionalPalette(raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !validPalette(value) {
		return nil, errors.New("invalid palette")
	}
	return &value, nil
}

func validSiteName(value string) bool {
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 48 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validRetentionDays(days int) bool {
	switch days {
	case 0, 7, 30, 90, 365:
		return true
	default:
		return false
	}
}
