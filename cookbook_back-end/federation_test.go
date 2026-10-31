package main

import (
	"encoding/json"
	"errors"
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

func TestFederationReceiverDeduplicatesAndAppliesSequenceAtomically(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, testFederationOrigin, true); err != nil {
		t.Fatal(err)
	}
	base := federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic}
	makeEvent := func(eventID, eventType string, sequence int64, source federationRecipeSource) []byte {
		t.Helper()
		payload, err := serializeFederationEvent(testFederationOrigin, eventID, eventType, sequence, time.Now(), true, true, source)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	first := makeEvent("11111111111111111111111111111111", federationEventSnapshot, 1, base)
	status, response, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, first)
	if err != nil || status != 202 || !strings.Contains(string(response), "accepted") {
		t.Fatalf("snapshot status=%d response=%s err=%v", status, response, err)
	}
	status, _, err = acceptFederationEvent(t.Context(), db, testFederationOrigin, first)
	if err != nil || status != 202 {
		t.Fatalf("duplicate event status=%d err=%v", status, err)
	}
	conflictingID := strings.Replace(string(first), "\"title\":\"Dish\"", "\"title\":\"Other dish\"", 1)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, []byte(conflictingID)); !errors.Is(err, errFederationConflict) {
		t.Fatalf("conflicting event ID error=%v", err)
	}
	update := base
	update.Title = "Updated dish"
	second := makeEvent("22222222222222222222222222222222", federationEventUpdate, 2, update)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, second); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	stale := makeEvent("33333333333333333333333333333333", federationEventUpdate, 2, base)
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, stale); !errors.Is(err, errFederationConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	var sequence, deleted int
	var recipeJSON string
	if err := db.QueryRow(`SELECT sequence,deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, testFederationOrigin+"/federation/recipes/"+testFederationID).Scan(&sequence, &deleted, &recipeJSON); err != nil {
		t.Fatal(err)
	}
	if sequence != 2 || deleted != 0 || !strings.Contains(recipeJSON, "Updated dish") {
		t.Fatalf("remote recipe was not updated atomically: seq=%d deleted=%d payload=%s", sequence, deleted, recipeJSON)
	}
	tombstone, err := serializeFederationEvent(testFederationOrigin, "44444444444444444444444444444444", federationEventTombstone, 3, time.Now(), true, true, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acceptFederationEvent(t.Context(), db, testFederationOrigin, tombstone); err != nil {
		t.Fatalf("tombstone failed: %v", err)
	}
	if err := db.QueryRow(`SELECT sequence,deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, testFederationOrigin+"/federation/recipes/"+testFederationID).Scan(&sequence, &deleted, &recipeJSON); err != nil {
		t.Fatal(err)
	}
	if sequence != 3 || deleted != 1 || recipeJSON != "" {
		t.Fatalf("tombstone retained content: seq=%d deleted=%d payload=%q", sequence, deleted, recipeJSON)
	}
}

func TestDecodeFederationEventRejectsDuplicateUnknownAndMismatchedData(t *testing.T) {
	valid, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"duplicate key":       []byte(strings.Replace(string(valid), `"event_id":`, `"event_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event_id":`, 1)),
		"unknown field":       []byte(strings.TrimSuffix(string(valid), "}") + `,"secret":"value"}`),
		"wrong object origin": []byte(strings.Replace(string(valid), testFederationOrigin+"/federation/recipes/", "https://other.example.org/federation/recipes/", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeFederationEvent(body); err == nil {
				t.Fatal("accepted invalid event")
			}
		})
	}
}
