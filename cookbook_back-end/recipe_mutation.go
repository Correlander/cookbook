package main

import (
	"database/sql"
	"errors"
)

var (
	errRecipeMutationConflict     = errors.New("recipe mutation version conflict")
	errRecipeMutationCookbookRefs = errors.New("recipe cookbook reference denied")
	errRecipeMutationFederation   = errors.New("recipe federation queue unavailable")
	errRecipeMutationNotFound     = errors.New("recipe mutation target not found")
	errRecipeMutationDenied       = errors.New("recipe mutation authorization denied")
	errRecipeMutationVisibility   = errors.New("recipe visibility is owner-only")
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
	RecipeID            string
	ActorID             string
	ExpectedVersion     int
	Snapshot            recipeMutationSnapshot
	ImagePath           *string
	CookbookIDs         []string
	ReplaceCookbookRefs bool
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
	if err := authorizeRecipeMutationTx(tx, mutation); err != nil {
		return 0, err
	}

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
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newVersion, nil
}

func authorizeRecipeMutationTx(tx *sql.Tx, mutation recipeMutation) error {
	var ownerID string
	var editPolicy string
	var currentVisibility visibility
	err := tx.QueryRow(`SELECT owner_id,edit_policy,visibility FROM recipes WHERE id=? AND deleted_at IS NULL`, mutation.RecipeID).Scan(&ownerID, &editPolicy, &currentVisibility)
	if errors.Is(err, sql.ErrNoRows) {
		return errRecipeMutationNotFound
	}
	if err != nil {
		return err
	}

	var memberRole role
	if mutation.ActorID != "" && mutation.ActorID != ownerID {
		err := tx.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END LIMIT 1`, mutation.RecipeID, mutation.ActorID).Scan(&memberRole)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if !canEditRecipe(mutation.ActorID, access{ownerID: ownerID, memberRole: memberRole}, editPolicy) {
		return errRecipeMutationDenied
	}
	if mutation.ActorID != ownerID && mutation.Snapshot.Visibility != currentVisibility {
		return errRecipeMutationVisibility
	}
	return nil
}
