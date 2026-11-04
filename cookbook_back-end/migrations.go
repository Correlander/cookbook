package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const migrationLedgerDDL = `CREATE TABLE schema_migrations (
	version INTEGER NOT NULL PRIMARY KEY CHECK (version > 0),
	name TEXT NOT NULL,
	checksum TEXT NOT NULL,
	applied_at TEXT NOT NULL
)`

// Keep this registry ordered and contiguous. Adding a later migration changes
// this file, not database.go; applied definitions and their checksums are
// immutable. V1 is frozen once released; new behavior belongs in a later
// migration rather than changing the baseline for already-versioned databases.
const legacyBaselineFingerprint = "baseline-v1/current-schema-and-conditional-compatibility-repairs"

type migrationExecutor = sqliteExecutor

type schemaMigration struct {
	version  int
	name     string
	checksum string
	apply    func(context.Context, migrationExecutor) error
}

func registeredSchemaMigrations() []schemaMigration {
	return []schemaMigration{{
		version:  1,
		name:     "legacy-baseline",
		checksum: migrationChecksum("legacy-baseline", legacyBaselineDefinition()),
		apply:    applyLegacyBaseline,
	}, {
		version:  2,
		name:     "instance-owner-authority",
		checksum: migrationChecksum("instance-owner-authority", instanceOwnerSchemaDDL),
		apply:    ApplyInstanceOwnerDDL,
	}, {
		version:  3,
		name:     "recipe-publication-policy",
		checksum: migrationChecksum("recipe-publication-policy", recipePublicationPolicyDDL+"\x00"+recipePublicationPolicyBackfillSQL),
		apply:    ApplyRecipePublicationPolicyMigration,
	}, {
		version:  4,
		name:     "offline-passkey-enrollment",
		checksum: migrationChecksum("offline-passkey-enrollment", passkeyEnrollmentSchemaDDL),
		apply:    ApplyPasskeyEnrollmentMigration,
	}, {
		version:  5,
		name:     "person-actor-identity",
		checksum: migrationChecksum("person-actor-identity", personActorSchemaDDL),
		apply:    ApplyPersonActorMigration,
	}, {
		version:  6,
		name:     "personal-library-bookmarks",
		checksum: migrationChecksum("personal-library-bookmarks", recipeBookmarkSchemaDDL+"\x00"+recipeBookmarkLimitTriggerDDL),
		apply:    ApplyRecipeBookmarkMigration,
	}, {
		version:  7,
		name:     "creator-profile-household-foundation",
		checksum: migrationChecksum("creator-profile-household-foundation", creatorProfileFoundationDDL),
		apply:    ApplyCreatorProfileFoundationMigration,
	}, {
		version:  8,
		name:     "personal-library-cookbook-bookmarks",
		checksum: migrationChecksum("personal-library-cookbook-bookmarks", cookbookBookmarkSchemaDDL+"\x00"+cookbookBookmarkLimitTriggerDDL),
		apply:    ApplyCookbookBookmarkMigration,
	}, {
		version:  9,
		name:     "library-remote-search-projection",
		checksum: migrationChecksum("library-remote-search-projection", libraryRemoteRecipeSearchProjectionDDL+"\x00"+libraryRemoteRecipeSearchInvalidationTriggerDDL),
		apply:    applyLibraryRemoteRecipeSearchProjectionMigration,
	}, {
		version:  10,
		name:     "personal-library-course-bookmarks",
		checksum: migrationChecksum("personal-library-course-bookmarks", courseBookmarkSchemaDDL+"\x00"+courseBookmarkLimitTriggerDDL),
		apply:    ApplyCourseBookmarkMigration,
	}, {
		version:  11,
		name:     "federation-resource-limits",
		checksum: migrationChecksum("federation-resource-limits", federationResourceLimitsDDL+"\x00"+federationReceivedEventsV11DDL+"\x00"+federationNonceResourceSchemaDDL),
		apply:    applyFederationResourceLimitsMigration,
	}, {
		version:  12,
		name:     "federation-retention-receipts",
		checksum: migrationChecksum("federation-retention-receipts", federationRetentionReceiptsDDL),
		apply:    applyFederationRetentionReceiptsMigration,
	}, {
		version:  13,
		name:     "private-remote-recipe-snapshots",
		checksum: migrationChecksum("private-remote-recipe-snapshots", libraryRemoteSnapshotsDDL),
		apply:    applyLibraryRemoteSnapshotsMigration,
	}}
}

