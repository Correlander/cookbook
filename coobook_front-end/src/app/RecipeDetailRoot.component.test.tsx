import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import RecipeDetailRoot from "./RecipeDetailRoot.tsx";
import { fetchRecipeDetail } from "../features/recipes/recipeDetailApi.ts";

const user = {
  id: "owner-1",
  email: "owner@example.test",
  display_name: "Owner",
  is_admin: false,
};

const recipe = {
  id: "recipe-1",
  owner_id: "owner-1",
  source_recipe_id: "source-recipe-1",
  creator_name: "Owner",
  title: "Tomato soup",
  description: "A warm soup.",
  ingredients: [{ name: "Tomato", quantity: "2" }],
  steps: [{ text: "Cook the tomato." }],
  servings: 2,
  prep_time_minutes: 5,
  cook_time_minutes: 10,
  tags: [],
  visibility: "private",
  can_edit: true,
  can_delete: true,
  can_manage_sharing: true,
  version: 1,
  cookbook_ids: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

it("waits for session then reads signed-in detail and current saved status", async () => {
  window.history.replaceState(null, "", "/recipe?id=recipe-1");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/recipes/recipe-1") return Promise.resolve(jsonResponse(recipe));
    if (url === "/api/library/bookmarks/status?local_recipe_id=recipe-1")
      return Promise.resolve(jsonResponse({ saved: true, bookmark_id: "bookmark-1" }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<RecipeDetailRoot />);

  expect(await screen.findByRole("heading", { name: "Tomato soup" })).toBeTruthy();
  expect(screen.getByText("Owned by you · Your copy")).toBeTruthy();
  expect(
    await screen.findByRole("button", { name: "Remove from Library" }),
  ).toBeTruthy();
  expect(calls).toContain("/api/recipes/recipe-1");
  expect(calls).not.toContain("/api/public/recipes/recipe-1");
  expect(calls).toContain("/api/library/bookmarks/status?local_recipe_id=recipe-1");
  expect(calls.some((url) => url.startsWith("/api/library/bookmarks?"))).toBe(false);
});

it("uses only the redacted public endpoint for an anonymous detail and keeps denials generic", async () => {
  window.history.replaceState(null, "", "/recipe?id=hidden-id");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
    if (url === "/api/public/recipes/hidden-id")
      return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
    return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<RecipeDetailRoot />);

  expect(await screen.findByRole("heading", { name: "Recipe unavailable" })).toBeTruthy();
  expect(calls).toContain("/api/public/recipes/hidden-id");
  expect(calls).not.toContain("/api/recipes/hidden-id");
  expect(calls.some((url) => url.startsWith("/api/library/bookmarks"))).toBe(false);
  expect(screen.queryByText("hidden-id")).toBeNull();
});

it("loads an authenticated exact remote snapshot, keeps it read-only, and checks that URI's saved state", async () => {
  const uri = "https://peer.example/federation/recipes/abc?rev=4";
  window.history.replaceState(null, "", `/recipe?remote_object_uri=${encodeURIComponent(uri)}`);
  const calls: string[] = [];
  const remoteView = {
    object_uri: uri,
    source_origin: "https://peer.example",
    source_recipe_id: "abc",
    creator_name: "Remote chef",
    sequence: 4,
    updated_at: "2026-10-01T00:00:00Z",
    recipe: { title: "Remote stew", description: "Cached copy", ingredients: [], steps: [{ text: "Simmer." }], servings: 2, prep_time_minutes: 0, cook_time_minutes: 20, tags: [] },
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === `/api/library/recipes/remote?remote_object_uri=${encodeURIComponent(uri)}`)
      return Promise.resolve(jsonResponse(remoteView));
    if (url === `/api/library/bookmarks/status?remote_object_uri=${encodeURIComponent(uri)}`)
      return Promise.resolve(jsonResponse({ saved: true, bookmark_id: "bookmark-remote" }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<RecipeDetailRoot />);

  expect(await screen.findByRole("heading", { name: "Remote stew" })).toBeTruthy();
  expect(screen.getByText("From https://peer.example. This saved snapshot is read-only.")).toBeTruthy();
  expect(await screen.findByRole("button", { name: "Remove from Library" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Edit recipe" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Move to trash" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Create a copy" })).toBeNull();
  expect(calls).toContain(`/api/library/recipes/remote?remote_object_uri=${encodeURIComponent(uri)}`);
  expect(calls).toContain(`/api/library/bookmarks/status?remote_object_uri=${encodeURIComponent(uri)}`);
  expect(calls.some((url) => url.startsWith("/api/public/remote-recipes"))).toBe(false);
  expect(calls.some((url) => url.startsWith("/api/recipes/"))).toBe(false);
});

it("does not request an exact remote snapshot when the session is anonymous", async () => {
  const uri = "https://peer.example/federation/recipes/abc";
  window.history.replaceState(null, "", `/recipe?remote_object_uri=${encodeURIComponent(uri)}`);
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<RecipeDetailRoot />);

  expect(await screen.findByRole("heading", { name: "Recipe unavailable" })).toBeTruthy();
  expect(calls).toContain("/api/auth/me");
  expect(calls.some((url) => url.startsWith("/api/recipes/"))).toBe(false);
  expect(calls.some((url) => url.startsWith("/api/library/recipes/remote?"))).toBe(false);
});

it("rejects ambiguous detail selectors without requesting either target", async () => {
  window.history.replaceState(null, "", "/recipe?id=recipe-1&remote_object_uri=https%3A%2F%2Fpeer.example%2Fabc");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<RecipeDetailRoot />);

  expect(await screen.findByRole("heading", { name: "Recipe unavailable" })).toBeTruthy();
  expect(calls).toContain("/api/auth/me");
  expect(calls.some((url) => url.startsWith("/api/recipes/"))).toBe(false);
  expect(calls.some((url) => url.startsWith("/api/library/recipes/remote?"))).toBe(false);
});

it("keeps saved state unknown after an exact status failure and offers a deliberate retry", async () => {
  window.history.replaceState(null, "", "/recipe?id=recipe-1");
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/recipes/recipe-1") return Promise.resolve(jsonResponse(recipe));
    if (url === "/api/library/bookmarks/status?local_recipe_id=recipe-1") return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<RecipeDetailRoot />);
  expect(await screen.findByRole("heading", { name: "Tomato soup" })).toBeTruthy();
  expect(await screen.findByRole("button", { name: "Retry saved status" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Remove from Library" })).toBeNull();
  expect(fetchMock.mock.calls.some(([input]) => String(input).startsWith("/api/library/bookmarks?"))).toBe(false);
});

it("does not choose an anonymous detail endpoint after a transient session error", async () => {
  window.history.replaceState(null, "", "/recipe?id=recipe-1");
  const calls: string[] = [];
  const initialSession = deferred<Response>();
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(
        sessionChecks === 1
          ? initialSession.promise
          : jsonResponse({
              id: "owner-1",
              email: "owner@example.test",
              display_name: "Owner",
              is_admin: false,
            }),
      );
    }
    if (url === "/api/recipes/recipe-1")
      return Promise.resolve(jsonResponse(recipe));
    if (url === "/api/library/bookmarks?limit=24&offset=0")
      return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<RecipeDetailRoot />);

  expect(await screen.findByText("Checking your session…")).toBeTruthy();
  expect(calls).toEqual(["/api/auth/me"]);
  await act(async () => {
    initialSession.resolve(jsonResponse({}, 500));
    await initialSession.promise;
  });
  expect(await screen.findByRole("heading", { name: "Could not check your session" })).toBeTruthy();
  expect(calls).toEqual(["/api/auth/me"]);
  expect(screen.queryByRole("heading", { name: "Recipe unavailable" })).toBeNull();

  fireEvent.click(screen.getByRole("button", { name: "Retry session check" }));
  expect(await screen.findByRole("heading", { name: "Tomato soup" })).toBeTruthy();
  expect(calls.filter((url) => url === "/api/auth/me")).toHaveLength(2);
  expect(calls).toContain("/api/recipes/recipe-1");
  expect(calls).not.toContain("/api/public/recipes/recipe-1");
});

it("discards a pending same-recipe edit when the detail page changes actors", async () => {
  window.history.replaceState(null, "", "/recipe?id=recipe-1");
  const pendingUpdate = deferred<Response>();
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/recipes/recipe-1" && init?.method === "PUT")
      return pendingUpdate.promise;
    if (url === "/api/recipes/recipe-1") return Promise.resolve(jsonResponse(recipe));
    if (url === "/api/public/recipes/recipe-1")
      return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
    if (url === "/api/library/bookmarks?limit=24&offset=0")
      return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    if (url === "/api/cookbooks") return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<RecipeDetailRoot />);

  fireEvent.click(await screen.findByRole("button", { name: "Edit recipe" }));
  fireEvent.change(screen.getByLabelText("Title"), {
    target: { value: "Actor A's update" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() =>
    expect(fetchMock.mock.calls.some(
      ([url, init]) => String(url) === "/api/recipes/recipe-1" && init?.method === "PUT",
    )).toBe(true),
  );

  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  expect(await screen.findByRole("heading", { name: "Recipe unavailable" })).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Edit recipe" })).toBeNull();
  expect(
    fetchMock.mock.calls.filter(([url]) => String(url) === "/api/public/recipes/recipe-1"),
  ).toHaveLength(1);

  await act(async () => {
    pendingUpdate.resolve(jsonResponse({ ...recipe, title: "Actor A's update" }));
    await pendingUpdate.promise;
  });
  expect(screen.getByRole("heading", { name: "Recipe unavailable" })).toBeTruthy();
  expect(screen.queryByText("Actor A's update")).toBeNull();
});

it("encodes the recipe ID and never retries the alternate detail endpoint", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ error: "not_found" }, 404));
  vi.stubGlobal("fetch", fetchMock);

  await expect(fetchRecipeDetail("private id", false)).rejects.toThrow(
    "Recipe unavailable",
  );
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledWith("/api/public/recipes/private%20id", {
    cache: "no-store",
  });
});
