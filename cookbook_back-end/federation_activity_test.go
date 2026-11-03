package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	testActivityOriginA = "https://alice.example.org"
	testActivityOriginB = "https://baker.example.net"
	testActivityActorA  = testActivityOriginA + "/actors/actor_alice_0000001"
	testActivityActorB  = testActivityOriginB + "/actors/actor_baker_0000001"
	testActivityObject  = testActivityOriginB + "/federation/recipes/0123456789abcdef0123456789abcdef"
)

var testActivityProfile = federationActivityProfile{
	version:            "cookbook-ap-local-proof-v1",
	extensionPrefix:    "cookbook",
	extensionNamespace: "https://cookbook.invalid/ns#",
}

func activityContextForCookbook() json.RawMessage {
	testContext, err := activityContextForProfile(testActivityProfile)
	if err != nil {
		panic(err)
	}
	return testContext
}

func decodeTestFederationActivity(body []byte, authenticatedOrigin string) (validatedFederationActivity, error) {
	return decodeFederationActivity(body, authenticatedOrigin, testActivityProfile)
}

func activityTestDocument(t *testing.T, _ string, actorID, id, activityType string, object any, sequence int64, context json.RawMessage) []byte {
	t.Helper()
	if len(context) == 0 {
		context = json.RawMessage(fmt.Sprintf("%q", activityStreamsContext))
	}
	document := map[string]any{
		"@context": context, "id": id, "type": activityType, "actor": actorID, "object": object,
	}
	if sequence > 0 {
		document[testActivityProfile.sequenceField()] = sequence
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func testFollowObject() map[string]any {
	return map[string]any{
		"id":     testActivityOriginA + "/activities/follow_activity_0001",
		"type":   "Follow",
		"actor":  testActivityActorA,
		"object": testActivityActorB,
	}
}

func testRecipeObject(id, attributedTo, name string) map[string]any {
	return map[string]any{
		"id":           id,
		"type":         testActivityProfile.extensionPrefix + ":Recipe",
		"attributedTo": attributedTo,
		"name":         name,
	}
}

func TestDecodeFederationActivityLifecycleProfile(t *testing.T) {
	follow := activityTestDocument(t, testActivityOriginA, testActivityActorA,
		testActivityOriginA+"/activities/follow_activity_0002", "Follow", testActivityActorB, 0, nil)
	decoded, err := decodeTestFederationActivity(follow, testActivityOriginA)
	if err != nil || decoded.ProfileVersion != testActivityProfile.version || decoded.Type != "Follow" || decoded.Actor != testActivityActorA || decoded.ObjectID != testActivityActorB {
		t.Fatalf("Follow decode = %#v, %v", decoded, err)
	}

	accept := activityTestDocument(t, testActivityOriginB, testActivityActorB,
		testActivityOriginB+"/activities/accept_activity_0001", "Accept", testFollowObject(), 0, nil)
	decoded, err = decodeTestFederationActivity(accept, testActivityOriginB)
	if err != nil || decoded.Type != "Accept" || decoded.ObjectID != testActivityOriginA+"/activities/follow_activity_0001" {
		t.Fatalf("Accept decode = %#v, %v", decoded, err)
	}

	create := activityTestDocument(t, testActivityOriginB, testActivityActorB,
		testActivityOriginB+"/activities/create_activity_0001", "Create",
		func() map[string]any {
			object := testRecipeObject(testActivityObject, testActivityActorB, "Tomato soup")
			object[testActivityProfile.sourceField()] = testActivityOriginA + "/federation/recipes/0123456789abcdef0123456789abcdef"
			object[testActivityProfile.adaptedFromField()] = testActivityOriginA + "/federation/recipes/0123456789abcdef0123456789abcdef"
			return object
		}(), 1, activityContextForCookbook())
	decoded, err = decodeTestFederationActivity(create, testActivityOriginB)
	if err != nil || decoded.ObjectID != testActivityObject || decoded.ObjectType != testActivityProfile.extensionPrefix+":Recipe" || decoded.Name != "Tomato soup" || decoded.Sequence != 1 || decoded.Source == "" || decoded.AdaptedFrom == "" {
		t.Fatalf("Create decode = %#v, %v", decoded, err)
	}

	update := activityTestDocument(t, testActivityOriginB, testActivityActorB,
		testActivityOriginB+"/activities/update_activity_0001", "Update",
		testRecipeObject(testActivityObject, testActivityActorB, "Tomato basil soup"), 2, activityContextForCookbook())
	decoded, err = decodeTestFederationActivity(update, testActivityOriginB)
	if err != nil || decoded.Type != "Update" || decoded.Sequence != 2 || decoded.Name != "Tomato basil soup" {
		t.Fatalf("Update decode = %#v, %v", decoded, err)
	}

	deleteBody := activityTestDocument(t, testActivityOriginB, testActivityActorB,
		testActivityOriginB+"/activities/delete_activity_0001", "Delete", testActivityObject, 3, activityContextForCookbook())
	decoded, err = decodeTestFederationActivity(deleteBody, testActivityOriginB)
	if err != nil || decoded.Type != "Delete" || decoded.ObjectID != testActivityObject || decoded.Sequence != 3 {
		t.Fatalf("Delete decode = %#v, %v", decoded, err)
	}

	undo := activityTestDocument(t, testActivityOriginA, testActivityActorA,
		testActivityOriginA+"/activities/undo_activity_0001", "Undo", testFollowObject(), 0, nil)
	decoded, err = decodeTestFederationActivity(undo, testActivityOriginA)
	if err != nil || decoded.Type != "Undo" || decoded.ObjectID != testActivityOriginA+"/activities/follow_activity_0001" {
		t.Fatalf("Undo decode = %#v, %v", decoded, err)
	}
}

func TestDecodeFederationActivityRequiresExplicitProfileVersion(t *testing.T) {
	follow := activityTestDocument(t, testActivityOriginA, testActivityActorA,
		testActivityOriginA+"/activities/follow_activity_0016", "Follow", testActivityActorB, 0, nil)
	if _, err := decodeFederationActivity(follow, testActivityOriginA, federationActivityProfile{}); err == nil {
		t.Fatal("activity decoded without an explicit profile")
	}
	otherProfile := testActivityProfile
	otherProfile.version = "cookbook-ap-local-proof-v2"
	otherProfile.extensionNamespace = "https://cookbook.invalid/another-ns#"
	create := activityTestDocument(t, testActivityOriginB, testActivityActorB,
		testActivityOriginB+"/activities/create_activity_0016", "Create",
		testRecipeObject(testActivityObject, testActivityActorB, "Soup"), 1, activityContextForCookbook())
	if _, err := decodeFederationActivity(create, testActivityOriginB, otherProfile); err == nil {
		t.Fatal("activity decoded under a profile with a different extension namespace")
	}
}

func TestDecodeFederationActivityUsesConfiguredExtensionProfile(t *testing.T) {
	profile := federationActivityProfile{
		version:            "kitchen-ap-local-proof-v1",
		extensionPrefix:    "kitchen",
		extensionNamespace: "https://kitchen.example/ns#",
	}
	context, err := activityContextForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	object := map[string]any{
		"id":                  testActivityObject,
		"type":                "kitchen:Recipe",
		"attributedTo":        testActivityActorB,
		"name":                "Soup",
		profile.sourceField(): testActivityOriginA + "/recipes/opaque_recipe_0001",
	}
	document := map[string]any{
		"@context":              context,
		"id":                    testActivityOriginB + "/activities/create_activity_0017",
		"type":                  "Create",
		"actor":                 testActivityActorB,
		"object":                object,
		profile.sequenceField(): 1,
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeFederationActivity(body, testActivityOriginB, profile)
	if err != nil || decoded.ProfileVersion != profile.version || decoded.ObjectType != "kitchen:Recipe" || decoded.Sequence != 1 || decoded.Source == "" {
		t.Fatalf("configured extension profile decode = %#v, %v", decoded, err)
	}
}

func TestDecodeFederationActivityRejectsUntrustedShapesAndOrigins(t *testing.T) {
	validObject := testRecipeObject(testActivityObject, testActivityActorB, "Soup")
	base := func() []byte {
		return activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginB+"/activities/create_activity_0002", "Create", validObject, 1, activityContextForCookbook())
	}
	tests := []struct {
		name   string
		body   []byte
		origin string
	}{
		{name: "authenticated origin mismatch", body: base(), origin: testActivityOriginA},
		{name: "actor origin mismatch", body: activityTestDocument(t, testActivityOriginA, testActivityActorA,
			testActivityOriginA+"/activities/create_activity_0003", "Create", validObject, 1, activityContextForCookbook()), origin: testActivityOriginA},
		{name: "activity ID origin mismatch", body: activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginA+"/activities/create_activity_0004", "Create", validObject, 1, activityContextForCookbook()), origin: testActivityOriginB},
		{name: "object origin mismatch", body: activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginB+"/activities/create_activity_0005", "Create", testRecipeObject(testActivityOriginA+"/objects/recipes/opaque_recipe_0001", testActivityActorB, "Soup"), 1, activityContextForCookbook()), origin: testActivityOriginB},
		{name: "attribution mismatch", body: activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginB+"/activities/create_activity_0006", "Create", testRecipeObject(testActivityObject, testActivityActorA, "Soup"), 1, activityContextForCookbook()), origin: testActivityOriginB},
		{name: "actor not opaque ID", body: activityTestDocument(t, testActivityOriginB, testActivityOriginB+"/actors/bob",
			testActivityOriginB+"/activities/create_activity_0007", "Create", validObject, 1, activityContextForCookbook()), origin: testActivityOriginB},
		{name: "unsupported activity", body: activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginB+"/activities/like_activity_0001", "Like", testActivityObject, 0, nil), origin: testActivityOriginB},
		{name: "unsupported object type", body: func() []byte {
			object := testRecipeObject(testActivityObject, testActivityActorB, "Soup")
			object["type"] = "Note"
			return activityTestDocument(t, testActivityOriginB, testActivityActorB,
				testActivityOriginB+"/activities/create_activity_0008", "Create", object, 1, activityContextForCookbook())
		}(), origin: testActivityOriginB},
		{name: "sequence without extension context", body: activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginB+"/activities/create_activity_0009", "Create", validObject, 1, nil), origin: testActivityOriginB},
		{name: "wrong Accept target", body: activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginB+"/activities/accept_activity_0002", "Accept", func() map[string]any {
				follow := testFollowObject()
				follow["object"] = testActivityOriginA + "/actors/actor_elsewhere_000001"
				return follow
			}(), 0, nil), origin: testActivityOriginB},
		{name: "remote JSON-LD context", body: []byte(`{"@context":"https://evil.example/context","id":"` + testActivityOriginB + `/activities/follow_activity_0003","type":"Follow","actor":"` + testActivityActorB + `","object":"` + testActivityActorA + `"}`), origin: testActivityOriginB},
		{name: "unknown activity field", body: bytes.Replace(base(), []byte(`"type":"Create"`), []byte(`"type":"Create","inReplyTo":"https://foreign.example/x"`), 1), origin: testActivityOriginB},
		{name: "wrong field casing", body: bytes.Replace(base(), []byte(`"type":"Create"`), []byte(`"Type":"Create"`), 1), origin: testActivityOriginB},
		{name: "duplicate field", body: []byte(`{"@context":"` + activityStreamsContext + `","id":"` + testActivityOriginB + `/activities/follow_activity_0004","type":"Follow","type":"Follow","actor":"` + testActivityActorB + `","object":"` + testActivityActorA + `"}`), origin: testActivityOriginB},
		{name: "duplicate inline context key", body: []byte(`{"@context":["` + activityStreamsContext + `",{"cookbook":"https://cookbook.invalid/ns#","cookbook":"https://cookbook.invalid/ns#"}],"id":"` + testActivityOriginB + `/activities/follow_activity_0018","type":"Follow","actor":"` + testActivityActorB + `","object":"` + testActivityActorA + `"}`), origin: testActivityOriginB},
		{name: "duplicate object field", body: []byte(`{"@context":["` + activityStreamsContext + `",{"cookbook":"https://cookbook.invalid/ns#"}],"id":"` + testActivityOriginB + `/activities/create_activity_0019","type":"Create","actor":"` + testActivityActorB + `","cookbook:sequence":1,"object":{"id":"` + testActivityObject + `","type":"cookbook:Recipe","attributedTo":"` + testActivityActorB + `","name":"Soup","name":"Stew"}}`), origin: testActivityOriginB},
		{name: "trailing JSON", body: append(base(), []byte(` {}`)...), origin: testActivityOriginB},
		{name: "control byte in actor URI", body: activityTestDocument(t, testActivityOriginB, "https://baker.example.net/actors/actor_baker_0000001?x=y",
			testActivityOriginB+"/activities/follow_activity_0005", "Follow", testActivityActorA, 0, nil), origin: testActivityOriginB},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeTestFederationActivity(test.body, test.origin); err == nil {
				t.Fatal("invalid activity was accepted")
			}
		})
	}

	t.Run("embedded Follow recipient bound", func(t *testing.T) {
		recipients := make([]string, activityMaxRecipients+1)
		for i := range recipients {
			recipients[i] = testActivityActorA
		}
		follow := testFollowObject()
		follow["to"] = recipients
		body := activityTestDocument(t, testActivityOriginB, testActivityActorB,
			testActivityOriginB+"/activities/accept_activity_0020", "Accept", follow, 0, nil)
		if _, err := decodeTestFederationActivity(body, testActivityOriginB); err == nil {
			t.Fatal("Accept with an oversized embedded Follow recipient list was accepted")
		}
	})
}

