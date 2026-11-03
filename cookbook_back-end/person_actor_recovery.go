package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	personActorRecoveryManifestVersion = 1
	maxPersonActorRecoveryManifest     = 32 << 20
	maxPersonActorRecoveryEntries      = 10000
	maxPersonActorRecoveryURIBytes     = 2048
	maxPersonActorRecoveryTimeBytes    = 64
	maxPersonActorRecoveryUserIDBytes  = 1024
	maxPersonActorRecoveryPrivateBytes = 4096
	maxPersonActorRecoveryJSONDepth    = 8
)

var (
	ErrPersonActorRecoveryManifest = errors.New("invalid Person actor recovery manifest")
	ErrPersonActorRecoveryDatabase = errors.New("Person actor recovery database is unavailable")
	ErrPersonActorRecoverySchema   = errors.New("Person actor recovery schema is incomplete")
	ErrPersonActorRecoveryState    = errors.New("Person actor recovery state is invalid")
	ErrPersonActorRecoveryMismatch = errors.New("Person actor recovery manifest does not match the database")
	ErrPersonActorRecoveryKey      = errors.New("Person actor recovery key verification failed")
)

// personActorRecoveryManifest contains only safe public identity metadata.
// Retired actor records carry a null fingerprint because their keys are erased.
type personActorRecoveryManifest struct {
	Version int                         `json:"version"`
	Actors  []personActorRecoveryRecord `json:"actors"`
}

type personActorRecoveryRecord struct {
	ActorID         string  `json:"actor_id"`
	ActorURI        string  `json:"actor_uri"`
	State           string  `json:"state"`
	PublicKeySHA256 *string `json:"public_key_sha256"`
}

type personActorRecoveryBinding struct {
	actorID       string
	userID        string
	userFound     bool
	publicKey     []byte
	privateKeyLen int
}

// describePersonActorRecovery reads one SQLite snapshot and validates every
// active private key with the ordinary identity key loader. The database must
// already be open read-only; no migration or repair is attempted here.
func describePersonActorRecovery(ctx context.Context, db *sql.DB) (personActorRecoveryManifest, error) {
	manifest := personActorRecoveryManifest{Version: personActorRecoveryManifestVersion, Actors: make([]personActorRecoveryRecord, 0)}
	if db == nil {
		return manifest, ErrPersonActorRecoveryDatabase
	}

	// A transaction pins the registry/binding view, including committed WAL
	// pages. The shared key loader uses *sql.DB, so allow a second read-only
	// connection and compare its URI/public key back to this pinned snapshot.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return manifest, ErrPersonActorRecoveryDatabase
	}
	defer tx.Rollback()

	hasRegistry, hasBindings, hasUsers, err := personActorRecoveryTables(ctx, tx)
	if err != nil {
		if errors.Is(err, ErrPersonActorRecoverySchema) {
			return manifest, ErrPersonActorRecoverySchema
		}
		return manifest, ErrPersonActorRecoveryDatabase
	}
	version, hasLedger, err := personActorRecoverySchemaVersion(ctx, tx)
	if err != nil {
		return manifest, err
	}
	hasActorSchema := hasRegistry && hasBindings
	if hasRegistry != hasBindings || !hasUsers {
		return manifest, ErrPersonActorRecoverySchema
	}
	if hasLedger {
		if (version >= 5) != hasActorSchema {
			return manifest, ErrPersonActorRecoverySchema
		}
	} else {
		// A pre-ledger backup is accepted only when its remaining tables match
		// a recognized legacy Cookbook schema. A missing ledger cannot prove
		// that an archive was not rewritten; it only preserves old-backup support.
		if hasActorSchema || validateUnversionedSchema(ctx, tx) != nil {
			return manifest, ErrPersonActorRecoverySchema
		}
	}
	if !hasRegistry && !hasBindings {
		return manifest, nil
	}

	registry, err := readPersonActorRecoveryRegistry(ctx, tx)
	if err != nil {
		return manifest, err
	}
	bindings, err := readPersonActorRecoveryBindings(ctx, tx)
	if err != nil {
		return manifest, err
	}

	seenURIs := make(map[string]struct{}, len(registry))
	seenUsers := make(map[string]struct{}, len(bindings))
	registryIDs := make(map[string]struct{}, len(registry))
	for _, actor := range registry {
		registryIDs[actor.ActorID] = struct{}{}
	}
	for actorID := range bindings {
		if !validPersonActorRecoveryID(actorID) {
			return manifest, ErrPersonActorRecoveryState
		}
		if _, exists := registryIDs[actorID]; !exists {
			return manifest, ErrPersonActorRecoveryState
		}
	}
	for _, actor := range registry {
		if _, duplicate := seenURIs[actor.ActorURI]; duplicate {
			return manifest, ErrPersonActorRecoveryState
		}
		seenURIs[actor.ActorURI] = struct{}{}

		binding, hasBinding := bindings[actor.ActorID]
		if actor.State == "retired" {
			if hasBinding {
				return manifest, ErrPersonActorRecoveryState
			}
			manifest.Actors = append(manifest.Actors, actor)
			continue
		}
		if !hasBinding || !binding.userFound || binding.userID == "" || len(binding.publicKey) != 32 {
			return manifest, ErrPersonActorRecoveryState
		}
		if _, duplicate := seenUsers[binding.userID]; duplicate {
			return manifest, ErrPersonActorRecoveryState
		}
		seenUsers[binding.userID] = struct{}{}

		key, err := loadPersonActorKey(ctx, db, actor.ActorID)
		if err != nil {
			return manifest, ErrPersonActorRecoveryKey
		}
		if key.URI != actor.ActorURI || !bytes.Equal(key.PublicKey, binding.publicKey) {
			clearPersonActorKey(&key)
			return manifest, ErrPersonActorRecoveryKey
		}
		sum := sha256.Sum256(key.PublicKey)
		fingerprint := hex.EncodeToString(sum[:])
		clearPersonActorKey(&key)
		actor.PublicKeySHA256 = &fingerprint
		manifest.Actors = append(manifest.Actors, actor)
	}
	if err := validatePersonActorRecoveryManifest(manifest); err != nil {
		return personActorRecoveryManifest{}, ErrPersonActorRecoveryState
	}
	return manifest, nil
}

