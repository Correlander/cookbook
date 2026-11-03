package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func migrationTestStep(version int, name string, apply func(context.Context, migrationExecutor) error) schemaMigration {
	return schemaMigration{
		version:  version,
		name:     name,
		checksum: migrationChecksum(name, "test-definition:"+name),
		apply:    apply,
	}
}

func migrationVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func migrationObjectExists(t *testing.T, db *sql.DB, objectType, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, objectType, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func rawMigrationDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestFreshDatabaseRecordsAllRegisteredMigrations(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != 3 {
		t.Fatalf("migration version=%d, want 3", got)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("migration ledger rows=%d err=%v, want 3", count, err)
	}
	for version, name := range map[int]string{1: "legacy-baseline", 2: "instance-owner-authority", 3: "recipe-publication-policy"} {
		var got string
		if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=?`, version).Scan(&got); err != nil || got != name {
			t.Fatalf("migration %d name=%q err=%v, want %q", version, got, err, name)
		}
	}
	var owner sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &generation); err != nil || owner.Valid || generation != 0 {
		t.Fatalf("fresh owner state=%v generation=%d err=%v, want unclaimed", owner, generation, err)
	}
}

func TestMigrationV1AndV2ChecksumsRemainImmutable(t *testing.T) {
	migrations := registeredSchemaMigrations()
	want := []string{
		"c77f29249dfa71f9c86b38cf6a3db05331f790156314647230d80868342f11ba",
		"4b2409dfd518076b3f4819fb1b010743acf2e8ab4febe4989e252569894e04f0",
	}
	if len(migrations) < len(want) {
		t.Fatalf("registered migrations=%d, want at least %d", len(migrations), len(want))
	}
	for index, checksum := range want {
		if migrations[index].checksum != checksum {
			t.Errorf("migration %d checksum changed: got %s want %s", index+1, migrations[index].checksum, checksum)
		}
	}
}

func TestCurrentPreLedgerDatabaseAdoptsWithoutChangingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current-preledger.db")
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('owner','owner@example.test','Owner',1,'created');
INSERT INTO recipes(id,owner_id,creator_name,title,visibility,version,created_at,updated_at) VALUES('recipe','owner','Claimed Creator','Preserve me','private',4,'created','updated');
INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at) VALUES('revision','recipe',4,'owner','Preserve me','','[]','[]','private','revision-time');
INSERT INTO sessions(id,token_hash,user_id,expires_at,created_at,user_agent) VALUES('stable-session','token-hash','owner','2099-01-01T00:00:00Z','session-time','agent');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var beforeRevisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='recipe'`).Scan(&beforeRevisions); err != nil || beforeRevisions != 1 {
		db.Close()
		t.Fatalf("pre-ledger revision count=%d err=%v", beforeRevisions, err)
	}
	if _, err := db.Exec(`DROP TABLE schema_migrations`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE recipe_publication_policy`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var public, admin, version, revisionCount int
	var creator, title, sessionID string
	if err := db.QueryRow(`SELECT s.public_instance,u.is_admin,r.creator_name,r.title,r.version,ss.id,(SELECT COUNT(*) FROM recipe_revisions rv WHERE rv.recipe_id=r.id) FROM instance_settings s JOIN users u ON u.id='owner' JOIN recipes r ON r.id='recipe' JOIN sessions ss ON ss.token_hash='token-hash' WHERE s.id=1`).Scan(&public, &admin, &creator, &title, &version, &sessionID, &revisionCount); err != nil {
		t.Fatal(err)
	}
	if public != 1 || admin != 1 || creator != "Claimed Creator" || title != "Preserve me" || version != 4 || sessionID != "stable-session" || revisionCount != beforeRevisions {
		t.Fatalf("adopted data changed: public=%d admin=%d creator=%q title=%q version=%d session=%q revisions=%d", public, admin, creator, title, version, sessionID, revisionCount)
	}
	if got := migrationVersion(t, db); got != 3 {
		t.Fatalf("adopted migration version=%d, want 3", got)
	}
	var owner sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &generation); err != nil || owner.Valid || generation != 0 {
		t.Fatalf("legacy authority mapping owner=%v generation=%d err=%v", owner, generation, err)
	}
}

func TestInstanceOwnerMigrationPreservesV1RowsAndLeavesAuthorityUnclaimed(t *testing.T) {
	db := rawMigrationDB(t, ":memory:")
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('legacy-admin','legacy-admin@example.test','Legacy Admin',1,'user-a'),
		('legacy-member','legacy-member@example.test','Legacy Member',0,'user-b');
		INSERT INTO recipes(id,owner_id,creator_name,title,version,created_at,updated_at) VALUES
		('legacy-recipe','legacy-admin','Unverified Creator','Keep me',4,'recipe-created','recipe-updated');
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at)
		VALUES('legacy-revision','legacy-recipe',4,'legacy-admin','Keep me','','[]','[]','private','revision-created');
		INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES
		('legacy-book','legacy-admin','Legacy book','book-created','book-updated');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at)
		VALUES('legacy-book','legacy-member','viewer','membership-created');`); err != nil {
		t.Fatal(err)
	}

	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()); err != nil {
		t.Fatal(err)
	}
	if got := migrationVersion(t, db); got != 3 {
		t.Fatalf("migration version=%d, want 3", got)
	}
	var owner sql.NullString
	var claimedAt sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,owner_claimed_at,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &claimedAt, &generation); err != nil || owner.Valid || claimedAt.Valid || generation != 0 {
		t.Fatalf("v1 authority mapping owner=%v claimed_at=%v generation=%d err=%v", owner, claimedAt, generation, err)
	}
	var adminFlag, memberFlag, recipeVersion int
	var recipeOwner, creator, title, bookOwner, memberRole string
	err := db.QueryRow(`SELECT admin.is_admin,member.is_admin,r.owner_id,r.creator_name,r.title,b.owner_id,m.role,r.version
		FROM users admin JOIN users member ON member.id='legacy-member'
		JOIN recipes r ON r.id='legacy-recipe'
		JOIN cookbooks b ON b.id='legacy-book'
		JOIN cookbook_memberships m ON m.cookbook_id=b.id AND m.user_id=member.id
		WHERE admin.id='legacy-admin'`).Scan(&adminFlag, &memberFlag, &recipeOwner, &creator, &title, &bookOwner, &memberRole, &recipeVersion)
	if err != nil {
		t.Fatal(err)
	}
	if adminFlag != 1 || memberFlag != 0 || recipeOwner != "legacy-admin" || creator != "Unverified Creator" || title != "Keep me" || bookOwner != "legacy-admin" || memberRole != "viewer" || recipeVersion != 4 {
		t.Fatalf("owner migration changed legacy rows: admin=%d member=%d recipe=%s/%s/%s v%d book=%s membership=%s", adminFlag, memberFlag, recipeOwner, creator, title, recipeVersion, bookOwner, memberRole)
	}
}

