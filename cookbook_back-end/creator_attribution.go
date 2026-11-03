package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

const creatorAttributionsSchemaDDL = `
CREATE TABLE content_attributions (
  id TEXT NOT NULL PRIMARY KEY
    CHECK (length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),
  recipe_id TEXT REFERENCES recipes(id) ON DELETE CASCADE,
  cookbook_id TEXT REFERENCES cookbooks(id) ON DELETE CASCADE,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('local','external_claim')),
  creator_id TEXT REFERENCES creator_profiles(creator_id) ON DELETE RESTRICT,
  claim_source_instance_id TEXT,
  claim_creator_id TEXT,
  claim_name TEXT,
  role TEXT NOT NULL CHECK (role IN ('creator','contributor','adapter')),
  provenance TEXT NOT NULL,
  created_at TEXT NOT NULL,
  CHECK ((recipe_id IS NOT NULL AND cookbook_id IS NULL)
      OR (recipe_id IS NULL AND cookbook_id IS NOT NULL)),
  CHECK (
    (subject_kind='local'
      AND creator_id IS NOT NULL
      AND claim_source_instance_id IS NULL
      AND claim_creator_id IS NULL
      AND claim_name IS NULL
      AND ((recipe_id IS NOT NULL AND role='creator' AND provenance='self_asserted_at_creation')
        OR (cookbook_id IS NOT NULL AND role='creator' AND provenance='local_record_creation')
        OR (recipe_id IS NOT NULL AND role='adapter' AND provenance='adapter_source')))
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
CREATE UNIQUE INDEX content_attributions_local_recipe_uq
  ON content_attributions(recipe_id,creator_id,role)
  WHERE recipe_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_local_cookbook_uq
  ON content_attributions(cookbook_id,creator_id,role)
  WHERE cookbook_id IS NOT NULL AND subject_kind='local';
CREATE UNIQUE INDEX content_attributions_claim_recipe_uq
  ON content_attributions(recipe_id,claim_source_instance_id,claim_creator_id,role)
  WHERE recipe_id IS NOT NULL AND subject_kind='external_claim';
CREATE UNIQUE INDEX content_attributions_claim_cookbook_uq
  ON content_attributions(cookbook_id,claim_source_instance_id,claim_creator_id,role)
  WHERE cookbook_id IS NOT NULL AND subject_kind='external_claim';
`

const creatorProfileFoundationDDL = creatorProfilesSchemaDDL + creatorAttributionsSchemaDDL

func ApplyCreatorProfileFoundationMigration(ctx context.Context, exec migrationExecutor) error {
	_, err := exec.ExecContext(ctx, creatorProfileFoundationDDL)
	return err
}

type creatorAttributionInput struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// recipeCreateRequest keeps creator attribution create-only. Update and
// revision-restore payloads cannot mutate these identity/provenance facts.
type recipeCreateRequest struct {
	recipeCreateInput
	CreatorAttribution *creatorAttributionInput `json:"creator_attribution,omitempty"`
}

type portableAttribution struct {
	TargetKind       string `json:"target_kind"`
	TargetID         string `json:"target_id"`
	SourceInstanceID string `json:"source_instance_id"`
	CreatorID        string `json:"creator_id"`
	NameClaim        string `json:"name_claim,omitempty"`
	Role             string `json:"role"`
	Provenance       string `json:"provenance"`
}

type creatorAttribution struct {
	Role       string `json:"role"`
	Provenance string `json:"provenance"`
}

func validateCreatorAttributionInput(input *creatorAttributionInput) error {
	if input == nil {
		return nil
	}
	switch input.Kind {
	case "local_profile", "unknown":
		if input.Name != "" {
			return errors.New("name is only valid for a name claim")
		}
	case "name_claim":
		name := strings.TrimSpace(input.Name)
		if !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 || len(name) > 320 {
			return errors.New("invalid creator name claim")
		}
		input.Name = name
	default:
		return errors.New("invalid creator attribution kind")
	}
	return nil
}

func insertLocalAttribution(ctx context.Context, tx *sql.Tx, targetKind, targetID, creatorID, roleValue, provenance string) error {
	if targetKind != "recipe" && targetKind != "cookbook" {
		return errors.New("invalid attribution target")
	}
	var recipeID, cookbookID any
	if targetKind == "recipe" {
		recipeID = targetID
	} else {
		cookbookID = targetID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO content_attributions(id,recipe_id,cookbook_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES(?,?,?,'local',?,?,?,?)`, newID(), recipeID, cookbookID, creatorID, roleValue, provenance, timestamp())
	return err
}

func insertExternalAttribution(ctx context.Context, tx *sql.Tx, targetKind, targetID string, item portableAttribution) error {
	var recipeID, cookbookID any
	if targetKind == "recipe" {
		recipeID = targetID
	} else if targetKind == "cookbook" {
		cookbookID = targetID
	} else {
		return errors.New("invalid attribution target")
	}
	var name any
	if item.NameClaim != "" {
		name = item.NameClaim
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO content_attributions(id,recipe_id,cookbook_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		VALUES(?,?,?,'external_claim',?,?,?,?,?,?)`, newID(), recipeID, cookbookID, item.SourceInstanceID, item.CreatorID, name, item.Role, item.Provenance, timestamp())
	return err
}
