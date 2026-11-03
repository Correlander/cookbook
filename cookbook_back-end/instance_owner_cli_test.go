package main

import (
	"bytes"
	"context"
	"database/sql"
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
	if handled, err := runInstanceOwnerCLI(args, strings.NewReader("NO\n"), &output); !handled || err == nil {
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
	if handled, err := runInstanceOwnerCLI(args, strings.NewReader("CLAIM target\n"), &output); !handled || !errors.Is(err, ErrMaintenanceLockHeld) {
		t.Fatalf("claim with running server lock handled=%v err=%v", handled, err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if handled, err := runInstanceOwnerCLI(args, strings.NewReader("CLAIM target\n"), &output); !handled || err != nil {
		t.Fatalf("confirmed claim handled=%v err=%v output=%s", handled, err, output.String())
	}
	check := openOwnerCLITestDatabase(t, database)
	defer check.Close()
	var owner string
	var admin int
	if err := check.QueryRow(`SELECT a.owner_user_id,u.is_admin FROM instance_authority a JOIN users u ON u.id=a.owner_user_id`).Scan(&owner, &admin); err != nil || owner != "target" || admin != 0 {
		t.Fatalf("owner=%q admin=%d err=%v; claim must not grant admin", owner, admin, err)
	}
	if handled, err := runInstanceOwnerCLI(args, strings.NewReader("CLAIM target\n"), &output); !handled || err == nil || !strings.Contains(err.Error(), "already claimed") {
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
	if handled, err := runInstanceOwnerCLI(args, strings.NewReader("RECOVER target\n"), &output); !handled || err != nil {
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
		if handled, err := runInstanceOwnerCLI(tc.args, strings.NewReader("CLAIM target\n"), &bytes.Buffer{}); !handled || err == nil || !strings.Contains(err.Error(), tc.want) {
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
	if handled, err := runInstanceOwnerCLI(args, strings.NewReader("ISSUE PASSKEY ENROLLMENT target\n"), &output); !handled || err != nil {
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
	if handled, err := runInstanceOwnerCLI([]string{"instance", "passkey-enrollment", "revoke", "--account-id", "target"}, strings.NewReader("REVOKE PASSKEY ENROLLMENT target\n"), &revokeOutput); !handled || err != nil {
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