func TestVersionedDatabaseDoesNotReplayBaselineAndRejectsMissingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versioned.db")
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','created');
INSERT INTO recipes(id,owner_id,creator_name,title,version,created_at,updated_at) VALUES('recipe','owner','Owner','Preserve',2,'created','updated');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE recipe_revisions`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openDatabase(path)
	if err == nil {
		db.Close()
		t.Fatal("opened versioned database with missing core table")
	}
	if !strings.Contains(err.Error(), "recipe_revisions") {
		t.Fatalf("missing-table error=%v, want recipe_revisions", err)
	}
	check := rawMigrationDB(t, path)
	if !migrationObjectExists(t, check, "table", "schema_migrations") || migrationObjectExists(t, check, "table", "recipe_revisions") {
		t.Fatal("missing-table check unexpectedly repaired or removed schema")
	}
	var title string
	if err := check.QueryRow(`SELECT title FROM recipes WHERE id='recipe'`).Scan(&title); err != nil || title != "Preserve" {
		t.Fatalf("user data after schema rejection title=%q err=%v", title, err)
	}
}

func TestFutureMigrationRunsOnceAndFailureRollsBackStepAndLedger(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fail := true
	runs := 0
	step := migrationTestStep(4, "test-marker", func(ctx context.Context, exec migrationExecutor) error {
		runs++
		if _, err := exec.ExecContext(ctx, `CREATE TABLE migration_marker(value TEXT NOT NULL); INSERT INTO migration_marker(value) VALUES('applied')`); err != nil {
			return err
		}
		if fail {
			return errors.New("injected migration failure")
		}
		return nil
	})
	migrations := append(registeredSchemaMigrations(), step)
	if err := runSchemaMigrations(context.Background(), db, migrations); err == nil || !strings.Contains(err.Error(), "injected migration failure") {
		t.Fatalf("failed migration error=%v", err)
	}
	if migrationObjectExists(t, db, "table", "migration_marker") || migrationVersion(t, db) != 3 {
		t.Fatal("failed migration left partial DDL/data or advanced the ledger")
	}
	fail = false
	if err := runSchemaMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	if migrationVersion(t, db) != 4 || !migrationObjectExists(t, db, "table", "migration_marker") {
		t.Fatal("pending migration did not apply")
	}
	if err := runSchemaMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	var markerCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM migration_marker`).Scan(&markerCount); err != nil || markerCount != 1 || runs != 2 {
		t.Fatalf("replayed migration count=%d callback runs=%d err=%v", markerCount, runs, err)
	}
}

func TestFailedBaselineRollsBackLedgerAndSchema(t *testing.T) {
	db := rawMigrationDB(t, ":memory:")
	step := migrationTestStep(1, "failing-baseline", func(ctx context.Context, exec migrationExecutor) error {
		if _, err := exec.ExecContext(ctx, `CREATE TABLE partial_baseline(value TEXT)`); err != nil {
			return err
		}
		return errors.New("injected baseline failure")
	})
	if err := runSchemaMigrations(context.Background(), db, []schemaMigration{step}); err == nil {
		t.Fatal("expected baseline failure")
	}
	if migrationObjectExists(t, db, "table", "schema_migrations") || migrationObjectExists(t, db, "table", "partial_baseline") {
		t.Fatal("failed baseline left a ledger or partial schema")
	}
}

