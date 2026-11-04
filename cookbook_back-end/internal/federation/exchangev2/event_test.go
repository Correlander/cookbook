package exchangev2

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const validRecipe = `{"title":"Pasta","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
const validPayloadDigest = "51e4a9ee79d8abdf9c50f1643c2e0e834f3b9e47ae5c582472bf96c18296c243"
const validPayloadB64U = "eyJ0aXRsZSI6IlBhc3RhIiwiZGVzY3JpcHRpb24iOiIiLCJpbmdyZWRpZW50cyI6W10sInN0ZXBzIjpbXSwic2VydmluZ3MiOjEsInByZXBfdGltZV9taW51dGVzIjowLCJjb29rX3RpbWVfbWludXRlcyI6MCwidGFncyI6W119"

func validSnapshot() Event {
	payload := []byte(validRecipe)
	objectID := "https://cookbook.example.org" + ObjectIDMarker + "0123456789abcdef0123456789abcdef"
	digest := PayloadDigest(payload)
	return Event{
		Protocol: Protocol, SchemaVersion: SchemaVersion,
		EventID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EventType: RecipeSnapshot,
		SourceOrigin: "https://cookbook.example.org", ObjectID: objectID,
		Sequence: 1, OccurredAt: "2026-11-04T12:00:00Z",
		Attribution: &Attribution{CreatorName: "Ada"},
		Payload:     payload, PayloadSHA256: digest,
		RetentionGrant: &RetentionGrant{
			Purpose: GrantPurpose, Retention: GrantRetention, Portability: GrantPortability,
			ObjectID: objectID, Sequence: 1, PayloadSHA256: digest,
		},
	}
}

func validWithdrawal() Event {
	event := validSnapshot()
	event.EventID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	event.EventType = RecipeWithdrawal
	event.Sequence = 2
	event.OccurredAt = "2026-11-04T12:05:00Z"
	event.Attribution = nil
	event.Payload = nil
	event.PayloadSHA256 = ""
	event.RetentionGrant = nil
	return event
}

func encodeFixture(t *testing.T, event Event) []byte {
	t.Helper()
	body, err := Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func replaceOnce(t *testing.T, body []byte, old, replacement string) []byte {
	t.Helper()
	if !bytes.Contains(body, []byte(old)) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return bytes.Replace(body, []byte(old), []byte(replacement), 1)
}

func TestEncodeDecodeSnapshotGoldenAndExactPayload(t *testing.T) {
	event := validSnapshot()
	body, err := Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"protocol":"cookbook-exchange","schema_version":2,"event_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event_type":"recipe.snapshot","source_origin":"https://cookbook.example.org","object_id":"https://cookbook.example.org/federation/recipes/0123456789abcdef0123456789abcdef","sequence":1,"occurred_at":"2026-11-04T12:00:00Z","attribution":{"creator_name":"Ada"},"payload_b64u":"eyJ0aXRsZSI6IlBhc3RhIiwiZGVzY3JpcHRpb24iOiIiLCJpbmdyZWRpZW50cyI6W10sInN0ZXBzIjpbXSwic2VydmluZ3MiOjEsInByZXBfdGltZV9taW51dGVzIjowLCJjb29rX3RpbWVfbWludXRlcyI6MCwidGFncyI6W119","payload_sha256":"51e4a9ee79d8abdf9c50f1643c2e0e834f3b9e47ae5c582472bf96c18296c243","retention_grant":{"purpose":"personal_private_retention","retention":"continues_after_ordinary_withdrawal","portability":"same_person_personal_account_only","object_id":"https://cookbook.example.org/federation/recipes/0123456789abcdef0123456789abcdef","sequence":1,"payload_sha256":"51e4a9ee79d8abdf9c50f1643c2e0e834f3b9e47ae5c582472bf96c18296c243"}}`
	if string(body) != golden {
		t.Fatalf("v2 golden bytes differ:\n%s", body)
	}
	decoded, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Payload, []byte(validRecipe)) || decoded.PayloadSHA256 != validPayloadDigest {
		t.Fatalf("payload bytes/digest changed: payload=%q digest=%s", decoded.Payload, decoded.PayloadSHA256)
	}
	reencoded, err := Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != golden {
		t.Fatalf("round-trip envelope differs:\n%s", reencoded)
	}
}

