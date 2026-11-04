package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
	"github.com/common-fate/httpsig/alg_ed25519"
	"github.com/common-fate/httpsig/contentdigest"
	"github.com/common-fate/httpsig/signature"
	"github.com/common-fate/httpsig/signer"
	"github.com/common-fate/httpsig/sigset"
)

const (
	federationDeliveryTimeout = 10 * time.Second
	federationResponseLimit   = 64 << 10
	federationOutboxPoll      = 5 * time.Second
	federationLeaseDuration   = 30 * time.Second
	federationReconcileBatch  = 100
)

type federationEd25519Signer struct{ privateKey ed25519.PrivateKey }

func (s federationEd25519Signer) Type() string { return "" }

func (s federationEd25519Signer) ContentDigest() contentdigest.Digester { return contentdigest.SHA256 }

func (s federationEd25519Signer) Sign(ctx context.Context, base string) ([]byte, error) {
	return (alg_ed25519.Ed25519{PrivateKey: s.privateKey}).Sign(ctx, base)
}

type federationIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func federationDialContext(resolver federationIPResolver, dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dial == nil {
		dialer := &net.Dialer{Timeout: federationDeliveryTimeout, KeepAlive: 30 * time.Second}
		dial = dialer.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("federation peer has no IP addresses")
		}
		if err := validateFederationAddresses(ips); err != nil {
			return nil, err
		}
		return dial(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

func buildFederationDeliveryClient(resolver federationIPResolver) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           federationDialContext(resolver, nil),
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   1,
		MaxConnsPerHost:       1,
	}
	return &http.Client{Transport: transport, Timeout: federationDeliveryTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func isPublicFederationIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	blocked := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	}
	for _, prefix := range blocked {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func validateFederationAddresses(addresses []netip.Addr) error {
	if len(addresses) == 0 {
		return errors.New("federation peer has no IP addresses")
	}
	for _, address := range addresses {
		if !isPublicFederationIP(address) {
			return errors.New("federation peer resolved to a non-public address")
		}
	}
	return nil
}

func deliverFederationEvent(ctx context.Context, client *http.Client, key federationSigningKey, keyID, peerOrigin string, payload []byte) error {
	canonical, err := canonicalFederationOrigin(peerOrigin)
	if err != nil || canonical != peerOrigin || len(payload) == 0 || len(payload) > maxFederationEventBytes {
		return errors.New("invalid federation delivery target or payload")
	}
	target := peerOrigin + "/api/federation/inbox"
	parsedTarget, err := url.Parse(target)
	if err != nil || parsedTarget.Scheme != "https" || parsedTarget.User != nil || parsedTarget.Fragment != "" || parsedTarget.RawQuery != "" {
		return errors.New("invalid federation delivery URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(payload))
	req.Header.Set("Content-Type", "application/json")
	digester := contentdigest.SHA256
	contentDigest, err := digester.HashRequest(nil, req)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Digest", contentDigest)
	tag := federationSignatureTag
	if _, err := exchangev2.Decode(payload); err == nil {
		tag = federationSignatureV2Tag
	} else if _, _, v1Err := decodeFederationEvent(payload); v1Err != nil {
		return errors.New("invalid queued federation event")
	}
	transport := &signer.Transport{
		KeyID: keyID, Tag: tag,
		Alg:               federationEd25519Signer{privateKey: key.PrivateKey},
		CoveredComponents: []string{"@method", "@target-uri", "content-type", "content-length", "content-digest"},
	}
	message, err := transport.Sign(req)
	if err != nil {
		return fmt.Errorf("sign federation delivery: %w", err)
	}
	set := sigset.Set{Messages: map[string]*signature.Message{}}
	set.Add(message)
	if err := set.Include(req); err != nil {
		return fmt.Errorf("include federation signature: %w", err)
	}
	if client == nil {
		client = buildFederationDeliveryClient(nil)
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send federation delivery: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, federationResponseLimit+1))
	if err != nil {
		return fmt.Errorf("read federation acknowledgement: %w", err)
	}
	if len(responseBody) > federationResponseLimit {
		return errors.New("federation acknowledgement exceeded limit")
	}
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("federation peer returned status %d", response.StatusCode)
	}
	return nil
}

type federationDelivery struct {
	id         string
	peerOrigin string
	payload    []byte
	attempts   int
}

func claimFederationDelivery(db *sql.DB, now time.Time) (*federationDelivery, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := reconcileFederationPublicationEgress(context.Background(), tx, now); err != nil {
		return nil, err
	}
	var federation int
	if err := tx.QueryRow(`SELECT federation_enabled FROM instance_settings WHERE id=1`).Scan(&federation); err != nil {
		return nil, err
	}
	if federation != 1 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var delivery federationDelivery
	err = tx.QueryRow(`SELECT q.id,q.peer_origin,q.payload,q.attempts FROM federation_outbox q JOIN federation_peers p ON p.origin=q.peer_origin WHERE p.enabled=1 AND q.attempts<12 AND q.next_attempt<=? AND (q.lease_until IS NULL OR q.lease_until<?) AND NOT EXISTS(SELECT 1 FROM federation_outbox earlier WHERE earlier.peer_origin=q.peer_origin AND earlier.object_id=q.object_id AND earlier.sequence<q.sequence) ORDER BY q.next_attempt,q.created_at,q.id LIMIT 1`, now.Unix(), now.Unix()).Scan(&delivery.id, &delivery.peerOrigin, &delivery.payload, &delivery.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v2Event, v2Err := exchangev2.Decode(delivery.payload)
	isV2 := v2Err == nil
	var eventType, objectID, sourceOrigin string
	if isV2 {
		eventType, objectID, sourceOrigin = v2Event.EventType, v2Event.ObjectID, v2Event.SourceOrigin
	} else {
		event, _, err := decodeFederationEvent(delivery.payload)
		if err != nil {
			return nil, err
		}
		eventType, objectID, sourceOrigin = event.EventType, event.ObjectID, event.SourceOrigin
	}
	var origin string
	if err := tx.QueryRow(`SELECT federation_origin FROM instance_settings WHERE id=1`).Scan(&origin); err != nil {
		return nil, err
	}
	if !validFederationEgressOrigin(origin) || sourceOrigin != origin {
		return nil, errors.New("federation origin is unavailable for queued event")
	}
	if eventType == federationEventSnapshot || eventType == federationEventUpdate || eventType == exchangev2.RecipeSnapshot || eventType == exchangev2.RecipeUpdate {
		recipeID, ok := federationObjectRecipeID(origin, objectID)
		if !ok || sourceOrigin != origin {
			return nil, errors.New("federation outbox object does not match local origin")
		}
		eligible, err := federationRecipePublicationEligible(context.Background(), tx, recipeID, origin)
		if err != nil {
			return nil, err
		}
		if !eligible {
			if err := enqueueFederationChange(tx, recipeID); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
	result, err := tx.Exec(`UPDATE federation_outbox SET lease_until=? WHERE id=? AND (lease_until IS NULL OR lease_until<?)`, now.Add(federationLeaseDuration).Unix(), delivery.id, now.Unix())
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &delivery, nil
}

func reconcileFederationPublicationEgress(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.Query(`SELECT DISTINCT r.id FROM federation_local_peer_objects l
		JOIN recipes r ON r.id=l.recipe_id
		JOIN instance_settings s ON s.id=1
		LEFT JOIN recipe_publication_policy p ON p.recipe_id=r.id
		WHERE l.published=1 AND NOT (s.federation_enabled=1 AND s.public_instance=1 AND s.federation_origin<>''
			AND r.visibility='public' AND r.deleted_at IS NULL
			AND p.recipe_id IS NOT NULL AND p.publication_state='published' AND p.federation_mode='followers')
		ORDER BY r.id LIMIT ?`, federationReconcileBatch)
	if err != nil {
		return err
	}
	recipeIDs := make(map[string]struct{})
	for rows.Next() {
		var recipeID string
		if err := rows.Scan(&recipeID); err != nil {
			rows.Close()
			return err
		}
		recipeIDs[recipeID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = tx.Query(`SELECT q.payload FROM federation_outbox q
		JOIN instance_settings s ON s.id=1
		JOIN recipes r ON q.object_id=s.federation_origin||'/federation/recipes/'||r.id
		LEFT JOIN recipe_publication_policy p ON p.recipe_id=r.id
		WHERE (q.lease_until IS NULL OR q.lease_until<?)
			AND json_extract(CAST(q.payload AS TEXT),'$.event_type') IN (?,?)
			AND NOT (s.federation_enabled=1 AND s.public_instance=1 AND s.federation_origin<>''
				AND r.visibility='public' AND r.deleted_at IS NULL
				AND p.recipe_id IS NOT NULL AND p.publication_state='published' AND p.federation_mode='followers')
		ORDER BY q.created_at,q.id LIMIT ?`, now.Unix(), federationEventSnapshot, federationEventUpdate, federationReconcileBatch)
	if err != nil {
		return err
	}
	var candidatePayloads [][]byte
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		candidatePayloads = append(candidatePayloads, payload)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var origin string
	if err := tx.QueryRow(`SELECT federation_origin FROM instance_settings WHERE id=1`).Scan(&origin); err != nil {
		return err
	}
	for _, payload := range candidatePayloads {
		event, _, err := decodeFederationEvent(payload)
		if err != nil {
			return err
		}
		if event.EventType != federationEventSnapshot && event.EventType != federationEventUpdate {
			continue
		}
		recipeID, ok := federationObjectRecipeID(origin, event.ObjectID)
		if !ok || event.SourceOrigin != origin {
			return errors.New("federation outbox object does not match local origin")
		}
		eligible, err := federationRecipePublicationEligible(ctx, tx, recipeID, origin)
		if err != nil {
			return err
		}
		if !eligible {
			recipeIDs[recipeID] = struct{}{}
		}
	}
	for recipeID := range recipeIDs {
		if err := enqueueFederationChange(tx, recipeID); err != nil {
			return err
		}
	}
	return nil
}

func federationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute
	for range attempt - 1 {
		delay *= 2
		if delay >= 6*time.Hour {
			delay = 6 * time.Hour
			break
		}
	}
	return delay/2 + time.Duration(rand.Int64N(int64(delay)))
}

func processNextFederationDelivery(ctx context.Context, db *sql.DB, key federationSigningKey, client *http.Client) (bool, error) {
	delivery, err := claimFederationDelivery(db, time.Now())
	if err != nil || delivery == nil {
		return false, err
	}
	err = deliverFederationEvent(ctx, client, key, key.ID, delivery.peerOrigin, delivery.payload)
	if err == nil {
		_, deleteErr := db.ExecContext(ctx, `DELETE FROM federation_outbox WHERE id=?`, delivery.id)
		return true, deleteErr
	}
	attempt := delivery.attempts + 1
	lastError := err.Error()
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}
	_, updateErr := db.ExecContext(ctx, `UPDATE federation_outbox SET attempts=?,next_attempt=?,lease_until=NULL,last_error=? WHERE id=?`, attempt, time.Now().Add(federationRetryDelay(attempt)).Unix(), lastError, delivery.id)
	if updateErr != nil {
		return true, errors.Join(err, updateErr)
	}
	return true, nil
}

func startFederationOutboxWorker(parent context.Context, db *sql.DB, keyDirectory string) {
	client := buildFederationDeliveryClient(nil)
	process := func() {
		ctx, cancel := context.WithTimeout(parent, federationDeliveryTimeout+time.Second)
		defer cancel()
		key, err := loadOrCreateFederationSigningKey(keyDirectory)
		if err != nil {
			log.Printf("federation signing key reload failed: %v", err)
			return
		}
		processed, err := processNextFederationDelivery(ctx, db, key, client)
		if err != nil {
			log.Printf("federation outbox delivery error: %v", err)
		} else if processed {
			log.Printf("federation outbox delivery processed")
		}
	}
	go func() {
		process()
		ticker := time.NewTicker(federationOutboxPoll)
		defer ticker.Stop()
		for {
			select {
			case <-parent.Done():
				return
			case <-ticker.C:
				process()
			}
		}
	}()
}
