package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func migrationTestStep(version int, name string, apply func(context.Context, migrationExecutor) error) schemaMigration {
	return schemaMigration{
		version:  version,
		name:     name,
		checksum: migrationChecksum(name, "test-definition:"+name),
		apply:    apply,
	}
}

func migrationVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func latestSchemaVersion() int {
	migrations := registeredSchemaMigrations()
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}

func migrationObjectExists(t *testing.T, db *sql.DB, objectType, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, objectType, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func rawMigrationDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestFreshDatabaseRecordsAllRegisteredMigrations(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if latestSchemaVersion() != 16 {
		t.Fatalf("registered latest migration=%d, want v16 anonymous homepage preset", latestSchemaVersion())
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("migration version=%d, want latest %d", got, latestSchemaVersion())
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES('https://default-profile.example.org',0,'now','now')`); err != nil {
		t.Fatal(err)
	}
	var exchangeProfile string
	if err := db.QueryRow(`SELECT exchange_profile FROM federation_peers WHERE origin='https://default-profile.example.org'`).Scan(&exchangeProfile); err != nil || exchangeProfile != federationExchangeV1 {
		t.Fatalf("new peer profile=%q err=%v, want v1", exchangeProfile, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != latestSchemaVersion() {
		t.Fatalf("migration ledger rows=%d err=%v, want latest %d", count, err, latestSchemaVersion())
	}
	for version, name := range map[int]string{1: "legacy-baseline", 2: "instance-owner-authority", 3: "recipe-publication-policy", 4: "offline-passkey-enrollment", 5: "person-actor-identity", 6: "personal-library-bookmarks", 7: "creator-profile-household-foundation", 8: "personal-library-cookbook-bookmarks", 9: "library-remote-search-projection", 10: "personal-library-course-bookmarks", 11: "federation-resource-limits", 12: "federation-retention-receipts", 13: "private-remote-recipe-snapshots", 14: "course-creator-attribution", 15: "private-meal-menu-schema", 16: "anonymous-home-presentation-preset"} {
		var got string
		if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=?`, version).Scan(&got); err != nil || got != name {
			t.Fatalf("migration %d name=%q err=%v, want %q", version, got, err, name)
		}
	}
	if !migrationObjectExists(t, db, "table", "federation_remote_recipe_search") || !migrationObjectExists(t, db, "trigger", "federation_remote_recipe_search_invalidate_after_update") {
		t.Fatal("fresh migration omitted the remote recipe projection table or invalidation trigger")
	}
	if !migrationObjectExists(t, db, "table", "course_bookmarks") || !migrationObjectExists(t, db, "trigger", "course_bookmarks_account_limit") {
		t.Fatal("fresh migration omitted course bookmark table or account limit trigger")
	}
	if !migrationObjectExists(t, db, "table", "library_remote_snapshot_versions") || !migrationObjectExists(t, db, "table", "library_remote_snapshot_pins") {
		t.Fatal("fresh migration omitted private snapshot storage")
	}
	if !migrationObjectExists(t, db, "table", "meal_menus") || !migrationObjectExists(t, db, "table", "meal_menu_entries") {
		t.Fatal("fresh migration omitted private meal menu tables")
	}
	var snapshotLimit int64
	var snapshotPins int
	if err := db.QueryRow(`SELECT private_snapshot_storage_limit_bytes FROM instance_settings WHERE id=1`).Scan(&snapshotLimit); err != nil || snapshotLimit != defaultRemoteSnapshotStoreSize {
		t.Fatalf("private snapshot default=%d err=%v, want %d", snapshotLimit, err, defaultRemoteSnapshotStoreSize)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins`).Scan(&snapshotPins); err != nil || snapshotPins != 0 {
		t.Fatalf("fresh migration created %d private snapshot pins (err=%v), want zero", snapshotPins, err)
	}
	var receiptForeignKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('federation_retention_receipts')`).Scan(&receiptForeignKeys); err != nil || receiptForeignKeys != 0 {
		t.Fatalf("current retention receipts have %d foreign keys, err=%v; peer/key deletion must not cascade copied evidence", receiptForeignKeys, err)
	}
	var projections int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search`).Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("fresh projection rows=%d err=%v, want zero", projections, err)
	}
	var owner sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &generation); err != nil || owner.Valid || generation != 0 {
		t.Fatalf("fresh owner state=%v generation=%d err=%v, want unclaimed", owner, generation, err)
	}
}

func TestV12UpgradePreservesLegacyRemoteReferenceAndCacheWithoutSnapshotBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v12-private-snapshot-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:12]); err != nil {
		t.Fatal(err)
	}
	const origin = "https://legacy-reference.example.org"
	const recipeID = "0123456789abcdef0123456789abcdef"
	const objectURI = origin + "/federation/recipes/" + recipeID
	const cacheBody = `{"title":"Existing cache","description":"legacy","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}`
	if err := registerFederationPeer(db, origin, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('legacy-owner','legacy@example.test','Legacy',0,'created')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, objectURI, origin, recipeID, 4, "Legacy author", cacheBody, "2026-11-04T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES('legacy-reference','legacy-owner','remote',?,'2026-11-04T12:01:00Z')`, objectURI); err != nil {
		t.Fatal(err)
	}
	if version := migrationVersion(t, db); version != 12 {
		t.Fatalf("pre-upgrade version=%d, want 12", version)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}
	assertLegacyReferenceAndNoSnapshot := func(current *sql.DB) {
		t.Helper()
		if version := migrationVersion(t, current); version != latestSchemaVersion() {
			t.Fatalf("post-upgrade version=%d, want latest %d", version, latestSchemaVersion())
		}
		var bookmarkURI, storedBody string
		if err := current.QueryRow(`SELECT remote_object_uri FROM recipe_bookmarks WHERE id='legacy-reference'`).Scan(&bookmarkURI); err != nil || bookmarkURI != objectURI {
			t.Fatalf("legacy bookmark target=%q err=%v", bookmarkURI, err)
		}
		if err := current.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id=?`, objectURI).Scan(&storedBody); err != nil || storedBody != cacheBody {
			t.Fatalf("legacy cache changed: body=%q err=%v", storedBody, err)
		}
		var pins, versions, receipts int
		if err := current.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_pins`).Scan(&pins); err != nil {
			t.Fatal(err)
		}
		if err := current.QueryRow(`SELECT COUNT(*) FROM library_remote_snapshot_versions`).Scan(&versions); err != nil {
			t.Fatal(err)
		}
		if err := current.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if pins != 0 || versions != 0 || receipts != 0 {
			t.Fatalf("migration promoted legacy rows: pins=%d versions=%d receipts=%d", pins, versions, receipts)
		}
	}
	assertLegacyReferenceAndNoSnapshot(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertLegacyReferenceAndNoSnapshot(reopened)
}

func TestFederationResourceMigrationBackfillsDurableCatalogAndPreservesReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation-resource-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:10]); err != nil {
		t.Fatal(err)
	}
	origin := "https://remote.example"
	object := origin + "/federation/recipes/recipe-one"
	response := `{"status":"accepted","note":"Crème brûlée 🥣"}`
	digest := make([]byte, 32)
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'created','updated')`, origin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,7,'Créatrice','{"title":"Soupe 🥣"}',0,'snapshot-time')`, object, origin, "recipe-one"); err != nil {
		t.Fatal(err)
	}
	missingProjectionObject := origin + "/federation/recipes/recipe-two"
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,2,'Author','not-a-recipe',0,'updated-time')`, missingProjectionObject, origin, "recipe-two"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_recipe_search(object_id,projection_version,available,title,description,tags_json) VALUES(?,1,1,'Soupe 🥣','Épicée','["été"]')`, object); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body) VALUES(?,?,?,?,?,?,202,?)`, origin, "event-one", digest, object, 7, "received-time", response); err != nil {
		t.Fatal(err)
	}
	nonceHash := strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO federation_nonces(nonce_hash,expires_at) VALUES(?,2099999999)`, nonceHash); err != nil {
		t.Fatal(err)
	}
	missingCacheObject := origin + "/federation/recipes/recipe-three"
	if _, err := db.Exec(`INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body) VALUES(?,?,?,?,?,?,202,?)`, origin, "event-without-cache", digest, missingCacheObject, 11, "received-time", `{"status":"accepted"}`); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}
	var catalogBytes, receiptBytes int64
	if err := db.QueryRow(`SELECT logical_bytes FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, object).Scan(&catalogBytes); err != nil {
		t.Fatal(err)
	}
	wantCatalogBytes := remoteCatalogLogicalBytes(origin, object, "recipe-one", "Créatrice", `{"title":"Soupe 🥣"}`, "Soupe 🥣", "Épicée", `["été"]`, "published", 7, 0)
	if catalogBytes != wantCatalogBytes {
		t.Fatalf("catalog bytes=%d want=%d", catalogBytes, wantCatalogBytes)
	}
	var liveState, receiptOnlyState string
	if err := db.QueryRow(`SELECT source_state FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, object).Scan(&liveState); err != nil || liveState != "published" {
		t.Fatalf("live legacy source state=%q err=%v", liveState, err)
	}
	if err := db.QueryRow(`SELECT source_state FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, missingCacheObject).Scan(&receiptOnlyState); err != nil || receiptOnlyState != "unknown_legacy" {
		t.Fatalf("receipt-only legacy source state=%q err=%v", receiptOnlyState, err)
	}
	var promoted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&promoted); err != nil || promoted != 0 {
		t.Fatalf("legacy receipt promotion rows=%d err=%v", promoted, err)
	}
	if err := db.QueryRow(`SELECT logical_bytes,response_body FROM federation_received_events WHERE source_origin=? AND event_id=?`, origin, "event-one").Scan(&receiptBytes, &response); err != nil {
		t.Fatal(err)
	}
	wantReceiptBytes := receivedEventLogicalBytes(origin, "event-one", digest, object, 7, "received-time", 202, response)
	if receiptBytes != wantReceiptBytes {
		t.Fatalf("receipt bytes=%d want=%d", receiptBytes, wantReceiptBytes)
	}
	var foreignKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('federation_received_events')`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("receipt foreign keys=%d err=%v", foreignKeys, err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("migration version=%d want latest %d", got, latestSchemaVersion())
	}
	var nonceOrigin string
	var nonceBytes int64
	var nonceExpiry int64
	if err := db.QueryRow(`SELECT source_origin,logical_bytes,expires_at FROM federation_nonces WHERE nonce_hash=?`, nonceHash).Scan(&nonceOrigin, &nonceBytes, &nonceExpiry); err != nil || nonceOrigin != "" || nonceBytes != 72 || nonceExpiry != 2099999999 {
		t.Fatalf("legacy nonce migration changed replay state: origin=%q bytes=%d expiry=%d err=%v", nonceOrigin, nonceBytes, nonceExpiry, err)
	}
	var highWater int64
	var known int
	if err := db.QueryRow(`SELECT high_water_sequence,state_known FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, missingCacheObject).Scan(&highWater, &known); err != nil || highWater != 11 || known != 0 {
		t.Fatalf("receipt-only history was not retained without invented cache state: sequence=%d known=%d err=%v", highWater, known, err)
	}
	if !migrationObjectExists(t, db, "table", "federation_remote_recipe_search") {
		t.Fatal("projection table missing after resource migration")
	}
	var projectionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search WHERE object_id=?`, missingProjectionObject).Scan(&projectionCount); err != nil || projectionCount != 1 {
		t.Fatalf("legacy missing projection was not backfilled: count=%d err=%v", projectionCount, err)
	}
}

