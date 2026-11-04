package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
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

func libraryRecipeEntriesQuery(actorID, instanceID, query string, includeProjectedRemote bool) (string, []any) {
	sqlQuery := `WITH candidate AS (
		SELECT r.id,r.updated_at,
			EXISTS(SELECT 1 FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=?) AS is_shared,
			COALESCE((SELECT m.role FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=? ORDER BY CASE m.role WHEN 'editor' THEN 0 ELSE 1 END,m.cookbook_id LIMIT 1),'') AS member_role,
			EXISTS(SELECT 1 FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id WHERE cr.recipe_id=r.id AND c.owner_id=?) AS in_owned_cookbook,
			(r.owner_id=?) AS is_owned,
			(SELECT b.id FROM recipe_bookmarks b WHERE b.user_id=? AND b.target_kind='local' AND b.local_instance_id=? AND b.local_recipe_id=r.id) AS bookmark_id
		FROM recipes r JOIN instance_settings s ON s.id=1
		WHERE r.deleted_at IS NULL AND (r.owner_id=? OR EXISTS(SELECT 1 FROM cookbook_memberships m JOIN cookbook_recipes cr ON cr.cookbook_id=m.cookbook_id WHERE cr.recipe_id=r.id AND m.user_id=?) OR EXISTS(SELECT 1 FROM cookbook_recipes cr JOIN cookbooks c ON c.id=cr.cookbook_id WHERE cr.recipe_id=r.id AND c.owner_id=?) OR EXISTS(SELECT 1 FROM recipe_bookmarks b WHERE b.user_id=? AND b.target_kind='local' AND b.local_instance_id=? AND b.local_recipe_id=r.id)) AND ` + recipeActorReadPredicate + `
			AND (?='' OR instr(lower(r.title||' '||r.description||' '||r.tags_json),lower(?))>0)
	)`
	args := []any{actorID, actorID, actorID, actorID, actorID, instanceID, actorID, actorID, actorID, actorID, instanceID, actorID, actorID, query, query}
	if includeProjectedRemote {
		sqlQuery += `, remote_candidate AS (
			SELECT b.id AS bookmark_id,b.remote_object_uri AS object_uri,b.created_at,r.updated_at,
				p.projection_version,p.available,p.title,p.description,p.tags_json
			FROM recipe_bookmarks b
			LEFT JOIN federation_remote_recipes r ON r.object_id=b.remote_object_uri AND r.deleted=0
				AND r.object_id=r.source_origin||'/federation/recipes/'||r.source_recipe_id
			LEFT JOIN federation_remote_recipe_search p ON p.object_id=r.object_id
			WHERE b.user_id=? AND b.target_kind='remote'
		), entries AS (`
		args = append(args, actorID)
	} else {
		sqlQuery += `, entries AS (`
	}
	sqlQuery += `
		SELECT 'local:'||c.id AS entry_key,c.id AS local_recipe_id,'' AS remote_object_uri,COALESCE(c.bookmark_id,'') AS bookmark_id,'local' AS target_kind,'available' AS availability,c.updated_at AS sort_at,'' AS created_at,c.is_shared,c.member_role,c.in_owned_cookbook,(c.bookmark_id IS NOT NULL) AS is_saved,c.is_owned FROM candidate c`
	if includeProjectedRemote {
		sqlQuery += `
		UNION ALL
		SELECT 'remote:'||r.object_uri,'',r.object_uri,r.bookmark_id,'remote','available',r.updated_at,'',0,'',0,1,0
		FROM remote_candidate r WHERE r.projection_version=? AND r.available=1
			AND (?='' OR instr(lower(r.title||' '||r.description||' '||r.tags_json),lower(?))>0)
		UNION ALL
		SELECT 'bookmark:'||r.bookmark_id,'','',r.bookmark_id,'remote','unavailable',r.created_at,r.created_at,0,'',0,1,0
		FROM remote_candidate r WHERE ?='' AND (r.projection_version IS NULL OR r.projection_version<>? OR r.available<>1)`
		args = append(args, remoteRecipeSearchProjectionVersion, query, query, query, remoteRecipeSearchProjectionVersion)
	}
	sqlQuery += `
		UNION ALL
		SELECT 'bookmark:'||b.id,'','',b.id,'local','unavailable',b.created_at,b.created_at,0,'',0,1,0
		FROM recipe_bookmarks b WHERE b.user_id=? AND b.target_kind='local' AND ?='' AND (b.local_instance_id<>? OR NOT EXISTS(SELECT 1 FROM candidate c WHERE c.id=b.local_recipe_id AND b.local_instance_id=?))
	)`
	args = append(args, actorID, query, instanceID, instanceID)
	return sqlQuery, args
}

