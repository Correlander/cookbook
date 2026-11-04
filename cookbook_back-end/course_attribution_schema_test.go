package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCourseAttributionMigrationFromV13PreservesRowsAndConstraints(t *testing.T) {
	ctx := context.Background()
	db := rawMigrationDB(t, filepath.Join(t.TempDir(), "course-attribution-v13.db"))
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	migrations := registeredSchemaMigrations()
	if len(migrations) != 14 || migrations[13].version != 14 || migrations[13].name != "course-creator-attribution" {
		t.Fatalf("registered latest migration count=%d version/name=%d/%q, want 14 course-creator-attribution", len(migrations), migrations[13].version, migrations[13].name)
	}
	if err := runSchemaMigrations(ctx, db, migrations[:13]); err != nil {
		t.Fatalf("create genuine v13 database: %v", err)
	}
	assertNoIncomingAttributionReferences(t, db)
	if _, err := db.Exec(`
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES
		 ('recipe-local','owner','','Recipe','now','now'),
		 ('recipe-claim','owner','','Claim recipe','now','now');
		INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES('book','owner','Book','now','now');
		INSERT INTO courses(id,owner_id,title,created_at,updated_at) VALUES('course','owner','Course','now','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		 VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner','Private name','private','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
		 VALUES('11111111111111111111111111111111','recipe-local','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','local-time');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		 VALUES('22222222222222222222222222222222','recipe-claim','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','cccccccccccccccccccccccccccccccc','Claimed name','contributor','portable_claim','claim-time');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at)
		 VALUES('33333333333333333333333333333333','book','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','local_record_creation','book-time');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		 VALUES('66666666666666666666666666666666','book','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','dddddddddddddddddddddddddddddddd','Book claim','contributor','portable_claim','book-claim-time');
	`); err != nil {
		t.Fatalf("seed v13 rows: %v", err)
	}
	v13Attributions := readCourseAttributionLegacyFields(t, db)
	v13Indexes := readCourseAttributionLegacyIndexes(t, db)
	assertCourseAttributionV13ForeignKeys(t, db)
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("upgrade v13 database: %v", err)
	}
	if got := readCourseAttributionLegacyFields(t, db); !reflect.DeepEqual(got, v13Attributions) {
		t.Fatalf("migration changed one or more old attribution fields:\nv13=%#v\nv14=%#v", v13Attributions, got)
	}
	if got := readCourseAttributionLegacyIndexes(t, db); !reflect.DeepEqual(got, v13Indexes) {
		t.Fatalf("migration changed one or more legacy index definitions:\nv13=%#v\nv14=%#v", v13Indexes, got)
	}
	var legacyCourseTargets int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE course_id IS NOT NULL`).Scan(&legacyCourseTargets); err != nil || legacyCourseTargets != 0 {
		t.Fatalf("migration assigned %d legacy course targets, err=%v", legacyCourseTargets, err)
	}
	if got := migrationVersion(t, db); got != 14 {
		t.Fatalf("migration version=%d, want 14", got)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions`).Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("preserved attribution rows=%d err=%v, want 4", rows, err)
	}
	var localRecipeID, creatorID, provenance, createdAt string
	if err := db.QueryRow(`SELECT id,creator_id,provenance,created_at FROM content_attributions WHERE recipe_id='recipe-local'`).Scan(&localRecipeID, &creatorID, &provenance, &createdAt); err != nil {
		t.Fatal(err)
	}
	if localRecipeID != "11111111111111111111111111111111" || creatorID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || provenance != "self_asserted_at_creation" || createdAt != "local-time" {
		t.Fatalf("local recipe attribution changed: id=%q creator=%q provenance=%q created=%q", localRecipeID, creatorID, provenance, createdAt)
	}
	var claimID, sourceID, claimCreator, claimName, claimProvenance string
	if err := db.QueryRow(`SELECT id,claim_source_instance_id,claim_creator_id,claim_name,provenance FROM content_attributions WHERE recipe_id='recipe-claim'`).Scan(&claimID, &sourceID, &claimCreator, &claimName, &claimProvenance); err != nil {
		t.Fatal(err)
	}
	if claimID != "22222222222222222222222222222222" || sourceID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || claimCreator != "cccccccccccccccccccccccccccccccc" || claimName != "Claimed name" || claimProvenance != "portable_claim" {
		t.Fatalf("external attribution changed: id=%q source=%q creator=%q name=%q provenance=%q", claimID, sourceID, claimCreator, claimName, claimProvenance)
	}
	var bookClaimID, bookClaimName, bookClaimCreatedAt string
	if err := db.QueryRow(`SELECT id,claim_name,created_at FROM content_attributions WHERE cookbook_id='book' AND subject_kind='external_claim'`).Scan(&bookClaimID, &bookClaimName, &bookClaimCreatedAt); err != nil {
		t.Fatal(err)
	}
	if bookClaimID != "66666666666666666666666666666666" || bookClaimName != "Book claim" || bookClaimCreatedAt != "book-claim-time" {
		t.Fatalf("external cookbook attribution changed: id=%q name=%q created=%q", bookClaimID, bookClaimName, bookClaimCreatedAt)
	}
	var emptyCreatorName string
	if err := db.QueryRow(`SELECT creator_name FROM courses WHERE id='course'`).Scan(&emptyCreatorName); err != nil || emptyCreatorName != "" {
		t.Fatalf("legacy course creator_name=%q err=%v, want empty without owner backfill", emptyCreatorName, err)
	}
	for _, index := range []string{
		"content_attributions_local_recipe_uq", "content_attributions_local_cookbook_uq", "content_attributions_claim_recipe_uq", "content_attributions_claim_cookbook_uq",
		"content_attributions_local_course_uq", "content_attributions_claim_course_uq",
	} {
		if !migrationObjectExists(t, db, "index", index) {
			t.Errorf("partial unique index %q is missing", index)
		}
	}
	assertCourseAttributionSchemaForeignKeys(t, db)

	if _, err := db.Exec(`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('44444444444444444444444444444444','course','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','new-local')`); err != nil {
		t.Fatalf("insert local course credit: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO content_attributions(id,course_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at)
		VALUES('55555555555555555555555555555555','course','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','dddddddddddddddddddddddddddddddd','creator','portable_claim','new-external')`); err != nil {
		t.Fatalf("insert external course credit: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO content_attributions(id,course_id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at) VALUES('66666666666666666666666666666666','course','recipe-local','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','creator','portable_claim','bad-two-targets')`,
		`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at) VALUES('77777777777777777777777777777777','course','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','adapter','adapter_source','bad-course-adapter')`,
		`INSERT INTO content_attributions(id,course_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at) VALUES('88888888888888888888888888888888','course','external_claim','bad','ffffffffffffffffffffffffffffffff','creator','portable_claim','bad-claim-id')`,
		`INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','recipe-local','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','contributor','self_asserted_at_creation','bad-old-provenance')`,
		`INSERT INTO content_attributions(id,course_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','course','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','dddddddddddddddddddddddddddddddd','invalid-role','portable_claim','bad-old-role')`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Errorf("new attribution constraints accepted invalid row: %s", statement)
		}
	}
	for _, statement := range []string{
		`INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at) VALUES('cccccccccccccccccccccccccccccccc','course','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','duplicate-local')`,
		`INSERT INTO content_attributions(id,course_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at) VALUES('dddddddddddddddddddddddddddddddd','course','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','dddddddddddddddddddddddddddddddd','creator','portable_claim','duplicate-external')`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Errorf("course attribution uniqueness constraint accepted duplicate: %s", statement)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO users(id,email,display_name,created_at) VALUES('course-credit-owner','credit@example.test','Credit Owner','now');
		INSERT INTO courses(id,owner_id,title,created_at,updated_at) VALUES('course-restrict','course-credit-owner','Restricted credit','now','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		 VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','course-credit-owner','Credit profile','private','now','now');
		INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at)
		 VALUES('ffffffffffffffffffffffffffffffff','course-restrict','local','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','creator','self_asserted_at_creation','restricted-local');
	`); err != nil {
		t.Fatalf("seed isolated local course attribution: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM creator_profiles WHERE creator_id='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'`); err == nil {
		t.Fatal("course attribution did not restrict creator profile deletion")
	}
	if _, err := db.Exec(`DELETE FROM courses WHERE id='course-restrict'`); err != nil {
		t.Fatalf("delete course for creator profile restriction check: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM creator_profiles WHERE creator_id='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'`); err != nil {
		t.Fatalf("delete unreferenced creator profile after course cascade: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM courses WHERE id='course'`); err != nil {
		t.Fatalf("delete course with attributions: %v", err)
	}
	var courseAttributions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE course_id='course'`).Scan(&courseAttributions); err != nil || courseAttributions != 0 {
		t.Fatalf("course cascade left %d attributions err=%v", courseAttributions, err)
	}
	if _, err := db.Exec(`DELETE FROM creator_profiles WHERE creator_id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`); err == nil {
		t.Fatal("creator profile deletion bypassed retained RESTRICT foreign key")
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("successful restart reran migration: %v", err)
	}
	if got := migrationVersion(t, db); got != 14 {
		t.Fatalf("restart migration version=%d, want 14", got)
	}
	var emptyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions`).Scan(&emptyCount); err != nil || emptyCount != 4 {
		t.Fatalf("restart changed remaining old attributions=%d err=%v", emptyCount, err)
	}
}

