package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPersonActorRecoveryDescribesAndVerifiesCompleteActiveAndRetiredSet(t *testing.T) {
	db := personActorRecoveryTestDB(t)
	setPersonActorOrigin(t, db, personActorTestOrigin)
	active, err := CreatePersonActor(context.Background(), db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	retired, err := CreatePersonActor(context.Background(), db, "person-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := RetirePersonActor(context.Background(), db, "person-b"); err != nil {
		t.Fatal(err)
	}

	manifest, err := describePersonActorRecovery(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != personActorRecoveryManifestVersion || len(manifest.Actors) != 2 {
		t.Fatalf("manifest header version=%d actors=%d", manifest.Version, len(manifest.Actors))
	}
	if !sort.SliceIsSorted(manifest.Actors, func(i, j int) bool { return manifest.Actors[i].ActorID < manifest.Actors[j].ActorID }) {
		t.Fatal("manifest actors are not sorted by actor_id")
	}
	var gotActive, gotRetired bool
	for _, actor := range manifest.Actors {
		switch actor.ActorID {
		case active.ID:
			sum := sha256.Sum256(active.PublicKey)
			want := hex.EncodeToString(sum[:])
			if actor.State != "active" || actor.ActorURI != active.URI || actor.PublicKeySHA256 == nil || *actor.PublicKeySHA256 != want {
				t.Fatalf("active record=%+v", actor)
			}
			gotActive = true
		case retired.ID:
			if actor.State != "retired" || actor.ActorURI != retired.URI || actor.PublicKeySHA256 != nil {
				t.Fatalf("retired record=%+v, retired keys must have null fingerprint", actor)
			}
			gotRetired = true
		default:
			t.Fatalf("unexpected actor in recovery manifest: %+v", actor)
		}
	}
	if !gotActive || !gotRetired {
		t.Fatalf("manifest active=%v retired=%v", gotActive, gotRetired)
	}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, manifest); err != nil {
		t.Fatalf("verify exact manifest: %v", err)
	}

	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := readPersonActorRecoveryManifest(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode described manifest: %v", err)
	}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, decoded); err != nil {
		t.Fatalf("verify decoded manifest: %v", err)
	}
}

func TestPersonActorRecoveryRejectsIncompleteAndMismatchedManifestSets(t *testing.T) {
	db := personActorRecoveryTestDB(t)
	setPersonActorOrigin(t, db, personActorTestOrigin)
	first, err := CreatePersonActor(context.Background(), db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreatePersonActor(context.Background(), db, "person-b")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := describePersonActorRecovery(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, personActorRecoveryManifest{Version: 1, Actors: manifest.Actors[:1]}); !errors.Is(err, ErrPersonActorRecoveryMismatch) {
		t.Fatalf("missing actor error=%v", err)
	}

	wrongFingerprint := clonePersonActorRecoveryManifest(manifest)
	for i := range wrongFingerprint.Actors {
		if wrongFingerprint.Actors[i].ActorID == first.ID {
			wrong := strings.Repeat("0", sha256.Size*2)
			wrongFingerprint.Actors[i].PublicKeySHA256 = &wrong
		}
	}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, wrongFingerprint); !errors.Is(err, ErrPersonActorRecoveryMismatch) {
		t.Fatalf("fingerprint mismatch error=%v", err)
	}

	wrongURI := clonePersonActorRecoveryManifest(manifest)
	for i := range wrongURI.Actors {
		if wrongURI.Actors[i].ActorID == first.ID {
			wrongURI.Actors[i].ActorURI = "https://different.example.org/actors/" + first.ID
		}
	}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, wrongURI); !errors.Is(err, ErrPersonActorRecoveryMismatch) {
		t.Fatalf("actor URI mismatch error=%v", err)
	}

	wrongState := clonePersonActorRecoveryManifest(manifest)
	for i := range wrongState.Actors {
		if wrongState.Actors[i].ActorID == first.ID {
			wrongState.Actors[i].State = "retired"
			wrongState.Actors[i].PublicKeySHA256 = nil
		}
	}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, wrongState); !errors.Is(err, ErrPersonActorRecoveryMismatch) {
		t.Fatalf("actor state mismatch error=%v", err)
	}

	extra := personActorRecoveryRecord{ActorID: "ffffffffffffffffffffffffffffffff", ActorURI: personActorTestOrigin + "/actors/ffffffffffffffffffffffffffffffff", State: "retired"}
	withExtra := clonePersonActorRecoveryManifest(manifest)
	withExtra.Actors = append(withExtra.Actors, extra)
	sort.Slice(withExtra.Actors, func(i, j int) bool { return withExtra.Actors[i].ActorID < withExtra.Actors[j].ActorID })
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, withExtra); !errors.Is(err, ErrPersonActorRecoveryMismatch) {
		t.Fatalf("unlisted actor error=%v", err)
	}

	duplicate := clonePersonActorRecoveryManifest(manifest)
	duplicate.Actors = append(duplicate.Actors, duplicate.Actors[0])
	sort.Slice(duplicate.Actors, func(i, j int) bool { return duplicate.Actors[i].ActorID < duplicate.Actors[j].ActorID })
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, duplicate); !errors.Is(err, ErrPersonActorRecoveryManifest) {
		t.Fatalf("duplicate actor error=%v", err)
	}
	if first.ID == second.ID {
		t.Fatal("test actors unexpectedly share an ID")
	}
}

