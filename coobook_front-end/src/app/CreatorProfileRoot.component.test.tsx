import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CreatorProfileRoot from "./CreatorProfileRoot.tsx";

afterEach(() => {
  cleanup();
  window.history.replaceState({}, "", "/");
  vi.unstubAllGlobals();
});

const user = {
  id: "actor-1",
  email: "cook@example.test",
  display_name: "Cook",
  is_admin: false,
};

function profileResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status });
}

it("provides an independent authenticated editor root with shared navigation", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(profileResponse(user));
    if (url === "/api/account/creator-profile")
      return Promise.resolve(profileResponse({ profile: null }));
    return Promise.resolve(profileResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CreatorProfileRoot mode="editor" />);

  expect(
    await screen.findByRole("heading", { name: "Creator profile details" }),
  ).toBeTruthy();
  expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  expect(
    screen
      .getByRole("link", { name: "Creator profile" })
      .getAttribute("aria-current"),
  ).toBe("page");
  fireEvent.click(screen.getByRole("button", { name: "Cook account menu" }));
  expect(
    screen.getByRole("link", { name: "Account" }).getAttribute("href"),
  ).toBe("/account");
  expect(fetchMock).toHaveBeenCalledWith("/api/auth/me", { cache: "no-store" });
});

it("does not request private profile data for an anonymous page visit", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) =>
    String(input) === "/api/auth/me"
      ? Promise.resolve(profileResponse({}, 401))
      : Promise.resolve(profileResponse({}, 404)),
  );
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/creator?id=secret");

  render(<CreatorProfileRoot mode="view" />);

  expect(
    await screen.findByRole("heading", { name: "Sign in to continue" }),
  ).toBeTruthy();
  expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
    "/api/auth/me",
  ]);
});

it("loads the linked profile under one page heading", async () => {
  const profile = {
    id: "p1",
    public_name: "Baker",
    bio: "Local profile",
    visibility: "instance",
    created_at: "",
    updated_at: "",
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(profileResponse(user));
    if (url === "/api/creator-profiles/p1")
      return Promise.resolve(profileResponse({ profile }));
    if (url.includes("/api/creator-profiles/p1/recipes?limit=24&offset=0"))
      return Promise.resolve(
        profileResponse({ profile, items: [], limit: 24, offset: 0 }),
      );
    if (url.includes("/api/creator-profiles/p1/cookbooks?limit=24&offset=0"))
      return Promise.resolve(
        profileResponse({ profile, items: [], limit: 24, offset: 0 }),
      );
    return Promise.resolve(profileResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/creator?id=p1");

  render(<CreatorProfileRoot mode="view" />);

  expect(await screen.findByRole("heading", { name: "Baker" })).toBeTruthy();
  expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  expect(screen.getByRole("heading", { name: "Baker" }).tagName).toBe("H2");
});
