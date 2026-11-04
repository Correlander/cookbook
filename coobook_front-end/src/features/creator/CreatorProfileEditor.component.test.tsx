import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CreatorProfileEditor from "./CreatorProfileEditor.tsx";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status });
const profile = {
  id: "p1",
  public_name: "",
  bio: "",
  visibility: "private",
  created_at: "",
  updated_at: "",
};

it("loads an empty private profile without copying account identity and explains visibility", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response({ profile: null }));
  vi.stubGlobal("fetch", fetchMock);
  render(<CreatorProfileEditor actorID="u1" onSessionExpired={vi.fn()} />);
  expect(
    await screen.findByRole("heading", { name: "Creator profile details" }),
  ).toBeTruthy();
  expect((screen.getByLabelText("Public name") as HTMLInputElement).value).toBe(
    "",
  );
  expect(screen.getByLabelText("Profile visibility")).toBeTruthy();
  expect(screen.getByText(/Only this account can view/)).toBeTruthy();
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/account/creator-profile");
});

it("requires a name for instance visibility and retains input after save errors", async () => {
  let calls = 0;
  const fetchMock = vi.fn((url: RequestInfo | URL) => {
    if (String(url) === "/api/account/creator-profile" && !calls++)
      return Promise.resolve(response({ profile }));
    return Promise.resolve(response({ error: "invalid_profile" }, 400));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CreatorProfileEditor actorID="u1" onSessionExpired={vi.fn()} />);
  await screen.findByRole("heading", { name: "Creator profile details" });
  fireEvent.change(screen.getByLabelText("Profile visibility"), {
    target: { value: "instance" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save profile" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "enter a profile name",
  );
  fireEvent.change(screen.getByLabelText("Public name"), {
    target: { value: "Baker" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save profile" }));
  expect((await screen.findByRole("alert")).textContent).toContain("not valid");
  expect((screen.getByLabelText("Public name") as HTMLInputElement).value).toBe(
    "Baker",
  );
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
});

it("shows an authenticated same-instance preview only after profile creation", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        response({ profile: { ...profile, public_name: "Baker" } }),
      ),
  );
  render(<CreatorProfileEditor actorID="u1" onSessionExpired={vi.fn()} />);
  const preview = await screen.findByRole("link", {
    name: "Preview private profile",
  });
  expect(preview.getAttribute("href")).toBe("/creator?id=p1");
});

it("routes an expired profile session through the session retry callback", async () => {
  const onSessionExpired = vi.fn();
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(response({ error: "unauthorized" }, 401)),
  );
  render(
    <CreatorProfileEditor actorID="u1" onSessionExpired={onSessionExpired} />,
  );
  fireEvent.click(await screen.findByRole("button", { name: "Retry session" }));
  expect(onSessionExpired).toHaveBeenCalledTimes(1);
});
