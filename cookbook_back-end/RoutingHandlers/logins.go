package routinghandlers

import (
	"Cookbook_Database_Backend/utils"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

var limiter = rate.NewLimiter(rate.Every(1*time.Second), 3)

func CheckAuth(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", CORS_ALLOWED_ORIGIN)
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "")
	// Handle preflight
	if r.Method == "OPTIONS" {
		return
	}

	if !limiter.Allow() {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		// LOGGER entry here?
		return
	}

	// Check for JWT cookie
	jwtCookie, err := r.Cookie("jwt")
	if err == nil {
		// JWT cookie found, verify it
		if err := utils.VerifyJWT(jwtCookie.Value); err != nil {
			http.Error(w, "Invalid JWT", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Authorized"))
		return // Return saying JWT is valid
	}

	// If JWT didn't return, means it wasn't found.
	// Check for refresh_token cookie
	refreshTokenCookie, err := r.Cookie("refresh_token")
	if err == nil {
		fmt.Print("REFRESH TOKEN COOKIE FOUND. WOOO")
		// Refresh token cookie found, verify it and issue a new JWT and new Refresh Token
		refreshToken, jwtToken, err := utils.RefreshLogin(refreshTokenCookie.Value, r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		// Write the new refresh token to cookie
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
		// Write the JWT to cookie
		http.SetCookie(w, &http.Cookie{
			Name:  "jwt",
			Value: jwtToken,
			Path:  "/",
			//Domain:   DOMAIN_VAL,
			HttpOnly: true,
			Secure:   false, //this NEEDS to be changed to true once TLS certificates are implemented.
			//SameSite: http.SameSiteNoneMode,
			Expires: time.Now().Add(5*time.Hour + 59*time.Minute), // Expire ~1 minute before the JWT
		})

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Authorized"))
		return // Return now that new JWT and RefreshToken have been issued
	}

	// If refresh token didn't return, means it wasn't found either
	fmt.Print("It's default failing")
	http.Error(w, "No authorization method found", http.StatusUnauthorized)
}
