import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import InstanceWorkbenchRoot from "./app/InstanceWorkbenchRoot.tsx";

const ordinaryAuthority = {
  is_instance_owner: false,
  is_admin: false,
  owner_claimed: true,
  can_manage_instance_roles: false,
};

const ownerAuthority = {
  is_instance_owner: true,
  is_admin: false,
  owner_claimed: true,
  can_manage_instance_roles: true,
};

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("does not request the account list for an ordinary account without an incoming transfer", async () => {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    if (url === "/api/auth/me")
      return Promise.resolve(response({
        id: "reader-1",
        email: "reader@example.test",
        display_name: "Reader",
        is_admin: false,
      }));
    if (url === "/api/instance/authority")
      return Promise.resolve(response(ordinaryAuthority));
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(response({ transfers: [] }));
    return Promise.resolve(response([], 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByRole("heading", { name: "No instance actions are available" })).toBeTruthy();
  expect(calls.some(({ url }) => url.startsWith("/api/admin/users?"))).toBe(false);
  expect(screen.queryByRole("link", { name: "Instance" })).toBeNull();
  expect(calls.filter(({ url }) => url === "/api/instance/owner-transfers")
    .every(({ init }) => init?.cache === "no-store")).toBe(true);
});

it("creates a named transfer from the existing paginated account row and refreshes current state", async () => {
  let pending = false;
  const postBodies: string[] = [];
  const calls: string[] = [];
  const confirmMock = vi.fn(() => true);
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me")
      return Promise.resolve(response({
        id: "owner-1",
        email: "owner@example.test",
        display_name: "Owner",
        is_admin: false,
      }));
    if (url === "/api/instance/authority") return Promise.resolve(response(ownerAuthority));
    if (url === "/api/instance/owner-transfers" && init?.method === "POST") {
      postBodies.push(String(init.body));
      pending = true;
      return Promise.resolve(response({}, 201));
    }
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(response({ transfers: pending ? [{
        id: "transfer-1",
        direction: "outgoing",
        state: "pending",
        created_at: "2026-11-03T10:00:00Z",
        expires_at: "2026-11-10T10:00:00Z",
        counterparty_display_name: "Member",
      }] : [] }));
    if (url === "/api/admin/users?limit=100&offset=0")
      return Promise.resolve(response([
        {
          id: "owner-1",
          email: "owner@example.test",
          display_name: "Owner",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        },
        {
          id: "member-1",
          email: "member@example.test",
          display_name: "Member",
          is_admin: false,
          created_at: "2026-01-01T00:00:00Z",
        },
      ]));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("confirm", confirmMock);

  render(<InstanceWorkbenchRoot />);

  fireEvent.click(await screen.findByRole("button", { name: "Transfer ownership to Member" }));
  await waitFor(() => expect(screen.getByText(/Ownership request created/)).toBeTruthy());
  expect(confirmMock).toHaveBeenCalledWith(expect.stringContaining("Member (member@example.test)"));
  expect(confirmMock).toHaveBeenCalledWith(expect.stringContaining("expires in seven days"));
  expect(postBodies).toEqual(['{"target_user_id":"member-1"}']);
  expect(screen.getByText(/Request sent to Member/)).toBeTruthy();
  expect(calls.filter((url) => url === "/api/admin/users?limit=100&offset=0")).toHaveLength(1);
  expect(screen.queryByRole("button", { name: /Decline/ })).toBeNull();
});

it("accepts an exact recipient request without changing administrator status", async () => {
  let accepted = false;
  let sessions = 0;
  const confirmMock = vi.fn(() => true);
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    if (url === "/api/auth/me") {
      sessions += 1;
      return Promise.resolve(response({
        id: "recipient-1",
        email: "recipient@example.test",
        display_name: "Recipient",
        is_admin: false,
      }));
    }
    if (url === "/api/instance/authority")
      return Promise.resolve(response(accepted ? ownerAuthority : ordinaryAuthority));
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(response({ transfers: accepted ? [] : [{
        id: "transfer-2",
        direction: "incoming",
        state: "pending",
        created_at: "2026-11-03T10:00:00Z",
        expires_at: "2026-11-10T10:00:00Z",
        counterparty_display_name: "Owner",
      }] }));
    if (url === "/api/instance/owner-transfers/transfer-2/accept") {
      accepted = true;
      return Promise.resolve(response({ owner_claimed: true, generation: 2 }));
    }
    if (url.startsWith("/api/admin/users?")) return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("confirm", confirmMock);

  render(<InstanceWorkbenchRoot />);

  fireEvent.click(await screen.findByRole("button", { name: "Accept ownership" }));
  await waitFor(() => expect(screen.getByText(/Ownership transfer accepted/)).toBeTruthy());
  expect(confirmMock).toHaveBeenCalledWith(expect.stringContaining("will not make you an administrator"));
  expect(calls.some(({ url, init }) => url.endsWith("/accept") && init?.method === "POST")).toBe(true);
  expect(sessions).toBeGreaterThan(1);
  expect(screen.queryByRole("button", { name: /Decline/ })).toBeNull();
});

it("explains recent authentication and never automatically replays a failed transfer mutation", async () => {
  let postCount = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(response({
        id: "owner-1",
        email: "owner@example.test",
        display_name: "Owner",
        is_admin: false,
      }));
    if (url === "/api/instance/authority") return Promise.resolve(response(ownerAuthority));
    if (url === "/api/instance/owner-transfers" && init?.method === "POST") {
      postCount += 1;
      return Promise.resolve(response({ error: "reauthentication_required" }, 403));
    }
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    if (url === "/api/admin/users?limit=100&offset=0") return Promise.resolve(response([
      {
        id: "member-1",
        email: "member@example.test",
        display_name: "Member",
        is_admin: false,
        created_at: "2026-01-01T00:00:00Z",
      },
    ]));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("confirm", vi.fn(() => true));

  render(<InstanceWorkbenchRoot />);

  fireEvent.click(await screen.findByRole("button", { name: "Transfer ownership to Member" }));
  expect(await screen.findByText(/Sign out and sign back in/)).toBeTruthy();
  expect(await screen.findAllByRole("link", { name: "Instance" })).toHaveLength(2);
  expect(postCount).toBe(1);
});

it("checks state before offering a deliberate retry after an uncertain network outcome", async () => {
  let postCount = 0;
  let readCount = 0;
  let pending = false;
  const confirmMock = vi.fn(() => true);
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(response({ id: "owner-1", email: "owner@example.test", display_name: "Owner", is_admin: false }));
    if (url === "/api/instance/authority") return Promise.resolve(response(ownerAuthority));
    if (url === "/api/admin/users?limit=100&offset=0") return Promise.resolve(response([
      { id: "member-1", email: "member@example.test", display_name: "Member", is_admin: false, created_at: "2026-01-01T00:00:00Z" },
    ]));
    if (url === "/api/instance/owner-transfers" && init?.method === "POST") {
      postCount += 1;
      if (postCount === 1) throw new TypeError("connection lost");
      pending = true;
      return Promise.resolve(response({}, 201));
    }
    if (url === "/api/instance/owner-transfers") {
      readCount += 1;
      return Promise.resolve(response({ transfers: pending ? [{
        id: "transfer-uncertain",
        direction: "outgoing",
        state: "pending",
        created_at: "2026-11-03T10:00:00Z",
        expires_at: "2026-11-10T10:00:00Z",
        counterparty_display_name: "Member",
      }] : [] }));
    }
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("confirm", confirmMock);

  render(<InstanceWorkbenchRoot />);
  const targetAction = () => screen.getByRole("button", { name: "Transfer ownership to Member" });
  fireEvent.click(await screen.findByRole("button", { name: "Transfer ownership to Member" }));
  expect(await screen.findByText(/outcome could not be confirmed/i)).toBeTruthy();
  expect(postCount).toBe(1);
  expect(readCount).toBeGreaterThan(1);
  expect(confirmMock).toHaveBeenCalledTimes(1);

  fireEvent.click(targetAction());
  await waitFor(() => expect(screen.getByText(/Ownership request created/)).toBeTruthy());
  expect(postCount).toBe(2);
  expect(confirmMock).toHaveBeenCalledTimes(2);
});

it("refreshes an expired incoming request and explains the stale state", async () => {
  let currentTransfer: "incoming" | null = "incoming";
  let acceptCount = 0;
  const confirmMock = vi.fn(() => true);
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(response({ id: "recipient-1", email: "recipient@example.test", display_name: "Recipient", is_admin: false }));
    if (url === "/api/instance/authority") return Promise.resolve(response(ordinaryAuthority));
    if (url === "/api/instance/owner-transfers/transfer-expired/accept") {
      acceptCount += 1;
      currentTransfer = null;
      return Promise.resolve(response({ error: "owner_transfer_expired" }, 410));
    }
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(response({ transfers: currentTransfer ? [{
        id: "transfer-expired",
        direction: "incoming",
        state: "pending",
        created_at: "2026-11-03T10:00:00Z",
        expires_at: "2026-11-03T11:00:00Z",
        counterparty_display_name: "Owner",
      }] : [] }));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("confirm", confirmMock);

  render(<InstanceWorkbenchRoot />);
  fireEvent.click(await screen.findByRole("button", { name: "Accept ownership" }));

  expect(await screen.findByText(/request expired/i)).toBeTruthy();
  expect(acceptCount).toBe(1);
  expect(screen.queryByRole("button", { name: "Accept ownership" })).toBeNull();
  expect(confirmMock).toHaveBeenCalledWith(expect.stringContaining("Owner"));
});

it("confirms outgoing cancellation and reloads when the server reports a state conflict", async () => {
  let reads = 0;
  const confirmMock = vi.fn(() => true);
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/auth/me")
      return Promise.resolve(response({ id: "owner-1", email: "owner@example.test", display_name: "Owner", is_admin: false }));
    if (url === "/api/instance/authority") return Promise.resolve(response(ownerAuthority));
    if (url === "/api/instance/owner-transfers/transfer-3" && init?.method === "DELETE")
      return Promise.resolve(response({ error: "owner_state_conflict" }, 409));
    if (url === "/api/instance/owner-transfers") {
      reads += 1;
      return Promise.resolve(response({ transfers: [{
        id: "transfer-3",
        direction: "outgoing",
        state: "pending",
        created_at: "2026-11-03T10:00:00Z",
        expires_at: "2026-11-10T10:00:00Z",
        counterparty_display_name: "Recipient",
      }] }));
    }
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("confirm", confirmMock);

  render(<InstanceWorkbenchRoot />);
  fireEvent.click(await screen.findByRole("button", { name: "Cancel request" }));

  expect(await screen.findByText(/ownership changed while you were acting/i)).toBeTruthy();
  expect(confirmMock).toHaveBeenCalledWith(expect.stringContaining("Cancel the ownership request to Recipient"));
  expect(reads).toBeGreaterThan(2);
  expect(fetchMock).toHaveBeenCalledWith("/api/instance/owner-transfers/transfer-3", { method: "DELETE" });
});
