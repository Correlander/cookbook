package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstanceOwnerCLIClaimRequiresExplicitTargetConfirmationAndExclusiveLock(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db := openOwnerCLITestDatabase(t, database)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('target','target@example.test','Target',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", database)
	t.Setenv("COOKBOOK_FEDERATION_ORIGIN", "")
	args := []string{"instance", "owner", "claim", "--account-id", "target"}
	var output bytes.Buffer
	if handled, err := runInstanceCLI(args, strings.NewReader("NO\n"), &output); !handled || err == nil {
		t.Fatalf("unconfirmed command handled=%v err=%v", handled, err)
	}
	lockPath, err := maintenanceLockPath(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfirmed command created lock file: %v", err)
	}
	shared, err := acquireMaintenanceLock(database, maintenanceLockShared)
	if err != nil {
		t.Fatal(err)
	}
	if handled, err := runInstanceCLI(args, strings.NewReader("CLAIM target\n"), &output); !handled || !errors.Is(err, ErrMaintenanceLockHeld) {
		t.Fatalf("claim with running server lock handled=%v err=%v", handled, err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if handled, err := runInstanceCLI(args, strings.NewReader("CLAIM target\n"), &output); !handled || err != nil {
		t.Fatalf("confirmed claim handled=%v err=%v output=%s", handled, err, output.String())
	}
	check := openOwnerCLITestDatabase(t, database)
	defer check.Close()
	var owner string
	var admin int
	if err := check.QueryRow(`SELECT a.owner_user_id,u.is_admin FROM instance_authority a JOIN users u ON u.id=a.owner_user_id`).Scan(&owner, &admin); err != nil || owner != "target" || admin != 0 {
		t.Fatalf("owner=%q admin=%d err=%v; claim must not grant admin", owner, admin, err)
	}
	if handled, err := runInstanceCLI(args, strings.NewReader("CLAIM target\n"), &output); !handled || err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("duplicate claim handled=%v err=%v", handled, err)
	}
}

func TestInstanceOwnerCLIRecoverAuditsReasonAndInvalidatesPendingTransfer(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db := openOwnerCLITestDatabase(t, database)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',1,'now'),('target','target@example.test','Target',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner"); err != nil {
		t.Fatal(err)
	}
	transfer, err := CreateInstanceOwnerTransfer(context.Background(), db, "owner", "target")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", database)
	t.Setenv("COOKBOOK_FEDERATION_ORIGIN", "")
	args := []string{"instance", "owner", "recover", "--account-id", "target", "--reason", "lost-owner-credential"}
	var output bytes.Buffer
	if handled, err := runInstanceCLI(args, strings.NewReader("RECOVER target\n"), &output); !handled || err != nil {
		t.Fatalf("confirmed recovery handled=%v err=%v output=%s", handled, err, output.String())
	}
	check := openOwnerCLITestDatabase(t, database)
	defer check.Close()
	var owner, resourceID, transferState string
	var generation int64
	var admin int
	if err := check.QueryRow(`SELECT owner_user_id,generation FROM instance_authority`).Scan(&owner, &generation); err != nil || owner != "target" || generation != 2 {
		t.Fatalf("recovered authority owner=%q generation=%d err=%v", owner, generation, err)
	}
	if err := check.QueryRow(`SELECT state FROM instance_owner_transfers WHERE id=?`, transfer.ID).Scan(&transferState); err != nil || transferState != string(OwnerTransferCancelled) {
		t.Fatalf("recovered pending transfer state=%q err=%v", transferState, err)
	}
	if err := check.QueryRow(`SELECT is_admin FROM users WHERE id='target'`).Scan(&admin); err != nil || admin != 0 {
		t.Fatalf("recovery changed target admin role=%d err=%v", admin, err)
	}
	if err := check.QueryRow(`SELECT resource_id FROM audit_events WHERE action='instance_owner.recover'`).Scan(&resourceID); err != nil || !strings.Contains(resourceID, "previous=owner; target=target; reason=lost-owner-credential") {
		t.Fatalf("recovery audit resource=%q err=%v", resourceID, err)
	}
}

func TestInstanceOwnerCLIRequiresRecoveryReasonAndRejectsImplicitTarget(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"instance", "owner", "claim", "--reason", "no-target"}, "account-id"},
		{[]string{"instance", "owner", "recover", "--account-id", "target"}, "reason"},
		{[]string{"instance", "owner", "claim", "--account-id", "target", "--reason", "wrong"}, "only valid for recovery"},
	} {
		if handled, err := runInstanceCLI(tc.args, strings.NewReader("CLAIM target\n"), &bytes.Buffer{}); !handled || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("args=%v handled=%v err=%v, want error containing %q", tc.args, handled, err, tc.want)
		}
	}
}