func TestPersonActorRecoveryRejectsCorruptKeysAndLifecycleBindings(t *testing.T) {
	t.Run("malformed private key", func(t *testing.T) {
		db := personActorRecoveryTestDB(t)
		setPersonActorOrigin(t, db, personActorTestOrigin)
		actor, err := CreatePersonActor(context.Background(), db, "person-a")
		if err != nil {
			t.Fatal(err)
		}
		var privateDER []byte
		if err := db.QueryRow(`SELECT private_key_pkcs8 FROM person_actors WHERE actor_id=?`, actor.ID).Scan(&privateDER); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE person_actors SET private_key_pkcs8=x'00' WHERE actor_id=?`, actor.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := describePersonActorRecovery(context.Background(), db); !errors.Is(err, ErrPersonActorRecoveryKey) || strings.Contains(err.Error(), hex.EncodeToString(privateDER)) {
			t.Fatalf("corrupt key error=%v", err)
		}
	})

	t.Run("public key mismatch", func(t *testing.T) {
		db := personActorRecoveryTestDB(t)
		setPersonActorOrigin(t, db, personActorTestOrigin)
		actor, err := CreatePersonActor(context.Background(), db, "person-a")
		if err != nil {
			t.Fatal(err)
		}
		other, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE person_actors SET public_key=? WHERE actor_id=?`, []byte(other), actor.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := describePersonActorRecovery(context.Background(), db); !errors.Is(err, ErrPersonActorRecoveryKey) {
			t.Fatalf("mismatched key error=%v", err)
		}
	})

	t.Run("oversized private key material", func(t *testing.T) {
		db := personActorRecoveryTestDB(t)
		setPersonActorOrigin(t, db, personActorTestOrigin)
		actor, err := CreatePersonActor(context.Background(), db, "person-a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE person_actors SET private_key_pkcs8=zeroblob(?) WHERE actor_id=?`, maxPersonActorRecoveryPrivateBytes+1, actor.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := describePersonActorRecovery(context.Background(), db); !errors.Is(err, ErrPersonActorRecoveryState) {
			t.Fatalf("oversized private-key data error=%v", err)
		}
	})

	t.Run("missing active binding", func(t *testing.T) {
		db := personActorRecoveryTestDB(t)
		setPersonActorOrigin(t, db, personActorTestOrigin)
		actor, err := CreatePersonActor(context.Background(), db, "person-a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TRIGGER person_actor_binding_delete_requires_retirement`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM person_actors WHERE actor_id=?`, actor.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := describePersonActorRecovery(context.Background(), db); !errors.Is(err, ErrPersonActorRecoveryState) {
			t.Fatalf("missing active binding error=%v", err)
		}
	})

	t.Run("retired actor retains binding", func(t *testing.T) {
		db := personActorRecoveryTestDB(t)
		setPersonActorOrigin(t, db, personActorTestOrigin)
		actor, err := CreatePersonActor(context.Background(), db, "person-b")
		if err != nil {
			t.Fatal(err)
		}
		var publicKey, privateDER []byte
		if err := db.QueryRow(`SELECT public_key,private_key_pkcs8 FROM person_actors WHERE actor_id=?`, actor.ID).Scan(&publicKey, &privateDER); err != nil {
			t.Fatal(err)
		}
		if err := RetirePersonActor(context.Background(), db, "person-b"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TRIGGER person_actor_binding_insert_active`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO person_actors(actor_id,user_id,public_key,private_key_pkcs8,created_at,key_rotated_at) VALUES(?,?,?,?,?,?)`, actor.ID, "person-b", publicKey, privateDER, "now", "now"); err != nil {
			t.Fatal(err)
		}
		if _, err := describePersonActorRecovery(context.Background(), db); !errors.Is(err, ErrPersonActorRecoveryState) {
			t.Fatalf("retired binding error=%v", err)
		}
	})
}

