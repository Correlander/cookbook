package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
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

var (
	ErrInstanceAccountNotFound       = errors.New("no local account matches that email address")
	ErrInstanceAccountAlreadyExists  = errors.New("a local account already uses that email address")
	ErrInstanceAccountBootstrapFirst = errors.New("no local accounts exist; bootstrap the initial administrator before creating additional accounts")
	ErrInstanceAccountDatabaseSchema = errors.New("database is not initialized for local account creation; start the server before adding accounts")
)

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

func createInstanceAccount(ctx context.Context, db *sql.DB, rawEmail, rawDisplayName string) (account instanceAccountIdentity, err error) {
	if db == nil {
		return instanceAccountIdentity{}, errors.New("account database is unavailable")
	}
	email, err := normalizeInstanceAccountEmail(rawEmail)
	if err != nil {
		return instanceAccountIdentity{}, err
	}
	displayName, err := normalizeInstanceAccountDisplayName(rawDisplayName, email)
	if err != nil {
		return instanceAccountIdentity{}, err
	}
	account = instanceAccountIdentity{ID: newID(), Email: email, DisplayName: displayName}
	err = withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		if err := validateInstanceAccountCreateEligibility(ctx, tx); err != nil {
			return err
		}

		var existingID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email)=? LIMIT 1`, email).Scan(&existingID)
		if err == nil {
			return ErrInstanceAccountAlreadyExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check existing account email: %w", err)
		}

		now := time.Now().UTC()
		createdAt := formatOwnerTime(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES(?,?,?,0,?)`, account.ID, account.Email, account.DisplayName, createdAt); err != nil {
			return fmt.Errorf("create local account: %w", err)
		}
		if err := insertInstanceOwnerAudit(ctx, tx, "", "instance_account.create", "account", account.ID, 201, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return instanceAccountIdentity{}, err
	}
	return account, nil
}

func validateInstanceAccountCreateEligibility(ctx context.Context, queryer migrationExecutor) error {
	requiredTables := []struct {
		table   string
		columns []string
	}{
		{table: "users", columns: []string{"id", "email", "display_name", "is_admin", "created_at"}},
		{table: "audit_events", columns: []string{"id", "actor_id", "action", "resource_type", "resource_id", "response_status", "created_at"}},
	}
	for _, required := range requiredTables {
		var objectType string
		if err := queryer.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name=?`, required.table).Scan(&objectType); err != nil || objectType != "table" {
			return ErrInstanceAccountDatabaseSchema
		}
		columns, err := tableColumnDetails(ctx, queryer, required.table)
		if err != nil || len(columns) == 0 {
			return ErrInstanceAccountDatabaseSchema
		}
		present := make(map[string]struct{}, len(columns))
		for _, column := range columns {
			present[column.name] = struct{}{}
		}
		for _, column := range required.columns {
			if _, exists := present[column]; !exists {
				return ErrInstanceAccountDatabaseSchema
			}
		}
	}
	var accountCount int
	if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&accountCount); err != nil {
		return ErrInstanceAccountDatabaseSchema
	}
	if accountCount == 0 {
		return ErrInstanceAccountBootstrapFirst
	}
	return nil
}

func normalizeInstanceAccountEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 {
		return "", errors.New("a single valid email address is required")
	}
	return email, nil
}

// normalizeInstanceAccountDisplayName follows the name behavior of email
// registration: trim surrounding whitespace, use the email local-part when
// blank, and enforce the same 100-byte limit.
func normalizeInstanceAccountDisplayName(raw, email string) (string, error) {
	displayName := strings.TrimSpace(raw)
	if len(displayName) > 100 {
		return "", errors.New("display name must be at most 100 bytes")
	}
	if displayName == "" {
		displayName = strings.SplitN(email, "@", 2)[0]
	}
	return displayName, nil
}

// formatInstanceAccountIdentity returns one JSON line so stored names and
// emails cannot inject terminal control sequences into offline CLI output.
func formatInstanceAccountIdentity(account instanceAccountIdentity) string {
	encoded, _ := json.Marshal(struct {
		AccountID   string `json:"account_id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}{AccountID: account.ID, Email: account.Email, DisplayName: account.DisplayName})
	return string(encoded)
}

// openAccountLookupDatabase opens an existing SQLite file in read-only mode for
// offline local inspection. It never creates a database or applies migrations.
func openAccountLookupDatabase(path string) (*sql.DB, error) {
	return openExistingAccountDatabase(path, "ro", "account lookup")
}

// openExistingAccountDatabase opens only an existing regular file. In
// particular, the provisioning CLI must not initialize an empty path or run
// schema migrations as a side effect.
func openExistingAccountDatabase(path, mode, operation string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("%s requires an existing filesystem database path", operation)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s database path: %w", operation, err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", operation, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s database must be a regular file", operation)
	}
	dsn := (&url.URL{Scheme: "file", Path: absolutePath}).String() + "?mode=" + mode
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", operation, err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s database: %w", operation, err)
	}
	return db, nil
}