func TestFederationResourceMigrationPreservesLegacyDataOverDefaultsAndRejectsNewWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation-resource-over-default-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:10]); err != nil {
		t.Fatal(err)
	}
	const origin = "https://legacy-over-default.example.org"
	const legacyRows = defaultPeerObjectRows + 1
	const response = `{"legacy":"accepted"}`
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'created','updated')`, origin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?)
		INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at)
		SELECT ? || '/federation/recipes/legacy-' || printf('%04d',n),?, 'legacy-' || printf('%04d',n),1,'Legacy','{"title":"Legacy"}',0,'legacy-time' FROM seq`, legacyRows, origin, origin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?)
		INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body)
		SELECT ?, 'legacy-event-' || printf('%04d',n),zeroblob(32),? || '/federation/recipes/legacy-' || printf('%04d',n),1,'legacy-received',202,? FROM seq`, legacyRows, origin, origin, response); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("upgrade rejected over-default legacy data: %v", err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("migration version=%d want latest %d", got, latestSchemaVersion())
	}
	usage, err := readFederationResourceUsage(ctx, db, origin, false)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ObjectRows != legacyRows || usage.EventRows != legacyRows {
		t.Fatalf("over-default usage=%+v want %d catalog objects and receipts", usage, legacyRows)
	}
	var digest []byte
	var preservedResponse string
	if err := db.QueryRow(`SELECT body_digest,response_body FROM federation_received_events WHERE source_origin=? AND event_id='legacy-event-0001'`, origin).Scan(&digest, &preservedResponse); err != nil {
		t.Fatal(err)
	}
	if len(digest) != 32 || preservedResponse != response {
		t.Fatalf("exact old receipt changed: digest bytes=%d response=%q", len(digest), preservedResponse)
	}
	body, err := serializeFederationEvent(origin, strings.Repeat("f", 32), federationEventSnapshot, 1, time.Now(), true, true, federationRecipeSource{ID: strings.Repeat("a", 32), CreatorName: "New", Title: "New recipe", Visibility: visibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acceptFederationEvent(ctx, db, origin, body); !errors.Is(err, errFederationResourceCapacity) {
		t.Fatalf("new write over default did not fail closed: %v", err)
	}
	var catalogRows, receiptRows int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_catalog WHERE source_origin=?`, origin).Scan(&catalogRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events WHERE source_origin=?`, origin).Scan(&receiptRows); err != nil {
		t.Fatal(err)
	}
	if catalogRows != legacyRows || receiptRows != legacyRows {
		t.Fatalf("rejected over-default write changed retained rows: catalog=%d receipts=%d", catalogRows, receiptRows)
	}
}

func TestFederationResourceMigrationRebuildsProjectionForExpandedStoredRecipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation-resource-expanded-recipe-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:10]); err != nil {
		t.Fatal(err)
	}
	const origin = "https://expanded-recipe.example.org"
	const recipeID = "0123456789abcdef0123456789abcdef"
	const eventID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := registerFederationPeer(db, origin, true); err != nil {
		t.Fatal(err)
	}
	step := `{"text":"` + strings.Repeat("<", 5000) + `"}`
	steps := make([]string, 200)
	for index := range steps {
		steps[index] = step
	}
	body := []byte(`{"protocol":"cookbook-exchange","schema_version":1,"event_id":"` + eventID + `","event_type":"recipe.snapshot","source_origin":"` + origin + `","object_id":"` + origin + `/federation/recipes/` + recipeID + `","sequence":1,"occurred_at":"2026-11-04T12:00:00Z","attribution":{"creator_name":"Legacy"},"recipe":{"title":"Expanded legacy recipe","description":"","ingredients":[],"steps":[` + strings.Join(steps, ",") + `],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}}`)
	if int64(len(body)) >= maxFederationEventBytes {
		t.Fatalf("fixture wire body=%d bytes; must be below %d", len(body), maxFederationEventBytes)
	}
	_, normalizedRecipe, err := decodeFederationEvent(body)
	if err != nil {
		t.Fatalf("decode expanded wire recipe: %v", err)
	}
	if int64(len(normalizedRecipe)) <= maxFederationEventBytes {
		t.Fatalf("normalized stored recipe=%d bytes; expected expansion beyond wire cap %d", len(normalizedRecipe), maxFederationEventBytes)
	}
	objectID := origin + "/federation/recipes/" + recipeID
	digest := sha256.Sum256(body)
	if _, err := db.Exec(`INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at) VALUES(?,?,?,?,?,?,0,?)`, objectID, origin, recipeID, 1, "Legacy", string(normalizedRecipe), "2026-11-04T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_received_events(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body) VALUES(?,?,?,?,?, ?,202,?)`, origin, eventID, digest[:], objectID, 1, "2026-11-04T12:00:00Z", `{"status":"accepted"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM federation_remote_recipe_search WHERE object_id=?`, objectID); err != nil {
		t.Fatal(err)
	}
	var storedBefore string
	if err := db.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id=?`, objectID).Scan(&storedBefore); err != nil {
		t.Fatal(err)
	}
	if int64(len(storedBefore)) <= maxFederationEventBytes || storedBefore != string(normalizedRecipe) {
		t.Fatalf("legacy stored recipe bytes=%d does not match expanded normalized recipe", len(storedBefore))
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("migration could not rebuild expanded legacy projection: %v", err)
	}
	var storedAfter, projectedTitle string
	var projectionAvailable int
	if err := db.QueryRow(`SELECT r.recipe_json,p.available,p.title FROM federation_remote_recipes r JOIN federation_remote_recipe_search p ON p.object_id=r.object_id WHERE r.object_id=?`, objectID).Scan(&storedAfter, &projectionAvailable, &projectedTitle); err != nil {
		t.Fatal(err)
	}
	if storedAfter != storedBefore || projectionAvailable != 0 || projectedTitle != "" {
		t.Fatalf("migration changed expanded recipe or missed unavailable projection marker: stored bytes=%d available=%d title=%q", len(storedAfter), projectionAvailable, projectedTitle)
	}
	var receiptCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_received_events WHERE source_origin=? AND event_id=?`, origin, eventID).Scan(&receiptCount); err != nil || receiptCount != 1 {
		t.Fatalf("expanded legacy receipt count=%d err=%v", receiptCount, err)
	}
}

