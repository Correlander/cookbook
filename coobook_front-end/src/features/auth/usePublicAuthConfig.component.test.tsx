import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import usePublicAuthConfig from "./usePublicAuthConfig.ts";

function Probe() {
  const { state, retry } = usePublicAuthConfig();
  if (state.status === "loading")
    return <p role="status">Loading sign-in options…</p>;
  if (state.status === "error")
    return (
      <>
        <p role="alert">{state.message}</p>
        <button onClick={retry}>Retry</button>
      </>
    );
  return <p>{state.config.auth_methods.email_link ? "Email enabled" : "Email disabled"} · course attribution {state.config.course_creator_attribution ? "enabled" : "disabled"} · menus {state.config.menu_api ? "enabled" : "disabled"} · Home {state.config.anonymous_home_preset} · brand {state.config.branding?.site_name} · palette {state.config.branding?.palette}</p>;
}

function configResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("keeps configuration unknown while loading and exposes a retry after failure", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(configResponse({}, 500))
    .mockResolvedValueOnce(
      configResponse({
        auth_methods: { email_link: true, passkey: false },
        registration_policy: "invite_only",
        public_instance: false,
        courses_enabled: false,
        course_creator_attribution: true,
      }),
    );
  vi.stubGlobal("fetch", fetchMock);

  render(<Probe />);
  expect(await screen.findByRole("status")).toBeTruthy();
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Could not load sign-in options. Try again.",
  );

  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText(/Email enabled/)).toBeTruthy();
  expect(screen.getByText(/course attribution enabled/)).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it.each([
  ["absent", undefined],
  ["false", false],
  ["malformed", "true"],
])("fails closed for an %s course attribution signal", async (_label, capability) => {
  const config: Record<string, unknown> = {
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: false,
    courses_enabled: true,
  };
  if (capability !== undefined) config.course_creator_attribution = capability;
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse(config))));
  render(<Probe />);
  expect(await screen.findByText(/course attribution disabled/)).toBeTruthy();
});

it.each([
  ["absent", undefined],
  ["false", false],
  ["string true", "true"],
  ["malformed object", { enabled: true }],
])("fails closed for an %s menu protocol capability", async (_label, capability) => {
  const config: Record<string, unknown> = {
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: false,
    courses_enabled: false,
  };
  if (capability !== undefined) config.menu_api = capability;
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse(config))));
  render(<Probe />);
  expect(await screen.findByText(/menus disabled/)).toBeTruthy();
});

it("enables menus only for an explicit supported protocol response", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse({
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: false,
    courses_enabled: false,
    menu_api: true,
  }))));
  render(<Probe />);
  expect(await screen.findByText(/menus enabled/)).toBeTruthy();
});

it.each([
  ["missing", undefined],
  ["unknown", "publication_first"],
  ["malformed", { value: "discovery_first" }],
])("defaults %s anonymous homepage config to library-first", async (_label, preset) => {
  const config: Record<string, unknown> = {
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: true,
    courses_enabled: false,
  };
  if (preset !== undefined) config.anonymous_home_preset = preset;
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse(config))));
  render(<Probe />);
  expect(await screen.findByText(/Home library_first/)).toBeTruthy();
});

it("accepts the explicit discovery-first preset", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse({
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: true,
    courses_enabled: false,
    anonymous_home_preset: "discovery_first",
  }))));
  render(<Probe />);
  expect(await screen.findByText(/Home discovery_first/)).toBeTruthy();
});

it("rejects malformed capability data instead of fabricating availability", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse({}))));

  render(<Probe />);

  expect((await screen.findByRole("alert")).textContent).toContain(
    "Could not load sign-in options. Try again.",
  );
  expect(screen.queryByText("Email enabled")).toBeNull();
});

it.each([
  ["missing branding", undefined],
  ["missing name", {}],
  ["empty name", { site_name: " \u2003 " }],
  ["overlong name", { site_name: "x".repeat(49) }],
  ["control character", { site_name: "Kitchen\nname" }],
  ["unknown name shape", { site_name: { text: "Kitchen" } }],
])("keeps valid auth config and defaults %s to Cookbook", async (_label, branding) => {
  const value: Record<string, unknown> = {
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: true,
    courses_enabled: false,
  };
  if (branding !== undefined) value.branding = branding;
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse(value))));
  render(<Probe />);
  expect(await screen.findByText(/Email enabled/)).toBeTruthy();
  expect(screen.getByText(/brand Cookbook/)).toBeTruthy();
});

it("trims and preserves a valid Unicode instance name while ignoring unknown palette values", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse({
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: true,
    courses_enabled: false,
    branding: { site_name: "  Crème 🍲  ", palette: "untrusted-css" },
  }))));
  render(<Probe />);
  expect(await screen.findByText(/brand Crème 🍲/)).toBeTruthy();
  expect(screen.getByText(/palette garden/)).toBeTruthy();
});

it.each([
  ["garden", "garden"],
  ["plum", "plum"],
  ["missing", "garden"],
  ["malformed", "garden"],
])("normalizes the %s palette to the finite local value %s", async (input, expected) => {
  const branding: Record<string, unknown> = { site_name: "Kitchen" };
  if (input === "garden" || input === "plum") branding.palette = input;
  if (input === "malformed") branding.palette = { selector: "body" };
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse({
    auth_methods: { email_link: true, passkey: false },
    registration_policy: "invite_only",
    public_instance: true,
    courses_enabled: false,
    branding,
  }))));
  render(<Probe />);
  expect(await screen.findByText(new RegExp(`palette ${expected}$`))).toBeTruthy();
});
