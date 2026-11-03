import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CookbookDetailRoot from "./CookbookDetailRoot.tsx";

const user = {
  id: "owner-1",
  email: "owner@example.test",
  display_name: "Owner",
  is_admin: false,
};

const publicBook = {
  id: "public-book",
  title: "Summer table",
  description: "Seasonal recipes.",
  recipe_count: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const publicRecipe = {
  id: "public-recipe",
  creator_name: "Ada",
  title: "Tomato salad",
  description: "Fresh and simple.",
  ingredients: [{ name: "Tomatoes" }],
  steps: [{ text: "Slice." }],
  servings: 2,
  prep_time_minutes: 5,
  cook_time_minutes: 0,
  tags: ["summer"],
  visibility: "public",
  version: 1,
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
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

it("loads public metadata and recipes only after confirmed anonymity and preserves the fixed return query", async () => {
  window.history.replaceState(null, "", "/cookbook?id=public-book&return_q=summer%20table");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
    if (url === "/api/public/cookbooks/public-book") return Promise.resolve(jsonResponse(publicBook));
    if (url.startsWith("/api/public/cookbooks/public-book/recipes?"))
      return Promise.resolve(jsonResponse([publicRecipe]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookDetailRoot />);
  expect(await screen.findByRole("heading", { name: "Summer table" })).toBeTruthy();
  expect(screen.getByText("1 public recipe")).toBeTruthy();
  expect((await screen.findByRole("link", { name: "Open Tomato salad" })).getAttribute("href"))
    .toBe("/recipe?id=public-recipe");
  expect(screen.getByRole("link", { name: "Back to cookbooks" }).getAttribute("href"))
    .toBe("/cookbooks?q=summer+table");
  expect(calls).toContain("/api/public/cookbooks/public-book");
  expect(calls.some((url) => url.startsWith("/api/cookbooks/public-book"))).toBe(false);
  expect(calls.some((url) => url.startsWith("/api/recipes?cookbook_id="))).toBe(false);
});

it("uses authenticated metadata and recipe APIs without public fallback on denial", async () => {
  window.history.replaceState(null, "", "/cookbook?id=private-book");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/cookbooks/private-book") return Promise.resolve(jsonResponse({
      id: "private-book",
      title: "My recipes",
      description: "",
      visibility: "private",
      owner_id: user.id,
    }));
    if (url === "/api/recipes?cookbook_id=private-book&limit=24&offset=0&sort=recent")
      return Promise.resolve(jsonResponse([]));
    return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookDetailRoot />);
  expect(await screen.findByRole("heading", { name: "My recipes" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Edit cookbook" })).toBeTruthy();
  expect(screen.queryByText(/public recipes/)).toBeNull();
  expect(calls).toContain("/api/cookbooks/private-book");
  expect(calls).toContain("/api/recipes?cookbook_id=private-book&limit=24&offset=0&sort=recent");
  expect(calls.some((url) => url.startsWith("/api/public/cookbooks"))).toBe(false);
});

it("shows the same generic unavailable state for missing IDs and anonymous denials", async () => {
  window.history.replaceState(null, "", "/cookbook?id=hidden-book");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse({}, 401));
    return Promise.resolve(jsonResponse({ error: "not_found" }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookDetailRoot />);
  expect(await screen.findByRole("heading", { name: "Cookbook unavailable" })).toBeTruthy();
  expect(screen.getByText("This cookbook could not be found or is not available to you.")).toBeTruthy();
  expect(calls).toContain("/api/public/cookbooks/hidden-book");
  expect(calls.some((url) => url.startsWith("/api/cookbooks/hidden-book"))).toBe(false);

  cleanup();
  window.history.replaceState(null, "", "/cookbook");
  const missingIDFetch = vi.fn().mockResolvedValue(jsonResponse({}, 401));
  vi.stubGlobal("fetch", missingIDFetch);
  render(<CookbookDetailRoot />);
  expect(await screen.findByRole("heading", { name: "Cookbook unavailable" })).toBeTruthy();
  expect(missingIDFetch).toHaveBeenCalledTimes(1);
  expect(missingIDFetch).toHaveBeenCalledWith("/api/auth/me", { cache: "no-store" });
});

it("does not request cookbook data while session status is unresolved or errored", async () => {
  window.history.replaceState(null, "", "/cookbook?id=private-book");
  const session = deferred<Response>();
  const calls: string[] = [];
  let checks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") {
      checks += 1;
      return checks === 1 ? session.promise : Promise.resolve(jsonResponse(user));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookDetailRoot />);
  expect(await screen.findByText("Checking your session…")).toBeTruthy();
  expect(calls).toEqual(["/api/auth/me"]);
  await act(async () => {
    session.resolve(jsonResponse({}, 503));
    await session.promise;
  });
  expect(await screen.findByRole("heading", { name: "Could not check your session" })).toBeTruthy();
  expect(calls).toEqual(["/api/auth/me"]);
});
