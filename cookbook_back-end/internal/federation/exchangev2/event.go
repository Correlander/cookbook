// Package exchangev2 implements the syntax-only Exchange v2 retention-grant
// event codec. It does not authenticate a sender, authorize a grant, or apply
// federation state transitions.
package exchangev2

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Protocol            = "cookbook-exchange"
	SchemaVersion       = 2
	MaxEventBytes       = 2 << 20
	MaxPayloadBytes     = 1_500_000
	MaxJSONDepth        = 24
	MaxJSONTokens       = 20_000
	MaxOriginBytes      = 512
	MaxObjectIDBytes    = 768
	MaxOccurredAtBytes  = 35
	MaxCreatorNameBytes = 200
	ObjectIDMarker      = "/federation/recipes/"
	GrantPurpose        = "personal_private_retention"
	GrantRetention      = "continues_after_ordinary_withdrawal"
	GrantPortability    = "same_person_personal_account_only"
	RecipeSnapshot      = "recipe.snapshot"
	RecipeUpdate        = "recipe.update"
	RecipeWithdrawal    = "recipe.withdrawal"
)

type Event struct {
	Protocol      string
	SchemaVersion int
	EventID       string
	EventType     string
	SourceOrigin  string
	ObjectID      string
	Sequence      int64
	OccurredAt    string
	Attribution   *Attribution

	// Payload is the exact decoded UTF-8 recipe document. Encode base64url
	// encodes these bytes without parsing or re-marshaling them.
	Payload        []byte
	PayloadSHA256  string
	RetentionGrant *RetentionGrant
}

type Attribution struct {
	CreatorName string `json:"creator_name"`
}

type RetentionGrant struct {
	Purpose       string `json:"purpose"`
	Retention     string `json:"retention"`
	Portability   string `json:"portability"`
	ObjectID      string `json:"object_id"`
	Sequence      int64  `json:"sequence"`
	PayloadSHA256 string `json:"payload_sha256"`
}

type wireEvent struct {
	Protocol       string          `json:"protocol"`
	SchemaVersion  int             `json:"schema_version"`
	EventID        string          `json:"event_id"`
	EventType      string          `json:"event_type"`
	SourceOrigin   string          `json:"source_origin"`
	ObjectID       string          `json:"object_id"`
	Sequence       int64           `json:"sequence"`
	OccurredAt     string          `json:"occurred_at"`
	Attribution    *Attribution    `json:"attribution,omitempty"`
	PayloadB64U    string          `json:"payload_b64u,omitempty"`
	PayloadSHA256  string          `json:"payload_sha256,omitempty"`
	RetentionGrant *RetentionGrant `json:"retention_grant,omitempty"`
}

// PayloadDigest returns lowercase hexadecimal SHA-256 of exact payload bytes.
// It is version/integrity metadata, not a signature or permission proof.
func PayloadDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// Decode enforces the bounded, strict v2 JSON syntax. Domain recipe validation,
// peer capability, signatures, replay policy, and grant authorization belong
// to higher layers.
func Decode(body []byte) (Event, error) {
	var result Event
	if len(body) == 0 || len(body) > MaxEventBytes || !utf8.Valid(body) {
		return result, errors.New("invalid Exchange v2 event body")
	}
	if err := rejectUnpairedSurrogates(body); err != nil {
		return result, err
	}
	if err := validateJSONStructure(body); err != nil {
		return result, err
	}

	var kind struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal(body, &kind); err != nil {
		return result, err
	}
	if kind.EventType != RecipeSnapshot && kind.EventType != RecipeUpdate && kind.EventType != RecipeWithdrawal {
		return result, errors.New("unsupported Exchange v2 event type")
	}
	if err := validateSchema(body, "event", kind.EventType); err != nil {
		return result, err
	}

	var wire wireEvent
	if err := json.Unmarshal(body, &wire); err != nil {
		return result, err
	}
	result = Event{
		Protocol: wire.Protocol, SchemaVersion: wire.SchemaVersion,
		EventID: wire.EventID, EventType: wire.EventType,
		SourceOrigin: wire.SourceOrigin, ObjectID: wire.ObjectID,
		Sequence: wire.Sequence, OccurredAt: wire.OccurredAt,
		Attribution: wire.Attribution, PayloadSHA256: wire.PayloadSHA256,
		RetentionGrant: wire.RetentionGrant,
	}
	if kind.EventType == RecipeSnapshot || kind.EventType == RecipeUpdate {
		if len(wire.PayloadB64U) == 0 || len(wire.PayloadB64U) > base64.RawURLEncoding.EncodedLen(MaxPayloadBytes) {
			return Event{}, errors.New("invalid Exchange v2 payload length")
		}
		payload, err := base64.RawURLEncoding.Strict().DecodeString(wire.PayloadB64U)
		if err != nil || len(payload) == 0 || len(payload) > MaxPayloadBytes || base64.RawURLEncoding.EncodeToString(payload) != wire.PayloadB64U {
			return Event{}, errors.New("invalid Exchange v2 base64url payload")
		}
		if !utf8.Valid(payload) {
			return Event{}, errors.New("invalid Exchange v2 payload UTF-8")
		}
		if err := rejectUnpairedSurrogates(payload); err != nil {
			return Event{}, fmt.Errorf("invalid Exchange v2 payload: %w", err)
		}
		if err := validateJSONStructure(payload); err != nil {
			return Event{}, fmt.Errorf("invalid Exchange v2 payload: %w", err)
		}
		if err := validateSchema(payload, "recipe", ""); err != nil {
			return Event{}, fmt.Errorf("invalid Exchange v2 payload: %w", err)
		}
		if wire.PayloadSHA256 != PayloadDigest(payload) {
			return Event{}, errors.New("Exchange v2 payload digest mismatch")
		}
		result.Payload = append([]byte(nil), payload...)
	}
	if err := validateEvent(result); err != nil {
		return Event{}, err
	}
	return result, nil
}

