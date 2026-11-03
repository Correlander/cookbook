package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFederationDeliveryAuthenticatesAndAppliesRemoteSnapshot(t *testing.T) {
	senderKey, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receiverDB := testDatabase(t)
	if err := registerFederationPeer(receiverDB, "https://sender.example.org", true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(receiverDB, "https://receiver.example.org", true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(receiverDB, "https://sender.example.org", senderKey.ID, senderKey.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := receiverDB.Exec(`UPDATE instance_settings SET federation_enabled=1,federation_origin='https://receiver.example.org' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationEvent("https://sender.example.org", testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Source cook", Title: "Snapshot", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	handler := federationInboxHandler{db: receiverDB, origin: "https://receiver.example.org"}
	client := &http.Client{Transport: federationHandlerTransport{handler: handler}}
	if err := deliverFederationEvent(context.Background(), client, senderKey, senderKey.ID, "https://receiver.example.org", payload); err != nil {
		t.Fatal(err)
	}
	var recipe string
	if err := receiverDB.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id=?`, "https://sender.example.org/federation/recipes/"+testFederationID).Scan(&recipe); err != nil || recipe == "" {
		t.Fatalf("remote snapshot missing: recipe=%q err=%v", recipe, err)
	}
	if _, err := receiverDB.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	listing := httptest.NewRecorder()
	publicFederationRecipesHandler{db: receiverDB}.ServeHTTP(listing, httptest.NewRequest(http.MethodGet, "/api/public/remote-recipes", nil))
	if listing.Code != http.StatusOK || !strings.Contains(listing.Body.String(), "Snapshot") {
		t.Fatalf("public remote recipe listing status=%d body=%s", listing.Code, listing.Body)
	}
}

func TestTwoDatabaseFederationPublishUpdateDuplicateAndUnpublish(t *testing.T) {
	senderOrigin := "https://sender.example.org"
	receiverOrigin := "https://receiver.example.org"
	senderKey, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receiverDB := testDatabase(t)
	if err := registerFederationPeer(receiverDB, senderOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(receiverDB, receiverOrigin, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeerKey(receiverDB, senderOrigin, senderKey.ID, senderKey.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := receiverDB.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, receiverOrigin); err != nil {
		t.Fatal(err)
	}
	senderDB := testDatabase(t)
	if _, err := senderDB.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := senderDB.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at) VALUES(?, 'owner','Source cook','First title','[]','[]','[]','public','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := senderDB.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at)
		VALUES(?,'published','followers',0,'now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := senderDB.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, senderOrigin); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(senderDB, receiverOrigin, true); err != nil {
		t.Fatal(err)
	}
	handler := federationInboxHandler{db: receiverDB, origin: receiverOrigin}
	client := &http.Client{Transport: federationHandlerTransport{handler: handler}}
	queueAndDeliver := func() []byte {
		t.Helper()
		tx, err := senderDB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := enqueueFederationChange(tx, testFederationID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var payload []byte
		if err := senderDB.QueryRow(`SELECT payload FROM federation_outbox ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		processed, err := processNextFederationDelivery(context.Background(), senderDB, senderKey, client)
		if err != nil || !processed {
			t.Fatalf("delivery processed=%t err=%v", processed, err)
		}
		return payload
	}

	firstEvent := queueAndDeliver()
	firstObjectID := senderOrigin + "/federation/recipes/" + testFederationID
	var title string
	if err := receiverDB.QueryRow(`SELECT json_extract(recipe_json,'$.title') FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&title); err != nil || title != "First title" {
		t.Fatalf("first snapshot title=%q err=%v", title, err)
	}
	if err := deliverFederationEvent(context.Background(), client, senderKey, senderKey.ID, receiverOrigin, firstEvent); err != nil {
		t.Fatalf("duplicate delivery was not idempotent: %v", err)
	}
	if _, err := senderDB.Exec(`UPDATE recipes SET title='Updated title' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	if err := receiverDB.QueryRow(`SELECT json_extract(recipe_json,'$.title') FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&title); err != nil || title != "Updated title" {
		t.Fatalf("updated remote title=%q err=%v", title, err)
	}
	if _, err := senderDB.Exec(`UPDATE recipes SET visibility='private' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	var deleted int
	var recipeJSON string
	if err := receiverDB.QueryRow(`SELECT deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&deleted, &recipeJSON); err != nil || deleted != 1 || recipeJSON != "" {
		t.Fatalf("remote tombstone deleted=%d recipe=%q err=%v", deleted, recipeJSON, err)
	}
	listing := httptest.NewRecorder()
	publicFederationRecipesHandler{db: receiverDB}.ServeHTTP(listing, httptest.NewRequest(http.MethodGet, "/api/public/remote-recipes", nil))
	if listing.Code != http.StatusOK || listing.Body.String() != "[]\n" {
		t.Fatalf("unpublished remote recipe remained public: status=%d body=%s", listing.Code, listing.Body)
	}
	if _, err := senderDB.Exec(`UPDATE recipes SET visibility='public' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	if _, err := senderDB.Exec(`UPDATE recipes SET deleted_at='now' WHERE id=?`, testFederationID); err != nil {
		t.Fatal(err)
	}
	queueAndDeliver()
	if err := receiverDB.QueryRow(`SELECT deleted,recipe_json FROM federation_remote_recipes WHERE object_id=?`, firstObjectID).Scan(&deleted, &recipeJSON); err != nil || deleted != 1 || recipeJSON != "" {
		t.Fatalf("deleted remote recipe retained content: deleted=%d recipe=%q err=%v", deleted, recipeJSON, err)
	}
}

type federationHandlerTransport struct{ handler http.Handler }

func (t federationHandlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Host = request.URL.Host
	clone.RequestURI = request.URL.RequestURI()
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, clone)
	return recorder.Result(), nil
}

func TestFederationEgressRejectsNonPublicDNSAnswers(t *testing.T) {
	for name, addresses := range map[string][]netip.Addr{
		"public IPv4":    {netip.MustParseAddr("1.1.1.1")},
		"public IPv6":    {netip.MustParseAddr("2606:4700:4700::1111")},
		"private":        {netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("10.0.0.1")},
		"loopback":       {netip.MustParseAddr("127.0.0.1")},
		"link local":     {netip.MustParseAddr("169.254.169.254")},
		"shared address": {netip.MustParseAddr("100.64.0.1")},
		"IPv6 ULA":       {netip.MustParseAddr("fd00::1")},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateFederationAddresses(addresses)
			if name == "public IPv4" || name == "public IPv6" {
				if err != nil {
					t.Fatalf("rejected public address: %v", err)
				}
			} else if err == nil {
				t.Fatal("accepted non-public DNS answer")
			}
		})
	}
}

func TestFederationDialRevalidatesDNSOnEveryConnection(t *testing.T) {
	resolver := &rebindingFederationResolver{}
	dialed := []string{}
	dial := federationDialContext(resolver, func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, io.ErrClosedPipe
	})
	if _, err := dial(context.Background(), "tcp", "peer.example.org:443"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("first dial error=%v", err)
	}
	if _, err := dial(context.Background(), "tcp", "peer.example.org:443"); err == nil {
		t.Fatal("accepted a private address after DNS changed")
	}
	if resolver.calls != 2 || len(dialed) != 1 || dialed[0] != "1.1.1.1:443" {
		t.Fatalf("DNS calls=%d dialed=%v", resolver.calls, dialed)
	}
}

type rebindingFederationResolver struct{ calls int }

func (r *rebindingFederationResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	if r.calls == 1 {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestFederationDeliveryDoesNotFollowRedirects(t *testing.T) {
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	transport := &federationRedirectTransport{}
	client := buildFederationDeliveryClient(nil)
	client.Transport = transport
	if err := deliverFederationEvent(context.Background(), client, key, key.ID, "https://peer.example.org", payload); err == nil {
		t.Fatal("accepted a redirect response")
	}
	if transport.calls != 1 {
		t.Fatalf("redirect request count=%d, want 1", transport.calls)
	}
}

type federationRedirectTransport struct{ calls int }

func (t *federationRedirectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://other.example.org/inbox"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func insertOptedInFederationDeliverySource(t *testing.T, db *sql.DB, origin string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('delivery-owner','delivery@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at)
		VALUES(?, 'delivery-owner','Creator','Dish','[]','[]','[]','public','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at)
		VALUES(?,'published','followers',0,'now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, origin); err != nil {
		t.Fatal(err)
	}
}

func TestFederationWorkerRetriesAndDeletesAcknowledgedItems(t *testing.T) {
	db := testDatabase(t)
	insertOptedInFederationDeliverySource(t, db, testFederationOrigin)
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	var event federationEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at) VALUES(?,?,?,?,?,?,?,?)`, newID(), "https://peer.example.org", event.EventID, event.ObjectID, event.Sequence, payload, time.Now().Unix(), timestamp()); err != nil {
		t.Fatal(err)
	}
	failedClient := &http.Client{Transport: federationStatusTransport{status: http.StatusServiceUnavailable}}
	processed, err := processNextFederationDelivery(context.Background(), db, key, failedClient)
	if err != nil || !processed {
		t.Fatalf("failed attempt processed=%t err=%v", processed, err)
	}
	var attempts int
	var nextAttempt int64
	if err := db.QueryRow(`SELECT attempts,next_attempt FROM federation_outbox`).Scan(&attempts, &nextAttempt); err != nil || attempts != 1 || nextAttempt <= time.Now().Unix() {
		t.Fatalf("retry state attempts=%d next=%d err=%v", attempts, nextAttempt, err)
	}
	if _, err := db.Exec(`UPDATE federation_outbox SET next_attempt=?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	workingClient := &http.Client{Transport: federationStatusTransport{status: http.StatusAccepted}}
	processed, err = processNextFederationDelivery(context.Background(), db, key, workingClient)
	if err != nil || !processed {
		t.Fatalf("success processed=%t err=%v", processed, err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining queue=%d err=%v", remaining, err)
	}
}

func TestFederationOutboxSurvivesDatabaseRestartAndExpiredLease(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "federation.db")
	db, err := openDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	insertOptedInFederationDeliverySource(t, db, testFederationOrigin)
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := serializeFederationEvent(testFederationOrigin, testFederationID, federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: testFederationID, CreatorName: "Creator", Title: "Dish", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	var event federationEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,lease_until,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, newID(), "https://peer.example.org", event.EventID, event.ObjectID, event.Sequence, payload, time.Now().Unix(), time.Now().Add(-time.Minute).Unix(), timestamp()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	processed, err := processNextFederationDelivery(context.Background(), db, key, &http.Client{Transport: federationStatusTransport{status: http.StatusAccepted}})
	if err != nil || !processed {
		t.Fatalf("recovered delivery processed=%t err=%v", processed, err)
	}
	var queued int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("queue after recovery=%d err=%v", queued, err)
	}
}

type federationStatusTransport struct{ status int }

func (t federationStatusTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Body: io.NopCloser(strings.NewReader(`{"status":"accepted"}`)), Header: make(http.Header)}, nil
}