func TestMigrationV1ToV9ChecksumsRemainImmutable(t *testing.T) {
	migrations := registeredSchemaMigrations()
	want := []string{
		"c77f29249dfa71f9c86b38cf6a3db05331f790156314647230d80868342f11ba",
		"4b2409dfd518076b3f4819fb1b010743acf2e8ab4febe4989e252569894e04f0",
		"501f3ae2edf1e67188f16515e4d35b9edf463e89bb15dc7b1f37f95128ec8e4b",
		"1dabfe8d20a0da74bcf584f1f072da9ea03eb89d98c8e7e5f6edab2924a09430",
		"533bc6b05a609de6a66ca71fb5ea272bf6476c5a0a8c957025498e3d301cd02d",
		"0134ad6cbd8b0798fc648fe693cdff4f61f9e1fb2df8c16a6acf901e0c6bd3b3",
		"b6359ac22a017f3269fde15bc9f66167368e54a84fb8fa3b1e901be2703de453",
		"3b37e28d24ba0090c8b278603cb6dd16580ee149189e6377657d3c8c7bf68d30",
		"d507ce5fb6e9cedd4a8bc48713b180a06e2a30861f5174cbe00ffde4fcb9e1c4",
	}
	if len(migrations) < 13 {
		t.Fatalf("registered migrations=%d, want 13 contiguous migrations", len(migrations))
	}
	for index, checksum := range want {
		if migrations[index].checksum != checksum {
			t.Errorf("migration %d checksum changed: got %s want %s", index+1, migrations[index].checksum, checksum)
		}
	}
}

