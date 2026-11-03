import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AccountSecurityRoot from "./AccountSecurityRoot.tsx";

const user = {
  id: "user-1",
  email: "cook@example.test",
  display_name: "Cook",
  is_admin: false,
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("renders the account root for an anonymous user without requesting security data", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/auth/me")
      return Promise.resolve(jsonResponse({}, 401));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AccountSecurityRoot />);

  expect(
    await screen.findByRole("heading", { name: "Sign in to continue" }),
  ).toBeTruthy();
  expect(screen.getByRole("main").getAttribute("id")).toBe("home");
  expect(screen.getByRole("heading", { name: "Account security", level: 1 })).toBeTruthy();
  expect(
    screen.getAllByRole("link", { name: "Sign in" })
      .every((link) => link.getAttribute("href") === "/?signin=1"),
  ).toBe(true);
  expect(fetchMock).toHaveBeenCalledWith("/api/auth/me", { cache: "no-store" });
  expect(fetchMock.mock.calls.some(([url]) =>
    ["/api/auth/passkeys", "/api/auth/sessions"].includes(String(url)),
  )).toBe(false);
});

it("keeps session errors retryable and loads account security after retry", async () => {
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(sessionChecks === 1
        ? jsonResponse({}, 503)
        : jsonResponse(user));
    }
    if (url === "/api/auth/passkeys" || url === "/api/auth/sessions")
      return Promise.resolve(jsonResponse([]));
    if (url === "/api/instance/authority")
      return Promise.resolve(jsonResponse({
        is_instance_owner: false,
        is_admin: false,
        owner_claimed: true,
        can_manage_instance_roles: false,
      }));
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(jsonResponse({ transfers: [] }));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AccountSecurityRoot />);

  expect(
    await screen.findByRole("heading", { name: "Could not check your session" }),
  ).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Sign in to continue" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry session check" }));

  expect(await screen.findByRole("heading", { name: "Browser sessions" })).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith("/api/auth/me", { cache: "no-store" });
  expect(fetchMock).toHaveBeenCalledWith("/api/auth/passkeys");
  expect(fetchMock).toHaveBeenCalledWith("/api/auth/sessions");
});
