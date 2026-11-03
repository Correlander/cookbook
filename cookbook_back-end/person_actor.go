package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const personActorSchemaDDL = `
CREATE TABLE person_actor_ids (
  actor_id TEXT NOT NULL PRIMARY KEY
    CHECK (length(actor_id)=32 AND actor_id NOT GLOB '*[^0-9a-f]*'),
  actor_uri TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL CHECK (state IN ('active','retired')),
  created_at TEXT NOT NULL,
  retired_at TEXT,
  CHECK ((state='active' AND retired_at IS NULL) OR (state='retired' AND retired_at IS NOT NULL))
);
CREATE TABLE person_actors (
  actor_id TEXT NOT NULL PRIMARY KEY REFERENCES person_actor_ids(actor_id) ON DELETE RESTRICT,
  user_id TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
  public_key BLOB NOT NULL CHECK (length(public_key)=32),
  private_key_pkcs8 BLOB NOT NULL CHECK (length(private_key_pkcs8)>0),
  created_at TEXT NOT NULL,
  key_rotated_at TEXT NOT NULL
);
CREATE TRIGGER person_actor_binding_insert_active
BEFORE INSERT ON person_actors
WHEN NOT EXISTS (SELECT 1 FROM person_actor_ids WHERE actor_id=NEW.actor_id AND state='active')
BEGIN SELECT RAISE(ABORT, 'person actor is not active'); END;
CREATE TRIGGER person_actor_binding_update_identity_immutable
BEFORE UPDATE OF actor_id,user_id ON person_actors
BEGIN SELECT RAISE(ABORT, 'person actor account binding is immutable'); END;
CREATE TRIGGER person_actor_binding_delete_requires_retirement
BEFORE DELETE ON person_actors
WHEN EXISTS (SELECT 1 FROM person_actor_ids WHERE actor_id=OLD.actor_id AND state='active')
BEGIN SELECT RAISE(ABORT, 'person actor must be retired before key removal'); END;
CREATE TRIGGER person_actor_registry_identity_immutable
BEFORE UPDATE OF actor_id,actor_uri ON person_actor_ids
BEGIN SELECT RAISE(ABORT, 'person actor URI reservation is immutable'); END;
CREATE TRIGGER person_actor_registry_terminal
BEFORE UPDATE OF state ON person_actor_ids
WHEN NOT (OLD.state='active' AND NEW.state='retired')
BEGIN SELECT RAISE(ABORT, 'person actor retirement is terminal'); END;
CREATE TRIGGER person_actor_registry_retire_key
AFTER UPDATE OF state ON person_actor_ids
WHEN NEW.state='retired'
BEGIN DELETE FROM person_actors WHERE actor_id=NEW.actor_id; END;
CREATE TRIGGER person_actor_registry_reservation_permanent
BEFORE DELETE ON person_actor_ids
BEGIN SELECT RAISE(ABORT, 'person actor URI reservation is permanent'); END;
`

var (
	ErrPersonActorOriginUnavailable = errors.New("Person actor creation requires a pinned HTTPS origin")
	ErrPersonActorAccountNotFound   = errors.New("local account does not exist")
	ErrPersonActorAlreadyExists     = errors.New("local account already has a Person actor")
	ErrPersonActorNotActive         = errors.New("local account has no active Person actor")
)

// PersonActor contains only public actor identity material. The private key is
// deliberately kept in the unexported personActorKey type below.
type PersonActor struct {
	ID        string
	URI       string
	PublicKey []byte
}

type personActorKey struct {
	ID         string
	URI        string
	KeyID      string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

// ApplyPersonActorMigration is the additive v5 schema body. Its caller owns
// the transaction that applies the DDL and records the migration ledger row.
func ApplyPersonActorMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, personActorSchemaDDL); err != nil {
		return fmt.Errorf("apply Person actor schema: %w", err)
	}
	return nil
}

// CreatePersonActor explicitly creates the one optional Person actor for an
// existing local account. The canonical origin is read from pinned instance
// settings inside the transaction; an unset origin does not affect local use.
func CreatePersonActor(ctx context.Context, db *sql.DB, accountID string) (PersonActor, error) {
	if strings.TrimSpace(accountID) == "" {
		return PersonActor{}, ErrPersonActorAccountNotFound
	}

	var actor PersonActor
	err := withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		var origin string
		if err := tx.QueryRowContext(ctx, `SELECT federation_origin FROM instance_settings WHERE id=1`).Scan(&origin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrPersonActorOriginUnavailable
			}
			return fmt.Errorf("read pinned Person actor origin: %w", err)
		}
		canonicalOrigin, err := canonicalFederationOrigin(origin)
		if err != nil || canonicalOrigin != origin {
			return ErrPersonActorOriginUnavailable
		}
		if err := requirePersonActorAccount(ctx, tx, accountID); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM person_actors WHERE user_id=?)`, accountID).Scan(&exists); err != nil {
			return fmt.Errorf("check existing Person actor: %w", err)
		}
		if exists != 0 {
			return ErrPersonActorAlreadyExists
		}

		var randomID [16]byte
		if _, err := rand.Read(randomID[:]); err != nil {
			return fmt.Errorf("generate Person actor ID: %w", err)
		}
		actor.ID = hex.EncodeToString(randomID[:])
		actor.URI = canonicalOrigin + "/actors/" + actor.ID

		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("generate Person actor key: %w", err)
		}
		privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
		if err != nil {
			return fmt.Errorf("encode Person actor key: %w", err)
		}
		now := formatOwnerTime(time.Now().UTC())
		if _, err := tx.ExecContext(ctx, `INSERT INTO person_actor_ids(actor_id,actor_uri,state,created_at) VALUES(?,?,'active',?)`, actor.ID, actor.URI, now); err != nil {
			return fmt.Errorf("reserve Person actor URI: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO person_actors(actor_id,user_id,public_key,private_key_pkcs8,created_at,key_rotated_at) VALUES(?,?,?,?,?,?)`, actor.ID, accountID, []byte(publicKey), privateKeyDER, now, now); err != nil {
			return fmt.Errorf("store Person actor key: %w", err)
		}
		if err := insertInstanceOwnerAudit(ctx, tx, accountID, "person_actor.create", "person_actor", actor.ID, 201, time.Now().UTC()); err != nil {
			return fmt.Errorf("audit Person actor creation: %w", err)
		}
		actor.PublicKey = append([]byte(nil), publicKey...)
		return nil
	})
	if err != nil {
		return PersonActor{}, err
	}
	return actor, nil
}

