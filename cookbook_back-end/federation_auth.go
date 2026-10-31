package main

import (
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
	"github.com/common-fate/httpsig/verifier"
)

const (
	federationSignatureTag  = "cookbook-exchange-v1"
	federationSignatureSkew = 5 * time.Minute
	federationNonceTTL      = 10 * time.Minute
	federationInboxPerHour  = 1000
)

type federationKeyDirectory struct{ db *sql.DB }

type federationEd25519 struct{ publicKey []byte }

func (a federationEd25519) Type() string { return alg_ed25519.Ed25519Alg }

func (a federationEd25519) ContentDigest() contentdigest.Digester { return contentdigest.SHA256 }

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
	err := d.db.QueryRowContext(ctx, `SELECT k.public_key FROM federation_peer_keys k JOIN federation_peers p ON p.origin=k.origin WHERE k.key_id=? AND k.status='active' AND p.enabled=1`, keyID).Scan(&publicKey)
	if err != nil {
		return nil, errors.New("unknown federation key")
	}
	if len(publicKey) != 32 {
		return nil, errors.New("invalid federation public key")
	}
	return federationEd25519{publicKey: publicKey}, nil
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
		allowed, err := allowAuthRequest(db, r, "federation-inbox", federationInboxPerHour, time.Hour, trustedProxies)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "rate_limit_unavailable")
			return
		}
		if !allowed {
			writeError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFederationEventBytes)
		verify.ServeHTTP(w, r)
	})
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
	if len(publicKey) != 32 || strings.TrimSpace(keyID) != keyID || keyID == "" || len(keyID) > 200 {
		return errors.New("invalid federation peer key")
	}
	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM federation_peers WHERE origin=?`, origin).Scan(&enabled); err != nil || enabled != 1 {
		return errors.New("federation peer is not enabled")
	}
	_, err := db.Exec(`INSERT INTO federation_peer_keys(key_id,origin,public_key,status,created_at) VALUES(?,?,?,'active',?) ON CONFLICT(key_id) DO UPDATE SET public_key=excluded.public_key,status='active',created_at=excluded.created_at,revoked_at=NULL WHERE federation_peer_keys.origin=excluded.origin`, keyID, origin, publicKey, timestamp())
	if err != nil {
		return fmt.Errorf("save federation peer key: %w", err)
	}
	var storedOrigin string
	if err := db.QueryRow(`SELECT origin FROM federation_peer_keys WHERE key_id=?`, keyID).Scan(&storedOrigin); err != nil || storedOrigin != origin {
		return errors.New("federation key ID already belongs to another origin")
	}
	return nil
}

func revokeFederationPeerKey(db *sql.DB, origin, keyID string) error {
	result, err := db.Exec(`UPDATE federation_peer_keys SET status='revoked',revoked_at=? WHERE origin=? AND key_id=? AND status<>'revoked'`, timestamp(), origin, keyID)
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
	return nil
}
