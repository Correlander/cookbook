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

it("exposes disabled-course saved placeholders and removes them without requesting course content", async () => {
  window.history.replaceState(null, "", "/library?type=courses");
  const calls: string[] = [];
  let coursePages = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input); calls.push(`${init?.method ?? "GET"} ${url}`);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/public/config") return Promise.resolve(jsonResponse({ courses_enabled: false }));
    if (url === "/api/library/courses?limit=24&offset=0") { coursePages += 1; return Promise.resolve(coursePages === 1 ? jsonResponse({ items: [{ availability: "unavailable", relationships: ["saved"], bookmark_id: "opaque-course", created_at: "then" }], total: 1, limit: 24, offset: 0 }) : jsonResponse({ items: [], total: 0, limit: 24, offset: 0 })); }
    if (url === "/api/library/courses?limit=1&offset=0") return Promise.resolve(jsonResponse({ items: [{ availability: "unavailable", relationships: ["saved"], bookmark_id: "opaque-course", created_at: "then" }], total: 1, limit: 1, offset: 0 }));
    if (url === "/api/library/course-bookmarks/opaque-course" && init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  expect(await screen.findByRole("button", { name: "Remove unavailable course reference" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Remove unavailable course reference" }));
  await screen.findByRole("heading", { name: "Your courses" });
  expect(calls.some((call) => call.includes("/api/courses/") || call.includes("/api/public/courses/"))).toBe(false);
  expect(calls.some((call) => call === "GET /api/library/courses?limit=1&offset=0")).toBe(true);
});

it("reveals the Courses selector from the disabled-instance metadata probe", async () => {
  window.history.replaceState(null, "", "/library?type=recipes");
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/public/config") return Promise.resolve(jsonResponse({ courses_enabled: false }));
    if (url === "/api/library/recipes?limit=24&offset=0") return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    if (url === "/api/library/courses?limit=1&offset=0") return Promise.resolve(jsonResponse({ items: [{ availability: "unavailable", relationships: ["saved"], bookmark_id: "opaque", created_at: "then" }], total: 1, limit: 1, offset: 0 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  expect(await screen.findByRole("button", { name: "Courses" })).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith("/api/library/courses?limit=1&offset=0", { cache: "no-store", credentials: "same-origin" });
});

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

it("restores a multiword URL query and ignores a stale query response", async () => {
  window.history.replaceState(null, "", "/library?type=recipes&q=tomato+soup");
  let resolveOld!: (response: Response) => void;
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/library/recipes?limit=24&offset=0&q=tomato+soup") return new Promise<Response>((resolve) => { resolveOld = resolve; });
    if (url === "/api/library/recipes?limit=24&offset=0&q=lentil+soup") return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  const input = await screen.findByRole("searchbox", { name: "Search your recipes" });
  expect((input as HTMLInputElement).value).toBe("tomato soup");
  fireEvent.change(input, { target: { value: "lentil soup" } });
  await screen.findByText("Unavailable saved references cannot be searched because their source content is unavailable.");
  expect(window.location.search).toBe("?type=recipes&q=lentil+soup");
  await act(async () => resolveOld(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["owned"], recipe: { ...recipe, id: "old", title: "Stale tomato soup" } }], total: 25, limit: 24, offset: 0 })));
  expect(screen.queryByText("Stale tomato soup")).toBeNull();
  expect(calls).toContain("/api/library/recipes?limit=24&offset=0&q=tomato+soup");
  expect(calls).toContain("/api/library/recipes?limit=24&offset=0&q=lentil+soup");
  expect(calls.some((url) => url.includes("/api/library/cookbooks?") && url.includes("q="))).toBe(false);
});

it("resets a later recipe page to offset zero when the search query changes", async () => {
  window.history.replaceState(null, "", "/library?type=recipes&q=older");
  const calls: string[] = [];
  const firstItems = Array.from({ length: 24 }, (_, index) => ({
    availability: "available",
    target_kind: "local",
    relationships: ["owned"],
    recipe: { ...recipe, id: `older-${index}`, title: `Older recipe ${index}` },
  }));
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/library/recipes?limit=24&offset=0&q=older")
      return Promise.resolve(jsonResponse({ items: firstItems, total: 50, limit: 24, offset: 0 }));
    if (url === "/api/library/recipes?limit=24&offset=24&q=older")
      return Promise.resolve(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["owned"], recipe: { ...recipe, id: "older-24", title: "Older recipe 24" } }], total: 50, limit: 24, offset: 24 }));
    if (url === "/api/library/recipes?limit=24&offset=0&q=newer")
      return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  await screen.findByText("Showing 24 of 50 recipes.");
  fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  await screen.findByText("Showing 25 of 50 recipes.");
  expect(calls).toContain("/api/library/recipes?limit=24&offset=24&q=older");
  fireEvent.change(screen.getByRole("searchbox", { name: "Search your recipes" }), { target: { value: "newer" } });
  await screen.findByText("Unavailable saved references cannot be searched because their source content is unavailable.");
  expect(calls).toContain("/api/library/recipes?limit=24&offset=0&q=newer");
  expect(calls.some((url) => url.includes("offset=24") && url.includes("q=newer"))).toBe(false);
});