func TestV8DatabaseUpgradeDefersProjectionUntilResourceMigrationAndPreservesSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v8-library-projection-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES('https://remote.example',1,'created','updated');
		INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at)
		VALUES('https://remote.example/federation/recipes/recipe-one','https://remote.example','recipe-one',7,'Remote cook','{"title":"Legacy snapshot"}',0,'snapshot-time')`); err != nil {
		t.Fatal(err)
	}
	type migrationRecord struct {
		version        int
		name, checksum string
	}
	var before []migrationRecord
	rows, err := db.Query(`SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var item migrationRecord
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		before = append(before, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(before) != 8 {
		t.Fatalf("pre-upgrade ledger rows=%d, want 8", len(before))
	}
	if err := runSchemaMigrations(ctx, db, migrations[:9]); err != nil {
		t.Fatal(err)
	}
	if got := migrationVersion(t, db); got != 9 {
		t.Fatalf("after v9 upgrade version=%d, want 9", got)
	}
	var projections int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search`).Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("v9 migration populated deferred projection rows=%d err=%v, want zero", projections, err)
	}
	var cachedBody string
	if err := db.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id='https://remote.example/federation/recipes/recipe-one'`).Scan(&cachedBody); err != nil || cachedBody != `{"title":"Legacy snapshot"}` {
		t.Fatalf("v9 migration changed cached remote snapshot: body=%q err=%v", cachedBody, err)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("upgraded version=%d, want latest %d", got, latestSchemaVersion())
	}
	for _, item := range before {
		var name, checksum string
		if err := db.QueryRow(`SELECT name,checksum FROM schema_migrations WHERE version=?`, item.version).Scan(&name, &checksum); err != nil || name != item.name || checksum != item.checksum {
			t.Fatalf("migration v%d changed: name=%q checksum=%q err=%v", item.version, name, checksum, err)
		}
	}
	cachedBody = ""
	if err := db.QueryRow(`SELECT recipe_json FROM federation_remote_recipes WHERE object_id='https://remote.example/federation/recipes/recipe-one'`).Scan(&cachedBody); err != nil || cachedBody != `{"title":"Legacy snapshot"}` {
		t.Fatalf("cached remote snapshot changed: body=%q err=%v", cachedBody, err)
	}
	var available int
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(available),-1) FROM federation_remote_recipe_search`).Scan(&projections, &available); err != nil || projections != 1 || available != 0 {
		t.Fatalf("v11 migration did not backfill unavailable projection: rows=%d available=%d err=%v", projections, available, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("restarted version=%d, want latest %d", got, latestSchemaVersion())
	}
	if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(available),-1) FROM federation_remote_recipe_search`).Scan(&projections, &available); err != nil || projections != 1 || available != 0 {
		t.Fatalf("restart lost latest-v11 projection: rows=%d available=%d err=%v", projections, available, err)
	}
}

