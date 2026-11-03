package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupInstanceAccountByNormalizedExactEmailIsReadOnly(t *testing.T) {
	database := filepath.Join(t.TempDir(), "cookbook.db")
	db, err := openDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES('boot-id','bootstrap@example.test','Bootstrap',1,'now')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	lookupDB, err := openAccountLookupDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	defer lookupDB.Close()
	account, err := lookupInstanceAccountByEmail(context.Background(), lookupDB, "  BOOTSTRAP@example.test ")
	if err != nil || account.ID != "boot-id" || account.Email != "bootstrap@example.test" || account.DisplayName != "Bootstrap" {
		t.Fatalf("account=%+v err=%v", account, err)
	}
	if _, err := lookupDB.Exec(`UPDATE users SET is_admin=0 WHERE id='boot-id'`); err == nil {
		t.Fatal("read-only account lookup database accepted a write")
	}
	var admin int
	if err := lookupDB.QueryRow(`SELECT is_admin FROM users WHERE id='boot-id'`).Scan(&admin); err != nil || admin != 1 {
		t.Fatalf("lookup changed account role: admin=%d err=%v", admin, err)
	}
	if _, err := lookupInstanceAccountByEmail(context.Background(), lookupDB, "other@example.test"); !errors.Is(err, ErrInstanceAccountNotFound) {
		t.Fatalf("unknown exact email error=%v", err)
	}
	for _, input := range []string{"", "Bootstrap <bootstrap@example.test>", "bootstrap@example.test,other@example.test", "missing-at-sign"} {
		if _, err := lookupInstanceAccountByEmail(context.Background(), lookupDB, input); err == nil {
			t.Errorf("lookup accepted invalid/non-exact address %q", input)
		}
	}
}

func TestOpenAccountLookupDatabaseDoesNotCreateOrMigrate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := openAccountLookupDatabase(missing); err == nil {
		t.Fatal("account lookup opened a nonexistent database")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup created missing database: stat error=%v", err)
	}
	for _, path := range []string{"", ":memory:", "file:existing.db"} {
		if _, err := openAccountLookupDatabase(path); err == nil {
			t.Errorf("lookup accepted unsupported database path %q", path)
		}
	}
}
