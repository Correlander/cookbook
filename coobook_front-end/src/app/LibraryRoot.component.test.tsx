import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import LibraryRoot from "./LibraryRoot.tsx";
import {
  fetchRecipeBookmarks,
  removeRecipeBookmark,
  saveRecipeBookmark,
} from "../features/library/libraryApi.ts";

const user = {
  id: "reader-1",
  email: "reader@example.test",
  display_name: "Reader",
  is_admin: false,
  created_at: "2026-01-01T00:00:00Z",
};

const accessibleRecipe = {
  id: "recipe-1",
  owner_id: "owner-1",
  source_recipe_id: "source-recipe-1",
  creator_name: "Recipe Author",
  title: "Shared soup",
  description: "Warm and simple",
  ingredients: [{ name: "Tomato", quantity: "2" }],
  steps: [{ text: "Cook it." }],
  servings: 2,
  prep_time_minutes: 5,
  cook_time_minutes: 10,
  tags: ["soup"],
  visibility: "private",
  can_edit: false,
  can_delete: false,
  version: 1,
  cookbook_ids: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const unavailableBookmark = {
  id: "opaque-bookmark-id",
  target_kind: "local",
  availability: "unavailable",
  created_at: "2026-10-01T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("does not request the private Library for a signed-out visitor", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({}, 401));
  vi.stubGlobal("fetch", fetchMock);

  render(<LibraryRoot />);

  expect(
    await screen.findByRole("heading", { name: "Sign in to see your Library" }),
  ).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledWith("/api/auth/me");
});

it("shows the accessible feed separately from saved references and removes only an unavailable bookmark", async () => {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([accessibleRecipe]));
    if (url === "/api/library/bookmarks?limit=24&offset=0")
      return Promise.resolve(
        jsonResponse({ items: [unavailableBookmark], total: 1, limit: 24, offset: 0 }),
      );
    if (url === "/api/library/bookmarks/opaque-bookmark-id")
      return Promise.resolve(new Response(null, { status: 204 }));
    return Promise.resolve(jsonResponse({
      ...unavailableBookmark,
      id: "created-bookmark",
      target: { local_recipe_id: "recipe-1" },
      availability: "available",
      local_recipe: accessibleRecipe,
    }, 201));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<LibraryRoot />);

  expect(
    await screen.findByRole("heading", { name: "Recipes you can access" }),
  ).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Saved" })).toBeTruthy();
  expect(
    await screen.findByRole("link", { name: "Open Shared soup" }),
  ).toBeTruthy();
  expect(screen.getByText("Available to you")).toBeTruthy();
  expect(screen.queryByText("Your copy")).toBeNull();
  expect(screen.getByText("By Recipe Author")).toBeTruthy();
  expect(screen.getByRole("link", { name: "Open Shared soup" }).getAttribute("href"))
    .toBe("/recipe?id=recipe-1");
  expect(screen.getByRole("heading", { name: "Unavailable" })).toBeTruthy();
  expect(document.body.textContent).not.toContain("opaque-bookmark-id");

  fireEvent.click(screen.getByRole("button", { name: "Remove from Library" }));
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Unavailable" })).toBeNull(),
  );
  expect(
    calls.some(
      (call) =>
        call.url === "/api/library/bookmarks/opaque-bookmark-id" &&
        call.init?.method === "DELETE",
    ),
  ).toBe(true);
  expect(calls.some((call) => call.url.startsWith("/api/recipes/recipe-1"))).toBe(false);
});

