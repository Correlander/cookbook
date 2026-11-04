import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AccountRoot from "./AccountRoot.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("keeps account details guarded while session is pending, then links existing pages", async () => {
  let resolve!: (response: Response) => void;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input) === "/api/auth/me" ? new Promise<Response>((done) => { resolve = done; }) : Promise.resolve(new Response("{}", { status: 404 }))));
  render(<AccountRoot />);
  expect(screen.getByText("Checking your session…")).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Manage creator profile" })).toBeNull();
  resolve(new Response(JSON.stringify({ id: "reader", email: "reader@example.test", display_name: "Reader" }), { status: 200, headers: { "content-type": "application/json" } }));
  const profileLink = await screen.findByRole("link", { name: /Manage creator profile/ });
  expect(profileLink.getAttribute("href")).toBe("/account/creator-profile");
  expect(screen.getByRole("link", { name: /Manage account security/ }).getAttribute("href")).toBe("/account/security");
});

it("offers sign-in rather than account details when anonymous", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(null, { status: 401 }))));
  render(<AccountRoot />);
  expect(await screen.findByRole("heading", { name: "Sign in to continue" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Manage account security" })).toBeNull();
});
