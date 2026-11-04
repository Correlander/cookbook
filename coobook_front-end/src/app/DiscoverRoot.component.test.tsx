import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { afterEach, expect, it, vi } from "vitest";
import DiscoverRoot from "./DiscoverRoot.tsx";

function response(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => {
  cleanup(); vi.unstubAllGlobals(); window.history.replaceState({}, "", "/");
});

it("server-renders the static Browse shell without browser globals", () => {
  const browserWindow = globalThis.window;
  const fetchMock = vi.fn();
  vi.stubGlobal("window", undefined);
  vi.stubGlobal("fetch", fetchMock);
  try {
    const html = renderToString(<DiscoverRoot />);
  expect(html).toContain("Browse");
    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain("Checking your session");
    expect(fetchMock).not.toHaveBeenCalled();
  } finally {
    vi.stubGlobal("window", browserWindow);
  }
});

it("reads, writes and restores the selected type through the route query", async () => {
  window.history.replaceState({}, "", "/discover?type=cookbooks");
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "u1", email: "u@example.test", display_name: "User", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(response({ courses_enabled: true, public_instance: true }));
    if (url === "/api/instance/authority") return Promise.resolve(response({ is_admin: false, is_instance_owner: false, owner_claimed: true, can_manage_instance_roles: false }));
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    if (url.startsWith("/api/cookbooks?")) return Promise.resolve(response([]));
    if (url.startsWith("/api/public/cookbooks")) return Promise.resolve(response([]));
    return Promise.resolve(response({}));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<DiscoverRoot />);
  expect(await screen.findByRole("heading", { name: "Cookbooks you can access" })).toBeTruthy();
  expect(screen.getByText(/recipes and cookbooks available from this instance/i)).toBeTruthy();
  expect(screen.queryByRole("button", { name: /create cookbook/i })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Recipes" }));
  await waitFor(() => expect(new URLSearchParams(window.location.search).get("type")).toBe("recipes"));
  window.history.replaceState({}, "", "/discover?type=courses");
  window.dispatchEvent(new PopStateEvent("popstate"));
  await waitFor(() => expect(screen.getByRole("button", { name: "Courses" }).getAttribute("aria-pressed")).toBe("true"));
  expect(screen.queryByRole("button", { name: /create/i })).toBeNull();
});

it("requests one bounded public course catalog", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response(null, 401));
    if (url === "/api/public/config") return Promise.resolve(response({ courses_enabled: true, public_instance: true }));
    if (url.startsWith("/api/public/courses")) return Promise.resolve(response([]));
    return Promise.resolve(response({}));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/discover?type=courses");
  render(<DiscoverRoot />);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/public/courses?limit=24"));
  expect(fetchMock.mock.calls.filter(([input]) => String(input).startsWith("/api/public/courses")).length).toBe(1);
});
