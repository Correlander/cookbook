package main

import "database/sql"

type visibility string

const (
	visibilityPrivate  visibility = "private"
	visibilityPublic   visibility = "public"
	visibilityUnlisted visibility = "unlisted"
)

type role string

const (
	roleOwner  role = "owner"
	roleEditor role = "editor"
	roleViewer role = "viewer"
)

type access struct {
	instancePublic bool
	visibility     visibility
	ownerID        string
	memberRole     role
}

// recipeMemberRole prefers editor when a recipe appears in multiple cookbooks.
func recipeMemberRole(db *sql.DB, recipeID, userID string) role {
	if userID == "" {
		return ""
	}
	var memberRole role
	_ = db.QueryRow(`SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=? AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END LIMIT 1`, recipeID, userID).Scan(&memberRole)
	return memberRole
}

func canReadResource(userID string, resource access) bool {
	if userID != "" && (userID == resource.ownerID || resource.memberRole != "") {
		return true
	}
	return resource.visibility == visibilityPublic && resource.instancePublic
}

func canEditResource(userID string, resource access) bool {
	if userID == "" {
		return false
	}
	return userID == resource.ownerID || resource.memberRole == roleOwner || resource.memberRole == roleEditor
}

func canEditRecipe(userID string, resource access, editPolicy string) bool {
	return userID != "" && (userID == resource.ownerID || (editPolicy == "cookbook_editors" && resource.memberRole == roleEditor))
}
