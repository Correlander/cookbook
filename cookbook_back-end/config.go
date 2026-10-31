package main

import (
	"database/sql"
	"errors"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type config struct {
	address            string
	database           string
	mediaDir           string
	trashRetentionDays int
	trustedProxies     []*net.IPNet
	federationOrigin   string
}

func canonicalFederationOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.ForceQuery || parsed.RawQuery != "" || strings.Contains(raw, "#") {
		return "", errors.New("federation origin must be an HTTPS origin")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", errors.New("federation origin must use the default HTTPS port")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.HasSuffix(host, ".") || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return "", errors.New("federation origin must use a public DNS name")
	}
	for _, reserved := range []string{".localhost", ".local", ".test", ".invalid", ".example", ".internal"} {
		if host == strings.TrimPrefix(reserved, ".") || strings.HasSuffix(host, reserved) {
			return "", errors.New("federation origin must use a public DNS name")
		}
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("federation origin has an invalid DNS name")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("federation origin has an invalid DNS name")
			}
		}
	}
	lastLabel := labels[len(labels)-1]
	if allDigits(lastLabel) {
		return "", errors.New("federation origin has an invalid DNS name")
	}
	return "https://" + host, nil
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func configureFederationOrigin(db *sql.DB, configured string) error {
	normalized := ""
	var err error
	if strings.TrimSpace(configured) != "" {
		normalized, err = canonicalFederationOrigin(configured)
		if err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stored string
	var enabled, locked int
	if err := tx.QueryRow(`SELECT federation_origin,federation_enabled,federation_origin_locked FROM instance_settings WHERE id=1`).Scan(&stored, &enabled, &locked); err != nil {
		return err
	}
	if stored != "" {
		canonicalStored, err := canonicalFederationOrigin(stored)
		if err != nil || canonicalStored != stored {
			return errors.New("stored federation origin is invalid")
		}
	}
	if normalized == "" {
		normalized = stored
	}
	if enabled == 1 && normalized == "" {
		return errors.New("federation is enabled but no federation origin is configured")
	}
	if enabled == 1 && stored != "" && normalized != stored {
		return errors.New("federation origin cannot change while federation is enabled")
	}
	if locked == 1 && normalized != stored {
		return errors.New("federation origin cannot change after federation is enabled")
	}
	if normalized != stored {
		if _, err := tx.Exec(`UPDATE instance_settings SET federation_origin=? WHERE id=1`, normalized); err != nil {
			return err
		}
	}
	if enabled == 1 && locked == 0 {
		if _, err := tx.Exec(`UPDATE instance_settings SET federation_origin_locked=1 WHERE id=1`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func federationRecipeObjectID(origin, recipeID string) (string, error) {
	canonical, err := canonicalFederationOrigin(origin)
	if err != nil || canonical != origin || len(recipeID) != 32 {
		return "", errors.New("invalid federation object identity")
	}
	for _, char := range recipeID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", errors.New("invalid federation object identity")
		}
	}
	return origin + "/federation/recipes/" + recipeID, nil
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
	if policy != "" && policy != "open" && policy != "invite_only" && policy != "disabled" {
		return errors.New("COOKBOOK_REGISTRATION must be open, invite_only, or disabled")
	}
	var configured int
	if err := db.QueryRow(`SELECT registration_policy_configured FROM instance_settings WHERE id=1`).Scan(&configured); err != nil {
		return errors.New("could not read COOKBOOK_REGISTRATION state")
	}
	if configured == 1 {
		return nil
	}
	if _, err := db.Exec(`UPDATE instance_settings SET registration_policy=CASE WHEN ?='' THEN registration_policy ELSE ? END,registration_policy_configured=1 WHERE id=1`, policy, policy); err != nil {
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
	mediaDir := strings.TrimSpace(os.Getenv("COOKBOOK_MEDIA_DIR"))
	if mediaDir == "" {
		mediaDir = filepath.Join(filepath.Dir(database), "media")
	}
	retentionDays := 30
	if raw := strings.TrimSpace(os.Getenv("COOKBOOK_TRASH_RETENTION_DAYS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 3650 {
			return config{}, errors.New("COOKBOOK_TRASH_RETENTION_DAYS must be between 0 and 3650")
		}
		retentionDays = parsed
	}
	var trustedProxies []*net.IPNet
	if raw := strings.TrimSpace(os.Getenv("COOKBOOK_TRUSTED_PROXIES")); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			_, network, err := net.ParseCIDR(strings.TrimSpace(entry))
			if err != nil {
				return config{}, errors.New("COOKBOOK_TRUSTED_PROXIES must be comma-separated IP CIDRs")
			}
			trustedProxies = append(trustedProxies, network)
		}
	}
	federationOrigin, err := canonicalFederationOriginIfConfigured(os.Getenv("COOKBOOK_FEDERATION_ORIGIN"))
	if err != nil {
		return config{}, err
	}
	return config{address: address, database: database, mediaDir: mediaDir, trashRetentionDays: retentionDays, trustedProxies: trustedProxies, federationOrigin: federationOrigin}, nil
}

func canonicalFederationOriginIfConfigured(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return canonicalFederationOrigin(raw)
}
