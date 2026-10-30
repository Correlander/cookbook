package main

import (
	"database/sql"
	"errors"
	"net/mail"
	"os"
	"strings"
)

type config struct {
	address  string
	database string
}

func provisionAdministrator(db *sql.DB) error {
	email := strings.ToLower(strings.TrimSpace(os.Getenv("COOKBOOK_ADMIN_EMAIL")))
	if email == "" {
		return nil
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return errors.New("COOKBOOK_ADMIN_EMAIL must be a valid email address")
	}
	var admins int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin=1`).Scan(&admins); err != nil {
		return errors.New("could not check administrator bootstrap state")
	}
	if admins != 0 {
		return nil
	}
	var uid string
	if err := db.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&uid); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.New("could not find bootstrap account")
	} else if errors.Is(err, sql.ErrNoRows) {
		uid = newID()
		name := strings.TrimSpace(os.Getenv("COOKBOOK_ADMIN_NAME"))
		if name == "" {
			name = strings.Split(email, "@")[0]
		}
		if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES(?,?,?,1,?)`, uid, email, name, timestamp()); err != nil {
			return errors.New("could not create bootstrap administrator")
		}
		return nil
	}
	if _, err := db.Exec(`UPDATE users SET is_admin=1 WHERE id=?`, uid); err != nil {
		return errors.New("could not grant initial administrator role")
	}
	return nil
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