it("ignores a pending recipe search response after the signed-in actor changes", async () => {
  window.history.replaceState(null, "", "/library?type=recipes&q=actor");
  const nextUser = { ...user, id: "reader-2", email: "second@example.test", display_name: "Second Reader" };
  let sessionChecks = 0;
  let searchRequests = 0;
  let resolveFirstSearch!: (response: Response) => void;
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(jsonResponse(sessionChecks === 1 ? user : nextUser));
    }
    if (url === "/api/auth/logout" && init?.method === "POST") return Promise.resolve(new Response(null, { status: 204 }));
    if (url === "/api/library/recipes?limit=24&offset=0&q=actor") {
      searchRequests += 1;
      if (searchRequests === 1) return new Promise<Response>((resolve) => { resolveFirstSearch = resolve; });
      return Promise.resolve(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["owned"], recipe: { ...recipe, id: "second-actor", title: "Second actor recipe" } }], total: 1, limit: 24, offset: 0 }));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  await screen.findByRole("searchbox", { name: "Search your recipes" });
  fireEvent.click(screen.getByRole("button", { name: "Reader account menu" }));
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  expect(await screen.findByRole("link", { name: "Open Second actor recipe" })).toBeTruthy();
  await act(async () => resolveFirstSearch(jsonResponse({ items: [{ availability: "available", target_kind: "local", relationships: ["owned"], recipe: { ...recipe, id: "first-actor-secret", title: "First actor stale recipe" } }], total: 25, limit: 24, offset: 0 })));
  expect(screen.queryByText("First actor stale recipe")).toBeNull();
  expect(screen.getByRole("link", { name: "Open Second actor recipe" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  expect(calls.filter((url) => url === "/api/library/recipes?limit=24&offset=0&q=actor")).toHaveLength(2);
  expect(calls.some((url) => url.includes("offset=24") && url.includes("q=actor"))).toBe(false);
});

it("hides old-query Load more after the new query fails", async () => {
  window.history.replaceState(null, "", "/library?type=recipes&q=older");
  const calls: string[] = [];
  const oldItems = Array.from({ length: 24 }, (_, index) => ({
    availability: "available",
    target_kind: "local",
    relationships: ["owned"],
    recipe: { ...recipe, id: `old-${index}`, title: `Old recipe ${index}` },
  }));
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/library/recipes?limit=24&offset=0&q=older")
      return Promise.resolve(jsonResponse({ items: oldItems, total: 25, limit: 24, offset: 0 }));
    if (url === "/api/library/recipes?limit=24&offset=0&q=newer")
      return Promise.resolve(jsonResponse({}, 503));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  expect(await screen.findByRole("button", { name: "Load more" })).toBeTruthy();
  fireEvent.change(screen.getByRole("searchbox", { name: "Search your recipes" }), { target: { value: "newer" } });
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  expect(screen.queryByText("Old recipe 0")).toBeNull();
  expect(calls).toContain("/api/library/recipes?limit=24&offset=0&q=newer");
  expect(calls.some((url) => url.includes("offset=24") && url.includes("q=older"))).toBe(false);
});

it("clears recipe search when switching to cookbooks and restores it through history", async () => {
  window.history.replaceState(null, "", "/library?type=recipes&q=tomato+soup");
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url.startsWith("/api/library/")) return Promise.resolve(jsonResponse({ items: [], total: 0, limit: 24, offset: 0 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<LibraryRoot />);
  await screen.findByText("My Library includes items you own, can access through a cookbook, or bookmark.");
  fireEvent.click(screen.getByRole("button", { name: "Cookbooks" }));
  await waitFor(() => expect(window.location.search).toBe("?type=cookbooks"));
  expect(screen.queryByRole("searchbox", { name: "Search your recipes" })).toBeNull();
  expect(calls.some((url) => url.startsWith("/api/library/cookbooks?") && url.includes("q="))).toBe(false);
  window.history.back();
  act(() => window.dispatchEvent(new PopStateEvent("popstate")));
  await waitFor(() => expect((screen.getByRole("searchbox", { name: "Search your recipes" }) as HTMLInputElement).value).toBe("tomato soup"));
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
  await screen.findByText("My Library includes items you own, can access through a cookbook, or bookmark.");
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
  await screen.findByText("My Library includes items you own, can access through a cookbook, or bookmark.");
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
  await screen.findByText("My Library includes items you own, can access through a cookbook, or bookmark.");
  expect(fetchMock.mock.calls.filter(([input]) => String(input).startsWith("/api/library/recipes?"))).toHaveLength(1);
});
