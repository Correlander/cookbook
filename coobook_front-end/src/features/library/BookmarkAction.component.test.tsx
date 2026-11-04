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
