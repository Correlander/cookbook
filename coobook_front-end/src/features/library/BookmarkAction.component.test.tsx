import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import BookmarkAction from "./BookmarkAction.tsx";

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}
function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("uses exact status for detail actions and never scans the first bookmark page", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/library/bookmarks/status?local_recipe_id=recipe-30") return Promise.resolve(jsonResponse({ saved: true, bookmark_id: "bookmark-30" }));
    if (url === "/api/library/bookmarks/bookmark-30" && init?.method === "DELETE") return Promise.resolve(new Response(null, { status: 204 }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<BookmarkAction currentUserID="actor-a" target={{ local_recipe_id: "recipe-30" }} />);
  fireEvent.click(await screen.findByRole("button", { name: "Remove from Library" }));
  expect(await screen.findByRole("button", { name: "Save to Library" })).toBeTruthy();
  expect(fetchMock.mock.calls.some(([input]) => String(input).startsWith("/api/library/bookmarks?"))).toBe(false);
});

it("keeps an older actor and target status response from replacing the current target", async () => {
  const firstStatus = deferred<Response>();
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("local_recipe_id=recipe-a")) return firstStatus.promise;
    if (url.endsWith("local_recipe_id=recipe-b")) return Promise.resolve(jsonResponse({ saved: true, bookmark_id: "bookmark-b" }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<BookmarkAction currentUserID="actor-a" target={{ local_recipe_id: "recipe-a" }} />);
  view.rerender(<BookmarkAction currentUserID="actor-b" target={{ local_recipe_id: "recipe-b" }} />);
  expect(await screen.findByRole("button", { name: "Remove from Library" })).toBeTruthy();
  await act(async () => { firstStatus.resolve(jsonResponse({ saved: false })); await firstStatus.promise; });
  expect(screen.getByRole("button", { name: "Remove from Library" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Save to Library" })).toBeNull();
});

it("keeps status unknown after a failed read until the user retries", async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse({}, 404))
    .mockResolvedValueOnce(jsonResponse({ saved: false }));
  vi.stubGlobal("fetch", fetchMock);
  render(<BookmarkAction currentUserID="actor-a" target={{ remote_object_uri: "https://peer.example/federation/recipes/abc" }} />);
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Save to Library" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry saved status" }));
  expect(await screen.findByRole("button", { name: "Save to Library" })).toBeTruthy();
});

it("does not replay a save after a lost response and reconciles with exact status", async () => {
  let saves = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/library/bookmarks/status?local_recipe_id=recipe-lost")
      return Promise.resolve(jsonResponse(saves ? { saved: true, bookmark_id: "bookmark-lost" } : { saved: false }));
    if (url === "/api/library/bookmarks" && init?.method === "POST") {
      saves += 1;
      return Promise.reject(new Error("connection lost"));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<BookmarkAction currentUserID="actor-a" target={{ local_recipe_id: "recipe-lost" }} onMutationUncertain={() => {}} />);
  fireEvent.click(await screen.findByRole("button", { name: "Save to Library" }));
  expect(await screen.findByRole("button", { name: "Remove from Library" })).toBeTruthy();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
});

it("uses conditional mode for a new remote save and reports the captured copy", async () => {
  const uri = "https://peer.example/federation/recipes/new";
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.startsWith("/api/library/bookmarks/status?")) return Promise.resolve(jsonResponse({ saved: false }));
    if (url === "/api/library/bookmarks" && init?.method === "POST") {
      expect(JSON.parse(String(init.body))).toEqual({ target: { remote_object_uri: uri }, save_mode: "conditional" });
      return Promise.resolve(jsonResponse({
        id: "pin-new", target_kind: "remote", target: { remote_object_uri: uri }, availability: "available", created_at: "now", saved: true,
        save_state: "snapshot", provenance_state: "publisher_verified", snapshot_recipe: { object_uri: uri, source_origin: "https://peer.example", source_recipe_id: "new", creator_name: "Cook", sequence: 2, updated_at: "now", recipe: { title: "Soup", description: "", ingredients: [], steps: [], servings: 1, prep_time_minutes: 0, cook_time_minutes: 1, tags: [] } },
      }, 201));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<BookmarkAction currentUserID="actor-a" target={{ remote_object_uri: uri }} />);
  fireEvent.click(await screen.findByRole("button", { name: "Save to Library" }));
  expect(await screen.findByText("Private copy saved. It remains fixed to the captured version.")).toBeTruthy();
  expect(fetchMock.mock.calls.some(([input]) => String(input).startsWith("/api/library/recipes/remote?"))).toBe(false);
});

it("requires an explicit action to capture an existing reference and never downgrades a capture error", async () => {
  const uri = "https://peer.example/federation/recipes/existing";
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === `/api/library/bookmarks/status?remote_object_uri=${encodeURIComponent(uri)}`)
      return Promise.resolve(jsonResponse({ saved: true, bookmark_id: "bookmark-existing", can_capture_snapshot: true }));
    if (url === "/api/library/bookmarks/bookmark-existing/capture" && init?.method === "POST")
      return Promise.resolve(jsonResponse({ error: "snapshot_limit_reached" }, 409));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<BookmarkAction currentUserID="actor-a" target={{ remote_object_uri: uri }} />);
  fireEvent.click(await screen.findByRole("button", { name: "Keep this version" }));
  expect((await screen.findByRole("alert")).textContent).toContain("Your private snapshot storage limit has been reached.");
  expect(screen.getByRole("button", { name: "Keep this version" })).toBeTruthy();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
});
