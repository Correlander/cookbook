import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AdminSettingsRoot from "./AdminSettingsRoot.tsx";

const admin = {
  id: "admin-1",
  email: "admin@example.test",
  display_name: "Admin",
  is_admin: true,
};

const settings = {
  public_instance: false,
  courses_enabled: false,
  registration_policy: "invite_only",
  revision_retention_days: 365,
  audit_retention_days: 365,
  federation_enabled: false,
  federation_origin: "",
  public_cookbooks: 0,
  public_recipes: 0,
  public_courses: 0,
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("does not request instance settings for an ordinary signed-in user", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(jsonResponse({ ...admin, is_admin: false }));
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

  render(<AdminSettingsRoot />);

  expect(
    await screen.findByRole("heading", { name: "Administrator access required" }),
  ).toBeTruthy();
  expect(fetchMock.mock.calls.some(([url]) => String(url) === "/api/admin/settings"))
    .toBe(false);
  expect(screen.getByRole("heading", { name: "Instance administration", level: 1 }))
    .toBeTruthy();
});

it("renders the administrator sign-in guard for an anonymous session", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/auth/me")
      return Promise.resolve(jsonResponse({}, 401));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettingsRoot />);

  expect(
    await screen.findByRole("heading", { name: "Sign in to continue" }),
  ).toBeTruthy();
  expect(fetchMock.mock.calls.some(([url]) => String(url) === "/api/admin/settings"))
    .toBe(false);
  expect(screen.getByRole("heading", { name: "Instance administration", level: 1 }))
    .toBeTruthy();
});

it("loads settings for an administrator and keeps server denials from exposing controls", async () => {
  let settingsStatus = 200;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(jsonResponse(admin));
    if (url === "/api/instance/authority")
      return Promise.resolve(jsonResponse({
        is_instance_owner: false,
        is_admin: true,
        owner_claimed: true,
        can_manage_instance_roles: false,
      }));
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(jsonResponse({ transfers: [] }));
    if (url === "/api/admin/settings")
      return Promise.resolve(jsonResponse(settingsStatus === 200 ? settings : {}, settingsStatus));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  const view = render(<AdminSettingsRoot />);

  expect(
    await screen.findByRole("checkbox", { name: /Enable public browsing/ }),
  ).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith("/api/admin/settings");
  expect(screen.getByRole("link", { name: "Instance" })).toBeTruthy();
  view.unmount();

  settingsStatus = 403;
  render(<AdminSettingsRoot />);
  await waitFor(() =>
    expect(screen.queryByRole("checkbox", { name: /Enable public browsing/ })).toBeNull(),
  );
  expect(screen.getByRole("heading", { name: "Instance administration", level: 1 }))
    .toBeTruthy();
});
