package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/common-fate/httpsig/alg_ed25519"
	"github.com/common-fate/httpsig/contentdigest"
	"github.com/common-fate/httpsig/signature"
	"github.com/common-fate/httpsig/signer"
	"github.com/common-fate/httpsig/sigset"
	"github.com/dunglas/httpsfv"
)

type federationTestSigner struct{ alg_ed25519.Ed25519 }

func (f federationTestSigner) Type() string { return "" }

func (f federationTestSigner) ContentDigest() contentdigest.Digester { return contentdigest.SHA256 }

func TestFederationSignatureMiddleware(t *testing.T) {
	db := testDatabase(t)
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, "https://peer.example.org", "peer-key-1", publicKey); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		keyID      string
		body       string
		mutate     func(*http.Request)
		wantStatus int
	}{
		{name: "valid", keyID: "peer-key-1", body: `{"event":"x"}`, wantStatus: http.StatusNoContent},
		{name: "unknown key", keyID: "missing", body: `{"event":"x"}`, wantStatus: http.StatusUnauthorized},
		{name: "tampered body", keyID: "peer-key-1", body: `{"event":"x"}`, mutate: func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"event":"y"}`)) }, wantStatus: http.StatusUnauthorized},
		{name: "missing content length", keyID: "peer-key-1", body: `{"event":"x"}`, mutate: func(r *http.Request) { r.ContentLength = -1 }, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "oversized", keyID: "peer-key-1", body: strings.Repeat("x", maxFederationEventBytes+1), wantStatus: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			req := httptest.NewRequest(http.MethodPost, "https://local.example/api/federation/inbox", bytes.NewReader(body))
			req.Host = "local.example"
			req.Header.Set("Content-Type", "application/json")
			digest := sha256.Sum256(body)
			digestHeader := httpsfv.NewDictionary()
			digestHeader.Add("sha-256", httpsfv.NewItem(digest[:]))
			digestValue, err := httpsfv.Marshal(digestHeader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Digest", digestValue)
			roundTripper := signer.Transport{KeyID: tc.keyID, Tag: federationSignatureTag, Alg: federationTestSigner{alg_ed25519.Ed25519{PrivateKey: privateKey}}, CoveredComponents: []string{"@method", "@target-uri", "content-type", "content-length", "content-digest"}, GetNonce: func() (string, error) { return strings.Repeat("n", 40), nil }}
			message, err := roundTripper.Sign(req)
			if err != nil {
				t.Fatal(err)
			}
			set := sigset.Set{Messages: make(map[string]*signature.Message)}
			set.Add(message)
			if err := set.Include(req); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(req)
			}
			handler := federationSignatureMiddleware(db, "https://local.example", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
		})
	}

	request := signedFederationRequest(t, privateKey, "peer-key-1", strings.Repeat("replay", 8), `{"event":"once"}`)
	handler := federationSignatureMiddleware(db, "https://local.example", nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for attempt, want := range []int{http.StatusNoContent, http.StatusUnauthorized} {
		replay := request.Clone(request.Context())
		replay.Body = io.NopCloser(strings.NewReader(`{"event":"once"}`))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, replay)
		if recorder.Code != want {
			t.Fatalf("attempt %d status=%d want=%d", attempt+1, recorder.Code, want)
		}
	}

	if err := revokeFederationPeerKey(db, "https://peer.example.org", "peer-key-1"); err != nil {
		t.Fatal(err)
	}
	request = signedFederationRequest(t, privateKey, "peer-key-1", strings.Repeat("revoked", 6), `{"event":"after-revoke"}`)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key status=%d want=%d", recorder.Code, http.StatusUnauthorized)
	}
}

func signedFederationRequest(t *testing.T, privateKey ed25519.PrivateKey, keyID, nonce, body string) *http.Request {
	t.Helper()
	bodyBytes := []byte(body)
	req := httptest.NewRequest(http.MethodPost, "https://local.example/api/federation/inbox", bytes.NewReader(bodyBytes))
	req.Host = "local.example"
	req.Header.Set("Content-Type", "application/json")
	digest := sha256.Sum256(bodyBytes)
	digestHeader := httpsfv.NewDictionary()
	digestHeader.Add("sha-256", httpsfv.NewItem(digest[:]))
	digestValue, err := httpsfv.Marshal(digestHeader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Digest", digestValue)
	transport := signer.Transport{
		KeyID: keyID, Tag: federationSignatureTag,
		Alg:               federationTestSigner{alg_ed25519.Ed25519{PrivateKey: privateKey}},
		CoveredComponents: []string{"@method", "@target-uri", "content-type", "content-length", "content-digest"},
		GetNonce:          func() (string, error) { return nonce, nil },
	}
	message, err := transport.Sign(req)
	if err != nil {
		t.Fatal(err)
	}
	set := sigset.Set{Messages: make(map[string]*signature.Message)}
	set.Add(message)
	if err := set.Include(req); err != nil {
		t.Fatal(err)
	}
	return req
}
