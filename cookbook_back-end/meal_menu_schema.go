package main

import (
	"context"
	"fmt"
)

const mealMenuSchemaDDL = `
CREATE TABLE meal_menus (
  id TEXT NOT NULL PRIMARY KEY
    CHECK (length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),
  owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title TEXT NOT NULL
    CHECK (length(CAST(title AS BLOB)) BETWEEN 1 AND 640 AND length(title)<=160
      AND length(trim(title))>0 AND instr(title,char(0))=0),
  note TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(note AS BLOB))<=16000 AND length(note)<=4000 AND instr(note,char(0))=0),
  creator_name TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(creator_name AS BLOB))<=320 AND length(creator_name)<=80 AND instr(creator_name,char(0))=0),
  version INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version)='integer' AND version>=1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX meal_menus_owner_updated ON meal_menus(owner_id,updated_at DESC,id);

CREATE TABLE meal_menu_entries (
  id TEXT NOT NULL PRIMARY KEY
    CHECK (length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),
  menu_id TEXT NOT NULL REFERENCES meal_menus(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
  local_instance_id TEXT
    CHECK (local_instance_id IS NULL OR (length(local_instance_id)=32 AND local_instance_id NOT GLOB '*[^0-9a-f]*')),
  local_recipe_id TEXT
    CHECK (local_recipe_id IS NULL OR (length(local_recipe_id)=32 AND local_recipe_id NOT GLOB '*[^0-9a-f]*')),
  label TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(label AS BLOB))<=320 AND length(label)<=80 AND instr(label,char(0))=0),
  note TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(note AS BLOB))<=8000 AND length(note)<=2000 AND instr(note,char(0))=0),
  serving_note TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(serving_note AS BLOB))<=640 AND length(serving_note)<=160 AND instr(serving_note,char(0))=0),
  CHECK ((local_instance_id IS NULL AND local_recipe_id IS NULL)
      OR (local_instance_id IS NOT NULL AND local_recipe_id IS NOT NULL)),
  UNIQUE(menu_id,position)
);
CREATE INDEX meal_menu_entries_menu_order ON meal_menu_entries(menu_id,position,id);

CREATE TRIGGER meal_menus_account_limit
BEFORE INSERT ON meal_menus
WHEN (SELECT COUNT(*) FROM meal_menus WHERE owner_id=NEW.owner_id)>=1000
BEGIN SELECT RAISE(ABORT, 'meal menu account limit reached'); END;

CREATE TRIGGER meal_menus_account_limit_on_owner_change
BEFORE UPDATE OF owner_id ON meal_menus
WHEN NEW.owner_id<>OLD.owner_id
  AND (SELECT COUNT(*) FROM meal_menus WHERE owner_id=NEW.owner_id)>=1000
BEGIN SELECT RAISE(ABORT, 'meal menu account limit reached'); END;

CREATE TRIGGER meal_menu_entries_count_limit
BEFORE INSERT ON meal_menu_entries
WHEN (SELECT COUNT(*) FROM meal_menu_entries WHERE menu_id=NEW.menu_id)>=64
BEGIN SELECT RAISE(ABORT, 'meal menu entry limit reached'); END;

CREATE TRIGGER meal_menu_entries_count_limit_on_move
BEFORE UPDATE OF menu_id ON meal_menu_entries
WHEN NEW.menu_id<>OLD.menu_id
  AND (SELECT COUNT(*) FROM meal_menu_entries WHERE menu_id=NEW.menu_id)>=64
BEGIN SELECT RAISE(ABORT, 'meal menu entry limit reached'); END;
`