const libraryRemoteRecipeSearchProjectionDDL = `CREATE TABLE federation_remote_recipe_search (
	object_id TEXT PRIMARY KEY REFERENCES federation_remote_recipes(object_id) ON DELETE CASCADE,
	projection_version INTEGER NOT NULL,
	available INTEGER NOT NULL CHECK(available IN (0,1)),
	title TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	tags_json TEXT NOT NULL DEFAULT '[]'
)`

const libraryRemoteRecipeSearchInvalidationTriggerDDL = `CREATE TRIGGER federation_remote_recipe_search_invalidate_after_update
AFTER UPDATE ON federation_remote_recipes
BEGIN
	DELETE FROM federation_remote_recipe_search WHERE object_id=OLD.object_id;
END`

func applyLibraryRemoteRecipeSearchProjectionMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, libraryRemoteRecipeSearchProjectionDDL); err != nil {
		return fmt.Errorf("create remote recipe search projection: %w", err)
	}
	if _, err := exec.ExecContext(ctx, libraryRemoteRecipeSearchInvalidationTriggerDDL); err != nil {
		return fmt.Errorf("create remote recipe search invalidation trigger: %w", err)
	}
	return nil
}

func legacyBaselineDefinition() string {
	parts := []string{legacyBaselineFingerprint, schema}
	for _, column := range legacyCompatibilityColumns() {
		parts = append(parts, column.table, column.name, column.declaration)
	}
	parts = append(parts, sessionIdentifierRepairSQL, sessionIdentifierIndexSQL, recipeTrashIndexSQL, pendingInvitationRepairSQL, pendingInvitationIndexSQL)
	return strings.Join(parts, "\x00")
}

func migrationChecksum(name, definition string) string {
	digest := sha256.Sum256([]byte(name + "\x00" + definition))
	return hex.EncodeToString(digest[:])
}

func validateMigrationRegistry(migrations []schemaMigration) error {
	seenNames := make(map[string]struct{}, len(migrations))
	for i, migration := range migrations {
		wantVersion := i + 1
		if migration.version != wantVersion {
			return fmt.Errorf("migration registry is not contiguous: position %d has version %d, want %d", i, migration.version, wantVersion)
		}
		if strings.TrimSpace(migration.name) == "" || migration.apply == nil {
			return fmt.Errorf("migration %d must have a name and implementation", migration.version)
		}
		if _, exists := seenNames[migration.name]; exists {
			return fmt.Errorf("migration name %q is duplicated", migration.name)
		}
		seenNames[migration.name] = struct{}{}
		if len(migration.checksum) != sha256.Size*2 {
			return fmt.Errorf("migration %d has an invalid checksum length", migration.version)
		}
		if _, err := hex.DecodeString(migration.checksum); err != nil {
			return fmt.Errorf("migration %d has an invalid checksum: %w", migration.version, err)
		}
	}
	if len(migrations) == 0 || migrations[0].version != 1 {
		return errors.New("migration registry must begin with baseline version 1")
	}
	return nil
}

func migrateDatabase(ctx context.Context, db *sql.DB) error {
	return runSchemaMigrations(ctx, db, registeredSchemaMigrations())
}

