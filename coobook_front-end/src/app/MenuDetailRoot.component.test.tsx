import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import MenuDetailRoot from "./MenuDetailRoot.tsx";

const menuID = "a".repeat(32);
const config = { auth_methods: { email_link: true, passkey: false }, registration_policy: "invite_only", public_instance: false, courses_enabled: false };

function response(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/menu"); });

it("does not request private menu data for an anonymous visitor", async () => {
  window.history.replaceState(null, "", `/menu?id=${menuID}`);
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    return Promise.resolve(url === "/api/auth/me" ? response(null, 401) : response({ ...config, menu_api: true }));
  }));
  render(<MenuDetailRoot />);
  expect(await screen.findByRole("heading", { name: "Sign in to view this private menu" })).toBeTruthy();
  expect(calls.some((url) => url.startsWith("/api/menus/") || url.startsWith("/api/library/menus"))).toBe(false);
});

it.each([undefined, false, "true", { enabled: true }])("does not request private menu data when the protocol capability is %j", async (menuApi) => {
  window.history.replaceState(null, "", `/menu?id=${menuID}`);
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(response({ id: "owner", email: "owner@example.test", display_name: "Owner", is_admin: false }));
    return Promise.resolve(response({ ...config, ...(menuApi === undefined ? {} : { menu_api: menuApi }) }));
  }));
  render(<MenuDetailRoot />);
  expect(await screen.findByRole("heading", { name: "Menu unavailable" })).toBeTruthy();
  expect(calls.some((url) => url.startsWith("/api/menus/") || url.startsWith("/api/library/menus"))).toBe(false);
});

it("rejects duplicate, unrelated, and malformed query parameters before requesting a menu", async () => {
  for (const query of [`id=${menuID}&id=${menuID}`, `id=${menuID}&q=secret`, "id=not-an-id"]) {
    cleanup();
    window.history.replaceState(null, "", `/menu?${query}`);
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input); calls.push(url);
      return Promise.resolve(url === "/api/auth/me" ? response({ id: "owner", email: "owner@example.test", display_name: "Owner", is_admin: false }) : response(config));
    }));
    render(<MenuDetailRoot />);
    expect(await screen.findByRole("heading", { name: "Menu unavailable" })).toBeTruthy();
    expect(calls.some((url) => url.startsWith("/api/menus/"))).toBe(false);
    vi.unstubAllGlobals();
  }
});
