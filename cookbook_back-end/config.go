package main

import (
	"database/sql"
	"errors"
	"os"
	"strings"
)

type config struct {
	address  string
	database string
}

func configureRegistrationPolicy(db *sql.DB) error {
	policy := strings.TrimSpace(os.Getenv("COOKBOOK_REGISTRATION"))
	if policy == "" {
		return nil
	}
	if policy != "open" && policy != "invite_only" && policy != "disabled" {
		return errors.New("COOKBOOK_REGISTRATION must be open, invite_only, or disabled")
	}
	if _, err := db.Exec(`UPDATE instance_settings SET registration_policy=? WHERE id=1`, policy); err != nil {
		return errors.New("could not apply COOKBOOK_REGISTRATION")
	}
	return nil
}

func loadConfig() (config, error) {
	address := strings.TrimSpace(os.Getenv("COOKBOOK_ADDR"))
	if address == "" {
		address = "127.0.0.1:8000"
	}
	if !strings.Contains(address, ":") {
		return config{}, errors.New("COOKBOOK_ADDR must be a host:port address")
	}
	database := strings.TrimSpace(os.Getenv("COOKBOOK_DATABASE"))
	if database == "" {
		database = "cookbook.db"
	}
	return config{address: address, database: database}, nil
}
