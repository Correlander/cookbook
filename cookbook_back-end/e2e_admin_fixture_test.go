package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	e2eAdminActorFixtureOptIn = "COOKBOOK_E2E_ADMIN_ACTOR_FIXTURE"
	e2eAdminActorRunDir       = "COOKBOOK_E2E_RUN_DIR"
	e2eAdminActorDatabase     = "COOKBOOK_DATABASE"
)

type e2eAdminActor struct {
	id, email, displayName string
}

var e2eAdminActors = [...]e2eAdminActor{
	{"e2e-admin-course-attribution", "admin-course-attribution@example.test", "Course Attribution Admin"},
	{"e2e-admin-instance-workbench", "admin-instance-workbench@example.test", "Instance Workbench Admin"},
	{"e2e-admin-library-detail", "admin-library-detail@example.test", "Library Detail Admin"},
	{"e2e-admin-private-snapshot", "admin-private-snapshot@example.test", "Private Snapshot Admin"},
	{"e2e-admin-recipe-publication", "admin-recipe-publication@example.test", "Recipe Publication Admin"},
	{"e2e-admin-retention-peer-profile", "admin-retention-peer-profile@example.test", "Retention Peer Profile Admin"},
	{"e2e-admin-static-host", "admin-static-host@example.test", "Static Host Admin"},
	{"e2e-profile-courses-admin", "profile-courses-admin@example.test", "Profile Courses Admin"},
}

// This runner-only fixture adds administrator actors to the runner's disposable
// database. It deliberately creates no owner assignment or authentication state.
func TestPrepareE2EAdminActorFixture(t *testing.T) {
	if os.Getenv(e2eAdminActorFixtureOptIn) != "1" {
		t.Skip("E2E admin actor fixture requires explicit runner opt-in")
	}
	_, databasePath, err := validateE2EAdminActorFixtureEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(databasePath)
	if err != nil {
		t.Fatal("open runner database:", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := seedE2EAdminActors(context.Background(), db); err != nil {
		t.Fatal("seed runner admin actors:", err)
	}
}

func validateE2EAdminActorFixtureEnvironment() (runDir, databasePath string, err error) {
	runDir = strings.TrimSpace(os.Getenv(e2eAdminActorRunDir))
	databasePath = strings.TrimSpace(os.Getenv(e2eAdminActorDatabase))
	if runDir == "" || databasePath == "" {
		return "", "", errors.New("runner-owned directory and database are required")
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", "", errors.New("invalid runner-owned directory")
	}
	rootInfo, err := os.Lstat(absRunDir)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 ||
		rootInfo.Mode().Perm()&0077 != 0 ||
		!strings.HasPrefix(filepath.Base(absRunDir), "cookbook-e2e-") {
		return "", "", errors.New("fixture directory must be an existing private cookbook-e2e directory")
	}
	if filepath.Base(databasePath) != "cookbook.db" {
		return "", "", errors.New("fixture database must use the runner's disposable database path")
	}
	absDatabase, err := filepath.Abs(databasePath)
	if err != nil {
		return "", "", errors.New("invalid fixture database path")
	}
	relativeDatabase, err := filepath.Rel(absRunDir, absDatabase)
	if err != nil || relativeDatabase == "." || relativeDatabase == ".." ||
		strings.HasPrefix(relativeDatabase, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relativeDatabase) {
		return "", "", errors.New("fixture database must be inside the runner-owned directory")
	}
	resolvedRunDir, runErr := filepath.EvalSymlinks(absRunDir)
	resolvedDatabase, databaseErr := filepath.EvalSymlinks(absDatabase)
	if runErr != nil || databaseErr != nil {
		return "", "", errors.New("fixture database and directory must already exist")
	}
	resolvedRelativeDatabase, err := filepath.Rel(resolvedRunDir, resolvedDatabase)
	if err != nil || resolvedRelativeDatabase == "." || resolvedRelativeDatabase == ".." ||
		strings.HasPrefix(resolvedRelativeDatabase, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(resolvedRelativeDatabase) {
		return "", "", errors.New("resolved fixture database escaped the runner-owned directory")
	}
	databaseInfo, err := os.Lstat(absDatabase)
	if err != nil || !databaseInfo.Mode().IsRegular() || databaseInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("fixture database must be an existing regular file")
	}
	return resolvedRunDir, resolvedDatabase, nil
}

func seedE2EAdminActors(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fixture transaction: %w", err)
	}
	defer tx.Rollback()
	for _, actor := range e2eAdminActors {
		if _, err := tx.ExecContext(ctx, "INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES(?,?,?,1,?) ON CONFLICT(id) DO NOTHING",
			actor.id, actor.email, actor.displayName, timestamp()); err != nil {
			return fmt.Errorf("insert fixed admin actor %s: %w", actor.id, err)
		}
		var email, displayName string
		var isAdmin int
		if err := tx.QueryRowContext(ctx, "SELECT email,display_name,is_admin FROM users WHERE id=?", actor.id).
			Scan(&email, &displayName, &isAdmin); err != nil {
			return fmt.Errorf("verify fixed admin actor %s: %w", actor.id, err)
		}
		if email != actor.email || displayName != actor.displayName || isAdmin != 1 {
			return fmt.Errorf("fixed admin actor row conflicts for %s", actor.id)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fixture transaction: %w", err)
	}
	return nil
}

func TestE2EAdminActorFixtureIsIdempotentAndLeavesOwnershipAndAuthStateUntouched(t *testing.T) {
	db := testDatabase(t)
	if err := seedE2EAdminActors(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := seedE2EAdminActors(context.Background(), db); err != nil {
		t.Fatal("second seed:", err)
	}
	var users, sessions, challenges, rateLimits int
	var ownerID sql.NullString
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT owner_user_id FROM instance_authority WHERE singleton_id=1").Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM email_challenges").Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM auth_rate_limits").Scan(&rateLimits); err != nil {
		t.Fatal(err)
	}
	if users != len(e2eAdminActors) || ownerID.Valid || sessions != 0 || challenges != 0 || rateLimits != 0 {
		t.Fatalf("fixture state users=%d owner=%v sessions=%d challenges=%d rate_limits=%d",
			users, ownerID, sessions, challenges, rateLimits)
	}
}

func TestE2EAdminActorFixtureRollsBackOnConflictingEmailWithoutPromoting(t *testing.T) {
	t.Run("existing unrelated email", func(t *testing.T) {
		db := testDatabase(t)
		conflict := e2eAdminActors[len(e2eAdminActors)-1]
		if _, err := db.Exec("INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES(?,?,?,?,?)",
			"unrelated-account", conflict.email, "Unrelated Account", 0, timestamp()); err != nil {
			t.Fatal(err)
		}
		if err := seedE2EAdminActors(context.Background(), db); err == nil {
			t.Fatal("seed with conflicting unrelated email succeeded")
		}
		assertE2EActorSeedRolledBack(t, db, "unrelated-account")
	})
	t.Run("fixed id has a conflicting non-admin row", func(t *testing.T) {
		db := testDatabase(t)
		actor := e2eAdminActors[0]
		if _, err := db.Exec("INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES(?,?,?,?,?)",
			actor.id, "unrelated@example.test", "Unrelated Account", 0, timestamp()); err != nil {
			t.Fatal(err)
		}
		if err := seedE2EAdminActors(context.Background(), db); err == nil {
			t.Fatal("seed with conflicting fixed ID succeeded")
		}
		assertE2EActorSeedRolledBack(t, db, actor.id)
	})
}

func assertE2EActorSeedRolledBack(t *testing.T, db *sql.DB, preservedID string) {
	t.Helper()
	var users, admin int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT is_admin FROM users WHERE id=?", preservedID).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if users != 1 || admin != 0 {
		t.Fatalf("failed seed left users=%d preserved_admin=%d; want one unpromoted account", users, admin)
	}
}