func TestMalformedLedgerIsRejectedBeforeSchemaMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed-ledger.db")
	seed := rawMigrationDB(t, path)
	if _, err := seed.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL);
INSERT INTO users VALUES('owner','owner@example.test','Owner','created');
CREATE TABLE schema_migrations(version TEXT,name TEXT,checksum TEXT,applied_at TEXT);
INSERT INTO schema_migrations VALUES('1','legacy-baseline','bad','not-a-time')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := openDatabase(path); err == nil {
		db.Close()
		t.Fatal("opened malformed migration ledger")
	}
	check := rawMigrationDB(t, path)
	if migrationObjectExists(t, check, "table", "instance_settings") {
		t.Fatal("baseline schema was applied despite malformed ledger")
	}
	var displayName string
	if err := check.QueryRow(`SELECT display_name FROM users WHERE id='owner'`).Scan(&displayName); err != nil || displayName != "Owner" {
		t.Fatalf("user data changed after malformed-ledger rejection: %q err=%v", displayName, err)
	}
}

func TestMigrationNameChecksumAndNewerVersionAreValidated(t *testing.T) {
	for _, test := range []struct {
		name       string
		update     string
		wantErrSub string
	}{
		{name: "name", update: `UPDATE schema_migrations SET name='other' WHERE version=2`, wantErrSub: `name`},
		{name: "checksum", update: `UPDATE schema_migrations SET checksum=lower(hex(randomblob(32))) WHERE version=2`, wantErrSub: `checksum mismatch`},
		{name: "newer version", update: `UPDATE schema_migrations SET version=999 WHERE version=3`, wantErrSub: `newer than this binary`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.db")
			db, err := openDatabase(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.update); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if db, err := openDatabase(path); err == nil || !strings.Contains(err.Error(), test.wantErrSub) {
				if db != nil {
					db.Close()
				}
				t.Fatalf("open error=%v, want substring %q", err, test.wantErrSub)
			}
		})
	}
}

func TestUnknownUnversionedSchemaIsPreservedAndRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.db")
	seed := rawMigrationDB(t, path)
	if _, err := seed.Exec(`CREATE TABLE unrelated_data(value TEXT NOT NULL); INSERT INTO unrelated_data VALUES('keep');`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := openDatabase(path); err == nil || !strings.Contains(err.Error(), "unrecognized unversioned") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("open error=%v, want unrecognized schema", err)
	}
	check := rawMigrationDB(t, path)
	if migrationObjectExists(t, check, "table", "schema_migrations") || migrationObjectExists(t, check, "table", "users") {
		t.Fatal("unknown database was modified")
	}
	var value string
	if err := check.QueryRow(`SELECT value FROM unrelated_data`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("unknown user data changed: value=%q err=%v", value, err)
	}
}

func TestUnrelatedUsersOnlySchemaIsPreservedAndRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unrelated-users.db")
	seed := rawMigrationDB(t, path)
	if _, err := seed.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL);
INSERT INTO users VALUES('unrelated-owner','unrelated@example.test','Unrelated','created');`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := openDatabase(path); err == nil || !strings.Contains(err.Error(), "unrecognized unversioned") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("open error=%v, want unrecognized schema", err)
	}

	check := rawMigrationDB(t, path)
	var tableCount int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil || tableCount != 1 {
		t.Fatalf("unrelated database table count=%d err=%v, want only original users table", tableCount, err)
	}
	var id, email, displayName, createdAt string
	if err := check.QueryRow(`SELECT id,email,display_name,created_at FROM users`).Scan(&id, &email, &displayName, &createdAt); err != nil {
		t.Fatal(err)
	}
	if id != "unrelated-owner" || email != "unrelated@example.test" || displayName != "Unrelated" || createdAt != "created" {
		t.Fatalf("unrelated user row changed: id=%q email=%q name=%q created=%q", id, email, displayName, createdAt)
	}
}

func TestConcurrentStartupSerializesBaselineMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first := rawMigrationDB(t, path)
	second := rawMigrationDB(t, path)
	if _, err := first.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, db := range []*sql.DB{first, second} {
		wait.Add(1)
		go func(db *sql.DB) {
			defer wait.Done()
			<-start
			errs <- runSchemaMigrations(context.Background(), db, registeredSchemaMigrations())
		}(db)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := migrationVersion(t, first); got != 3 {
		t.Fatalf("concurrent migration version=%d, want 3", got)
	}
}

func TestMigrationRegistryRejectsGapsAndDuplicateNames(t *testing.T) {
	noop := func(context.Context, migrationExecutor) error { return nil }
	if err := validateMigrationRegistry([]schemaMigration{
		migrationTestStep(1, "one", noop),
		migrationTestStep(3, "three", noop),
	}); err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("gapped registry error=%v", err)
	}
	if err := validateMigrationRegistry([]schemaMigration{
		migrationTestStep(1, "same", noop),
		migrationTestStep(2, "same", noop),
	}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate-name registry error=%v", err)
	}
}
