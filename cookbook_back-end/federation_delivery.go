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
	transport := &signer.Transport{
		KeyID: keyID, Tag: federationSignatureTag,
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
	var delivery federationDelivery
	err = tx.QueryRow(`SELECT q.id,q.peer_origin,q.payload,q.attempts FROM federation_outbox q JOIN federation_peers p ON p.origin=q.peer_origin WHERE p.enabled=1 AND q.attempts<12 AND q.next_attempt<=? AND (q.lease_until IS NULL OR q.lease_until<?) AND NOT EXISTS(SELECT 1 FROM federation_outbox earlier WHERE earlier.peer_origin=q.peer_origin AND earlier.object_id=q.object_id AND earlier.sequence<q.sequence) ORDER BY q.next_attempt,q.created_at,q.id LIMIT 1`, now.Unix(), now.Unix()).Scan(&delivery.id, &delivery.peerOrigin, &delivery.payload, &delivery.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
