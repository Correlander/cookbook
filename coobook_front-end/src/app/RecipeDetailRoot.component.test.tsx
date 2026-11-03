import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
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
    if (url === "/api/library/bookmarks?limit=24&offset=0")
      return Promise.resolve(
        jsonResponse({
          items: [{
            id: "bookmark-1",
            target_kind: "local",
            target: { local_recipe_id: "recipe-1" },
            availability: "available",
            local_recipe: recipe,
            created_at: "2026-01-02T00:00:00Z",
          }],
          total: 1,
          limit: 24,
          offset: 0,
        }),
      );
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
