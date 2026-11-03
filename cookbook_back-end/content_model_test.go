package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestDefaultRecipePublicationPolicyIsPrivateAndConstrained(t *testing.T) {
	policy := defaultRecipePublicationPolicy("recipe")
	if policy.RecipeID != "recipe" || policy.PublicationState != RecipePublicationUnpublished || policy.FederationMode != RecipeFederationOff || policy.IndexEligible || policy.UpdatedBy.Valid || policy.PublishedAt.Valid {
		t.Fatalf("default recipe publication policy = %#v", policy)
	}
	if err := validateRecipePublicationPolicy(policy); err != nil {
		t.Fatalf("default policy is invalid: %v", err)
	}

	for _, invalid := range []RecipePublicationPolicy{
		{PublicationState: RecipePublicationUnpublished, FederationMode: RecipeFederationOff},
		{RecipeID: "recipe", PublicationState: "scheduled", FederationMode: RecipeFederationOff},
		{RecipeID: "recipe", PublicationState: RecipePublicationUnpublished, FederationMode: "public"},
		{RecipeID: "recipe", PublicationState: RecipePublicationUnpublished, FederationMode: RecipeFederationFollowers},
		{RecipeID: "recipe", PublicationState: RecipePublicationWithdrawn, FederationMode: RecipeFederationOff, IndexEligible: true},
	} {
		if err := validateRecipePublicationPolicy(invalid); err == nil {
			t.Errorf("accepted invalid policy %#v", invalid)
		}
	}
}

func TestRecipePublicationPolicyMigrationBackfillsV2WithoutChangingLegacyRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v2-publication.db")
	db := rawMigrationDB(t, dbPath)
	ctx := context.Background()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	migrations := registeredSchemaMigrations()
	if len(migrations) != 4 {
		t.Fatalf("registered migrations=%d, want 4", len(migrations))
	}
	if err := runSchemaMigrations(ctx, db, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin='https://cookbook.example.test' WHERE id=1;
INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','user-created');
INSERT INTO recipes(id,owner_id,creator_name,title,visibility,version,created_at,updated_at,deleted_at) VALUES
 ('legacy-public','owner','Claim','Public preserve','public',4,'public-created','public-updated',NULL),
 ('legacy-private','owner','Claim','Private preserve','private',2,'private-created','private-updated',NULL),
 ('legacy-unlisted','owner','Claim','Unlisted preserve','unlisted',3,'unlisted-created','unlisted-updated',NULL),
 ('legacy-trashed','owner','Claim','Trashed preserve','public',5,'trash-created','trash-updated','trashed-at');
INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at)
 VALUES('legacy-revision','legacy-public',4,'owner','Public preserve','','[]','[]','public','revision-created');