func TestFreshDatabaseUsesCourseAttributionSchema(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := migrationVersion(t, db); got != 14 {
		t.Fatalf("fresh migration version=%d, want 14", got)
	}
	var defaultName string
	if err := db.QueryRow(`SELECT dflt_value FROM pragma_table_info('courses') WHERE name='creator_name'`).Scan(&defaultName); err != nil || defaultName != "''" {
		t.Fatalf("fresh creator_name default=%q err=%v, want empty string", defaultName, err)
	}
	if !migrationObjectExists(t, db, "index", "content_attributions_local_course_uq") || !migrationObjectExists(t, db, "index", "content_attributions_claim_course_uq") {
		t.Fatal("fresh database omitted course attribution indexes")
	}
}

func TestCourseAttributionMigrationFailureRollsBackAndCanRetry(t *testing.T) {
	ctx := context.Background()
	db := rawMigrationDB(t, filepath.Join(t.TempDir(), "course-attribution-rollback.db"))
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:13]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES('recipe','owner','','Recipe','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at)
		VALUES('99999999999999999999999999999999','recipe','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','cccccccccccccccccccccccccccccccc','creator','portable_claim','now')`); err != nil {
		t.Fatal(err)
	}
	failed := append([]schemaMigration(nil), migrations...)
	actualApply := failed[13].apply
	failed[13].apply = func(ctx context.Context, exec migrationExecutor) error {
		if err := actualApply(ctx, exec); err != nil {
			return err
		}
		return errors.New("injected after schema rebuild")
	}
	if err := runSchemaMigrations(ctx, db, failed); err == nil {
		t.Fatal("injected failure after rebuild unexpectedly succeeded")
	}
	if got := migrationVersion(t, db); got != 13 {
		t.Fatalf("failed migration advanced ledger to %d, want 13", got)
	}
	if migrationObjectExists(t, db, "table", "content_attributions_v14") {
		t.Fatal("failed migration left its replacement table")
	}
	var creatorNameColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('courses') WHERE name='creator_name'`).Scan(&creatorNameColumns); err != nil || creatorNameColumns != 0 {
		t.Fatalf("failed migration left courses.creator_name column count=%d err=%v", creatorNameColumns, err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='content_attributions'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("rollback left content_attributions table count=%d err=%v", tables, err)
	}
	var recipeID string
	if err := db.QueryRow(`SELECT recipe_id FROM content_attributions WHERE id='99999999999999999999999999999999'`).Scan(&recipeID); err != nil || recipeID != "recipe" {
		t.Fatalf("rollback changed old attribution recipe_id=%q err=%v", recipeID, err)
	}
	if _, err := db.Exec(`INSERT INTO courses(id,owner_id,title,created_at,updated_at) VALUES('course','owner','Course','now','now')`); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("retry migration after rollback: %v", err)
	}
	var creatorName string
	if err := db.QueryRow(`SELECT creator_name FROM courses WHERE id='course'`).Scan(&creatorName); err != nil || creatorName != "" {
		t.Fatalf("new course creator_name=%q err=%v", creatorName, err)
	}
}

func assertCourseAttributionSchemaForeignKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	want := []courseAttributionForeignKey{
		{table: "cookbooks", from: "cookbook_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE", match: "NONE"},
		{table: "courses", from: "course_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE", match: "NONE"},
		{table: "creator_profiles", from: "creator_id", to: "creator_id", onUpdate: "NO ACTION", onDelete: "RESTRICT", match: "NONE"},
		{table: "recipes", from: "recipe_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE", match: "NONE"},
	}
	if got := readCourseAttributionForeignKeys(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("content_attributions foreign keys=%#v, want %#v", got, want)
	}
	check, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	if check.Next() {
		t.Fatal("foreign_key_check found a violation after migration")
	}
	if err := check.Err(); err != nil {
		t.Fatal(err)
	}
}

type courseAttributionForeignKey struct {
	table, from, to, onUpdate, onDelete, match string
}

func readCourseAttributionForeignKeys(t *testing.T, db *sql.DB) []courseAttributionForeignKey {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list(content_attributions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []courseAttributionForeignKey
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		got = append(got, courseAttributionForeignKey{table: table, from: from, to: to, onUpdate: onUpdate, onDelete: onDelete, match: match})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].from < got[j].from })
	return got
}

func assertCourseAttributionV13ForeignKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	want := []courseAttributionForeignKey{
		{table: "cookbooks", from: "cookbook_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE", match: "NONE"},
		{table: "creator_profiles", from: "creator_id", to: "creator_id", onUpdate: "NO ACTION", onDelete: "RESTRICT", match: "NONE"},
		{table: "recipes", from: "recipe_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE", match: "NONE"},
	}
	if got := readCourseAttributionForeignKeys(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("v13 content_attributions foreign keys=%#v, want %#v", got, want)
	}
}

