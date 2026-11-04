package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
	"github.com/common-fate/httpsig/alg_ed25519"
	"github.com/common-fate/httpsig/contentdigest"
	"github.com/common-fate/httpsig/sigbase"
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
		{name: "tampered body", keyID: "peer-key-1", body: `{"event":"x"}`, mutate: func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"event":"y"}`)) }, wantStatus: http.StatusBadRequest},
		{name: "mismatched digest", keyID: "peer-key-1", body: `{"event":"x"}`, mutate: func(r *http.Request) {
			r.Header.Set("Content-Digest", "sha-256=:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=:")
		}, wantStatus: http.StatusBadRequest},
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
			roundTripper := signer.Transport{KeyID: tc.keyID, Tag: federationSignatureTag, Alg: federationTestSigner{alg_ed25519.Ed25519{PrivateKey: privateKey}}, CoveredComponents: []string{"@method", "@target-uri", "content-type", "content-length", "content-digest"}, GetNonce: func() (string, error) { return strings.Repeat("n", 40) + tc.name, nil }}
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
	if err := registerFederationPeerKey(db, "https://peer.example.org", "peer-key-1", publicKey); err == nil {
		t.Fatal("reactivated revoked federation key ID")
	}
	request = signedFederationRequest(t, privateKey, "peer-key-1", strings.Repeat("revoked", 6), `{"event":"after-revoke"}`)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key status=%d want=%d", recorder.Code, http.StatusUnauthorized)
	}
	if err := registerFederationPeerKey(db, "https://peer.example.org", "peer-key-2", publicKey); err != nil {
		t.Fatal(err)
	}
	staleRequest := signedFederationRequest(t, privateKey, "peer-key-2", strings.Repeat("stale", 8), `{"event":"stale"}`)
	signatureSet, err := sigset.Unmarshal(staleRequest)
	if err != nil {
		t.Fatal(err)
	}
	message, err := signatureSet.Find(federationSignatureTag)
	if err != nil {
		t.Fatal(err)
	}
	message.Input.Created = time.Now().Add(-6 * time.Minute)
	base, err := sigbase.Derive(message.Input, nil, staleRequest, contentdigest.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	signingString, err := base.CanonicalString(message.Input)
	if err != nil {
		t.Fatal(err)
	}
	message.Signature = ed25519.Sign(privateKey, []byte(signingString))
	if err := signatureSet.Include(staleRequest); err != nil {
		t.Fatal(err)
	}
	handler = federationSignatureMiddleware(db, "https://local.example", nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusUnauthorized {
		t.Fatalf("stale signature status=%d body=%s", staleResponse.Code, staleResponse.Body)
	}
}

func TestFederationNonceStorageEnforcesPeerAndGlobalCapsWithDuplicateFirst(t *testing.T) {
	db := testDatabase(t)
	peerA, peerB := "https://peer-a.example.org", "https://peer-b.example.org"
	for _, origin := range []string{peerA, peerB} {
		if err := registerFederationPeer(db, origin, true); err != nil {
			t.Fatal(err)
		}
	}
	global := defaultFederationGlobalResourceLimits()
	global.NonceRows = 2
	global.NonceBytes = 2 * federationNonceLogicalBytes(peerA)
	defaults := defaultFederationResourceLimits()
	defaults.NonceRows = 1
	defaults.NonceBytes = federationNonceLogicalBytes(peerA)
	if err := setFederationResourceLimits(t.Context(), db, global, defaults, nil); err != nil {
		t.Fatal(err)
	}
	storage := federationNonceStorage{db: db}
	ctx := func(origin string) context.Context {
		return context.WithValue(context.Background(), federationNonceAttemptKey{}, &federationNonceAttempt{sourceOrigin: origin})
	}
	nonceA := strings.Repeat("a", 40)
	if seen, err := storage.Seen(ctx(peerA), nonceA); err != nil || seen {
		t.Fatalf("first peer nonce seen=%v err=%v", seen, err)
	}
	if seen, err := storage.Seen(ctx(peerA), nonceA); err != nil || !seen {
		t.Fatalf("known nonce at cap seen=%v err=%v", seen, err)
	}
	if _, err := storage.Seen(ctx(peerA), strings.Repeat("b", 40)); !errors.Is(err, errFederationNonceCapacity) {
		t.Fatalf("per-peer capacity error=%v", err)
	}
	if seen, err := storage.Seen(ctx(peerB), strings.Repeat("c", 40)); err != nil || seen {
		t.Fatalf("other peer nonce seen=%v err=%v", seen, err)
	}
	if _, err := storage.Seen(ctx(peerB), strings.Repeat("d", 40)); !errors.Is(err, errFederationNonceCapacity) {
		t.Fatalf("global capacity error=%v", err)
	}
	usageA, err := readFederationResourceUsage(t.Context(), db, peerA, false)
	if err != nil {
		t.Fatal(err)
	}
	usageB, err := readFederationResourceUsage(t.Context(), db, peerB, false)
	if err != nil {
		t.Fatal(err)
	}
	globalUsage, err := readFederationResourceUsage(t.Context(), db, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if usageA.NonceRows != 1 || usageB.NonceRows != 1 || globalUsage.NonceRows != 2 || globalUsage.NonceBytes != global.NonceBytes {
		t.Fatalf("unexpected nonce accounting: peerA=%+v peerB=%+v global=%+v", usageA, usageB, globalUsage)
	}
}

func TestFederationNonceDatabaseFailureIsDistinctFromCapacity(t *testing.T) {
	db := testDatabase(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := (federationNonceStorage{db: db}).Seen(context.Background(), strings.Repeat("z", 40))
	if !errors.Is(err, errFederationNonceUnavailable) || errors.Is(err, errFederationNonceCapacity) {
		t.Fatalf("database error classification=%v", err)
	}
}

func TestFederationSignatureMiddlewareReleasesInvalidNonceAndEnforcesCapacity(t *testing.T) {
	db := testDatabase(t)
	origin := "https://peer.example.org"
	if err := registerFederationPeer(db, origin, true); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, origin, "peer-key-1", publicKey); err != nil {
		t.Fatal(err)
	}
	peerLimits := defaultFederationResourceLimits()
	peerLimits.NonceRows = 1
	peerLimits.NonceBytes = federationNonceLogicalBytes(origin)
	if err := setFederationResourceLimits(t.Context(), db, defaultFederationGlobalResourceLimits(), peerLimits, nil); err != nil {
		t.Fatal(err)
	}
	handler := federationSignatureMiddleware(db, "https://local.example", nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	invalid := signedFederationRequest(t, privateKey, "peer-key-1", strings.Repeat("i", 40), `{"event":"invalid"}`)
	set, err := sigset.Unmarshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	message, err := set.Find(federationSignatureTag)
	if err != nil {
		t.Fatal(err)
	}
	message.Signature = []byte("invalid signature")
	if err := set.Include(invalid); err != nil {
		t.Fatal(err)
	}
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status=%d body=%s", invalidResponse.Code, invalidResponse.Body)
	}
	var nonceRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_nonces`).Scan(&nonceRows); err != nil || nonceRows != 0 {
		t.Fatalf("invalid signature retained nonce rows=%d err=%v", nonceRows, err)
	}

	valid := signedFederationRequest(t, privateKey, "peer-key-1", strings.Repeat("v", 40), `{"event":"valid"}`)
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusNoContent {
		t.Fatalf("valid signature status=%d body=%s", validResponse.Code, validResponse.Body)
	}
	replay := signedFederationRequest(t, privateKey, "peer-key-1", strings.Repeat("v", 40), `{"event":"valid"}`)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("known nonce replay status=%d body=%s", replayResponse.Code, replayResponse.Body)
	}
	full := signedFederationRequest(t, privateKey, "peer-key-1", strings.Repeat("c", 40), `{"event":"capacity"}`)
	fullResponse := httptest.NewRecorder()
	handler.ServeHTTP(fullResponse, full)
	if fullResponse.Code != http.StatusServiceUnavailable || fullResponse.Header().Get("Retry-After") != "3600" || !strings.Contains(fullResponse.Body.String(), "federation_nonce_capacity") {
		t.Fatalf("capacity response status=%d retry-after=%q body=%s", fullResponse.Code, fullResponse.Header().Get("Retry-After"), fullResponse.Body)
	}
}