func personActorRecoverySchemaVersion(ctx context.Context, tx *sql.Tx) (int, bool, error) {
	hasLedger, err := migrationLedgerExists(ctx, tx)
	if err != nil {
		return 0, false, ErrPersonActorRecoverySchema
	}
	if !hasLedger {
		return 0, false, nil
	}
	version, err := readAndValidateMigrationLedger(ctx, tx, registeredSchemaMigrations())
	if err != nil {
		return 0, true, ErrPersonActorRecoverySchema
	}
	return version, true, nil
}

func verifyPersonActorRecoveryManifest(ctx context.Context, db *sql.DB, expected personActorRecoveryManifest) error {
	if err := validatePersonActorRecoveryManifest(expected); err != nil {
		return err
	}
	actual, err := describePersonActorRecovery(ctx, db)
	if err != nil {
		return err
	}
	if len(actual.Actors) != len(expected.Actors) {
		return ErrPersonActorRecoveryMismatch
	}
	for index := range actual.Actors {
		if actual.Actors[index].ActorID != expected.Actors[index].ActorID ||
			actual.Actors[index].ActorURI != expected.Actors[index].ActorURI ||
			actual.Actors[index].State != expected.Actors[index].State ||
			!samePersonActorFingerprint(actual.Actors[index].PublicKeySHA256, expected.Actors[index].PublicKeySHA256) {
			return ErrPersonActorRecoveryMismatch
		}
	}
	return nil
}

