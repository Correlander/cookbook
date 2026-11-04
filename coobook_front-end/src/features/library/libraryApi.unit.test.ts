import { afterEach, expect, it, vi } from "vitest";
import {
  fetchCachedRemoteRecipe,
  fetchLibraryPage,
  fetchRecipeBookmarkStatus,
  fetchCookbookBookmarkStatus,
  fetchLibraryCourses,
  fetchCourseBookmarkStatus,
  saveCourseBookmark,
  removeCourseBookmark,
  fetchCookbookBookmarks,
  removeCookbookBookmark,
  saveCookbookBookmark,
  saveRecipeBookmark,
  fetchPrivateSnapshot,
  captureRecipeBookmark,
} from "./libraryApi.ts";

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => vi.unstubAllGlobals());

it("parses course Library unions and preserves unavailable-reference redaction", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ items: [
    { availability: "available", relationships: ["owned", "saved"], bookmark_id: "bm-1", course: { id: "course-1", owner_name: "Owner", owner_id: "actor", title: "Basics", description: "", visibility: "private", lesson_count: 2, created_at: "now", updated_at: "now" } },
    { availability: "unavailable", relationships: ["saved"], bookmark_id: "bm-2", created_at: "then" },
  ], total: 2, limit: 24, offset: 0 }));
  vi.stubGlobal("fetch", fetchMock);
  const page = await fetchLibraryCourses();
  expect(fetchMock).toHaveBeenCalledWith("/api/library/courses?limit=24&offset=0", { cache: "no-store", credentials: "same-origin" });
  expect(page.items[1]).toEqual({ availability: "unavailable", relationships: ["saved"], bookmark_id: "bm-2", created_at: "then" });
});

it("uses exact course saved-status, idempotent save and reference removal endpoints", async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse({ saved: true, bookmark_id: "bm-1" }))
    .mockResolvedValueOnce(jsonResponse({ bookmark_id: "bm-1", course_id: "course-1", created_at: "now" }, 200))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);
  expect(await fetchCourseBookmarkStatus("course-1")).toEqual({ saved: true, bookmark_id: "bm-1" });
  expect(await saveCourseBookmark("course-1")).toEqual({ bookmarkID: "bm-1", created: false });
  await removeCourseBookmark("bm-1");
  expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
    "/api/library/course-bookmarks/status?local_course_id=course-1", "/api/library/course-bookmarks", "/api/library/course-bookmarks/bm-1",
  ]);
});

it("reads the matching canonical Library endpoint with server paging metadata", async () => {
  const recipe = {
    id: "recipe-1", title: "Soup", description: "", creator_name: "Author", can_edit: false, can_delete: false,
    version: 1, servings: 2, prep_time_minutes: 0, cook_time_minutes: 5, ingredients: [], steps: [{ text: "Cook" }],
    tags: [], visibility: "public", created_at: "2026-01-01", updated_at: "2026-01-02",
  };
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["saved", "shared"], bookmark_id: "bookmark-1", recipe }], total: 25, limit: 24, offset: 24 }));
  vi.stubGlobal("fetch", fetchMock);
  const page = await fetchLibraryPage("recipes", 24);
  expect(fetchMock).toHaveBeenCalledWith("/api/library/recipes?limit=24&offset=24", { cache: "no-store", credentials: "same-origin" });
  expect(page.total).toBe(25);
  expect(page.items[0]?.availability).toBe("available");
});

it("serializes a trimmed recipe query and never sends search to cookbooks", async () => {
  const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 })));
  vi.stubGlobal("fetch", fetchMock);
  await fetchLibraryPage("recipes", 0, "  tomato soup  ");
  expect(fetchMock).toHaveBeenLastCalledWith("/api/library/recipes?limit=24&offset=0&q=tomato+soup", { cache: "no-store", credentials: "same-origin" });
  await fetchLibraryPage("recipes", 0, "   ");
  expect(fetchMock).toHaveBeenLastCalledWith("/api/library/recipes?limit=24&offset=0", { cache: "no-store", credentials: "same-origin" });
  await fetchLibraryPage("cookbooks", 0);
  expect(fetchMock).toHaveBeenLastCalledWith("/api/library/cookbooks?limit=24&offset=0", { cache: "no-store", credentials: "same-origin" });
});

