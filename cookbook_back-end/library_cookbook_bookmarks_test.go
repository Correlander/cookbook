package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const cookbookBookmarkRoute = "/api/library/cookbook-bookmarks"

func seedCookbookBookmarkActors(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('owner','owner@example.test','Owner',0,'now'),
		('member','member@example.test','Member',0,'now'),
		('outsider','outsider@example.test','Outsider',0,'now'),
		('admin','admin@example.test','Admin',1,'now');
		UPDATE instance_authority SET owner_user_id='admin',owner_claimed_at='now',generation=1 WHERE singleton_id=1;
		UPDATE instance_settings SET public_instance=1 WHERE id=1;
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES
		('private-book','owner','Private title','Private description','private','created','updated'),
		('shared-book','owner','Shared title','Shared description','private','created','updated'),
		('public-book','owner','Public title','Public description','public','created','updated');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('shared-book','member','viewer','now')`); err != nil {
		t.Fatal(err)
	}
}

func saveCookbookBookmark(t *testing.T, h http.Handler, userID, cookbookID string) *httptest.ResponseRecorder {
	t.Helper()
	return callCreatorProfileAPI(t, h, http.MethodPost, cookbookBookmarkRoute, userID, map[string]any{"local_cookbook_id": cookbookID})
}

func getCookbookBookmarkStatus(t *testing.T, h http.Handler, userID, cookbookID string) *httptest.ResponseRecorder {
	t.Helper()
	return callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute+"/status?local_cookbook_id="+cookbookID, userID, nil)
}

func decodeCookbookBookmarkStatus(t *testing.T, response *httptest.ResponseRecorder) (bool, string) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response: %v; body=%s", err, response.Body)
	}
	var status struct {
		Saved      bool   `json:"saved"`
		BookmarkID string `json:"bookmark_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status values: %v; body=%s", err, response.Body)
	}
	if _, ok := body["saved"]; !ok {
		t.Fatalf("status response omitted saved field: %s", response.Body)
	}
	_, hasBookmarkID := body["bookmark_id"]
	if status.Saved && (len(body) != 2 || !hasBookmarkID) {
		t.Fatalf("saved status exposed unexpected fields: %s", response.Body)
	}
	if !status.Saved && (len(body) != 1 || hasBookmarkID || status.BookmarkID != "") {
		t.Fatalf("unsaved status exposed unexpected fields: %s", response.Body)
	}
	return status.Saved, status.BookmarkID
}

