package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	federationActorDocumentLimit = federationResponseLimit
	federationActorKeyIDFragment = "#main-key"
)

// federationActorProvenance records the public actor document and publication
// endpoints associated with a discovered key. It does not establish a human
// identity or authorize access to any resource.
type federationActorProvenance struct {
	ProfileVersion string
	ActorID        string
	ActorType      string
	ActorOrigin    string
	Inbox          string
	Outbox         string
	KeyID          string
	KeyOwner       string
}

type resolvedFederationActor struct {
	Provenance federationActorProvenance
	PublicKey  ed25519.PublicKey
}

type federationActorDocumentJSON struct {
	Context   json.RawMessage              `json:"@context"`
	ID        string                       `json:"id"`
	Type      string                       `json:"type"`
	Inbox     string                       `json:"inbox"`
	Outbox    string                       `json:"outbox"`
	PublicKey federationActorPublicKeyJSON `json:"publicKey"`
}

type federationActorPublicKeyJSON struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

// resolveFederationActor fetches only the canonical actor URL using the
// existing public-DNS-pinned, TLS-verifying, no-proxy, no-redirect client.
func resolveFederationActor(ctx context.Context, actorID string, profile federationActivityProfile) (resolvedFederationActor, error) {
	return resolveFederationActorWithClient(ctx, buildFederationDeliveryClient(nil), actorID, profile)
}

// resolveFederationActorWithClient is an internal fixture seam. Production
// callers must use resolveFederationActor so fetches retain the hardened
// federation transport policy.
func resolveFederationActorWithClient(ctx context.Context, client *http.Client, actorID string, profile federationActivityProfile) (resolvedFederationActor, error) {
	var zero resolvedFederationActor
	if err := validateFederationActorURL(actorID); err != nil {
		return zero, err
	}
	if err := validateFederationActivityProfile(profile); err != nil {
		return zero, err
	}
	if client == nil {
		client = buildFederationDeliveryClient(nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actorID, nil)
	if err != nil {
		return zero, errors.New("invalid actor discovery request")
	}
	req.Header.Set("Accept", "application/activity+json, application/ld+json")
	response, err := client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("fetch federation actor: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("actor document returned status %d", response.StatusCode)
	}
	if !isFederationActorContentType(response.Header.Get("Content-Type")) {
		return zero, errors.New("actor document has an unsupported content type")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, federationActorDocumentLimit+1))
	if err != nil {
		return zero, errors.New("could not read actor document")
	}
	if len(body) > federationActorDocumentLimit {
		return zero, errors.New("actor document exceeded the size limit")
	}
	return decodeFederationActorDocument(body, actorID, profile)
}

func isFederationActorContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && (strings.EqualFold(mediaType, "application/activity+json") || strings.EqualFold(mediaType, "application/ld+json"))
}

func decodeFederationActorDocument(body []byte, requestedActorID string, profile federationActivityProfile) (resolvedFederationActor, error) {
	var zero resolvedFederationActor
	if len(body) == 0 || len(body) > federationActorDocumentLimit || !utf8.Valid(body) {
		return zero, errors.New("invalid actor document body")
	}
	if err := validateFederationActorURL(requestedActorID); err != nil {
		return zero, err
	}
	if err := validateFederationActivityProfile(profile); err != nil {
		return zero, err
	}
	if err := validateUniqueFederationActorJSON(body); err != nil {
		return zero, err
	}
	var document federationActorDocumentJSON
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || ensureJSONEOF(decoder) != nil {
		return zero, errors.New("invalid actor document shape")
	}
	if err := validateActivityContext(document.Context, true, profile); err != nil {
		return zero, err
	}
	if document.ID != requestedActorID {
		return zero, errors.New("actor document ID does not match requested actor")
	}
	if document.Type != "Person" && document.Type != "Service" {
		return zero, errors.New("unsupported actor type")
	}
	if err := validateFederationActorURL(document.ID); err != nil {
		return zero, err
	}
	actorOrigin, _, err := parseActivityURI(document.ID)
	if err != nil {
		return zero, errors.New("invalid actor origin")
	}
	inboxOrigin, _, inboxErr := parseActivityURI(document.Inbox)
	outboxOrigin, _, outboxErr := parseActivityURI(document.Outbox)
	if inboxErr != nil || inboxOrigin != actorOrigin {
		return zero, errors.New("actor inbox must use the actor origin")
	}
	if outboxErr != nil || outboxOrigin != actorOrigin {
		return zero, errors.New("actor outbox must use the actor origin")
	}
	key := document.PublicKey
	if key.ID != document.ID+federationActorKeyIDFragment || key.Owner != document.ID {
		return zero, errors.New("actor public key ID and owner must identify the actor")
	}
	if len(key.PublicKeyPEM) == 0 || len(key.PublicKeyPEM) > 2048 {
		return zero, errors.New("invalid actor public key encoding")
	}
	block, rest := pem.Decode([]byte(key.PublicKeyPEM))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return zero, errors.New("invalid actor Ed25519 public key PEM")
	}
	parsedKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return zero, errors.New("invalid actor Ed25519 public key")
	}
	publicKey, ok := parsedKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return zero, errors.New("actor public key must use Ed25519")
	}
	return resolvedFederationActor{
		Provenance: federationActorProvenance{
			ProfileVersion: profile.version,
			ActorID:        document.ID,
			ActorType:      document.Type,
			ActorOrigin:    actorOrigin,
			Inbox:          document.Inbox,
			Outbox:         document.Outbox,
			KeyID:          key.ID,
			KeyOwner:       key.Owner,
		},
		PublicKey: append(ed25519.PublicKey(nil), publicKey...),
	}, nil
}

func validateFederationActorURL(actorID string) error {
	if err := validateActivityActorURI(actorID); err != nil {
		return errors.New("actor ID must be a canonical public HTTPS actor URL")
	}
	return nil
}

type federationActorJSONState struct{ tokens int }

func validateUniqueFederationActorJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	state := federationActorJSONState{}
	if err := consumeUniqueFederationActorJSON(decoder, &state, 0); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing actor document JSON")
	}
	return nil
}

func consumeUniqueFederationActorJSON(decoder *json.Decoder, state *federationActorJSONState, depth int) error {
	state.tokens++
	if state.tokens > activityMaxJSONTokens || depth > activityMaxJSONDepth {
		return errors.New("actor document JSON exceeds structural limits")
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
				return errors.New("actor document JSON exceeds structural limits")
			}
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid actor document JSON key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate actor document JSON key")
			}
			seen[key] = struct{}{}
			if err := consumeUniqueFederationActorJSON(decoder, state, depth+1); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeUniqueFederationActorJSON(decoder, state, depth+1); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid actor document JSON delimiter")
	}
}