const libraryRemoteRecipeBatchSize = 128

type libraryRecipeEntryRef struct {
	SortAt   string
	EntryKey string
	Item     LibraryRecipeItem
}

func libraryRecipeEntryBefore(left, right libraryRecipeEntryRef) bool {
	if left.SortAt != right.SortAt {
		return left.SortAt > right.SortAt
	}
	return left.EntryKey < right.EntryKey
}

func libraryRemoteRecipeMatches(query string, view RemoteRecipeReadView) bool {
	if query == "" {
		return true
	}
	tagsJSON, _ := json.Marshal(view.Recipe.Tags)
	searchable := view.Recipe.Title + " " + view.Recipe.Description + " " + string(tagsJSON)
	return strings.Contains(sqliteASCIILower(searchable), sqliteASCIILower(query))
}

func sqliteASCIILower(value string) string {
	lowered := []byte(value)
	for index, char := range lowered {
		if char >= 'A' && char <= 'Z' {
			lowered[index] = char + ('a' - 'A')
		}
	}
	return string(lowered)
}

func scanLibraryLocalEntry(rows *sql.Rows) (libraryRecipeEntryRef, error) {
	var entry libraryRecipeEntryRef
	var localID, remoteURI string
	var shared, inOwned, saved, owned int
	item := LibraryRecipeItem{}
	if err := rows.Scan(&entry.EntryKey, &entry.SortAt, &localID, &remoteURI, &item.BookmarkID, &item.TargetKind, &item.Availability, &item.CreatedAt, &shared, &item.MembershipRole, &inOwned, &saved, &owned); err != nil {
		return libraryRecipeEntryRef{}, err
	}
	if item.Availability == "available" {
		item.Relationships = []string{}
		if item.TargetKind == "remote" {
			item.remoteObjectURI = remoteURI
			item.Relationships = append(item.Relationships, "saved")
		} else {
			item.localRecipeID = localID
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
		}
	} else {
		item.Relationships = []string{"saved"}
	}
	entry.Item = item
	return entry, nil
}