func TestV9UpgradeAddsCourseReferencesWithoutChangingCourseCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v9-course-bookmarks-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:9]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('reader','reader@example.test','Reader','now');
INSERT INTO courses(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('course','owner','Existing title','Existing description','private','created','updated');
INSERT INTO course_lessons(id,course_id,title,content,position,created_at,updated_at) VALUES('lesson','course','Lesson','Body',1,'created','updated');`); err != nil {
		t.Fatal(err)
	}
	var before struct {
		version        int
		name, checksum string
	}
	if err := db.QueryRow(`SELECT version,name,checksum FROM schema_migrations WHERE version=9`).Scan(&before.version, &before.name, &before.checksum); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("upgraded version=%d, want latest %d", got, latestSchemaVersion())
	}
	var name, checksum string
	if err := db.QueryRow(`SELECT name,checksum FROM schema_migrations WHERE version=9`).Scan(&name, &checksum); err != nil || name != before.name || checksum != before.checksum {
		t.Fatalf("v9 ledger changed: %q/%q err=%v", name, checksum, err)
	}
	var owner, title, description, lessonTitle, lessonBody string
	if err := db.QueryRow(`SELECT c.owner_id,c.title,c.description,l.title,l.content FROM courses c JOIN course_lessons l ON l.course_id=c.id WHERE c.id='course'`).Scan(&owner, &title, &description, &lessonTitle, &lessonBody); err != nil || owner != "owner" || title != "Existing title" || description != "Existing description" || lessonTitle != "Lesson" || lessonBody != "Body" {
		t.Fatalf("course custody/content changed: %q %q %q %q %q err=%v", owner, title, description, lessonTitle, lessonBody, err)
	}
	if _, err := db.Exec(`INSERT INTO course_bookmarks(id,user_id,local_instance_id,local_course_id,created_at) VALUES('first','reader',?,'course','now')`, strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM courses WHERE id='course'`); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM course_bookmarks WHERE id='first'`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("course FK removed saved identity: rows=%d err=%v", retained, err)
	}
}

func TestLibraryRemoteRecipeSearchMigrationFailureRollsBackDDLAndLedger(t *testing.T) {
	db := rawMigrationDB(t, ":memory:")
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(context.Background(), db, migrations[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER federation_remote_recipe_search_invalidate_after_update AFTER INSERT ON users BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(context.Background(), db, migrations); err == nil {
		t.Fatal("migration unexpectedly succeeded with a conflicting trigger name")
	}
	if migrationObjectExists(t, db, "table", "federation_remote_recipe_search") || migrationVersion(t, db) != 8 {
		t.Fatal("failed v9 migration left partial DDL or advanced the ledger")
	}
	if _, err := db.Exec(`DROP TRIGGER federation_remote_recipe_search_invalidate_after_update`); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	if migrationVersion(t, db) != latestSchemaVersion() || !migrationObjectExists(t, db, "table", "federation_remote_recipe_search") || !migrationObjectExists(t, db, "trigger", "federation_remote_recipe_search_invalidate_after_update") {
		t.Fatal("retry did not complete v9 schema and ledger")
	}
}

func TestLibraryRemoteRecipeSearchProjectionInvalidatesOnSourceUpdatesAndDelete(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES
		('https://remote.example',1,'created','updated'),('https://other.example',1,'created','updated');
		INSERT INTO federation_remote_recipes(object_id,source_origin,source_recipe_id,sequence,creator_name,recipe_json,deleted,updated_at)
		VALUES('https://remote.example/federation/recipes/recipe-one','https://remote.example','recipe-one',7,'Remote cook','{"title":"Legacy snapshot"}',0,'snapshot-time')`); err != nil {
		t.Fatal(err)
	}
	updates := []struct {
		name, statement string
		value           any
		nextObjectID    string
	}{
		{name: "object identity", statement: `UPDATE federation_remote_recipes SET object_id=? WHERE object_id=?`, value: "https://remote.example/federation/recipes/recipe-renamed", nextObjectID: "https://remote.example/federation/recipes/recipe-renamed"},
		{name: "source origin", statement: `UPDATE federation_remote_recipes SET source_origin=? WHERE object_id=?`, value: "https://other.example"},
		{name: "source recipe id", statement: `UPDATE federation_remote_recipes SET source_recipe_id=? WHERE object_id=?`, value: "recipe-two"},
		{name: "sequence", statement: `UPDATE federation_remote_recipes SET sequence=? WHERE object_id=?`, value: 8},
		{name: "creator metadata", statement: `UPDATE federation_remote_recipes SET creator_name=? WHERE object_id=?`, value: "Updated cook"},
		{name: "snapshot body", statement: `UPDATE federation_remote_recipes SET recipe_json=? WHERE object_id=?`, value: `{"title":"Updated snapshot"}`},
		{name: "updated timestamp", statement: `UPDATE federation_remote_recipes SET updated_at=? WHERE object_id=?`, value: "updated-time"},
		{name: "tombstone", statement: `UPDATE federation_remote_recipes SET deleted=? WHERE object_id=?`, value: 1},
	}
	objectID := "https://remote.example/federation/recipes/recipe-one"
	for _, update := range updates {
		t.Run(update.name, func(t *testing.T) {
			if _, err := db.Exec(`INSERT INTO federation_remote_recipe_search(object_id,projection_version,available,title,description,tags_json) VALUES(?,1,1,'Projected','Searchable','[]')`, objectID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(update.statement, update.value, objectID); err != nil {
				t.Fatal(err)
			}
			var remaining int
			if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search WHERE object_id=?`, objectID).Scan(&remaining); err != nil || remaining != 0 {
				t.Fatalf("projection survived source update: count=%d err=%v", remaining, err)
			}
			if update.nextObjectID != "" {
				objectID = update.nextObjectID
			}
		})
	}
	if _, err := db.Exec(`INSERT INTO federation_remote_recipe_search(object_id,projection_version,available,title,description,tags_json) VALUES(?,1,1,'Projected','Searchable','[]')`, objectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM federation_remote_recipes WHERE object_id=?`, objectID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_remote_recipe_search WHERE object_id=?`, objectID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("projection survived cache-row delete cascade: count=%d err=%v", remaining, err)
	}
}

func TestV4DatabaseUpgradesToV9WithoutChangingEarlierLedgerRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:4]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('person','person@example.test','Person','created')`); err != nil {
		t.Fatal(err)
	}
	type migrationRecord struct {
		version        int
		name, checksum string
	}
	var before []migrationRecord
	rows, err := db.Query(`SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var entry migrationRecord
		if err := rows.Scan(&entry.version, &entry.name, &entry.checksum); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		before = append(before, entry)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(before) != 4 {
		t.Fatalf("v4 ledger rows=%d, want 4", len(before))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("upgraded migration version=%d, want latest %d", got, latestSchemaVersion())
	}
	for _, entry := range before {
		var name, checksum string
		if err := db.QueryRow(`SELECT name,checksum FROM schema_migrations WHERE version=?`, entry.version).Scan(&name, &checksum); err != nil || name != entry.name || checksum != entry.checksum {
			db.Close()
			t.Fatalf("migration v%d changed: name=%q checksum=%q err=%v", entry.version, name, checksum, err)
		}
	}
	var userID string
	if err := db.QueryRow(`SELECT id FROM users WHERE id='person'`).Scan(&userID); err != nil || userID != "person" {
		db.Close()
		t.Fatalf("v4 user changed: id=%q err=%v", userID, err)
	}
	var actors int
	if err := db.QueryRow(`SELECT COUNT(*) FROM person_actor_ids`).Scan(&actors); err != nil || actors != 0 {
		db.Close()
		t.Fatalf("v4 upgrade created Person actors: count=%d err=%v", actors, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("restarted migration version=%d, want latest %d", got, latestSchemaVersion())
	}
}

func TestV5DatabaseUpgradesToV9WithStableLibraryNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5-bookmarks-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:5]); err != nil {
		t.Fatal(err)
	}
	type migrationRecord struct {
		version        int
		name, checksum string
	}
	var before []migrationRecord
	rows, err := db.Query(`SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var entry migrationRecord
		if err := rows.Scan(&entry.version, &entry.name, &entry.checksum); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		before = append(before, entry)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(before) != 5 {
		t.Fatalf("v5 ledger rows=%d, want 5", len(before))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	var namespace string
	if err := db.QueryRow(`SELECT instance_id FROM library_instance_identity WHERE singleton_id=1`).Scan(&namespace); err != nil || !validLibraryInstanceID(namespace) {
		db.Close()
		t.Fatalf("new library namespace=%q err=%v", namespace, err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		db.Close()
		t.Fatalf("upgraded version=%d, want latest %d", got, latestSchemaVersion())
	}
	for _, entry := range before {
		var name, checksum string
		if err := db.QueryRow(`SELECT name,checksum FROM schema_migrations WHERE version=?`, entry.version).Scan(&name, &checksum); err != nil || name != entry.name || checksum != entry.checksum {
			db.Close()
			t.Fatalf("migration v%d changed: name=%q checksum=%q err=%v", entry.version, name, checksum, err)
		}
	}
	var foreignTables []string
	fks, err := db.Query(`PRAGMA foreign_key_list(recipe_bookmarks)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	for fks.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := fks.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			fks.Close()
			db.Close()
			t.Fatal(err)
		}
		foreignTables = append(foreignTables, table)
	}
	if err := fks.Close(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if len(foreignTables) != 1 || foreignTables[0] != "users" {
		db.Close()
		t.Fatalf("bookmark foreign keys=%v, want users only", foreignTables)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var restartedNamespace string
	if err := db.QueryRow(`SELECT instance_id FROM library_instance_identity WHERE singleton_id=1`).Scan(&restartedNamespace); err != nil || restartedNamespace != namespace {
		t.Fatalf("restarted library namespace=%q err=%v, want stable %q", restartedNamespace, err, namespace)
	}
}

func TestCurrentPreLedgerDatabaseAdoptsWithoutChangingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current-preledger.db")
	db := rawMigrationDB(t, path)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var err error
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1;
INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('owner','owner@example.test','Owner',1,'created');
INSERT INTO recipes(id,owner_id,creator_name,title,visibility,version,created_at,updated_at) VALUES('recipe','owner','Claimed Creator','Preserve me','private',4,'created','updated');
INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at) VALUES('revision','recipe',4,'owner','Preserve me','','[]','[]','private','revision-time');
INSERT INTO sessions(id,token_hash,user_id,expires_at,created_at,user_agent) VALUES('stable-session','token-hash','owner','2099-01-01T00:00:00Z','session-time','agent');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var beforeRevisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_revisions WHERE recipe_id='recipe'`).Scan(&beforeRevisions); err != nil || beforeRevisions != 1 {
		db.Close()
		t.Fatalf("pre-ledger revision count=%d err=%v", beforeRevisions, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var public, admin, version, revisionCount int
	var creator, title, sessionID string
	if err := db.QueryRow(`SELECT s.public_instance,u.is_admin,r.creator_name,r.title,r.version,ss.id,(SELECT COUNT(*) FROM recipe_revisions rv WHERE rv.recipe_id=r.id) FROM instance_settings s JOIN users u ON u.id='owner' JOIN recipes r ON r.id='recipe' JOIN sessions ss ON ss.token_hash='token-hash' WHERE s.id=1`).Scan(&public, &admin, &creator, &title, &version, &sessionID, &revisionCount); err != nil {
		t.Fatal(err)
	}
	if public != 1 || admin != 1 || creator != "Claimed Creator" || title != "Preserve me" || version != 4 || sessionID != "stable-session" || revisionCount != beforeRevisions {
		t.Fatalf("adopted data changed: public=%d admin=%d creator=%q title=%q version=%d session=%q revisions=%d", public, admin, creator, title, version, sessionID, revisionCount)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("adopted migration version=%d, want latest %d", got, latestSchemaVersion())
	}
	var owner sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &generation); err != nil || owner.Valid || generation != 0 {
		t.Fatalf("legacy authority mapping owner=%v generation=%d err=%v", owner, generation, err)
	}
}

func TestV6DatabaseUpgradesToV9WithoutBackfillAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6-creator-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES('book','owner','Old book','now','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES('recipe','owner','Legacy name','Old recipe','now','now')`); err != nil {
		t.Fatal(err)
	}
	var v6Checksum string
	if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=6`).Scan(&v6Checksum); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()); err != nil {
		t.Fatal(err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("upgraded version=%d, want latest %d", got, latestSchemaVersion())
	}
	var unchanged string
	if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=6`).Scan(&unchanged); err != nil || unchanged != v6Checksum {
		t.Fatalf("v6 checksum changed: got %q err=%v want %q", unchanged, err, v6Checksum)
	}
	for _, table := range []string{"creator_profiles", "content_attributions"} {
		if !migrationObjectExists(t, db, "table", table) {
			t.Fatalf("migration table %q is missing", table)
		}
	}
	var profiles, attributions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM creator_profiles`).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions`).Scan(&attributions); err != nil {
		t.Fatal(err)
	}
	var creatorName string
	if err := db.QueryRow(`SELECT creator_name FROM recipes WHERE id='recipe'`).Scan(&creatorName); err != nil || creatorName != "Legacy name" || profiles != 0 || attributions != 0 {
		t.Fatalf("migration backfilled legacy identity: creator=%q profiles=%d attributions=%d err=%v", creatorName, profiles, attributions, err)
	}
	for _, index := range []string{"content_attributions_local_recipe_uq", "content_attributions_local_cookbook_uq", "content_attributions_claim_recipe_uq", "content_attributions_claim_cookbook_uq", "content_attributions_local_course_uq", "content_attributions_claim_course_uq", "content_attributions_local_menu_uq", "content_attributions_claim_menu_uq"} {
		if !migrationObjectExists(t, db, "index", index) {
			t.Errorf("partial unique index %q is missing", index)
		}
	}
	wantForeignKeys := map[string]map[string]string{
		"creator_profiles":     {"user_id": "users"},
		"content_attributions": {"recipe_id": "recipes", "cookbook_id": "cookbooks", "course_id": "courses", "menu_id": "meal_menus", "creator_id": "creator_profiles"},
	}
	for table, want := range wantForeignKeys {
		rows, err := db.Query(`PRAGMA foreign_key_list(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		found := make(map[string]string)
		for rows.Next() {
			var id, seq int
			var target, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			found[from] = target
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if len(found) != len(want) {
			t.Errorf("%s foreign keys=%v, want %v", table, found, want)
			continue
		}
		for from, target := range want {
			if found[from] != target {
				t.Errorf("%s foreign key %s targets %q, want %q", table, from, found[from], target)
			}
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Errorf("migration foreign-key violations=%d err=%v", violations, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("restart migration version=%d, want latest %d", got, latestSchemaVersion())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM creator_profiles`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("restart changed empty profiles: count=%d err=%v", count, err)
	}
}

func TestV7DatabaseUpgradesToV9WithoutChangingLedgerOrExistingBookmarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7-cookbook-bookmarks-upgrade.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:7]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('reader','reader@example.test','Reader','now')`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_bookmarks(id,user_id,target_kind,local_instance_id,local_recipe_id,created_at)
		VALUES('existing-recipe-ref','reader','local',?,'source-recipe','saved')`, instanceID); err != nil {
		t.Fatal(err)
	}
	type ledgerEntry struct {
		version        int
		name, checksum string
	}
	var before []ledgerEntry
	rows, err := db.Query(`SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var item ledgerEntry
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		before = append(before, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(before) != 7 {
		t.Fatalf("pre-upgrade ledger rows=%d, want 7", len(before))
	}
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()); err != nil {
		t.Fatal(err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("upgraded version=%d, want latest %d", got, latestSchemaVersion())
	}
	for _, item := range before {
		var name, checksum string
		if err := db.QueryRow(`SELECT name,checksum FROM schema_migrations WHERE version=?`, item.version).Scan(&name, &checksum); err != nil || name != item.name || checksum != item.checksum {
			t.Fatalf("migration v%d changed: name=%q checksum=%q err=%v", item.version, name, checksum, err)
		}
	}
	var recipeRows, cookbookRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE id='existing-recipe-ref' AND local_recipe_id='source-recipe'`).Scan(&recipeRows); err != nil || recipeRows != 1 {
		t.Fatalf("existing recipe reference count=%d err=%v", recipeRows, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks`).Scan(&cookbookRows); err != nil || cookbookRows != 0 {
		t.Fatalf("v8 migration backfilled cookbook references: count=%d err=%v", cookbookRows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("restart version=%d, want latest %d", got, latestSchemaVersion())
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_bookmarks WHERE id='existing-recipe-ref'`).Scan(&recipeRows); err != nil || recipeRows != 1 {
		t.Fatalf("restart changed recipe reference: count=%d err=%v", recipeRows, err)
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("v8 database integrity=%q err=%v", integrity, err)
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("v8 FK violations=%d err=%v", violations, err)
	}
}

func TestInstanceOwnerMigrationPreservesV1RowsAndLeavesAuthorityUnclaimed(t *testing.T) {
	db := rawMigrationDB(t, ":memory:")
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('legacy-admin','legacy-admin@example.test','Legacy Admin',1,'user-a'),
		('legacy-member','legacy-member@example.test','Legacy Member',0,'user-b');
		INSERT INTO recipes(id,owner_id,creator_name,title,version,created_at,updated_at) VALUES
		('legacy-recipe','legacy-admin','Unverified Creator','Keep me',4,'recipe-created','recipe-updated');
		INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at)
		VALUES('legacy-revision','legacy-recipe',4,'legacy-admin','Keep me','','[]','[]','private','revision-created');
		INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES
		('legacy-book','legacy-admin','Legacy book','book-created','book-updated');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at)
		VALUES('legacy-book','legacy-member','viewer','membership-created');`); err != nil {
		t.Fatal(err)
	}

	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()); err != nil {
		t.Fatal(err)
	}
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("migration version=%d, want latest %d", got, latestSchemaVersion())
	}
	var owner sql.NullString
	var claimedAt sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,owner_claimed_at,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &claimedAt, &generation); err != nil || owner.Valid || claimedAt.Valid || generation != 0 {
		t.Fatalf("v1 authority mapping owner=%v claimed_at=%v generation=%d err=%v", owner, claimedAt, generation, err)
	}
	var adminFlag, memberFlag, recipeVersion int
	var recipeOwner, creator, title, bookOwner, memberRole string
	err := db.QueryRow(`SELECT admin.is_admin,member.is_admin,r.owner_id,r.creator_name,r.title,b.owner_id,m.role,r.version
		FROM users admin JOIN users member ON member.id='legacy-member'
		JOIN recipes r ON r.id='legacy-recipe'
		JOIN cookbooks b ON b.id='legacy-book'
		JOIN cookbook_memberships m ON m.cookbook_id=b.id AND m.user_id=member.id
		WHERE admin.id='legacy-admin'`).Scan(&adminFlag, &memberFlag, &recipeOwner, &creator, &title, &bookOwner, &memberRole, &recipeVersion)
	if err != nil {
		t.Fatal(err)
	}
	if adminFlag != 1 || memberFlag != 0 || recipeOwner != "legacy-admin" || creator != "Unverified Creator" || title != "Keep me" || bookOwner != "legacy-admin" || memberRole != "viewer" || recipeVersion != 4 {
		t.Fatalf("owner migration changed legacy rows: admin=%d member=%d recipe=%s/%s/%s v%d book=%s membership=%s", adminFlag, memberFlag, recipeOwner, creator, title, recipeVersion, bookOwner, memberRole)
	}
}

func TestVersionedDatabaseDoesNotReplayBaselineAndRejectsMissingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versioned.db")
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','created');
INSERT INTO recipes(id,owner_id,creator_name,title,version,created_at,updated_at) VALUES('recipe','owner','Owner','Preserve',2,'created','updated');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE recipe_revisions`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = openDatabase(path)
	if err == nil {
		db.Close()
		t.Fatal("opened versioned database with missing core table")
	}
	if !strings.Contains(err.Error(), "recipe_revisions") {
		t.Fatalf("missing-table error=%v, want recipe_revisions", err)
	}
	check := rawMigrationDB(t, path)
	if !migrationObjectExists(t, check, "table", "schema_migrations") || migrationObjectExists(t, check, "table", "recipe_revisions") {
		t.Fatal("missing-table check unexpectedly repaired or removed schema")
	}
	var title string
	if err := check.QueryRow(`SELECT title FROM recipes WHERE id='recipe'`).Scan(&title); err != nil || title != "Preserve" {
		t.Fatalf("user data after schema rejection title=%q err=%v", title, err)
	}
}

