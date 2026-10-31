package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/common-fate/httpsig"
)

type federationInboxHandler struct {
	db             *sql.DB
	origin         string
	enabled        bool
	trustedProxies []*net.IPNet
}

func (h federationInboxHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.enabled || h.origin == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	if r.ContentLength < 0 {
		writeError(w, http.StatusLengthRequired, "content_length_required")
		return
	}
	federationSignatureMiddleware(h.db, h.origin, h.trustedProxies, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerOrigin, ok := httpsig.AttributesFromContext(r.Context()).(string)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxFederationEventBytes+1))
		if err != nil || int64(len(body)) > maxFederationEventBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "event_too_large")
			return
		}
		status, response, err := acceptFederationEvent(r.Context(), h.db, peerOrigin, body)
		if err != nil {
			if errors.Is(err, errFederationConflict) {
				writeError(w, http.StatusConflict, "federation_conflict")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_federation_event")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(response)
	})).ServeHTTP(w, r)
}

func federationEnabled(db *sql.DB) bool {
	var enabled int
	if err := db.QueryRow(`SELECT federation_enabled FROM instance_settings WHERE id=1`).Scan(&enabled); err != nil {
		return false
	}
	return enabled == 1
}

const (
	federationEventSnapshot  = "recipe.snapshot"
	federationEventUpdate    = "recipe.update"
	federationEventTombstone = "recipe.tombstone"
	maxFederationEventBytes  = 2 << 20
)

var errFederationConflict = errors.New("federation event conflicts with accepted state")

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
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
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
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
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

func acceptFederationEvent(ctx context.Context, db *sql.DB, keyOrigin string, body []byte) (int, []byte, error) {
	event, recipeJSON, err := decodeFederationEvent(body)
	if err != nil || event.SourceOrigin != keyOrigin {
		return 0, nil, errors.New("invalid or mismatched federation event")
	}
	digest := sha256.Sum256(body)
	response, err := json.Marshal(map[string]any{"status": "accepted", "event_id": event.EventID, "object_id": event.ObjectID, "sequence": event.Sequence})
	if err != nil {
		return 0, nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var previousDigest []byte
	var previousStatus int
	var previousResponse string
	err = tx.QueryRowContext(ctx, `SELECT body_digest,response_status,response_body FROM federation_received_events WHERE source_origin=? AND event_id=?`, keyOrigin, event.EventID).Scan(&previousDigest, &previousStatus, &previousResponse)
	if err == nil {
		if !bytes.Equal(previousDigest, digest[:]) {
			return 0, nil, errFederationConflict
		}
		if err := tx.Commit(); err != nil {
			return 0, nil, err
		}
		return previousStatus, []byte(previousResponse), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	var peerEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM federation_peers WHERE origin=?`, keyOrigin).Scan(&peerEnabled); err != nil || peerEnabled != 1 {
		return 0, nil, errors.New("federation peer is disabled")
	}
	var latestSequence int64
	var deleted int
	err = tx.QueryRowContext(ctx, `SELECT sequence,deleted FROM federation_remote_recipes WHERE object_id=? AND source_origin=?`, event.ObjectID, keyOrigin).Scan(&latestSequence, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		if event.EventType != federationEventSnapshot {
			return 0, nil, errFederationConflict
		}
		latestSequence = 0
	} else if err != nil {
		return 0, nil, err
	}
	if event.Sequence <= latestSequence || (event.EventType == federationEventUpdate && deleted == 1) {
		return 0, nil, errFederationConflict
	}
	if event.EventType == federationEventTombstone {
		_, err = tx.ExecContext(ctx, `UPDATE federation_remote_recipes SET sequence=?,creator_name='',recipe_json='',deleted=1,updated_at=? WHERE object_id=? AND source_origin=?`, event.Sequence, timestamp(), event.ObjectID, keyOrigin)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?) ON CONFLICT(object_id) DO UPDATE SET sequence=excluded.sequence,creator_name=excluded.creator_name,recipe_json=excluded.recipe_json,deleted=0,updated_at=excluded.updated_at`, event.ObjectID, keyOrigin, strings.Split(event.ObjectID, "/federation/recipes/")[1], event.Sequence, event.Attribution.CreatorName, string(recipeJSON), timestamp())
	}
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body) VALUES(?,?,?,?,?,?,?,?)`, keyOrigin, event.EventID, digest[:], event.ObjectID, event.Sequence, timestamp(), http.StatusAccepted, string(response))
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, response, nil
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