func TestE2EAdminActorFixtureEnvironmentRejectsUnsafeTargets(t *testing.T) {
	parent := t.TempDir()
	privateDir := filepath.Join(parent, "cookbook-e2e-fixture")
	if err := os.Mkdir(privateDir, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(privateDir, "cookbook.db")
	if err := os.WriteFile(database, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eAdminActorRunDir, privateDir)
	t.Setenv(e2eAdminActorDatabase, database)
	if _, _, err := validateE2EAdminActorFixtureEnvironment(); err != nil {
		t.Fatalf("valid private runner paths rejected: %v", err)
	}

	insecureDir := filepath.Join(parent, "cookbook-e2e-insecure")
	if err := os.Mkdir(insecureDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eAdminActorRunDir, insecureDir)
	t.Setenv(e2eAdminActorDatabase, filepath.Join(insecureDir, "cookbook.db"))
	if _, _, err := validateE2EAdminActorFixtureEnvironment(); err == nil {
		t.Fatal("world-accessible runner directory accepted")
	}

	wrongName := filepath.Join(parent, "not-a-cookbook-e2e-directory")
	if err := os.Mkdir(wrongName, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wrongName, "cookbook.db"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eAdminActorRunDir, wrongName)
	t.Setenv(e2eAdminActorDatabase, filepath.Join(wrongName, "cookbook.db"))
	if _, _, err := validateE2EAdminActorFixtureEnvironment(); err == nil {
		t.Fatal("non-runner directory accepted")
	}

	runDirLink := filepath.Join(parent, "cookbook-e2e-link")
	if err := os.Symlink(privateDir, runDirLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eAdminActorRunDir, runDirLink)
	t.Setenv(e2eAdminActorDatabase, filepath.Join(runDirLink, "cookbook.db"))
	if _, _, err := validateE2EAdminActorFixtureEnvironment(); err == nil {
		t.Fatal("symlinked runner directory accepted")
	}

	t.Setenv(e2eAdminActorRunDir, privateDir)
	if err := os.Remove(database); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outside.db"), database); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.db"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(e2eAdminActorDatabase, database)
	if _, _, err := validateE2EAdminActorFixtureEnvironment(); err == nil {
		t.Fatal("symlinked database accepted")
	}

	t.Setenv(e2eAdminActorDatabase, filepath.Join(parent, "cookbook.db"))
	if _, _, err := validateE2EAdminActorFixtureEnvironment(); err == nil {
		t.Fatal("database outside runner directory accepted")
	}
}
