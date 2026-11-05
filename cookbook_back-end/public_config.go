package main

import (
	"database/sql"
	"net/http"
)

type publicConfigHandler struct {
	db               *sql.DB
	emailLinkEnabled bool
	passkeyEnabled   bool
}

type publicAuthMethods struct {
	EmailLink bool `json:"email_link"`
	Passkey   bool `json:"passkey"`
}

type publicConfigResponse struct {
	AuthMethods              publicAuthMethods `json:"auth_methods"`
	RegistrationPolicy       string            `json:"registration_policy"`
	PublicInstance           bool              `json:"public_instance"`
	CoursesEnabled           bool              `json:"courses_enabled"`
	CourseCreatorAttribution bool              `json:"course_creator_attribution,omitempty"`
	MenuAPI                  bool              `json:"menu_api"`
	AnonymousHomePreset      string            `json:"anonymous_home_preset"`
}

func publicConfigFor(db *sql.DB, auth authHandler) http.Handler {
	return publicConfigHandler{
		db:               db,
		emailLinkEnabled: auth.sender != nil,
		passkeyEnabled:   auth.webauthn != nil,
	}
}

func (h publicConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var public, courses int
	response := publicConfigResponse{
		AuthMethods: publicAuthMethods{
			EmailLink: h.emailLinkEnabled,
			Passkey:   h.passkeyEnabled,
		},
	}
	if err := h.db.QueryRow(`SELECT registration_policy,public_instance,courses_enabled,anonymous_home_preset FROM instance_settings WHERE id=1`).Scan(&response.RegistrationPolicy, &public, &courses, &response.AnonymousHomePreset); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	response.PublicInstance = public == 1
	response.CoursesEnabled = courses == 1
	response.CourseCreatorAttribution = true
	response.MenuAPI = true
	writeJSON(w, http.StatusOK, response)
}
