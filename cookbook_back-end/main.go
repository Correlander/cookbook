package main

import (
	routing "Cookbook_Database_Backend/RoutingHandlers"
	"log"
	"net/http"
)

func main() {
	////////////////////////////////
	// Intitiate routes ///////
	//////////////////////
	// OAuth
	http.HandleFunc("/oauth/init", routing.OAuthInitHandler)
	http.HandleFunc("/oauth/callback", routing.OAuthCallbackHandler)
	// Login
	http.HandleFunc("/auth", routing.CheckAuth)

	// Start server
	port := ":8000"
	log.Printf("Server started on %s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
