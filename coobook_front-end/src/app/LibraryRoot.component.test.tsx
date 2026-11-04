import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import LibraryRoot from "./LibraryRoot.tsx";

const user = { id: "reader-1", email: "reader@example.test", display_name: "Reader", is_admin: false };
const recipe = {
  id: "recipe-1", owner_id: "owner-1", creator_name: "Recipe Author", title: "Shared soup", description: "Warm and simple",
  ingredients: [{ name: "Tomato", quantity: "2" }], steps: [{ text: "Cook it." }], servings: 2,
  prep_time_minutes: 5, cook_time_minutes: 10, tags: ["soup"], visibility: "private", can_edit: false,
  can_delete: false, version: 1, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/library"); });

it("reads only the selected canonical recipes page and renders current relationships", async () => {
  window.history.replaceState(null, "", "/library?type=recipes");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/library/recipes?limit=24&offset=0") return Promise.resolve(jsonResponse({
      items: [{ availability: "available", target_kind: "local", relationships: ["shared", "saved"], membership_role: "editor", bookmark_id: "saved-1", in_owned_cookbook: false, recipe }],
      total: 1, limit: 24, offset: 0,
    }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  expect(await screen.findByRole("link", { name: "Open Shared soup" })).toBeTruthy();
  expect(screen.getByText("Shared with you · editor · Saved to Library")).toBeTruthy();
  expect(screen.getByRole("group", { name: "Library type" })).toBeTruthy();
  expect(calls).toContain("/api/library/recipes?limit=24&offset=0");
  expect(calls.some((url) => url === "/api/recipes?limit=24&offset=0" || url.startsWith("/api/library/bookmarks?"))).toBe(false);
});

it("switches types through browser history and requests only the matching endpoint", async () => {
  window.history.replaceState(null, "", "/library?type=recipes");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url.startsWith("/api/library/")) return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  await screen.findByText("Items you own, can access through a cookbook, or save appear here.");
  fireEvent.click(screen.getByRole("button", { name: "Cookbooks" }));
  await waitFor(() => expect(window.location.search).toBe("?type=cookbooks"));
  expect(screen.getByRole("group", { name: "Library type" })).toBeTruthy();
  await waitFor(() => expect(calls).toContain("/api/library/cookbooks?limit=24&offset=0"));
  window.history.back();
  act(() => window.dispatchEvent(new PopStateEvent("popstate")));
  await waitFor(() => expect(calls.filter((url) => url === "/api/library/recipes?limit=24&offset=0").length).toBeGreaterThanOrEqual(2));
});

it("does not apply a recipes page that completes after switching to cookbooks", async () => {
  window.history.replaceState(null, "", "/library?type=recipes");
  let resolveRecipes!: (response: Response) => void;
  const pendingRecipes = new Promise<Response>((resolve) => { resolveRecipes = resolve; });
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/library/recipes?limit=24&offset=0") return pendingRecipes;
    if (url === "/api/library/cookbooks?limit=24&offset=0") return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  await screen.findByRole("button", { name: "Cookbooks" });
  fireEvent.click(screen.getByRole("button", { name: "Cookbooks" }));
  await screen.findByText("Items you own, can access through a cookbook, or save appear here.");
  await act(async () => {
    resolveRecipes(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["owned"], recipe: { ...recipe, id: "late-recipe", title: "Late recipes result" } }], total: 1, limit: 24, offset: 0 }));
  });
  expect(screen.getByRole("heading", { name: "Your cookbooks" })).toBeTruthy();
  expect(screen.queryByText("Late recipes result")).toBeNull();
});

it("keeps unavailable references redacted and removes only their bookmark", async () => {
  window.history.replaceState(null, "", "/library?type=cookbooks");
  let pages = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/library/cookbooks?limit=24&offset=0") {
      pages += 1;
      return Promise.resolve(jsonResponse({ items: pages === 1 ? [{ availability: "unavailable", bookmark_id: "opaque-1", created_at: "2026-10-01T00:00:00Z", cookbook: { title: "Secret stale title" } }] : [], total: pages === 1 ? 1 : 0, limit: 24, offset: 0 }));
    }
    if (url === "/api/library/cookbook-bookmarks/opaque-1" && init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  expect(await screen.findByRole("heading", { name: "Unavailable cookbook" })).toBeTruthy();
  expect(document.body.textContent).not.toContain("Secret stale title");
  fireEvent.click(screen.getByRole("button", { name: "Remove unavailable cookbook reference" }));
  await waitFor(() => expect(pages).toBe(2));
  expect(fetchMock.mock.calls.some(([input, init]) => String(input) === "/api/library/cookbook-bookmarks/opaque-1" && init?.method === "DELETE")).toBe(true);
});

it("uses server offsets, deduplicates page overlap, and retries the same failed offset", async () => {
  window.history.replaceState(null, "", "/library?type=cookbooks");
  const offsets: number[] = [];
  let continuationAttempts = 0;
  const firstItems = Array.from({ length: 24 }, (_, index) => ({ availability: "unavailable", bookmark_id: `bookmark-${index + 1}`, created_at: "2026-10-01" }));
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url.startsWith("/api/library/cookbooks?")) {
      const offset = Number(new URL(url, "https://cookbook.test").searchParams.get("offset"));
      offsets.push(offset);
      if (offset === 0) return Promise.resolve(jsonResponse({ items: firstItems, total: 25, limit: 24, offset: 0 }));
      continuationAttempts += 1;
      if (continuationAttempts === 1) return Promise.resolve(jsonResponse({}, 503));
      return Promise.resolve(jsonResponse({ items: [firstItems[23], { availability: "unavailable", bookmark_id: "bookmark-25", created_at: "2026-10-02" }], total: 25, limit: 24, offset: 24 }));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  await screen.findByText("Showing 24 of 25 cookbooks.");
  fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  expect((await screen.findByRole("alert")).textContent).toContain("Could not load your cookbooks Library.");
  fireEvent.click(screen.getByRole("button", { name: "Retry loading more" }));
  await waitFor(() => expect(screen.getByText("Showing 25 of 25 cookbooks.")).toBeTruthy());
  expect(offsets).toEqual([0, 24, 24]);
  expect(screen.getAllByRole("button", { name: "Remove unavailable cookbook reference" })).toHaveLength(25);
});

it("keeps Library requests stopped until a failed session check is retried successfully", async () => {
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(sessionChecks === 1 ? jsonResponse({}, 503) : jsonResponse(user));
    }
    if (url.startsWith("/api/library/recipes?")) return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  expect(await screen.findByRole("heading", { name: "Could not check your session" })).toBeTruthy();
  expect(fetchMock.mock.calls.some(([input]) => String(input).startsWith("/api/library/"))).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Retry session check" }));
  await screen.findByText("Items you own, can access through a cookbook, or save appear here.");
  expect(fetchMock.mock.calls.filter(([input]) => String(input).startsWith("/api/library/recipes?"))).toHaveLength(1);
});