func TestCookbookBookmarkStatusIsAccountScopedAndDoesNotScanPages(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 24; index++ {
		if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES(?,?,?,?,?)`, fmt.Sprintf("page-%02d", index), "member", instanceID, fmt.Sprintf("unavailable-%02d", index), "zzzz"); err != nil {
			t.Fatal(err)
		}
	}
	h := apiHandler{db: db}
	before := getCookbookBookmarkStatus(t, h, "member", "shared-book")
	if before.Code != http.StatusOK || before.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("initial status=%d cache=%q body=%s", before.Code, before.Header().Get("Cache-Control"), before.Body)
	}
	if saved, id := decodeCookbookBookmarkStatus(t, before); saved || id != "" {
		t.Fatalf("unbookmarked readable target status saved=%t id=%q", saved, id)
	}
	var initialCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id='member'`).Scan(&initialCount); err != nil || initialCount != 24 {
		t.Fatalf("status read changed bookmark rows: count=%d err=%v", initialCount, err)
	}
	created := saveCookbookBookmark(t, h, "member", "shared-book")
	var bookmark CookbookBookmarkView
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &bookmark) != nil {
		t.Fatalf("save target status=%d body=%s", created.Code, created.Body)
	}
	firstPage := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute+"?limit=24&offset=0", "member", nil)
	var page CookbookBookmarkPage
	if firstPage.Code != http.StatusOK || json.Unmarshal(firstPage.Body.Bytes(), &page) != nil || page.Total != 25 || len(page.Items) != 24 {
		t.Fatalf("first saved page status=%d body=%s", firstPage.Code, firstPage.Body)
	}
	for _, item := range page.Items {
		if item.ID == bookmark.ID || (item.Target != nil && item.Target.LocalCookbookID == "shared-book") {
			t.Fatal("test target unexpectedly appeared in the first 24-item page")
		}
	}
	after := getCookbookBookmarkStatus(t, h, "member", "shared-book")
	if after.Code != http.StatusOK {
		t.Fatalf("saved status=%d body=%s", after.Code, after.Body)
	}
	if saved, id := decodeCookbookBookmarkStatus(t, after); !saved || id != bookmark.ID {
		t.Fatalf("saved target status saved=%t id=%q want %q", saved, id, bookmark.ID)
	}
	ownerStatus := getCookbookBookmarkStatus(t, h, "owner", "shared-book")
	if ownerStatus.Code != http.StatusOK {
		t.Fatalf("other account status=%d body=%s", ownerStatus.Code, ownerStatus.Body)
	}
	if saved, id := decodeCookbookBookmarkStatus(t, ownerStatus); saved || id != "" {
		t.Fatalf("another account's reference leaked: saved=%t id=%q", saved, id)
	}
	var afterCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id='member'`).Scan(&afterCount); err != nil || afterCount != 25 {
		t.Fatalf("status read changed saved rows: count=%d err=%v", afterCount, err)
	}
	foreignID := strings.Repeat("f", 32)
	if foreignID == instanceID {
		foreignID = strings.Repeat("e", 32)
	}
	if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES('foreign-book','outsider',?,'public-book','now')`, foreignID); err != nil {
		t.Fatal(err)
	}
	foreignStatus := getCookbookBookmarkStatus(t, h, "outsider", "public-book")
	if foreignStatus.Code != http.StatusOK {
		t.Fatalf("foreign namespace status=%d body=%s", foreignStatus.Code, foreignStatus.Body)
	}
	if saved, id := decodeCookbookBookmarkStatus(t, foreignStatus); saved || id != "" {
		t.Fatalf("foreign instance reference counted as local: saved=%t id=%q", saved, id)
	}
}

func TestCookbookBookmarkStatusUsesCurrentACLAndHidesPrivateExistence(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES
		('private-saved','outsider',?,'private-book','now'),
		('admin-private-saved','admin',?,'private-book','now')`, instanceID, instanceID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	private := getCookbookBookmarkStatus(t, h, "outsider", "private-book")
	adminPrivate := getCookbookBookmarkStatus(t, h, "admin", "private-book")
	missing := getCookbookBookmarkStatus(t, h, "outsider", "missing-book")
	for name, response := range map[string]*httptest.ResponseRecorder{"private": private, "admin private": adminPrivate, "missing": missing} {
		if response.Code != http.StatusNotFound || response.Body.String() != missing.Body.String() {
			t.Errorf("%s target response differs from generic missing: status=%d body=%s", name, response.Code, response.Body)
		}
	}
	if strings.Contains(private.Body.String(), "private-saved") || strings.Contains(adminPrivate.Body.String(), "admin-private-saved") {
		t.Fatal("private saved state disclosed its bookmark identity")
	}
	if anonymous := getCookbookBookmarkStatus(t, h, "", "shared-book"); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", anonymous.Code, anonymous.Body)
	}
	public := getCookbookBookmarkStatus(t, h, "outsider", "public-book")
	if public.Code != http.StatusOK {
		t.Fatalf("public target status=%d body=%s", public.Code, public.Body)
	}
	if saved, _ := decodeCookbookBookmarkStatus(t, public); saved {
		t.Fatal("unbookmarked public target was reported as saved")
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	publicDenied := getCookbookBookmarkStatus(t, h, "outsider", "public-book")
	if publicDenied.Code != http.StatusNotFound || publicDenied.Body.String() != missing.Body.String() {
		t.Fatalf("public-instance policy denial status=%d body=%s", publicDenied.Code, publicDenied.Body)
	}
	memberSaved := saveCookbookBookmark(t, h, "member", "shared-book")
	if memberSaved.Code != http.StatusCreated {
		t.Fatalf("member save status=%d body=%s", memberSaved.Code, memberSaved.Body)
	}
	if _, err := db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id='shared-book' AND user_id='member'`); err != nil {
		t.Fatal(err)
	}
	revoked := getCookbookBookmarkStatus(t, h, "member", "shared-book")
	if revoked.Code != http.StatusNotFound || revoked.Body.String() != missing.Body.String() {
		t.Fatalf("revoked status=%d body=%s", revoked.Code, revoked.Body)
	}
}