func TestFutureMigrationRunsOnceAndFailureRollsBackStepAndLedger(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fail := true
	runs := 0
	migrations := registeredSchemaMigrations()
	futureVersion := migrations[len(migrations)-1].version + 1
	step := migrationTestStep(futureVersion, "test-marker", func(ctx context.Context, exec migrationExecutor) error {
		runs++
		if _, err := exec.ExecContext(ctx, `CREATE TABLE migration_marker(value TEXT NOT NULL); INSERT INTO migration_marker(value) VALUES('applied')`); err != nil {
			return err
		}
		if fail {
			return errors.New("injected migration failure")
		}
		return nil
	})
	migrations = append(migrations, step)
	if err := runSchemaMigrations(context.Background(), db, migrations); err == nil || !strings.Contains(err.Error(), "injected migration failure") {
		t.Fatalf("failed migration error=%v", err)
	}
	if migrationObjectExists(t, db, "table", "migration_marker") || migrationVersion(t, db) != futureVersion-1 {
		t.Fatal("failed migration left partial DDL/data or advanced the ledger")
	}
	fail = false
	if err := runSchemaMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	if migrationVersion(t, db) != futureVersion || !migrationObjectExists(t, db, "table", "migration_marker") {
		t.Fatal("pending migration did not apply")
	}
	if err := runSchemaMigrations(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	var markerCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM migration_marker`).Scan(&markerCount); err != nil || markerCount != 1 || runs != 2 {
		t.Fatalf("replayed migration count=%d callback runs=%d err=%v", markerCount, runs, err)
	}
}

func TestFailedBaselineRollsBackLedgerAndSchema(t *testing.T) {
	db := rawMigrationDB(t, ":memory:")
	step := migrationTestStep(1, "failing-baseline", func(ctx context.Context, exec migrationExecutor) error {
		if _, err := exec.ExecContext(ctx, `CREATE TABLE partial_baseline(value TEXT)`); err != nil {
			return err
		}
		return errors.New("injected baseline failure")
	})
	if err := runSchemaMigrations(context.Background(), db, []schemaMigration{step}); err == nil {
		t.Fatal("expected baseline failure")
	}
	if migrationObjectExists(t, db, "table", "schema_migrations") || migrationObjectExists(t, db, "table", "partial_baseline") {
		t.Fatal("failed baseline left a ledger or partial schema")
	}
}

func TestMalformedLedgerIsRejectedBeforeSchemaMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed-ledger.db")
	seed := rawMigrationDB(t, path)
	if _, err := seed.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL);
INSERT INTO users VALUES('owner','owner@example.test','Owner','created');
CREATE TABLE schema_migrations(version TEXT,name TEXT,checksum TEXT,applied_at TEXT);
INSERT INTO schema_migrations VALUES('1','legacy-baseline','bad','not-a-time')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := openDatabase(path); err == nil {
		db.Close()
		t.Fatal("opened malformed migration ledger")
	}
	check := rawMigrationDB(t, path)
	if migrationObjectExists(t, check, "table", "instance_settings") {
		t.Fatal("baseline schema was applied despite malformed ledger")
	}
	var displayName string
	if err := check.QueryRow(`SELECT display_name FROM users WHERE id='owner'`).Scan(&displayName); err != nil || displayName != "Owner" {
		t.Fatalf("user data changed after malformed-ledger rejection: %q err=%v", displayName, err)
	}
}

func TestMigrationNameChecksumAndNewerVersionAreValidated(t *testing.T) {
	for _, test := range []struct {
		name       string
		update     string
		wantErrSub string
	}{
		{name: "name", update: `UPDATE schema_migrations SET name='other' WHERE version=2`, wantErrSub: `name`},
		{name: "checksum", update: `UPDATE schema_migrations SET checksum=lower(hex(randomblob(32))) WHERE version=2`, wantErrSub: `checksum mismatch`},
		{name: "newer version", update: `UPDATE schema_migrations SET version=999 WHERE version=(SELECT MAX(version) FROM schema_migrations)`, wantErrSub: `newer than this binary`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.db")
			db, err := openDatabase(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.update); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if db, err := openDatabase(path); err == nil || !strings.Contains(err.Error(), test.wantErrSub) {
				if db != nil {
					db.Close()
				}
				t.Fatalf("open error=%v, want substring %q", err, test.wantErrSub)
			}
		})
	}
}

func TestUnknownUnversionedSchemaIsPreservedAndRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.db")
	seed := rawMigrationDB(t, path)
	if _, err := seed.Exec(`CREATE TABLE unrelated_data(value TEXT NOT NULL); INSERT INTO unrelated_data VALUES('keep');`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := openDatabase(path); err == nil || !strings.Contains(err.Error(), "unrecognized unversioned") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("open error=%v, want unrecognized schema", err)
	}
	check := rawMigrationDB(t, path)
	if migrationObjectExists(t, check, "table", "schema_migrations") || migrationObjectExists(t, check, "table", "users") {
		t.Fatal("unknown database was modified")
	}
	var value string
	if err := check.QueryRow(`SELECT value FROM unrelated_data`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("unknown user data changed: value=%q err=%v", value, err)
	}
}

func TestUnrelatedUsersOnlySchemaIsPreservedAndRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unrelated-users.db")
	seed := rawMigrationDB(t, path)
	if _, err := seed.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL,created_at TEXT NOT NULL);
INSERT INTO users VALUES('unrelated-owner','unrelated@example.test','Unrelated','created');`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err := openDatabase(path); err == nil || !strings.Contains(err.Error(), "unrecognized unversioned") {
		if db != nil {
			db.Close()
		}
		t.Fatalf("open error=%v, want unrecognized schema", err)
	}

	check := rawMigrationDB(t, path)
	var tableCount int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil || tableCount != 1 {
		t.Fatalf("unrelated database table count=%d err=%v, want only original users table", tableCount, err)
	}
	var id, email, displayName, createdAt string
	if err := check.QueryRow(`SELECT id,email,display_name,created_at FROM users`).Scan(&id, &email, &displayName, &createdAt); err != nil {
		t.Fatal(err)
	}
	if id != "unrelated-owner" || email != "unrelated@example.test" || displayName != "Unrelated" || createdAt != "created" {
		t.Fatalf("unrelated user row changed: id=%q email=%q name=%q created=%q", id, email, displayName, createdAt)
	}
}

func TestConcurrentStartupSerializesBaselineMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first := rawMigrationDB(t, path)
	second := rawMigrationDB(t, path)
	if _, err := first.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, db := range []*sql.DB{first, second} {
		wait.Add(1)
		go func(db *sql.DB) {
			defer wait.Done()
			<-start
			errs <- runSchemaMigrations(context.Background(), db, registeredSchemaMigrations())
		}(db)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := migrationVersion(t, first); got != latestSchemaVersion() {
		t.Fatalf("concurrent migration version=%d, want latest %d", got, latestSchemaVersion())
	}
}

func TestMigrationRegistryRejectsGapsAndDuplicateNames(t *testing.T) {
	noop := func(context.Context, migrationExecutor) error { return nil }
	if err := validateMigrationRegistry([]schemaMigration{
		migrationTestStep(1, "one", noop),
		migrationTestStep(3, "three", noop),
	}); err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("gapped registry error=%v", err)
	}
	if err := validateMigrationRegistry([]schemaMigration{
		migrationTestStep(1, "same", noop),
		migrationTestStep(2, "same", noop),
	}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate-name registry error=%v", err)
	}
}

