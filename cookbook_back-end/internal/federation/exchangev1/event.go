package exchangev1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const MaxEventBytes = 2 << 20

const (
	EventSnapshot  = "recipe.snapshot"
	EventUpdate    = "recipe.update"
	EventTombstone = "recipe.tombstone"
)

type Event struct {
	Protocol      string       `json:"protocol"`
	SchemaVersion int          `json:"schema_version"`
	EventID       string       `json:"event_id"`
	EventType     string       `json:"event_type"`
	SourceOrigin  string       `json:"source_origin"`
	ObjectID      string       `json:"object_id"`
	Sequence      int64        `json:"sequence"`
	OccurredAt    string       `json:"occurred_at"`
	Attribution   *Attribution `json:"attribution,omitempty"`
	Recipe        *Recipe      `json:"recipe,omitempty"`
}

type Attribution struct {
	CreatorName string `json:"creator_name"`
}

type Recipe struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Ingredients []Ingredient `json:"ingredients"`
	Steps       []Step       `json:"steps"`
	Servings    int          `json:"servings"`
	PrepTime    int          `json:"prep_time_minutes"`
	CookTime    int          `json:"cook_time_minutes"`
	Tags        []string     `json:"tags"`
}

type Ingredient struct {
	Name     string `json:"name"`
	Quantity string `json:"quantity,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

type Step struct {
	Text string `json:"text"`
}

// Decode enforces only the Exchange v1 wire syntax. Domain and event policy
// validation belongs to the calling application layer.
func Decode(body []byte) (Event, error) {
	var event Event
	if len(body) == 0 || len(body) > MaxEventBytes || !utf8.Valid(body) {
		return event, errors.New("invalid event body")
	}
	if err := rejectDuplicateOrUnknownKeys(body); err != nil {
		return event, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return event, err
	}
	if err := ensureEOF(decoder); err != nil {
		return event, err
	}
	return event, nil
}

func Encode(event Event) ([]byte, error) {
	body, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxEventBytes {
		return nil, errors.New("federation event is too large")
	}
	return body, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func rejectDuplicateOrUnknownKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := consumeUniqueValue(decoder, "event"); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func consumeUniqueValue(decoder *json.Decoder, kind string) error {
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
		seen := map[string]struct{}{}
		allowed := allowedKeys(kind)
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
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeUniqueValue(decoder, childKind(kind, key)); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeUniqueValue(decoder, kind); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func allowedKeys(kind string) map[string]bool {
	var keys []string
	switch kind {
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

func childKind(parent, key string) string {
	if parent == "event" && (key == "attribution" || key == "recipe") {
		return key
	}
	if parent == "recipe" && key == "ingredients" {
		return "ingredient"
	}
	if parent == "recipe" && key == "steps" {
		return "step"
	}
	return ""
}