// libraryCanonicalRemoteRecipeEntries is the one-request fallback used if a
// selected projected row fails canonical detail decoding.
func libraryCanonicalRemoteRecipeEntries(ctx context.Context, tx *sql.Tx, actorID, query string) ([]libraryRecipeEntryRef, error) {
	entries := make([]libraryRecipeEntryRef, 0)
	lastID := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,remote_object_uri,created_at FROM recipe_bookmarks
			WHERE user_id=? AND target_kind='remote' AND id>? ORDER BY id LIMIT ?`, actorID, lastID, libraryRemoteRecipeBatchSize)
		if err != nil {
			return nil, fmt.Errorf("read remote library bookmarks: %w", err)
		}
		type bookmarkRef struct{ id, uri, createdAt string }
		batch := make([]bookmarkRef, 0, libraryRemoteRecipeBatchSize)
		for rows.Next() {
			var ref bookmarkRef
			if err := rows.Scan(&ref.id, &ref.uri, &ref.createdAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan remote library bookmark: %w", err)
			}
			batch = append(batch, ref)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read remote library bookmarks: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close remote library bookmarks: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		uris := make([]string, 0, len(batch))
		for _, ref := range batch {
			uris = append(uris, ref.uri)
		}
		resolved, err := readCachedPublicRemoteRecipeResults(ctx, tx, ContentActor{AccountID: actorID}, uris)
		if err != nil {
			return nil, err
		}
		for _, ref := range batch {
			result := resolved[ref.uri]
			if result.Available {
				if !libraryRemoteRecipeMatches(query, result.View) {
					continue
				}
				entries = append(entries, libraryRecipeEntryRef{
					SortAt:   result.View.UpdatedAt,
					EntryKey: "remote:" + result.View.ObjectURI,
					Item: LibraryRecipeItem{
						Availability: "available", TargetKind: "remote", BookmarkID: ref.id,
						Relationships: []string{"saved"}, remoteObjectURI: result.View.ObjectURI,
					},
				})
			} else if query == "" {
				entries = append(entries, libraryRecipeEntryRef{
					SortAt:   ref.createdAt,
					EntryKey: "bookmark:" + ref.id,
					Item: LibraryRecipeItem{
						Availability: "unavailable", TargetKind: "remote", BookmarkID: ref.id,
						Relationships: []string{"saved"}, CreatedAt: ref.createdAt,
					},
				})
			}
		}
		lastID = batch[len(batch)-1].id
		if len(batch) < libraryRemoteRecipeBatchSize {
			break
		}
	}
	sort.Slice(entries, func(left, right int) bool { return libraryRecipeEntryBefore(entries[left], entries[right]) })
	return entries, nil
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
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_query")
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
	page, retryCanonical, err := h.libraryRecipePage(r.Context(), tx, actorID, instanceID, query, limit, offset, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if retryCanonical {
		page, _, err = h.libraryRecipePage(r.Context(), tx, actorID, instanceID, query, limit, offset, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h apiHandler) libraryRecipePage(ctx context.Context, tx *sql.Tx, actorID, instanceID, query string, limit, offset int, canonicalRemote bool) (LibraryRecipePage, bool, error) {
	base, args := libraryRecipeEntriesQuery(actorID, instanceID, query, !canonicalRemote)
	if !canonicalRemote {
		var total int
		if err := tx.QueryRowContext(ctx, base+` SELECT COUNT(*) FROM entries`, args...).Scan(&total); err != nil {
			return LibraryRecipePage{}, false, err
		}
		rows, err := tx.QueryContext(ctx, base+` SELECT entry_key,sort_at,local_recipe_id,remote_object_uri,bookmark_id,target_kind,availability,created_at,is_shared,member_role,in_owned_cookbook,is_saved,is_owned FROM entries ORDER BY sort_at DESC,entry_key LIMIT ? OFFSET ?`, append(args, limit, offset)...)
		if err != nil {
			return LibraryRecipePage{}, false, err
		}
		page := LibraryRecipePage{Items: make([]LibraryRecipeItem, 0, limit), Total: total, Limit: limit, Offset: offset}
		localIDs := make([]string, 0, limit)
		remoteURIs := make([]string, 0, limit)
		for rows.Next() {
			entry, scanErr := scanLibraryLocalEntry(rows)
			if scanErr != nil {
				rows.Close()
				return LibraryRecipePage{}, false, scanErr
			}
			page.Items = append(page.Items, entry.Item)
			if entry.Item.Availability != "available" {
				continue
			}
			if entry.Item.TargetKind == "local" {
				localIDs = append(localIDs, entry.Item.localRecipeID)
			} else {
				remoteURIs = append(remoteURIs, entry.Item.remoteObjectURI)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return LibraryRecipePage{}, false, err
		}
		if err := rows.Close(); err != nil {
			return LibraryRecipePage{}, false, err
		}
		remoteRecipes, err := readCachedPublicRemoteRecipeResults(ctx, tx, ContentActor{AccountID: actorID}, remoteURIs)
		if err != nil {
			return LibraryRecipePage{}, false, err
		}
		for _, uri := range remoteURIs {
			if !remoteRecipes[uri].Available {
				return page, true, nil
			}
		}
		localRecipes, cookbookContexts, err := readRecipeDetails(ctx, tx, ContentActor{AccountID: actorID}, localIDs)
		if err != nil {
			return LibraryRecipePage{}, false, err
		}
		for index := range page.Items {
			item := &page.Items[index]
			if item.Availability != "available" {
				continue
			}
			if item.TargetKind == "local" {
				recipeItem, ok := localRecipes[item.localRecipeID]
				if !ok {
					return LibraryRecipePage{}, false, fmt.Errorf("selected local recipe projection missing")
				}
				item.Recipe = &recipeItem
				item.CookbookContexts = cookbookContexts[item.localRecipeID]
			} else {
				remote := remoteRecipes[item.remoteObjectURI].View
				item.RemoteRecipe = &remote
			}
		}
		return page, false, nil
	}
	var remoteEntries []libraryRecipeEntryRef
	var err error
	remoteEntries, err = libraryCanonicalRemoteRecipeEntries(ctx, tx, actorID, query)
	if err != nil {
		return LibraryRecipePage{}, false, err
	}
	var localTotal int
	if err := tx.QueryRowContext(ctx, base+` SELECT COUNT(*) FROM entries`, args...).Scan(&localTotal); err != nil {
		return LibraryRecipePage{}, false, err
	}
	total := localTotal + len(remoteEntries)
	rows, err := tx.QueryContext(ctx, base+` SELECT entry_key,sort_at,local_recipe_id,remote_object_uri,bookmark_id,target_kind,availability,created_at,is_shared,member_role,in_owned_cookbook,is_saved,is_owned FROM entries ORDER BY sort_at DESC,entry_key`, args...)
	if err != nil {
		return LibraryRecipePage{}, false, err
	}
	page := LibraryRecipePage{Items: make([]LibraryRecipeItem, 0, limit), Total: total, Limit: limit, Offset: offset}
	localIDs := make([]string, 0, limit)
	remoteURIs := make([]string, 0, limit)
	nextLocal := func() (libraryRecipeEntryRef, bool, error) {
		if !rows.Next() {
			return libraryRecipeEntryRef{}, false, rows.Err()
		}
		entry, scanErr := scanLibraryLocalEntry(rows)
		return entry, scanErr == nil, scanErr
	}
	localEntry, hasLocal, err := nextLocal()
	if err != nil {
		rows.Close()
		return LibraryRecipePage{}, false, err
	}
	remoteIndex := 0
	pageEnd := offset + limit
	for rank := 0; rank < pageEnd && (hasLocal || remoteIndex < len(remoteEntries)); rank++ {
		var selected libraryRecipeEntryRef
		if !hasLocal || (remoteIndex < len(remoteEntries) && libraryRecipeEntryBefore(remoteEntries[remoteIndex], localEntry)) {
			selected = remoteEntries[remoteIndex]
			remoteIndex++
		} else {
			selected = localEntry
			localEntry, hasLocal, err = nextLocal()
			if err != nil {
				rows.Close()
				return LibraryRecipePage{}, false, err
			}
		}
		if rank >= offset {
			page.Items = append(page.Items, selected.Item)
			if selected.Item.Availability == "available" && selected.Item.TargetKind == "local" {
				localIDs = append(localIDs, selected.Item.localRecipeID)
			} else if selected.Item.Availability == "available" && selected.Item.TargetKind == "remote" {
				remoteURIs = append(remoteURIs, selected.Item.remoteObjectURI)
			}
		}
	}
	if err := rows.Close(); err != nil {
		return LibraryRecipePage{}, false, err
	}
	remoteRecipes, err := readCachedPublicRemoteRecipeResults(ctx, tx, ContentActor{AccountID: actorID}, remoteURIs)
	if err != nil {
		return LibraryRecipePage{}, false, err
	}
	for _, uri := range remoteURIs {
		if !remoteRecipes[uri].Available {
			if !canonicalRemote {
				return page, true, nil
			}
			return LibraryRecipePage{}, false, fmt.Errorf("canonical remote recipe became unavailable in the read snapshot")
		}
	}
	localRecipes, cookbookContexts, err := readRecipeDetails(ctx, tx, ContentActor{AccountID: actorID}, localIDs)
	if err != nil {
		return LibraryRecipePage{}, false, err
	}
	for index := range page.Items {
		item := &page.Items[index]
		if item.Availability != "available" {
			continue
		}
		if item.TargetKind == "local" {
			recipeItem, ok := localRecipes[item.localRecipeID]
			if !ok {
				return LibraryRecipePage{}, false, fmt.Errorf("selected local recipe projection missing")
			}
			item.Recipe = &recipeItem
			item.CookbookContexts = cookbookContexts[item.localRecipeID]
		} else {
			remote := remoteRecipes[item.remoteObjectURI].View
			item.RemoteRecipe = &remote
		}
	}
	return page, false, nil
}
