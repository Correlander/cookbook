package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev1"
)

const (
	federationEventSnapshot  = exchangev1.EventSnapshot
	federationEventUpdate    = exchangev1.EventUpdate
	federationEventTombstone = exchangev1.EventTombstone
	maxFederationEventBytes  = exchangev1.MaxEventBytes
)

type federationEvent = exchangev1.Event
type federationEventAttribution = exchangev1.Attribution
type federationEventRecipe = exchangev1.Recipe

func decodeFederationEvent(body []byte) (federationEvent, []byte, error) {
	event, err := exchangev1.Decode(body)
	if err != nil {
		return federationEvent{}, nil, err
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
		ingredients := make([]ingredient, len(event.Recipe.Ingredients))
		for i, value := range event.Recipe.Ingredients {
			ingredients[i] = ingredient(value)
		}
		steps := make([]recipeStep, len(event.Recipe.Steps))
		for i, value := range event.Recipe.Steps {
			steps[i] = recipeStep(value)
		}
		input := recipeInput{
			Title: event.Recipe.Title, Description: event.Recipe.Description,
			Ingredients: ingredients, Steps: steps,
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
		wireIngredients := make([]exchangev1.Ingredient, len(input.Ingredients))
		for i, value := range input.Ingredients {
			wireIngredients[i] = exchangev1.Ingredient(value)
		}
		wireSteps := make([]exchangev1.Step, len(input.Steps))
		for i, value := range input.Steps {
			wireSteps[i] = exchangev1.Step(value)
		}
		event.Recipe = &federationEventRecipe{Title: input.Title, Description: input.Description, Ingredients: wireIngredients, Steps: wireSteps, Servings: input.Servings, PrepTime: input.PrepTime, CookTime: input.CookTime, Tags: input.Tags}
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
		wireIngredients := make([]exchangev1.Ingredient, len(input.Ingredients))
		for i, value := range input.Ingredients {
			wireIngredients[i] = exchangev1.Ingredient(value)
		}
		wireSteps := make([]exchangev1.Step, len(input.Steps))
		for i, value := range input.Steps {
			wireSteps[i] = exchangev1.Step(value)
		}
		event.Recipe = &federationEventRecipe{
			Title: input.Title, Description: input.Description, Ingredients: wireIngredients,
			Steps: wireSteps, Servings: input.Servings, PrepTime: input.PrepTime,
			CookTime: input.CookTime, Tags: input.Tags,
		}
	case federationEventTombstone:
	default:
		return nil, errors.New("unsupported federation event type")
	}
	return exchangev1.Encode(event)
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
