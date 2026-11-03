package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const testDiscoveredActorID = "https://alice.example.org/actors/actor_alice_0000001"

func makeFederationActorDocument(t *testing.T, profile federationActivityProfile, actorID, actorType, inbox, outbox, keyID, keyOwner string, publicKey ed25519.PublicKey) []byte {
	t.Helper()
	contextValue, err := activityContextForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"@context": contextValue,
		"id":       actorID,
		"type":     actorType,
		"inbox":    inbox,
		"outbox":   outbox,
		"publicKey": map[string]string{
			"id":           keyID,
			"owner":        keyOwner,
			"publicKeyPem": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
		},
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDecodeFederationActorDocumentReturnsBoundProvenance(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := testDiscoveredActorID + federationActorKeyIDFragment
	body := makeFederationActorDocument(t, testActivityProfile, testDiscoveredActorID, "Person",
		"https://alice.example.org/inbox", "https://alice.example.org/outbox", keyID, testDiscoveredActorID, publicKey)
	resolved, err := decodeFederationActorDocument(body, testDiscoveredActorID, testActivityProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := federationActorProvenance{
		ProfileVersion: testActivityProfile.version,
		ActorID:        testDiscoveredActorID,
		ActorType:      "Person",
		ActorOrigin:    "https://alice.example.org",
		Inbox:          "https://alice.example.org/inbox",
		Outbox:         "https://alice.example.org/outbox",
		KeyID:          keyID,
		KeyOwner:       testDiscoveredActorID,
	}
	if !reflect.DeepEqual(resolved.Provenance, want) || !bytes.Equal(resolved.PublicKey, publicKey) {
		t.Fatalf("resolved actor = %#v, want provenance %#v and matching key", resolved, want)
	}
}

func TestDecodeFederationActorDocumentRejectsForeignAndMismatchedMetadata(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	base := func() []byte {
		return makeFederationActorDocument(t, testActivityProfile, testDiscoveredActorID, "Service",
			"https://alice.example.org/inbox", "https://alice.example.org/outbox",
			testDiscoveredActorID+federationActorKeyIDFragment, testDiscoveredActorID, publicKey)
	}
	tests := []struct {
		name       string
		requested  string
		mutateBody func([]byte) []byte
	}{
		{name: "actor ID from another origin", requested: "https://mallory.example.net/actors/actor_mallory_000001"},
		{name: "cross-origin inbox", requested: testDiscoveredActorID, mutateBody: func(body []byte) []byte {
			return bytes.Replace(body, []byte("https://alice.example.org/inbox"), []byte("https://mallory.example.net/inbox"), 1)
		}},
		{name: "cross-origin outbox", requested: testDiscoveredActorID, mutateBody: func(body []byte) []byte {
			return bytes.Replace(body, []byte("https://alice.example.org/outbox"), []byte("https://mallory.example.net/outbox"), 1)
		}},
		{name: "cross-origin key ID", requested: testDiscoveredActorID, mutateBody: func(body []byte) []byte {
			return bytes.Replace(body, []byte(testDiscoveredActorID+federationActorKeyIDFragment), []byte("https://mallory.example.net/actors/actor_mallory_000001#main-key"), 1)
		}},
		{name: "key owner mismatch", requested: testDiscoveredActorID, mutateBody: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"owner":"`+testDiscoveredActorID+`"`), []byte(`"owner":"https://mallory.example.net/actors/actor_mallory_000001"`), 1)
		}},
		{name: "unsupported actor type", requested: testDiscoveredActorID, mutateBody: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"type":"Service"`), []byte(`"type":"Group"`), 1)
		}},
		{name: "remote JSON-LD context", requested: testDiscoveredActorID, mutateBody: func(body []byte) []byte {
			return bytes.Replace(body, []byte(activityStreamsContext), []byte("https://remote.example/context"), 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := base()
			if test.mutateBody != nil {
				body = test.mutateBody(body)
			}
			if _, err := decodeFederationActorDocument(body, test.requested, testActivityProfile); err == nil {
				t.Fatal("foreign or mismatched actor metadata was accepted")
			}
		})
	}
}

func TestDecodeFederationActorDocumentRejectsMalformedAndUnboundedJSON(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	valid := makeFederationActorDocument(t, testActivityProfile, testDiscoveredActorID, "Person",
		"https://alice.example.org/inbox", "https://alice.example.org/outbox",
		testDiscoveredActorID+federationActorKeyIDFragment, testDiscoveredActorID, publicKey)
	deep := []byte(`{"@context":"` + activityStreamsContext + `","id":"` + testDiscoveredActorID + `","type":"Person","inbox":"https://alice.example.org/inbox","outbox":"https://alice.example.org/outbox","publicKey":` + strings.Repeat(`{"id":`, activityMaxJSONDepth+1) + `null` + strings.Repeat(`}`, activityMaxJSONDepth+1) + `}`)
	tooManyValues := []byte(`{"@context":[` + strings.Repeat(`{},`, activityMaxJSONTokens/2+1) + `null],"id":"` + testDiscoveredActorID + `","type":"Person","inbox":"https://alice.example.org/inbox","outbox":"https://alice.example.org/outbox","publicKey":{}}`)
	tests := map[string][]byte{
		"empty":                        nil,
		"malformed":                    []byte(`{"@context":`),
		"invalid UTF-8":                {0xff, '{', '}'},
		"oversized":                    bytes.Repeat([]byte(" "), federationActorDocumentLimit+1),
		"too deep":                     deep,
		"too many tokens":              tooManyValues,
		"duplicate actor field":        bytes.Replace(valid, []byte(`"type":"Person"`), []byte(`"type":"Person","type":"Service"`), 1),
		"duplicate nested key field":   bytes.Replace(valid, []byte(`"owner":"`+testDiscoveredActorID+`"`), []byte(`"owner":"`+testDiscoveredActorID+`","owner":"`+testDiscoveredActorID+`"`), 1),
		"duplicate inline context key": []byte(`{"@context":["` + activityStreamsContext + `",{"cookbook":"https://cookbook.invalid/ns#","cookbook":"https://cookbook.invalid/ns#"}],"id":"` + testDiscoveredActorID + `","type":"Person","inbox":"https://alice.example.org/inbox","outbox":"https://alice.example.org/outbox","publicKey":{}}`),
		"unapproved attachment field":  bytes.Replace(valid, []byte(`"type":"Person"`), []byte(`"type":"Person","attachment":"https://media.example/image"`), 1),
		"trailing JSON":                append(append([]byte(nil), valid...), []byte(` {}`)...),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeFederationActorDocument(body, testDiscoveredActorID, testActivityProfile); err == nil {
				t.Fatal("invalid actor document was accepted")
			}
		})
	}
}