func TestCookbookBookmarkStatusValidatesInputAndReportsStorageErrors(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	h := apiHandler{db: db}
	for _, path := range []string{
		cookbookBookmarkRoute + "/status",
		cookbookBookmarkRoute + "/status?local_cookbook_id=",
		cookbookBookmarkRoute + "/status?local_cookbook_id=shared-book&local_cookbook_id=shared-book",
		cookbookBookmarkRoute + "/status?local_cookbook_id=shared-book&offset=0",
		cookbookBookmarkRoute + "/status?local_cookbook_id=%20shared-book",
		cookbookBookmarkRoute + "/status?local_cookbook_id=" + strings.Repeat("x", 81),
	} {
		if response := callCreatorProfileAPI(t, h, http.MethodGet, path, "owner", nil); response.Code != http.StatusBadRequest {
			t.Errorf("invalid status query %q returned %d body=%s", path, response.Code, response.Body)
		}
	}
	if response := callCreatorProfileAPI(t, h, http.MethodPost, cookbookBookmarkRoute+"/status?local_cookbook_id=shared-book", "owner", nil); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("non-GET status method=%d body=%s", response.Code, response.Body)
	}
	if _, err := db.Exec(`DROP TABLE cookbook_bookmarks`); err != nil {
		t.Fatal(err)
	}
	if response := getCookbookBookmarkStatus(t, h, "owner", "shared-book"); response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"error":"storage_error"`) {
		t.Fatalf("bookmark lookup storage error status=%d body=%s", response.Code, response.Body)
	}
}

func TestCookbookBookmarkSaveDuplicateRevokeAndRemove(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	profileID := strings.Repeat("a", 32)
	if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		VALUES(?,'owner','Household cook','instance','now','now');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('11111111111111111111111111111111','shared-book','local',?,'creator','local_record_creation','now')`, profileID, profileID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	created := saveCookbookBookmark(t, h, "member", "shared-book")
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("save status=%d headers=%v body=%s", created.Code, created.Header(), created.Body)
	}
	var view CookbookBookmarkView
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ID == "" || view.TargetKind != "local_cookbook" || view.Availability != "available" || view.Target == nil || view.Target.LocalCookbookID != "shared-book" || view.Cookbook == nil || view.Cookbook.Title != "Shared title" || len(view.Cookbook.CreatorAttributions) != 1 || view.Cookbook.CreatorAttributions[0].Profile == nil {
		t.Fatalf("unexpected saved cookbook view: %+v", view)
	}
	duplicate := saveCookbookBookmark(t, h, "member", "shared-book")
	var duplicateView CookbookBookmarkView
	if duplicate.Code != http.StatusOK || json.Unmarshal(duplicate.Body.Bytes(), &duplicateView) != nil || duplicateView.ID != view.ID || duplicateView.CreatedAt != view.CreatedAt {
		t.Fatalf("duplicate status=%d body=%s view=%+v", duplicate.Code, duplicate.Body, duplicateView)
	}
	if _, err := db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id='shared-book' AND user_id='member'; UPDATE cookbooks SET title='Current title',description='Current description' WHERE id='shared-book'`); err != nil {
		t.Fatal(err)
	}
	deniedDetail := callCreatorProfileAPI(t, h, http.MethodGet, "/api/cookbooks/shared-book", "member", nil)
	if deniedDetail.Code != http.StatusNotFound {
		t.Fatalf("saved reference granted cookbook access: status=%d body=%s", deniedDetail.Code, deniedDetail.Body)
	}
	revokedDuplicate := saveCookbookBookmark(t, h, "member", "shared-book")
	if revokedDuplicate.Code != http.StatusNotFound {
		t.Fatalf("revoked duplicate save status=%d body=%s", revokedDuplicate.Code, revokedDuplicate.Body)
	}
	list := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute, "member", nil)
	var page CookbookBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("revoked list status=%d body=%s", list.Code, list.Body)
	}
	if item := page.Items[0]; item.Availability != "unavailable" || item.Target != nil || item.Cookbook != nil || item.ID != view.ID {
		t.Fatalf("revoked target projection was not redacted: %+v", item)
	}
	var unavailable map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	itemJSON, _ := json.Marshal(unavailable["items"])
	for _, forbidden := range []string{"shared-book", "Shared title", "Current title", "Current description", "Household cook", profileID, "recipe_ids", "owner_id"} {
		if strings.Contains(string(itemJSON), forbidden) {
			t.Fatalf("revoked bookmark disclosed %q: %s", forbidden, itemJSON)
		}
	}
	if _, err := db.Exec(`INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at) VALUES('shared-book','member','viewer','later')`); err != nil {
		t.Fatal(err)
	}
	list = callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute, "member", nil)
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Items[0].Availability != "available" || page.Items[0].Cookbook.Title != "Current title" {
		t.Fatalf("restored access did not resolve live target: status=%d body=%s", list.Code, list.Body)
	}
	otherDelete := callCreatorProfileAPI(t, h, http.MethodDelete, cookbookBookmarkRoute+"/"+view.ID, "outsider", nil)
	if otherDelete.Code != http.StatusNotFound {
		t.Fatalf("other account delete status=%d body=%s", otherDelete.Code, otherDelete.Body)
	}
	removed := callCreatorProfileAPI(t, h, http.MethodDelete, cookbookBookmarkRoute+"/"+view.ID, "member", nil)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", removed.Code, removed.Body)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("removed reference count=%d err=%v", remaining, err)
	}
	var memberships, cookbooks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_memberships WHERE cookbook_id='shared-book' AND user_id='member'`).Scan(&memberships); err != nil || memberships != 1 {
		t.Fatalf("bookmark removal changed membership count=%d err=%v", memberships, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbooks WHERE id='shared-book' AND title='Current title'`).Scan(&cookbooks); err != nil || cookbooks != 1 {
		t.Fatalf("bookmark removal changed source cookbook count=%d err=%v", cookbooks, err)
	}
}

func TestCookbookBookmarkListOmitsAnotherUsersPrivateCreatorProfile(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	profileID := strings.Repeat("b", 32)
	if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at)
		VALUES(?,'outsider','Private profile secret','private','now','now');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at)
		VALUES('22222222222222222222222222222222','shared-book','local',?,'creator','local_record_creation','now')`, profileID, profileID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	if got := saveCookbookBookmark(t, h, "member", "shared-book"); got.Code != http.StatusCreated {
		t.Fatalf("save readable cookbook status=%d body=%s", got.Code, got.Body)
	}
	list := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute, "member", nil)
	var page CookbookBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list saved cookbook status=%d body=%s", list.Code, list.Body)
	}
	item := page.Items[0]
	if item.Availability != "available" || item.Cookbook == nil || item.Cookbook.ID != "shared-book" || len(item.Cookbook.CreatorAttributions) != 0 {
		t.Fatalf("private creator profile leaked through saved cookbook: %+v", item)
	}
	for _, forbidden := range []string{profileID, "Private profile secret"} {
		if strings.Contains(list.Body.String(), forbidden) {
			t.Errorf("saved cookbook response disclosed private profile value %q: %s", forbidden, list.Body)
		}
	}
}

