package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type creatorProfileReadSummary struct {
	ID         string `json:"id"`
	PublicName string `json:"public_name"`
	Visibility string `json:"visibility"`
}

type creatorAttributionRead struct {
	SubjectKind string                     `json:"subject_kind"`
	Profile     *creatorProfileReadSummary `json:"profile,omitempty"`
	NameClaim   string                     `json:"name_claim,omitempty"`
	Role        string                     `json:"role"`
	Provenance  string                     `json:"provenance"`
}

func readCreatorAttributions(ctx context.Context, queryer contentQueryer, actorID, targetKind string, targetIDs []string) (map[string][]creatorAttributionRead, error) {
	result := make(map[string][]creatorAttributionRead, len(targetIDs))
	if actorID == "" || len(targetIDs) == 0 {
		return result, nil
	}

	var targetColumn, resourceJoin, resourceACL string
	switch targetKind {
	case "recipe":
		targetColumn = "a.recipe_id"
		resourceJoin = "JOIN recipes r ON r.id=a.recipe_id AND r.deleted_at IS NULL JOIN instance_settings s ON s.id=1"
		resourceACL = recipeActorReadPredicate
	case "cookbook":
		targetColumn = "a.cookbook_id"
		resourceJoin = "JOIN cookbooks c ON c.id=a.cookbook_id JOIN instance_settings s ON s.id=1"
		resourceACL = cookbookActorReadPredicate
	default:
		return nil, errors.New("invalid creator attribution target kind")
	}

	var placeholders strings.Builder
	args := make([]any, 0, len(targetIDs)+3)
	for index, id := range targetIDs {
		if index > 0 {
			placeholders.WriteByte(',')
		}
		placeholders.WriteByte('?')
		args = append(args, id)
	}
	args = append(args, actorID, actorID, actorID)
	query := `SELECT ` + targetColumn + `,a.subject_kind,a.role,a.provenance,
		p.creator_id,p.public_name,p.visibility,a.claim_name
		FROM content_attributions a ` + resourceJoin + `
		LEFT JOIN creator_profiles p ON a.subject_kind='local' AND p.creator_id=a.creator_id
		WHERE ` + targetColumn + ` IN (` + placeholders.String() + `)
		AND ` + resourceACL + `
		AND (a.subject_kind='external_claim' OR (a.subject_kind='local' AND p.creator_id IS NOT NULL AND (p.user_id=? OR p.visibility='instance')))
		ORDER BY ` + targetColumn + `,CASE a.role WHEN 'creator' THEN 0 WHEN 'contributor' THEN 1 ELSE 2 END,
		a.subject_kind,COALESCE(p.public_name,a.claim_name,''),a.id`
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read visible creator attributions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var targetID, subjectKind, roleValue, provenance string
		var profileID, profileName, profileVisibility, nameClaim sql.NullString
		if err := rows.Scan(&targetID, &subjectKind, &roleValue, &provenance, &profileID, &profileName, &profileVisibility, &nameClaim); err != nil {
			return nil, fmt.Errorf("scan visible creator attribution: %w", err)
		}
		item := creatorAttributionRead{SubjectKind: subjectKind, Role: roleValue, Provenance: provenance}
		switch subjectKind {
		case "local":
			if !profileID.Valid || !profileName.Valid || !profileVisibility.Valid {
				return nil, errors.New("local creator attribution has no readable profile")
			}
			item.Profile = &creatorProfileReadSummary{ID: profileID.String, PublicName: profileName.String, Visibility: profileVisibility.String}
		case "external_claim":
			if nameClaim.Valid {
				item.NameClaim = nameClaim.String
			}
		default:
			return nil, errors.New("unknown creator attribution subject")
		}
		result[targetID] = append(result[targetID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visible creator attributions: %w", err)
	}
	return result, nil
}

func recipeMemberRolesForActor(ctx context.Context, queryer contentQueryer, actorID string, recipeIDs []string) (map[string]role, error) {
	result := make(map[string]role, len(recipeIDs))
	if actorID == "" || len(recipeIDs) == 0 {
		return result, nil
	}
	var placeholders strings.Builder
	args := make([]any, 0, len(recipeIDs)+1)
	for index, id := range recipeIDs {
		if index > 0 {
			placeholders.WriteByte(',')
		}
		placeholders.WriteByte('?')
		args = append(args, id)
	}
	args = append(args, actorID)
	rows, err := queryer.QueryContext(ctx, `SELECT cr.recipe_id,m.role FROM cookbook_recipes cr
		JOIN cookbook_memberships m ON m.cookbook_id=cr.cookbook_id
		WHERE cr.recipe_id IN (`+placeholders.String()+`) AND m.user_id=?
		ORDER BY cr.recipe_id,CASE m.role WHEN 'editor' THEN 0 ELSE 1 END,m.cookbook_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read recipe member roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var recipeID string
		var memberRole role
		if err := rows.Scan(&recipeID, &memberRole); err != nil {
			return nil, fmt.Errorf("scan recipe member role: %w", err)
		}
		if _, exists := result[recipeID]; !exists {
			result[recipeID] = memberRole
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipe member roles: %w", err)
	}
	return result, nil
}

func readCookbookDetail(ctx context.Context, queryer contentQueryer, actorID, cookbookID string) (cookbook, error) {
	items, err := readCookbookDetails(ctx, queryer, actorID, []string{cookbookID})
	if err != nil {
		return cookbook{}, err
	}
	item, ok := items[cookbookID]
	if !ok {
		return cookbook{}, sql.ErrNoRows
	}
	return item, nil
}

// readCookbookDetails returns the canonical actor-visible cookbook projection
// for a bounded set of IDs. Resource ACL and creator attribution are read
// through the same queryer so callers can keep both in one transaction
// snapshot. IDs that are missing or unreadable are omitted from the result.
func readCookbookDetails(ctx context.Context, queryer contentQueryer, actorID string, cookbookIDs []string) (map[string]cookbook, error) {
	items := make(map[string]cookbook, len(cookbookIDs))
	if len(cookbookIDs) == 0 {
		return items, nil
	}
	ids := make([]string, 0, len(cookbookIDs))
	seen := make(map[string]struct{}, len(cookbookIDs))
	for _, id := range cookbookIDs {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	var placeholders strings.Builder
	args := make([]any, 0, len(ids)+2)
	for index, id := range ids {
		if index > 0 {
			placeholders.WriteByte(',')
		}
		placeholders.WriteByte('?')
		args = append(args, id)
	}
	args = append(args, actorID, actorID)
	rows, err := queryer.QueryContext(ctx, `SELECT c.id,c.owner_id,c.title,c.description,c.visibility,c.created_at,c.updated_at
		FROM cookbooks c JOIN instance_settings s ON s.id=1
		WHERE c.id IN (`+placeholders.String()+`) AND `+cookbookActorReadPredicate+`
		ORDER BY c.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read cookbook details: %w", err)
	}
	visibleIDs := make([]string, 0, len(ids))
	for rows.Next() {
		var item cookbook
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Title, &item.Description, &item.Visibility, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan cookbook details: %w", err)
		}
		items[item.ID] = item
		visibleIDs = append(visibleIDs, item.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate cookbook details: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close cookbook details: %w", err)
	}
	attributions, err := readCreatorAttributions(ctx, queryer, actorID, "cookbook", visibleIDs)
	if err != nil {
		return nil, err
	}
	for id, item := range items {
		item.CreatorAttributions = attributions[id]
		items[id] = item
	}
	return items, nil
}
