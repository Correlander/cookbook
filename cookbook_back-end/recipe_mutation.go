package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

var (
	errRecipeMutationConflict     = errors.New("recipe mutation version conflict")
	errRecipeMutationCookbookRefs = errors.New("recipe cookbook reference denied")
	errRecipeMutationFederation   = errors.New("recipe federation queue unavailable")
	errRecipeMutationNotFound     = errors.New("recipe mutation target not found")
	errRecipeMutationDenied       = errors.New("recipe mutation authorization denied")
	errRecipeMutationVisibility   = errors.New("recipe visibility is owner-only")
	errRecipeMutationConfirmation = errors.New("recipe public visibility requires confirmation")
	errRecipeMutationInstanceGate = errors.New("recipe visibility is unavailable on a private instance")
)

type recipeMutationSnapshot struct {
	Title           string
	Description     string
	IngredientsJSON []byte
	StepsJSON       []byte
	Servings        int
	PrepTime        int
	CookTime        int
	TagsJSON        []byte
	Visibility      visibility
}

// recipeMutation carries normalized, authorized content from an HTTP handler.
// ImagePath nil preserves the current image; a non-nil value restores that path.
// Cookbook references are replaced only when ReplaceCookbookRefs is true.
type recipeMutation struct {
	Context             context.Context
	RecipeID            string
	ActorID             string
	ExpectedVersion     int
	Snapshot            recipeMutationSnapshot
	ImagePath           *string
	CookbookIDs         []string
	ReplaceCookbookRefs bool
	ConfirmPublicAccess bool
	VisibilityChanged   *bool
	CanonicalResponse   *recipe
}

// commitRecipeMutation assumes its caller has already authenticated ActorID,
// authorized edits to RecipeID, validated the snapshot, and enforced any
// visibility-specific policy. It is not a general-purpose authorization entry
// point. It commits the recipe update, revision, and required federation event
// together, returning the committed version on success.
func commitRecipeMutation(db *sql.DB, mutation recipeMutation) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	currentVisibility, err := authorizeRecipeMutationTx(tx, mutation)
	if err != nil {
		return 0, err
	}
	visibilityChanged := currentVisibility != mutation.Snapshot.Visibility

	updatedAt := timestamp()
	var result sql.Result
	if mutation.ImagePath == nil {
		result, err = tx.Exec(`UPDATE recipes SET title=?,description=?,ingredients_json=?,steps_json=?,servings=?,prep_time_minutes=?,cook_time_minutes=?,tags_json=?,visibility=?,version=version+1,updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`,
			mutation.Snapshot.Title, mutation.Snapshot.Description, string(mutation.Snapshot.IngredientsJSON), string(mutation.Snapshot.StepsJSON), mutation.Snapshot.Servings, mutation.Snapshot.PrepTime, mutation.Snapshot.CookTime, string(mutation.Snapshot.TagsJSON), mutation.Snapshot.Visibility, updatedAt, mutation.RecipeID, mutation.ExpectedVersion)
	} else {
		result, err = tx.Exec(`UPDATE recipes SET title=?,description=?,ingredients_json=?,steps_json=?,servings=?,prep_time_minutes=?,cook_time_minutes=?,tags_json=?,visibility=?,image_path=?,version=version+1,updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`,
			mutation.Snapshot.Title, mutation.Snapshot.Description, string(mutation.Snapshot.IngredientsJSON), string(mutation.Snapshot.StepsJSON), mutation.Snapshot.Servings, mutation.Snapshot.PrepTime, mutation.Snapshot.CookTime, string(mutation.Snapshot.TagsJSON), mutation.Snapshot.Visibility, *mutation.ImagePath, updatedAt, mutation.RecipeID, mutation.ExpectedVersion)
	}
	if err != nil {
		return 0, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return 0, errRecipeMutationConflict
	}
	if mutation.ReplaceCookbookRefs {
		if err := replaceCookbookRefs(tx, mutation.RecipeID, mutation.ActorID, mutation.CookbookIDs); err != nil {
			return 0, errRecipeMutationCookbookRefs
		}
	}

	imagePath := ""
	if mutation.ImagePath != nil {
		imagePath = *mutation.ImagePath
	} else if err := tx.QueryRow(`SELECT image_path FROM recipes WHERE id=?`, mutation.RecipeID).Scan(&imagePath); err != nil {
		return 0, err
	}
	newVersion := mutation.ExpectedVersion + 1
	if err := insertRecipeRevision(tx, mutation.RecipeID, newVersion, mutation.ActorID, mutation.Snapshot.Title, mutation.Snapshot.Description, mutation.Snapshot.IngredientsJSON, mutation.Snapshot.StepsJSON, mutation.Snapshot.Servings, mutation.Snapshot.PrepTime, mutation.Snapshot.CookTime, mutation.Snapshot.TagsJSON, mutation.Snapshot.Visibility, imagePath); err != nil {
		return 0, err
	}
	if err := enqueueFederationChange(tx, mutation.RecipeID); err != nil {
		return 0, errRecipeMutationFederation
	}
	if visibilityChanged {
		if err := insertAuditEvent(tx, mutation.ActorID, "recipe.visibility.change", "recipes", mutation.RecipeID, http.StatusOK); err != nil {
			return 0, err
		}
	}
	if mutation.CanonicalResponse != nil {
		ctx := mutation.Context
		if ctx == nil {
			ctx = context.Background()
		}
		response, err := readRecipeDetail(ctx, tx, ContentActor{AccountID: mutation.ActorID}, mutation.RecipeID)
		if err != nil {
			return 0, err
		}
		*mutation.CanonicalResponse = response
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if mutation.VisibilityChanged != nil {
		*mutation.VisibilityChanged = visibilityChanged
	}
	return newVersion, nil
}

func authorizeRecipeMutationTx(tx *sql.Tx, mutation recipeMutation) (visibility, error) {
	var ownerID string
	var editPolicy string
	var currentVisibility visibility
	var publicInstance int
	err := tx.QueryRow(`SELECT r.owner_id,r.edit_policy,r.visibility,s.public_instance FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL`, mutation.RecipeID).Scan(&ownerID, &editPolicy, &currentVisibility, &publicInstance)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errRecipeMutationNotFound
	}
	if err != nil {
		return "", err
	}

	var memberRole role
	if mutation.ActorID != "" && mutation.ActorID != ownerID {
		err := tx.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END LIMIT 1`, mutation.RecipeID, mutation.ActorID).Scan(&memberRole)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if !canEditRecipe(mutation.ActorID, access{ownerID: ownerID, memberRole: memberRole}, editPolicy) {
		return "", errRecipeMutationDenied
	}
	if mutation.ActorID != ownerID && mutation.Snapshot.Visibility != currentVisibility {
		return "", errRecipeMutationVisibility
	}
	if mutation.Snapshot.Visibility != visibilityPrivate && publicInstance != 1 {
		return "", errRecipeMutationInstanceGate
	}
	if currentVisibility != visibilityPublic && mutation.Snapshot.Visibility == visibilityPublic && !mutation.ConfirmPublicAccess {
		return "", errRecipeMutationConfirmation
	}
	return currentVisibility, nil
}
