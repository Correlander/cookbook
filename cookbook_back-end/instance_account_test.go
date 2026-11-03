package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLookupInstanceAccountByNormalizedExactEmailIsReadOnly(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db, err := openDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('boot-id','bootstrap@example.test','Bootstrap',1,'now')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	lookupDB, err := openAccountLookupDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	defer lookupDB.Close()
	account, err := lookupInstanceAccountByEmail(context.Background(), lookupDB, "  BOOTSTRAP@example.test ")
	if err != nil || account.ID != "boot-id" || account.Email != "bootstrap@example.test" || account.DisplayName != "Bootstrap" {
		t.Fatalf("account=%+v err=%v", account, err)
	}
	if _, err := lookupDB.Exec(`UPDATE users SET is_admin=0 WHERE id='boot-id'`); err == nil {
		t.Fatal("read-only account lookup database accepted a write")
	}
	var admin int
	if err := lookupDB.QueryRow(`SELECT is_admin FROM users WHERE id='boot-id'`).Scan(&admin); err != nil || admin != 1 {
		t.Fatalf("lookup changed account role: admin=%d err=%v", admin, err)
	}
	if _, err := lookupInstanceAccountByEmail(context.Background(), lookupDB, "other@example.test"); !errors.Is(err, ErrInstanceAccountNotFound) {
		t.Fatalf("unknown exact email error=%v", err)
	}
	for _, input := range []string{"", "Bootstrap <bootstrap@example.test>", "bootstrap@example.test,other@example.test", "missing-at-sign"} {
		if _, err := lookupInstanceAccountByEmail(context.Background(), lookupDB, input); err == nil {
			t.Errorf("lookup accepted invalid/non-exact address %q", input)
		}
	}
}

func TestOpenAccountLookupDatabaseDoesNotCreateOrMigrate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := openAccountLookupDatabase(missing); err == nil {
		t.Fatal("account lookup opened a nonexistent database")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup created missing database: stat error=%v", err)
	}
	for _, path := range []string{"", ":memory:", "file:existing.db"} {
		if _, err := openAccountLookupDatabase(path); err == nil {
			t.Errorf("lookup accepted unsupported database path %q", path)
		}
	}
}

