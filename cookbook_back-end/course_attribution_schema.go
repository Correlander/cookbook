package main

import (
	"context"
	"fmt"
)

// courseAttributionSchemaDDL is migration 14's frozen target schema. Keep the
// v1-v13 definitions immutable and make future changes in later migrations.
const courseAttributionSchemaDDL = `
ALTER TABLE courses ADD COLUMN creator_name TEXT NOT NULL DEFAULT '';
CREATE TABLE content_attributions_v14 (
  id TEXT NOT NULL PRIMARY KEY
    CHECK (length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),
  recipe_id TEXT REFERENCES recipes(id) ON DELETE CASCADE,
  cookbook_id TEXT REFERENCES cookbooks(id) ON DELETE CASCADE,
  course_id TEXT REFERENCES courses(id) ON DELETE CASCADE,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('local','external_claim')),
  creator_id TEXT REFERENCES creator_profiles(creator_id) ON DELETE RESTRICT,
  claim_source_instance_id TEXT,
  claim_creator_id TEXT,
  claim_name TEXT,
  role TEXT NOT NULL CHECK (role IN ('creator','contributor','adapter')),
  provenance TEXT NOT NULL,
  created_at TEXT NOT NULL,
  CHECK ((recipe_id IS NOT NULL AND cookbook_id IS NULL AND course_id IS NULL)
      OR (recipe_id IS NULL AND cookbook_id IS NOT NULL AND course_id IS NULL)
      OR (recipe_id IS NULL AND cookbook_id IS NULL AND course_id IS NOT NULL)),
  CHECK (
    (subject_kind='local'
      AND creator_id IS NOT NULL
      AND claim_source_instance_id IS NULL
      AND claim_creator_id IS NULL
      AND claim_name IS NULL
      AND ((recipe_id IS NOT NULL AND role='creator' AND provenance='self_asserted_at_creation')
        OR (cookbook_id IS NOT NULL AND role='creator' AND provenance='local_record_creation')
        OR (recipe_id IS NOT NULL AND role='adapter' AND provenance='adapter_source')
        OR (course_id IS NOT NULL AND role='creator' AND provenance='self_asserted_at_creation')))
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
INSERT INTO content_attributions_v14 (
  id,recipe_id,cookbook_id,course_id,subject_kind,creator_id,
  claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at
)
SELECT id,recipe_id,cookbook_id,NULL,subject_kind,creator_id,
       claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at
FROM content_attributions;
`

func applyCourseAttributionSchemaMigration(ctx context.Context, exec migrationExecutor) error {
	var before int64
	if err := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_attributions`).Scan(&before); err != nil {
		return fmt.Errorf("count existing content attributions: %w", err)
	}
	if _, err := exec.ExecContext(ctx, courseAttributionSchemaDDL); err != nil {
		return fmt.Errorf("create course attribution schema and copy existing rows: %w", err)
	}
	var after int64
	if err := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_attributions_v14`).Scan(&after); err != nil {
		return fmt.Errorf("count copied content attributions: %w", err)
	}
	if after != before {
		return fmt.Errorf("course attribution migration copied %d of %d existing rows", after, before)
	}
	if _, err := exec.ExecContext(ctx, `DROP TABLE content_attributions; ALTER TABLE content_attributions_v14 RENAME TO content_attributions;`); err != nil {
		return fmt.Errorf("replace content attributions table: %w", err)
	}
	if _, err := exec.ExecContext(ctx, courseAttributionIndexesDDL); err != nil {
		return fmt.Errorf("create course attribution indexes: %w", err)
	}
	rows, err := exec.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check foreign keys after course attribution migration: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, rowID, parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return fmt.Errorf("read course attribution foreign key violation: %w", err)
		}
		return fmt.Errorf("course attribution migration produced foreign key violation in %s row %s referencing %s (constraint %d)", table, rowID, parent, fkID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("finish course attribution foreign key check: %w", err)
	}
	return nil
}

const courseAttributionIndexesDDL = `
CREATE UNIQUE INDEX content_attributions_local_recipe_uq
  ON content_attributions(recipe_id,creator_id,role)
  WHERE recipe_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_local_cookbook_uq
  ON content_attributions(cookbook_id,creator_id,role)
  WHERE cookbook_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_local_course_uq
  ON content_attributions(course_id,creator_id,role)
  WHERE course_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_claim_recipe_uq
  ON content_attributions(recipe_id,claim_source_instance_id,claim_creator_id,role)
  WHERE recipe_id IS NOT NULL AND subject_kind='external_claim';
CREATE UNIQUE INDEX content_attributions_claim_cookbook_uq
  ON content_attributions(cookbook_id,claim_source_instance_id,claim_creator_id,role)
  WHERE cookbook_id IS NOT NULL AND subject_kind='external_claim';
CREATE UNIQUE INDEX content_attributions_claim_course_uq
  ON content_attributions(course_id,claim_source_instance_id,claim_creator_id,role)
  WHERE course_id IS NOT NULL AND subject_kind='external_claim';
`