const mealMenuAttributionSchemaDDL = `
CREATE TABLE content_attributions_v15 (
  id TEXT NOT NULL PRIMARY KEY
    CHECK (length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),
  recipe_id TEXT REFERENCES recipes(id) ON DELETE CASCADE,
  cookbook_id TEXT REFERENCES cookbooks(id) ON DELETE CASCADE,
  course_id TEXT REFERENCES courses(id) ON DELETE CASCADE,
  menu_id TEXT REFERENCES meal_menus(id) ON DELETE CASCADE,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('local','external_claim')),
  creator_id TEXT REFERENCES creator_profiles(creator_id) ON DELETE RESTRICT,
  claim_source_instance_id TEXT,
  claim_creator_id TEXT,
  claim_name TEXT,
  role TEXT NOT NULL CHECK (role IN ('creator','contributor','adapter')),
  provenance TEXT NOT NULL,
  created_at TEXT NOT NULL,
  CHECK ((recipe_id IS NOT NULL AND cookbook_id IS NULL AND course_id IS NULL AND menu_id IS NULL)
      OR (recipe_id IS NULL AND cookbook_id IS NOT NULL AND course_id IS NULL AND menu_id IS NULL)
      OR (recipe_id IS NULL AND cookbook_id IS NULL AND course_id IS NOT NULL AND menu_id IS NULL)
      OR (recipe_id IS NULL AND cookbook_id IS NULL AND course_id IS NULL AND menu_id IS NOT NULL)),
  CHECK (
    (subject_kind='local'
      AND creator_id IS NOT NULL
      AND claim_source_instance_id IS NULL
      AND claim_creator_id IS NULL
      AND claim_name IS NULL
      AND ((recipe_id IS NOT NULL AND role='creator' AND provenance='self_asserted_at_creation')
        OR (cookbook_id IS NOT NULL AND role='creator' AND provenance='local_record_creation')
        OR (recipe_id IS NOT NULL AND role='adapter' AND provenance='adapter_source')
        OR (course_id IS NOT NULL AND role='creator' AND provenance='self_asserted_at_creation')
        OR (menu_id IS NOT NULL AND role='creator' AND provenance='self_asserted_at_creation')))
    OR
    (subject_kind='external_claim'
      AND creator_id IS NULL
      AND claim_source_instance_id IS NOT NULL
      AND claim_creator_id IS NOT NULL
      AND length(claim_source_instance_id)=32
      AND claim_source_instance_id NOT GLOB '*[^0-9a-f]*'
      AND length(claim_creator_id)=32
      AND claim_creator_id NOT GLOB '*[^0-9a-f]*'
      AND (claim_name IS NULL OR (length(CAST(claim_name AS BLOB)) BETWEEN 1 AND 320 AND length(claim_name)<=80 AND instr(claim_name,char(0))=0))
      AND length(CAST(provenance AS BLOB)) BETWEEN 1 AND 128
      AND instr(provenance,char(0))=0)
  )
);
INSERT INTO content_attributions_v15 (
  id,recipe_id,cookbook_id,course_id,menu_id,subject_kind,creator_id,
  claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at
)
SELECT id,recipe_id,cookbook_id,course_id,NULL,subject_kind,creator_id,
       claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at
FROM content_attributions;
`

const mealMenuAttributionIndexesDDL = `
CREATE UNIQUE INDEX content_attributions_local_recipe_uq
  ON content_attributions(recipe_id,creator_id,role)
  WHERE recipe_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_local_cookbook_uq
  ON content_attributions(cookbook_id,creator_id,role)
  WHERE cookbook_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_local_course_uq
  ON content_attributions(course_id,creator_id,role)
  WHERE course_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_local_menu_uq
  ON content_attributions(menu_id,creator_id,role)
  WHERE menu_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_claim_recipe_uq
  ON content_attributions(recipe_id,claim_source_instance_id,claim_creator_id,role)
  WHERE recipe_id IS NOT NULL AND subject_kind='external_claim';
CREATE UNIQUE INDEX content_attributions_claim_cookbook_uq
  ON content_attributions(cookbook_id,claim_source_instance_id,claim_creator_id,role)
  WHERE cookbook_id IS NOT NULL AND subject_kind='external_claim';
CREATE UNIQUE INDEX content_attributions_claim_course_uq
  ON content_attributions(course_id,claim_source_instance_id,claim_creator_id,role)
  WHERE course_id IS NOT NULL AND subject_kind='external_claim';
CREATE UNIQUE INDEX content_attributions_claim_menu_uq
  ON content_attributions(menu_id,claim_source_instance_id,claim_creator_id,role)
  WHERE menu_id IS NOT NULL AND subject_kind='external_claim';
`

func applyMealMenuSchemaMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, mealMenuSchemaDDL); err != nil {
		return fmt.Errorf("create private meal menu schema: %w", err)
	}
	var before int64
	if err := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_attributions`).Scan(&before); err != nil {
		return fmt.Errorf("count existing content attributions: %w", err)
	}
	if _, err := exec.ExecContext(ctx, mealMenuAttributionSchemaDDL); err != nil {
		return fmt.Errorf("create menu attribution schema and copy existing rows: %w", err)
	}
	var after int64
	if err := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_attributions_v15`).Scan(&after); err != nil {
		return fmt.Errorf("count copied content attributions: %w", err)
	}
	if after != before {
		return fmt.Errorf("menu attribution migration copied %d of %d existing rows", after, before)
	}
	if _, err := exec.ExecContext(ctx, `DROP TABLE content_attributions; ALTER TABLE content_attributions_v15 RENAME TO content_attributions;`); err != nil {
		return fmt.Errorf("replace content attributions table: %w", err)
	}
	if _, err := exec.ExecContext(ctx, mealMenuAttributionIndexesDDL); err != nil {
		return fmt.Errorf("create menu attribution indexes: %w", err)
	}
	rows, err := exec.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check foreign keys after menu migration: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, rowID, parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return fmt.Errorf("read menu migration foreign key violation: %w", err)
		}
		return fmt.Errorf("menu migration produced foreign key violation in %s row %s referencing %s (constraint %d)", table, rowID, parent, fkID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("finish menu migration foreign key check: %w", err)
	}
	return nil
}
