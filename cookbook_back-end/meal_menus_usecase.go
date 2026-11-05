package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"Cookbook_Database_Backend/internal/menus"
)

type menuEntryResponse struct {
	ID          string             `json:"id"`
	Position    int                `json:"position"`
	Label       string             `json:"label"`
	Note        string             `json:"note"`
	ServingNote string             `json:"serving_note"`
	Available   bool               `json:"available"`
	Recipe      *menuRecipeSummary `json:"recipe,omitempty"`
}

type mealMenuResponse struct {
	ID                  string                   `json:"id"`
	Title               string                   `json:"title"`
	Note                string                   `json:"note"`
	CreatorName         string                   `json:"creator_name,omitempty"`
	Version             int                      `json:"version"`
	CreatedAt           string                   `json:"created_at"`
	UpdatedAt           string                   `json:"updated_at"`
	CreatorAttributions []creatorAttributionRead `json:"creator_attributions,omitempty"`
	Entries             []menuEntryResponse      `json:"entries"`
}

type mealMenuPage struct {
	Menus  []mealMenuResponse `json:"menus"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

type mealMenuUsecase struct {
	db    *sql.DB
	store menus.Store
}

func (u mealMenuUsecase) create(ctx context.Context, owner string, input menuCreateInput) (mealMenuResponse, error) {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return mealMenuResponse{}, err
	}
	defer tx.Rollback()
	now, id := timestamp(), newID()
	item, err := u.store.Create(ctx, tx, menus.CreateInput{ID: id, OwnerID: owner, Title: menus.NormalizeTitle(input.Title), Note: input.Note, CreatorName: menuClaimName(input.CreatorAttribution), Now: now})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if input.CreatorAttribution != nil && input.CreatorAttribution.Kind == "local_profile" {
		profile, profileErr := ensureCreatorProfile(ctx, tx, owner)
		if profileErr == nil {
			profileErr = insertLocalAttribution(ctx, tx, "menu", id, profile.ID, "creator", "self_asserted_at_creation")
		}
		if profileErr != nil {
			return mealMenuResponse{}, profileErr
		}
	}
	response, err := u.project(ctx, tx, owner, []menus.Menu{item})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuResponse{}, err
	}
	return response[0], nil
}

func (u mealMenuUsecase) read(ctx context.Context, owner, id string) (mealMenuResponse, error) {
	tx, err := u.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mealMenuResponse{}, err
	}
	defer tx.Rollback()
	item, err := u.store.Get(ctx, tx, id, owner)
	if err != nil {
		return mealMenuResponse{}, err
	}
	response, err := u.project(ctx, tx, owner, []menus.Menu{item})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuResponse{}, err
	}
	return response[0], nil
}

func (u mealMenuUsecase) list(ctx context.Context, owner string, limit, offset int) (mealMenuPage, error) {
	tx, err := u.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mealMenuPage{}, err
	}
	defer tx.Rollback()
	items, total, err := u.store.List(ctx, tx, owner, limit, offset)
	if err != nil {
		return mealMenuPage{}, err
	}
	projected, err := u.project(ctx, tx, owner, items)
	if err != nil {
		return mealMenuPage{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuPage{}, err
	}
	return mealMenuPage{Menus: projected, Total: total, Limit: limit, Offset: offset}, nil
}

func (u mealMenuUsecase) update(ctx context.Context, owner, id string, version int, input menuUpdateInput) (mealMenuResponse, error) {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return mealMenuResponse{}, err
	}
	defer tx.Rollback()
	now := timestamp()
	next, err := u.store.Reserve(ctx, tx, id, owner, version, now)
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := u.store.Update(ctx, tx, id, owner, menus.UpdateInput{Title: menus.NormalizeTitle(input.Title), Note: input.Note, Now: now}); err != nil {
		return mealMenuResponse{}, err
	}
	item, err := u.store.Get(ctx, tx, id, owner)
	if err != nil {
		return mealMenuResponse{}, err
	}
	item.Version = next
	response, err := u.project(ctx, tx, owner, []menus.Menu{item})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuResponse{}, err
	}
	return response[0], nil
}

func (u mealMenuUsecase) delete(ctx context.Context, owner, id string, version int) error {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := u.store.Delete(ctx, tx, id, owner, version); err != nil {
		return err
	}
	return tx.Commit()
}

func (u mealMenuUsecase) addEntry(ctx context.Context, owner, id string, version int, input menuEntryInput) (mealMenuResponse, error) {
	if err := menus.ValidateEntryFields(input.Label, input.Note, input.ServingNote); err != nil {
		return mealMenuResponse{}, err
	}
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return mealMenuResponse{}, err
	}
	defer tx.Rollback()
	now := timestamp()
	next, err := u.store.Reserve(ctx, tx, id, owner, version, now)
	if err != nil {
		return mealMenuResponse{}, err
	}
	if input.RecipeID == "" {
		return mealMenuResponse{}, menus.ErrTarget
	}
	instanceID, err := currentLibraryInstanceID(ctx, tx)
	if err != nil {
		return mealMenuResponse{}, err
	}
	entry, err := u.entryInput(ctx, tx, owner, id, input, instanceID)
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := u.store.AddEntry(ctx, tx, entry); err != nil {
		return mealMenuResponse{}, err
	}
	item, err := u.store.Get(ctx, tx, id, owner)
	if err != nil {
		return mealMenuResponse{}, err
	}
	item.Version = next
	response, err := u.project(ctx, tx, owner, []menus.Menu{item})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuResponse{}, err
	}
	return response[0], nil
}

func (u mealMenuUsecase) updateEntry(ctx context.Context, owner, id, entryID string, version int, input menuEntryInput) (mealMenuResponse, error) {
	if err := menus.ValidateEntryFields(input.Label, input.Note, input.ServingNote); err != nil {
		return mealMenuResponse{}, err
	}
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return mealMenuResponse{}, err
	}
	defer tx.Rollback()
	now := timestamp()
	next, err := u.store.Reserve(ctx, tx, id, owner, version, now)
	if err != nil {
		return mealMenuResponse{}, err
	}
	instanceID, err := currentLibraryInstanceID(ctx, tx)
	if err != nil {
		return mealMenuResponse{}, err
	}
	entry, err := u.entryInput(ctx, tx, owner, id, input, instanceID)
	if err != nil {
		return mealMenuResponse{}, err
	}
	if input.RecipeID == "" {
		current, getErr := u.store.Get(ctx, tx, id, owner)
		if getErr != nil {
			return mealMenuResponse{}, getErr
		}
		found := false
		for _, existing := range current.Entries {
			if existing.ID == entryID {
				entry.LocalInstanceID = existing.LocalInstanceID
				entry.LocalRecipeID = existing.LocalRecipeID
				found = true
				break
			}
		}
		if !found {
			return mealMenuResponse{}, menus.ErrEntry
		}
	}
	if err := menus.ValidateEntry(entry); err != nil {
		return mealMenuResponse{}, err
	}
	if err := u.store.UpdateEntry(ctx, tx, id, entryID, entry); err != nil {
		return mealMenuResponse{}, err
	}
	item, err := u.store.Get(ctx, tx, id, owner)
	if err != nil {
		return mealMenuResponse{}, err
	}
	item.Version = next
	response, err := u.project(ctx, tx, owner, []menus.Menu{item})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuResponse{}, err
	}
	return response[0], nil
}

func (u mealMenuUsecase) removeEntry(ctx context.Context, owner, id, entryID string, version int) (mealMenuResponse, error) {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return mealMenuResponse{}, err
	}
	defer tx.Rollback()
	now := timestamp()
	next, err := u.store.Reserve(ctx, tx, id, owner, version, now)
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := u.store.RemoveEntry(ctx, tx, id, entryID); err != nil {
		return mealMenuResponse{}, err
	}
	item, err := u.store.Get(ctx, tx, id, owner)
	if err != nil {
		return mealMenuResponse{}, err
	}
	item.Version = next
	response, err := u.project(ctx, tx, owner, []menus.Menu{item})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuResponse{}, err
	}
	return response[0], nil
}

func (u mealMenuUsecase) reorder(ctx context.Context, owner, id string, version int, ids []string) (mealMenuResponse, error) {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return mealMenuResponse{}, err
	}
	defer tx.Rollback()
	now := timestamp()
	next, err := u.store.Reserve(ctx, tx, id, owner, version, now)
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := u.store.Reorder(ctx, tx, id, ids); err != nil {
		return mealMenuResponse{}, err
	}
	item, err := u.store.Get(ctx, tx, id, owner)
	if err != nil {
		return mealMenuResponse{}, err
	}
	item.Version = next
	response, err := u.project(ctx, tx, owner, []menus.Menu{item})
	if err != nil {
		return mealMenuResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return mealMenuResponse{}, err
	}
	return response[0], nil
}

func (u mealMenuUsecase) entryInput(ctx context.Context, tx *sql.Tx, owner, menuID string, input menuEntryInput, instanceID string) (menus.EntryInput, error) {
	result := menus.EntryInput{ID: newID(), MenuID: menuID, Label: input.Label, Note: input.Note, ServingNote: input.ServingNote}
	if input.RecipeID != "" {
		if !validLibraryInstanceID(input.RecipeID) {
			return menus.EntryInput{}, menus.ErrTarget
		}
		var found string
		err := tx.QueryRowContext(ctx, `SELECT r.id FROM recipes r JOIN instance_settings s ON s.id=1 WHERE r.id=? AND r.deleted_at IS NULL AND `+recipeActorReadPredicate, input.RecipeID, owner, owner).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return menus.EntryInput{}, menus.ErrTarget
		}
		if err != nil {
			return menus.EntryInput{}, fmt.Errorf("authorize menu recipe target: %w", err)
		}
		result.LocalInstanceID, result.LocalRecipeID = instanceID, found
	}
	if err := menus.ValidateEntry(result); err != nil {
		return menus.EntryInput{}, err
	}
	return result, nil
}

func (u mealMenuUsecase) project(ctx context.Context, tx *sql.Tx, owner string, items []menus.Menu) ([]mealMenuResponse, error) {
	responses := make([]mealMenuResponse, len(items))
	ids := make([]string, 0, len(items))
	recipeIDs := make([]string, 0)
	for i, item := range items {
		ids = append(ids, item.ID)
		for _, entry := range item.Entries {
			if entry.LocalRecipeID != "" {
				recipeIDs = append(recipeIDs, entry.LocalRecipeID)
			}
		}
		responses[i] = mealMenuResponse{ID: item.ID, Title: item.Title, Note: item.Note, Version: item.Version, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, Entries: make([]menuEntryResponse, 0, len(item.Entries))}
	}
	attrs, err := readCreatorAttributions(ctx, tx, owner, "menu", ids)
	if err != nil {
		return nil, err
	}
	instanceID, err := currentLibraryInstanceID(ctx, tx)
	if err != nil {
		return nil, err
	}
	summaries, err := readMenuRecipeSummaries(ctx, tx, owner, recipeIDs)
	if err != nil {
		return nil, err
	}
	for i, item := range items {
		responses[i].CreatorName = item.CreatorName
		responses[i].CreatorAttributions = attrs[item.ID]
		for _, entry := range item.Entries {
			out := menuEntryResponse{ID: entry.ID, Position: entry.Position, Label: entry.Label, Note: entry.Note, ServingNote: entry.ServingNote}
			if entry.LocalRecipeID != "" && entry.LocalInstanceID == instanceID {
				if summary, ok := summaries[entry.LocalRecipeID]; ok {
					out.Available = true
					out.Recipe = &menuRecipeSummary{ID: summary.ID, Title: summary.Title}
				}
			}
			responses[i].Entries = append(responses[i].Entries, out)
		}
	}
	return responses, nil
}

func menuClaimName(input *creatorAttributionInput) string {
	if input != nil && input.Kind == "name_claim" {
		return input.Name
	}
	return ""
}