INSERT INTO federation_peers(origin,enabled,created_at,updated_at) VALUES('https://peer.example.test',1,'peer-created','peer-updated');
INSERT INTO federation_local_objects(recipe_id,sequence,published) VALUES('legacy-public',7,1);
INSERT INTO federation_local_peer_objects(peer_origin,recipe_id,published,updated_at) VALUES('https://peer.example.test','legacy-public',1,'peer-object-updated');
INSERT INTO federation_outbox(id,peer_origin,event_id,object_id,sequence,payload,next_attempt,created_at)
 VALUES('queued','https://peer.example.test','event','object',7,X'010203',123,'outbox-created');`); err != nil {
		t.Fatal(err)
	}
	var beforeRecipes, beforeRevisions, beforePeers, beforeLocalObjects, beforePeerObjects, beforeOutbox int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM recipes),(SELECT COUNT(*) FROM recipe_revisions),(SELECT COUNT(*) FROM federation_peers),(SELECT COUNT(*) FROM federation_local_objects),(SELECT COUNT(*) FROM federation_local_peer_objects),(SELECT COUNT(*) FROM federation_outbox)`).Scan(&beforeRecipes, &beforeRevisions, &beforePeers, &beforeLocalObjects, &beforePeerObjects, &beforeOutbox); err != nil {
		t.Fatal(err)
	}
	if beforeRecipes != 4 || beforeRevisions != 1 || beforePeers != 1 || beforeLocalObjects != 1 || beforePeerObjects != 1 || beforeOutbox != 1 {
		t.Fatalf("fixture rows recipes=%d revisions=%d peers=%d local=%d peer-objects=%d outbox=%d", beforeRecipes, beforeRevisions, beforePeers, beforeLocalObjects, beforePeerObjects, beforeOutbox)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}

	wantState := map[string]RecipePublicationState{
		"legacy-public":   RecipePublicationPublished,
		"legacy-private":  RecipePublicationUnpublished,
		"legacy-unlisted": RecipePublicationUnpublished,
		"legacy-trashed":  RecipePublicationPublished,
	}
	for recipeID, want := range wantState {
		var state, federation string
		var indexEligible int
		var updatedBy, publishedAt sql.NullString
		var updatedAt string
		if err := db.QueryRow(`SELECT publication_state,federation_mode,index_eligible,updated_by,updated_at,published_at FROM recipe_publication_policy WHERE recipe_id=?`, recipeID).Scan(&state, &federation, &indexEligible, &updatedBy, &updatedAt, &publishedAt); err != nil {
			t.Fatal(err)
		}
		if RecipePublicationState(state) != want || federation != string(RecipeFederationOff) || indexEligible != 0 || updatedBy.Valid || updatedAt == "" || publishedAt.Valid {
			t.Errorf("policy for %s = state %q federation %q index %d updated_by %v updated_at %q published_at %v", recipeID, state, federation, indexEligible, updatedBy, updatedAt, publishedAt)
		}
	}
	var missingPolicies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipes r LEFT JOIN recipe_publication_policy p ON p.recipe_id=r.id WHERE p.recipe_id IS NULL`).Scan(&missingPolicies); err != nil || missingPolicies != 0 {
		t.Fatalf("recipes without a publication policy=%d err=%v", missingPolicies, err)
	}
	var policyRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_publication_policy`).Scan(&policyRows); err != nil || policyRows != beforeRecipes {
		t.Fatalf("policy row count=%d err=%v, want %d", policyRows, err, beforeRecipes)
	}
	var recipeCount, revisionCount, peerCount, localObjectCount, peerObjectCount, outboxCount int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM recipes),(SELECT COUNT(*) FROM recipe_revisions),(SELECT COUNT(*) FROM federation_peers),(SELECT COUNT(*) FROM federation_local_objects),(SELECT COUNT(*) FROM federation_local_peer_objects),(SELECT COUNT(*) FROM federation_outbox)`).Scan(&recipeCount, &revisionCount, &peerCount, &localObjectCount, &peerObjectCount, &outboxCount); err != nil {
		t.Fatal(err)
	}
	if recipeCount != beforeRecipes || revisionCount != beforeRevisions || peerCount != beforePeers || localObjectCount != beforeLocalObjects || peerObjectCount != beforePeerObjects || outboxCount != beforeOutbox {
		t.Fatalf("migration changed rows: recipes=%d revisions=%d peers=%d local=%d peer-objects=%d outbox=%d", recipeCount, revisionCount, peerCount, localObjectCount, peerObjectCount, outboxCount)
	}
	var creator, title, visibility, createdAt, updatedAt, deletedAt string
	var version int
	if err := db.QueryRow(`SELECT creator_name,title,visibility,version,created_at,updated_at,deleted_at FROM recipes WHERE id='legacy-trashed'`).Scan(&creator, &title, &visibility, &version, &createdAt, &updatedAt, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if creator != "Claim" || title != "Trashed preserve" || visibility != "public" || version != 5 || createdAt != "trash-created" || updatedAt != "trash-updated" || deletedAt != "trashed-at" {
		t.Fatalf("legacy recipe changed: creator=%q title=%q visibility=%q version=%d created=%q updated=%q deleted=%q", creator, title, visibility, version, createdAt, updatedAt, deletedAt)
	}
	if err := db.QueryRow(`SELECT title FROM recipe_revisions WHERE id='legacy-revision'`).Scan(&title); err != nil || title != "Public preserve" {
		t.Fatalf("legacy revision title=%q err=%v", title, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		rows.Close()
		t.Fatal("foreign_key_check reported a violation")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var policyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recipe_publication_policy`).Scan(&policyCount); err != nil || policyCount != beforeRecipes {
		t.Fatalf("policy rows after restart=%d err=%v, want %d", policyCount, err, beforeRecipes)
	}
	var versionAfterRestart int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&versionAfterRestart); err != nil || versionAfterRestart != 4 {
		t.Fatalf("migration version after restart=%d err=%v, want 4", versionAfterRestart, err)
	}
}

