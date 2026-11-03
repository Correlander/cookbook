package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFederationKeyActivationReportsUnknownAfterDirectorySyncFailure(t *testing.T) {
	directory := t.TempDir()
	active, err := loadOrCreateFederationSigningKey(directory)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := stageFederationKeyRotation(directory)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := activateFederationKeyRotationWithSync(directory, func(string) error {
		return errors.New("injected directory sync failure")
	})
	if !errors.Is(err, errFederationRotationOutcomeUnknown) || activated.ID != pending.ID {
		t.Fatalf("activation key=%s err=%v, want pending key and indeterminate result", activated.ID, err)
	}
	current, err := readFederationSigningKey(filepath.Join(directory, federationPrivateKeyFile))
	if err != nil || current.ID != pending.ID || string(current.PrivateKey) != string(pending.PrivateKey) {
		t.Fatalf("active file is not the complete intended key: id=%s err=%v", current.ID, err)
	}
	if current.ID == active.ID {
		t.Fatal("active file retained the prior key after successful rename")
	}
	if _, err := os.Lstat(federationPendingKeyPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending file after activation=%v, want absent", err)
	}
}

func TestFederationKeyRotationCommitFailureReturnsStableUnknownAndFingerprint(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.org','Owner',0,'now'),('target','target@example.org','Target',0,'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimInstanceOwner(context.Background(), db, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE rotation_commit_parent(id TEXT PRIMARY KEY);
		CREATE TABLE rotation_commit_child(parent_id TEXT NOT NULL,
			FOREIGN KEY(parent_id) REFERENCES rotation_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	active, err := loadOrCreateFederationSigningKey(directory)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := stageFederationKeyRotation(directory)
	if err != nil {
		t.Fatal(err)
	}
	transaction := func(ctx context.Context, db *sql.DB, apply func(sqliteExecutor) error) error {
		return withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
			if err := apply(tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO rotation_commit_child(parent_id) VALUES('missing-parent')`)
			return err
		})
	}
	key, rotationErr := rotateFederationSigningKeyForActorWithTransaction(context.Background(), db, "owner", directory, "activate", nil, transaction)
	if !errors.Is(rotationErr, errFederationRotationOutcomeUnknown) || key.ID != pending.ID {
		t.Fatalf("rotation key=%s err=%v, want intended key and indeterminate result", key.ID, rotationErr)
	}
	current, err := readFederationSigningKey(filepath.Join(directory, federationPrivateKeyFile))
	if err != nil || current.ID != pending.ID || string(current.PrivateKey) != string(pending.PrivateKey) {
		t.Fatalf("active key bytes differ from intended pending key: id=%s err=%v", current.ID, err)
	}
	if current.ID == active.ID {
		t.Fatal("active key was not changed by the injected finalization-failure case")
	}
	if _, err := os.Lstat(federationPendingKeyPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending file after activation=%v, want absent", err)
	}

	request := requestForUser(http.MethodPost, "/api/admin/federation/identity/rotation", "owner")
	response := httptest.NewRecorder()
	if !writeFederationRotationError(response, request, db, rotationErr) || response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"error\":\"federation_key_rotation_outcome_unknown\"}\n" {
		t.Fatalf("unknown result API response: status=%d body=%s", response.Code, response.Body)
	}
	var auditStatus int
	if err := db.QueryRow(`SELECT response_status FROM audit_events WHERE actor_id='owner' AND action='POST /admin/federation/identity/rotation' ORDER BY created_at DESC LIMIT 1`).Scan(&auditStatus); err != nil || auditStatus != http.StatusServiceUnavailable {
		t.Fatalf("unknown-outcome attempt audit status=%d err=%v", auditStatus, err)
	}

	api := apiHandler{db: db, federationKeyDir: directory}
	identity := httptest.NewRecorder()
	api.ServeHTTP(identity, requestForUser(http.MethodGet, "/api/admin/federation/identity", "owner"))
	var observed struct {
		Fingerprint        string `json:"key_fingerprint"`
		PendingFingerprint string `json:"pending_key_fingerprint"`
	}
	if identity.Code != http.StatusOK || json.Unmarshal(identity.Body.Bytes(), &observed) != nil || observed.Fingerprint != federationKeyFingerprint(pending) || observed.PendingFingerprint != "" {
		t.Fatalf("identity inspection status=%d body=%s observed=%+v", identity.Code, identity.Body, observed)
	}
}