// Encode returns a compact event envelope while preserving Payload bytes
// exactly inside canonical unpadded base64url. Re-encoding does not preserve
// original outer JSON whitespace or member order; transport evidence must keep
// the exact received HTTP body separately.
func Encode(event Event) ([]byte, error) {
	if err := validateEvent(event); err != nil {
		return nil, err
	}
	wire := wireEvent{
		Protocol: event.Protocol, SchemaVersion: event.SchemaVersion,
		EventID: event.EventID, EventType: event.EventType,
		SourceOrigin: event.SourceOrigin, ObjectID: event.ObjectID,
		Sequence: event.Sequence, OccurredAt: event.OccurredAt,
		Attribution: event.Attribution, PayloadSHA256: event.PayloadSHA256,
		RetentionGrant: event.RetentionGrant,
	}
	if event.EventType == RecipeSnapshot || event.EventType == RecipeUpdate {
		wire.PayloadB64U = base64.RawURLEncoding.EncodeToString(event.Payload)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxEventBytes {
		return nil, errors.New("Exchange v2 event exceeds body limit")
	}
	return body, nil
}

func validOccurredAt(value string) bool {
	if len(value) < len("2006-01-02T15:04:05Z") || len(value) > MaxOccurredAtBytes || !isASCII(value) {
		return false
	}
	// Require the profile's fixed-width RFC3339 date/time spelling before
	// delegating calendar and clock validation to Go's parser.
	if value[4] != '-' || value[7] != '-' || value[10] != 'T' || value[13] != ':' || value[16] != ':' {
		return false
	}
	for _, index := range [...]int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	zoneStart := 19
	if zoneStart < len(value) && value[zoneStart] == '.' {
		zoneStart++
		fractionStart := zoneStart
		for zoneStart < len(value) && value[zoneStart] >= '0' && value[zoneStart] <= '9' {
			zoneStart++
		}
		if zoneStart == fractionStart {
			return false
		}
	}
	if zoneStart >= len(value) {
		return false
	}
	switch value[zoneStart] {
	case 'Z':
		if zoneStart != len(value)-1 {
			return false
		}
	case '+', '-':
		if zoneStart+6 != len(value) || value[zoneStart+3] != ':' {
			return false
		}
		for _, index := range [...]int{zoneStart + 1, zoneStart + 2, zoneStart + 4, zoneStart + 5} {
			if value[index] < '0' || value[index] > '9' {
				return false
			}
		}
		hour := (int(value[zoneStart+1]-'0') * 10) + int(value[zoneStart+2]-'0')
		minute := (int(value[zoneStart+4]-'0') * 10) + int(value[zoneStart+5]-'0')
		if hour > 23 || minute > 59 {
			return false
		}
	default:
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validateEvent(event Event) error {
	if event.Protocol != Protocol || event.SchemaVersion != SchemaVersion || !validHexID(event.EventID) || event.Sequence < 1 {
		return errors.New("invalid Exchange v2 event metadata")
	}
	if len(event.SourceOrigin) > MaxOriginBytes || !canonicalOrigin(event.SourceOrigin) {
		return errors.New("invalid Exchange v2 source origin")
	}
	if len(event.ObjectID) > MaxObjectIDBytes || !validObjectID(event.SourceOrigin, event.ObjectID) {
		return errors.New("invalid Exchange v2 object ID")
	}
	if !validOccurredAt(event.OccurredAt) {
		return errors.New("invalid Exchange v2 occurred_at")
	}

	switch event.EventType {
	case RecipeSnapshot, RecipeUpdate:
		if event.Attribution == nil || !utf8.ValidString(event.Attribution.CreatorName) || len(event.Attribution.CreatorName) > MaxCreatorNameBytes {
			return errors.New("invalid Exchange v2 attribution")
		}
		if len(event.Payload) == 0 || len(event.Payload) > MaxPayloadBytes || !utf8.Valid(event.Payload) {
			return errors.New("invalid Exchange v2 payload")
		}
		if err := rejectUnpairedSurrogates(event.Payload); err != nil {
			return err
		}
		if err := validateJSONStructure(event.Payload); err != nil {
			return err
		}
		if err := validateSchema(event.Payload, "recipe", ""); err != nil {
			return err
		}
		if event.PayloadSHA256 != PayloadDigest(event.Payload) {
			return errors.New("Exchange v2 payload digest mismatch")
		}
		if event.RetentionGrant != nil {
			if err := validateGrant(*event.RetentionGrant, event); err != nil {
				return err
			}
		}
	case RecipeWithdrawal:
		if event.Attribution != nil || event.Payload != nil || event.PayloadSHA256 != "" || event.RetentionGrant != nil {
			return errors.New("Exchange v2 withdrawal cannot contain recipe or grant fields")
		}
	default:
		return errors.New("unsupported Exchange v2 event type")
	}
	return nil
}

func validateGrant(grant RetentionGrant, event Event) error {
	if grant.Purpose != GrantPurpose || grant.Retention != GrantRetention || grant.Portability != GrantPortability ||
		grant.ObjectID != event.ObjectID || grant.Sequence != event.Sequence || grant.PayloadSHA256 != event.PayloadSHA256 {
		return errors.New("invalid Exchange v2 retention grant binding or scope")
	}
	return nil
}

func validHexID(value string) bool {
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

func validObjectID(origin, objectID string) bool {
	if !isASCII(objectID) || !strings.HasPrefix(objectID, origin+ObjectIDMarker) {
		return false
	}
	return validHexID(strings.TrimPrefix(objectID, origin+ObjectIDMarker))
}

func canonicalOrigin(origin string) bool {
	if origin == "" || !isASCII(origin) {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.ForceQuery || parsed.RawQuery != "" || strings.Contains(origin, "#") {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.HasSuffix(host, ".") || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return false
	}
	for _, reserved := range []string{".localhost", ".local", ".test", ".invalid", ".example", ".internal"} {
		if host == strings.TrimPrefix(reserved, ".") || strings.HasSuffix(host, reserved) {
			return false
		}
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return false
	}
	// Equality to the existing canonical representation rejects uppercase
	// hosts, explicit :443, a trailing slash, and all noncanonical spellings.
	return origin == "https://"+host
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
}

// validateJSONStructure uses the same documented limits for an event and its
// decoded payload. A token is each JSON value (including delimiters and
// scalars) plus each object-member key; closing delimiters are not tokens.
// Root containers have depth one, so at most 24 nested containers are allowed.
func validateJSONStructure(body []byte) error {
	if !utf8.Valid(body) {
		return errors.New("invalid JSON UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	state := jsonParseState{}
	if err := consumeJSONValue(decoder, &state, 0); err != nil {
		return err
	}
	if err := ensureEOF(decoder); err != nil {
		return err
	}
	return nil
}

type jsonParseState struct{ tokens int }

func consumeJSONValue(decoder *json.Decoder, state *jsonParseState, parentDepth int) error {
	state.tokens++
	if state.tokens > MaxJSONTokens {
		return errors.New("JSON token limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("JSON null is not allowed")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	depth := parentDepth + 1
	if depth > MaxJSONDepth {
		return errors.New("JSON nesting depth exceeded")
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			state.tokens++
			if state.tokens > MaxJSONTokens {
				return errors.New("JSON token limit exceeded")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder, state, depth); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder, state, depth); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func ensureEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return errors.New("invalid trailing JSON")
	}
	return errors.New("trailing JSON value")
}

func validateSchema(body []byte, rootKind, eventType string) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := validateSchemaValue(decoder, rootKind, eventType); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func validateSchemaValue(decoder *json.Decoder, kind, eventType string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("JSON null is not allowed")
	}
	if kind == "ingredient_array" || kind == "step_array" || kind == "string_array" {
		delimiter, ok := token.(json.Delim)
		if !ok || delimiter != '[' {
			return fmt.Errorf("%s must be an array", kind)
		}
		itemKind := "ingredient"
		switch kind {
		case "step_array":
			itemKind = "step"
		case "string_array":
			itemKind = "scalar"
		}
		for decoder.More() {
			if err := validateSchemaValue(decoder, itemKind, eventType); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	}
	if kind == "scalar" {
		if _, nested := token.(json.Delim); nested {
			return errors.New("unexpected JSON object or array for scalar field")
		}
		return nil
	}
	if !isSchemaObject(kind) {
		if _, nested := token.(json.Delim); nested {
			return errors.New("unexpected JSON object or array for scalar field")
		}
		return nil
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("%s must be an object", kind)
	}
	allowed, required, children := schemaFields(kind, eventType)
	seen := make(map[string]struct{}, len(required))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid JSON object key")
		}
		childKind, exists := children[key]
		if !exists && !allowed[key] {
			return fmt.Errorf("unknown or incorrectly cased %s field", kind)
		}
		seen[key] = struct{}{}
		if err := validateSchemaValue(decoder, childKind, eventType); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for key := range required {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("missing required %s field %q", kind, key)
		}
	}
	return nil
}

func isSchemaObject(kind string) bool {
	switch kind {
	case "event", "attribution", "grant", "recipe", "ingredient", "step":
		return true
	default:
		return false
	}
}

func schemaFields(kind, eventType string) (map[string]bool, map[string]bool, map[string]string) {
	fields := func(names ...string) map[string]bool {
		result := make(map[string]bool, len(names))
		for _, name := range names {
			result[name] = true
		}
		return result
	}
	switch kind {
	case "event":
		common := []string{"protocol", "schema_version", "event_id", "event_type", "source_origin", "object_id", "sequence", "occurred_at"}
		allowed := fields(common...)
		required := fields(common...)
		children := map[string]string{}
		if eventType == RecipeSnapshot || eventType == RecipeUpdate {
			allowed["attribution"] = true
			allowed["payload_b64u"] = true
			allowed["payload_sha256"] = true
			allowed["retention_grant"] = true
			required["attribution"] = true
			required["payload_b64u"] = true
			required["payload_sha256"] = true
			children["attribution"] = "attribution"
			children["retention_grant"] = "grant"
		} else {
			// recipe.withdrawal requires exactly the eight common fields.
		}
		return allowed, required, children
	case "attribution":
		return fields("creator_name"), fields("creator_name"), nil
	case "grant":
		keys := []string{"purpose", "retention", "portability", "object_id", "sequence", "payload_sha256"}
		return fields(keys...), fields(keys...), nil
	case "recipe":
		keys := []string{"title", "description", "ingredients", "steps", "servings", "prep_time_minutes", "cook_time_minutes", "tags"}
		return fields(keys...), fields(keys...), map[string]string{"ingredients": "ingredient_array", "steps": "step_array", "tags": "string_array"}
	case "ingredient":
		return fields("name", "quantity", "notes"), fields("name"), nil
	case "step":
		return fields("text"), fields("text"), nil
	default:
		return nil, nil, nil
	}
}

// rejectUnpairedSurrogates rejects JSON string escapes that Go's decoder would
// otherwise replace with U+FFFD. Raw UTF-8 validity is checked separately.
func rejectUnpairedSurrogates(body []byte) error {
	for i := 0; i < len(body); {
		if body[i] != '"' {
			i++
			continue
		}
		i++
		for i < len(body) && body[i] != '"' {
			if body[i] != '\\' {
				i++
				continue
			}
			if i+1 >= len(body) {
				return errors.New("truncated JSON escape")
			}
			if body[i+1] != 'u' {
				i += 2
				continue
			}
			value, ok := unicodeEscape(body, i)
			if !ok {
				return errors.New("invalid Unicode escape")
			}
			if value >= 0xD800 && value <= 0xDBFF {
				next := i + 6
				if next+6 > len(body) || body[next] != '\\' || body[next+1] != 'u' {
					return errors.New("unpaired high surrogate")
				}
				low, ok := unicodeEscape(body, next)
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return errors.New("unpaired high surrogate")
				}
				i = next + 6
				continue
			}
			if value >= 0xDC00 && value <= 0xDFFF {
				return errors.New("unpaired low surrogate")
			}
			i += 6
		}
		if i >= len(body) {
			return errors.New("unterminated JSON string")
		}
		i++
	}
	return nil
}

func unicodeEscape(body []byte, slash int) (uint16, bool) {
	if slash+6 > len(body) || body[slash] != '\\' || body[slash+1] != 'u' {
		return 0, false
	}
	var value uint16
	for _, char := range body[slash+2 : slash+6] {
		value <<= 4
		switch {
		case char >= '0' && char <= '9':
			value |= uint16(char - '0')
		case char >= 'a' && char <= 'f':
			value |= uint16(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value |= uint16(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
