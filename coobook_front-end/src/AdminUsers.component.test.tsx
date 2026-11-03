import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AdminUsers from "./AdminUsers.tsx";
import type { InstanceAuthorityState } from "./features/auth/useInstanceAuthority";

const account = {
  id: "member-1",
  email: "member@example.test",
  display_name: "Member",
  is_admin: false,
  created_at: "2026-01-01T00:00:00Z",
};

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

function renderUsers(
  authorityState: InstanceAuthorityState,
  options: {
    currentUserID?: string;
    onRetryAuthority?: () => void;
    onRoleChanged?: () => void;
  } = {},
) {
  return render(
    <AdminUsers
      currentUser={{
        id:
          options.currentUserID ??
          (authorityState.status === "idle" ? "owner-1" : authorityState.userID),
      }}
      authorityState={authorityState}
      onRetryAuthority={options.onRetryAuthority ?? vi.fn()}
      onRoleChanged={options.onRoleChanged ?? vi.fn()}
    />,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("shows role controls for a non-admin owner when the server grants the capability", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).startsWith("/api/admin/users?") && !init?.method)
        return Promise.resolve(response([account]));
      if (String(input) === "/api/admin/users/member-1")
        return Promise.resolve(response({ ...account, is_admin: true }));
      return Promise.resolve(response({}, 404));
    }),
  );
  const onRoleChanged = vi.fn();
  vi.stubGlobal(
    "confirm",
    vi.fn(() => true),
  );
  renderUsers(
    {
      status: "ready",
      userID: "owner-1",
      authority: {
        is_instance_owner: true,
        is_admin: false,
        owner_claimed: true,
        can_manage_instance_roles: true,
      },
    },
    { onRoleChanged },
  );

  fireEvent.click(
    await screen.findByRole("button", { name: "Make administrator" }),
  );
  await waitFor(() => expect(onRoleChanged).toHaveBeenCalledTimes(1));
});

it("hides controls for an administrator without owner role-management permission", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(response([account]))),
  );
  renderUsers({
    status: "ready",
    userID: "admin-1",
    authority: {
      is_instance_owner: false,
      is_admin: true,
      owner_claimed: true,
      can_manage_instance_roles: false,
    },
  });

  expect(await screen.findByText("Member")).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Make administrator" }),
  ).toBeNull();
  expect(
    screen.getByText(/this account cannot change roles/i),
  ).toBeTruthy();
});

it("does not use a ready capability state belonging to a different account", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(response([account]))),
  );
  renderUsers({
    status: "ready",
    userID: "previous-owner",
    authority: {
      is_instance_owner: true,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: true,
    },
  }, { currentUserID: "owner-1" });

  expect(await screen.findByText("Member")).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Make administrator" }),
  ).toBeNull();
});

it("clears the previous account list before loading a new user's authorization", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response([account]))
    .mockResolvedValueOnce(response({}, 403));
  vi.stubGlobal("fetch", fetchMock);
  const view = renderUsers({
    status: "ready",
    userID: "owner-1",
    authority: {
      is_instance_owner: true,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: true,
    },
  });

  expect(await screen.findByText(/member@example\.test/)).toBeTruthy();
  view.rerender(
    <AdminUsers
      currentUser={{ id: "regular-user" }}
      authorityState={{
        status: "ready",
        userID: "regular-user",
        authority: {
          is_instance_owner: false,
          is_admin: false,
          owner_claimed: true,
          can_manage_instance_roles: false,
        },
      }}
      onRetryAuthority={vi.fn()}
      onRoleChanged={vi.fn()}
    />,
  );

  expect(screen.queryByText("member@example.test")).toBeNull();
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Accounts" })).toBeNull(),
  );
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it("never renders prior-account rows during a user switch when the old list has an error", async () => {
  const firstPage = Array.from({ length: 100 }, (_, index) => ({
    ...account,
    id: `member-${index}`,
    email: `member${index}@example.test`,
  }));
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(firstPage))
    .mockResolvedValueOnce(response({}, 500))
    .mockResolvedValueOnce(response({}, 403));
  vi.stubGlobal("fetch", fetchMock);
  const view = renderUsers({
    status: "ready",
    userID: "owner-1",
    authority: {
      is_instance_owner: true,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: true,
    },
  });

  fireEvent.click(
    await screen.findByRole("button", { name: "Load more accounts" }),
  );
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByText(/member0@example\.test/)).toBeTruthy();

  view.rerender(
    <AdminUsers
      currentUser={{ id: "regular-user" }}
      authorityState={{
        status: "ready",
        userID: "regular-user",
        authority: {
          is_instance_owner: false,
          is_admin: false,
          owner_claimed: true,
          can_manage_instance_roles: false,
        },
      }}
      onRetryAuthority={vi.fn()}
      onRoleChanged={vi.fn()}
    />,
  );

  expect(screen.queryByText(/member0@example\.test/)).toBeNull();
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Accounts" })).toBeNull(),
  );
  expect(fetchMock).toHaveBeenCalledTimes(3);
});

it("keeps role controls hidden while authority is unresolved or failed and offers retry", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(response([account]))),
  );
  const onRetryAuthority = vi.fn();
  const view = renderUsers(
    { status: "loading", userID: "owner-1" },
    { onRetryAuthority },
  );
  expect(await screen.findByText("Member")).toBeTruthy();
  expect(screen.getByRole("status").textContent).toContain(
    "Checking role permissions",
  );
  expect(
    screen.queryByRole("button", { name: "Make administrator" }),
  ).toBeNull();

  view.rerender(
    <AdminUsers
      currentUser={{ id: "owner-1" }}
      authorityState={{
        status: "error",
        userID: "owner-1",
        message: "Could not load role permissions. Try again.",
      }}
      onRetryAuthority={onRetryAuthority}
      onRoleChanged={vi.fn()}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Retry role permissions" }),
  );
  expect(onRetryAuthority).toHaveBeenCalledTimes(1);
  expect(
    screen.queryByRole("button", { name: "Make administrator" }),
  ).toBeNull();
});

it("keeps controls hidden when the admin-user listing denies access", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(response({}, 403))),
  );
  renderUsers({
    status: "ready",
    userID: "member-1",
    authority: {
      is_instance_owner: false,
      is_admin: false,
      owner_claimed: true,
      can_manage_instance_roles: false,
    },
  });
  await waitFor(() =>
    expect(screen.queryByRole("heading", { name: "Accounts" })).toBeNull(),
  );
  expect(
    screen.queryByRole("button", { name: "Make administrator" }),
  ).toBeNull();
});
