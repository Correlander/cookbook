package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/common-fate/httpsig"
	"github.com/common-fate/httpsig/alg_ed25519"
	"github.com/common-fate/httpsig/contentdigest"
	"github.com/common-fate/httpsig/sigset"
	"github.com/common-fate/httpsig/verifier"
)

const (
	federationSignatureTag  = "cookbook-exchange-v1"
	federationSignatureSkew = 5 * time.Minute
	federationNonceTTL      = 10 * time.Minute
	federationInboxPerHour  = 1000
)

type federationKeyDirectory struct{ db *sql.DB }

type federationEd25519 struct {
	publicKey []byte
	origin    string
}

func (a federationEd25519) Type() string { return alg_ed25519.Ed25519Alg }

func (a federationEd25519) ContentDigest() contentdigest.Digester { return contentdigest.SHA256 }

func (a federationEd25519) Attributes() any { return a.origin }

func (a federationEd25519) Verify(ctx context.Context, base string, signature []byte) error {
	if len(a.publicKey) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(a.publicKey), []byte(base), signature) {
		return errors.New("signature verification failed")
	}
	return nil
}

func (d federationKeyDirectory) GetKey(ctx context.Context, keyID, clientAlgorithm string) (verifier.Algorithm, error) {
	if strings.TrimSpace(keyID) == "" || clientAlgorithm != "" {
		return nil, errors.New("invalid federation signature key")
	}
	var publicKey []byte
	var origin string
	err := d.db.QueryRowContext(ctx, `SELECT k.public_key,k.origin FROM federation_peer_keys k JOIN federation_peers p ON p.origin=k.origin WHERE k.key_id=? AND k.status='active' AND p.enabled=1`, keyID).Scan(&publicKey, &origin)
	if err != nil {
		return nil, errors.New("unknown federation key")
	}
	if len(publicKey) != 32 {
		return nil, errors.New("invalid federation public key")
	}
	return federationEd25519{publicKey: publicKey, origin: origin}, nil
}

type federationNonceStorage struct{ db *sql.DB }

func (s federationNonceStorage) Seen(ctx context.Context, nonce string) (bool, error) {
	if len(nonce) < 32 || len(nonce) > 256 {
		return false, errors.New("invalid federation nonce")
	}
	hash := sha256.Sum256([]byte(nonce))
	var inserted string
	err := s.db.QueryRowContext(ctx, `INSERT INTO federation_nonces(nonce_hash,expires_at) VALUES(?,?) ON CONFLICT(nonce_hash) DO NOTHING RETURNING nonce_hash`, hex.EncodeToString(hash[:]), time.Now().Add(federationNonceTTL).Unix()).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func federationSignatureMiddleware(db *sql.DB, origin string, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	validation := httpsig.DefaultValidationOpts()
	validation.BeforeDuration = federationSignatureSkew
	validation.AfterDuration = 0
	validation.ForbidClientSideAlg = true
	validation.RequiredCoveredComponents = map[string]bool{
		"@method": true, "@target-uri": true, "content-type": true,
		"content-length": true, "content-digest": true,
	}
	validation.RequireNonce = true
	verify := httpsig.Middleware(httpsig.MiddlewareOpts{
		NonceStorage: federationNonceStorage{db: db},
		KeyDirectory: federationKeyDirectory{db: db},
		Tag:          federationSignatureTag, Validation: &validation,
		Scheme: "https", Authority: strings.TrimPrefix(origin, "https://"),
	})(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 0 || r.ContentLength > maxFederationEventBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_content_length")
			return
		}
		allowed, err := allowAuthRequestContext(r.Context(), db, r, "federation-inbox", federationInboxPerHour, time.Hour, trustedProxies)
		if writeAuthRateLimitError(w, err) {
			return
		}
		if !allowed {
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFederationEventBytes)
		digest, err := contentdigest.SHA256.HashRequest(w, r)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "event_too_large")
			} else {
				writeError(w, http.StatusBadRequest, "invalid_body")
			}
			return
		}
		providedDigest := r.Header.Values("Content-Digest")
		if len(providedDigest) != 1 || strings.TrimSpace(providedDigest[0]) != digest {
			writeError(w, http.StatusBadRequest, "invalid_content_digest")
			return
		}
		if err := validateFederationSignatureProfile(r); err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_signature_profile")
			return
		}
		verify.ServeHTTP(w, r)
	})
}