func TestPasskeyEnrollmentCLIIssuesHashOnlyTokenAndRevokesIt(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db := openOwnerCLITestDatabase(t, database)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('target','target@example.test','Target',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", database)
	t.Setenv("COOKBOOK_FEDERATION_ORIGIN", "")
	args := []string{"instance", "passkey-enrollment", "issue", "--account-id", "target"}
	var output bytes.Buffer
	if handled, err := runInstanceCLI(args, strings.NewReader("ISSUE PASSKEY ENROLLMENT target\n"), &output); !handled || err != nil {
		t.Fatalf("issue handled=%v err=%v output=%q", handled, err, output.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	token := lines[len(lines)-1]
	id, digest, ok := parseEnrollmentToken(token)
	if !ok {
		t.Fatalf("CLI did not print one valid token: %q", output.String())
	}
	check := openOwnerCLITestDatabase(t, database)
	var stored []byte
	var expires string
	var consumed, revoked sql.NullString
	if err := check.QueryRow(`SELECT id,token_hash,expires_at,consumed_at,revoked_at FROM passkey_enrollment_grants`).Scan(&id, &stored, &expires, &consumed, &revoked); err != nil || !bytes.Equal(stored, digest) || consumed.Valid || revoked.Valid {
		t.Fatalf("stored grant id=%q hash-match=%v consumed=%v revoked=%v err=%v", id, bytes.Equal(stored, digest), consumed, revoked, err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || time.Until(expiresAt) <= 0 || time.Until(expiresAt) > 15*time.Minute+time.Second {
		t.Fatalf("grant expiry=%q parsed=%v err=%v, want within 15 minutes", expires, expiresAt, err)
	}
	if err := check.Close(); err != nil {
		t.Fatal(err)
	}
	var revokeOutput bytes.Buffer
	if handled, err := runInstanceCLI([]string{"instance", "passkey-enrollment", "revoke", "--account-id", "target"}, strings.NewReader("REVOKE PASSKEY ENROLLMENT target\n"), &revokeOutput); !handled || err != nil {
		t.Fatalf("revoke handled=%v err=%v output=%q", handled, err, revokeOutput.String())
	}
	check = openOwnerCLITestDatabase(t, database)
	defer check.Close()
	if _, valid, err := validEnrollmentGrant(context.Background(), check, token, time.Now().UTC()); err != nil || valid {
		t.Fatalf("revoked token valid=%v err=%v", valid, err)
	}
}

func openOwnerCLITestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNoMailBootstrapAccountLookupAndFirstPasskeyGrant(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db := openOwnerCLITestDatabase(t, database)
	t.Setenv("COOKBOOK_DATABASE", database)
	t.Setenv("COOKBOOK_FEDERATION_ORIGIN", "")
	t.Setenv("COOKBOOK_ADMIN_EMAIL", "Bootstrap@example.test")
	t.Setenv("COOKBOOK_ADMIN_NAME", "Bootstrap Admin")
	if err := provisionAdministrator(db); err != nil {
		t.Fatal(err)
	}
	var bootstrappedID string
	if err := db.QueryRow(`SELECT id FROM users WHERE email='bootstrap@example.test'`).Scan(&bootstrappedID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("COOKBOOK_RP_ID", "localhost")
	t.Setenv("COOKBOOK_RP_ORIGIN", "http://localhost:5173")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "")
	t.Setenv("COOKBOOK_SMTP_HOST", "")
	t.Setenv("COOKBOOK_SMTP_FROM", "")
	t.Setenv("COOKBOOK_SMTP_USERNAME", "")
	t.Setenv("COOKBOOK_SMTP_PASSWORD", "")
	configured, err := authConfig(db)
	if err != nil || configured.sender != nil {
		t.Fatalf("no-mail startup auth config sender=%T err=%v", configured.sender, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	shared, err := acquireMaintenanceLock(database, maintenanceLockShared)
	if err != nil {
		t.Fatal(err)
	}
	var blockedLookup bytes.Buffer
	if handled, err := runInstanceCLI([]string{"instance", "account", "lookup", "--email", "bootstrap@example.test"}, strings.NewReader(""), &blockedLookup); !handled || !errors.Is(err, ErrMaintenanceLockHeld) {
		t.Fatalf("lookup while server lock is held handled=%v err=%v", handled, err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	var lookupOutput bytes.Buffer
	if handled, err := runInstanceCLI([]string{"instance", "account", "lookup", "--email", " BOOTSTRAP@example.test "}, strings.NewReader(""), &lookupOutput); !handled || err != nil {
		t.Fatalf("account lookup handled=%v err=%v output=%q", handled, err, lookupOutput.String())
	}
	if want := formatInstanceAccountIdentity(instanceAccountIdentity{ID: bootstrappedID, Email: "bootstrap@example.test", DisplayName: "Bootstrap Admin"}) + "\n"; lookupOutput.String() != want {
		t.Fatalf("lookup did not return only the expected identity confirmation: %q", lookupOutput.String())
	}

	var grantOutput bytes.Buffer
	confirm := "ISSUE PASSKEY ENROLLMENT " + bootstrappedID + "\n"
	args := []string{"instance", "passkey-enrollment", "issue", "--account-id", bootstrappedID}
	if handled, err := runInstanceCLI(args, strings.NewReader(confirm), &grantOutput); !handled || err != nil {
		t.Fatalf("initial passkey grant handled=%v err=%v", handled, err)
	}
	fields := strings.Fields(grantOutput.String())
	token := fields[len(fields)-1]
	if len(strings.Split(token, ".")) != 2 || len(strings.Split(token, ".")[0]) != 32 || len(strings.Split(token, ".")[1]) != 64 {
		t.Fatal("passkey enrollment grant did not print the expected one-time token format")
	}
	check := openOwnerCLITestDatabase(t, database)
	defer check.Close()
	var admin, grants int
	if err := check.QueryRow(`SELECT is_admin FROM users WHERE id=?`, bootstrappedID).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM passkey_enrollment_grants WHERE user_id=? AND consumed_at IS NULL AND revoked_at IS NULL`, bootstrappedID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if admin != 1 || grants != 1 {
		t.Fatalf("no-mail bootstrap state admin=%d active grants=%d", admin, grants)
	}
}

func TestInstanceOwnerCLIDispatchesExplicitRecoveryDescribe(t *testing.T) {
	database := filepath.Join(t.TempDir(), "recovery-dispatch.db")
	db := openOwnerCLITestDatabase(t, database)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	handled, err := runInstanceCLI([]string{"instance", "recovery", "describe", "--database", database}, strings.NewReader(""), &output)
	if !handled || err != nil {
		t.Fatalf("recovery describe handled=%v err=%v", handled, err)
	}
	if !strings.Contains(output.String(), `"version": 1`) || !strings.Contains(output.String(), `"actors": []`) {
		t.Fatalf("recovery describe output=%q", output.String())
	}
}

func TestNoMailAccountCreateLookupAndSeparatePasskeyGrant(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db := openOwnerCLITestDatabase(t, database)
	t.Setenv("COOKBOOK_ADMIN_EMAIL", "Bootstrap@example.test")
	t.Setenv("COOKBOOK_ADMIN_NAME", "Bootstrap Admin")
	if err := provisionAdministrator(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", database)
	t.Setenv("COOKBOOK_FEDERATION_ORIGIN", "")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "")
	for _, key := range []string{"COOKBOOK_SMTP_HOST", "COOKBOOK_SMTP_FROM", "COOKBOOK_SMTP_USERNAME", "COOKBOOK_SMTP_PASSWORD"} {
		t.Setenv(key, "")
	}

	var createOutput bytes.Buffer
	createArgs := []string{"instance", "account", "create", "--email", " MEMBER@EXAMPLE.TEST ", "--display-name", "  Household Member  "}
	if handled, err := runInstanceCLI(createArgs, strings.NewReader("CREATE ACCOUNT\n"), &createOutput); !handled || err != nil {
		t.Fatalf("account create handled=%v err=%v output=%q", handled, err, createOutput.String())
	}
	if !strings.Contains(createOutput.String(), "ordinary local account") || !strings.Contains(createOutput.String(), "Account created:") || !strings.Contains(createOutput.String(), `"email":"member@example.test"`) || !strings.Contains(createOutput.String(), `"display_name":"Household Member"`) {
		t.Fatalf("account create output=%q", createOutput.String())
	}
	var createdIdentity struct {
		ID          string `json:"account_id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	resultJSON := createOutput.String()[strings.LastIndex(createOutput.String(), "Account created:\n")+len("Account created:\n"):]
	if err := json.Unmarshal([]byte(strings.TrimSpace(resultJSON)), &createdIdentity); err != nil {
		t.Fatalf("parse create result identity %q: %v", resultJSON, err)
	}
	accountID := createdIdentity.ID
	if accountID == "" {
		t.Fatalf("account create output omitted account ID: %q", createOutput.String())
	}

	var lookupOutput bytes.Buffer
	if handled, err := runInstanceCLI([]string{"instance", "account", "lookup", "--email", "MEMBER@example.test"}, strings.NewReader(""), &lookupOutput); !handled || err != nil {
		t.Fatalf("created account lookup handled=%v err=%v output=%q", handled, err, lookupOutput.String())
	}
	if want := formatInstanceAccountIdentity(instanceAccountIdentity{ID: accountID, Email: "member@example.test", DisplayName: "Household Member"}) + "\n"; !strings.HasSuffix(lookupOutput.String(), want) {
		t.Fatalf("lookup output=%q want suffix=%q", lookupOutput.String(), want)
	}

	check := openOwnerCLITestDatabase(t, database)
	var createdRole, credentials, sessions, grants, actorBindings, actorRegistry int
	var owner sql.NullString
	var auditActor sql.NullString
	if err := check.QueryRow(`SELECT is_admin FROM users WHERE id=?`, accountID).Scan(&createdRole); err != nil {
		t.Fatal(err)
	}
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id=?`:      &credentials,
		`SELECT COUNT(*) FROM sessions WHERE user_id=?`:                  &sessions,
		`SELECT COUNT(*) FROM passkey_enrollment_grants WHERE user_id=?`: &grants,
		`SELECT COUNT(*) FROM person_actors WHERE user_id=?`:             &actorBindings,
		`SELECT COUNT(*) FROM person_actor_ids`:                          &actorRegistry,
	} {
		if err := check.QueryRow(query, accountID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := check.QueryRow(`SELECT owner_user_id FROM instance_authority WHERE singleton_id=1`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT actor_id FROM audit_events WHERE action='instance_account.create' AND resource_id=?`, accountID).Scan(&auditActor); err != nil {
		t.Fatal(err)
	}
	if createdRole != 0 || credentials != 0 || sessions != 0 || grants != 0 || actorBindings != 0 || actorRegistry != 0 || owner.Valid || auditActor.Valid {
		t.Fatalf("pre-grant account role=%d credentials=%d sessions=%d grants=%d actorBindings=%d actorRegistry=%d owner=%v auditActor=%v", createdRole, credentials, sessions, grants, actorBindings, actorRegistry, owner, auditActor)
	}
	if err := check.Close(); err != nil {
		t.Fatal(err)
	}

	var grantOutput bytes.Buffer
	confirm := "ISSUE PASSKEY ENROLLMENT " + accountID + "\n"
	if handled, err := runInstanceCLI([]string{"instance", "passkey-enrollment", "issue", "--account-id", accountID}, strings.NewReader(confirm), &grantOutput); !handled || err != nil {
		t.Fatalf("separate passkey grant handled=%v err=%v output=%q", handled, err, grantOutput.String())
	}
	grantCheck := openOwnerCLITestDatabase(t, database)
	defer grantCheck.Close()
	if err := grantCheck.QueryRow(`SELECT COUNT(*) FROM passkey_enrollment_grants WHERE user_id=? AND consumed_at IS NULL AND revoked_at IS NULL`, accountID).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("separate grant count=%d err=%v", grants, err)
	}
}

func TestInstanceAccountCreateRequiresExclusiveLockAndDoesNotCreateMissingDatabase(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db := openOwnerCLITestDatabase(t, database)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('bootstrap','bootstrap@example.test','Bootstrap',1,'now')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", database)
	shared, err := acquireMaintenanceLock(database, maintenanceLockShared)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	args := []string{"instance", "account", "create", "--email", "member@example.test", "--display-name", "Member"}
	lockPath, err := maintenanceLockPath(database)
	if err != nil {
		t.Fatal(err)
	}
	lockInfoBefore, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lockContentsBefore, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if handled, err := runInstanceCLI(args, strings.NewReader("CREATE ACCOUNT\n"), &output); !handled || !errors.Is(err, ErrMaintenanceLockHeld) {
		t.Fatalf("create while server lock held handled=%v err=%v output=%q", handled, err, output.String())
	}
	lockInfoAfter, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(lockInfoBefore, lockInfoAfter) {
		t.Fatalf("failed create did not preserve stable maintenance lock: before=%v after=%v err=%v", lockInfoBefore, lockInfoAfter, err)
	}
	lockContentsAfter, err := os.ReadFile(lockPath)
	if err != nil || !bytes.Equal(lockContentsBefore, lockContentsAfter) {
		t.Fatalf("failed create changed stable maintenance lock contents before=%q after=%q err=%v", lockContentsBefore, lockContentsAfter, err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	check := openOwnerCLITestDatabase(t, database)
	var accounts, audits int
	if err := check.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='instance_account.create'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := check.Close(); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || audits != 0 {
		t.Fatalf("blocked account create changed accounts=%d audits=%d", accounts, audits)
	}

	missing := filepath.Join(t.TempDir(), "missing.db")
	t.Setenv("COOKBOOK_DATABASE", missing)
	if handled, err := runInstanceCLI(args, strings.NewReader("CREATE ACCOUNT\n"), &output); !handled || err == nil {
		t.Fatalf("missing database create handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("account create created missing database: stat error=%v", err)
	}
	if _, err := os.Stat(missing + ".maintenance.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database create left a lock sidecar: stat error=%v", err)
	}

	emptyDatabase := filepath.Join(t.TempDir(), "empty.db")
	empty := openOwnerCLITestDatabase(t, emptyDatabase)
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", emptyDatabase)
	if handled, err := runInstanceCLI(args, strings.NewReader("CREATE ACCOUNT\n"), &output); !handled || !errors.Is(err, ErrInstanceAccountBootstrapFirst) {
		t.Fatalf("initialized empty database create handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(emptyDatabase + ".maintenance.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty instance create left a lock sidecar: stat error=%v", err)
	}

	uninitializedDatabase := filepath.Join(t.TempDir(), "uninitialized.db")
	if err := os.WriteFile(uninitializedDatabase, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", uninitializedDatabase)
	if handled, err := runInstanceCLI(args, strings.NewReader("CREATE ACCOUNT\n"), &output); !handled || err == nil {
		t.Fatalf("uninitialized database create handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(uninitializedDatabase + ".maintenance.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninitialized database create left a lock sidecar: stat error=%v", err)
	}
	if info, err := os.Stat(uninitializedDatabase); err != nil || info.Size() != 0 {
		t.Fatalf("uninitialized database changed during preflight: info=%v err=%v", info, err)
	}

	malformedDatabase := filepath.Join(t.TempDir(), "malformed.db")
	malformed, err := sql.Open("sqlite", malformedDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := malformed.Exec(`CREATE VIEW users AS SELECT 'existing' AS id,'existing@example.test' AS email,'Existing' AS display_name,0 AS is_admin,'now' AS created_at; CREATE TABLE audit_events(id TEXT PRIMARY KEY,actor_id TEXT,action TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,response_status INTEGER NOT NULL,created_at TEXT NOT NULL)`); err != nil {
		malformed.Close()
		t.Fatal(err)
	}
	if err := malformed.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", malformedDatabase)
	if handled, err := runInstanceCLI(args, strings.NewReader("CREATE ACCOUNT\n"), &output); !handled || !errors.Is(err, ErrInstanceAccountDatabaseSchema) {
		t.Fatalf("malformed schema create handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(malformedDatabase + ".maintenance.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed schema create left a lock sidecar: stat error=%v", err)
	}
}

func TestInstanceAccountCLIJSONEscapesTerminalSensitiveIdentityText(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db := openOwnerCLITestDatabase(t, database)
	unsafeLookupName := "Lookup Member\n\x1b[31mRED"
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('unsafe','unsafe@example.test',?,0,'now')`, unsafeLookupName); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", database)
	var lookupOutput bytes.Buffer
	if handled, err := runInstanceCLI([]string{"instance", "account", "lookup", "--email", "unsafe@example.test"}, strings.NewReader(""), &lookupOutput); !handled || err != nil {
		t.Fatalf("unsafe-name lookup handled=%v err=%v output=%q", handled, err, lookupOutput.String())
	}
	if strings.Contains(lookupOutput.String(), "\x1b") || strings.Contains(lookupOutput.String(), "Lookup Member\n") {
		t.Fatalf("lookup emitted raw terminal-sensitive identity: %q", lookupOutput.String())
	}
	var lookedUp struct {
		ID          string `json:"account_id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(lookupOutput.Bytes(), &lookedUp); err != nil || lookedUp.ID != "unsafe" || lookedUp.Email != "unsafe@example.test" || lookedUp.DisplayName != unsafeLookupName {
		t.Fatalf("lookup JSON identity=%+v err=%v output=%q", lookedUp, err, lookupOutput.String())
	}

	unsafeCreateName := "Created Member\n\x1b[2J"
	var createOutput bytes.Buffer
	args := []string{"instance", "account", "create", "--email", "created@example.test", "--display-name", unsafeCreateName}
	if handled, err := runInstanceCLI(args, strings.NewReader("CREATE ACCOUNT\n"), &createOutput); !handled || err != nil {
		t.Fatalf("unsafe-name create handled=%v err=%v output=%q", handled, err, createOutput.String())
	}
	if strings.Contains(createOutput.String(), "\x1b") || strings.Contains(createOutput.String(), "Created Member\n") {
		t.Fatalf("create prompt/result emitted raw terminal-sensitive identity: %q", createOutput.String())
	}
	if strings.Count(createOutput.String(), "\n") != 2 {
		t.Fatalf("unsafe display name injected terminal lines: %q", createOutput.String())
	}
	resultJSON := createOutput.String()[strings.LastIndex(createOutput.String(), "Account created:\n")+len("Account created:\n"):]
	var created struct {
		ID          string `json:"account_id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(resultJSON)), &created); err != nil || created.ID == "" || created.Email != "created@example.test" || created.DisplayName != unsafeCreateName {
		t.Fatalf("create JSON identity=%+v err=%v output=%q", created, err, createOutput.String())
	}
}
