import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CookbooksRoot from "./CookbooksRoot.tsx";

const user = {
  id: "owner-1",
  email: "owner@example.test",
  display_name: "Owner",
  is_admin: false,
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

it("waits for confirmed anonymity before loading and searching public cookbooks", async () => {
  window.history.replaceState(null, "", "/cookbooks?q=summer");
  const session = deferred<Response>();
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return session.promise;
    return Promise.resolve(jsonResponse([
      {
        id: "public-1",
        title: "Summer table",
        description: "Fresh meals.",
        recipe_count: 3,
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
      },
    ]));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbooksRoot />);
  expect(await screen.findByText("Checking your session…")).toBeTruthy();
  expect(calls.slice().sort()).toEqual(["/api/auth/me", "/api/public/config"]);
  await act(async () => {
    session.resolve(jsonResponse({}, 401));
    await session.promise;
  });
  expect(await screen.findByRole("heading", { name: "Browse public cookbooks" })).toBeTruthy();
  expect(screen.getByText("3 public recipes")).toBeTruthy();
  expect(calls).toContain("/api/public/cookbooks?limit=24&offset=0&q=summer");
  expect(calls.filter((url) => url === "/api/public/config")).toHaveLength(1);
  expect(calls.some((url) => url.startsWith("/api/cookbooks"))).toBe(false);
  expect(screen.getByRole("link", { name: "Open cookbook" }).getAttribute("href"))
    .toBe("/cookbook?id=public-1&return_q=summer");
});

it("uses only the authenticated access union and owner controls for a signed-in visitor", async () => {
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(user));
    if (url === "/api/cookbooks?limit=24&offset=0")
      return Promise.resolve(jsonResponse([
        { id: "owned", title: "My kitchen", description: "", visibility: "private", owner_id: user.id },
        { id: "shared", title: "Shared kitchen", description: "", visibility: "public", owner_id: "other" },
      ]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbooksRoot />);
  expect(await screen.findByRole("heading", { name: "Cookbooks you can access" })).toBeTruthy();
  expect(await screen.findByText("My kitchen")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Edit cookbook" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Share and manage members" })).toBeTruthy();
  expect(screen.queryByLabelText("Search public cookbooks")).toBeNull();
  expect(calls).toContain("/api/cookbooks?limit=24&offset=0");
  expect(calls.some((url) => url.startsWith("/api/public/cookbooks"))).toBe(false);
  const ownedCard = screen.getByText("My kitchen").closest("article");
  expect(ownedCard?.querySelector("a")?.getAttribute("href"))
    .toBe("/cookbook?id=owned");
});

it("keeps cookbook reads stopped after a session error and retries the session first", async () => {
  const firstSession = deferred<Response>();
  const calls: string[] = [];
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return sessionChecks === 1
        ? firstSession.promise
        : Promise.resolve(jsonResponse({}, 401));
    }
    return Promise.resolve(jsonResponse([]));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbooksRoot />);
  expect(await screen.findByText("Checking your session…")).toBeTruthy();
  expect(calls.slice().sort()).toEqual(["/api/auth/me", "/api/public/config"]);
  await act(async () => {
    firstSession.resolve(jsonResponse({}, 503));
    await firstSession.promise;
  });
  expect(await screen.findByRole("heading", { name: "Could not check your session" })).toBeTruthy();
  expect(calls.slice().sort()).toEqual(["/api/auth/me", "/api/public/config"]);
  fireEvent.click(screen.getByRole("button", { name: "Retry session check" }));
  expect(await screen.findByRole("heading", { name: "Browse public cookbooks" })).toBeTruthy();
  await waitFor(() => expect(calls).toContain("/api/public/cookbooks?limit=24&offset=0"));
  expect(calls.slice().sort()).toEqual([
    "/api/auth/me",
    "/api/public/config",
    "/api/auth/me",
    "/api/public/cookbooks?limit=24&offset=0",
  ].sort());
  expect(calls.some((url) => url.startsWith("/api/cookbooks?"))).toBe(false);
});