it("rejects malformed union rows and strips extra metadata from unavailable entries", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ items: [{ availability: "unavailable", target_kind: "local", bookmark_id: "saved-1", created_at: "2026-01-01", recipe: { title: "stale secret" }, local_recipe_id: "private-id" }], total: 1, limit: 24, offset: 0 }));
  vi.stubGlobal("fetch", fetchMock);
  const page = await fetchLibraryPage("recipes");
  expect(page.items[0]).toEqual({ availability: "unavailable", target_kind: "local", bookmark_id: "saved-1", created_at: "2026-01-01" });

  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["friend"], recipe: {} }], total: 1, limit: 24, offset: 0 })));
  await expect(fetchLibraryPage("recipes")).rejects.toThrow("Could not load your recipes Library.");
});

it("accepts an owned-cookbook-only recipe relationship without inventing a save or membership", async () => {
  const recipe = {
    id: "owned-cookbook-recipe", title: "Soup", description: "", creator_name: "",
    can_edit: false, can_delete: false, version: 1, servings: 1,
    prep_time_minutes: 0, cook_time_minutes: 1, ingredients: [], steps: [{ text: "Cook" }],
    tags: [], visibility: "private", created_at: "2026-01-01", updated_at: "2026-01-01",
  };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ items: [{
    availability: "available", target_kind: "local", relationships: ["in_owned_cookbook"],
    in_owned_cookbook: true, cookbook_contexts: [{ id: "owner-book" }], recipe,
  }], total: 1, limit: 24, offset: 0 })));

  const page = await fetchLibraryPage("recipes");
  expect(page.items[0]).toMatchObject({
    availability: "available", target_kind: "local", relationships: ["in_owned_cookbook"],
    in_owned_cookbook: true, cookbook_contexts: [{ id: "owner-book" }],
  });
  expect(page.items[0]?.target_kind === "local" && page.items[0].bookmark_id).toBeUndefined();
  expect(page.items[0]?.target_kind === "local" && page.items[0].membership_role).toBeUndefined();
});

it("parses a private snapshot Library item and keeps blocked items body-free", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ items: [
    { availability: "available", target_kind: "remote", relationships: ["saved"], bookmark_id: "pin-1", save_state: "snapshot", version_id: "version-1", source_state: "withdrawn", provenance_state: "user_imported_unverified", snapshot_recipe: { object_uri: "https://peer.example/r/1", source_origin: "https://peer.example", source_recipe_id: "1", creator_name: "Claimed cook", sequence: null, updated_at: "then", recipe: { title: "Saved soup", description: "", ingredients: [], steps: [], servings: 1, prep_time_minutes: 0, cook_time_minutes: 0, tags: [] } } },
    { availability: "unavailable", target_kind: "remote", bookmark_id: "blocked-1", created_at: "then", save_state: "restricted", moderation_reason: "privacy", moderated_at: "now", source_state: "disconnected" },
  ], total: 2, limit: 24, offset: 0 }));
  vi.stubGlobal("fetch", fetchMock);
  const page = await fetchLibraryPage("recipes");
  expect(page.items[0]).toMatchObject({ save_state: "snapshot", provenance_state: "user_imported_unverified", snapshot_recipe: { recipe: { title: "Saved soup" } } });
  expect(page.items[0]?.target_kind === "remote" && page.items[0].snapshot_recipe?.sequence).toBeUndefined();
  expect(page.items[1]).toMatchObject({ availability: "unavailable", save_state: "restricted", moderation_reason: "privacy" });
  expect(page.items[1]).not.toHaveProperty("snapshot_recipe");
});

