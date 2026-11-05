import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AuthenticatedUser } from "../apiModels";
import type { PublicAuthConfigState } from "../features/auth/usePublicAuthConfig.ts";
import AppNavigation from "./AppNavigation.tsx";

const ownerAuthority = {
  is_instance_owner: true,
  is_admin: false,
  owner_claimed: true,
  can_manage_instance_roles: true,
};

const ordinaryAuthority = {
  is_instance_owner: false,
  is_admin: false,
  owner_claimed: true,
  can_manage_instance_roles: false,
};

const ordinaryUser: AuthenticatedUser = {
  id: "reader-1",
  email: "reader@example.test",
  display_name: "Reader",
  is_admin: false,
};

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

function renderNavigation({
  user = ordinaryUser,
  pathname = "/library",
  sessionStatus,
  publicConfigState = { status: "ready", config: {
    auth_methods: { email_link: true, passkey: false }, registration_policy: "invite_only" as const,
    public_instance: false, courses_enabled: false, anonymous_home_preset: "library_first" as const,
    branding: { site_name: "Cookbook", palette: "garden" as const },
  } },
  omitPublicConfig = false,
}: {
  user?: AuthenticatedUser | null;
  pathname?: string;
  sessionStatus?: "loading" | "authenticated" | "anonymous" | "error";
  publicConfigState?: PublicAuthConfigState;
  omitPublicConfig?: boolean;
} = {}) {
  return render(
    <AppNavigation
      currentUser={user}
      pathname={pathname}
      sessionStatus={sessionStatus ?? (user ? "authenticated" : "anonymous")}
      onRetrySession={vi.fn()}
      {...(!omitPublicConfig ? { publicConfigState } : {})}
    />,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("shows Instance only after authority confirms the owner capability", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/authority")
      return Promise.resolve(response(ownerAuthority));
    return Promise.resolve(response({ transfers: [] }));
  }));

  renderNavigation();

  fireEvent.click(screen.getByRole("button", { name: "Reader account menu" }));
  expect((await screen.findByRole("link", { name: "Instance" })).getAttribute("href")).toBe("/admin");
  expect(screen.getByRole("link", { name: "Account" }).getAttribute("href")).toBe("/account");
});

it("keeps Account routed to security while marking creator-profile routes active", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/authority") return Promise.resolve(response(ordinaryAuthority));
    return Promise.resolve(response({ transfers: [] }));
  }));
  renderNavigation({ pathname: "/account/creator-profile" });
  fireEvent.click(screen.getByRole("button", { name: "Reader account menu" }));
  const account = screen.getByRole("link", { name: "Account" });
  expect(account.getAttribute("href")).toBe("/account");
  expect(screen.getByRole("link", { name: "Creator profile" }).getAttribute("aria-current")).toBe("page");
});

it("shows Instance to an exact incoming recipient and marks settings as part of that section", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/authority")
      return Promise.resolve(response(ordinaryAuthority));
    if (String(input) === "/api/instance/owner-transfers")
      return Promise.resolve(response({ transfers: [{
        id: "transfer-1",
        direction: "incoming",
        state: "pending",
        created_at: "2026-11-03T10:00:00Z",
        expires_at: "2026-11-10T10:00:00Z",
        counterparty_display_name: "Owner",
      }] }));
    return Promise.resolve(response({}, 404));
  }));

  renderNavigation({ pathname: "/admin/settings" });

  fireEvent.click(screen.getByRole("button", { name: "Reader account menu" }));
  const instanceLink = await screen.findByRole("link", { name: "Instance" });
  expect(instanceLink.getAttribute("href")).toBe("/admin");
  expect(instanceLink.getAttribute("aria-current")).toBe("page");
});

it("keeps ordinary users without incoming requests out of Instance navigation", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/authority")
      return Promise.resolve(response(ordinaryAuthority));
    return Promise.resolve(response({ transfers: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderNavigation();

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/instance/owner-transfers", { cache: "no-store" }));
  fireEvent.click(screen.getByRole("button", { name: "Reader account menu" }));
  await waitFor(() => expect(screen.queryByRole("link", { name: "Instance" })).toBeNull());
});

it("does not trust the account admin flag without current server authority", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/authority")
      return Promise.resolve(response(ordinaryAuthority));
    return Promise.resolve(response({ transfers: [] }));
  }));

  renderNavigation({ user: { ...ordinaryUser, is_admin: true } });

  fireEvent.click(screen.getByRole("button", { name: "Reader account menu" }));
  await waitFor(() => expect(screen.queryByRole("link", { name: "Instance" })).toBeNull());
  expect(screen.queryByRole("link", { name: "Administration" })).toBeNull();
});

