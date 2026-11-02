package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	federationEventSnapshot  = "recipe.snapshot"
	federationEventUpdate    = "recipe.update"
	federationEventTombstone = "recipe.tombstone"
	maxFederationEventBytes  = 2 << 20
)

type federationEvent struct {
	Protocol      string                      `json:"protocol"`
	SchemaVersion int                         `json:"schema_version"`
	EventID       string                      `json:"event_id"`
	EventType     string                      `json:"event_type"`
	SourceOrigin  string                      `json:"source_origin"`
	ObjectID      string                      `json:"object_id"`
	Sequence      int64                       `json:"sequence"`
	OccurredAt    string                      `json:"occurred_at"`
	Attribution   *federationEventAttribution `json:"attribution,omitempty"`
	Recipe        *federationEventRecipe      `json:"recipe,omitempty"`
}

type federationEventAttribution struct {
	CreatorName string `json:"creator_name"`
}

type federationEventRecipe struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []ingredient `json:"ingredients"`
	Steps       []recipeStep `json:"steps"`
	Servings    int          `json:"servings"`
	PrepTime    int          `json:"prep_time_minutes"`
	CookTime    int          `json:"cook_time_minutes"`
	Tags        []string     `json:"tags"`
}

func decodeFederationEvent(body []byte) (federationEvent, []byte, error) {
	var event federationEvent
	if len(body) == 0 || int64(len(body)) > maxFederationEventBytes || !utf8.Valid(body) {
		return event, nil, errors.New("invalid event body")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return event, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return event, nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return event, nil, err
	}
	if err := validateFederationEvent(&event); err != nil {
		return event, nil, err
	}
	var normalized []byte
	if event.Recipe != nil {
		recipeBytes, err := json.Marshal(event.Recipe)
		if err != nil {
			return event, nil, err
		}
		normalized = recipeBytes
	}
	return event, normalized, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder, "event"); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder, objectKind string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		allowed := federationJSONAllowedKeys(objectKind)
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
				return errors.New("unknown or incorrectly cased JSON field")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeUniqueJSONValue(decoder, federationJSONChildKind(objectKind, key)); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder, objectKind); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func federationJSONAllowedKeys(objectKind string) map[string]bool {
	var keys []string
	switch objectKind {
	case "event":
		keys = []string{"protocol", "schema_version", "event_id", "event_type", "source_origin", "object_id", "sequence", "occurred_at", "attribution", "recipe"}
	case "attribution":
		keys = []string{"creator_name"}
	case "recipe":
		keys = []string{"title", "description", "ingredients", "steps", "servings", "prep_time_minutes", "cook_time_minutes", "tags"}
	case "ingredient":
		keys = []string{"name", "quantity", "notes"}
	case "step":
		keys = []string{"text"}
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	return allowed
}

func federationJSONChildKind(parent, key string) string {
	switch parent {
	case "event":
		if key == "attribution" || key == "recipe" {
			return key
		}
	case "recipe":
		if key == "ingredients" {
			return "ingredient"
		}
		if key == "steps" {
			return "step"
		}
	}
	return ""
}

func validateFederationEvent(event *federationEvent) error {
	if event.Protocol != "cookbook-exchange" || event.SchemaVersion != 1 || !isFederationID(event.EventID) || event.Sequence < 1 {
		return errors.New("invalid event metadata")
	}
	origin, err := canonicalFederationOrigin(event.SourceOrigin)
	if err != nil || origin != event.SourceOrigin {
		return errors.New("invalid source origin")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.OccurredAt); err != nil {
		return errors.New("invalid occurred_at")
	}
	parts := strings.Split(event.ObjectID, "/federation/recipes/")
	if len(parts) != 2 || parts[0] != event.SourceOrigin || !isFederationID(parts[1]) {
		return errors.New("invalid object ID")
	}
	expected, err := federationRecipeObjectID(event.SourceOrigin, parts[1])
	if err != nil || expected != event.ObjectID {
		return errors.New("invalid object ID")
	}
	switch event.EventType {
	case federationEventSnapshot, federationEventUpdate:
		if event.Recipe == nil || event.Attribution == nil || len(strings.TrimSpace(event.Attribution.CreatorName)) == 0 || len(event.Attribution.CreatorName) > 200 {
			return errors.New("missing recipe attribution")
		}
		input := recipeInput{
			Title: event.Recipe.Title, Description: event.Recipe.Description,
			Ingredients: event.Recipe.Ingredients, Steps: event.Recipe.Steps,
			Servings: event.Recipe.Servings, PrepTime: event.Recipe.PrepTime,
			CookTime: event.Recipe.CookTime, Tags: event.Recipe.Tags,
			Visibility: visibilityPublic, EditPolicy: "owner_only",
		}
		if err := input.validate(); err != nil {
			return err
		}
		if input.Ingredients == nil {
			input.Ingredients = []ingredient{}
		}
		if input.Steps == nil {
			input.Steps = []recipeStep{}
		}
		event.Recipe = &federationEventRecipe{Title: input.Title, Description: input.Description, Ingredients: input.Ingredients, Steps: input.Steps, Servings: input.Servings, PrepTime: input.PrepTime, CookTime: input.CookTime, Tags: input.Tags}
		event.Attribution.CreatorName = strings.TrimSpace(event.Attribution.CreatorName)
	case federationEventTombstone:
		if event.Recipe != nil || event.Attribution != nil {
			return errors.New("tombstones cannot contain recipe content")
		}
	default:
		return errors.New("unsupported event type")
	}
	return nil
}

func serializeFederationEvent(origin, eventID, eventType string, sequence int64, occurredAt time.Time, federationEnabled, instancePublic bool, source federationRecipeSource) ([]byte, error) {
	if (!federationEnabled && eventType != federationEventTombstone) || sequence < 1 || occurredAt.IsZero() || !isFederationID(eventID) {
		return nil, errors.New("invalid federation event metadata")
	}
	objectID, err := federationRecipeObjectID(origin, source.ID)
	if err != nil {
		return nil, err
	}
	event := federationEvent{
		Protocol: "cookbook-exchange", SchemaVersion: 1, EventID: eventID,
		EventType: eventType, SourceOrigin: origin, ObjectID: objectID,
		Sequence: sequence, OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano),
	}
	switch eventType {
	case federationEventSnapshot, federationEventUpdate:
		if !instancePublic || source.Visibility != visibilityPublic || source.Deleted {
			return nil, errors.New("recipe is not eligible for federation")
		}
		creator := strings.TrimSpace(source.CreatorName)
		if len(creator) > 200 {
			return nil, errors.New("invalid federation attribution")
		}
		if creator == "" {
			creator = "Unknown creator"
		}
		input := recipeInput{
			Title: source.Title, Description: source.Description,
			Ingredients: source.Ingredients, Steps: source.Steps,
			Servings: source.Servings, PrepTime: source.PrepTime,
			CookTime: source.CookTime, Tags: source.Tags,
			Visibility: visibilityPublic, EditPolicy: "owner_only",
		}
		if err := input.validate(); err != nil {
			return nil, errors.New("invalid public recipe")
		}
		if input.Ingredients == nil {
			input.Ingredients = []ingredient{}
		}
		if input.Steps == nil {
			input.Steps = []recipeStep{}
		}
		event.Attribution = &federationEventAttribution{CreatorName: creator}
		event.Recipe = &federationEventRecipe{
			Title: input.Title, Description: input.Description, Ingredients: input.Ingredients,
			Steps: input.Steps, Servings: input.Servings, PrepTime: input.PrepTime,
			CookTime: input.CookTime, Tags: input.Tags,
		}
	case federationEventTombstone:
	default:
		return nil, errors.New("unsupported federation event type")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(body) > maxFederationEventBytes {
		return nil, errors.New("federation event is too large")
	}
	return body, nil
}

func isFederationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
