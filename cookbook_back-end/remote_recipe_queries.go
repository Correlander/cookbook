package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

var errRemoteRecipeNotFound = errors.New("cached remote recipe not found")
var errRemoteRecipeInvalidURI = errors.New("invalid remote recipe URI")

// RemoteRecipeContent is the public recipe body retained from a remote snapshot.
// It deliberately excludes local ownership, edit, image, and cookbook metadata.
type RemoteRecipeContent struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Servings    int          `json:"servings"`
	PrepTime    int          `json:"prep_time_minutes"`
	CookTime    int          `json:"cook_time_minutes"`
	Tags        []string     `json:"tags"`
}

// RemoteRecipeReadView is a bookmark-facing projection of an already cached,
// live public remote snapshot. It performs no network or discovery requests.
type RemoteRecipeReadView struct {
	ObjectURI      string              `json:"object_uri"`
	SourceOrigin   string              `json:"source_origin"`
	SourceRecipeID string              `json:"source_recipe_id"`
	CreatorName    string              `json:"creator_name"`
	Recipe         RemoteRecipeContent `json:"recipe"`
	Sequence       int64               `json:"sequence"`
	UpdatedAt      string              `json:"updated_at"`
}

func parseFederationRecipeObjectURI(raw string) (origin, recipeID string, err error) {
	if raw == "" || len(raw) > maxRecipeBookmarkURIBytes {
		return "", "", errRemoteRecipeInvalidURI
	}
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return "", "", errRemoteRecipeInvalidURI
	}
	origin, parseErr = canonicalFederationOrigin(parsed.Scheme + "://" + parsed.Host)
	if parseErr != nil || parsed.Scheme+"://"+parsed.Host != origin {
		return "", "", errRemoteRecipeInvalidURI
	}
	const prefix = "/federation/recipes/"
	if !strings.HasPrefix(parsed.EscapedPath(), prefix) {
		return "", "", errRemoteRecipeInvalidURI
	}
	pathID := strings.TrimPrefix(parsed.EscapedPath(), prefix)
	if strings.Contains(pathID, "/") || !isFederationID(pathID) {
		return "", "", errRemoteRecipeInvalidURI
	}
	expected, buildErr := federationRecipeObjectID(origin, pathID)
	if buildErr != nil || expected != raw {
		return "", "", errRemoteRecipeInvalidURI
	}
	return origin, pathID, nil
}

func readCachedPublicRemoteRecipe(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, actor ContentActor, objectURI string) (RemoteRecipeReadView, error) {
	if strings.TrimSpace(actor.AccountID) == "" {
		return RemoteRecipeReadView{}, errRemoteRecipeNotFound
	}
	origin, recipeID, err := parseFederationRecipeObjectURI(objectURI)
	if err != nil {
		return RemoteRecipeReadView{}, err
	}
	var result RemoteRecipeReadView
	var body string
	err = queryer.QueryRowContext(ctx, `SELECT object_id,source_origin,source_recipe_id,creator_name,recipe_json,sequence,updated_at
		FROM federation_remote_recipes WHERE object_id=? AND deleted=0`, objectURI).Scan(
		&result.ObjectURI, &result.SourceOrigin, &result.SourceRecipeID, &result.CreatorName, &body, &result.Sequence, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteRecipeReadView{}, errRemoteRecipeNotFound
	}
	if err != nil {
		return RemoteRecipeReadView{}, fmt.Errorf("read cached remote recipe: %w", err)
	}
	canonicalOrigin, originErr := canonicalFederationOrigin(result.SourceOrigin)
	expectedObjectURI, objectErr := federationRecipeObjectID(result.SourceOrigin, result.SourceRecipeID)
	if originErr != nil || canonicalOrigin != result.SourceOrigin || result.SourceOrigin != origin || result.SourceRecipeID != recipeID || objectErr != nil || expectedObjectURI != result.ObjectURI || result.ObjectURI != objectURI || result.Sequence < 1 || strings.TrimSpace(result.CreatorName) == "" || len(result.CreatorName) > 200 {
		return RemoteRecipeReadView{}, errors.New("invalid cached remote recipe metadata")
	}
	if _, err := time.Parse(time.RFC3339Nano, result.UpdatedAt); err != nil {
		return RemoteRecipeReadView{}, errors.New("invalid cached remote recipe timestamp")
	}
	var content RemoteRecipeContent
	if body == "" || len(body) > 1<<20 {
		return RemoteRecipeReadView{}, errors.New("invalid cached remote recipe body")
	}
	if err := rejectJSONDuplicateKeys(body); err != nil {
		return RemoteRecipeReadView{}, fmt.Errorf("decode cached remote recipe: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&content); err != nil {
		return RemoteRecipeReadView{}, fmt.Errorf("decode cached remote recipe: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return RemoteRecipeReadView{}, fmt.Errorf("decode cached remote recipe: %w", err)
	}
	input := recipeInput{
		Title: content.Title, Description: content.Description,
		Ingredients: content.Ingredients, Steps: content.Steps,
		Servings: content.Servings, PrepTime: content.PrepTime,
		CookTime: content.CookTime, Tags: content.Tags,
		Visibility: visibilityPublic, EditPolicy: "owner_only",
	}
	if err := input.validate(); err != nil {
		return RemoteRecipeReadView{}, fmt.Errorf("invalid cached remote recipe content: %w", err)
	}
	if input.Ingredients == nil {
		input.Ingredients = []ingredient{}
	}
	if input.Steps == nil {
		input.Steps = []recipeStep{}
	}
	result.CreatorName = strings.TrimSpace(result.CreatorName)
	result.Recipe = RemoteRecipeContent{
		Title: input.Title, Description: input.Description, Ingredients: input.Ingredients,
		Steps: input.Steps, Servings: input.Servings, PrepTime: input.PrepTime,
		CookTime: input.CookTime, Tags: input.Tags,
	}
	return result, nil
}

func rejectJSONDuplicateKeys(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := consumeJSONUniqueKeys(decoder, "recipe"); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func consumeJSONUniqueKeys(decoder *json.Decoder, objectKind string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		allowed := remoteRecipeJSONAllowedKeys(objectKind)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if !allowed[key] {
				return errors.New("unknown or incorrectly cased cached recipe field")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeJSONUniqueKeys(decoder, remoteRecipeJSONChildKind(objectKind, key)); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeJSONUniqueKeys(decoder, objectKind); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func remoteRecipeJSONAllowedKeys(objectKind string) map[string]bool {
	var fields []string
	switch objectKind {
	case "recipe":
		fields = []string{"title", "description", "ingredients", "steps", "servings", "prep_time_minutes", "cook_time_minutes", "tags"}
	case "ingredient":
		fields = []string{"name", "quantity", "notes"}
	case "step":
		fields = []string{"text"}
	default:
		return map[string]bool{}
	}
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	return allowed
}

func remoteRecipeJSONChildKind(parentKind, key string) string {
	if parentKind == "recipe" {
		switch key {
		case "ingredients":
			return "ingredient"
		case "steps":
			return "step"
		}
	}
	return ""
}
