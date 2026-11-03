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
  expect(calls.some(({ url }) => url.startsWith("/api/admin/invitations"))).toBe(false);
  expect(calls.some(({ url }) => url.startsWith("/api/admin/audit"))).toBe(false);
  expect(screen.queryByRole("link", { name: "Instance" })).toBeNull();
  expect(calls.filter(({ url }) => url === "/api/instance/owner-transfers")
    .every(({ init }) => init?.cache === "no-store")).toBe(true);
});

it("loads administrator panels only after current authority confirms an administrator", async () => {
  const calls: string[] = [];
  const adminAuthority = {
    is_instance_owner: false,
    is_admin: true,
    owner_claimed: false,
    can_manage_instance_roles: true,
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me")
      return Promise.resolve(response({
        id: "admin-1",
        email: "admin@example.test",
        display_name: "Administrator",
        is_admin: true,
      }));
    if (url === "/api/instance/authority") return Promise.resolve(response(adminAuthority));
    if (url === "/api/instance/owner-transfers")
      return Promise.resolve(response({ transfers: [] }));
    if (url === "/api/admin/users?limit=100&offset=0") return Promise.resolve(response([]));
    if (url === "/api/admin/invitations")
      return Promise.resolve(response([{
        id: "invite-1",
        email: "invitee@example.test",
        expires_at: "2026-11-10T10:00:00Z",
      }]));
    if (url === "/api/admin/audit?limit=24&offset=0")
      return Promise.resolve(response([{
        id: "audit-1",
        actor_name: "Administrator",
        action: "role.changed",
        resource_type: "account",
        resource_id: "account-1",
        created_at: "2026-11-03T10:00:00Z",
        response_status: 200,
      }]));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByText("invitee@example.test")).toBeTruthy();
  expect(await screen.findByText("Administrator · role.changed account/account-1")).toBeTruthy();
  const authorityIndex = calls.indexOf("/api/instance/authority");
  expect(authorityIndex).toBeGreaterThanOrEqual(0);
  expect(calls.indexOf("/api/admin/invitations")).toBeGreaterThan(authorityIndex);
  expect(calls.indexOf("/api/admin/audit?limit=24&offset=0")).toBeGreaterThan(authorityIndex);
  expect(screen.getByRole("heading", { name: "Invitations" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Audit log" })).toBeTruthy();
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
  expect(calls.some((url) => url.startsWith("/api/admin/invitations"))).toBe(false);
  expect(calls.some((url) => url.startsWith("/api/admin/audit"))).toBe(false);
  expect(screen.queryByRole("button", { name: /Decline/ })).toBeNull();
});

it("accepts an exact recipient request without changing administrator status", async () => {
  let accepted = false;
  let sessions = 0;
  const sessionRefresh = { resolve: undefined as ((session: Response) => void) | undefined };
  const confirmMock = vi.fn(() => true);
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    if (url === "/api/auth/me") {
      sessions += 1;
      if (sessions > 1)
        return new Promise<Response>((resolve) => { sessionRefresh.resolve = resolve; });
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
  expect(await screen.findByText("Checking your session…")).toBeTruthy();
  const acceptanceNotice = await screen.findByText(/Ownership transfer accepted/);
  expect(acceptanceNotice.getAttribute("role")).toBe("status");
  expect(screen.queryByRole("button", { name: "Accept ownership" })).toBeNull();
  expect(confirmMock).toHaveBeenCalledWith(expect.stringContaining("will not make you an administrator"));
  expect(calls.some(({ url, init }) => url.endsWith("/accept") && init?.method === "POST")).toBe(true);
  expect(sessions).toBeGreaterThan(1);
  expect(sessionRefresh.resolve).toBeDefined();
  sessionRefresh.resolve?.(response({
    id: "recipient-1",
    email: "recipient@example.test",
    display_name: "Recipient",
    is_admin: false,
  }));
  await waitFor(() => expect(screen.getByText(/Ownership transfer accepted/)).toBeTruthy());
  expect(screen.queryByRole("button", { name: /Decline/ })).toBeNull();
});

it.each(["logout", "actor switch"] as const)(
  "clears the acceptance notice after a real %s",
  async (outcome) => {
    let accepted = false;
    let otherActor = false;
    let sessions = 0;
    const confirmMock = vi.fn(() => true);
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/auth/me") {
        sessions += 1;
        if (sessions === 1)
          return Promise.resolve(response({
            id: "recipient-1",
            email: "recipient@example.test",
            display_name: "Recipient",
            is_admin: false,
          }));
        if (outcome === "logout") return Promise.resolve(response({}, 401));
        otherActor = true;
        return Promise.resolve(response({
          id: "different-actor",
          email: "other@example.test",
          display_name: "Other actor",
          is_admin: false,
        }));
      }
      if (url === "/api/instance/authority")
        return Promise.resolve(response(
          otherActor ? ordinaryAuthority : accepted ? ownerAuthority : ordinaryAuthority,
        ));
      if (url === "/api/instance/owner-transfers")
        return Promise.resolve(response({ transfers: accepted ? [] : [{
          id: "transfer-logout",
          direction: "incoming",
          state: "pending",
          created_at: "2026-11-03T10:00:00Z",
          expires_at: "2026-11-10T10:00:00Z",
          counterparty_display_name: "Owner",
        }] }));
      if (url === "/api/instance/owner-transfers/transfer-logout/accept") {
        accepted = true;
        return Promise.resolve(response({ owner_claimed: true, generation: 2 }));
      }
      return Promise.resolve(response({}, 404));
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("confirm", confirmMock);

    render(<InstanceWorkbenchRoot />);
    fireEvent.click(await screen.findByRole("button", { name: "Accept ownership" }));

    if (outcome === "logout")
      expect(await screen.findByRole("heading", { name: "Sign in to view instance access" })).toBeTruthy();
    else
      expect(await screen.findByRole("heading", { name: "No instance actions are available" })).toBeTruthy();
    expect(screen.queryByText(/Ownership transfer accepted/)).toBeNull();
  },
);

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

it("shows the instance signing identity to a non-admin owner without admin federation requests", async () => {
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
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
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    if (url === "/api/admin/federation/identity")
      return Promise.resolve(response({
        origin: "https://cookbook.example.test",
        key_id: "owner-instance-key",
        public_key: "public-key",
      }));
    return Promise.resolve(response({ error: "forbidden" }, 403));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByText("owner-instance-key")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Instance signing identity" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Stage signing-key rotation" })).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Trusted peers" })).toBeNull();
  expect(screen.queryByRole("heading", { name: "Outbox" })).toBeNull();
  expect(calls).toContain("/api/admin/federation/identity");
  expect(calls.some((url) => url.includes("/api/admin/federation/peers"))).toBe(false);
  expect(calls.some((url) => url.includes("/api/admin/federation/outbox"))).toBe(false);
});

