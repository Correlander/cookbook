package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	activityStreamsContext  = "https://www.w3.org/ns/activitystreams"
	activityMaxJSONDepth    = 24
	activityMaxJSONTokens   = 20000
	activityMaxURIBytes     = 2048
	activityMaxNameBytes    = 200
	activityMaxSummaryBytes = 5000
	activityMaxRecipients   = 100
	// A supported control activity may contain one embedded Follow, so its
	// outer and embedded recipient lists are each bounded and together capped.
	activityMaxTotalRecipients = 2 * activityMaxRecipients
)

// federationActivityProfile is supplied by the integration owner. The version and
// extension namespace are local configuration; they are never inferred from input.
type federationActivityProfile struct {
	version            string
	extensionPrefix    string
	extensionNamespace string
}

func (p federationActivityProfile) sequenceField() string { return p.extensionPrefix + ":sequence" }
func (p federationActivityProfile) sourceField() string   { return p.extensionPrefix + ":source" }
func (p federationActivityProfile) adaptedFromField() string {
	return p.extensionPrefix + ":adaptedFrom"
}

type validatedFederationActivity struct {
	ProfileVersion string
	ID             string
	Type           string
	Actor          string
	ObjectID       string
	ObjectType     string
	AttributedTo   string
	Name           string
	Summary        string
	Source         string
	AdaptedFrom    string
	Sequence       int64
	Recipients     []string
}

type activityJSON struct {
	Context  json.RawMessage `json:"@context"`
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Actor    string          `json:"actor"`
	Object   json.RawMessage `json:"object"`
	To       []string        `json:"to,omitempty"`
	Sequence json.RawMessage `json:"-"`
}