// runSchemaMigrations serializes startup migration decisions with BEGIN
// IMMEDIATE. Each applied version and its ledger row commit together. Keeping
// one migration per transaction means a failed later step does not undo an
// already committed predecessor.
func runSchemaMigrations(ctx context.Context, db *sql.DB, migrations []schemaMigration) error {
	if err := validateMigrationRegistry(migrations); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	for {
		appliedVersion := 0
		err := withImmediateMigrationTransaction(ctx, conn, func(exec migrationExecutor) error {
			ledgerExists, err := migrationLedgerExists(ctx, exec)
			if err != nil {
				return err
			}
			if !ledgerExists {
				if err := validateUnversionedSchema(ctx, exec); err != nil {
					return err
				}
				migration := migrations[0]
				if _, err := exec.ExecContext(ctx, migrationLedgerDDL); err != nil {
					return fmt.Errorf("create migration ledger: %w", err)
				}
				if err := migration.apply(ctx, exec); err != nil {
					return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
				}
				if err := recordMigration(ctx, exec, migration); err != nil {
					return err
				}
				appliedVersion = migration.version
				return nil
			}

			appliedVersion, err = readAndValidateMigrationLedger(ctx, exec, migrations)
			if err != nil {
				return err
			}
			if err := validateBaselineSchema(ctx, exec); err != nil {
				return err
			}
			if appliedVersion == len(migrations) {
				return nil
			}
			migration := migrations[appliedVersion]
			if err := migration.apply(ctx, exec); err != nil {
				return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
			}
			if err := recordMigration(ctx, exec, migration); err != nil {
				return err
			}
			appliedVersion = migration.version
			return nil
		})
		if err != nil {
			return err
		}
		if appliedVersion == len(migrations) {
			return nil
		}
	}
}

func withImmediateMigrationTransaction(ctx context.Context, conn *sql.Conn, apply func(migrationExecutor) error) (err error) {
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin immediate migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, rollbackErr := conn.ExecContext(rollbackCtx, `ROLLBACK`); rollbackErr != nil && err == nil {
			err = fmt.Errorf("rollback migration transaction: %w", rollbackErr)
		}
	}()
	if err := apply(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit migration transaction: %w", err)
	}
	committed = true
	return nil
}

func migrationLedgerExists(ctx context.Context, exec migrationExecutor) (bool, error) {
	var objectType string
	err := exec.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name='schema_migrations'`).Scan(&objectType)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect migration ledger: %w", err)
	}
	if objectType != "table" {
		return false, fmt.Errorf("migration ledger name is occupied by a %s, want table", objectType)
	}
	return true, nil
}

func readAndValidateMigrationLedger(ctx context.Context, exec migrationExecutor, migrations []schemaMigration) (int, error) {
	columns, err := tableColumnDetails(ctx, exec, "schema_migrations")
	if err != nil {
		return 0, err
	}
	wantColumns := []struct {
		name     string
		typeName string
		notNull  int
		primary  int
	}{
		{name: "version", typeName: "INTEGER", notNull: 1, primary: 1},
		{name: "name", typeName: "TEXT", notNull: 1},
		{name: "checksum", typeName: "TEXT", notNull: 1},
		{name: "applied_at", typeName: "TEXT", notNull: 1},
	}
	if len(columns) != len(wantColumns) {
		return 0, fmt.Errorf("malformed migration ledger: found %d columns, want %d", len(columns), len(wantColumns))
	}
	for i, want := range wantColumns {
		got := columns[i]
		if got.name != want.name || !strings.EqualFold(got.typeName, want.typeName) || got.notNull != want.notNull || got.primaryKey != want.primary {
			return 0, fmt.Errorf("malformed migration ledger column %d: got %s %s notnull=%d pk=%d", i, got.name, got.typeName, got.notNull, got.primaryKey)
		}
	}

	rows, err := exec.QueryContext(ctx, `SELECT version,name,checksum,applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return 0, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	version := 0
	for rows.Next() {
		var gotVersion int
		var name, checksum, appliedAt string
		if err := rows.Scan(&gotVersion, &name, &checksum, &appliedAt); err != nil {
			return 0, fmt.Errorf("read migration ledger row: %w", err)
		}
		if gotVersion > len(migrations) {
			return 0, fmt.Errorf("database schema version %d is newer than this binary (%d)", gotVersion, len(migrations))
		}
		wantVersion := version + 1
		if gotVersion != wantVersion {
			return 0, fmt.Errorf("malformed migration ledger: found version %d, want contiguous version %d", gotVersion, wantVersion)
		}
		want := migrations[gotVersion-1]
		if name != want.name {
			return 0, fmt.Errorf("migration ledger version %d has name %q, want %q", gotVersion, name, want.name)
		}
		if checksum != want.checksum {
			return 0, fmt.Errorf("migration ledger checksum mismatch at version %d (%s)", gotVersion, name)
		}
		if _, err := time.Parse(time.RFC3339Nano, appliedAt); err != nil {
			return 0, fmt.Errorf("migration ledger version %d has invalid applied_at timestamp: %w", gotVersion, err)
		}
		version = gotVersion
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read migration ledger rows: %w", err)
	}
	if version == 0 {
		return 0, errors.New("malformed migration ledger: no baseline version is recorded")
	}
	return version, nil
}