func readCourseAttributionLegacyFields(t *testing.T, db *sql.DB) [][11]sql.NullString {
	t.Helper()
	rows, err := db.Query(`SELECT id,recipe_id,cookbook_id,subject_kind,creator_id,
		claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at
		FROM content_attributions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][11]sql.NullString
	for rows.Next() {
		var fields [11]sql.NullString
		dest := make([]any, len(fields))
		for i := range fields {
			dest[i] = &fields[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		got = append(got, fields)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func readCourseAttributionLegacyIndexes(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE type='index' AND tbl_name='content_attributions' AND name IN (
		'content_attributions_claim_cookbook_uq','content_attributions_claim_recipe_uq',
		'content_attributions_local_cookbook_uq','content_attributions_local_recipe_uq') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]string)
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		got[name] = strings.Join(strings.Fields(ddl), " ")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"content_attributions_claim_cookbook_uq": "CREATE UNIQUE INDEX content_attributions_claim_cookbook_uq ON content_attributions(cookbook_id,claim_source_instance_id,claim_creator_id,role) WHERE cookbook_id IS NOT NULL AND subject_kind='external_claim'",
		"content_attributions_claim_recipe_uq":   "CREATE UNIQUE INDEX content_attributions_claim_recipe_uq ON content_attributions(recipe_id,claim_source_instance_id,claim_creator_id,role) WHERE recipe_id IS NOT NULL AND subject_kind='external_claim'",
		"content_attributions_local_cookbook_uq": "CREATE UNIQUE INDEX content_attributions_local_cookbook_uq ON content_attributions(cookbook_id,creator_id,role) WHERE cookbook_id IS NOT NULL AND subject_kind='local'",
		"content_attributions_local_recipe_uq":   "CREATE UNIQUE INDEX content_attributions_local_recipe_uq ON content_attributions(recipe_id,creator_id,role) WHERE recipe_id IS NOT NULL AND subject_kind='local'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy index definitions=%#v, want exact normalized definitions %#v", got, want)
	}
	return got
}

func assertNoIncomingAttributionReferences(t *testing.T, db *sql.DB) {
	t.Helper()
	objects, err := db.Query(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND type IN ('table','trigger')`)
	if err != nil {
		t.Fatal(err)
	}
	type schemaObject struct{ kind, name, ddl string }
	var all []schemaObject
	for objects.Next() {
		var object schemaObject
		if err := objects.Scan(&object.kind, &object.name, &object.ddl); err != nil {
			objects.Close()
			t.Fatal(err)
		}
		all = append(all, object)
	}
	if err := objects.Close(); err != nil {
		t.Fatal(err)
	}
	for _, object := range all {
		if object.kind == "trigger" && strings.Contains(strings.ToLower(object.ddl), "content_attributions") {
			t.Fatalf("unexpected production trigger references content_attributions: %s", object.name)
		}
		if object.kind != "table" || object.name == "content_attributions" || object.name == "schema_migrations" {
			continue
		}
		rows, err := db.Query("PRAGMA foreign_key_list(\"" + strings.ReplaceAll(object.name, `"`, `""`) + "\")")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, seq int
			var table, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if table == "content_attributions" {
				rows.Close()
				t.Fatalf("incoming foreign key from %s.%s requires rebuild preservation handling", object.name, from)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