func TestPayloadDigestUsesExactBytesNotRemarshaledRecipe(t *testing.T) {
	payload := []byte(`{ "title" : "Pasta", "description":"", "ingredients":[], "steps":[], "servings":1, "prep_time_minutes":0, "cook_time_minutes":0, "tags":[] }`)
	event := validSnapshot()
	event.Payload = payload
	event.PayloadSHA256 = PayloadDigest(payload)
	event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
	body := encodeFixture(t, event)
	decoded, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Fatalf("payload bytes were normalized:\n got: %q\nwant: %q", decoded.Payload, payload)
	}
	if decoded.PayloadSHA256 != PayloadDigest(payload) || decoded.PayloadSHA256 == validPayloadDigest {
		t.Fatalf("digest was not calculated over exact bytes: %s", decoded.PayloadSHA256)
	}
}

func TestEncodeDecodeWithdrawalHasNoRecipeFields(t *testing.T) {
	body := encodeFixture(t, validWithdrawal())
	const golden = `{"protocol":"cookbook-exchange","schema_version":2,"event_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","event_type":"recipe.withdrawal","source_origin":"https://cookbook.example.org","object_id":"https://cookbook.example.org/federation/recipes/0123456789abcdef0123456789abcdef","sequence":2,"occurred_at":"2026-11-04T12:05:00Z"}`
	if string(body) != golden {
		t.Fatalf("withdrawal bytes differ: %s", body)
	}
	decoded, err := Decode(body)
	if err != nil || decoded.EventType != RecipeWithdrawal || decoded.Payload != nil || decoded.RetentionGrant != nil {
		t.Fatalf("withdrawal decode=%+v err=%v", decoded, err)
	}
}

func TestGrantAbsentForUpdateIsValidAndNullIsNot(t *testing.T) {
	event := validSnapshot()
	event.EventType = RecipeUpdate
	event.RetentionGrant = nil
	body := encodeFixture(t, event)
	if !bytes.Contains(body, []byte(`"event_type":"recipe.update"`)) || bytes.Contains(body, []byte("retention_grant")) {
		t.Fatalf("grant-absent update shape is wrong: %s", body)
	}
	if _, err := Decode(body); err != nil {
		t.Fatal(err)
	}
	nullGrant := replaceOnce(t, body, `"payload_sha256":"`+validPayloadDigest+`"}`, `"payload_sha256":"`+validPayloadDigest+`","retention_grant":null}`)
	if _, err := Decode(nullGrant); err == nil {
		t.Fatal("accepted explicit null optional grant")
	}
}

func TestDecodeRejectsGrantAndPayloadBindingMutations(t *testing.T) {
	body := encodeFixture(t, validSnapshot())
	tests := map[string][]byte{
		"event digest":   replaceOnce(t, body, validPayloadDigest, "61e4a9ee79d8abdf9c50f1643c2e0e834f3b9e47ae5c582472bf96c18296c243"),
		"grant sequence": replaceOnce(t, body, `"sequence":1,"payload_sha256"`, `"sequence":2,"payload_sha256"`),
		"grant object":   replaceOnce(t, body, `"object_id":"https://cookbook.example.org/federation/recipes/0123456789abcdef0123456789abcdef","sequence":1`, `"object_id":"https://other.example.org/federation/recipes/0123456789abcdef0123456789abcdef","sequence":1`),
		"grant purpose":  replaceOnce(t, body, GrantPurpose, "redistribution"),
		"account field":  replaceOnce(t, body, `"purpose":"personal_private_retention"`, `"account_id":"private","purpose":"personal_private_retention"`),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(candidate); err == nil {
				t.Fatal("accepted invalid grant binding")
			}
		})
	}
}

