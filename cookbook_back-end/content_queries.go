package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ContentActor is the authenticated local principal for a content query.
// Authorization must not be inferred from an HTTP request or a public actor URI.
type contentQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type ContentActor struct {
	AccountID string
}

var (
	errRecipeDetailNotFound = errors.New("recipe detail not found")
	errRecipeDetailDenied   = errors.New("recipe detail access denied")
)

// readRecipeDetail returns the same local recipe projection as GET /api/recipes/{id}.
// It owns the authorization and projection reads so other features do not need
// to fabricate HTTP requests or re-enter an HTTP handler to read a recipe.
func readRecipeDetail(ctx context.Context, queryer contentQueryer, actor ContentActor, recipeID string) (recipe, error) {
	var item recipe
	var public int
	var ingredients, steps, tags, imagePath string
	err := queryer.QueryRowContext(ctx, `SELECT r.id,r.owner_id,r.creator_name,r.source_recipe_id,r.title,r.description,r.ingredients_json,r.steps_json,r.servings,r.prep_time_minutes,r.cook_time_minutes,r.tags_json,r.visibility,r.edit_policy,r.version,r.created_at,r.updated_at,s.public_instance,r.image_path FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, recipeID).Scan(&item.ID, &item.OwnerID, &item.CreatorName, &item.SourceRecipeID, &item.Title, &item.Description, &ingredients, &steps, &item.Servings, &item.PrepTime, &item.CookTime, &tags, &item.Visibility, &item.EditPolicy, &item.Version, &item.CreatedAt, &item.UpdatedAt, &public, &imagePath)
	if errors.Is(err, sql.ErrNoRows) {
		return recipe{}, errRecipeDetailNotFound
	}
	if err != nil {
		return recipe{}, fmt.Errorf("read recipe detail: %w", err)
	}
	if err := json.Unmarshal([]byte(ingredients), &item.Ingredients); err != nil {
		return recipe{}, fmt.Errorf("decode recipe ingredients: %w", err)
	}
	if err := json.Unmarshal([]byte(steps), &item.Steps); err != nil {
		return recipe{}, fmt.Errorf("decode recipe steps: %w", err)
	}
	if err := json.Unmarshal([]byte(tags), &item.Tags); err != nil {
		return recipe{}, fmt.Errorf("decode recipe tags: %w", err)
	}

	memberRole, err := recipeMemberRoleContext(ctx, queryer, recipeID, actor.AccountID)
	if err != nil {
		return recipe{}, fmt.Errorf("read recipe membership: %w", err)
	}
	resource := access{instancePublic: public == 1, visibility: item.Visibility, ownerID: item.OwnerID, memberRole: memberRole}
	if !canReadResource(actor.AccountID, resource) {
		return recipe{}, errRecipeDetailDenied
	}
	if !hasLocalRecipeRead(actor.AccountID, item.OwnerID, memberRole) {
		facts, err := readRecipePublicReadFacts(ctx, queryer, recipeID)
		if err != nil {
			return recipe{}, fmt.Errorf("read recipe publication facts: %w", err)
		}
		if !facts.AnonymousEligible() {
			return recipe{}, errRecipeDetailDenied
		}
		item.SourceRecipeID = ""
	}

	item.CookbookIDs, err = readableRecipeCookbooks(ctx, queryer, recipeID, actor.AccountID)
	if err != nil {
		return recipe{}, fmt.Errorf("read recipe cookbook references: %w", err)
	}
	if imagePath != "" {
		item.Image = "/api/recipes/" + recipeID + "/image"
	}
	item.CanEdit = canEditRecipe(actor.AccountID, access{ownerID: item.OwnerID, memberRole: memberRole}, item.EditPolicy)
	item.CanDelete = actor.AccountID != "" && actor.AccountID == item.OwnerID
	item.CanManageSharing = canManageRecipeSharing(actor.AccountID, item.OwnerID)
	return item, nil
}

func hasLocalRecipeRead(accountID, ownerID string, memberRole role) bool {
	return accountID != "" && (accountID == ownerID || memberRole != "")
}

func canManageRecipeSharing(accountID, recipeOwnerID string) bool {
	return accountID != "" && accountID == recipeOwnerID
}

func recipeMemberRoleContext(ctx context.Context, queryer contentQueryer, recipeID, accountID string) (role, error) {
	if accountID == "" {
		return "", nil
	}
	var memberRole role
	err := queryer.QueryRowContext(ctx, `SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END LIMIT 1`, recipeID, accountID).Scan(&memberRole)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return memberRole, err
}

func readableRecipeCookbooks(ctx context.Context, queryer contentQueryer, recipeID, accountID string) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT cr.cookbook_id FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id JOIN instance_settings s ON s.id=1 WHERE cr.recipe_id=? AND (c.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships m WHERE m.cookbook_id=c.id AND m.user_id=?) OR (s.public_instance=1 AND c.visibility='public')) ORDER BY cr.cookbook_id`, recipeID, accountID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}
