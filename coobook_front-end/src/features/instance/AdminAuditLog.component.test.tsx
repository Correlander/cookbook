import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AdminAuditLog from "./AdminAuditLog.tsx";

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

function event(index: number) {
  return {
    id: `event-${index}`,
    actor_name: `Actor ${index}`,
    action: "recipe.updated",
    resource_type: "recipe",
    resource_id: `recipe-${index}`,
    created_at: "2026-11-03T10:00:00Z",
    response_status: 200,
  };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("loads 24 audit events and requests the next page with the current offset", async () => {
  const requests: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    requests.push(url);
    if (url === "/api/admin/audit?limit=24&offset=0")
      return Promise.resolve(response(Array.from({ length: 24 }, (_, index) => event(index + 1))));
    if (url === "/api/admin/audit?limit=24&offset=24")
      return Promise.resolve(response([event(25)]));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminAuditLog />);

  expect(await screen.findByText("Actor 24 · recipe.updated recipe/recipe-24")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Load more audit events" }));

  expect(await screen.findByText("Actor 25 · recipe.updated recipe/recipe-25")).toBeTruthy();
  expect(requests).toEqual([
    "/api/admin/audit?limit=24&offset=0",
    "/api/admin/audit?limit=24&offset=24",
  ]);
  expect(screen.queryByRole("button", { name: "Load more audit events" })).toBeNull();
});

it("preserves empty, error, and denied audit states", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response([]))));
  const empty = render(<AdminAuditLog />);
  expect(await screen.findByText("No audit events have been recorded yet.")).toBeTruthy();
  empty.unmount();

  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({}, 500))));
  const failed = render(<AdminAuditLog />);
  expect((await screen.findByRole("alert")).textContent).toContain("Could not load the security audit log.");
  failed.unmount();

  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({}, 403))));
  render(<AdminAuditLog />);
  await waitFor(() => expect(screen.queryByRole("heading", { name: "Audit log" })).toBeNull());
});
