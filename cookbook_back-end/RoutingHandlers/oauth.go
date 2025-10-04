package routinghandlers

import (
	"Cookbook_Database_Backend/utils"
	"encoding/json"
	"net/http"
	"time"
)

// /login route handler - redirects to Google to start auth process
func OAuthInitHandler(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", CORS_ALLOWED_ORIGIN)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	// Handle preflight
	if r.Method == "OPTIONS" {
		return
	}

	// Get constructed URL and send it back in response
	var oauth_url = utils.OAuthURL()
	response := map[string]string{"oauth_url": oauth_url}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func OAuthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", CORS_ALLOWED_ORIGIN)
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "")
	// Handle preflight
	if r.Method == "OPTIONS" {
		return
	}

	// Verify the state
	state := r.URL.Query().Get("state")
	if state == "" { // Make sure it's not empty
		http.Error(w, "State missing", http.StatusBadRequest)
		// LOGGER ENTRY HERE
		return
	}
	if !utils.VerifyState(state) {
		http.Error(w, "Invalid state", http.StatusForbidden)
		// LOGGER ENTRY HERE
		return
	}

	// Exchange the code for a JWT and refresh token
	code := r.URL.Query().Get("code")
	if code == "" { // Make sure it's not empty
		http.Error(w, "Authorization code missing", http.StatusBadRequest)
		// LOGGER ENTRY HERE
		return
	}

	// Exchange handled in separate package
	refreshToken, jwt, err := utils.CallbackExchange(code, r.Context())

	if err != nil { // Make sure no issues with exchange
		http.Error(w, err.Error(), http.StatusInternalServerError)
		// LOGGER ENTRY HERE OF THE ERR
		return
	}

	// Set the JWT in a secure cookie
	http.SetCookie(w, &http.Cookie{
		Name:  "jwt",
		Value: jwt,
		Path:  "/",
		//Domain:   DOMAIN_VAL,
		HttpOnly: true,
		Secure:   false, //this NEEDS to be changed to true once TLS certificates are implemented.
		//SameSite: http.SameSiteNoneMode,
		Expires: time.Now().Add(5*time.Hour + 59*time.Minute), // Expire ~1 minute before the JWT
	})

	// Set the refresh token in a secure cookie
	http.SetCookie(w, &http.Cookie{
		Name:  "refresh_token",
		Value: refreshToken,
		Path:  "/",
		//Domain:   DOMAIN_VAL,
		HttpOnly: true,
		Secure:   false, // THIS ALSO NEEDS TO BE CHANGED TO TRUE
		//SameSite: http.SameSiteNoneMode,
		Expires: time.Now().Add(7 * 24 * time.Hour), // Expires in a Week
	})

	// Redirect to front end
	http.Redirect(w, r, "http://localhost:9712", http.StatusFound)
}