func runInstanceAccountCLI(args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 1 && args[0] == "--help" {
		_, _ = fmt.Fprintln(output, instanceAccountCLIUsage)
		return true, nil
	}
	if len(args) == 2 && (args[0] == "lookup" || args[0] == "create") && args[1] == "--help" {
		_, _ = fmt.Fprintln(output, instanceAccountCLIUsage)
		return true, nil
	}
	if len(args) > 0 && args[0] == "create" {
		return runInstanceAccountCreateCLI(args[1:], input, output)
	}
	if len(args) < 1 || args[0] != "lookup" {
		_, _ = fmt.Fprintln(output, instanceAccountCLIUsage)
		return true, errors.New("expected account lookup or create")
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
	_, err = fmt.Fprintln(output, formatInstanceAccountIdentity(account))
	return true, err
}

const instanceAccountCLIUsage = `Usage:
  cookbook-backend instance account create --email <address> --display-name <name>
  cookbook-backend instance account lookup --email <address>

Create provisions one ordinary local account on an initialized instance. The server must be stopped; an exact interactive confirmation and exclusive maintenance lock are required. It does not verify mailbox ownership or create credentials, sessions, roles, actors, or enrollment grants.
Lookup performs an exact local account lookup without changing account state.`

func runInstanceAccountCreateCLI(args []string, input io.Reader, output io.Writer) (bool, error) {
	email, displayName, err := parseAccountCreateFlags(args)
	if err != nil {
		return true, err
	}
	if input == os.Stdin {
		info, statErr := os.Stdin.Stat()
		if statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
			return true, errors.New("account creation requires an interactive terminal")
		}
	}
	identityJSON := formatInstanceAccountIdentity(instanceAccountIdentity{Email: email, DisplayName: displayName})
	confirmation := "CREATE ACCOUNT"
	_, _ = fmt.Fprintf(output, "This creates an ordinary local account. Identity: %s. Type %s to continue: ", identityJSON, confirmation)
	line, readErr := bufio.NewReader(input).ReadString('\n')
	if readErr != nil || strings.TrimSpace(line) != confirmation {
		return true, errors.New("account creation was not confirmed")
	}
	cfg, err := loadConfig()
	if err != nil {
		return true, err
	}
	// Do harmless read-only validation before creating the stable lock sidecar.
	// The account state and schema are checked again inside the write transaction
	// after the exclusive lock, so this preflight is never the write authority.
	preflight, err := openExistingAccountDatabase(cfg.database, "ro", "account creation")
	if err != nil {
		return true, err
	}
	preflightErr := validateInstanceAccountCreateEligibility(context.Background(), preflight)
	closeErr := preflight.Close()
	if preflightErr != nil {
		return true, preflightErr
	}
	if closeErr != nil {
		return true, fmt.Errorf("close account creation preflight database: %w", closeErr)
	}
	lock, err := acquireMaintenanceLock(cfg.database, maintenanceLockExclusive)
	if err != nil {
		return true, fmt.Errorf("offline account creation requires exclusive maintenance lock: %w", err)
	}
	defer lock.Close()
	db, err := openExistingAccountDatabase(cfg.database, "rw", "account creation")
	if err != nil {
		return true, err
	}
	defer db.Close()
	account, err := createInstanceAccount(context.Background(), db, email, displayName)
	if err != nil {
		return true, err
	}
	_, err = fmt.Fprintf(output, "Account created:\n%s\n", formatInstanceAccountIdentity(account))
	return true, err
}

func parseAccountLookupFlags(args []string) (string, error) {
	var email string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return "", fmt.Errorf("%q requires a value", args[i])
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

func parseAccountCreateFlags(args []string) (string, string, error) {
	var email, displayName string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return "", "", fmt.Errorf("%q requires a value", args[i])
		}
		value := args[i+1]
		switch args[i] {
		case "--email":
			if email != "" {
				return "", "", errors.New("exactly one --email is required")
			}
			normalized, err := normalizeInstanceAccountEmail(value)
			if err != nil {
				return "", "", err
			}
			email = normalized
		case "--display-name":
			if displayName != "" {
				return "", "", errors.New("exactly one --display-name is required")
			}
			if strings.TrimSpace(value) == "" {
				return "", "", errors.New("exactly one --display-name is required")
			}
			displayName = value
		default:
			return "", "", fmt.Errorf("unsupported account create flag %q", args[i])
		}
		i++
	}
	if email == "" {
		return "", "", errors.New("exactly one --email is required")
	}
	if displayName == "" {
		return "", "", errors.New("exactly one --display-name is required")
	}
	normalizedName, err := normalizeInstanceAccountDisplayName(displayName, email)
	if err != nil {
		return "", "", err
	}
	return email, normalizedName, nil
}