func validateFederationSignatureProfile(r *http.Request) error {
	set, err := sigset.Unmarshal(r)
	if err != nil {
		return err
	}
	message, err := set.Find(federationSignatureTag)
	if err != nil {
		return err
	}
	want := []string{"@method", "@target-uri", "content-type", "content-length", "content-digest"}
	got := message.Input.CoveredComponents
	if message.Input.Alg != "" || message.Input.Tag != federationSignatureTag || len(got) != len(want) {
		return errors.New("unsupported federation signature profile")
	}
	for i := range want {
		if got[i] != want[i] {
			return errors.New("unsupported federation signature components")
		}
	}
	return nil
}

func federationServerOrigin(db *sql.DB) (string, bool) {
	var origin string
	var enabled int
	if err := db.QueryRow(`SELECT federation_origin,federation_enabled FROM instance_settings WHERE id=1`).Scan(&origin, &enabled); err != nil {
		return "", false
	}
	return origin, enabled == 1 && origin != ""
}

func registerFederationPeer(db *sql.DB, origin string, enabled bool) error {
	canonical, err := canonicalFederationOrigin(origin)
	if err != nil {
		return errors.New("invalid federation peer origin")
	}
	origin = canonical
	state := 0
	if enabled {
		state = 1
	}
	_, err = db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(origin) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`, origin, state, timestamp(), timestamp())
	if err != nil {
		return fmt.Errorf("save federation peer: %w", err)
	}
	return nil
}

func registerFederationPeerKey(db *sql.DB, origin, keyID string, publicKey []byte) error {
	canonical, err := canonicalFederationOrigin(origin)
	if err != nil || canonical != origin || len(publicKey) != 32 || !validFederationKeyID(keyID) {
		return errors.New("invalid federation peer key")
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_peers WHERE origin=?`, origin).Scan(&exists); err != nil || exists != 1 {
		return errors.New("federation peer does not exist")
	}
	var storedOrigin, status string
	var storedKey []byte
	err = db.QueryRow(`SELECT origin,public_key,status FROM federation_peer_keys WHERE key_id=?`, keyID).Scan(&storedOrigin, &storedKey, &status)
	if err == nil {
		if storedOrigin == origin && status == "active" && bytes.Equal(storedKey, publicKey) {
			return nil
		}
		return errors.New("federation key ID is already registered and cannot be reused")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := db.Exec(`INSERT INTO federation_peer_keys(key_id,origin,public_key,status,created_at) VALUES(?,?,?,'active',?)`, keyID, origin, publicKey, timestamp()); err != nil {
		return fmt.Errorf("save federation peer key: %w", err)
	}
	return nil
}

func validFederationKeyID(keyID string) bool {
	if keyID == "" || len(keyID) > 200 {
		return false
	}
	for _, char := range keyID {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' && char != ':' {
			return false
		}
	}
	return true
}

func revokeFederationPeerKey(db *sql.DB, origin, keyID string) error {
	canonical, err := canonicalFederationOrigin(origin)
	if err != nil || canonical != origin || !validFederationKeyID(keyID) {
		return errors.New("invalid federation peer key")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE federation_peer_keys SET status='revoked',revoked_at=? WHERE origin=? AND key_id=? AND status<>'revoked'`, timestamp(), origin, keyID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	var activeKeys int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM federation_peer_keys WHERE origin=? AND status='active'`, origin).Scan(&activeKeys); err != nil {
		return err
	}
	if activeKeys == 0 {
		if _, err := tx.Exec(`UPDATE federation_peers SET enabled=0,updated_at=? WHERE origin=?`, timestamp(), origin); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM federation_outbox WHERE peer_origin=?`, origin); err != nil {
			return err
		}
	}
	return tx.Commit()
}