func samePersonActorFingerprint(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func personActorRecoveryTables(ctx context.Context, tx *sql.Tx) (bool, bool, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,type FROM sqlite_master WHERE name IN ('person_actor_ids','person_actors','users')`)
	if err != nil {
		return false, false, false, err
	}
	defer rows.Close()
	var registry, bindings, users bool
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return false, false, false, err
		}
		if kind != "table" {
			return false, false, false, ErrPersonActorRecoverySchema
		}
		switch name {
		case "person_actor_ids":
			registry = true
		case "person_actors":
			bindings = true
		case "users":
			users = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, false, err
	}
	return registry, bindings, users, nil
}

func readPersonActorRecoveryRegistry(ctx context.Context, tx *sql.Tx) ([]personActorRecoveryRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
		substr(CAST(actor_id AS BLOB),1,33),length(CAST(actor_id AS BLOB)),
		substr(CAST(actor_uri AS BLOB),1,2049),length(CAST(actor_uri AS BLOB)),
		substr(CAST(state AS BLOB),1,8),length(CAST(state AS BLOB)),
		substr(CAST(created_at AS BLOB),1,65),length(CAST(created_at AS BLOB)),
		substr(CAST(retired_at AS BLOB),1,65),COALESCE(length(CAST(retired_at AS BLOB)),-1)
		FROM person_actor_ids ORDER BY actor_id`)
	if err != nil {
		return nil, ErrPersonActorRecoverySchema
	}
	defer rows.Close()
	actors := make([]personActorRecoveryRecord, 0)
	previousID := ""
	for rows.Next() {
		if len(actors) >= maxPersonActorRecoveryEntries {
			return nil, ErrPersonActorRecoveryState
		}
		var actorID, actorURI, state, createdAt, retiredAt []byte
		var actorIDLen, actorURILen, stateLen, createdAtLen, retiredAtLen int
		if err := rows.Scan(&actorID, &actorIDLen, &actorURI, &actorURILen, &state, &stateLen, &createdAt, &createdAtLen, &retiredAt, &retiredAtLen); err != nil {
			return nil, ErrPersonActorRecoveryState
		}
		stateText := string(state)
		if actorIDLen != 32 || len(actorID) != actorIDLen || actorURILen < 1 || actorURILen > maxPersonActorRecoveryURIBytes || len(actorURI) != actorURILen ||
			stateLen < 1 || stateLen > len("retired") || len(state) != stateLen || createdAtLen < 1 || createdAtLen > maxPersonActorRecoveryTimeBytes || len(createdAt) != createdAtLen ||
			(stateText == "active" && retiredAtLen != -1) || (stateText == "retired" && (retiredAtLen < 1 || retiredAtLen > maxPersonActorRecoveryTimeBytes || len(retiredAt) != retiredAtLen)) {
			return nil, ErrPersonActorRecoveryState
		}
		actor := personActorRecoveryRecord{ActorID: string(actorID), ActorURI: string(actorURI), State: string(state)}
		if !validPersonActorRecoveryID(actor.ActorID) || !validPersonActorURI(actor.ActorURI, actor.ActorID) ||
			(actor.State != "active" && actor.State != "retired") || (previousID != "" && actor.ActorID <= previousID) {
			return nil, ErrPersonActorRecoveryState
		}
		previousID = actor.ActorID
		actors = append(actors, actor)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrPersonActorRecoveryState
	}
	return actors, nil
}

func readPersonActorRecoveryBindings(ctx context.Context, tx *sql.Tx) (map[string]personActorRecoveryBinding, error) {
	rows, err := tx.QueryContext(ctx, `SELECT
		substr(CAST(actor.actor_id AS BLOB),1,33),length(CAST(actor.actor_id AS BLOB)),
		substr(CAST(actor.user_id AS BLOB),1,1025),length(CAST(actor.user_id AS BLOB)),
		length(actor.public_key),substr(actor.public_key,1,33),length(actor.private_key_pkcs8),
		EXISTS(SELECT 1 FROM users WHERE users.id=actor.user_id)
		FROM person_actors AS actor ORDER BY actor.actor_id`)
	if err != nil {
		return nil, ErrPersonActorRecoverySchema
	}
	defer rows.Close()
	bindings := make(map[string]personActorRecoveryBinding)
	for rows.Next() {
		if len(bindings) >= maxPersonActorRecoveryEntries {
			return nil, ErrPersonActorRecoveryState
		}
		var binding personActorRecoveryBinding
		var actorID, userID []byte
		var actorIDLen, userIDLen, publicKeyLen, userExists int
		if err := rows.Scan(&actorID, &actorIDLen, &userID, &userIDLen, &publicKeyLen, &binding.publicKey, &binding.privateKeyLen, &userExists); err != nil {
			return nil, ErrPersonActorRecoveryState
		}
		binding.actorID = string(actorID)
		binding.userID = string(userID)
		if actorIDLen != 32 || len(actorID) != actorIDLen || userIDLen < 1 || userIDLen > maxPersonActorRecoveryUserIDBytes || len(userID) != userIDLen ||
			publicKeyLen != 32 || len(binding.publicKey) != 32 || binding.privateKeyLen < 1 || binding.privateKeyLen > maxPersonActorRecoveryPrivateBytes {
			return nil, ErrPersonActorRecoveryState
		}
		if _, exists := bindings[binding.actorID]; exists {
			return nil, ErrPersonActorRecoveryState
		}
		binding.userFound = userExists == 1
		binding.publicKey = append([]byte(nil), binding.publicKey...)
		bindings[binding.actorID] = binding
	}
	if err := rows.Err(); err != nil {
		return nil, ErrPersonActorRecoveryState
	}
	return bindings, nil
}

func validPersonActorRecoveryID(actorID string) bool {
	if len(actorID) != 32 || actorID != strings.ToLower(actorID) {
		return false
	}
	decoded, err := hex.DecodeString(actorID)
	return err == nil && len(decoded) == 16
}