func TestDecodeRejectsWrongTypeUnknownMissingNullDuplicateAndTrailing(t *testing.T) {
	body := encodeFixture(t, validSnapshot())
	tests := map[string][]byte{
		"unknown top-level":         replaceOnce(t, body, `"event_id":`, `"private_id":"x","event_id":`),
		"wrong-case top-level":      replaceOnce(t, body, `"event_id":`, `"Event_id":`),
		"duplicate top-level":       replaceOnce(t, body, `"event_id":`, `"event_id":"cccccccccccccccccccccccccccccccc","event_id":`),
		"duplicate nested grant":    replaceOnce(t, body, `"purpose":"personal_private_retention"`, `"purpose":"personal_private_retention","purpose":"personal_private_retention"`),
		"missing attribution field": replaceOnce(t, body, `"attribution":{"creator_name":"Ada"}`, `"attribution":{}`),
		"missing grant field":       replaceOnce(t, body, `,"retention":"continues_after_ordinary_withdrawal"`, ``),
		"null creator":              replaceOnce(t, body, `"creator_name":"Ada"`, `"creator_name":null`),
		"null payload digest":       replaceOnce(t, body, `"payload_sha256":"`+validPayloadDigest+`"`, `"payload_sha256":null`),
		"escaped duplicate key":     replaceOnce(t, body, `"protocol":"cookbook-exchange"`, `"protocol":"cookbook-exchange","pro\u0074ocol":"cookbook-exchange"`),
		"trailing value":            append(append([]byte(nil), body...), []byte(` {}`)...),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(candidate); err == nil {
				t.Fatal("accepted invalid event")
			}
		})
	}

	missingRecipe := bytes.Replace([]byte(validRecipe), []byte(`,"tags":[]`), nil, 1)
	event := validSnapshot()
	event.Payload = missingRecipe
	event.PayloadSHA256 = PayloadDigest(missingRecipe)
	event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
	if _, err := Encode(event); err == nil {
		t.Fatal("encoded payload missing a required recipe field")
	}
}

func TestPayloadMatrixOptionalIngredientFieldsAndNoNulls(t *testing.T) {
	payloads := []string{
		`{"title":"Pasta","description":"","ingredients":[{"name":"flour"}],"steps":[{"text":"Mix"}],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`,
		`{"title":"Pasta","description":"","ingredients":[{"name":"flour","quantity":"2 cups","notes":"sifted"}],"steps":[{"text":"Mix"}],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`,
	}
	for _, payload := range payloads {
		event := validSnapshot()
		event.Payload = []byte(payload)
		event.PayloadSHA256 = PayloadDigest(event.Payload)
		event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
		if _, err := Encode(event); err != nil {
			t.Fatalf("valid recipe matrix rejected: %v", err)
		}
	}
	for name, payload := range map[string]string{
		"missing title":           strings.Replace(validRecipe, `"title":"Pasta",`, "", 1),
		"missing description":     strings.Replace(validRecipe, `"description":"",`, "", 1),
		"missing ingredients":     strings.Replace(validRecipe, `"ingredients":[],`, "", 1),
		"missing steps":           strings.Replace(validRecipe, `"steps":[],`, "", 1),
		"missing servings":        strings.Replace(validRecipe, `"servings":1,`, "", 1),
		"missing prep time":       strings.Replace(validRecipe, `"prep_time_minutes":0,`, "", 1),
		"missing cook time":       strings.Replace(validRecipe, `"cook_time_minutes":0,`, "", 1),
		"missing tags":            strings.Replace(validRecipe, `,"tags":[]`, "", 1),
		"missing ingredient name": strings.Replace(validRecipe, `"ingredients":[]`, `"ingredients":[{}]`, 1),
		"missing step text":       strings.Replace(validRecipe, `"steps":[]`, `"steps":[{}]`, 1),
		"null title":              strings.Replace(validRecipe, `"title":"Pasta"`, `"title":null`, 1),
		"null tags":               strings.Replace(validRecipe, `"tags":[]`, `"tags":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			event := validSnapshot()
			event.Payload = []byte(payload)
			event.PayloadSHA256 = PayloadDigest(event.Payload)
			event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
			if _, err := Encode(event); err == nil {
				t.Fatal("accepted missing required or explicit-null recipe field")
			}
		})
	}
	for name, payload := range map[string]string{
		"null optional quantity":    strings.Replace(validRecipe, `"ingredients":[]`, `"ingredients":[{"name":"flour","quantity":null}]`, 1),
		"null optional notes":       strings.Replace(validRecipe, `"ingredients":[]`, `"ingredients":[{"name":"flour","notes":null}]`, 1),
		"null ingredients":          strings.Replace(validRecipe, `"ingredients":[]`, `"ingredients":null`, 1),
		"null step":                 strings.Replace(validRecipe, `"steps":[]`, `"steps":[null]`, 1),
		"unknown nested ingredient": strings.Replace(validRecipe, `"ingredients":[]`, `"ingredients":[{"name":"flour","image":"x"}]`, 1),
		"wrong-case nested key":     strings.Replace(validRecipe, `"ingredients":[]`, `"ingredients":[{"Name":"flour"}]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			event := validSnapshot()
			event.Payload = []byte(payload)
			event.PayloadSHA256 = PayloadDigest(event.Payload)
			event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
			if _, err := Encode(event); err == nil {
				t.Fatal("accepted invalid inner recipe document")
			}
		})
	}
}

