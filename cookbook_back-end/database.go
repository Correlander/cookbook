package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS instance_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  public_instance INTEGER NOT NULL DEFAULT 0 CHECK (public_instance IN (0, 1)),
  registration_policy TEXT NOT NULL DEFAULT 'invite_only' CHECK (registration_policy IN ('open', 'invite_only', 'disabled')),
  courses_enabled INTEGER NOT NULL DEFAULT 0 CHECK (courses_enabled IN (0, 1)),
  federation_enabled INTEGER NOT NULL DEFAULT 0 CHECK (federation_enabled IN (0, 1))
);
INSERT OR IGNORE INTO instance_settings (id) VALUES (1);
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS invitations (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL,
  cookbook_id TEXT REFERENCES cookbooks(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('viewer', 'editor')),
  invited_by TEXT NOT NULL REFERENCES users(id),
  expires_at TEXT NOT NULL,
  accepted_at TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_invitations_email_state ON invitations(email,accepted_at,revoked_at,expires_at);
CREATE TABLE IF NOT EXISTS email_challenges (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  invitation_id TEXT REFERENCES invitations(id),
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  consumed_at TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_challenges_email_created ON email_challenges(email, created_at DESC);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS webauthn_credentials (
  credential_id BLOB PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_webauthn_credentials_user ON webauthn_credentials(user_id);
CREATE TABLE IF NOT EXISTS webauthn_ceremonies (
  id TEXT PRIMARY KEY,
  user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL CHECK (purpose IN ('registration', 'login')),
  session_data TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_webauthn_ceremonies_expiry ON webauthn_ceremonies(expires_at);
CREATE TABLE IF NOT EXISTS cookbooks (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES users(id),
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public', 'unlisted')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cookbook_memberships (
  cookbook_id TEXT NOT NULL REFERENCES cookbooks(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('editor', 'viewer')),
  created_at TEXT NOT NULL,
  PRIMARY KEY (cookbook_id, user_id)
);
CREATE TABLE IF NOT EXISTS recipes (
  id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL REFERENCES users(id),
  creator_name TEXT NOT NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  ingredients_json TEXT NOT NULL DEFAULT '[]',
  steps_json TEXT NOT NULL DEFAULT '[]',
  servings INTEGER NOT NULL DEFAULT 0 CHECK (servings BETWEEN 0 AND 1000),
  prep_time_minutes INTEGER NOT NULL DEFAULT 0 CHECK (prep_time_minutes BETWEEN 0 AND 1440),
  cook_time_minutes INTEGER NOT NULL DEFAULT 0 CHECK (cook_time_minutes BETWEEN 0 AND 1440),
  tags_json TEXT NOT NULL DEFAULT '[]',
  visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public', 'unlisted')),
  edit_policy TEXT NOT NULL DEFAULT 'owner_only' CHECK (edit_policy IN ('owner_only', 'cookbook_editors')),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  deleted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS recipe_revisions (
  id TEXT PRIMARY KEY,
  recipe_id TEXT NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
  version INTEGER NOT NULL CHECK (version > 0),
  editor_id TEXT NOT NULL REFERENCES users(id),
  title TEXT NOT NULL,
  description TEXT NOT NULL,
  ingredients_json TEXT NOT NULL,
  steps_json TEXT NOT NULL,
  servings INTEGER NOT NULL DEFAULT 0 CHECK (servings BETWEEN 0 AND 1000),
  prep_time_minutes INTEGER NOT NULL DEFAULT 0 CHECK (prep_time_minutes BETWEEN 0 AND 1440),
  cook_time_minutes INTEGER NOT NULL DEFAULT 0 CHECK (cook_time_minutes BETWEEN 0 AND 1440),
  tags_json TEXT NOT NULL DEFAULT '[]',
  visibility TEXT NOT NULL CHECK (visibility IN ('private', 'public', 'unlisted')),
  created_at TEXT NOT NULL,
  UNIQUE(recipe_id, version)
);
CREATE INDEX IF NOT EXISTS idx_recipe_revisions_recipe_version ON recipe_revisions(recipe_id, version DESC);
INSERT OR IGNORE INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at)
SELECT lower(hex(randomblob(16))),id,version,owner_id,title,description,ingredients_json,steps_json,visibility,updated_at FROM recipes;
CREATE TABLE IF NOT EXISTS cookbook_recipes (
  cookbook_id TEXT NOT NULL REFERENCES cookbooks(id) ON DELETE CASCADE,
  recipe_id TEXT NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
  added_by TEXT NOT NULL REFERENCES users(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (cookbook_id, recipe_id)
);
CREATE INDEX IF NOT EXISTS idx_cookbooks_owner_updated ON cookbooks(owner_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_recipes_owner_updated ON recipes(owner_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_recipes_visibility_updated ON recipes(visibility, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memberships_user ON cookbook_memberships(user_id, cookbook_id);
CREATE INDEX IF NOT EXISTS idx_cookbook_recipes_recipe ON cookbook_recipes(recipe_id, cookbook_id);
`

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure sqlite database: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply initial schema: %w", err)
	}
	for _, column := range []struct{ table, name, declaration string }{
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
		{"recipe_revisions", "servings", `INTEGER NOT NULL DEFAULT 0 CHECK (servings BETWEEN 0 AND 1000)`},
		{"recipe_revisions", "prep_time_minutes", `INTEGER NOT NULL DEFAULT 0 CHECK (prep_time_minutes BETWEEN 0 AND 1440)`},
		{"recipe_revisions", "cook_time_minutes", `INTEGER NOT NULL DEFAULT 0 CHECK (cook_time_minutes BETWEEN 0 AND 1440)`},
		{"recipe_revisions", "tags_json", `TEXT NOT NULL DEFAULT '[]'`},
	} {
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, column.table, column.name).Scan(&exists); err != nil {
			db.Close()
			return nil, fmt.Errorf("inspect %s schema: %w", column.table, err)
		}
		if exists == 0 {
			if _, err := db.ExecContext(ctx, `ALTER TABLE `+column.table+` ADD COLUMN `+column.name+` `+column.declaration); err != nil {
				db.Close()
				return nil, fmt.Errorf("upgrade %s schema: %w", column.table, err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_recipes_owner_deleted ON recipes(owner_id,deleted_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("index recipe trash: %w", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE invitations AS older SET revoked_at=COALESCE(older.created_at,?) WHERE older.accepted_at IS NULL AND older.revoked_at IS NULL AND EXISTS (SELECT 1 FROM invitations AS newer WHERE COALESCE(newer.cookbook_id,'')=COALESCE(older.cookbook_id,'') AND lower(newer.email)=lower(older.email) AND newer.accepted_at IS NULL AND newer.revoked_at IS NULL AND (newer.created_at>older.created_at OR (newer.created_at=older.created_at AND newer.id>older.id)))`, timestamp()); err != nil {
		db.Close()
		return nil, fmt.Errorf("deduplicate pending invitations: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_invitations_pending_email_scope ON invitations(COALESCE(cookbook_id,''),lower(email)) WHERE accepted_at IS NULL AND revoked_at IS NULL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("index pending invitations: %w", err)
	}
	return db, nil
}
