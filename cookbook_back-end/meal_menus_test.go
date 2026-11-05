package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/menus"
)

func TestMealMenusOwnerACLRedactionAndComposedRoutes(t *testing.T) {
	db := testDatabase(t)
	ownerRecipeID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	privateRecipeID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),('other','other@example.test','Other',0,'now'),('admin','admin@example.test','Admin',1,'now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES(?, 'owner','Owner','Visible title','[]','[]','[]','private','owner_only',1,'now','now'),
		(?, 'other','Other','Secret title','[]','[]','[]','private','owner_only',1,'now','now')`, ownerRecipeID, privateRecipeID); err != nil {
		t.Fatal(err)
	}
	tokens := map[string]*http.Cookie{}
	for _, uid := range []string{"owner", "other", "admin"} {
		token, _, err := issueSession(db, uid)
		if err != nil {
			t.Fatal(err)
		}
		tokens[uid] = &http.Cookie{Name: sessionCookie, Value: token}
	}
	handler := handlerWithAuth(db, testAuth(db))
	created := callAPI(handler, http.MethodPost, "/api/menus", map[string]any{"title": "  Supper plan  ", "note": "Keep it simple", "creator_attribution": map[string]string{"kind": "local_profile"}}, tokens["owner"])
	var menu mealMenuResponse
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &menu) != nil || menu.Title != "Supper plan" || menu.Version != 1 || len(menu.CreatorAttributions) != 1 || menu.CreatorAttributions[0].Profile == nil {
		t.Fatalf("create menu status=%d body=%s", created.Code, created.Body)
	}
	for _, uid := range []string{"other", "admin"} {
		response := callAPI(handler, http.MethodGet, "/api/menus/"+menu.ID, nil, tokens[uid])
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s read owner-private menu status=%d body=%s", uid, response.Code, response.Body)
		}
	}
	denied := callAPI(handler, http.MethodPost, "/api/menus/"+menu.ID+"/entries", map[string]any{"version": 1, "recipe_id": privateRecipeID}, tokens["owner"])
	if denied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("private recipe add status=%d body=%s", denied.Code, denied.Body)
	}
	invalidBeforeACL := callAPI(handler, http.MethodPost, "/api/menus/"+menu.ID+"/entries", map[string]any{"version": 1, "recipe_id": privateRecipeID, "note": strings.Repeat("x", 2001)}, tokens["owner"])
	if invalidBeforeACL.Code != http.StatusBadRequest || !strings.Contains(invalidBeforeACL.Body.String(), "invalid_menu") {
		t.Fatalf("invalid note plus unreadable target did not fail validation first: status=%d body=%s", invalidBeforeACL.Code, invalidBeforeACL.Body)
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM meal_menus WHERE id=?`, menu.ID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("rejected add committed reservation version=%d err=%v", version, err)
	}
	var entryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menu_entries WHERE menu_id=?`, menu.ID).Scan(&entryCount); err != nil || entryCount != 0 {
		t.Fatalf("rejected add left an entry count=%d err=%v", entryCount, err)
	}
	added := callAPI(handler, http.MethodPost, "/api/menus/"+menu.ID+"/entries", map[string]any{"version": 1, "recipe_id": ownerRecipeID, "label": "Main", "note": "Owner note", "serving_note": "Warm"}, tokens["owner"])
	if added.Code != http.StatusCreated || json.Unmarshal(added.Body.Bytes(), &menu) != nil || menu.Version != 2 || len(menu.Entries) != 1 || !menu.Entries[0].Available || menu.Entries[0].Recipe == nil || menu.Entries[0].Recipe.Title != "Visible title" || strings.Contains(added.Body.String(), `"url"`) {
		t.Fatalf("add readable recipe status=%d body=%s", added.Code, added.Body)
	}
	entryID := menu.Entries[0].ID
	otherCreated := callAPI(handler, http.MethodPost, "/api/menus", map[string]string{"title": "Separate menu"}, tokens["owner"])
	var otherMenu mealMenuResponse
	if otherCreated.Code != http.StatusCreated || json.Unmarshal(otherCreated.Body.Bytes(), &otherMenu) != nil {
		t.Fatalf("create second menu status=%d body=%s", otherCreated.Code, otherCreated.Body)
	}
	otherAdded := callAPI(handler, http.MethodPost, "/api/menus/"+otherMenu.ID+"/entries", map[string]any{"version": 1, "recipe_id": ownerRecipeID}, tokens["owner"])
	if otherAdded.Code != http.StatusCreated || json.Unmarshal(otherAdded.Body.Bytes(), &otherMenu) != nil || len(otherMenu.Entries) != 1 {
		t.Fatalf("add second-menu entry status=%d body=%s", otherAdded.Code, otherAdded.Body)
	}
	foreignEntry := callAPI(handler, http.MethodPut, "/api/menus/"+menu.ID+"/entries/"+otherMenu.Entries[0].ID, map[string]any{"version": 2, "label": "Wrong menu"}, tokens["owner"])
	if foreignEntry.Code != http.StatusNotFound {
		t.Fatalf("entry from another menu was mutable: status=%d body=%s", foreignEntry.Code, foreignEntry.Body)
	}
	var unchangedVersion int
	if err := db.QueryRow(`SELECT version FROM meal_menus WHERE id=?`, menu.ID).Scan(&unchangedVersion); err != nil || unchangedVersion != 2 {
		t.Fatalf("cross-menu entry attempt committed version=%d err=%v", unchangedVersion, err)
	}
	if _, err := db.Exec(`UPDATE recipes SET owner_id='other',title='Private secret' WHERE id=?`, ownerRecipeID); err != nil {
		t.Fatal(err)
	}
	read := callAPI(handler, http.MethodGet, "/api/menus/"+menu.ID, nil, tokens["owner"])
	if read.Code != http.StatusOK {
		t.Fatalf("read after ACL loss status=%d body=%s", read.Code, read.Body)
	}
	var redacted mealMenuResponse
	if err := json.Unmarshal(read.Body.Bytes(), &redacted); err != nil {
		t.Fatal(err)
	}
	if len(redacted.Entries) != 1 || redacted.Entries[0].Available || redacted.Entries[0].Recipe != nil || strings.Contains(read.Body.String(), ownerRecipeID) || strings.Contains(read.Body.String(), "Private secret") {
		t.Fatalf("unavailable target was disclosed: %s", read.Body)
	}
	if _, err := db.Exec(`UPDATE meal_menu_entries SET local_instance_id='cccccccccccccccccccccccccccccccc' WHERE id=?`, entryID); err != nil {
		t.Fatal(err)
	}
	mismatched := callAPI(handler, http.MethodGet, "/api/menus/"+menu.ID, nil, tokens["owner"])
	if mismatched.Code != http.StatusOK || strings.Contains(mismatched.Body.String(), ownerRecipeID) || strings.Contains(mismatched.Body.String(), "Private secret") {
		t.Fatalf("mismatched local identity was resolved: status=%d body=%s", mismatched.Code, mismatched.Body)
	}
	if _, err := db.Exec(`DELETE FROM recipes WHERE id=?`, ownerRecipeID); err != nil {
		t.Fatal(err)
	}
	deletedTarget := callAPI(handler, http.MethodGet, "/api/menus/"+menu.ID, nil, tokens["owner"])
	if deletedTarget.Code != http.StatusOK || !strings.Contains(deletedTarget.Body.String(), "Owner note") || strings.Contains(deletedTarget.Body.String(), ownerRecipeID) {
		t.Fatalf("recipe deletion did not retain a redacted owner slot: status=%d body=%s", deletedTarget.Code, deletedTarget.Body)
	}
	updated := callAPI(handler, http.MethodPut, "/api/menus/"+menu.ID+"/entries/"+entryID, map[string]any{"version": 2, "label": "Still dinner", "note": "Retain this", "serving_note": "Covered"}, tokens["owner"])
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &redacted) != nil || redacted.Version != 3 || redacted.Entries[0].Label != "Still dinner" || redacted.Entries[0].Available {
		t.Fatalf("edit unavailable slot status=%d body=%s", updated.Code, updated.Body)
	}
	stale := callAPI(handler, http.MethodPut, "/api/menus/"+menu.ID, map[string]any{"version": 2, "title": "Stale", "note": ""}, tokens["owner"])
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale menu update status=%d body=%s", stale.Code, stale.Body)
	}
	updatedMenu := callAPI(handler, http.MethodPut, "/api/menus/"+menu.ID, map[string]any{"version": 3, "title": "Updated plan", "note": "New note"}, tokens["owner"])
	if updatedMenu.Code != http.StatusOK || json.Unmarshal(updatedMenu.Body.Bytes(), &menu) != nil || menu.Version != 4 || menu.Title != "Updated plan" {
		t.Fatalf("menu update status=%d body=%s", updatedMenu.Code, updatedMenu.Body)
	}
	library := callAPI(handler, http.MethodGet, "/api/library/menus", nil, tokens["owner"])
	if library.Code != http.StatusOK || !strings.Contains(library.Body.String(), menu.ID) {
		t.Fatalf("owner library status=%d body=%s", library.Code, library.Body)
	}
	for _, path := range []string{"/api/menus/" + menu.ID + "/", "/api//menus/" + menu.ID, "/api/menus/" + menu.ID + "%2fentries", "/api/library/menus/extra"} {
		response := callAPI(handler, http.MethodGet, path, nil, tokens["owner"])
		if response.Code != http.StatusNotFound {
			t.Fatalf("canonical path variant %q status=%d body=%s", path, response.Code, response.Body)
		}
	}
	deleted := callAPI(handler, http.MethodDelete, "/api/menus/"+menu.ID+"?version=4", nil, tokens["admin"])
	if deleted.Code != http.StatusNotFound {
		t.Fatalf("administrator bypassed owner ACL: status=%d body=%s", deleted.Code, deleted.Body)
	}
	deleted = callAPI(handler, http.MethodDelete, "/api/menus/"+menu.ID+"?version=4", nil, tokens["owner"])
	if deleted.Code != http.StatusOK {
		t.Fatalf("owner menu deletion status=%d body=%s", deleted.Code, deleted.Body)
	}
	missing := callAPI(handler, http.MethodGet, "/api/menus/"+menu.ID, nil, tokens["owner"])
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted menu remained readable: status=%d body=%s", missing.Code, missing.Body)
	}
}

func TestMealMenuCreatorChoiceAndFailureRollback(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	handler := handlerWithAuth(db, testAuth(db))
	for _, choice := range []any{nil, map[string]string{"kind": "unknown"}, map[string]string{"kind": "name_claim", "name": "A cook"}} {
		body := map[string]any{"title": "Menu"}
		if choice != nil {
			body["creator_attribution"] = choice
		}
		response := callAPI(handler, http.MethodPost, "/api/menus", body, cookie)
		if response.Code != http.StatusCreated {
			t.Fatalf("creator choice %v status=%d body=%s", choice, response.Code, response.Body)
		}
	}
	var profiles, attrs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM creator_profiles`).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM content_attributions WHERE menu_id IS NOT NULL`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 || attrs != 0 {
		t.Fatalf("unknown/name/omitted creator created local profile or identity attribution: profiles=%d attrs=%d", profiles, attrs)
	}
	paged := callAPI(handler, http.MethodGet, "/api/library/menus?limit=1&offset=2", nil, cookie)
	var page mealMenuPage
	if paged.Code != http.StatusOK || json.Unmarshal(paged.Body.Bytes(), &page) != nil || page.Total != 3 || page.Limit != 1 || page.Offset != 2 || len(page.Menus) != 1 {
		t.Fatalf("paged owner Library status=%d body=%s", paged.Code, paged.Body)
	}
	badPage := callAPI(handler, http.MethodGet, "/api/library/menus?limit=101", nil, cookie)
	if badPage.Code != http.StatusBadRequest {
		t.Fatalf("invalid menu page status=%d body=%s", badPage.Code, badPage.Body)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_creator_profile BEFORE INSERT ON creator_profiles BEGIN SELECT RAISE(ABORT,'injected profile failure'); END`); err != nil {
		t.Fatal(err)
	}
	failed := callAPI(handler, http.MethodPost, "/api/menus", map[string]any{"title": "Rollback", "creator_attribution": map[string]string{"kind": "local_profile"}}, cookie)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("injected profile failure status=%d body=%s", failed.Code, failed.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menus WHERE title='Rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed creator write left menu behind count=%d err=%v", count, err)
	}
}