func clearPersonActorKey(key *personActorKey) {
	for index := range key.PrivateKey {
		key.PrivateKey[index] = 0
	}
	for index := range key.PublicKey {
		key.PublicKey[index] = 0
	}
}

func readPersonActorRecoveryManifest(reader io.Reader) (personActorRecoveryManifest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxPersonActorRecoveryManifest+1))
	if err != nil || len(data) > maxPersonActorRecoveryManifest {
		return personActorRecoveryManifest{}, ErrPersonActorRecoveryManifest
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return personActorRecoveryManifest{}, ErrPersonActorRecoveryManifest
	}
	var envelope struct {
		Version json.RawMessage `json:"version"`
		Actors  json.RawMessage `json:"actors"`
	}
	if err := decodeRecoveryJSON(data, &envelope); err != nil || len(envelope.Version) == 0 || len(envelope.Actors) == 0 {
		return personActorRecoveryManifest{}, ErrPersonActorRecoveryManifest
	}
	var version int
	if err := json.Unmarshal(envelope.Version, &version); err != nil || version != personActorRecoveryManifestVersion {
		return personActorRecoveryManifest{}, ErrPersonActorRecoveryManifest
	}
	var actorItems []json.RawMessage
	if err := json.Unmarshal(envelope.Actors, &actorItems); err != nil || actorItems == nil || len(actorItems) > maxPersonActorRecoveryEntries {
		return personActorRecoveryManifest{}, ErrPersonActorRecoveryManifest
	}
	manifest := personActorRecoveryManifest{Version: version, Actors: make([]personActorRecoveryRecord, 0, len(actorItems))}
	for _, item := range actorItems {
		var wire struct {
			ActorID         *string         `json:"actor_id"`
			ActorURI        *string         `json:"actor_uri"`
			State           *string         `json:"state"`
			PublicKeySHA256 json.RawMessage `json:"public_key_sha256"`
		}
		if err := decodeRecoveryJSON(item, &wire); err != nil || wire.ActorID == nil || wire.ActorURI == nil || wire.State == nil || len(wire.PublicKeySHA256) == 0 {
			return personActorRecoveryManifest{}, ErrPersonActorRecoveryManifest
		}
		var fingerprint *string
		if !bytes.Equal(bytes.TrimSpace(wire.PublicKeySHA256), []byte("null")) {
			var value string
			if err := json.Unmarshal(wire.PublicKeySHA256, &value); err != nil {
				return personActorRecoveryManifest{}, ErrPersonActorRecoveryManifest
			}
			fingerprint = &value
		}
		manifest.Actors = append(manifest.Actors, personActorRecoveryRecord{ActorID: *wire.ActorID, ActorURI: *wire.ActorURI, State: *wire.State, PublicKeySHA256: fingerprint})
	}
	if err := validatePersonActorRecoveryManifest(manifest); err != nil {
		return personActorRecoveryManifest{}, err
	}
	return manifest, nil
}

func decodeRecoveryJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrPersonActorRecoveryManifest
	}
	return nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var readValue func(int) error
	readValue = func(depth int) error {
		if depth > maxPersonActorRecoveryJSONDepth {
			return ErrPersonActorRecoveryManifest
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delim {
		case '{':
			if !recoveryJSONObjectAllowedAtDepth(depth) {
				return ErrPersonActorRecoveryManifest
			}
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return ErrPersonActorRecoveryManifest
				}
				if !recoveryJSONFieldAllowed(depth, key) {
					return ErrPersonActorRecoveryManifest
				}
				if _, duplicate := seen[key]; duplicate {
					return ErrPersonActorRecoveryManifest
				}
				seen[key] = struct{}{}
				if err := readValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return ErrPersonActorRecoveryManifest
			}
		case '[':
			for decoder.More() {
				if err := readValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return ErrPersonActorRecoveryManifest
			}
		default:
			return ErrPersonActorRecoveryManifest
		}
		return nil
	}
	if err := readValue(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrPersonActorRecoveryManifest
	}
	return nil
}

func recoveryJSONObjectAllowedAtDepth(depth int) bool {
	return depth == 0 || depth == 2
}

func recoveryJSONFieldAllowed(depth int, key string) bool {
	if depth == 0 {
		return key == "version" || key == "actors"
	}
	if depth == 2 {
		switch key {
		case "actor_id", "actor_uri", "state", "public_key_sha256":
			return true
		}
	}
	return false
}