it("checks exact recipe status by one target and never treats an error as unsaved", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ saved: true, bookmark_id: "bookmark-1" }));
  vi.stubGlobal("fetch", fetchMock);
  await expect(fetchRecipeBookmarkStatus({ remote_object_uri: "https://peer.example/federation/recipes/abc" })).resolves.toEqual({ saved: true, bookmark_id: "bookmark-1" });
  expect(fetchMock).toHaveBeenCalledWith("/api/library/bookmarks/status?remote_object_uri=https%3A%2F%2Fpeer.example%2Ffederation%2Frecipes%2Fabc", { cache: "no-store", credentials: "same-origin" });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ saved: false }, 404)));
  await expect(fetchRecipeBookmarkStatus({ local_recipe_id: "private-id" })).rejects.toThrow("Could not check saved status.");
});

it("parses the full remote status view before accepting capture eligibility", async () => {
  const uri = "https://peer.example/federation/recipes/abc";
  const remoteRecipe = {
    object_uri: uri, source_origin: "https://peer.example", source_recipe_id: "abc", creator_name: "Remote chef",
    sequence: 4, updated_at: "2026-11-04T12:00:00Z",
    recipe: { title: "Soup", description: "", servings: 2, prep_time_minutes: 5, cook_time_minutes: 10, ingredients: [], steps: [{ text: "Cook" }], tags: [] },
  };
  const status = {
    id: "bookmark-1", target_kind: "remote", target: { remote_object_uri: uri }, availability: "available",
    created_at: "2026-11-04T12:00:00Z", saved: true, save_state: "reference", can_capture_snapshot: true,
    source_state: "published", remote_recipe: remoteRecipe,
  };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(status)));

  await expect(fetchRecipeBookmarkStatus({ remote_object_uri: uri })).resolves.toMatchObject({
    saved: true, bookmark_id: "bookmark-1", can_capture_snapshot: true,
    bookmark: { id: "bookmark-1", save_state: "reference", remote_recipe: remoteRecipe },
  });
});

it("rejects malformed full remote status views rather than granting capture", async () => {
  const uri = "https://peer.example/federation/recipes/abc";
  const status = {
    id: "bookmark-1", target_kind: "remote", target: { remote_object_uri: uri }, availability: "available",
    created_at: "now", saved: true, save_state: "reference", can_capture_snapshot: "true",
    remote_recipe: {
      object_uri: uri, source_origin: "https://peer.example", source_recipe_id: "abc", creator_name: "Remote chef",
      sequence: 4, updated_at: "now",
      recipe: { title: "Soup", description: "", servings: 2, prep_time_minutes: 5, cook_time_minutes: 10, ingredients: [], steps: [{ text: "Cook" }], tags: [] },
    },
  };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(status)));

  await expect(fetchRecipeBookmarkStatus({ remote_object_uri: uri })).rejects.toThrow("Could not check saved status.");
});

it("sends conditional mode for a remote save and parses the frozen-copy result", async () => {
  const uri = "https://peer.example/federation/recipes/abc";
  const snapshot = { object_uri: uri, source_origin: "https://peer.example", source_recipe_id: "abc", creator_name: "Remote chef", sequence: 5, updated_at: "2026-11-04", recipe: { title: "Soup", description: "", ingredients: [], steps: [{ text: "Cook" }], servings: 1, prep_time_minutes: 0, cook_time_minutes: 1, tags: [] } };
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ id: "bm-1", target_kind: "remote", target: { remote_object_uri: uri }, availability: "available", created_at: "now", saved: true, save_state: "snapshot", provenance_state: "publisher_verified", snapshot_recipe: snapshot }, 201));
  vi.stubGlobal("fetch", fetchMock);
  const result = await saveRecipeBookmark({ remote_object_uri: uri }, "conditional");
  expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({ target: { remote_object_uri: uri }, save_mode: "conditional" });
  expect(result).toMatchObject({ created: true, bookmark: { id: "bm-1", save_state: "snapshot", snapshot_recipe: { recipe: { title: "Soup" } } } });
});