func TestPersonActorRecoveryAcceptsRecognizedPreV5SchemasAndRejectsDamagedV5(t *testing.T) {
	t.Run("pre-ledger legacy schema", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pre-ledger.db")
		legacy, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.Exec(schema); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
		if err := legacy.Close(); err != nil {
			t.Fatal(err)
		}
		verifyEmptyPreV5RecoveryManifest(t, path)
	})

	t.Run("ledgered v4 schema", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "v4.db")
		legacy, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if err := runSchemaMigrations(context.Background(), legacy, registeredSchemaMigrations()[:4]); err != nil {
			legacy.Close()
			t.Fatalf("create valid v4 database: %v", err)
		}
		if err := legacy.Close(); err != nil {
			t.Fatal(err)
		}
		verifyEmptyPreV5RecoveryManifest(t, path)
	})

	t.Run("v5 ledger with missing actor tables", func(t *testing.T) {
		db := personActorRecoveryTestDB(t)
		if _, err := db.Exec(`DROP TABLE person_actors; DROP TABLE person_actor_ids`); err != nil {
			t.Fatal(err)
		}
		if _, err := describePersonActorRecovery(context.Background(), db); !errors.Is(err, ErrPersonActorRecoverySchema) {
			t.Fatalf("damaged v5 schema error=%v", err)
		}
	})

	partialPath := filepath.Join(t.TempDir(), "partial.db")
	partial, err := sql.Open("sqlite", partialPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Exec(`CREATE TABLE person_actor_ids(actor_id TEXT)`); err != nil {
		partial.Close()
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	partialDB, err := openAccountLookupDatabase(partialPath)
	if err != nil {
		t.Fatal(err)
	}
	defer partialDB.Close()
	if _, err := describePersonActorRecovery(context.Background(), partialDB); !errors.Is(err, ErrPersonActorRecoverySchema) {
		t.Fatalf("partial schema error=%v", err)
	}
}

func verifyEmptyPreV5RecoveryManifest(t *testing.T, path string) {
	t.Helper()
	db, err := openAccountLookupDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manifest, err := describePersonActorRecovery(context.Background(), db)
	if err != nil || len(manifest.Actors) != 0 || manifest.Version != personActorRecoveryManifestVersion {
		t.Fatalf("pre-v5 empty manifest=%+v err=%v", manifest, err)
	}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, manifest); err != nil {
		t.Fatalf("verify pre-v5 empty manifest: %v", err)
	}
	manifest.Actors = []personActorRecoveryRecord{{ActorID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ActorURI: personActorTestOrigin + "/actors/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "retired"}}
	if err := verifyPersonActorRecoveryManifest(context.Background(), db, manifest); !errors.Is(err, ErrPersonActorRecoveryMismatch) {
		t.Fatalf("pre-v5 nonempty manifest error=%v", err)
	}
}

func TestPersonActorRecoveryRejectsEqualCountOrphanBinding(t *testing.T) {
	db := personActorRecoveryTestDB(t)
	setPersonActorOrigin(t, db, personActorTestOrigin)
	actor, err := CreatePersonActor(context.Background(), db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := RetirePersonActor(context.Background(), db, "person-a"); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `DROP TRIGGER person_actor_binding_insert_active`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO person_actors(actor_id,user_id,public_key,private_key_pkcs8,created_at,key_rotated_at) VALUES(?, 'person-b', zeroblob(32), x'00', 'now', 'now')`, "ffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatal(err)
	}
	var registryCount, bindingCount int
	if err := conn.QueryRowContext(context.Background(), `SELECT (SELECT COUNT(*) FROM person_actor_ids), (SELECT COUNT(*) FROM person_actors)`).Scan(&registryCount, &bindingCount); err != nil {
		t.Fatal(err)
	}
	if registryCount != 1 || bindingCount != registryCount {
		t.Fatalf("corruption fixture registry=%d bindings=%d; want equal counts of one", registryCount, bindingCount)
	}
	if _, err := describePersonActorRecovery(context.Background(), db); !errors.Is(err, ErrPersonActorRecoveryState) {
		t.Fatalf("equal-count orphan binding error=%v (actor %s)", err, actor.ID)
	}
}

func TestPersonActorRecoveryReadOnlyWALSnapshotPreservesDatabaseAndWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	db := personActorTestDB(t, path)
	setPersonActorOrigin(t, db, personActorTestOrigin)
	if _, err := CreatePersonActor(context.Background(), db, "person-a"); err != nil {
		t.Fatal(err)
	}
	walPath := path + "-wal"
	if info, err := os.Stat(walPath); err != nil || info.Size() == 0 {
		t.Fatalf("test did not produce an active WAL file: info=%v err=%v", info, err)
	}
	databaseBefore := fileSHA256(t, path)
	walBefore := fileSHA256(t, walPath)
	readOnly, err := openAccountLookupDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := describePersonActorRecovery(context.Background(), readOnly)
	if err != nil || len(manifest.Actors) != 1 || manifest.Actors[0].State != "active" {
		t.Fatalf("read-only WAL manifest=%+v err=%v", manifest, err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fileSHA256(t, path); got != databaseBefore {
		t.Fatalf("read-only verify changed database bytes: before=%s after=%s", databaseBefore, got)
	}
	if got := fileSHA256(t, walPath); got != walBefore {
		t.Fatalf("read-only verify changed WAL bytes: before=%s after=%s", walBefore, got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPersonActorRecoveryColdCopyCanBeReopenedWithoutChangingDatabase(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	source := personActorTestDB(t, sourcePath)
	setPersonActorOrigin(t, source, personActorTestOrigin)
	actor, err := CreatePersonActor(context.Background(), source, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := describePersonActorRecovery(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	coldPath := filepath.Join(t.TempDir(), "cold-copy.db")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coldPath, contents, 0600); err != nil {
		t.Fatal(err)
	}
	before := fileSHA256(t, coldPath)
	for attempt := 0; attempt < 2; attempt++ {
		cold, err := openAccountLookupDatabase(coldPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyPersonActorRecoveryManifest(context.Background(), cold, manifest); err != nil {
			cold.Close()
			t.Fatalf("cold-copy verify attempt %d: %v", attempt+1, err)
		}
		if err := cold.Close(); err != nil {
			t.Fatal(err)
		}
		if got := fileSHA256(t, coldPath); got != before {
			t.Fatalf("cold-copy verification changed database bytes: before=%s after=%s", before, got)
		}
	}
	if manifest.Actors[0].ActorID != actor.ID {
		t.Fatal("cold-copy manifest lost actor identity")
	}
}

func TestPersonActorRecoveryManifestRejectsUnknownDuplicateAndUnboundedInput(t *testing.T) {
	fingerprint := strings.Repeat("a", sha256.Size*2)
	validEntry := fmt.Sprintf(`{"actor_id":"%s","actor_uri":"%s/actors/%s","state":"active","public_key_sha256":"%s"}`, strings.Repeat("a", 32), personActorTestOrigin, strings.Repeat("a", 32), fingerprint)
	valid := `{"version":1,"actors":[` + validEntry + `]}`
	if _, err := readPersonActorRecoveryManifest(strings.NewReader(valid)); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	invalid := []string{
		strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"actors":[`, `"extra":true,"actors":[`, 1),
		strings.Replace(validEntry, `"state":"active"`, `"state":"active","extra":true`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(validEntry, `"state":"active"`, `"state":"active","state":"retired"`, 1),
		strings.Replace(validEntry, `"public_key_sha256":"`+fingerprint+`"`, `"public_key_sha256":null`, 1),
		strings.Replace(validEntry, `"public_key_sha256":"`+fingerprint+`"`, `"public_key_sha256":"`+strings.ToUpper(fingerprint)+`"`, 1),
		strings.Replace(validEntry, `,"public_key_sha256":"`+fingerprint+`"`, ``, 1),
		strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		`{"version":1,"actors":null}`,
		`{"version":1,"actors":[],"actors":[]}`,
		`{"version":1,"actors":[]}{}`,
	}
	for index, raw := range invalid {
		if _, err := readPersonActorRecoveryManifest(strings.NewReader(raw)); !errors.Is(err, ErrPersonActorRecoveryManifest) {
			t.Errorf("invalid manifest case %d error=%v", index+1, err)
		}
	}
	if _, err := readPersonActorRecoveryManifest(strings.NewReader(strings.Repeat(" ", maxPersonActorRecoveryManifest+1))); !errors.Is(err, ErrPersonActorRecoveryManifest) {
		t.Fatalf("oversized input error=%v", err)
	}
	tooMany := personActorRecoveryManifest{Version: 1, Actors: make([]personActorRecoveryRecord, maxPersonActorRecoveryEntries+1)}
	if err := validatePersonActorRecoveryManifest(tooMany); !errors.Is(err, ErrPersonActorRecoveryManifest) {
		t.Fatalf("oversized actor list error=%v", err)
	}
}

func TestPersonActorRecoveryCLIRequiresExplicitDatabaseAndRoundTripsManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.db")
	db := personActorTestDB(t, path)
	setPersonActorOrigin(t, db, personActorTestOrigin)
	actor, err := CreatePersonActor(context.Background(), db, "person-a")
	if err != nil {
		t.Fatal(err)
	}
	var privateDER []byte
	if err := db.QueryRow(`SELECT private_key_pkcs8 FROM person_actors WHERE actor_id=?`, actor.ID).Scan(&privateDER); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COOKBOOK_DATABASE", filepath.Join(t.TempDir(), "configured-live.db"))
	var output bytes.Buffer
	if handled, err := runInstanceOwnerCLI([]string{"instance", "recovery", "describe"}, strings.NewReader(""), &output); !handled || err == nil {
		t.Fatalf("describe without explicit DB handled=%v err=%v", handled, err)
	}
	if _, err := os.Stat(os.Getenv("COOKBOOK_DATABASE")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery CLI created or opened implicit configured DB: stat error=%v", err)
	}

	if handled, err := runInstanceOwnerCLI([]string{"instance", "recovery", "describe", "--database", path}, strings.NewReader(""), &output); !handled || err != nil {
		t.Fatalf("describe handled=%v err=%v", handled, err)
	}
	if bytes.Contains(output.Bytes(), privateDER) || strings.Contains(output.String(), hex.EncodeToString(privateDER)) {
		t.Fatal("describe output disclosed private key bytes")
	}
	manifest, err := readPersonActorRecoveryManifest(bytes.NewReader(output.Bytes()))
	if err != nil || len(manifest.Actors) != 1 || manifest.Actors[0].ActorID != actor.ID {
		t.Fatalf("CLI manifest=%+v err=%v output=%s", manifest, err, output.String())
	}

	var verifyOutput bytes.Buffer
	args := []string{"instance", "recovery", "verify", "--database", path, "--manifest", "-"}
	if handled, err := runInstanceOwnerCLI(args, bytes.NewReader(output.Bytes()), &verifyOutput); !handled || err != nil {
		t.Fatalf("verify handled=%v err=%v output=%q", handled, err, verifyOutput.String())
	}
	want := "Verified Person actor recovery manifest v1 (1 actors).\n"
	if verifyOutput.String() != want {
		t.Fatalf("verify output=%q, want %q", verifyOutput.String(), want)
	}
	manifestPath := filepath.Join(t.TempDir(), "actor-manifest.json")
	if err := os.WriteFile(manifestPath, output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var fileVerifyOutput bytes.Buffer
	fileArgs := []string{"instance", "recovery", "verify", "--database", path, "--manifest", manifestPath}
	if handled, err := runInstanceOwnerCLI(fileArgs, strings.NewReader(""), &fileVerifyOutput); !handled || err != nil || fileVerifyOutput.String() != want {
		t.Fatalf("file manifest verify handled=%v err=%v output=%q", handled, err, fileVerifyOutput.String())
	}
}

func clonePersonActorRecoveryManifest(manifest personActorRecoveryManifest) personActorRecoveryManifest {
	copy := personActorRecoveryManifest{Version: manifest.Version, Actors: make([]personActorRecoveryRecord, len(manifest.Actors))}
	for index, actor := range manifest.Actors {
		copy.Actors[index] = actor
		if actor.PublicKeySHA256 != nil {
			fingerprint := *actor.PublicKeySHA256
			copy.Actors[index].PublicKeySHA256 = &fingerprint
		}
	}
	return copy
}

func personActorRecoveryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return personActorTestDB(t, filepath.Join(t.TempDir(), "recovery.db"))
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}