func TestCreateInstanceAccountCreatesOrdinaryAccountAndAuditWithoutCredentials(t *testing.T) {
	db := accountCreationTestDatabase(t)
	created, err := createInstanceAccount(context.Background(), db, "  MEMBER@EXAMPLE.TEST ", "  Household Member  ")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Email != "member@example.test" || created.DisplayName != "Household Member" {
		t.Fatalf("created account=%+v", created)
	}
	lookedUp, err := lookupInstanceAccountByEmail(context.Background(), db, " MEMBER@example.test ")
	if err != nil || lookedUp != created {
		t.Fatalf("lookup after create=%+v err=%v want=%+v", lookedUp, err, created)
	}

	var isAdmin int
	var credentialCount, sessionCount, grantCount, actorCount int
	var auditActor sql.NullString
	var auditAction, auditResourceType, auditResourceID string
	var auditStatus int
	var ownerID sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT is_admin FROM users WHERE id=?`, created.ID).Scan(&isAdmin); err != nil {
		t.Fatal(err)
	}
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id=?`:      &credentialCount,
		`SELECT COUNT(*) FROM sessions WHERE user_id=?`:                  &sessionCount,
		`SELECT COUNT(*) FROM passkey_enrollment_grants WHERE user_id=?`: &grantCount,
		`SELECT COUNT(*) FROM person_actors WHERE user_id=?`:             &actorCount,
	} {
		if err := db.QueryRow(query, created.ID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(`SELECT actor_id,action,resource_type,resource_id,response_status FROM audit_events WHERE action='instance_account.create' AND resource_id=?`, created.ID).Scan(&auditActor, &auditAction, &auditResourceType, &auditResourceID, &auditStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&ownerID, &generation); err != nil {
		t.Fatal(err)
	}
	if isAdmin != 0 || credentialCount != 0 || sessionCount != 0 || grantCount != 0 || actorCount != 0 || auditActor.Valid || auditAction != "instance_account.create" || auditResourceType != "account" || auditResourceID != created.ID || auditStatus != 201 || ownerID.Valid || generation != 0 {
		t.Fatalf("account side effects admin=%d credentials=%d sessions=%d grants=%d actors=%d auditActor=%v auditAction=%q resource=%s/%s status=%d owner=%v generation=%d", isAdmin, credentialCount, sessionCount, grantCount, actorCount, auditActor, auditAction, auditResourceType, auditResourceID, auditStatus, ownerID, generation)
	}
}

func TestCreateInstanceAccountRejectsInvalidAndDuplicateIdentity(t *testing.T) {
	db := accountCreationTestDatabase(t)
	for _, tc := range []struct {
		email string
		name  string
	}{
		{email: "not-an-email", name: "Member"},
		{email: "member@example.test,other@example.test", name: "Member"},
		{email: "member@example.test", name: strings.Repeat("x", 101)},
	} {
		if _, err := createInstanceAccount(context.Background(), db, tc.email, tc.name); err == nil {
			t.Errorf("accepted invalid identity email=%q name bytes=%d", tc.email, len(tc.name))
		}
	}
	created, err := createInstanceAccount(context.Background(), db, "member@example.test", "Member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createInstanceAccount(context.Background(), db, " MEMBER@EXAMPLE.TEST ", "Another Name"); !errors.Is(err, ErrInstanceAccountAlreadyExists) {
		t.Fatalf("case-normalized duplicate error=%v", err)
	}
	var accounts, creationAudits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='instance_account.create'`).Scan(&creationAudits); err != nil {
		t.Fatal(err)
	}
	if accounts != 2 || creationAudits != 1 || created.ID == "" {
		t.Fatalf("duplicate create accounts=%d audits=%d createdID=%q", accounts, creationAudits, created.ID)
	}
}

func TestParseAccountCreateFlagsRejectsInvalidAndPrivilegedOptions(t *testing.T) {
	for _, tc := range []struct {
		args []string
	}{
		{args: nil},
		{args: []string{"--email", "person@example.test"}},
		{args: []string{"--display-name", "Person"}},
		{args: []string{"--email", "person@example.test", "--display-name", "Person", "--admin", "true"}},
		{args: []string{"--email", "person@example.test", "--display-name", "Person", "--is-owner", "true"}},
		{args: []string{"--email", "person@example.test", "--email", "other@example.test", "--display-name", "Person"}},
		{args: []string{"--email", "person@example.test", "--display-name", "Person", "--display-name", "Other"}},
		{args: []string{"--email", "person@example.test", "--display-name", "  "}},
	} {
		if _, _, err := parseAccountCreateFlags(tc.args); err == nil {
			t.Errorf("accepted invalid or privileged flags %v", tc.args)
		}
	}
	gotEmail, gotName, err := parseAccountCreateFlags([]string{"--display-name", "  Household Member  ", "--email", " MEMBER@EXAMPLE.TEST "})
	if err != nil || gotEmail != "member@example.test" || gotName != "Household Member" {
		t.Fatalf("normalized parsed identity email=%q name=%q err=%v", gotEmail, gotName, err)
	}
}

func TestCreateInstanceAccountSerializesDuplicateRace(t *testing.T) {
	db := accountCreationTestDatabase(t)
	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := createInstanceAccount(context.Background(), db, "race@example.test", "Race")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created, duplicates := 0, 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrInstanceAccountAlreadyExists):
			duplicates++
		default:
			t.Errorf("concurrent duplicate creation error=%v", err)
		}
	}
	var accounts, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='instance_account.create'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if created != 1 || duplicates != attempts-1 || accounts != 2 || audits != 1 {
		t.Fatalf("race created=%d duplicates=%d accounts=%d audits=%d", created, duplicates, accounts, audits)
	}
}

func TestCreateInstanceAccountRollsBackWhenAuditFails(t *testing.T) {
	db := accountCreationTestDatabase(t)
	if _, err := db.Exec(`CREATE TRIGGER reject_instance_account_audit BEFORE INSERT ON audit_events WHEN NEW.action='instance_account.create' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := createInstanceAccount(context.Background(), db, "member@example.test", "Member"); err == nil {
		t.Fatal("account creation succeeded without its audit event")
	}
	var accounts, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='instance_account.create'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || audits != 0 {
		t.Fatalf("audit rollback left accounts=%d audits=%d", accounts, audits)
	}
}

func TestCreateInstanceAccountRequiresBootstrapAndLeavesEmptyInstanceUntouched(t *testing.T) {
	database := filepath.Join(t.TempDir(), "empty.db")
	db, err := openDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := createInstanceAccount(context.Background(), db, "member@example.test", "Member"); !errors.Is(err, ErrInstanceAccountBootstrapFirst) {
		t.Fatalf("empty instance error=%v", err)
	}
	var accounts, audits, grants, credentials int
	var owner sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM passkey_enrollment_grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM webauthn_credentials`).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &generation); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 || audits != 0 || grants != 0 || credentials != 0 || owner.Valid || generation != 0 {
		t.Fatalf("bootstrap refusal changed state accounts=%d audits=%d grants=%d credentials=%d owner=%v generation=%d", accounts, audits, grants, credentials, owner, generation)
	}
}

func accountCreationTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := openOwnerCLITestDatabase(t, filepath.Join(t.TempDir(), "accounts.db"))
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('bootstrap','bootstrap@example.test','Bootstrap',1,'now')`); err != nil {
		t.Fatal(err)
	}
	return db
}
