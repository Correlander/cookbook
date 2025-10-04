package utils

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"sync"
)

func generateSecret() string {
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	if err != nil {
		log.Fatal("Failed to generate secret key:", err) // Replace with logging at later date
	}
	return base64.StdEncoding.EncodeToString(secret)
}

func generateState() string {
	// Generate a random 32b slice
	stateBytes := make([]byte, 32)
	_, err := rand.Read(stateBytes)
	if err != nil {
		log.Fatal("failed to generate state!!!: %v", err) // Replace with logging at later date
		return ""
	}

	// Encode the bytes to a URL-safe string
	state := base64.URLEncoding.EncodeToString(stateBytes)

	return state
}

// Storage for states. Allows easy quick check for if a received state is valid.
var statePool = make(map[string]struct{})
var mapMutex = &sync.Mutex{}

func VerifyState(state string) bool {
	// Check if the value is in the pool
	if _, exists := statePool[state]; exists {
		mapMutex.Lock()
		delete(statePool, state)
		mapMutex.Unlock()
		return true
	} else {
		return false
	}
}
