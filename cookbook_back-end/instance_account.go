package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrInstanceAccountNotFound = errors.New("no local account matches that email address")

type instanceAccountIdentity struct {
	ID          string
	Email       string
	DisplayName string
}

func lookupInstanceAccountByEmail(ctx context.Context, db *sql.DB, rawEmail string) (instanceAccountIdentity, error) {
	email, err := normalizeInstanceAccountEmail(rawEmail)
	if err != nil {
		return instanceAccountIdentity{}, err
	}
	var account instanceAccountIdentity
	err = db.QueryRowContext(ctx, `SELECT id,email,display_name FROM users WHERE email=?`, email).Scan(&account.ID, &account.Email, &account.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return instanceAccountIdentity{}, ErrInstanceAccountNotFound
	}
	if err != nil {
		return instanceAccountIdentity{}, fmt.Errorf("look up local account: %w", err)
	}
	return account, nil
}

func normalizeInstanceAccountEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 {
		return "", errors.New("a single valid email address is required")
	}
	return email, nil
}

// openAccountLookupDatabase opens an existing database using SQLite read-only
// mode. In particular, this path never creates a database or applies schema
// migrations as a side effect of an account lookup.
func openAccountLookupDatabase(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, errors.New("account lookup requires an existing filesystem database path")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve account lookup database path: %w", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open account lookup database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("account lookup database must be a regular file")
	}
	dsn := (&url.URL{Scheme: "file", Path: absolutePath}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open account lookup database: %w", err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open account lookup database: %w", err)
	}
	return db, nil
}

func runInstanceAccountCLI(args []string, output io.Writer) (bool, error) {
	if len(args) == 1 && args[0] == "--help" {
		_, _ = fmt.Fprintln(output, instanceAccountCLIUsage)
		return true, nil
	}
	if len(args) == 2 && args[0] == "lookup" && args[1] == "--help" {
		_, _ = fmt.Fprintln(output, instanceAccountCLIUsage)
		return true, nil
	}
	if len(args) < 1 || args[0] != "lookup" {
		_, _ = fmt.Fprintln(output, instanceAccountCLIUsage)
		return true, errors.New("expected account lookup")
	}
	email, err := parseAccountLookupFlags(args[1:])
	if err != nil {
		return true, err
	}
	cfg, err := loadConfig()
	if err != nil {
		return true, err
	}
	lock, err := acquireMaintenanceLock(cfg.database, maintenanceLockExclusive)
	if err != nil {
		return true, fmt.Errorf("account lookup requires exclusive maintenance lock: %w", err)
	}
	defer lock.Close()
	db, err := openAccountLookupDatabase(cfg.database)
	if err != nil {
		return true, err
	}
	defer db.Close()
	account, err := lookupInstanceAccountByEmail(context.Background(), db, email)
	if err != nil {
		return true, err
	}
	_, err = fmt.Fprintf(output, "Account ID: %s\nEmail: %s\nName: %s\n", account.ID, account.Email, account.DisplayName)
	return true, err
}

const instanceAccountCLIUsage = `Usage:
  cookbook-backend instance account lookup --email <address>

Performs an exact local account lookup without changing account state. The server must be stopped.`

func parseAccountLookupFlags(args []string) (string, error) {
	var email string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return "", fmt.Errorf("%s requires a value", args[i])
		}
		if args[i] != "--email" || email != "" {
			return "", errors.New("exactly one --email is required")
		}
		email = args[i+1]
		i++
	}
	if email == "" {
		return "", errors.New("exactly one --email is required")
	}
	normalized, err := normalizeInstanceAccountEmail(email)
	if err != nil {
		return "", err
	}
	return normalized, nil
}