it("reads a private snapshot by bookmark ID and never falls back to remote cache", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ object_uri: "https://peer.example/r/1", source_origin: "https://peer.example", source_recipe_id: "1", creator_name: "Cook", received_at: "then", version_id: "v-1", provenance_state: "user_imported_unverified", source_state: "disconnected", recipe: { title: "Private soup", description: "", ingredients: [], steps: [], servings: 1, prep_time_minutes: 0, cook_time_minutes: 0, tags: [] } }));
  vi.stubGlobal("fetch", fetchMock);
  await expect(fetchPrivateSnapshot("bookmark/1")).resolves.toMatchObject({ version_id: "v-1", provenance_state: "user_imported_unverified", updated_at: "then" });
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledWith("/api/library/bookmarks/bookmark%2F1/snapshot", { cache: "no-store", credentials: "same-origin" });
});

it("captures an existing reference only through its explicit bookmark capture route", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ id: "bm-1", target_kind: "remote", target: { remote_object_uri: "https://peer.example/r/1" }, availability: "available", created_at: "now", save_state: "snapshot", snapshot_recipe: { object_uri: "https://peer.example/r/1", source_origin: "https://peer.example", source_recipe_id: "1", creator_name: "Cook", received_at: "then", recipe: { title: "Soup", description: "", ingredients: [], steps: [], servings: 1, prep_time_minutes: 0, cook_time_minutes: 0, tags: [] } } }));
  vi.stubGlobal("fetch", fetchMock);
  await expect(captureRecipeBookmark("bm-1")).resolves.toMatchObject({ save_state: "snapshot" });
  expect(fetchMock).toHaveBeenCalledWith("/api/library/bookmarks/bm-1/capture", { method: "POST", credentials: "same-origin" });
});

it("reads one exact cached remote snapshot without a public-feed fallback", async () => {
  const uri = "https://peer.example/federation/recipes/a b";
  const view = {
    object_uri: uri,
    source_origin: "https://peer.example",
    source_recipe_id: "recipe-a",
    creator_name: "Remote chef",
    sequence: 3,
    updated_at: "2026-10-01T00:00:00Z",
    recipe: { title: "Cached soup", description: "Snapshot", ingredients: [], steps: [{ text: "Simmer." }], servings: 2, prep_time_minutes: 0, cook_time_minutes: 20, tags: [] },
  };
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse(view));
  vi.stubGlobal("fetch", fetchMock);

  await expect(fetchCachedRemoteRecipe(uri)).resolves.toEqual(view);
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/library/recipes/remote?remote_object_uri=https%3A%2F%2Fpeer.example%2Ffederation%2Frecipes%2Fa+b",
    { cache: "no-store", credentials: "same-origin" },
  );
});

it("rejects a malformed cached remote view instead of trusting its fields", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ object_uri: "https://peer.example/x", recipe: {} })));
  await expect(fetchCachedRemoteRecipe("https://peer.example/x")).rejects.toThrow("Could not load your recipes Library.");
});

it("rejects a cached remote view whose identity differs from the requested URI", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
    object_uri: "https://peer.example/federation/recipes/other",
    source_origin: "https://peer.example",
    source_recipe_id: "other",
    creator_name: "Remote chef",
    sequence: 3,
    updated_at: "2026-10-01T00:00:00Z",
    recipe: { title: "Other recipe", description: "Snapshot", ingredients: [], steps: [{ text: "Simmer." }], servings: 2, prep_time_minutes: 0, cook_time_minutes: 20, tags: [] },
  })));
  await expect(fetchCachedRemoteRecipe("https://peer.example/requested")).rejects.toThrow("Recipe unavailable");
});