func TestMealMenuAnonymousAndStrictInput(t *testing.T) {
	db := testDatabase(t)
	handler := handlerWithAuth(db, testAuth(db))
	if response := callAPI(handler, http.MethodGet, "/api/library/menus", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous Library status=%d", response.Code)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	response := callAPI(handler, http.MethodPost, "/api/menus", map[string]any{"title": "Menu", "private": true}, &http.Cookie{Name: sessionCookie, Value: token})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted status=%d body=%s", response.Code, response.Body)
	}
}

func TestMealMenuCompetingFileDatabaseUpdatesReserveVersionFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "menus.sqlite")
	db1, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db2.SetMaxOpenConns(1)
	defer db2.Close()
	if _, err := db2.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	created, err := (mealMenuUsecase{db: db1}).create(context.Background(), "owner", menuCreateInput{Title: "Concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*sql.DB{db1, db2} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			_, err := (mealMenuUsecase{db: db}).update(context.Background(), "owner", created.ID, 1, menuUpdateInput{Title: "Concurrent update", Note: "same expected version"})
			results <- err
		}(db)
	}
	close(start)
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, menus.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent update error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("competing expected-version updates successes=%d conflicts=%d", successes, conflicts)
	}
	var version int
	var title string
	if err := db1.QueryRow(`SELECT version,title FROM meal_menus WHERE id=?`, created.ID).Scan(&version, &title); err != nil || version != 2 || title != "Concurrent update" {
		t.Fatalf("serialized result version=%d title=%q err=%v", version, title, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := db1.PingContext(ctx); err != nil {
		t.Fatalf("file database did not remain usable: %v", err)
	}
}

func TestMealMenuPinnedConnectionReturnsBusyUntilReservationRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "menu-lock.sqlite")
	db1, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db2.SetMaxOpenConns(1)
	conn, err := db2.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON; PRAGMA busy_timeout=0;`); err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if _, err := db1.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	created, err := (mealMenuUsecase{db: db1}).create(context.Background(), "owner", menuCreateInput{Title: "Lock"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db1.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (menus.Store{}).Reserve(ctx, tx, created.ID, "owner", 1, timestamp()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	busyTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_, busyErr := (menus.Store{}).Reserve(ctx, busyTx, created.ID, "owner", 1, timestamp())
	if busyErr == nil || (!strings.Contains(strings.ToUpper(busyErr.Error()), "SQLITE_BUSY") && !strings.Contains(strings.ToUpper(busyErr.Error()), "SQLITE_LOCKED")) {
		_ = busyTx.Rollback()
		_ = tx.Rollback()
		t.Fatalf("pinned second writer error=%v, want an actual SQLite BUSY/LOCKED result", busyErr)
	}
	if err := busyTx.Rollback(); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	retryTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (menus.Store{}).Reserve(ctx, retryTx, created.ID, "owner", 2, timestamp()); err != nil {
		_ = retryTx.Rollback()
		t.Fatalf("pinned writer did not succeed after the reservation was released: %v", err)
	}
	if err := retryTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db1.QueryRow(`SELECT version FROM meal_menus WHERE id=?`, created.ID).Scan(&version); err != nil || version != 3 {
		t.Fatalf("post-contention version=%d err=%v", version, err)
	}
}

func TestMealMenuDuplicateOrderRemovalAndEntryQuota(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,version,created_at,updated_at)
		VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner','Owner','Dish','[]','[]','[]','private','owner_only',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	handler := handlerWithAuth(db, testAuth(db))
	created := callAPI(handler, http.MethodPost, "/api/menus", map[string]string{"title": "Ordered"}, cookie)
	var item mealMenuResponse
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &item) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	for version := 1; version <= 2; version++ {
		response := callAPI(handler, http.MethodPost, "/api/menus/"+item.ID+"/entries", map[string]any{"version": version, "recipe_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, cookie)
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &item) != nil {
			t.Fatalf("duplicate reference %d status=%d body=%s", version, response.Code, response.Body)
		}
	}
	if len(item.Entries) != 2 || item.Entries[0].ID == item.Entries[1].ID || item.Entries[0].Recipe.ID != item.Entries[1].Recipe.ID {
		t.Fatalf("repeated recipe references were collapsed: %+v", item.Entries)
	}
	ids := []string{item.Entries[1].ID, item.Entries[0].ID}
	reordered := callAPI(handler, http.MethodPut, "/api/menus/"+item.ID+"/entries/order", map[string]any{"version": 3, "entry_ids": ids}, cookie)
	if reordered.Code != http.StatusOK || json.Unmarshal(reordered.Body.Bytes(), &item) != nil || item.Entries[0].ID != ids[0] || item.Entries[0].Position != 0 || item.Version != 4 {
		t.Fatalf("reorder status=%d body=%s", reordered.Code, reordered.Body)
	}
	removed := callAPI(handler, http.MethodDelete, "/api/menus/"+item.ID+"/entries/"+ids[0]+"?version=4", nil, cookie)
	if removed.Code != http.StatusOK || json.Unmarshal(removed.Body.Bytes(), &item) != nil || len(item.Entries) != 1 || item.Entries[0].Position != 0 || item.Version != 5 {
		t.Fatalf("remove did not compact order: status=%d body=%s", removed.Code, removed.Body)
	}
	if _, err := db.Exec(`DELETE FROM meal_menu_entries WHERE menu_id=?; WITH RECURSIVE seq(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM seq WHERE n<63) INSERT INTO meal_menu_entries(id,menu_id,position,label) SELECT printf('%032x',n+1),?,n,'Slot' FROM seq`, item.ID); err != nil {
		t.Fatal(err)
	}
	limited := callAPI(handler, http.MethodPost, "/api/menus/"+item.ID+"/entries", map[string]any{"version": 5, "recipe_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, cookie)
	if limited.Code != http.StatusConflict || !strings.Contains(limited.Body.String(), "menu_limit_reached") {
		t.Fatalf("entry quota status=%d body=%s", limited.Code, limited.Body)
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM meal_menus WHERE id=?`, item.ID).Scan(&version); err != nil || version != 5 {
		t.Fatalf("entry quota did not roll back reserved version=%d err=%v", version, err)
	}
}