func TestCreatorClaimEmptyAndByteLimit(t *testing.T) {
	event := validSnapshot()
	event.Attribution.CreatorName = ""
	if _, err := Encode(event); err != nil {
		t.Fatalf("empty creator claim should remain representable: %v", err)
	}
	event.Attribution.CreatorName = strings.Repeat("a", MaxCreatorNameBytes)
	if _, err := Encode(event); err != nil {
		t.Fatalf("creator claim at byte limit rejected: %v", err)
	}
	event.Attribution.CreatorName += "a"
	if _, err := Encode(event); err == nil {
		t.Fatal("accepted creator claim over byte limit")
	}
	event.Attribution.CreatorName = string([]byte{0xff})
	if _, err := Encode(event); err == nil {
		t.Fatal("accepted invalid UTF-8 creator claim")
	}
}

func TestWithdrawalRejectsAnyContentFieldIncludingNull(t *testing.T) {
	body := encodeFixture(t, validWithdrawal())
	candidates := [][]byte{
		replaceOnce(t, body, `"occurred_at":`, `"attribution":null,"occurred_at":`),
		replaceOnce(t, body, `"occurred_at":`, `"payload_b64u":null,"occurred_at":`),
		replaceOnce(t, body, `"occurred_at":`, `"payload_sha256":null,"occurred_at":`),
		replaceOnce(t, body, `"occurred_at":`, `"retention_grant":null,"occurred_at":`),
	}
	for i, candidate := range candidates {
		if _, err := Decode(candidate); err == nil {
			t.Fatalf("accepted withdrawal with prohibited content field case %d", i)
		}
	}
}

func TestDecodeRejectsUnpairedSurrogatesAndAcceptsPair(t *testing.T) {
	body := encodeFixture(t, validSnapshot())
	badEvent := replaceOnce(t, body, `"creator_name":"Ada"`, `"creator_name":"\uD800"`)
	if _, err := Decode(badEvent); err == nil {
		t.Fatal("accepted unpaired event high surrogate")
	}
	badPayload := []byte(strings.Replace(validRecipe, `"title":"Pasta"`, `"title":"\uDC00"`, 1))
	event := validSnapshot()
	event.Payload = badPayload
	event.PayloadSHA256 = PayloadDigest(badPayload)
	event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
	if _, err := Encode(event); err == nil {
		t.Fatal("accepted unpaired payload low surrogate")
	}
	paired := replaceOnce(t, body, `"creator_name":"Ada"`, `"creator_name":"\uD83D\uDE00"`)
	if _, err := Decode(paired); err != nil {
		t.Fatalf("rejected valid surrogate pair: %v", err)
	}
}

func TestDecodeRejectsBase64AndPayloadDigestErrors(t *testing.T) {
	body := encodeFixture(t, validSnapshot())
	candidates := [][]byte{
		replaceOnce(t, body, validPayloadB64U, validPayloadB64U+"="),
		replaceOnce(t, body, validPayloadB64U, validPayloadB64U[:len(validPayloadB64U)-1]+"A"),
		replaceOnce(t, body, validPayloadB64U, base64.RawURLEncoding.EncodeToString([]byte("not a recipe"))),
	}
	for i, candidate := range candidates {
		if _, err := Decode(candidate); err == nil {
			t.Fatalf("accepted invalid base64/payload case %d", i)
		}
	}
}