type tableColumn struct {
	name       string
	typeName   string
	notNull    int
	primaryKey int
}

func tableColumnDetails(ctx context.Context, exec migrationExecutor, table string) ([]tableColumn, error) {
	rows, err := exec.QueryContext(ctx, `SELECT name,type,"notnull",pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("inspect table %q: %w", table, err)
	}
	defer rows.Close()
	columns := []tableColumn{}
	for rows.Next() {
		var column tableColumn
		if err := rows.Scan(&column.name, &column.typeName, &column.notNull, &column.primaryKey); err != nil {
			return nil, fmt.Errorf("read table %q columns: %w", table, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read table %q columns: %w", table, err)
	}
	return columns, nil
}

func validateUnversionedSchema(ctx context.Context, exec migrationExecutor) error {
	rows, err := exec.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("inspect unversioned database: %w", err)
	}
	tables := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("read unversioned database tables: %w", err)
		}
		tables[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read unversioned database tables: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close unversioned database table list: %w", err)
	}
	if len(tables) == 0 {
		return nil // Fresh SQLite database.
	}
	markers := map[string][]string{
		"instance_settings":    {"id", "public_instance", "registration_policy"},
		"users":                {"id", "email", "display_name", "created_at"},
		"recipes":              {"id", "owner_id", "creator_name", "title", "description", "ingredients_json", "steps_json", "visibility", "edit_policy", "version", "created_at", "updated_at"},
		"sessions":             {"token_hash", "user_id", "expires_at", "created_at"},
		"cookbooks":            {"id", "owner_id", "title", "description", "visibility", "created_at", "updated_at"},
		"webauthn_credentials": {"credential_id", "user_id", "credential_json", "created_at", "last_used_at"},
		"email_challenges":     {"id", "email", "token_hash", "expires_at", "consumed_at", "attempts", "created_at"},
		"invitations":          {"id", "token_hash", "email", "invited_by", "expires_at", "accepted_at", "revoked_at", "created_at"},
	}
	columnsByTable := make(map[string][]string, len(markers))
	for table, required := range markers {
		if _, exists := tables[table]; !exists {
			continue
		}
		columns, err := tableColumnDetails(ctx, exec, table)
		if err != nil {
			return err
		}
		present := make(map[string]struct{}, len(columns))
		for _, column := range columns {
			present[column.name] = struct{}{}
			columnsByTable[table] = append(columnsByTable[table], column.name)
		}
		for _, column := range required {
			if _, exists := present[column]; !exists {
				return fmt.Errorf("unrecognized legacy schema: table %q lacks required column %q", table, column)
			}
		}
	}

	// The settings-only fixture is a specific supported pre-identity schema.
	// A users table by itself is too common to identify this application, so
	// require it to appear with another known Cookbook table signature.
	settingsOnly := len(tables) == 1 && len(columnsByTable["instance_settings"]) == 3 &&
		columnsByTable["instance_settings"][0] == "id" &&
		columnsByTable["instance_settings"][1] == "public_instance" &&
		columnsByTable["instance_settings"][2] == "registration_policy"
	usersWithKnownTable := false
	if _, hasUsers := tables["users"]; hasUsers {
		for _, companion := range []string{"instance_settings", "recipes", "sessions", "cookbooks", "webauthn_credentials", "invitations"} {
			if _, exists := tables[companion]; exists {
				usersWithKnownTable = true
				break
			}
		}
	}
	if !settingsOnly && !usersWithKnownTable {
		return errors.New("unrecognized unversioned database schema; refusing baseline migration")
	}
	return nil
}

func validateBaselineSchema(ctx context.Context, exec migrationExecutor) error {
	for table, required := range map[string][]string{
		"instance_settings": {"id", "public_instance", "registration_policy", "federation_enabled", "federation_origin"},
		"users":             {"id", "email", "display_name", "is_admin", "created_at"},
		"sessions":          {"id", "token_hash", "user_id", "expires_at", "created_at", "user_agent"},
		"recipes":           {"id", "owner_id", "creator_name", "source_recipe_id", "title", "description", "ingredients_json", "steps_json", "visibility", "version", "deleted_at", "created_at", "updated_at"},
		"recipe_revisions":  {"id", "recipe_id", "version", "editor_id", "title", "description", "ingredients_json", "steps_json", "visibility", "created_at"},
		"cookbooks":         {"id", "owner_id", "title", "visibility", "created_at", "updated_at"},
		"invitations":       {"id", "token_hash", "email", "invited_by", "expires_at", "accepted_at", "revoked_at", "created_at"},
	} {
		columns, err := tableColumnDetails(ctx, exec, table)
		if err != nil {
			return err
		}
		present := make(map[string]struct{}, len(columns))
		for _, column := range columns {
			present[column.name] = struct{}{}
		}
		for _, column := range required {
			if _, exists := present[column]; !exists {
				return fmt.Errorf("versioned database schema is missing %s.%s", table, column)
			}
		}
	}
	return nil
}

func recordMigration(ctx context.Context, exec migrationExecutor, migration schemaMigration) error {
	if _, err := exec.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.version, migration.name, migration.checksum, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %d (%s): %w", migration.version, migration.name, err)
	}
	return nil
}

type legacyColumnAddition struct{ table, name, declaration string }

func legacyCompatibilityColumns() []legacyColumnAddition {
	return []legacyColumnAddition{
		{"instance_settings", "registration_policy_configured", `INTEGER NOT NULL DEFAULT 0 CHECK (registration_policy_configured IN (0, 1))`},
		{"instance_settings", "revision_retention_days", `INTEGER NOT NULL DEFAULT 365 CHECK (revision_retention_days IN (0, 7, 30, 90, 365))`},
		{"instance_settings", "audit_retention_days", `INTEGER NOT NULL DEFAULT 365 CHECK (audit_retention_days IN (0, 7, 30, 90, 365))`},
		{"instance_settings", "federation_enabled", `INTEGER NOT NULL DEFAULT 0 CHECK (federation_enabled IN (0, 1))`},
		{"instance_settings", "federation_origin", `TEXT NOT NULL DEFAULT ''`},
		{"instance_settings", "federation_origin_locked", `INTEGER NOT NULL DEFAULT 0 CHECK (federation_origin_locked IN (0, 1))`},
		{"webauthn_credentials", "name", `TEXT NOT NULL DEFAULT 'Passkey'`},
		{"sessions", "id", `TEXT NOT NULL DEFAULT ''`},
		{"sessions", "user_agent", `TEXT NOT NULL DEFAULT ''`},
		{"users", "is_admin", `INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1))`},
		{"email_challenges", "display_name", `TEXT NOT NULL DEFAULT ''`},
		{"email_challenges", "invitation_id", `TEXT REFERENCES invitations(id)`},
		{"invitations", "cookbook_id", `TEXT REFERENCES cookbooks(id) ON DELETE CASCADE`},
		{"invitations", "role", `TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('viewer', 'editor'))`},
		{"recipes", "servings", `INTEGER NOT NULL DEFAULT 0 CHECK (servings BETWEEN 0 AND 1000)`},
		{"recipes", "prep_time_minutes", `INTEGER NOT NULL DEFAULT 0 CHECK (prep_time_minutes BETWEEN 0 AND 1440)`},
		{"recipes", "cook_time_minutes", `INTEGER NOT NULL DEFAULT 0 CHECK (cook_time_minutes BETWEEN 0 AND 1440)`},
		{"recipes", "tags_json", `TEXT NOT NULL DEFAULT '[]'`},
		{"recipes", "deleted_at", `TEXT`},
		{"recipes", "image_path", `TEXT NOT NULL DEFAULT ''`},
		{"recipes", "source_recipe_id", `TEXT NOT NULL DEFAULT ''`},
		{"recipe_revisions", "servings", `INTEGER NOT NULL DEFAULT 0 CHECK (servings BETWEEN 0 AND 1000)`},
		{"recipe_revisions", "prep_time_minutes", `INTEGER NOT NULL DEFAULT 0 CHECK (prep_time_minutes BETWEEN 0 AND 1440)`},
		{"recipe_revisions", "cook_time_minutes", `INTEGER NOT NULL DEFAULT 0 CHECK (cook_time_minutes BETWEEN 0 AND 1440)`},
		{"recipe_revisions", "tags_json", `TEXT NOT NULL DEFAULT '[]'`},
		{"recipe_revisions", "image_path", `TEXT NOT NULL DEFAULT ''`},
	}
}

const sessionIdentifierRepairSQL = `UPDATE sessions SET id=lower(hex(randomblob(16))) WHERE id=''`
const sessionIdentifierIndexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_id ON sessions(id)`
const recipeTrashIndexSQL = `CREATE INDEX IF NOT EXISTS idx_recipes_owner_deleted ON recipes(owner_id,deleted_at)`
const pendingInvitationRepairSQL = `UPDATE invitations AS older SET revoked_at=COALESCE(older.created_at,?) WHERE older.accepted_at IS NULL AND older.revoked_at IS NULL AND EXISTS (SELECT 1 FROM invitations AS newer WHERE COALESCE(newer.cookbook_id,'')=COALESCE(older.cookbook_id,'') AND lower(newer.email)=lower(older.email) AND newer.accepted_at IS NULL AND newer.revoked_at IS NULL AND (newer.created_at>older.created_at OR (newer.created_at=older.created_at AND newer.id>older.id)))`
const pendingInvitationIndexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_invitations_pending_email_scope ON invitations(COALESCE(cookbook_id,''),lower(email)) WHERE accepted_at IS NULL AND revoked_at IS NULL`

func applyLegacyBaseline(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply initial schema: %w", err)
	}
	for _, column := range legacyCompatibilityColumns() {
		var exists int
		if err := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, column.table, column.name).Scan(&exists); err != nil {
			return fmt.Errorf("inspect %s schema: %w", column.table, err)
		}
		if exists == 0 {
			if _, err := exec.ExecContext(ctx, `ALTER TABLE `+column.table+` ADD COLUMN `+column.name+` `+column.declaration); err != nil {
				return fmt.Errorf("upgrade %s schema: %w", column.table, err)
			}
		}
	}
	if _, err := exec.ExecContext(ctx, sessionIdentifierRepairSQL); err != nil {
		return fmt.Errorf("upgrade session identifiers: %w", err)
	}
	if _, err := exec.ExecContext(ctx, sessionIdentifierIndexSQL); err != nil {
		return fmt.Errorf("index session identifiers: %w", err)
	}
	if _, err := exec.ExecContext(ctx, recipeTrashIndexSQL); err != nil {
		return fmt.Errorf("index recipe trash: %w", err)
	}
	if _, err := exec.ExecContext(ctx, pendingInvitationRepairSQL, timestamp()); err != nil {
		return fmt.Errorf("deduplicate pending invitations: %w", err)
	}
	if _, err := exec.ExecContext(ctx, pendingInvitationIndexSQL); err != nil {
		return fmt.Errorf("index pending invitations: %w", err)
	}
	return nil
}
