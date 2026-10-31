package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupOrphanedMediaPreservesRevisionImages(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'); INSERT INTO recipes(id,owner_id,creator_name,title,created_at,updated_at) VALUES('recipe','owner','Owner','Soup','now','now'); INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at,image_path) VALUES('revision','recipe',1,'owner','Soup','','[]','[]','private','now','11111111111111111111111111111111.png')`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	retained := filepath.Join(dir, "11111111111111111111111111111111.png")
	orphan := filepath.Join(dir, "22222222222222222222222222222222.jpg")
	if err := os.WriteFile(retained, []byte("revision image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("unreferenced"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupOrphanedMedia(db, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(retained); err != nil {
		t.Fatalf("referenced revision image was removed: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan image still exists, err=%v", err)
	}
}
