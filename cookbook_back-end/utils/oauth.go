package utils

import (
	"Cookbook_Database_Backend/models"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Google app ClientID and Redirect URI. Don't ever change unless I completely change things in OAuth in which case I need to update this code.
const CLIENT_ID = "740105900233-2d32k4qo18g6ocodqfeepbajkid1e9tk.apps.googleusercontent.com"
const REDIRECT_URI = "http://localhost:8000/callback"

// OAuth2 Config
var googleOAuth2Config = oauth2.Config{
	ClientID:     "740105900233-2d32k4qo18g6ocodqfeepbajkid1e9tk.apps.googleusercontent.com",
	ClientSecret: "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",
	RedirectURL:  "http://localhost:8000/oauth/callback",
	Scopes: []string{
		"profile",
		"email",
	},
	Endpoint: google.Endpoint,
}

func OAuthURL() string {
	// Generate state
	var state = generateState()

	// Lock map and update it
	mapMutex.Lock()
	statePool[state] = struct{}{}
	mapMutex.Unlock()

	// Generate the OAuth URL and return it
	oAuthUrl := googleOAuth2Config.AuthCodeURL(state, oauth2.AccessTypeOffline)
	return oAuthUrl
}

func CallbackExchange(code string, ctx context.Context) (refreshToken string, jwtToken string, err error) {
	// Exchange code
	authToken, err := googleOAuth2Config.Exchange(ctx, code)
	if err != nil { // If google token couldn't be exchanged, exit early
		return "", "", fmt.Errorf("failed to exchange code for token: %v", err)
	}
	refreshToken = authToken.RefreshToken

	// Use authToken to retrieve UserInfo
	userInfo, err := retrieveUserInfo(authToken, ctx)
	if err != nil {
		return "Failed to retrieve user info", "", err
	}

	// Use userInfo to create a jwt
	jwtToken, err = IssueJwt(userInfo)
	if err != nil {
		return "Failed to issue a JWT", "", err
	}

	// Return values
	return refreshToken, jwtToken, err
}

func RefreshLogin(refreshToken string, ctx context.Context) (newRefreshToken string, jwtToken string, err error) {
	// Verify the refresh token and exchange it for a new authToken
	authToken, err := exchangeRefreshToken(refreshToken, ctx)
	if err != nil {
		return "", "", fmt.Errorf("failed to exchange refresh token: %v", err)
	}
	newRefreshToken = authToken.RefreshToken

	// Retrieve the user info and use it to issue a new JWT
	userInfo, err := retrieveUserInfo(authToken, ctx)
	if err != nil {
		return "", "", err
	}
	jwtToken, err = IssueJwt(userInfo)

	// Return
	return newRefreshToken, jwtToken, err
}

// Validates a refresh token, and returns a new AuthToken if refresh token is valid
func exchangeRefreshToken(refreshToken string, ctx context.Context) (authToken *oauth2.Token, err error) {
	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}

	// Use token source to refresh
	tokenSource := googleOAuth2Config.TokenSource(ctx, token)
	authToken, err = tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to exchange refresh token: %v", err)
	}

	// Return authToken
	return authToken, nil
}

// Retrieves user info using an AuthToken Object
func retrieveUserInfo(authToken *oauth2.Token, ctx context.Context) (user models.UserInfo, err error) {
	// Fetch user info from Google
	client := googleOAuth2Config.Client(ctx, authToken)
	response, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil { // If user info couldn't be fetched, exit early
		return user, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return user, errors.New("failed to get user info from Google API")
	}
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
		return user, errors.New("failed to parse fetched user info")
	}
	return user, nil
}