func TestCookbookBookmarkSurvivesSourceOwnerDeletionAsUnavailable(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES
		('source-owner','source@example.test','Source owner','now'),
		('saver','saver@example.test','Saver','now');
		INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at)
		VALUES('source-book','source-owner','Source title','Source description','private','now','now');
		INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at)
		VALUES('source-book','saver','viewer','now')`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	created := saveCookbookBookmark(t, h, "saver", "source-book")
	var saved CookbookBookmarkView
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &saved) != nil {
		t.Fatalf("save source cookbook status=%d body=%s", created.Code, created.Body)
	}
	// The existing cookbook owner FK is restrictive, so remove the source
	// resource before deleting its account. Bookmark rows have no source FK.
	if _, err := db.Exec(`DELETE FROM cookbooks WHERE id='source-book'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id='source-owner'`); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id='saver' AND id=?`, saved.ID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("source-owner deletion removed saver reference: count=%d err=%v", retained, err)
	}
	list := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute, "saver", nil)
	var page CookbookBookmarkPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list after source-owner deletion status=%d body=%s", list.Code, list.Body)
	}
	item := page.Items[0]
	if item.ID != saved.ID || item.Availability != "unavailable" || item.Target != nil || item.Cookbook != nil {
		t.Fatalf("deleted source projection was not retained and redacted: %+v", item)
	}
	for _, forbidden := range []string{"source-book", "Source title", "Source description", "source-owner"} {
		if strings.Contains(list.Body.String(), forbidden) {
			t.Errorf("unavailable source response disclosed %q: %s", forbidden, list.Body)
		}
	}
}