func TestRecipePublicationPolicyMigrationFailureRollsBackDDLRowsAndLedger(t *testing.T) {
	db := rawMigrationDB(t, ":memory:")
	ctx := context.Background()
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','created');
INSERT INTO recipes(id,owner_id,creator_name,title,visibility,created_at,updated_at) VALUES('recipe','owner','Claim','Keep','public','created','updated');`); err != nil {
		t.Fatal(err)
	}
	migrations := registeredSchemaMigrations()
	policyMigration := migrations[2]
	migrations[2].apply = func(ctx context.Context, exec migrationExecutor) error {
		if err := policyMigration.apply(ctx, exec); err != nil {
			return err
		}
		return errors.New("injected publication migration failure")
	}
	err := runSchemaMigrations(ctx, db, migrations)
	if err == nil || !containsError(err, "injected publication migration failure") {
		t.Fatalf("injected failure error=%v", err)
	}
	if migrationVersion(t, db) != 2 || migrationObjectExists(t, db, "table", "recipe_publication_policy") {
		t.Fatal("failed migration left policy DDL or advanced the migration ledger")
	}
	var title, visibility string
	if err := db.QueryRow(`SELECT title,visibility FROM recipes WHERE id='recipe'`).Scan(&title, &visibility); err != nil || title != "Keep" || visibility != "public" {
		t.Fatalf("legacy row after rollback title=%q visibility=%q err=%v", title, visibility, err)
	}
	if err := runSchemaMigrations(ctx, db, registeredSchemaMigrations()); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestDefaultRecipePublicationPolicyPersistsInCallerTransaction(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','created');
INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES('recipe','owner','Owner','Dish','created','updated')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertDefaultRecipePublicationPolicy(context.Background(), tx, "recipe"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state, federation string
	var index int
	var updatedBy, publishedAt sql.NullString
	if err := db.QueryRow(`SELECT publication_state,federation_mode,index_eligible,updated_by,published_at FROM recipe_publication_policy WHERE recipe_id='recipe'`).Scan(&state, &federation, &index, &updatedBy, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if state != string(RecipePublicationUnpublished) || federation != string(RecipeFederationOff) || index != 0 || updatedBy.Valid || publishedAt.Valid {
		t.Fatalf("persisted default policy state=%q federation=%q index=%d updated_by=%v published_at=%v", state, federation, index, updatedBy, publishedAt)
	}
	if _, err := db.Exec(`DELETE FROM recipe_publication_policy WHERE recipe_id='recipe'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('missing','unpublished','off',0,'now')`); err == nil {
		t.Fatal("policy FK accepted a missing recipe")
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('recipe','scheduled','off',0,'now')`); err == nil {
		t.Fatal("policy CHECK accepted an unknown publication state")
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES('recipe','unpublished','followers',0,'now')`); err == nil {
		t.Fatal("policy CHECK accepted external consent for an unpublished recipe")
	}
}

func containsError(err error, fragment string) bool {
	for err != nil {
		if err.Error() == fragment {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}
