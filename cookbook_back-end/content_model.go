package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type RecipePublicationState string

const (
	RecipePublicationUnpublished RecipePublicationState = "unpublished"
	RecipePublicationPublished   RecipePublicationState = "published"
	RecipePublicationWithdrawn   RecipePublicationState = "withdrawn"
)

type RecipeFederationMode string

const (
	RecipeFederationOff       RecipeFederationMode = "off"
	RecipeFederationFollowers RecipeFederationMode = "followers"
)

type RecipePublicationPolicy struct {
	RecipeID         string
	PublicationState RecipePublicationState
	FederationMode   RecipeFederationMode
	IndexEligible    bool
	UpdatedBy        sql.NullString
	UpdatedAt        string
	PublishedAt      sql.NullString
}

const recipePublicationPolicyDDL = `CREATE TABLE recipe_publication_policy (
  recipe_id TEXT PRIMARY KEY REFERENCES recipes(id) ON DELETE CASCADE,
  publication_state TEXT NOT NULL CHECK (publication_state IN ('unpublished','published','withdrawn')),
  federation_mode TEXT NOT NULL DEFAULT 'off' CHECK (federation_mode IN ('off','followers')),
  index_eligible INTEGER NOT NULL DEFAULT 0 CHECK (index_eligible IN (0,1)),
  updated_by TEXT REFERENCES users(id) ON DELETE RESTRICT,
  updated_at TEXT NOT NULL,
  published_at TEXT,
  CHECK (publication_state = 'published' OR (federation_mode = 'off' AND index_eligible = 0))
)`

const recipePublicationPolicyBackfillSQL = `INSERT INTO recipe_publication_policy
	(recipe_id,publication_state,federation_mode,index_eligible,updated_by,updated_at,published_at)
	SELECT id,CASE WHEN visibility='public' THEN 'published' ELSE 'unpublished' END,
	'off',0,NULL,?,NULL FROM recipes ORDER BY id`

// ApplyRecipePublicationPolicyMigration is migration 3's local-only DDL and
// compatibility backfill. Call it inside the migration ledger transaction.
func ApplyRecipePublicationPolicyMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, recipePublicationPolicyDDL); err != nil {
		return fmt.Errorf("create recipe publication policy: %w", err)
	}
	_, err := exec.ExecContext(ctx, recipePublicationPolicyBackfillSQL, timestamp())
	if err != nil {
		return fmt.Errorf("backfill recipe publication policy: %w", err)
	}
	return nil
}

func defaultRecipePublicationPolicy(recipeID string) RecipePublicationPolicy {
	return RecipePublicationPolicy{
		RecipeID:         recipeID,
		PublicationState: RecipePublicationUnpublished,
		FederationMode:   RecipeFederationOff,
	}
}

func validateRecipePublicationPolicy(policy RecipePublicationPolicy) error {
	if policy.RecipeID == "" {
		return errors.New("recipe publication policy requires a recipe ID")
	}
	switch policy.PublicationState {
	case RecipePublicationUnpublished, RecipePublicationPublished, RecipePublicationWithdrawn:
	default:
		return fmt.Errorf("invalid recipe publication state %q", policy.PublicationState)
	}
	switch policy.FederationMode {
	case RecipeFederationOff, RecipeFederationFollowers:
	default:
		return fmt.Errorf("invalid recipe federation mode %q", policy.FederationMode)
	}
	if policy.PublicationState != RecipePublicationPublished &&
		(policy.FederationMode != RecipeFederationOff || policy.IndexEligible) {
		return errors.New("federation and index consent require published state")
	}
	return nil
}

// insertDefaultRecipePublicationPolicy writes the safe policy alongside the
// newly created recipe in the caller's existing transaction.
func insertDefaultRecipePublicationPolicy(ctx context.Context, tx *sql.Tx, recipeID string) error {
	policy := defaultRecipePublicationPolicy(recipeID)
	if err := validateRecipePublicationPolicy(policy); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO recipe_publication_policy
		(recipe_id,publication_state,federation_mode,index_eligible,updated_by,updated_at,published_at)
		VALUES(?,?,?,?,?,?,?)`, policy.RecipeID, policy.PublicationState, policy.FederationMode,
		policy.IndexEligible, nil, timestamp(), nil)
	if err != nil {
		return fmt.Errorf("insert default recipe publication policy: %w", err)
	}
	return nil
}