it("keeps admin federation sections visible when a non-owner admin cannot read instance identity", async () => {
  const calls: string[] = [];
  const adminAuthority = {
    is_instance_owner: false,
    is_admin: true,
    owner_claimed: true,
    can_manage_instance_roles: false,
  };
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me")
      return Promise.resolve(response({
        id: "admin-1",
        email: "admin@example.test",
        display_name: "Admin",
        is_admin: true,
      }));
    if (url === "/api/instance/authority") return Promise.resolve(response(adminAuthority));
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    if (url === "/api/admin/users?limit=100&offset=0") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/peers" || url === "/api/admin/federation/outbox")
      return Promise.resolve(response([]));
    return Promise.resolve(response({ error: "forbidden" }, 403));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByRole("heading", { name: "Instance signing identity unavailable" })).toBeTruthy();
  expect(await screen.findByText("No peers have been configured.")).toBeTruthy();
  expect(await screen.findByText("No queued deliveries.")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Accounts" })).toBeTruthy();
  expect(calls).not.toContain("/api/admin/federation/identity");
  expect(calls).toContain("/api/admin/federation/peers");
  expect(calls).toContain("/api/admin/federation/outbox");
});

it("clears the owner identity after authoritative permission revocation", async () => {
  let revoked = false;
  const calls: string[] = [];
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me")
      return Promise.resolve(response({
        id: "owner-1",
        email: "owner@example.test",
        display_name: "Owner",
        is_admin: false,
      }));
    if (url === "/api/instance/authority") {
      return Promise.resolve(response(revoked ? ordinaryAuthority : ownerAuthority));
    }
    if (url === "/api/instance/owner-transfers") return Promise.resolve(response({ transfers: [] }));
    if (url === "/api/admin/federation/identity")
      return Promise.resolve(response({ origin: "", key_id: "revoked-key", public_key: "secret-public-data" }));
    return Promise.resolve(response({ error: "forbidden" }, 403));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByText("revoked-key")).toBeTruthy();
  revoked = true;
  window.dispatchEvent(new Event("focus"));
  expect(await screen.findByRole("heading", { name: "No instance actions are available" })).toBeTruthy();
  expect(screen.queryByText("revoked-key")).toBeNull();
  expect(screen.queryByText("secret-public-data")).toBeNull();
  expect(calls.filter((url) => url === "/api/admin/federation/identity")).toHaveLength(1);
});

it("makes no federation calls when authority cannot be loaded", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/auth/me")
      return Promise.resolve(response({
        id: "admin-1",
        email: "admin@example.test",
        display_name: "Admin",
        is_admin: true,
      }));
    if (url === "/api/instance/authority") return Promise.resolve(response({}, 500));
    return Promise.resolve(response({}, 404));
  }));

  render(<InstanceWorkbenchRoot />);

  expect(await screen.findByRole("heading", { name: "Could not check instance permissions" })).toBeTruthy();
  expect(calls.some((url) => url.startsWith("/api/admin/federation/"))).toBe(false);
});
