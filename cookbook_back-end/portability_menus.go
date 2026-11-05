package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"Cookbook_Database_Backend/internal/menus"
)

const maxPortableMenus = menus.MaxMenusPerOwner

type portableMenu struct {
	ID          string              `json:"id"`
	Title       string              `json:"title"`
	Note        string              `json:"note"`
	CreatorName string              `json:"creator_name"`
	Entries     []portableMenuEntry `json:"entries"`
}

type portableMenuEntry struct {
	ID                string `json:"id"`
	Position          int    `json:"position"`
	RecipeID          string `json:"recipe_id,omitempty"`
	RecipeLinkOmitted bool   `json:"recipe_link_omitted,omitempty"`
	Label             string `json:"label"`
	Note              string `json:"note"`
	ServingNote       string `json:"serving_note"`
}

func exportPortableMenus(ctx context.Context, db *sql.DB, owner string, recipeIDs map[string]bool, manifestSize *int) ([]portableMenu, []portableAttribution, int, error) {
	return exportPortableMenusWithLimit(ctx, db, owner, recipeIDs, manifestSize, maxImportManifestBytes)
}

func exportPortableMenusWithLimit(ctx context.Context, db *sql.DB, owner string, recipeIDs map[string]bool, manifestSize *int, manifestLimit int) ([]portableMenu, []portableAttribution, int, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback()
	instanceID, err := currentLibraryInstanceID(ctx, tx)
	if err != nil {
		return nil, nil, 0, err
	}
	store := menus.Store{}
	const pageSize = 100
	all := make([]portableMenu, 0)
	claims := make([]portableAttribution, 0)
	omitted := 0
	for offset := 0; ; offset += pageSize {
		items, total, err := store.List(ctx, tx, owner, pageSize, offset)
		if err != nil {
			return nil, nil, 0, err
		}
		if total > maxPortableMenus {
			return nil, nil, 0, errPortableExportTooLarge
		}
		for _, item := range items {
			portable := portableMenu{ID: item.ID, Title: item.Title, Note: item.Note, CreatorName: item.CreatorName, Entries: make([]portableMenuEntry, 0, len(item.Entries))}
			for _, entry := range item.Entries {
				out := portableMenuEntry{ID: entry.ID, Position: entry.Position, Label: entry.Label, Note: entry.Note, ServingNote: entry.ServingNote}
				if entry.LocalRecipeID != "" {
					if entry.LocalInstanceID == instanceID && recipeIDs[entry.LocalRecipeID] {
						out.RecipeID = entry.LocalRecipeID
					} else {
						out.RecipeLinkOmitted = true
						omitted++
					}
				}
				portable.Entries = append(portable.Entries, out)
			}
			encoded, err := json.Marshal(portable)
			if err != nil || *manifestSize+len(encoded)+1 > manifestLimit {
				return nil, nil, 0, errPortableExportTooLarge
			}
			*manifestSize += len(encoded) + 1
			all = append(all, portable)
		}
		if offset+len(items) >= total {
			break
		}
	}
	for offset := 0; offset < len(all); offset += pageSize {
		end := offset + pageSize
		if end > len(all) {
			end = len(all)
		}
		var placeholders strings.Builder
		args := make([]any, 0, end-offset+2)
		for i := offset; i < end; i++ {
			if i > offset {
				placeholders.WriteByte(',')
			}
			placeholders.WriteByte('?')
			args = append(args, all[i].ID)
		}
		args = append(args, owner, owner)
		rows, err := tx.QueryContext(ctx, `SELECT a.menu_id,a.subject_kind,a.creator_id,a.claim_source_instance_id,a.claim_creator_id,a.claim_name,a.role,a.provenance,
			p.user_id,p.visibility,p.public_name
			FROM content_attributions a JOIN meal_menus m ON m.id=a.menu_id
			LEFT JOIN creator_profiles p ON p.creator_id=a.creator_id
			WHERE a.menu_id IN (`+placeholders.String()+`) AND m.owner_id=?
			AND (a.subject_kind='external_claim' OR (a.subject_kind='local' AND (p.user_id=? OR p.visibility='instance')))
			ORDER BY a.menu_id,CASE a.role WHEN 'creator' THEN 0 WHEN 'contributor' THEN 1 ELSE 2 END,a.id`, args...)
		if err != nil {
			return nil, nil, 0, err
		}
		for rows.Next() {
			var menuID, subjectKind, roleValue, provenance string
			var creatorID, sourceInstanceID, claimCreatorID, claimName, profileUserID, profileVisibility, profileName sql.NullString
			if err := rows.Scan(&menuID, &subjectKind, &creatorID, &sourceInstanceID, &claimCreatorID, &claimName, &roleValue, &provenance, &profileUserID, &profileVisibility, &profileName); err != nil {
				rows.Close()
				return nil, nil, 0, err
			}
			item := portableAttribution{TargetKind: "menu", TargetID: menuID, Role: roleValue, Provenance: provenance}
			if subjectKind == "local" {
				if !profileUserID.Valid || (profileUserID.String != owner && profileVisibility.String != "instance") {
					continue
				}
				item.SourceInstanceID, item.CreatorID = instanceID, creatorID.String
				if profileName.Valid {
					item.NameClaim = profileName.String
				}
			} else if subjectKind == "external_claim" {
				if !sourceInstanceID.Valid || !claimCreatorID.Valid {
					rows.Close()
					return nil, nil, 0, errors.New("external menu creator claim has no source identity")
				}
				item.SourceInstanceID, item.CreatorID = sourceInstanceID.String, claimCreatorID.String
				if claimName.Valid {
					item.NameClaim = claimName.String
				}
			} else {
				rows.Close()
				return nil, nil, 0, errors.New("unknown menu creator subject")
			}
			encoded, err := json.Marshal(item)
			if err != nil || *manifestSize+len(encoded)+1 > manifestLimit || len(claims) >= maxPortableRelationships {
				rows.Close()
				return nil, nil, 0, errPortableExportTooLarge
			}
			*manifestSize += len(encoded) + 1
			claims = append(claims, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		if err := rows.Close(); err != nil {
			return nil, nil, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, 0, err
	}
	return all, claims, omitted, nil
}