func TestDecodeEnforcesEventAndPayloadByteBounds(t *testing.T) {
	body := encodeFixture(t, validSnapshot())
	atLimit := append(append([]byte(nil), body...), bytes.Repeat([]byte{' '}, MaxEventBytes-len(body))...)
	if len(atLimit) != MaxEventBytes {
		t.Fatal("test event boundary construction failed")
	}
	if _, err := Decode(atLimit); err != nil {
		t.Fatalf("rejected exact event byte limit: %v", err)
	}
	if _, err := Decode(append(atLimit, ' ')); err == nil {
		t.Fatal("accepted event over byte limit")
	}

	payload := exactSizeRecipe(t, MaxPayloadBytes)
	event := validSnapshot()
	event.Payload = payload
	event.PayloadSHA256 = PayloadDigest(payload)
	event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
	encoded := encodeFixture(t, event)
	decoded, err := Decode(encoded)
	if err != nil || !bytes.Equal(decoded.Payload, payload) {
		t.Fatalf("exact payload boundary decode err=%v length=%d", err, len(decoded.Payload))
	}
	event.Payload = append(payload, ' ')
	event.PayloadSHA256 = PayloadDigest(event.Payload)
	event.RetentionGrant.PayloadSHA256 = event.PayloadSHA256
	if _, err := Encode(event); err == nil {
		t.Fatal("encoded payload over limit")
	}
}

