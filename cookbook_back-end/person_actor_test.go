package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const personActorTestOrigin = "https://cookbook.example.org"

func personActorTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if path == "" {
		path = ":memory:"
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('person-a','a@example.test','Person A','created-a'),
		('person-b','b@example.test','Person B','created-b')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func setPersonActorOrigin(t *testing.T, db *sql.DB, origin string) {
	t.Helper()
	if err := configureFederationOrigin(db, origin); err != nil {
		t.Fatal(err)
	}
}

func TestPersonActorCreationIsOptInAndRequiresExistingAccountAndPinnedOrigin(t *testing.T) {
	db := personActorTestDB(t, "")
	ctx := context.Background()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM person_actor_ids`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fresh database actor count=%d err=%v, want 0", count, err)
	}
	if _, err := CreatePersonActor(ctx, db, "person-a"); !errors.Is(err, ErrPersonActorOriginUnavailable) {
		t.Fatalf("actor creation with unset origin error=%v, want origin unavailable", err)
	}
	setPersonActorOrigin(t, db, personActorTestOrigin)
	if _, err := CreatePersonActor(ctx, db, "missing-user"); !errors.Is(err, ErrPersonActorAccountNotFound) {
		t.Fatalf("actor creation for missing account error=%v, want account not found", err)
	}
	if _, err := CreatePersonActor(ctx, db, "person-a"); err != nil {
		t.Fatalf("create explicit Person actor: %v", err)
	}
	if _, err := CreatePersonActor(ctx, db, "person-a"); !errors.Is(err, ErrPersonActorAlreadyExists) {
		t.Fatalf("second active actor error=%v, want already exists", err)
	}
}

func TestPersonActorUsesOpaquePinnedURIAndValidatedEd25519Key(t *testing.T) {
	path := filepath.Join(t.TempDir(), "person-actor.db")
	db := personActorTestDB(t, path)
	setPersonActorOrigin(t, db, "https://Cookbook.Example.org:443/")
	ctx := context.Background()
	created, err := CreatePersonActor(ctx, db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 32 || created.ID != strings.ToLower(created.ID) {
		t.Fatalf("actor ID=%q, want 128-bit lowercase hex", created.ID)
	}
	if _, err := hex.DecodeString(created.ID); err != nil {
		t.Fatalf("actor ID is not hex: %v", err)
	}
	if strings.Contains(created.URI, "person-a") || strings.Contains(created.URI, "@example.test") || created.URI != personActorTestOrigin+"/actors/"+created.ID {
		t.Fatalf("actor URI=%q contains account data or is not canonical", created.URI)
	}
	if len(created.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("public key length=%d, want %d", len(created.PublicKey), ed25519.PublicKeySize)
	}
	loaded, err := loadPersonActorKey(ctx, db, created.ID)
	if err != nil {
		t.Fatalf("load validated actor key: %v", err)
	}
	if loaded.URI != created.URI || loaded.KeyID != created.URI+"#main-key" || !ed25519.PublicKey(loaded.PublicKey).Equal(ed25519.PublicKey(created.PublicKey)) {
		t.Fatalf("loaded actor identity differs from public creation result: %+v", loaded)
	}
	message := []byte("actor-signing-check")
	signature := ed25519.Sign(loaded.PrivateKey, message)
	if !ed25519.Verify(loaded.PublicKey, message, signature) {
		t.Fatal("loaded key did not sign for its stored public key")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := loadPersonActorKey(ctx, db, created.ID); err != nil {
		t.Fatalf("actor key did not survive restart: %v", err)
	}
	if _, err := CreatePersonActor(ctx, db, "person-b"); err != nil {
		t.Fatalf("second account could not create a distinct actor: %v", err)
	}
}

func TestPersonActorLoadFailsClosedOnMalformedOrMismatchedKeys(t *testing.T) {
	db := personActorTestDB(t, "")
	setPersonActorOrigin(t, db, personActorTestOrigin)
	ctx := context.Background()
	actor, err := CreatePersonActor(ctx, db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	var privateDER []byte
	if err := db.QueryRow(`SELECT private_key_pkcs8 FROM person_actors WHERE actor_id=?`, actor.ID).Scan(&privateDER); err != nil {
		t.Fatal(err)
	}
	otherPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE person_actors SET public_key=? WHERE actor_id=?`, []byte(otherPublicKey), actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPersonActorKey(ctx, db, actor.ID); err == nil || strings.Contains(err.Error(), hex.EncodeToString(privateDER)) {
		t.Fatalf("mismatched key load error=%v; want safe failure without private bytes", err)
	}
	if _, err := db.Exec(`UPDATE person_actors SET public_key=?,private_key_pkcs8=x'00' WHERE actor_id=?`, actor.PublicKey, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPersonActorKey(ctx, db, actor.ID); err == nil || strings.Contains(err.Error(), hex.EncodeToString(privateDER)) {
		t.Fatalf("malformed key load error=%v; want safe failure without private bytes", err)
	}
}

func TestPersonActorRetirementRemovesKeyAndPermanentlyReservesURI(t *testing.T) {
	db := personActorTestDB(t, "")
	setPersonActorOrigin(t, db, personActorTestOrigin)
	ctx := context.Background()
	actor, err := CreatePersonActor(ctx, db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := RetirePersonActor(ctx, db, "person-a"); err != nil {
		t.Fatalf("retire Person actor: %v", err)
	}
	var state, uri string
	if err := db.QueryRow(`SELECT state,actor_uri FROM person_actor_ids WHERE actor_id=?`, actor.ID).Scan(&state, &uri); err != nil || state != "retired" || uri != actor.URI {
		t.Fatalf("retired registry state=%q uri=%q err=%v", state, uri, err)
	}
	var bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM person_actors WHERE actor_id=?`, actor.ID).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("retired private-key binding count=%d err=%v, want 0", bindings, err)
	}
	if _, err := loadPersonActorKey(ctx, db, actor.ID); !errors.Is(err, ErrPersonActorNotActive) {
		t.Fatalf("retired actor key load error=%v, want not active", err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO person_actors(actor_id,user_id,public_key,private_key_pkcs8,created_at,key_rotated_at) VALUES(?,?,?,?,?,?)`, actor.ID, "person-b", []byte(privateKey.Public().(ed25519.PublicKey)), privateDER, "now", "now"); err == nil {
		t.Fatal("rebound retired actor ID to another account")
	}
	if _, err := db.Exec(`UPDATE person_actor_ids SET actor_uri=? WHERE actor_id=?`, "https://other.example.org/actors/"+actor.ID, actor.ID); err == nil {
		t.Fatal("changed permanent actor URI reservation")
	}
	if _, err := db.Exec(`DELETE FROM person_actor_ids WHERE actor_id=?`, actor.ID); err == nil {
		t.Fatal("deleted permanent actor URI reservation")
	}
	if err := RetirePersonActor(ctx, db, "person-a"); !errors.Is(err, ErrPersonActorNotActive) {
		t.Fatalf("second retirement error=%v, want not active", err)
	}
}

func TestPersonActorCreationAndRetirementRollbackWithAuditFailure(t *testing.T) {
	db := personActorTestDB(t, "")
	setPersonActorOrigin(t, db, personActorTestOrigin)
	if _, err := db.Exec(`CREATE TRIGGER reject_person_actor_create BEFORE INSERT ON audit_events WHEN NEW.action='person_actor.create' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePersonActor(context.Background(), db, "person-a"); err == nil {
		t.Fatal("creation succeeded despite audit failure")
	}
	var registryCount, bindingCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM person_actor_ids`).Scan(&registryCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM person_actors`).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if registryCount != 0 || bindingCount != 0 {
		t.Fatalf("failed create left registry=%d bindings=%d", registryCount, bindingCount)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_person_actor_create`); err != nil {
		t.Fatal(err)
	}
	actor, err := CreatePersonActor(context.Background(), db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_person_actor_retire BEFORE INSERT ON audit_events WHEN NEW.action='person_actor.retire' BEGIN SELECT RAISE(ABORT,'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := RetirePersonActor(context.Background(), db, "person-a"); err == nil {
		t.Fatal("retirement succeeded despite audit failure")
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM person_actor_ids WHERE actor_id=?`, actor.ID).Scan(&state); err != nil || state != "active" {
		t.Fatalf("failed retirement state=%q err=%v, want active", state, err)
	}
	if _, err := loadPersonActorKey(context.Background(), db, actor.ID); err != nil {
		t.Fatalf("failed retirement removed active key: %v", err)
	}
}

func TestPersonActorDatabaseConstraintsEnforceOneActiveBindingPerAccount(t *testing.T) {
	db := personActorTestDB(t, "")
	setPersonActorOrigin(t, db, personActorTestOrigin)
	actor, err := CreatePersonActor(context.Background(), db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	var privateDER []byte
	if err := db.QueryRow(`SELECT private_key_pkcs8 FROM person_actors WHERE actor_id=?`, actor.ID).Scan(&privateDER); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO person_actor_ids(actor_id,actor_uri,state,created_at) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?, 'active','now')`, personActorTestOrigin+"/actors/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO person_actors(actor_id,user_id,public_key,private_key_pkcs8,created_at,key_rotated_at) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','person-a',?,?,?,?)`, actor.PublicKey, privateDER, "now", "now"); err == nil {
		t.Fatal("database accepted two active Person bindings for one account")
	}
}
