package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestLibraryCookbooksMergesOwnedMemberAndSavedOnce(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	h := apiHandler{db: db}
	if got := saveCookbookBookmark(t, h, "member", "shared-book"); got.Code != http.StatusCreated {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	if got := saveCookbookBookmark(t, h, "member", "public-book"); got.Code != http.StatusCreated {
		t.Fatalf("save public: %d %s", got.Code, got.Body)
	}
	response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "member", nil)
	var page LibraryCookbookPage
	if response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(response.Body.Bytes(), &page) != nil {
		t.Fatalf("list %d %s", response.Code, response.Body)
	}
	if page.Total != 2 || len(page.Items) != 2 || page.Limit != 24 || page.Offset != 0 {
		t.Fatalf("union count/page/default metadata: %+v", page)
	}
	byID := map[string]LibraryCookbookItem{}
	for _, item := range page.Items {
		if item.Cookbook == nil {
			t.Fatalf("unexpected unavailable: %+v", item)
		}
		byID[item.Cookbook.ID] = item
	}
	shared := byID["shared-book"]
	if len(shared.Relationships) != 2 || shared.Relationships[0] != "shared" || shared.Relationships[1] != "saved" || shared.BookmarkID == "" || shared.MembershipRole != "viewer" {
		t.Fatalf("shared/saved projection: %+v", shared)
	}
	pub := byID["public-book"]
	if len(pub.Relationships) != 1 || pub.Relationships[0] != "saved" || pub.BookmarkID == "" {
		t.Fatalf("saved public projection: %+v", pub)
	}
	first := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks?limit=1", "member", nil)
	var firstPage LibraryCookbookPage
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || firstPage.Total != 2 || firstPage.Limit != 1 || firstPage.Offset != 0 || len(firstPage.Items) != 1 || firstPage.Items[0].Cookbook.ID != "public-book" {
		t.Fatalf("stable first page: %d %s", first.Code, first.Body)
	}
	second := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks?limit=1&offset=1", "member", nil)
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &firstPage) != nil || firstPage.Total != 2 || firstPage.Limit != 1 || firstPage.Offset != 1 || len(firstPage.Items) != 1 || firstPage.Items[0].Cookbook.ID != "shared-book" {
		t.Fatalf("stable second page: %d %s", second.Code, second.Body)
	}
	if _, ok := byID["private-book"]; ok {
		t.Fatal("unrelated public-readable cookbook included")
	}
	admin := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "admin", nil)
	var adminPage LibraryCookbookPage
	if admin.Code != 200 || json.Unmarshal(admin.Body.Bytes(), &adminPage) != nil || adminPage.Total != 0 {
		t.Fatalf("instance owner/admin received unrelated private cookbooks: %d %s", admin.Code, admin.Body)
	}
	owner := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "owner", nil)
	if owner.Code != 200 {
		t.Fatalf("owner list: %d %s", owner.Code, owner.Body)
	}
	var ownPage LibraryCookbookPage
	if json.Unmarshal(owner.Body.Bytes(), &ownPage) != nil || ownPage.Total != 3 {
		t.Fatalf("owner union: %s", owner.Body)
	}
}