func TestFederationV11ToV12BackfillsSourceStatesWithoutPromotionAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation-v11-to-v12.db")
	db := rawMigrationDB(t, path)
	ctx := context.Background()
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:11]); err != nil {
		t.Fatal(err)
	}
	origin := "https://legacy-peer.example.org"
	live := origin + "/federation/recipes/11111111111111111111111111111111"
	tombstone := origin + "/federation/recipes/22222222222222222222222222222222"
	receiptOnly := origin + "/federation/recipes/33333333333333333333333333333333"
	if _, err := db.Exec(`INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES(?,1,'created','updated')`, origin); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		object, recipe string
		sequence       int
		deleted, known int
		bytes          int
	}{
		{live, "11111111111111111111111111111111", 7, 0, 1, 100},
		{tombstone, "22222222222222222222222222222222", 8, 1, 1, 50},
		{receiptOnly, "33333333333333333333333333333333", 9, 0, 0, 40},
	} {
		if _, err := db.Exec(`INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,logical_bytes) VALUES(?,?,?,?,?,?,?)`, origin, row.object, row.recipe, row.sequence, row.deleted, row.known, row.bytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		object, state string
		sequence      int64
		bytes         int64
	}{
		{live, "published", 7, 109}, {tombstone, "unknown_legacy", 8, 64}, {receiptOnly, "unknown_legacy", 9, 54},
	} {
		var state string
		var sequence, bytes int64
		if err := db.QueryRow(`SELECT source_state,high_water_sequence,logical_bytes FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, expected.object).Scan(&state, &sequence, &bytes); err != nil || state != expected.state || sequence != expected.sequence || bytes != expected.bytes {
			t.Fatalf("upgraded object %s state=%q sequence=%d bytes=%d err=%v", expected.object, state, sequence, bytes, err)
		}
	}
	var profile string
	var receiptRows int
	if err := db.QueryRow(`SELECT exchange_profile FROM federation_peers WHERE origin=?`, origin).Scan(&profile); err != nil || profile != federationExchangeV1 {
		t.Fatalf("peer profile=%q err=%v", profile, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_retention_receipts`).Scan(&receiptRows); err != nil || receiptRows != 0 {
		t.Fatalf("promoted receipts=%d err=%v", receiptRows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != latestSchemaVersion() {
		t.Fatalf("restarted migration version=%d, want latest %d", got, latestSchemaVersion())
	}
	var after int64
	if err := db.QueryRow(`SELECT logical_bytes FROM federation_remote_catalog WHERE source_origin=? AND object_id=?`, origin, live).Scan(&after); err != nil || after != 109 {
		t.Fatalf("restart changed catalog bytes=%d err=%v", after, err)
	}
}
