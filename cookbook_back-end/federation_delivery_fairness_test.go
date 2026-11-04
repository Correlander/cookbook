package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFederationOutboxDeliveryFairnessAcrossOfflinePeer(t *testing.T) {
	db := testDatabase(t)
	localOrigin := testFederationOrigin
	insertOptedInFederationDeliverySource(t, db, localOrigin)
	offlinePeer := "https://a-offline.example.org"
	fastPeer := "https://z-fast.example.org"
	if err := registerFederationPeer(db, offlinePeer, true); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, fastPeer, true); err != nil {
		t.Fatal(err)
	}
	if err := enqueueFederationChangeTx(t, db, testFederationID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute).Unix()
	if _, err := db.Exec(`UPDATE federation_outbox SET next_attempt=?,created_at=CASE peer_origin WHEN ? THEN '2026-01-01T00:00:00Z' ELSE '2026-01-01T00:00:01Z' END`, past, offlinePeer); err != nil {
		t.Fatal(err)
	}

	observations := make(chan deliveryFairnessObservation, 4)
	fastServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		observations <- deliveryFairnessObservation{method: r.Method, path: r.URL.Path, host: r.Host, signatureInput: r.Header.Get("Signature-Input"), body: body, err: err}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(fastServer.Close)

	releaseOffline := make(chan struct{})
	startedOffline := make(chan struct{})
	var releaseOnce, startedOnce sync.Once
	forwarder := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(forwarder.CloseIdleConnections)
	transport := &deliveryFairnessTransport{
		offlineHost:   strings.TrimPrefix(offlinePeer, "https://"),
		fastHost:      strings.TrimPrefix(fastPeer, "https://"),
		fastServerURL: fastServer.URL,
		started:       startedOffline,
		release:       releaseOffline,
		onlineStart:   &startedOnce,
		forwarder:     forwarder,
	}
	client := &http.Client{Transport: transport}
	key, err := loadOrCreateFederationSigningKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	firstAttempt := make(chan deliveryFairnessResult, 1)
	go func() {
		processed, err := processNextFederationDelivery(ctx, db, key, client)
		firstAttempt <- deliveryFairnessResult{processed: processed, err: err}
	}()
	defer func() {
		releaseOnce.Do(func() { close(releaseOffline) })
		cancel()
	}()
	select {
	case <-startedOffline:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach the ordered offline peer")
	}

	mutationDone := make(chan error, 1)
	go func() {
		tx, err := db.Begin()
		if err != nil {
			mutationDone <- err
			return
		}
		if _, err := tx.Exec(`UPDATE recipes SET title='Responsive update',version=version+1,updated_at=? WHERE id=?`, timestamp(), testFederationID); err != nil {
			_ = tx.Rollback()
			mutationDone <- err
			return
		}
		if err := enqueueFederationChange(tx, testFederationID); err != nil {
			_ = tx.Rollback()
			mutationDone <- err
			return
		}
		mutationDone <- tx.Commit()
	}()
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatalf("local mutation while peer I/O was blocked: %v", err)
		}
	case <-time.After(federationDeliveryTimeout + 2*time.Second):
		releaseOnce.Do(func() { close(releaseOffline) })
		t.Fatal("local mutation did not finish while peer I/O was blocked (watchdog only; not a throughput target)")
	}

	releaseOnce.Do(func() { close(releaseOffline) })
	select {
	case result := <-firstAttempt:
		if !result.processed || result.err != nil {
			t.Fatalf("offline attempt processed=%t err=%v", result.processed, result.err)
		}
	case <-time.After(federationDeliveryTimeout + 2*time.Second):
		t.Fatal("offline attempt did not return after fixture release")
	}

	var attempts int
	var nextAttempt int64
	if err := db.QueryRow(`SELECT attempts,next_attempt FROM federation_outbox WHERE peer_origin=? ORDER BY sequence LIMIT 1`, offlinePeer).Scan(&attempts, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || nextAttempt <= time.Now().Unix() {
		t.Fatalf("offline peer retry state attempts=%d next_attempt=%d, want one future retry", attempts, nextAttempt)
	}

	for i := 0; i < 2; i++ {
		processed, err := processNextFederationDelivery(ctx, db, key, client)
		if err != nil || !processed {
			t.Fatalf("fast peer attempt %d processed=%t err=%v", i+1, processed, err)
		}
		select {
		case observation := <-observations:
			if observation.err != nil || observation.method != http.MethodPost || observation.path != "/api/federation/inbox" || observation.host != strings.TrimPrefix(fastPeer, "https://") || !strings.Contains(observation.signatureInput, `tag="`+federationSignatureTag+`"`) {
				t.Fatalf("invalid fast-peer request: method=%q path=%q host=%q signature=%q err=%v", observation.method, observation.path, observation.host, observation.signatureInput, observation.err)
			}
			var event federationEvent
			if err := json.Unmarshal(observation.body, &event); err != nil || event.SourceOrigin != localOrigin || event.Sequence != int64(i+1) {
				t.Fatalf("fast-peer event source=%q sequence=%d err=%v", event.SourceOrigin, event.Sequence, err)
			}
		default:
			t.Fatal("fast-peer HTTP endpoint did not observe the dispatched event")
		}
	}
	processed, err := processNextFederationDelivery(ctx, db, key, client)
	if err != nil || processed {
		t.Fatalf("queue with only backoff-blocked offline peer processed=%t err=%v", processed, err)
	}
	var fastRows, offlineRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, fastPeer).Scan(&fastRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE peer_origin=?`, offlinePeer).Scan(&offlineRows); err != nil {
		t.Fatal(err)
	}
	if fastRows != 0 || offlineRows != 2 {
		t.Fatalf("post-fairness queue fast=%d offline=%d, want drained fast peer and ordered offline backlog", fastRows, offlineRows)
	}
}

type deliveryFairnessObservation struct {
	method, path, host, signatureInput string
	body                               []byte
	err                                error
}

type deliveryFairnessResult struct {
	processed bool
	err       error
}

type deliveryFairnessTransport struct {
	offlineHost   string
	fastHost      string
	fastServerURL string
	started       chan struct{}
	release       chan struct{}
	onlineStart   *sync.Once
	forwarder     *http.Transport
}

func (t *deliveryFairnessTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Host {
	case t.offlineHost:
		t.onlineStart.Do(func() { close(t.started) })
		select {
		case <-t.release:
			return nil, errors.New("controlled offline peer")
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	case t.fastHost:
		serverURL, err := url.Parse(t.fastServerURL)
		if err != nil {
			return nil, err
		}
		clone := request.Clone(request.Context())
		clone.URL.Scheme = serverURL.Scheme
		clone.URL.Host = serverURL.Host
		clone.Host = t.fastHost
		return t.forwarder.RoundTrip(clone)
	default:
		return nil, errors.New("unexpected federation peer in fairness fixture")
	}
}

func enqueueFederationChangeTx(t *testing.T, db *sql.DB, recipeID string) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := enqueueFederationChange(tx, recipeID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
