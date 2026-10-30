package main

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