it("keeps pagination aligned after an idempotent save returns a bookmark beyond the loaded page", async () => {
  const firstPage = Array.from({ length: 24 }, (_, index) => ({
    ...unavailableBookmark,
    id: `bookmark-${index + 1}`,
  }));
  const existingBeyondPage = {
    ...unavailableBookmark,
    id: "bookmark-existing-beyond-page",
    target: { local_recipe_id: accessibleRecipe.id },
    availability: "available",
    local_recipe: accessibleRecipe,
  };
  const nextPage = [
    existingBeyondPage,
    { ...unavailableBookmark, id: "bookmark-26" },
    { ...unavailableBookmark, id: "bookmark-27" },
  ];
  const pageRequests: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([accessibleRecipe]));
    if (url.startsWith("/api/library/bookmarks?")) {
      pageRequests.push(url);
      return Promise.resolve(
        jsonResponse({
          items: url.includes("offset=0") ? firstPage : nextPage,
          total: 27,
          limit: 24,
          offset: url.includes("offset=0") ? 0 : 24,
        }),
      );
    }
    if (url === "/api/library/bookmarks" && init?.method === "POST")
      return Promise.resolve(jsonResponse(existingBeyondPage));
    return Promise.resolve(new Response(null, { status: 204 }));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<LibraryRoot />);
  await screen.findByRole("heading", { name: "Saved" });
  const saved = document.querySelector('section[aria-labelledby="saved-title"]');
  expect(saved).toBeTruthy();
  await waitFor(() =>
    expect(saved?.querySelectorAll("h3")).toHaveLength(24),
  );

  fireEvent.click(screen.getByRole("button", { name: "Save to Library" }));
  await waitFor(() =>
    expect(
      saved?.querySelector('a[aria-label="Open Shared soup"]'),
    ).toBeTruthy(),
  );
  fireEvent.click(screen.getByRole("button", { name: "Load more Saved recipes" }));

  await waitFor(() =>
    expect(saved?.querySelectorAll("h3")).toHaveLength(26),
  );
  expect(pageRequests).toEqual([
    "/api/library/bookmarks?limit=24&offset=0",
    "/api/library/bookmarks?limit=24&offset=24",
  ]);
  expect(
    saved?.querySelectorAll('a[aria-label="Open Shared soup"]'),
  ).toHaveLength(1);
});

it("opens an available remote bookmark as a read-only cached snapshot", async () => {
  const bookmark = {
    id: "remote-bookmark",
    target_kind: "remote",
    target: { remote_object_uri: "https://source.example/federation/recipes/0123456789abcdef0123456789abcdef" },
    availability: "available",
    created_at: "2026-10-01T00:00:00Z",
    remote_recipe: {
      object_uri: "https://source.example/federation/recipes/0123456789abcdef0123456789abcdef",
      source_origin: "https://source.example",
      source_recipe_id: "0123456789abcdef0123456789abcdef",
      creator_name: "Remote chef",
      recipe: {
        title: "Remote stew",
        description: "A cached public snapshot.",
        ingredients: [{ name: "Beans" }],
        steps: [{ text: "Simmer." }],
        servings: 2,
        prep_time_minutes: 0,
        cook_time_minutes: 20,
        tags: [],
      },
      sequence: 1,
      updated_at: "2026-10-01T00:00:00Z",
    },
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/recipes?limit=24&offset=0")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/library/bookmarks?limit=24&offset=0")
      return Promise.resolve(
        jsonResponse({ items: [bookmark], total: 1, limit: 24, offset: 0 }),
      );
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<LibraryRoot />);

  fireEvent.click(
    await screen.findByRole("button", {
      name: "Open saved recipe Remote stew from https://source.example",
    }),
  );
  expect(screen.getByRole("dialog", { name: "Remote stew" })).toBeTruthy();
  expect(screen.getByText("From https://source.example. This saved snapshot is read-only.")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Create a copy" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Edit recipe" })).toBeNull();
  expect(
    fetchMock.mock.calls.some(([input]) =>
      String(input).startsWith("/api/public/recipes/"),
    ),
  ).toBe(false);
});

it("uses exact bookmark API targets and explicit no-store pagination", async () => {
  const calls: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    calls.push([input, init]);
    if (init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
    if (init?.method === "POST")
      return Promise.resolve(
        jsonResponse({
          id: "saved-1",
          target_kind: "remote",
          target: { remote_object_uri: "https://source.example/recipe/1" },
          availability: "available",
          created_at: "2026-10-01T00:00:00Z",
        }, 201),
      );
    return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 24 }));
  });
  vi.stubGlobal("fetch", fetchMock);

  await fetchRecipeBookmarks(24);
  await saveRecipeBookmark({ remote_object_uri: "https://source.example/recipe/1" });
  await removeRecipeBookmark("saved id");

  expect(calls[0]).toEqual([
    "/api/library/bookmarks?limit=24&offset=24",
    { cache: "no-store" },
  ]);
  expect(calls[1][0]).toBe("/api/library/bookmarks");
  expect(calls[1][1]).toMatchObject({
    method: "POST",
    body: JSON.stringify({ target: { remote_object_uri: "https://source.example/recipe/1" } }),
  });
  expect(calls[2]).toEqual([
    "/api/library/bookmarks/saved%20id",
    { method: "DELETE" },
  ]);
});
