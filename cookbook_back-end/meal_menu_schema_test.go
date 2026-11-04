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

func TestMealMenuMigrationFromV14PreservesAttributionsAndConstraints(t *testing.T) {
	ctx := context.Background()
	db := rawMigrationDB(t, filepath.Join(t.TempDir(), "meal-menu-v14.db"))
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	migrations := registeredSchemaMigrations()
	if len(migrations) != 15 || migrations[14].version != 15 || migrations[14].name != "private-meal-menu-schema" {
		t.Fatalf("registered migration count=%d latest=%+v, want v15 private-meal-menu-schema", len(migrations), migrations[len(migrations)-1])
	}
	if err := runSchemaMigrations(ctx, db, migrations[:14]); err != nil {
		t.Fatalf("create genuine v14 database: %v", err)
	}
	assertNoIncomingAttributionReferences(t, db)
	if _, err := db.Exec(`
		INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES('recipe','owner','','Recipe','now','now');
		INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES('book','owner','Book','now','now');
		INSERT INTO courses(id,owner_id,title,created_at,updated_at) VALUES('course','owner','Course','now','now');
		INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		 VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner','Private name','private','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,creator_id,role,provenance,created_at)
		 VALUES('11111111111111111111111111111111','recipe','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','recipe-local');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		 VALUES('22222222222222222222222222222222','recipe','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','cccccccccccccccccccccccccccccccc','Recipe credit','contributor','portable_claim','recipe-claim');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at)
		 VALUES('33333333333333333333333333333333','book','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','local_record_creation','book-local');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		 VALUES('44444444444444444444444444444444','book','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','dddddddddddddddddddddddddddddddd','Book credit','contributor','portable_claim','book-claim');
		INSERT INTO content_attributions(id,course_id,subject_kind,creator_id,role,provenance,created_at)
		 VALUES('55555555555555555555555555555555','course','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','course-local');
		INSERT INTO content_attributions(id,course_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		 VALUES('66666666666666666666666666666666','course','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','Course credit','creator','portable_claim','course-claim');
	`); err != nil {
		t.Fatalf("seed v14 attribution rows: %v", err)
	}
	v14Rows := readMealMenuV14Attributions(t, db)
	v14Indexes := readMealMenuAttributionIndexes(t, db)
	v14ForeignKeys := readCourseAttributionForeignKeys(t, db)
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("upgrade v14 database: %v", err)
	}
	if got := readMealMenuV14Attributions(t, db); !reflect.DeepEqual(got, v14Rows) {
		t.Fatalf("migration changed v14 attribution rows:\nv14=%#v\nv15=%#v", v14Rows, got)
	}
	if got := readMealMenuAttributionIndexes(t, db); !reflect.DeepEqual(got, v14Indexes) {
		t.Fatalf("migration changed old attribution indexes:\nv14=%#v\nv15=%#v", v14Indexes, got)
	}
	wantForeignKeys := append([]courseAttributionForeignKey(nil), v14ForeignKeys...)
	wantForeignKeys = append(wantForeignKeys, courseAttributionForeignKey{table: "meal_menus", from: "menu_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE", match: "NONE"})
	sort.Slice(wantForeignKeys, func(i, j int) bool { return wantForeignKeys[i].from < wantForeignKeys[j].from })
	if got := readCourseAttributionForeignKeys(t, db); !reflect.DeepEqual(got, wantForeignKeys) {
		t.Fatalf("v15 attribution foreign keys=%#v, want prior keys plus menu cascade %#v", got, wantForeignKeys)
	}
	assertCourseAttributionSchemaForeignKeys(t, db)
	if got := migrationVersion(t, db); got != 15 {
		t.Fatalf("upgraded schema version=%d, want 15", got)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("idempotent migration restart: %v", err)
	}
	if got := migrationVersion(t, db); got != 15 {
		t.Fatalf("restart schema version=%d, want 15", got)
	}
}

