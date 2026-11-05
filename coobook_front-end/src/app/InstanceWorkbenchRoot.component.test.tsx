import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import InstanceWorkbenchRoot from "./InstanceWorkbenchRoot.tsx";

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("owns an independent session check and does not request authority after anonymous resolution", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    void input;
    return Promise.resolve(response({}, 401));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByRole("heading", { name: "Sign in to view instance access" })).toBeTruthy();
  expect(fetchMock.mock.calls.map(([input]) => String(input)).sort()).toEqual([
    "/api/auth/me",
    "/api/public/config",
  ]);
  expect(fetchMock).toHaveBeenCalledWith("/api/auth/me", { cache: "no-store" });
  expect(fetchMock.mock.calls.some(([input]) =>
    /\/api\/(?:instance\/authority|instance\/owner-transfers)/.test(String(input)),
  )).toBe(false);
  expect(screen.queryByText("Welcome back")).toBeNull();
});

it("retries an independent session check after a transient failure", async () => {
  let checks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/auth/me") {
      checks += 1;
      return Promise.resolve(checks === 1 ? response({}, 503) : response({}, 401));
    }
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByRole("heading", { name: "Could not check your session" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry session check" }));
  expect(await screen.findByRole("heading", { name: "Sign in to view instance access" })).toBeTruthy();
  await waitFor(() => expect(checks).toBe(2));
});