type activityObjectJSON struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	AttributedTo string `json:"attributedTo"`
	Name         string `json:"name,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Source       string `json:"-"`
	AdaptedFrom  string `json:"-"`
}

// decodeFederationActivity validates bounded syntax against an origin already
// authenticated by its caller. The caller must verify the request signature
// before decoding and authorize local Follow/Undo targets and resource access
// before any state mutation; this helper does not perform either operation.
func decodeFederationActivity(body []byte, authenticatedOrigin string, profile federationActivityProfile) (validatedFederationActivity, error) {
	var zero validatedFederationActivity
	if len(body) == 0 || int64(len(body)) > maxFederationEventBytes || !utf8.Valid(body) {
		return zero, errors.New("invalid activity body")
	}
	if err := validateFederationActivityProfile(profile); err != nil {
		return zero, err
	}
	if err := validateUniqueBoundedJSON(body, profile); err != nil {
		return zero, err
	}
	origin, err := canonicalFederationOrigin(authenticatedOrigin)
	if err != nil || origin != authenticatedOrigin {
		return zero, errors.New("invalid authenticated origin")
	}
	event, err := decodeActivityEnvelope(body, profile)
	if err != nil {
		return zero, err
	}
	if err := validateActivityContext(event.Context, true, profile); err != nil {
		return zero, err
	}
	if err := validateActivityActorURI(event.Actor); err != nil {
		return zero, errors.New("invalid activity actor")
	}
	actorOrigin, _, _ := parseActivityURI(event.Actor)
	if actorOrigin != origin {
		return zero, errors.New("activity actor does not match authenticated origin")
	}
	if err := validateActivityDocumentURI(event.ID, "/activities/"); err != nil {
		return zero, errors.New("invalid activity ID")
	}
	activityOrigin, _, _ := parseActivityURI(event.ID)
	if activityOrigin != origin {
		return zero, errors.New("activity ID does not match authenticated origin")
	}
	if err := validateActivityRecipients(event.To); err != nil {
		return zero, err
	}

	result := validatedFederationActivity{ProfileVersion: profile.version, ID: event.ID, Type: event.Type, Actor: event.Actor, Recipients: append([]string(nil), event.To...)}
	sequence, hasSequence, err := parseActivitySequence(event.Sequence)
	if err != nil {
		return zero, err
	}
	if hasSequence && !activityContextHasExtensions(event.Context, profile) {
		return zero, errors.New("activity extension requires the configured inline context")
	}
	result.Sequence = sequence
	switch event.Type {
	case "Follow":
		if hasSequence {
			return zero, errors.New("Follow cannot carry a cookbook sequence")
		}
		var target string
		if err := json.Unmarshal(event.Object, &target); err != nil || validateActivityActorURI(target) != nil {
			return zero, errors.New("Follow object must identify an actor")
		}
		result.ObjectID = target
	case "Accept", "Undo":
		if hasSequence {
			return zero, errors.New("control activity cannot carry a cookbook sequence")
		}
		var embedded activityJSON
		if err := decodeEmbeddedActivity(event.Object, &embedded, profile); err != nil {
			return zero, err
		}
		if len(embedded.Sequence) != 0 || embedded.Type != "Follow" || validateActivityActorURI(embedded.Actor) != nil {
			return zero, errors.New("control activity object must be a Follow")
		}
		if err := validateActivityRecipients(embedded.To); err != nil {
			return zero, fmt.Errorf("invalid embedded Follow recipients: %w", err)
		}
		if len(event.To)+len(embedded.To) > activityMaxTotalRecipients {
			return zero, errors.New("too many total control activity recipients")
		}
		embeddedOrigin, _, _ := parseActivityURI(embedded.Actor)
		if err := validateActivityDocumentURI(embedded.ID, "/activities/"); err != nil {
			return zero, errors.New("invalid embedded Follow ID")
		}
		embeddedIDOrigin, _, _ := parseActivityURI(embedded.ID)
		if embeddedIDOrigin != embeddedOrigin {
			return zero, errors.New("embedded Follow origin mismatch")
		}
		var target string
		if err := json.Unmarshal(embedded.Object, &target); err != nil || validateActivityActorURI(target) != nil {
			return zero, errors.New("embedded Follow target is invalid")
		}
		if event.Type == "Accept" {
			targetOrigin, _, _ := parseActivityURI(target)
			if target != event.Actor || targetOrigin != origin || embeddedOrigin == origin {
				return zero, errors.New("Accept does not target the authenticated actor")
			}
		} else if embedded.Actor != event.Actor || embeddedOrigin != origin {
			return zero, errors.New("Undo does not match the authenticated Follow actor")
		}
		result.ObjectID = embedded.ID
	case "Create", "Update":
		if !hasSequence {
			return zero, errors.New("content activity requires a cookbook sequence")
		}
		object, err := decodeActivityObject(event.Object, profile)
		if err != nil {
			return zero, err
		}
		if err := validateActivityObject(object, origin, event.Actor, activityContextHasExtensions(event.Context, profile), profile); err != nil {
			return zero, err
		}
		result.ObjectID, result.ObjectType = object.ID, object.Type
		result.AttributedTo, result.Name, result.Summary = object.AttributedTo, object.Name, object.Summary
		result.Source, result.AdaptedFrom = object.Source, object.AdaptedFrom
	case "Delete":
		if !hasSequence {
			return zero, errors.New("Delete requires a cookbook sequence")
		}
		var objectID string
		if err := json.Unmarshal(event.Object, &objectID); err == nil {
			if err := validateActivityObjectURI(objectID, origin); err != nil {
				return zero, errors.New("Delete object origin mismatch")
			}
			result.ObjectID = objectID
		} else {
			var tombstone struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if err := decodeStrictObject(event.Object, &tombstone); err != nil || tombstone.Type != "Tombstone" || validateActivityObjectURI(tombstone.ID, origin) != nil {
				return zero, errors.New("Delete must identify a same-origin object or Tombstone")
			}
			result.ObjectID, result.ObjectType = tombstone.ID, tombstone.Type
		}
	default:
		return zero, fmt.Errorf("unsupported ActivityStreams activity type %q", event.Type)
	}
	return result, nil
}

func validateActivityRecipients(recipients []string) error {
	if len(recipients) > activityMaxRecipients {
		return errors.New("too many activity recipients")
	}
	for _, recipient := range recipients {
		if recipient == "https://www.w3.org/ns/activitystreams#Public" {
			continue
		}
		if err := validateActivityActorURI(recipient); err != nil {
			return errors.New("invalid activity recipient")
		}
	}
	return nil
}

func validateUniqueBoundedJSON(body []byte, profile federationActivityProfile) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	state := activityJSONParseState{}
	if err := consumeUniqueActivityJSON(decoder, &state, 0, profile); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing activity JSON")
	}
	return nil
}

func isAllowedActivityJSONKey(key string, profile federationActivityProfile) bool {
	switch key {
	case "@context", "id", "type", "actor", "object", "to", "attributedTo", "name", "summary":
		return true
	}
	return key == profile.sequenceField() || key == profile.sourceField() || key == profile.adaptedFromField() || key == profile.extensionPrefix
}

type activityJSONParseState struct{ tokens int }

func consumeUniqueActivityJSON(decoder *json.Decoder, state *activityJSONParseState, depth int, profile federationActivityProfile) error {
	state.tokens++
	if state.tokens > activityMaxJSONTokens || depth > activityMaxJSONDepth {
		return errors.New("activity JSON exceeds structural limits")
	}
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
		for decoder.More() {
			state.tokens++
			if state.tokens > activityMaxJSONTokens {
				return errors.New("activity JSON exceeds structural limits")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid activity JSON object key")
			}
			if !isAllowedActivityJSONKey(key, profile) {
				return errors.New("unknown or incorrectly cased activity JSON field")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate activity JSON key")
			}
			seen[key] = struct{}{}
			if err := consumeUniqueActivityJSON(decoder, state, depth+1, profile); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeUniqueActivityJSON(decoder, state, depth+1, profile); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid activity JSON delimiter")
	}
}

func validateFederationActivityProfile(profile federationActivityProfile) error {
	if profile.version == "" || len(profile.version) > 64 || profile.extensionPrefix == "" || len(profile.extensionPrefix) > 32 || profile.extensionNamespace == "" || len(profile.extensionNamespace) > activityMaxURIBytes {
		return errors.New("ActivityStreams profile version and extension mapping are required")
	}
	for _, char := range profile.version {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
			return errors.New("invalid ActivityStreams profile version")
		}
	}
	for _, char := range profile.extensionPrefix {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return errors.New("invalid ActivityStreams extension prefix")
		}
	}
	firstPrefixChar := profile.extensionPrefix[0]
	if (firstPrefixChar < 'a' || firstPrefixChar > 'z') && (firstPrefixChar < 'A' || firstPrefixChar > 'Z') && firstPrefixChar != '_' {
		return errors.New("invalid ActivityStreams extension prefix")
	}
	if strings.ContainsAny(profile.extensionNamespace, "\\\t\r\n ") {
		return errors.New("invalid ActivityStreams extension namespace")
	}
	namespace, err := url.Parse(profile.extensionNamespace)
	if err != nil || namespace.Scheme == "" || namespace.Opaque != "" || namespace.User != nil || namespace.RawQuery != "" {
		return errors.New("extension namespace must be an absolute URI")
	}
	return nil
}

func validateActivityContext(raw json.RawMessage, required bool, profile federationActivityProfile) error {
	if len(raw) == 0 {
		if required {
			return errors.New("ActivityStreams context is required")
		}
		return nil
	}
	var contextURI string
	if err := json.Unmarshal(raw, &contextURI); err == nil && contextURI == activityStreamsContext {
		return nil
	}
	var contextParts []json.RawMessage
	if json.Unmarshal(raw, &contextParts) != nil || len(contextParts) != 2 {
		return errors.New("unsupported ActivityStreams context")
	}
	var baseContext string
	if json.Unmarshal(contextParts[0], &baseContext) != nil || baseContext != activityStreamsContext {
		return errors.New("unsupported ActivityStreams base context")
	}
	var extension map[string]string
	if json.Unmarshal(contextParts[1], &extension) != nil || len(extension) != 1 || extension[profile.extensionPrefix] != profile.extensionNamespace {
		return errors.New("unsupported inline ActivityStreams extension")
	}
	return nil
}

func decodeEmbeddedActivity(raw json.RawMessage, result *activityJSON, profile federationActivityProfile) error {
	if len(raw) == 0 || len(raw) > maxFederationEventBytes {
		return errors.New("invalid embedded activity")
	}
	if err := validateUniqueBoundedJSON(raw, profile); err != nil {
		return err
	}
	decoded, err := decodeActivityEnvelope(raw, profile)
	if err != nil {
		return errors.New("invalid embedded activity shape")
	}
	if err := validateActivityContext(decoded.Context, false, profile); err != nil {
		return err
	}
	*result = decoded
	return nil
}

func decodeActivityEnvelope(raw []byte, profile federationActivityProfile) (activityJSON, error) {
	var result activityJSON
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return result, err
	}
	sequence := fields[profile.sequenceField()]
	delete(fields, profile.sequenceField())
	normalized, err := json.Marshal(fields)
	if err != nil {
		return result, err
	}
	if err := decodeStrictObject(normalized, &result); err != nil {
		return activityJSON{}, err
	}
	result.Sequence = sequence
	return result, nil
}

func decodeActivityObject(raw json.RawMessage, profile federationActivityProfile) (activityObjectJSON, error) {
	var object activityObjectJSON
	if len(raw) == 0 || len(raw) > maxFederationEventBytes {
		return object, errors.New("invalid activity object")
	}
	if err := validateUniqueBoundedJSON(raw, profile); err != nil {
		return object, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return object, err
	}
	sourceRaw, hasSource := fields[profile.sourceField()]
	adaptedFromRaw, hasAdaptedFrom := fields[profile.adaptedFromField()]
	delete(fields, profile.sourceField())
	delete(fields, profile.adaptedFromField())
	normalized, err := json.Marshal(fields)
	if err != nil || decodeStrictObject(normalized, &object) != nil {
		return object, errors.New("invalid activity object shape")
	}
	if hasSource && json.Unmarshal(sourceRaw, &object.Source) != nil {
		return object, errors.New("invalid Cookbook source extension")
	}
	if hasAdaptedFrom && json.Unmarshal(adaptedFromRaw, &object.AdaptedFrom) != nil {
		return object, errors.New("invalid Cookbook adaptation extension")
	}
	return object, nil
}

func decodeStrictObject(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func parseActivitySequence(raw json.RawMessage) (int64, bool, error) {
	if len(raw) == 0 {
		return 0, false, nil
	}
	var sequence int64
	if err := json.Unmarshal(raw, &sequence); err != nil || sequence < 1 {
		return 0, false, errors.New("invalid Cookbook sequence extension")
	}
	return sequence, true, nil
}

func validateActivityObject(object activityObjectJSON, authenticatedOrigin, actorID string, extensionsAllowed bool, profile federationActivityProfile) error {
	if !isSupportedCookbookObjectType(object.Type, profile) {
		return errors.New("unsupported Cookbook object type")
	}
	if !extensionsAllowed {
		return errors.New("Cookbook object type requires the configured inline context")
	}
	if err := validateActivityObjectURI(object.ID, authenticatedOrigin); err != nil {
		return errors.New("object ID does not match authenticated origin")
	}
	if object.AttributedTo != actorID {
		return errors.New("object attribution does not match activity actor")
	}
	if strings.TrimSpace(object.Name) == "" || len(object.Name) > activityMaxNameBytes || len(object.Summary) > activityMaxSummaryBytes {
		return errors.New("invalid Cookbook object text fields")
	}
	for _, relation := range []string{object.Source, object.AdaptedFrom} {
		if relation != "" {
			if !extensionsAllowed {
				return errors.New("Cookbook provenance requires the fixed inline context")
			}
			if _, _, err := parseActivityURI(relation); err != nil {
				return errors.New("invalid object provenance URI")
			}
		}
	}
	return nil
}

func isSupportedCookbookObjectType(objectType string, profile federationActivityProfile) bool {
	if !strings.HasPrefix(objectType, profile.extensionPrefix+":") {
		return false
	}
	switch strings.TrimPrefix(objectType, profile.extensionPrefix+":") {
	case "Recipe", "Cookbook", "Course", "Menu":
		return true
	default:
		return false
	}
}

func validateActivityActorURI(value string) error {
	_, path, err := parseActivityURI(value)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/actors/"), "/")
	if !strings.HasPrefix(path, "/actors/") || len(parts) != 1 || !validOpaqueActivityID(parts[0]) {
		return errors.New("actor URI must be /actors/<opaque-id>")
	}
	return nil
}

func validateActivityDocumentURI(value, prefix string) error {
	_, path, err := parseActivityURI(value)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(path, prefix) {
		return errors.New("activity document URI has the wrong path")
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 1 || !validOpaqueActivityID(parts[0]) {
		return errors.New("activity document URI must have one opaque identifier")
	}
	return nil
}

func validateActivityObjectURI(value, authenticatedOrigin string) error {
	origin, path, err := parseActivityURI(value)
	if err != nil || origin != authenticatedOrigin || path == "/" || strings.HasPrefix(path, "/actors/") {
		return errors.New("invalid or foreign object URI")
	}
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("invalid object URI path")
		}
	}
	return nil
}

func parseActivityURI(value string) (origin, path string, err error) {
	if len(value) == 0 || len(value) > activityMaxURIBytes {
		return "", "", errors.New("URI is empty or too long")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.RawPath != "" || strings.ContainsAny(value, "%\\\t\r\n ") {
		return "", "", errors.New("URI must be an absolute HTTPS URI without credentials/query/fragment")
	}
	origin = parsed.Scheme + "://" + parsed.Host
	canonical, err := canonicalFederationOrigin(origin)
	if err != nil || canonical != origin {
		return "", "", errors.New("URI origin is not canonical")
	}
	if parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", "", errors.New("URI path is required")
	}
	return origin, parsed.Path, nil
}

func validOpaqueActivityID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func activityContextForProfile(profile federationActivityProfile) (json.RawMessage, error) {
	if err := validateFederationActivityProfile(profile); err != nil {
		return nil, err
	}
	context, err := json.Marshal([]any{activityStreamsContext, map[string]string{profile.extensionPrefix: profile.extensionNamespace}})
	return context, err
}

func activityContextHasExtensions(raw json.RawMessage, profile federationActivityProfile) bool {
	var contextParts []json.RawMessage
	if json.Unmarshal(raw, &contextParts) != nil || len(contextParts) != 2 {
		return false
	}
	var extension map[string]string
	return json.Unmarshal(contextParts[1], &extension) == nil && len(extension) == 1 && extension[profile.extensionPrefix] == profile.extensionNamespace
}