it("shows the single Instance entry when current server authority confirms an administrator", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/authority")
      return Promise.resolve(response({
        ...ordinaryAuthority,
        is_admin: true,
      }));
    return Promise.resolve(response({ transfers: [] }));
  }));

  renderNavigation({ user: { ...ordinaryUser, is_admin: true } });

  fireEvent.click(screen.getByRole("button", { name: "Reader account menu" }));
  const instanceLink = await screen.findByRole("link", { name: "Instance" });
  expect(instanceLink.getAttribute("href")).toBe("/admin");
  expect(screen.queryByRole("link", { name: "Administration" })).toBeNull();
});

it("keeps a signed-out navigation free of authority and transfer requests", () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);

  renderNavigation({ user: null });

  expect(screen.getByRole("link", { name: "Sign in" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Home" }).getAttribute("href")).toBe("/");
  expect(screen.getByRole("link", { name: "Browse" }).getAttribute("href")).toBe("/discover");
  expect(screen.getByRole("link", { name: "My Library" }).getAttribute("href")).toBe("/library");
  expect(screen.queryByRole("link", { name: "Recipes" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Cookbooks" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Create" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Instance" })).toBeNull();
  expect(fetchMock).not.toHaveBeenCalled();
});

it("renders a supplied instance name as escaped text without a second config request", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) =>
    String(input) === "/api/instance/authority"
      ? Promise.resolve(response(ordinaryAuthority))
      : Promise.resolve(response({ transfers: [] })),
  );
  vi.stubGlobal("fetch", fetchMock);
  const name = "&lt;script&gt;Kitchen & Sons";
  renderNavigation({ publicConfigState: {
    status: "ready",
    config: {
      auth_methods: { email_link: true, passkey: false },
      registration_policy: "invite_only",
      public_instance: false,
      courses_enabled: false,
      anonymous_home_preset: "library_first",
      branding: { site_name: name, palette: "garden" },
    },
  } });

  const brand = screen.getByRole("link", { name: "Cookbook home" });
  expect(brand.textContent).toContain(name);
  expect(brand.querySelector("script")).toBeNull();
  await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input) === "/api/public/config")).toBe(false));
});

it("uses its existing config hook once when no route owner supplies state", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/public/config") return Promise.resolve(response({
      auth_methods: { email_link: true, passkey: false },
      registration_policy: "invite_only", public_instance: false, courses_enabled: false,
      branding: { site_name: "My Kitchen" },
    }));
    if (String(input) === "/api/instance/authority") return Promise.resolve(response(ordinaryAuthority));
    return Promise.resolve(response({ transfers: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderNavigation({ omitPublicConfig: true, user: null });

  expect(await screen.findByText("My Kitchen")).toBeTruthy();
  expect(fetchMock.mock.calls.filter(([input]) => String(input) === "/api/public/config")).toHaveLength(1);
});

it("marks the independent Discover route active", () => {
  vi.stubGlobal("fetch", vi.fn());
  renderNavigation({ pathname: "/discover" });
  expect(screen.getByRole("link", { name: "Browse" }).className).toContain("main-nav__active");
  expect(screen.getByRole("link", { name: "Browse" }).getAttribute("href")).toBe("/discover");
});

it("keeps a fast pending header quiet and announces delayed session checks", () => {
  vi.useFakeTimers();
  renderNavigation({ user: null, sessionStatus: "loading" });
  expect(screen.getByRole("status").textContent).toBe("");
  act(() => { vi.advanceTimersByTime(450); });
  expect(screen.getByRole("status").textContent).toBe("Checking session…");
  cleanup();
  vi.useRealTimers();
});

it("offers a retry when authority or incoming-transfer discovery fails", async () => {
  let calls = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/authority")
      return Promise.resolve(calls++ === 0 ? response({}, 500) : response(ordinaryAuthority));
    return Promise.reject(new TypeError("offline"));
  });
  vi.stubGlobal("fetch", fetchMock);

  renderNavigation();

  const retry = await screen.findByRole("button", { name: "Retry Instance check" });
  fireEvent.click(retry);
  await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(4));
  expect(screen.queryByRole("link", { name: "Instance" })).toBeNull();
});