func TestCookbookBookmarkDeniedTargetsAndStrictInput(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	h := apiHandler{db: db}
	private := saveCookbookBookmark(t, h, "outsider", "private-book")
	missing := saveCookbookBookmark(t, h, "outsider", "missing-book")
	admin := saveCookbookBookmark(t, h, "admin", "private-book")
	if private.Code != http.StatusNotFound || missing.Code != private.Code || missing.Body.String() != private.Body.String() || admin.Code != private.Code || admin.Body.String() != private.Body.String() {
		t.Fatalf("private/missing/admin responses differ: private=%d %s missing=%d %s admin=%d %s", private.Code, private.Body, missing.Code, missing.Body, admin.Code, admin.Body)
	}
	public := saveCookbookBookmark(t, h, "outsider", "public-book")
	if public.Code != http.StatusCreated {
		t.Fatalf("public save status=%d body=%s", public.Code, public.Body)
	}
	var publicMemberships int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_memberships WHERE user_id='outsider' AND cookbook_id='public-book'`).Scan(&publicMemberships); err != nil || publicMemberships != 0 {
		t.Fatalf("saving public reference changed membership count=%d err=%v", publicMemberships, err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	publicDenied := saveCookbookBookmark(t, h, "outsider", "public-book")
	if publicDenied.Code != http.StatusNotFound || publicDenied.Body.String() != private.Body.String() {
		t.Fatalf("disabled public target status=%d body=%s", publicDenied.Code, publicDenied.Body)
	}
	unknown := callCreatorProfileAPI(t, h, http.MethodPost, cookbookBookmarkRoute, "owner", map[string]any{"local_cookbook_id": "private-book", "title": "secret"})
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown request field status=%d body=%s", unknown.Code, unknown.Body)
	}
	empty := callCreatorProfileAPI(t, h, http.MethodPost, cookbookBookmarkRoute, "owner", map[string]any{"local_cookbook_id": " "})
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty target status=%d body=%s", empty.Code, empty.Body)
	}
	anonymous := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute, "", nil)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d body=%s", anonymous.Code, anonymous.Body)
	}
}

func TestCookbookBookmarkListPagesUnavailableReferencesAndReturnsStorageErrors(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	foreignID := strings.Repeat("f", 32)
	if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES
		('a-ref','owner',?,'private-book','same'),
		('b-ref','owner',?,'private-book','same'),
		('c-ref','owner',?,'deleted-book','older')`, instanceID, foreignID, instanceID); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	first := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute+"?limit=2", "owner", nil)
	var page CookbookBookmarkPage
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &page) != nil || page.Total != 3 || len(page.Items) != 2 || page.Items[0].ID != "a-ref" || page.Items[1].ID != "b-ref" {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body)
	}
	if page.Items[0].Availability != "available" || page.Items[1].Availability != "unavailable" || page.Items[1].Target != nil || page.Items[1].Cookbook != nil {
		t.Fatalf("namespace mismatch projection: %+v", page.Items)
	}
	last := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute+"?limit=2&offset=2", "owner", nil)
	if last.Code != http.StatusOK || json.Unmarshal(last.Body.Bytes(), &page) != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].ID != "c-ref" || page.Items[0].Availability != "unavailable" {
		t.Fatalf("last page status=%d body=%s", last.Code, last.Body)
	}
	if _, err := db.Exec(`DROP TABLE cookbook_memberships`); err != nil {
		t.Fatal(err)
	}
	failure := callCreatorProfileAPI(t, h, http.MethodGet, cookbookBookmarkRoute, "owner", nil)
	if failure.Code != http.StatusInternalServerError || failure.Body.String() != `{"error":"storage_error"}`+"\n" {
		t.Fatalf("canonical read query failure status=%d body=%s", failure.Code, failure.Body)
	}
}

