import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AdminInvitations from "./AdminInvitations.tsx";

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("preserves invitation creation, acceptance URL, and encoded revocation", async () => {
  let items = [
    {
      id: "invite/one",
      email: "existing@example.test",
      expires_at: "2026-11-10T10:00:00Z",
    },
  ];
  let createBody: unknown;
  const requests: Array<{ url: string; method?: string }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method;
    requests.push({ url, method });
    if (url === "/api/admin/invitations" && method === "POST") {
      createBody = JSON.parse(String(init?.body));
      items = [...items, {
        id: "invite-two",
        email: "new@example.test",
        expires_at: "2026-11-10T10:00:00Z",
      }];
      return Promise.resolve(response({
        id: "invite-two",
        email: "new@example.test",
        token: "accept/token",
        expires_at: "2026-11-10T10:00:00Z",
      }));
    }
    if (url.startsWith("/api/admin/invitations?id=") && method === "DELETE") {
      items = items.filter((item) => item.id !== "invite/one");
      return Promise.resolve(response({}));
    }
    if (url === "/api/admin/invitations") return Promise.resolve(response(items));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminInvitations />);

  expect(await screen.findByText("existing@example.test")).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Invite an email address"), {
    target: { value: "new@example.test" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create invitation" }));

  expect(await screen.findByText("new@example.test")).toBeTruthy();
  expect(createBody).toEqual({ email: "new@example.test" });
  const link = screen.getByRole("link", { name: /accept%2Ftoken/ });
  expect(new URL(link.getAttribute("href") ?? "", window.location.origin).search)
    .toBe("?invite=accept%2Ftoken");
  await waitFor(() => expect(requests.filter(({ url, method }) =>
    url === "/api/admin/invitations" && method === undefined))
    .toHaveLength(2));

  fireEvent.click(screen.getAllByRole("button", { name: "Revoke" })[0]);

  await waitFor(() => expect(screen.queryByText("existing@example.test")).toBeNull());
  expect(requests).toContainEqual({
    url: "/api/admin/invitations?id=invite%2Fone",
    method: "DELETE",
  });
  expect(screen.getByText("new@example.test")).toBeTruthy();
});

it("retains invitation loading errors and hides the panel after a denied response", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({}, 500))));
  const failed = render(<AdminInvitations />);
  expect((await screen.findByRole("alert")).textContent).toContain("Could not load invitations.");
  failed.unmount();

  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({}, 403))));
  render(<AdminInvitations />);
  await waitFor(() => expect(screen.queryByRole("heading", { name: "Invitations" })).toBeNull());
});