func validatePersonActorRecoveryManifest(manifest personActorRecoveryManifest) error {
	if manifest.Version != personActorRecoveryManifestVersion || manifest.Actors == nil || len(manifest.Actors) > maxPersonActorRecoveryEntries {
		return ErrPersonActorRecoveryManifest
	}
	seenURIs := make(map[string]struct{}, len(manifest.Actors))
	previousID := ""
	for _, actor := range manifest.Actors {
		if !validPersonActorRecoveryID(actor.ActorID) || actor.ActorURI == "" || len(actor.ActorURI) > maxPersonActorRecoveryURIBytes || !validPersonActorURI(actor.ActorURI, actor.ActorID) ||
			(actor.State != "active" && actor.State != "retired") || (previousID != "" && actor.ActorID <= previousID) {
			return ErrPersonActorRecoveryManifest
		}
		previousID = actor.ActorID
		if _, duplicate := seenURIs[actor.ActorURI]; duplicate {
			return ErrPersonActorRecoveryManifest
		}
		seenURIs[actor.ActorURI] = struct{}{}
		if actor.State == "active" {
			if actor.PublicKeySHA256 == nil || !validPersonActorRecoveryFingerprint(*actor.PublicKeySHA256) {
				return ErrPersonActorRecoveryManifest
			}
		} else if actor.PublicKeySHA256 != nil {
			return ErrPersonActorRecoveryManifest
		}
	}
	return nil
}

func validPersonActorRecoveryFingerprint(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func parsePersonActorRecoveryCLIFlags(args []string, needsManifest bool) (databasePath, manifestPath string, err error) {
	for index := 0; index < len(args); index++ {
		if index+1 >= len(args) {
			return "", "", fmt.Errorf("%s requires a value", args[index])
		}
		value := args[index+1]
		switch args[index] {
		case "--database":
			if databasePath != "" || strings.TrimSpace(value) == "" {
				return "", "", errors.New("exactly one nonempty --database is required")
			}
			databasePath = value
		case "--manifest":
			if manifestPath != "" || strings.TrimSpace(value) == "" {
				return "", "", errors.New("exactly one nonempty --manifest is required")
			}
			manifestPath = value
		default:
			return "", "", fmt.Errorf("unknown recovery option %q", args[index])
		}
		index++
	}
	if databasePath == "" {
		return "", "", errors.New("an explicit --database path is required")
	}
	if needsManifest && manifestPath == "" {
		return "", "", errors.New("an explicit --manifest path or - is required")
	}
	if !needsManifest && manifestPath != "" {
		return "", "", errors.New("--manifest is only valid for recovery verify")
	}
	return databasePath, manifestPath, nil
}

func runPersonActorRecoveryCLI(args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 1 && args[0] == "--help" {
		_, _ = fmt.Fprintln(output, personActorRecoveryCLIUsage)
		return true, nil
	}
	if len(args) == 0 || (args[0] != "describe" && args[0] != "verify") {
		_, _ = fmt.Fprintln(output, personActorRecoveryCLIUsage)
		return true, errors.New("expected recovery describe or verify")
	}
	command := args[0]
	databasePath, manifestPath, err := parsePersonActorRecoveryCLIFlags(args[1:], command == "verify")
	if err != nil {
		return true, err
	}
	var expected personActorRecoveryManifest
	if command == "verify" {
		var reader io.Reader = input
		if manifestPath != "-" {
			file, err := os.Open(manifestPath)
			if err != nil {
				return true, ErrPersonActorRecoveryManifest
			}
			defer file.Close()
			reader = file
		}
		expected, err = readPersonActorRecoveryManifest(reader)
		if err != nil {
			return true, err
		}
	}
	db, err := openAccountLookupDatabase(databasePath)
	if err != nil {
		return true, ErrPersonActorRecoveryDatabase
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if command == "describe" {
		manifest, err := describePersonActorRecovery(ctx, db)
		if err != nil {
			return true, err
		}
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(manifest); err != nil {
			return true, errors.New("could not write Person actor recovery manifest")
		}
		return true, nil
	}
	if err := verifyPersonActorRecoveryManifest(ctx, db, expected); err != nil {
		return true, err
	}
	_, err = fmt.Fprintf(output, "Verified Person actor recovery manifest v%d (%d actors).\n", expected.Version, len(expected.Actors))
	return true, err
}

const personActorRecoveryCLIUsage = `Usage:
  cookbook-backend instance recovery describe --database <path>
  cookbook-backend instance recovery verify --database <path> --manifest <path|->

Reads an explicit SQLite file and never applies migrations. The database must
include its matching WAL sidecar when it contains uncheckpointed commits.`
