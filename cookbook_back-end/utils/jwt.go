package utils

import (
	"Cookbook_Database_Backend/models"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// SECRET
var secret = generateSecret()

// Verify the validity of a JWT
func VerifyJWT(jwtStr string) error {
	// Parse the token and validate its signature
	token, err := jwt.Parse(jwtStr, func(token *jwt.Token) (interface{}, error) {
		// Ensure it's the expected signing method
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return secret, nil
	})
	if err != nil { // If err isn't nil, token invalid
		return fmt.Errorf("invalid token: %v", err)
	}

	// Check if the token's claims
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// Check expiration
		if exp, ok := claims["exp"].(float64); ok {
			if time.Unix(int64(exp), 0).Before(time.Now()) {
				return errors.New("token expired")
			}
		}
		// Check issuer - I don't need to implement this since there is only one possible source atm
		return nil // Token is valid
	}

	return errors.New("invalid token")
}

// Create JWT using a refresh token to fetch info
func IssueJwt(user models.UserInfo) (jwtStr string, err error) {
	// Create the JWT
	claims := jwt.MapClaims{
		"sub":     user.ID,
		"name":    user.Name,
		"email":   user.Email,
		"picture": user.Picture,
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(time.Hour * 6).Unix(), // 6 hour expiration for sessions
		"iss":     "AKURR_COOKBOOK_API",
		"aud":     "AKURR_COOKBOOK_API",
	}
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Sign the jwt
	signedToken, err := jwtToken.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("failed to generate the JWT token: %v", err)
	}

	// Return
	return signedToken, err

	//make the jwt stored in local storage not a cookie
	//https needs local
}
