import { afterEach, expect, it, vi } from "vitest";
import {
  fetchCachedRemoteRecipe,
  fetchLibraryPage,
  fetchRecipeBookmarkStatus,
  fetchCookbookBookmarkStatus,
  fetchCookbookBookmarks,
  removeCookbookBookmark,
  saveCookbookBookmark,
} from "./libraryApi.ts";

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => vi.unstubAllGlobals());

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

it("rejects malformed union rows and strips extra metadata from unavailable entries", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ items: [{ availability: "unavailable", target_kind: "local", bookmark_id: "saved-1", created_at: "2026-01-01", recipe: { title: "stale secret" }, local_recipe_id: "private-id" }], total: 1, limit: 24, offset: 0 }));
  vi.stubGlobal("fetch", fetchMock);
  const page = await fetchLibraryPage("recipes");
  expect(page.items[0]).toEqual({ availability: "unavailable", target_kind: "local", bookmark_id: "saved-1", created_at: "2026-01-01" });

  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["friend"], recipe: {} }], total: 1, limit: 24, offset: 0 })));
  await expect(fetchLibraryPage("recipes")).rejects.toThrow("Could not load your recipes Library.");
});

it("checks exact recipe status by one target and never treats an error as unsaved", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ saved: true, bookmark_id: "bookmark-1" }));
  vi.stubGlobal("fetch", fetchMock);
  await expect(fetchRecipeBookmarkStatus({ remote_object_uri: "https://peer.example/federation/recipes/abc" })).resolves.toEqual({ saved: true, bookmark_id: "bookmark-1" });
  expect(fetchMock).toHaveBeenCalledWith("/api/library/bookmarks/status?remote_object_uri=https%3A%2F%2Fpeer.example%2Ffederation%2Frecipes%2Fabc", { cache: "no-store", credentials: "same-origin" });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ saved: false }, 404)));
  await expect(fetchRecipeBookmarkStatus({ local_recipe_id: "private-id" })).rejects.toThrow("Could not check saved status.");
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
