import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AuthConfirmRoot from "./AuthConfirmRoot.tsx";

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

afterEach(() => {
  cleanup();
  window.history.replaceState({}, "", "/");
  vi.unstubAllGlobals();
});

it("mounts a standalone auth route with one session check and no Home content reads", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({}, 401));
    if (url === "/api/public/config")
      return Promise.resolve(
        response({
          auth_methods: { email_link: true, passkey: false },
          registration_policy: "invite_only",
          public_instance: false,
          courses_enabled: false,
        }),
      );
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm");

  render(<AuthConfirmRoot />);

  expect(
    await screen.findByRole("heading", { name: "Continue to Cookbook" }),
  ).toBeTruthy();
  expect(screen.getByRole("main").getAttribute("id")).toBe("home");
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.filter(
        ([input]) => String(input) === "/api/auth/me",
      ),
    ).toHaveLength(1),
  );
  expect(
    fetchMock.mock.calls.some(([input]) =>
      /recipes|cookbooks|courses/.test(String(input)),
    ),
  ).toBe(false);
  expect(
    fetchMock.mock.calls.some(
      ([input]) => String(input) === "/api/auth/consume",
    ),
  ).toBe(false);
});

it("does not auto-consume a token for an already authenticated viewer", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(
        response({
          id: "reader-1",
          email: "reader@example.test",
          display_name: "Reader",
          is_admin: false,
        }),
      );
    if (url === "/api/public/config")
      return Promise.resolve(
        response({
          auth_methods: { email_link: true, passkey: false },
          registration_policy: "invite_only",
          public_instance: false,
          courses_enabled: false,
        }),
      );
    if (url === "/api/instance/authority")
      return Promise.resolve(
        response({
          is_instance_owner: false,
          is_admin: false,
          owner_claimed: true,
          can_manage_instance_roles: false,
        }),
      );
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(response({ transfers: [] }));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=still-explicit");

  render(<AuthConfirmRoot />);

  expect(
    await screen.findByRole("button", { name: "Continue to Cookbook" }),
  ).toBeTruthy();
  expect(
    fetchMock.mock.calls.filter(([input]) => String(input) === "/api/auth/me"),
  ).toHaveLength(1);
  expect(
    fetchMock.mock.calls.some(
      ([input]) => String(input) === "/api/auth/consume",
    ),
  ).toBe(false);
  expect(
    fetchMock.mock.calls.some(([input]) =>
      /\/api\/(recipes|cookbooks|courses)/.test(String(input)),
    ),
  ).toBe(false);
});
