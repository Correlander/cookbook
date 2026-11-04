package main

import (
	"database/sql"
	"net/http"
)

type LibraryRecipeCookbookContext struct {
	ID             string `json:"id"`
	MembershipRole string `json:"membership_role,omitempty"`
}

type LibraryRecipeItem struct {
	Availability     string                         `json:"availability"`
	TargetKind       string                         `json:"target_kind"`
	BookmarkID       string                         `json:"bookmark_id,omitempty"`
	Relationships    []string                       `json:"relationships"`
	MembershipRole   string                         `json:"membership_role,omitempty"`
	InOwnedCookbook  bool                           `json:"in_owned_cookbook,omitempty"`
	CookbookContexts []LibraryRecipeCookbookContext `json:"cookbook_contexts,omitempty"`
	Recipe           *recipe                        `json:"recipe,omitempty"`
	RemoteRecipe     *RemoteRecipeReadView          `json:"remote_recipe,omitempty"`
	CreatedAt        string                         `json:"created_at,omitempty"`
	localRecipeID    string                         `json:"-"`
	remoteObjectURI  string                         `json:"-"`
}

type LibraryRecipePage struct {
	Items  []LibraryRecipeItem `json:"items"`
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

func libraryRecipeEntriesQuery(actorID, instanceID string) (string, []any) {
	query := `WITH candidate AS (
		SELECT r.id,r.updated_at,
			EXISTS(SELECT 1 FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=?) AS is_shared,
			COALESCE((SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END,m.cookbook_id LIMIT 1),'') AS member_role,
			EXISTS(SELECT 1 FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id WHERE cr.recipe_id=r.id AND c.owner_id=?) AS in_owned_cookbook,
			(r.owner_id=?) AS is_owned,
			(SELECT b.id FROM recipe_bookmarks b WHERE b.user_id=? AND b.target_kind='local' AND b.local_instance_id=? AND b.local_recipe_id=r.id) AS bookmark_id
		FROM recipes r JOIN instance_settings s ON s.id=1
		WHERE r.deleted_at IS NULL AND (r.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=?) OR EXISTS(SELECT 1 FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id WHERE cr.recipe_id=r.id AND c.owner_id=?) OR EXISTS(SELECT 1 FROM recipe_bookmarks b WHERE b.user_id=? AND b.target_kind='local' AND b.local_instance_id=? AND b.local_recipe_id=r.id)) AND ` + recipeActorReadPredicate + `
	), remote_candidate AS (
		SELECT b.id AS bookmark_id,b.remote_object_uri AS object_uri,b.created_at,f.updated_at
		FROM recipe_bookmarks b JOIN federation_remote_recipes f ON f.object_id=b.remote_object_uri AND f.deleted=0
			AND f.object_id=f.source_origin||'/federation/recipes/'||f.source_recipe_id
		WHERE b.user_id=? AND b.target_kind='remote'
	), entries AS (
		SELECT 'local:'||c.id AS entry_key,c.id AS local_recipe_id,'' AS remote_object_uri,COALESCE(c.bookmark_id,'') AS bookmark_id,'local' AS target_kind,'available' AS availability,c.updated_at AS sort_at,'' AS created_at,c.is_shared,c.member_role,c.in_owned_cookbook,(c.bookmark_id IS NOT NULL) AS is_saved,c.is_owned FROM candidate c
		UNION ALL
		SELECT 'remote:'||r.object_uri,'',r.object_uri,r.bookmark_id,'remote','available',r.updated_at,'',0,'',0,1,0 FROM remote_candidate r
		UNION ALL
		SELECT 'bookmark:'||b.id,'','',b.id,'local','unavailable',b.created_at,b.created_at,0,'',0,1,0
		FROM recipe_bookmarks b WHERE b.user_id=? AND b.target_kind='local' AND (b.local_instance_id<>? OR NOT EXISTS(SELECT 1 FROM candidate c WHERE c.id=b.local_recipe_id AND b.local_instance_id=?))
		UNION ALL
		SELECT 'bookmark:'||b.id,'','',b.id,'remote','unavailable',b.created_at,b.created_at,0,'',0,1,0
		FROM recipe_bookmarks b WHERE b.user_id=? AND b.target_kind='remote' AND NOT EXISTS(SELECT 1 FROM remote_candidate r WHERE r.bookmark_id=b.id)
	)`
	args := []any{actorID, actorID, actorID, actorID, actorID, instanceID, actorID, actorID, actorID, actorID, instanceID, actorID, actorID, actorID, actorID, instanceID, instanceID, actorID}
	return query, args
}

func (h apiHandler) libraryRecipes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	actorID := authenticatedUser(r)
	if actorID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	limit, offset, err := listPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	instanceID, err := currentLibraryInstanceID(r.Context(), tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	base, args := libraryRecipeEntriesQuery(actorID, instanceID)
	var total int
	if err := tx.QueryRowContext(r.Context(), base+` SELECT COUNT(*) FROM entries`, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	rows, err := tx.QueryContext(r.Context(), base+` SELECT local_recipe_id,remote_object_uri,bookmark_id,target_kind,availability,created_at,is_shared,member_role,in_owned_cookbook,is_saved,is_owned FROM entries ORDER BY sort_at DESC,entry_key LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	page := LibraryRecipePage{Items: make([]LibraryRecipeItem, 0, limit), Total: total, Limit: limit, Offset: offset}
	localIDs := make([]string, 0, limit)
	remoteURIs := make([]string, 0, limit)
	for rows.Next() {
		var item LibraryRecipeItem
		var localID, remoteURI string
		var shared, inOwned, saved, owned int
		if err := rows.Scan(&localID, &remoteURI, &item.BookmarkID, &item.TargetKind, &item.Availability, &item.CreatedAt, &shared, &item.MembershipRole, &inOwned, &saved, &owned); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if item.Availability == "available" {
			item.Relationships = []string{}
			if item.TargetKind == "local" {
				item.localRecipeID = localID
				localIDs = append(localIDs, localID)
				if owned != 0 {
					item.Relationships = append(item.Relationships, "owned")
				}
				if shared != 0 {
					item.Relationships = append(item.Relationships, "shared")
				}
				if inOwned != 0 {
					item.InOwnedCookbook = true
					item.Relationships = append(item.Relationships, "in_owned_cookbook")
				}
				if saved != 0 {
					item.Relationships = append(item.Relationships, "saved")
				}
			} else {
				item.remoteObjectURI = remoteURI
				remoteURIs = append(remoteURIs, remoteURI)
				item.Relationships = []string{"saved"}
			}
		} else {
			item.Relationships = []string{"saved"}
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	localRecipes, cookbookContexts, err := readRecipeDetails(r.Context(), tx, ContentActor{AccountID: actorID}, localIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	remoteRecipes, err := readCachedPublicRemoteRecipes(r.Context(), tx, ContentActor{AccountID: actorID}, remoteURIs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	for index := range page.Items {
		item := &page.Items[index]
		if item.Availability != "available" {
			continue
		}
		if item.TargetKind == "local" {
			recipeItem, ok := localRecipes[item.localRecipeID]
			if !ok {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			item.Recipe = &recipeItem
			item.CookbookContexts = cookbookContexts[item.localRecipeID]
		} else {
			remote, ok := remoteRecipes[item.remoteObjectURI]
			if !ok {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			item.RemoteRecipe = &remote
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
