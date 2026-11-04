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
  return <p>{state.config.auth_methods.email_link ? "Email enabled" : "Email disabled"} · course attribution {state.config.course_creator_attribution ? "enabled" : "disabled"}</p>;
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

it("rejects malformed capability data instead of fabricating availability", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(configResponse({}))));

  render(<Probe />);

  expect((await screen.findByRole("alert")).textContent).toContain(
    "Could not load sign-in options. Try again.",
  );
  expect(screen.queryByText("Email enabled")).toBeNull();
});