func signedFederationRequest(t *testing.T, privateKey ed25519.PrivateKey, keyID, nonce, body string) *http.Request {
	return signedFederationRequestWithTag(t, privateKey, keyID, nonce, body, federationSignatureTag)
}

func signedFederationRequestWithTag(t *testing.T, privateKey ed25519.PrivateKey, keyID, nonce, body, tag string) *http.Request {
	return signedFederationRequestWithTagAtOrigin(t, privateKey, keyID, nonce, body, tag, "https://local.example")
}

func signedFederationRequestWithTagAtOrigin(t *testing.T, privateKey ed25519.PrivateKey, keyID, nonce, body, tag, origin string) *http.Request {
	t.Helper()
	bodyBytes := []byte(body)
	req := httptest.NewRequest(http.MethodPost, origin+"/api/federation/inbox", bytes.NewReader(bodyBytes))
	req.Host = strings.TrimPrefix(origin, "https://")
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
		KeyID: keyID, Tag: tag,
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

func TestFederationV2AcceptanceRechecksProfileAndKeyAfterVerification(t *testing.T) {
	db := testDatabase(t)
	origin := "https://sender.example.org"
	if err := registerFederationPeer(db, origin, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, origin); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(db, origin, "sender-key", publicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin='https://local.example' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	object, err := federationRecipeObjectID(origin, testFederationID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationV2Event(origin, newID(), exchangev2.RecipeSnapshot, 1, time.Now(), federationRecipeSource{ID: testFederationID, CreatorName: "Sender", Title: "Soup", Visibility: visibilityPublic}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	event, err := exchangev2.Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	event.RetentionGrant = &exchangev2.RetentionGrant{Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention, Portability: exchangev2.GrantPortability, ObjectID: object, Sequence: 1, PayloadSHA256: exchangev2.PayloadDigest(event.Payload)}
	payload, err = exchangev2.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, race := range []string{"profile reset", "key revoked"} {
		t.Run(race, func(t *testing.T) {
			request := signedFederationRequestWithTagAtOrigin(t, privateKey, "sender-key", strings.Repeat("r", 40)+race, string(payload), federationSignatureV2Tag, "https://receiver.example.org")
			meta, err := captureFederationV2RequestMetadata(request, "https://receiver.example.org")
			if err != nil {
				t.Fatal(err)
			}
			request = request.WithContext(context.WithValue(request.Context(), federationV2RequestMetadataKey, meta))
			middleware := federationSignatureMiddlewareForTag(db, "https://receiver.example.org", nil, federationSignatureV2Tag, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				verified, err := verifyFederationRetentionEvidence(r, db, origin, body)
				if err != nil {
					t.Fatal(err)
				}
				if race == "profile reset" {
					_, err = db.Exec(`UPDATE federation_peers SET exchange_profile='v1' WHERE origin=?`, origin)
				} else {
					_, err = db.Exec(`UPDATE federation_peer_keys SET status='revoked' WHERE key_id='sender-key'`)
				}
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.WithValue(r.Context(), federationVerifiedRequestKey{}, verified)
				if _, _, err := acceptFederationEvent(ctx, db, origin, body); err == nil {
					t.Error("accepted request after authenticated state changed")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			middleware.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("signature verification status=%d body=%s", response.Code, response.Body)
			}
			var receipts, events, objects int
			_ = db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&receipts)
			_ = db.QueryRow(`SELECT COUNT(*) FROM federation_received_events`).Scan(&events)
			_ = db.QueryRow(`SELECT COUNT(*) FROM federation_remote_catalog`).Scan(&objects)
			if receipts != 0 || events != 0 || objects != 0 {
				t.Fatalf("recheck race mutated receiver state receipt=%d event=%d object=%d", receipts, events, objects)
			}
		})
		if race == "profile reset" {
			if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, origin); err != nil {
				t.Fatal(err)
			}
		} else {
			// Use a fresh key for the next independent race because key IDs are never reactivated.
			publicKey, privateKey, err = ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := registerFederationPeerKey(db, origin, "sender-key-2", publicKey); err != nil {
				t.Fatal(err)
			}
		}
	}
}