// RetirePersonActor permanently reserves the account's actor URI and removes
// its private key in the same transaction. A retired actor cannot be rebound.
func RetirePersonActor(ctx context.Context, db *sql.DB, accountID string) error {
	if strings.TrimSpace(accountID) == "" {
		return ErrPersonActorNotActive
	}
	return withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		var actorID string
		err := tx.QueryRowContext(ctx, `SELECT actor_id FROM person_actors WHERE user_id=?`, accountID).Scan(&actorID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPersonActorNotActive
		}
		if err != nil {
			return fmt.Errorf("find active Person actor: %w", err)
		}
		now := formatOwnerTime(time.Now().UTC())
		result, err := tx.ExecContext(ctx, `UPDATE person_actor_ids SET state='retired',retired_at=? WHERE actor_id=? AND state='active'`, now, actorID)
		if err != nil {
			return fmt.Errorf("retire Person actor: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrPersonActorNotActive
		}
		if err := insertInstanceOwnerAudit(ctx, tx, accountID, "person_actor.retire", "person_actor", actorID, 200, time.Now().UTC()); err != nil {
			return fmt.Errorf("audit Person actor retirement: %w", err)
		}
		return nil
	})
}

// loadPersonActorKey validates all persisted identity material before any
// future caller is allowed to sign as this actor. It never repairs or replaces
// missing/corrupt keys.
func loadPersonActorKey(ctx context.Context, db *sql.DB, actorID string) (personActorKey, error) {
	var actor personActorKey
	var publicKey, privateKeyDER []byte
	err := db.QueryRowContext(ctx, `SELECT registry.actor_uri,actor.public_key,actor.private_key_pkcs8
		FROM person_actor_ids AS registry
		JOIN person_actors AS actor ON actor.actor_id=registry.actor_id
		WHERE registry.actor_id=? AND registry.state='active'`, actorID).Scan(&actor.URI, &publicKey, &privateKeyDER)
	if errors.Is(err, sql.ErrNoRows) {
		return personActorKey{}, ErrPersonActorNotActive
	}
	if err != nil {
		return personActorKey{}, fmt.Errorf("load Person actor key: %w", err)
	}
	if !validPersonActorURI(actor.URI, actorID) || len(publicKey) != ed25519.PublicKeySize {
		return personActorKey{}, errors.New("stored Person actor identity is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(privateKeyDER)
	if err != nil {
		return personActorKey{}, errors.New("stored Person actor key is invalid")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return personActorKey{}, errors.New("stored Person actor key has the wrong type")
	}
	derivedPublicKey := privateKey.Public().(ed25519.PublicKey)
	if subtle.ConstantTimeCompare(derivedPublicKey, publicKey) != 1 {
		return personActorKey{}, errors.New("stored Person actor key does not match its public key")
	}
	actor.ID = actorID
	actor.KeyID = actor.URI + "#main-key"
	actor.PublicKey = append(ed25519.PublicKey(nil), publicKey...)
	actor.PrivateKey = append(ed25519.PrivateKey(nil), privateKey...)
	return actor, nil
}

func validPersonActorURI(raw, actorID string) bool {
	if len(actorID) != 32 || actorID != strings.ToLower(actorID) {
		return false
	}
	if _, err := hex.DecodeString(actorID); err != nil {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	origin, err := canonicalFederationOrigin(parsed.Scheme + "://" + parsed.Host)
	if err != nil || parsed.Path != "/actors/"+actorID {
		return false
	}
	return raw == origin+"/actors/"+actorID
}

func requirePersonActorAccount(ctx context.Context, exec migrationExecutor, accountID string) error {
	var exists int
	if err := exec.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=?)`, accountID).Scan(&exists); err != nil {
		return fmt.Errorf("check local Person actor account: %w", err)
	}
	if exists == 0 {
		return ErrPersonActorAccountNotFound
	}
	return nil
}
