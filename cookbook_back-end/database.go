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
  created_at TEXT NOT NULL
);
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
  visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'public', 'unlisted')),
  edit_policy TEXT NOT NULL DEFAULT 'owner_only' CHECK (edit_policy IN ('owner_only', 'cookbook_editors')),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
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
	return db, nil
}