it("uses bounded no-store pagination and strips all target fields from unavailable rows", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    jsonResponse({
      items: [
        {
          id: "saved-1",
          target_kind: "local_cookbook",
          availability: "unavailable",
          created_at: "2026-10-01T00:00:00Z",
          target: { local_cookbook_id: "revoked-cookbook" },
          cookbook: { id: "revoked-cookbook", title: "Stale private title" },
        },
      ],
      total: 25,
      limit: 24,
      offset: 24,
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  const page = await fetchCookbookBookmarks(24);

  expect(fetchMock).toHaveBeenCalledWith(
    "/api/library/cookbook-bookmarks?limit=24&offset=24",
    { cache: "no-store", credentials: "same-origin" },
  );
  expect(page.total).toBe(25);
  expect(page.items[0]).toEqual({
    id: "saved-1",
    target_kind: "local_cookbook",
    availability: "unavailable",
    created_at: "2026-10-01T00:00:00Z",
  });
  expect(page.items[0]).not.toHaveProperty("target");
  expect(page.items[0]).not.toHaveProperty("cookbook");
});

it("rejects malformed paged cookbook bookmark payloads", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      jsonResponse({ items: {}, total: 0, limit: 24, offset: 0 }),
    );
  vi.stubGlobal("fetch", fetchMock);

  await expect(fetchCookbookBookmarks()).rejects.toThrow(
    "Could not load saved cookbooks.",
  );
});

it("reads exact saved status without an account identifier", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      jsonResponse({ saved: true, bookmark_id: "bookmark-1" }),
    );
  vi.stubGlobal("fetch", fetchMock);

  await expect(fetchCookbookBookmarkStatus("cookbook id")).resolves.toEqual({
    saved: true,
    bookmark_id: "bookmark-1",
  });
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/library/cookbook-bookmarks/status?local_cookbook_id=cookbook+id",
    { cache: "no-store", credentials: "same-origin" },
  );
});

it("keeps malformed and unavailable status responses unknown through errors", async () => {
  const malformedFetch = vi
    .fn()
    .mockResolvedValue(jsonResponse({ saved: true }));
  vi.stubGlobal("fetch", malformedFetch);
  await expect(fetchCookbookBookmarkStatus("book-1")).rejects.toThrow(
    "Could not check saved status.",
  );

  const notFoundFetch = vi.fn().mockResolvedValue(jsonResponse({}, 404));
  vi.stubGlobal("fetch", notFoundFetch);
  await expect(fetchCookbookBookmarkStatus("book-1")).rejects.toThrow(
    "Could not check saved status.",
  );
});

it("accepts successful save and idempotent duplicate responses without sending actor identity", async () => {
  const bookmark = {
    id: "bookmark-1",
    target_kind: "local_cookbook",
    target: { local_cookbook_id: "book-1" },
    availability: "available",
    cookbook: {
      id: "book-1",
      title: "Garden table",
      description: "",
      visibility: "public",
      owner_id: "owner-1",
    },
    created_at: "2026-10-01T00:00:00Z",
  };
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(jsonResponse(bookmark, 201))
    .mockResolvedValueOnce(jsonResponse(bookmark, 200));
  vi.stubGlobal("fetch", fetchMock);

  await expect(saveCookbookBookmark("book-1")).resolves.toEqual({
    bookmark,
    created: true,
  });
  await expect(saveCookbookBookmark("book-1")).resolves.toEqual({
    bookmark,
    created: false,
  });
  expect(fetchMock).toHaveBeenNthCalledWith(
    1,
    "/api/library/cookbook-bookmarks",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ local_cookbook_id: "book-1" }),
    },
  );
});

it("removes only the account's bookmark row using its escaped opaque ID", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);

  await expect(removeCookbookBookmark("bookmark/id")).resolves.toBeUndefined();
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/library/cookbook-bookmarks/bookmark%2Fid",
    { method: "DELETE", credentials: "same-origin" },
  );
});
