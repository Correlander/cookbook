package main

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	federationEventSnapshot  = "recipe.snapshot"
	federationEventUpdate    = "recipe.update"
	federationEventTombstone = "recipe.tombstone"
	maxFederationEventBytes  = 2 << 20
)

type federationRecipeSource struct {
	ID             string
	OwnerID        string
	CreatorName    string
	SourceRecipeID string
	Title          string
	Description    string
	Ingredients    []ingredient
	Steps          []recipeStep
	Servings       int
	PrepTime       int
	CookTime       int
	Tags           []string
	Visibility     visibility
	Deleted        bool
	ImagePath      string
	CookbookIDs    []string
}

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

func serializeFederationEvent(origin, eventID, eventType string, sequence int64, occurredAt time.Time, federationEnabled, instancePublic bool, source federationRecipeSource) ([]byte, error) {
	if !federationEnabled || sequence < 1 || occurredAt.IsZero() || !isFederationID(eventID) {
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