func TestFreshMealMenuSchemaBoundsTargetsAndCascades(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner','Private name','private','now','now')`); err != nil {
		t.Fatal(err)
	}
	if got := readCourseAttributionForeignKeys(t, db); !containsForeignKey(got, courseAttributionForeignKey{table: "meal_menus", from: "menu_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE", match: "NONE"}) {
		t.Fatalf("menu attribution FK missing: %#v", got)
	}
	var recipeForeignKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('meal_menu_entries') WHERE "table"='recipes'`).Scan(&recipeForeignKeys); err != nil || recipeForeignKeys != 0 {
		t.Fatalf("menu entries have %d recipe foreign keys, err=%v; recipe references must remain referential", recipeForeignKeys, err)
	}
	maxTitle := strings.Repeat("🥣", 160)
	if _, err := db.Exec(`INSERT INTO meal_menus(id,owner_id,title,created_at,updated_at)
		VALUES('11111111111111111111111111111111','owner',?,'now','now')`, maxTitle); err != nil {
		t.Fatalf("title at 160 Unicode characters and 640 bytes rejected: %v", err)
	}
	if _, err := db.Exec(`UPDATE meal_menus SET version=1.5 WHERE id='11111111111111111111111111111111'`); err == nil {
		t.Fatal("menu accepted a non-integer version")
	}
	if _, err := db.Exec(`UPDATE meal_menus SET note=?,creator_name=? WHERE id='11111111111111111111111111111111'`, strings.Repeat("🥣", 4000), strings.Repeat("🥣", 80)); err != nil {
		t.Fatalf("maximum menu note and creator name rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meal_menus(id,owner_id,title,created_at,updated_at)
		VALUES('22222222222222222222222222222222','owner',?,'now','now')`, strings.Repeat("🥣", 161)); err == nil {
		t.Fatal("title over character and byte bound was accepted")
	}
	if _, err := db.Exec(`INSERT INTO meal_menus(id,owner_id,title,created_at,updated_at)
		VALUES('22222222222222222222222222222222','owner',?,'now','now')`, strings.Repeat("t", 161)); err == nil {
		t.Fatal("title over character bound but under byte bound was accepted")
	}
	for _, statement := range []struct {
		name string
		sql  string
		arg  any
	}{
		{name: "blank title", sql: `INSERT INTO meal_menus(id,owner_id,title,created_at,updated_at) VALUES('22222222222222222222222222222222','owner',?,'now','now')`, arg: "  "},
		{name: "menu note", sql: `INSERT INTO meal_menus(id,owner_id,title,note,created_at,updated_at) VALUES('22222222222222222222222222222222','owner','Title',?,'now','now')`, arg: strings.Repeat("n", 16001)},
		{name: "menu note character limit", sql: `INSERT INTO meal_menus(id,owner_id,title,note,created_at,updated_at) VALUES('22222222222222222222222222222222','owner','Title',?,'now','now')`, arg: strings.Repeat("n", 4001)},
		{name: "creator name", sql: `INSERT INTO meal_menus(id,owner_id,title,creator_name,created_at,updated_at) VALUES('22222222222222222222222222222222','owner','Title',?,'now','now')`, arg: strings.Repeat("n", 321)},
		{name: "creator name character limit", sql: `INSERT INTO meal_menus(id,owner_id,title,creator_name,created_at,updated_at) VALUES('22222222222222222222222222222222','owner','Title',?,'now','now')`, arg: strings.Repeat("n", 81)},
	} {
		if _, err := db.Exec(statement.sql, statement.arg); err == nil {
			t.Errorf("invalid %s was accepted", statement.name)
		}
	}
	menuID := "11111111111111111111111111111111"
	if _, err := db.Exec(`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id,label,note,serving_note)
		VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,0,NULL,NULL,?,?,?)`, menuID, strings.Repeat("🥣", 80), strings.Repeat("🥣", 2000), strings.Repeat("🥣", 160)); err != nil {
		t.Fatalf("valid unresolved entry at field bounds rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id)
		VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,1,'cccccccccccccccccccccccccccccccc','dddddddddddddddddddddddddddddddd')`, menuID); err != nil {
		t.Fatalf("nonexistent recipe target was blocked by a recipe FK: %v", err)
	}
	for _, statement := range []struct {
		name string
		sql  string
		arg  any
	}{
		{name: "entry label", sql: `INSERT INTO meal_menu_entries(id,menu_id,position,label) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,1,?)`, arg: strings.Repeat("n", 321)},
		{name: "entry label character limit", sql: `INSERT INTO meal_menu_entries(id,menu_id,position,label) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,1,?)`, arg: strings.Repeat("n", 81)},
		{name: "entry note", sql: `INSERT INTO meal_menu_entries(id,menu_id,position,note) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,1,?)`, arg: strings.Repeat("n", 8001)},
		{name: "entry note character limit", sql: `INSERT INTO meal_menu_entries(id,menu_id,position,note) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,1,?)`, arg: strings.Repeat("n", 2001)},
		{name: "serving note", sql: `INSERT INTO meal_menu_entries(id,menu_id,position,serving_note) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,1,?)`, arg: strings.Repeat("n", 641)},
		{name: "serving note character limit", sql: `INSERT INTO meal_menu_entries(id,menu_id,position,serving_note) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,1,?)`, arg: strings.Repeat("n", 161)},
	} {
		if _, err := db.Exec(statement.sql, menuID, statement.arg); err == nil {
			t.Errorf("overlong %s was accepted", statement.name)
		}
	}
	for _, statement := range []string{
		`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','11111111111111111111111111111111',2,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',NULL)`,
		`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','11111111111111111111111111111111',2,NULL,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`,
		`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','11111111111111111111111111111111',2,'bad','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`,
		`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','11111111111111111111111111111111',64,NULL,NULL)`,
		`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','11111111111111111111111111111111',2.5,NULL,NULL)`,
		`INSERT INTO meal_menu_entries(id,menu_id,position) VALUES('eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','11111111111111111111111111111111',0)`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Errorf("invalid entry accepted: %s", statement)
		}
	}
	for position := 2; position < 64; position++ {
		id := fmtMenuID(position)
		if _, err := db.Exec(`INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id)
			VALUES(?,?,?,NULL,NULL)`, id, menuID, position); err != nil {
			t.Fatalf("insert entry at position %d: %v", position, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO meal_menu_entries(id,menu_id,position) VALUES('ffffffffffffffffffffffffffffffff',?,0)`, menuID); err == nil {
		t.Fatal("menu accepted a 65th entry")
	}
	if _, err := db.Exec(`INSERT INTO content_attributions(id,menu_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('77777777777777777777777777777777',?,'local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','now')`, menuID); err != nil {
		t.Fatalf("valid menu-local attribution rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO content_attributions(id,menu_id,subject_kind,claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at)
		VALUES('88888888888888888888888888888888',?,'external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','cccccccccccccccccccccccccccccccc','Creator name','creator','portable_claim','now')`, menuID); err != nil {
		t.Fatalf("valid menu external claim rejected: %v", err)
	}
	if !migrationObjectExists(t, db, "index", "content_attributions_local_menu_uq") || !migrationObjectExists(t, db, "index", "content_attributions_claim_menu_uq") {
		t.Fatal("menu attribution indexes are missing")
	}
	for _, statement := range []string{
		`INSERT INTO content_attributions(id,menu_id,subject_kind,creator_id,role,provenance,created_at) VALUES('88888888888888888888888888888887','11111111111111111111111111111111','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','duplicate-local')`,
		`INSERT INTO content_attributions(id,menu_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at) VALUES('88888888888888888888888888888886','11111111111111111111111111111111','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','cccccccccccccccccccccccccccccccc','creator','portable_claim','duplicate-claim')`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Errorf("duplicate menu attribution accepted: %s", statement)
		}
	}
	for _, statement := range []string{
		`INSERT INTO content_attributions(id,menu_id,recipe_id,subject_kind,creator_id,role,provenance,created_at) VALUES('99999999999999999999999999999999','11111111111111111111111111111111','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','self_asserted_at_creation','bad-two-targets')`,
		`INSERT INTO content_attributions(id,menu_id,subject_kind,creator_id,role,provenance,created_at) VALUES('99999999999999999999999999999998','11111111111111111111111111111111','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','contributor','self_asserted_at_creation','bad-menu-role')`,
		`INSERT INTO content_attributions(id,menu_id,subject_kind,creator_id,role,provenance,created_at) VALUES('99999999999999999999999999999997','11111111111111111111111111111111','local','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','creator','local_record_creation','bad-menu-provenance')`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Errorf("invalid menu attribution accepted: %s", statement)
		}
	}
	if _, err := db.Exec(`DELETE FROM creator_profiles WHERE creator_id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`); err == nil {
		t.Fatal("menu attribution did not preserve creator profile RESTRICT action")
	}
	if _, err := db.Exec(`DELETE FROM meal_menus WHERE id=?`, menuID); err != nil {
		t.Fatalf("delete menu: %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM meal_menu_entries WHERE menu_id=?) + (SELECT COUNT(*) FROM content_attributions WHERE menu_id=?)`, menuID, menuID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("menu cascade left %d entries or attributions, err=%v", remaining, err)
	}
	for i := 0; i < 1000; i++ {
		id := fmtMenuID(1000 + i)
		if _, err := db.Exec(`INSERT INTO meal_menus(id,owner_id,title,created_at,updated_at) VALUES(?,'owner','Menu','now','now')`, id); err != nil {
			t.Fatalf("insert account menu %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO meal_menus(id,owner_id,title,created_at,updated_at) VALUES('99999999999999999999999999999996','owner','Over quota','now','now')`); err == nil {
		t.Fatal("account accepted a 1001st menu")
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id='owner'`); err != nil {
		t.Fatalf("delete menu owner: %v", err)
	}
	var ownedMenus int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menus WHERE owner_id='owner'`).Scan(&ownedMenus); err != nil || ownedMenus != 0 {
		t.Fatalf("owner cascade left %d menus, err=%v", ownedMenus, err)
	}
}

func TestMealMenuMigrationFailureRollsBackAndCanRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meal-menu-rollback.db")
	db := rawMigrationDB(t, path)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	migrations := registeredSchemaMigrations()
	if err := runSchemaMigrations(ctx, db, migrations[:14]); err != nil {
		t.Fatalf("create v14 database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES('recipe','owner','','Recipe','now','now');
		INSERT INTO content_attributions(id,recipe_id,subject_kind,claim_source_instance_id,claim_creator_id,role,provenance,created_at)
		VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','recipe','external_claim','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','cccccccccccccccccccccccccccccccc','creator','portable_claim','now')`); err != nil {
		t.Fatal(err)
	}
	before := readMealMenuV14Attributions(t, db)
	failed := append([]schemaMigration(nil), migrations...)
	actualApply := failed[14].apply
	failed[14].apply = func(ctx context.Context, exec migrationExecutor) error {
		if err := actualApply(ctx, exec); err != nil {
			return err
		}
		return errors.New("injected after menu attribution rebuild")
	}
	if err := runSchemaMigrations(ctx, db, failed); err == nil || !strings.Contains(err.Error(), "injected after menu attribution rebuild") {
		t.Fatalf("injected migration failure=%v, want injected error", err)
	}
	if got := migrationVersion(t, db); got != 14 {
		t.Fatalf("failed migration advanced ledger to %d, want v14", got)
	}
	if migrationObjectExists(t, db, "table", "meal_menus") || migrationObjectExists(t, db, "table", "meal_menu_entries") || migrationObjectExists(t, db, "table", "content_attributions_v15") {
		t.Fatal("failed migration left menu or replacement attribution tables")
	}
	if got := readMealMenuV14Attributions(t, db); !reflect.DeepEqual(got, before) {
		t.Fatalf("rollback changed v14 attribution rows: before=%#v after=%#v", before, got)
	}
	if err := runSchemaMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("retry migration after rollback: %v", err)
	}
	if got := migrationVersion(t, db); got != 15 {
		t.Fatalf("retried schema version=%d, want v15", got)
	}
	if got := readMealMenuV14Attributions(t, db); !reflect.DeepEqual(got, before) {
		t.Fatalf("successful retry changed old attribution rows: before=%#v after=%#v", before, got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := rawMigrationDB(t, path)
	defer reopened.Close()
	if err := runSchemaMigrations(ctx, reopened, migrations); err != nil {
		t.Fatalf("restart after successful retry: %v", err)
	}
	if got := migrationVersion(t, reopened); got != 15 {
		t.Fatalf("reopened schema version=%d, want v15", got)
	}
}

func readMealMenuV14Attributions(t *testing.T, db *sql.DB) [][12]sql.NullString {
	t.Helper()
	rows, err := db.Query(`SELECT id,recipe_id,cookbook_id,course_id,subject_kind,creator_id,
		claim_source_instance_id,claim_creator_id,claim_name,role,provenance,created_at
		FROM content_attributions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][12]sql.NullString
	for rows.Next() {
		var fields [12]sql.NullString
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

func readMealMenuAttributionIndexes(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE type='index' AND tbl_name='content_attributions' AND name IN (
		'content_attributions_claim_cookbook_uq','content_attributions_claim_course_uq','content_attributions_claim_recipe_uq',
		'content_attributions_local_cookbook_uq','content_attributions_local_course_uq','content_attributions_local_recipe_uq') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]string)
	for rows.Next() {
		var name string
		var ddl sql.NullString
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		if ddl.Valid {
			got[name] = strings.Join(strings.Fields(ddl.String), " ")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"content_attributions_claim_cookbook_uq": "CREATE UNIQUE INDEX content_attributions_claim_cookbook_uq ON content_attributions(cookbook_id,claim_source_instance_id,claim_creator_id,role) WHERE cookbook_id IS NOT NULL AND subject_kind='external_claim'",
		"content_attributions_claim_course_uq":   "CREATE UNIQUE INDEX content_attributions_claim_course_uq ON content_attributions(course_id,claim_source_instance_id,claim_creator_id,role) WHERE course_id IS NOT NULL AND subject_kind='external_claim'",
		"content_attributions_claim_recipe_uq":   "CREATE UNIQUE INDEX content_attributions_claim_recipe_uq ON content_attributions(recipe_id,claim_source_instance_id,claim_creator_id,role) WHERE recipe_id IS NOT NULL AND subject_kind='external_claim'",
		"content_attributions_local_cookbook_uq": "CREATE UNIQUE INDEX content_attributions_local_cookbook_uq ON content_attributions(cookbook_id,creator_id,role) WHERE cookbook_id IS NOT NULL AND subject_kind='local'",
		"content_attributions_local_course_uq":   "CREATE UNIQUE INDEX content_attributions_local_course_uq ON content_attributions(course_id,creator_id,role) WHERE course_id IS NOT NULL AND subject_kind='local'",
		"content_attributions_local_recipe_uq":   "CREATE UNIQUE INDEX content_attributions_local_recipe_uq ON content_attributions(recipe_id,creator_id,role) WHERE recipe_id IS NOT NULL AND subject_kind='local'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("v14 attribution indexes=%#v, want exact definitions %#v", got, want)
	}
	return got
}

func containsForeignKey(got []courseAttributionForeignKey, want courseAttributionForeignKey) bool {
	for _, key := range got {
		if key == want {
			return true
		}
	}
	return false
}

func fmtMenuID(n int) string {
	const digits = "0123456789abcdef"
	var result [32]byte
	for i := len(result) - 1; i >= 0; i-- {
		result[i] = digits[n&15]
		n >>= 4
	}
	return string(result[:])
}