func TestMealMenuAccountQuotaRejectsProvisionalCreate(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now');
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<1000)
		INSERT INTO meal_menus(id,owner_id,title,created_at,updated_at) SELECT printf('%032x',n),'owner','Menu','now','now' FROM seq`); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	response := callAPI(handlerWithAuth(db, testAuth(db)), http.MethodPost, "/api/menus", map[string]string{"title": "One too many"}, &http.Cookie{Name: sessionCookie, Value: token})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "menu_limit_reached") {
		t.Fatalf("account quota status=%d body=%s", response.Code, response.Body)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM meal_menus WHERE owner_id='owner'`).Scan(&count); err != nil || count != 1000 {
		t.Fatalf("quota attempt persisted provisional row count=%d err=%v", count, err)
	}
}

func TestMealMenuRecipeTargetsUseSharedAndPublishedReadPolicy(t *testing.T) {
	db := testDatabase(t)
	sharedID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	publicID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	privateID := "cccccccccccccccccccccccccccccccc"
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('owner','owner@example.test','Owner','now'),('author','author@example.test','Author','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookbooks(id,owner_id,title,created_at,updated_at) VALUES('shared-book','author','Shared book','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('shared-book','owner','viewer','now')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, title, visibility string }{{sharedID, "Shared dish", "private"}, {publicID, "Public dish", "public"}, {privateID, "Private dish", "private"}} {
		if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,visibility,edit_policy,created_at,updated_at) VALUES(?,'author','Author',?,?,'owner_only','now','now')`, row.id, row.title, row.visibility); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cookbook_recipes(cookbook_id,recipe_id,added_by,created_at) VALUES('shared-book',?,'author','now')`, sharedID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES(?,'published','off',0,'now')`, publicID); err != nil {
		t.Fatal(err)
	}
	token, _, err := issueSession(db, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	handler := handlerWithAuth(db, testAuth(db))
	created := callAPI(handler, http.MethodPost, "/api/menus", map[string]string{"title": "Readable"}, cookie)
	var menu mealMenuResponse
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &menu) != nil {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	for version, recipeID := range []string{sharedID, publicID} {
		response := callAPI(handler, http.MethodPost, "/api/menus/"+menu.ID+"/entries", map[string]any{"version": version + 1, "recipe_id": recipeID}, cookie)
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &menu) != nil {
			t.Fatalf("readable target %s status=%d body=%s", recipeID, response.Code, response.Body)
		}
	}
	if len(menu.Entries) != 2 || !menu.Entries[0].Available || !menu.Entries[1].Available || menu.Entries[0].Recipe.Title != "Shared dish" || menu.Entries[1].Recipe.Title != "Public dish" {
		t.Fatalf("shared/public recipe summaries missing: %+v", menu.Entries)
	}
	denied := callAPI(handler, http.MethodPost, "/api/menus/"+menu.ID+"/entries", map[string]any{"version": 3, "recipe_id": privateID}, cookie)
	if denied.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unreadable private target status=%d body=%s", denied.Code, denied.Body)
	}
	if _, err := db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id='shared-book' AND user_id='owner'`); err != nil {
		t.Fatal(err)
	}
	read := callAPI(handler, http.MethodGet, "/api/menus/"+menu.ID, nil, cookie)
	var afterRemoval mealMenuResponse
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &afterRemoval) != nil || afterRemoval.Entries[0].Available || afterRemoval.Entries[0].Recipe != nil || !afterRemoval.Entries[1].Available {
		t.Fatalf("menu target ACL did not follow membership/public policy: status=%d body=%s", read.Code, read.Body)
	}
}