func TestConcurrentCookbookBookmarkSavesConverge(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	h := apiHandler{db: db}
	const callers = 8
	statuses := make(chan int, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := saveCookbookBookmark(t, h, "owner", "private-book")
			statuses <- response.Code
		}()
	}
	wait.Wait()
	close(statuses)
	created, duplicated := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			duplicated++
		default:
			t.Fatalf("concurrent save status=%d", status)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id='owner'`).Scan(&count); err != nil || count != 1 || created != 1 || duplicated != callers-1 {
		t.Fatalf("concurrent saves rows=%d created=%d duplicates=%d err=%v", count, created, duplicated, err)
	}
}

func TestCookbookBookmarkMigrationConstraintsAndLifecycle(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES('saved','member',?,'private-book','now')`, instanceID); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ rowID, userID, instanceID, cookbookID string }{
		{"bad-upper", "member", strings.Repeat("A", 32), "private-book"},
		{"bad-empty", "member", instanceID, ""},
		{"bad-long", "member", instanceID, strings.Repeat("x", 81)},
		{"bad-user", "missing-user", instanceID, "private-book"},
	} {
		if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES(?,?,?,?, 'now')`, invalid.rowID, invalid.userID, invalid.instanceID, invalid.cookbookID); err == nil {
			t.Fatalf("invalid row accepted: %+v", invalid)
		}
	}
	if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES('duplicate','member',?,'private-book','now')`, instanceID); err == nil {
		t.Fatal("duplicate target identity was accepted")
	}
	rows, err := db.Query(`PRAGMA foreign_key_list(cookbook_bookmarks)`)
	if err != nil {
		t.Fatal(err)
	}
	foreignKeys := map[string]string{}
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		foreignKeys[from] = table
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(foreignKeys, map[string]string{"user_id": "users"}) {
		t.Fatalf("cookbook bookmark foreign keys=%v", foreignKeys)
	}
	for _, index := range []string{"cookbook_bookmarks_identity_uq", "idx_cookbook_bookmarks_user_created"} {
		if !migrationObjectExists(t, db, "index", index) {
			t.Errorf("missing index %q", index)
		}
	}
	if _, err := db.Exec(`DELETE FROM cookbooks WHERE id='private-book'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE id='saved'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source deletion changed saved reference count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE id='saved'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("bookmark-owner deletion did not cascade count=%d err=%v", count, err)
	}
}

func TestCookbookBookmarkBatchReadMatchesCanonicalSingleRead(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	ids := []string{"private-book", "shared-book", "public-book", "missing-book"}
	for _, actor := range []string{"owner", "member", "outsider", "admin"} {
		batch, err := readCookbookDetails(context.Background(), db, actor, ids)
		if err != nil {
			t.Fatalf("actor %s batch read: %v", actor, err)
		}
		for _, id := range ids {
			item, err := readCookbookDetail(context.Background(), db, actor, id)
			got, ok := batch[id]
			if errors.Is(err, sql.ErrNoRows) {
				if ok {
					t.Errorf("actor %s batch exposed unreadable %s", actor, id)
				}
				continue
			}
			if err != nil || !ok || !reflect.DeepEqual(got, item) {
				t.Errorf("actor %s canonical parity %s: batch=%+v single=%+v err=%v", actor, id, got, item, err)
			}
		}
	}
}

func TestCookbookBookmarkAccountLimitTrigger(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('limit-user','limit@example.test','Limit','now')`); err != nil {
		t.Fatal(err)
	}
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES(?,?,?,?, 'now')`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for index := 0; index < maxCookbookBookmarksPerAccount; index++ {
		if _, err := stmt.Exec(fmt.Sprintf("limit-%05d", index), "limit-user", instanceID, fmt.Sprintf("target-%05d", index)); err != nil {
			stmt.Close()
			tx.Rollback()
			t.Fatalf("insert reference %d: %v", index, err)
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES('over-limit','limit-user',?,'over-limit','now')`, instanceID); err == nil || !strings.Contains(err.Error(), "cookbook bookmark limit reached") {
		t.Fatalf("over-limit insert error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cookbook_bookmarks WHERE user_id='limit-user'`).Scan(&count); err != nil || count != maxCookbookBookmarksPerAccount {
		t.Fatalf("limit rows=%d err=%v", count, err)
	}
}