func exactSizeRecipe(t *testing.T, length int) []byte {
	t.Helper()
	const prefix = `{"title":"Pasta","description":"`
	const suffix = `","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	if length <= len(prefix)+len(suffix) {
		t.Fatal("requested recipe boundary is too small")
	}
	return []byte(prefix + strings.Repeat("a", length-len(prefix)-len(suffix)) + suffix)
}

func TestValidateJSONStructureDepthAndTokenBoundaries(t *testing.T) {
	depth24 := strings.Repeat("[", MaxJSONDepth) + "0" + strings.Repeat("]", MaxJSONDepth)
	if err := validateJSONStructure([]byte(depth24)); err != nil {
		t.Fatalf("rejected exact depth limit: %v", err)
	}
	depth25 := strings.Repeat("[", MaxJSONDepth+1) + "0" + strings.Repeat("]", MaxJSONDepth+1)
	if err := validateJSONStructure([]byte(depth25)); err == nil {
		t.Fatal("accepted depth over limit")
	}
	tokensAtLimit := "[" + strings.Repeat("0,", MaxJSONTokens-2) + "0]"
	if err := validateJSONStructure([]byte(tokensAtLimit)); err != nil {
		t.Fatalf("rejected exact token limit: %v", err)
	}
	tokensOverLimit := "[" + strings.Repeat("0,", MaxJSONTokens-1) + "0]"
	if err := validateJSONStructure([]byte(tokensOverLimit)); err == nil {
		t.Fatal("accepted token count over limit")
	}
}

func TestOriginAndObjectIDMatchStableV1Shape(t *testing.T) {
	for _, origin := range []string{
		"https://cookbook.example.org",
		"https://sub.domain.example.net",
	} {
		if !canonicalOrigin(origin) {
			t.Errorf("rejected canonical origin %q", origin)
		}
	}
	for _, origin := range []string{
		"https://cookbook.example.org/",
		"https://cookbook.example.org:443",
		"https://COOKBOOK.example.org",
		"https://cookbook.example.org.",
		"https://cookbook.example",
		"https://cookbook.example.org/path",
		"https://cookbook.example.org?x=1",
		"https://127.0.0.1",
		"http://cookbook.example.org",
	} {
		if canonicalOrigin(origin) {
			t.Errorf("accepted noncanonical origin %q", origin)
		}
	}
	event := validSnapshot()
	event.ObjectID = "https://other.example.org" + ObjectIDMarker + "0123456789abcdef0123456789abcdef"
	if _, err := Encode(event); err == nil {
		t.Fatal("accepted foreign object origin")
	}
	maxOrigin := "https://" + strings.Join(makeLongDNSLabels(504), ".")
	if len(maxOrigin) != MaxOriginBytes || !canonicalOrigin(maxOrigin) {
		t.Fatalf("exact origin boundary fixture invalid: length=%d", len(maxOrigin))
	}
	event = validSnapshot()
	event.SourceOrigin = maxOrigin
	event.ObjectID = maxOrigin + ObjectIDMarker + "0123456789abcdef0123456789abcdef"
	event.RetentionGrant.ObjectID = event.ObjectID
	if _, err := Encode(event); err != nil {
		t.Fatalf("rejected origin at byte limit: %v", err)
	}
	event.SourceOrigin += "a"
	event.ObjectID = event.SourceOrigin + ObjectIDMarker + "0123456789abcdef0123456789abcdef"
	event.RetentionGrant.ObjectID = event.ObjectID
	if _, err := Encode(event); err == nil {
		t.Fatal("accepted origin over byte limit")
	}
}

func makeLongDNSLabels(hostLength int) []string {
	labels := make([]string, 8)
	for i := range 7 {
		labels[i] = strings.Repeat("a", 63)
	}
	labels[7] = strings.Repeat("b", hostLength-7*63-7)
	return labels
}

func TestRequiredTopLevelAndMetadataValidation(t *testing.T) {
	body := encodeFixture(t, validSnapshot())
	tests := map[string][]byte{
		"missing protocol":    replaceOnce(t, body, `"protocol":"cookbook-exchange",`, ""),
		"missing occurred_at": replaceOnce(t, body, `,"occurred_at":"2026-11-04T12:00:00Z"`, ""),
		"zero sequence":       replaceOnce(t, body, `"sequence":1`, `"sequence":0`),
		"bad timestamp":       replaceOnce(t, body, "2026-11-04T12:00:00Z", "not-a-time"),
		"upper event id":      replaceOnce(t, body, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		"wrong version":       replaceOnce(t, body, `"schema_version":2`, `"schema_version":1`),
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(candidate); err == nil {
				t.Fatal("accepted invalid event metadata")
			}
		})
	}
	maxTimestamp := "2006-01-02T15:04:05.123456789+15:00"
	if len(maxTimestamp) != MaxOccurredAtBytes {
		t.Fatalf("timestamp boundary fixture has length %d", len(maxTimestamp))
	}
	event := validSnapshot()
	event.OccurredAt = maxTimestamp
	if _, err := Encode(event); err != nil {
		t.Fatalf("rejected occurred_at at byte limit: %v", err)
	}
	event.OccurredAt = "2006-01-02T15:04:05.1234567890+15:00"
	if _, err := Encode(event); err == nil {
		t.Fatal("accepted invalid/overlong occurred_at")
	}
}

func TestOccurredAtProfileGrammar(t *testing.T) {
	valid := []string{
		"2000-02-29T00:00:00Z",
		"2026-11-04T12:00:00.1Z",
		"2026-11-04T12:00:00.123456789+23:59",
		"2026-11-04T12:00:00-00:00",
	}
	for _, timestamp := range valid {
		t.Run("valid/"+timestamp, func(t *testing.T) {
			event := validSnapshot()
			event.OccurredAt = timestamp
			if _, err := Encode(event); err != nil {
				t.Fatalf("rejected profile-valid timestamp %q: %v", timestamp, err)
			}
		})
	}

	invalid := []string{
		"2026-11-04T12:00:00,1Z",    // Go accepts comma fractions.
		"2026-11-04T2:00:00Z",       // Go accepts a single-digit hour.
		"2026-11-04t12:00:00Z",      // Lowercase T is outside the profile.
		"2026-11-04T12:00:00z",      // Lowercase Z is outside the profile.
		"2026-11-04T12:00:00+24:00", // Go accepts out-of-range offset hours.
		"2026-11-04T12:00:00+00:60", // Go accepts out-of-range offset minutes.
		"2026-02-29T12:00:00Z",      // Invalid calendar date.
		"2026-11-04T12:00:00.Z",     // Empty fractional part.
		"2026-11-04 12:00:00Z",      // Separator must be uppercase T.
		"2026-11-04T12:00:00+0100",  // Offset colon is required.
	}
	for _, timestamp := range invalid {
		t.Run("invalid/"+timestamp, func(t *testing.T) {
			event := validSnapshot()
			event.OccurredAt = timestamp
			if _, err := Encode(event); err == nil {
				t.Fatalf("accepted out-of-profile timestamp %q", timestamp)
			}
		})
	}
}

func TestStandardLibraryOnlyProductionFile(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "event.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"bytes": true, "crypto/sha256": true, "encoding/base64": true,
		"encoding/hex": true, "encoding/json": true, "errors": true,
		"fmt": true, "io": true, "net": true, "net/url": true,
		"strings": true, "time": true, "unicode/utf8": true,
	}
	for _, spec := range file.Imports {
		var importPath string
		if err := json.Unmarshal([]byte(spec.Path.Value), &importPath); err != nil {
			t.Fatal(err)
		}
		if !allowed[importPath] {
			t.Errorf("production import %q is outside the stdlib allowlist", importPath)
		}
	}
}