func TestLibraryCookbooksRedactsUnavailableAndDeniesAnonymous(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	h := apiHandler{db: db}
	if got := saveCookbookBookmark(t, h, "member", "shared-book"); got.Code != http.StatusCreated {
		t.Fatalf("save %d %s", got.Code, got.Body)
	}
	profileID := strings.Repeat("b", 32)
	if _, err := db.Exec(`INSERT INTO creator_profiles(creator_id,user_id,public_name,visibility,created_at,updated_at) VALUES(?,'outsider','Private credit','private','now','now');
		INSERT INTO content_attributions(id,cookbook_id,subject_kind,creator_id,role,provenance,created_at) VALUES('cccccccccccccccccccccccccccccccc','shared-book','local',?,'creator','local_record_creation','now')`, profileID, profileID); err != nil {
		t.Fatal(err)
	}
	readable := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "member", nil)
	var readablePage LibraryCookbookPage
	if readable.Code != 200 || json.Unmarshal(readable.Body.Bytes(), &readablePage) != nil || len(readablePage.Items) != 1 || readablePage.Items[0].Cookbook == nil || len(readablePage.Items[0].Cookbook.CreatorAttributions) != 0 || strings.Contains(readable.Body.String(), "Private credit") || strings.Contains(readable.Body.String(), profileID) {
		t.Fatalf("private creator attribution leaked: %d %s", readable.Code, readable.Body)
	}
	if _, err := db.Exec(`DELETE FROM cookbook_memberships WHERE cookbook_id='shared-book' AND user_id='member'; DELETE FROM cookbooks WHERE id='private-book'`); err != nil {
		t.Fatal(err)
	}
	response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "member", nil)
	var page LibraryCookbookPage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list %d %s", response.Code, response.Body)
	}
	item := page.Items[0]
	if item.Availability != "unavailable" || item.Cookbook != nil || item.BookmarkID == "" || len(item.Relationships) != 1 || item.Relationships[0] != "saved" {
		t.Fatalf("unavailable projection: %+v", item)
	}
	if response.Body.String() == "" || containsAny(response.Body.String(), "shared-book", "Shared title", "owner") {
		t.Fatalf("unavailable source leaked: %s", response.Body)
	}
	if anon := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "", nil); anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status %d", anon.Code)
	}
}

func TestLibraryCookbooksRejectsInvalidPagingAndSurfacesStorageErrors(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	h := apiHandler{db: db}
	if invalid := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks?limit=101", "owner", nil); invalid.Code != http.StatusBadRequest {
		t.Fatalf("over-limit page status=%d body=%s", invalid.Code, invalid.Body)
	}
	maximum := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks?limit=100", "owner", nil)
	var maximumPage LibraryCookbookPage
	if maximum.Code != http.StatusOK || json.Unmarshal(maximum.Body.Bytes(), &maximumPage) != nil || maximumPage.Limit != 100 || maximumPage.Offset != 0 || maximumPage.Total != 3 || len(maximumPage.Items) != 3 {
		t.Fatalf("maximum page: status=%d body=%s", maximum.Code, maximum.Body)
	}
	if _, err := db.Exec(`DROP TABLE cookbook_bookmarks`); err != nil {
		t.Fatal(err)
	}
	if failed := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "owner", nil); failed.Code != http.StatusInternalServerError || !strings.Contains(failed.Body.String(), `"error":"storage_error"`) {
		t.Fatalf("bookmark storage failure status=%d body=%s", failed.Code, failed.Body)
	}
}

func TestLibraryCookbooksKeepsForeignAndDeletedReferencesRedacted(t *testing.T) {
	db := testDatabase(t)
	seedCookbookBookmarkActors(t, db)
	instanceID, err := currentLibraryInstanceID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	foreignID := strings.Repeat("f", 32)
	if foreignID == instanceID {
		foreignID = strings.Repeat("e", 32)
	}
	if _, err := db.Exec(`INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at) VALUES('deleted-book','owner','Deleted title','Deleted description','private','created','updated');
		INSERT INTO cookbook_bookmarks(id,user_id,local_instance_id,local_cookbook_id,created_at) VALUES
		('foreign-ref','member',?,'shared-book','older'),
		('deleted-ref','member',?,'deleted-book','older')`, foreignID, instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM cookbooks WHERE id='deleted-book'`); err != nil {
		t.Fatal(err)
	}
	h := apiHandler{db: db}
	response := callCreatorProfileAPI(t, h, http.MethodGet, "/api/library/cookbooks", "member", nil)
	var page LibraryCookbookPage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("foreign/deleted references status=%d body=%s", response.Code, response.Body)
	}
	redacted := 0
	for _, item := range page.Items {
		if item.Availability == "unavailable" {
			redacted++
			if item.Cookbook != nil || item.BookmarkID == "" || len(item.Relationships) != 1 || item.Relationships[0] != "saved" {
				t.Fatalf("foreign/deleted reference projection: %+v", item)
			}
		}
	}
	if redacted != 2 {
		t.Fatalf("redacted references=%d want 2", redacted)
	}
	for _, item := range page.Items {
		if item.Availability != "unavailable" {
			continue
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if containsAny(string(encoded), "shared-book", "Shared title", "deleted-book", "Deleted title", "Source title") {
			t.Fatalf("foreign/deleted source metadata leaked: %s", encoded)
		}
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