func TestDecodeFederationActivityEnforcesBodyDepthTokenAndFieldBounds(t *testing.T) {
	valid := activityTestDocument(t, testActivityOriginB, testActivityActorB,
		testActivityOriginB+"/activities/create_activity_0011", "Create",
		testRecipeObject(testActivityObject, testActivityActorB, "Soup"), 1, activityContextForCookbook())
	oversized := bytes.Repeat([]byte(" "), maxFederationEventBytes+1)
	// Invalid nesting is rejected by the structural scanner before typed decoding.
	deep := []byte(`{"@context":"` + activityStreamsContext + `","id":"` + testActivityOriginB + `/activities/follow_activity_0012","type":"Follow","actor":"` + testActivityActorB + `","object":` + strings.Repeat("[", activityMaxJSONDepth+1) + `null` + strings.Repeat("]", activityMaxJSONDepth+1) + `}`)
	tooManyRecipients := make([]string, activityMaxRecipients+1)
	for i := range tooManyRecipients {
		tooManyRecipients[i] = testActivityActorA
	}
	withRecipients := map[string]any{"@context": activityStreamsContext, "id": testActivityOriginB + "/activities/follow_activity_0013", "type": "Follow", "actor": testActivityActorB, "object": testActivityActorA, "to": tooManyRecipients}
	tooMany, _ := json.Marshal(withRecipients)
	tooManyJSONValues := make([]string, activityMaxJSONTokens+1)
	for i := range tooManyJSONValues {
		tooManyJSONValues[i] = testActivityActorA
	}
	withTooManyJSONValues := map[string]any{"@context": activityStreamsContext, "id": testActivityOriginB + "/activities/follow_activity_0014", "type": "Follow", "actor": testActivityActorB, "object": testActivityActorA, "to": tooManyJSONValues}
	tooManyTokens, _ := json.Marshal(withTooManyJSONValues)
	tooLongName := activityTestDocument(t, testActivityOriginB, testActivityActorB,
		testActivityOriginB+"/activities/create_activity_0015", "Create",
		testRecipeObject(testActivityObject, testActivityActorB, strings.Repeat("x", activityMaxNameBytes+1)), 1, activityContextForCookbook())
	malformed := []byte(`{"@context":`)
	for name, body := range map[string][]byte{"oversized": oversized, "too deep": deep, "too many recipients": tooMany, "too many JSON tokens": tooManyTokens, "text field too long": tooLongName, "malformed JSON": malformed} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTestFederationActivity(body, testActivityOriginB); err == nil {
				t.Fatal("bounded activity input was accepted")
			}
		})
	}
	if _, err := decodeTestFederationActivity(valid, testActivityOriginB); err != nil {
		t.Fatalf("valid bounded control did not decode: %v", err)
	}
}
