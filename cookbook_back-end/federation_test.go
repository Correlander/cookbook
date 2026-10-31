package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testFederationOrigin = "https://cookbook.example.org"
const testFederationID = "0123456789abcdef0123456789abcdef"

func TestFederationSnapshotSerializesOnlyPublicAllowlistedFields(t *testing.T) {
	source := federationRecipeSource{
		ID: testFederationID, OwnerID: "private-owner-id", CreatorName: "  Ada Cook  ",
		SourceRecipeID: "private-source-id", Title: " Tomato soup ", Description: " Family recipe ",
		Ingredients: []ingredient{{Name: "tomatoes", Quantity: "4", Notes: "ripe"}},
		Steps:       []recipeStep{{Text: "Simmer."}}, Servings: 4, PrepTime: 10,
		CookTime: 25, Tags: []string{" Soup ", "soup"}, Visibility: visibilityPublic,
		ImagePath: "private-image-path", CookbookIDs: []string{"private-cookbook-id"},
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), true, true, source)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	allowedEventFields := map[string]bool{"protocol": true, "schema_version": true, "event_id": true, "event_type": true, "source_origin": true, "object_id": true, "sequence": true, "occurred_at": true, "attribution": true, "recipe": true}
	for key := range event {
		if !allowedEventFields[key] {
			t.Errorf("unexpected event field %q", key)
		}
	}
	if event["object_id"] != testFederationOrigin+"/federation/recipes/"+testFederationID {
		t.Fatalf("object ID=%v", event["object_id"])
	}
	attribution := event["attribution"].(map[string]any)
	if len(attribution) != 1 || attribution["creator_name"] != "Ada Cook" {
		t.Fatalf("unexpected attribution: %#v", attribution)
	}
	recipe := event["recipe"].(map[string]any)
	allowedRecipeFields := map[string]bool{"title": true, "description": true, "ingredients": true, "steps": true, "servings": true, "prep_time_minutes": true, "cook_time_minutes": true, "tags": true}
	for key := range recipe {
		if !allowedRecipeFields[key] {
			t.Errorf("unexpected recipe field %q", key)
		}
	}
	for _, privateValue := range []string{"private-owner-id", "private-source-id", "private-image-path", "private-cookbook-id"} {
		if strings.Contains(string(payload), privateValue) {
			t.Errorf("payload leaked %q", privateValue)
		}
	}
	if recipe["title"] != "Tomato soup" || recipe["description"] != "Family recipe" || len(recipe["tags"].([]any)) != 1 {
		t.Fatalf("recipe normalization failed: %#v", recipe)
	}
}

func TestFederationSnapshotRejectsNonPublicOrDisabledContent(t *testing.T) {
	base := federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic}
	tests := []struct {
		name              string
		source            federationRecipeSource
		federationEnabled bool
		instancePublic    bool
	}{
		{name: "private", source: withVisibility(base, visibilityPrivate), federationEnabled: true, instancePublic: true},
		{name: "unlisted", source: withVisibility(base, visibilityUnlisted), federationEnabled: true, instancePublic: true},
		{name: "deleted", source: func() federationRecipeSource { recipe := base; recipe.Deleted = true; return recipe }(), federationEnabled: true, instancePublic: true},
		{name: "private instance", source: base, federationEnabled: true, instancePublic: false},
		{name: "disabled", source: base, federationEnabled: false, instancePublic: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), test.federationEnabled, test.instancePublic, test.source)
			if err == nil || payload != nil {
				t.Fatalf("nonpublic content serialized: payload=%s err=%v", payload, err)
			}
		})
	}
}

func TestFederationTombstoneContainsNoRecipeOrAttribution(t *testing.T) {
	source := federationRecipeSource{
		ID: testFederationID, OwnerID: "private-owner-id", CreatorName: "Private creator",
		Title: "Private title", Visibility: visibilityPrivate, Deleted: true,
		ImagePath: "private-image-path", CookbookIDs: []string{"private-cookbook-id"},
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventTombstone, 2, time.Now(), true, false, source)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event["event_type"] != federationEventTombstone || event["sequence"] != float64(2) {
		t.Fatalf("unexpected tombstone: %#v", event)
	}
	if _, exists := event["recipe"]; exists {
		t.Fatal("tombstone included recipe data")
	}
	if _, exists := event["attribution"]; exists {
		t.Fatal("tombstone included attribution")
	}
	for _, privateValue := range []string{"private-owner-id", "Private creator", "Private title", "private-image-path", "private-cookbook-id"} {
		if strings.Contains(string(payload), privateValue) {
			t.Errorf("tombstone leaked %q", privateValue)
		}
	}
}

func withVisibility(source federationRecipeSource, visibility visibility) federationRecipeSource {
	source.Visibility = visibility
	return source
}