func TestDecodeFederationActorDocumentRejectsNonEd25519Key(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := makeFederationActorDocument(t, testActivityProfile, testDiscoveredActorID, "Person",
		"https://alice.example.org/inbox", "https://alice.example.org/outbox",
		testDiscoveredActorID+federationActorKeyIDFragment, testDiscoveredActorID, privateKey.Public().(ed25519.PublicKey))
	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&ecdsaPrivate.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(valid, &document); err != nil {
		t.Fatal(err)
	}
	key := document["publicKey"].(map[string]any)
	key["publicKeyPem"] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeFederationActorDocument(body, testDiscoveredActorID, testActivityProfile); err == nil {
		t.Fatal("accepted an actor key outside the Ed25519 profile")
	}
}

func TestResolveFederationActorUsesExactUnauthenticatedGET(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	body := makeFederationActorDocument(t, testActivityProfile, testDiscoveredActorID, "Service",
		"https://alice.example.org/inbox", "https://alice.example.org/outbox",
		testDiscoveredActorID+federationActorKeyIDFragment, testDiscoveredActorID, publicKey)
	transport := &federationActorFixtureTransport{responseBody: body, contentType: "application/activity+json; charset=utf-8"}
	client := &http.Client{Transport: transport}
	resolved, err := resolveFederationActorWithClient(context.Background(), client, testDiscoveredActorID, testActivityProfile)
	if err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || transport.method != http.MethodGet || transport.url != testDiscoveredActorID || transport.auth != "" || transport.cookie != "" || transport.proxyAuth != "" || !strings.Contains(transport.accept, "application/activity+json") {
		t.Fatalf("actor fetch method=%q url=%q calls=%d accept=%q auth/cookie/proxy=%q/%q/%q", transport.method, transport.url, transport.calls, transport.accept, transport.auth, transport.cookie, transport.proxyAuth)
	}
	if resolved.Provenance.ActorID != testDiscoveredActorID {
		t.Fatalf("resolved actor provenance = %#v", resolved.Provenance)
	}
}

func TestResolveFederationActorBoundsAndChecksResponse(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		contentType string
		body        []byte
	}{
		{name: "oversized response", status: http.StatusOK, contentType: "application/activity+json", body: bytes.Repeat([]byte(" "), federationActorDocumentLimit+1)},
		{name: "unsupported content type", status: http.StatusOK, contentType: "text/html", body: []byte(`{}`)},
		{name: "not found", status: http.StatusNotFound, contentType: "application/activity+json", body: []byte(`{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &federationActorFixtureTransport{status: test.status, responseBody: test.body, contentType: test.contentType}
			client := &http.Client{Transport: transport}
			if _, err := resolveFederationActorWithClient(context.Background(), client, testDiscoveredActorID, testActivityProfile); err == nil {
				t.Fatal("invalid actor response was accepted")
			}
		})
	}
}

func TestResolveFederationActorRejectsUnsafeURLBeforeFetch(t *testing.T) {
	for _, actorID := range []string{
		"http://alice.example.org/actors/actor_alice_0000001",
		"https://127.0.0.1/actors/actor_alice_0000001",
		"https://alice.example.org/actors/actor_alice_0000001?redirect=https://evil.example",
		"https://alice.example.org/actors/../internal",
	} {
		t.Run(actorID, func(t *testing.T) {
			transport := &federationActorFixtureTransport{}
			client := &http.Client{Transport: transport}
			if _, err := resolveFederationActorWithClient(context.Background(), client, actorID, testActivityProfile); err == nil {
				t.Fatal("unsafe actor URL was accepted")
			}
			if transport.calls != 0 {
				t.Fatalf("unsafe actor URL caused %d network request(s)", transport.calls)
			}
		})
	}
}

type federationActorFixtureTransport struct {
	calls        int
	method       string
	url          string
	accept       string
	auth         string
	cookie       string
	proxyAuth    string
	status       int
	contentType  string
	responseBody []byte
}

func (t *federationActorFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	t.method = request.Method
	t.url = request.URL.String()
	t.accept = request.Header.Get("Accept")
	t.auth = request.Header.Get("Authorization")
	t.cookie = request.Header.Get("Cookie")
	t.proxyAuth = request.Header.Get("Proxy-Authorization")
	if request.Body != nil {
		return nil, errors.New("actor discovery request must not have a body")
	}
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	contentType := t.contentType
	if contentType == "" {
		contentType = "application/activity+json"
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewReader(t.responseBody)),
		Request:    request,
	}, nil
}
