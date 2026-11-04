import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { InstanceAuthorityState } from "../auth/useInstanceAuthority.ts";
import FederationAdmin from "./FederationAdmin.tsx";

const ownerAuthority = {
  is_instance_owner: true,
  is_admin: false,
  owner_claimed: true,
  can_manage_instance_roles: true,
};

const adminAuthority = {
  is_instance_owner: false,
  is_admin: true,
  owner_claimed: true,
  can_manage_instance_roles: false,
};

const combinedAuthority = {
  is_instance_owner: true,
  is_admin: true,
  owner_claimed: true,
  can_manage_instance_roles: true,
};

const preclaimAdminAuthority = {
  is_instance_owner: false,
  is_admin: true,
  owner_claimed: false,
  can_manage_instance_roles: true,
};

function ready(userID: string, authority: typeof ownerAuthority | typeof adminAuthority | typeof combinedAuthority): InstanceAuthorityState {
  return { status: "ready", userID, authority };
}

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

const identity = {
  origin: "https://cookbook.example.test",
  key_id: "instance-key-1",
  public_key: "public-key-material",
};

function federationCalls(calls: Array<{ url: string; init?: RequestInit }>) {
  return calls.filter(({ url }) => url.startsWith("/api/admin/federation/"));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("lets a non-admin owner manage only the instance signing identity", async () => {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    return Promise.resolve(response({ error: "forbidden" }, 403));
  }));

  render(
    <FederationAdmin
      actorID="owner-1"
      authorityState={ready("owner-1", ownerAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );

  expect(await screen.findByText("instance-key-1")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Instance signing identity" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Stage signing-key rotation" })).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Trusted peers" })).toBeNull();
  expect(screen.queryByRole("heading", { name: "Outbox" })).toBeNull();
  expect(federationCalls(calls).map(({ url }) => url)).toEqual([
    "/api/admin/federation/identity",
  ]);
});

it("keeps admin peer and outbox tools when the admin cannot manage identity", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/admin/federation/peers") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    return Promise.resolve(response({ error: "forbidden" }, 403));
  }));

  render(
    <FederationAdmin
      actorID="admin-1"
      authorityState={ready("admin-1", adminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );

  expect(await screen.findByText("No peers have been configured.")).toBeTruthy();
  expect(await screen.findByText("No queued deliveries.")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Instance signing identity unavailable" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Approve and enable peer" })).toBeTruthy();
  expect(calls).toContain("/api/admin/federation/peers");
  expect(calls).toContain("/api/admin/federation/outbox");
  expect(calls).not.toContain("/api/admin/federation/identity");
});

it("isolates a denied peer section and waits for explicit retry after authority refresh", async () => {
  let peerReads = 0;
  const refreshAuthority = vi.fn();
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers") {
      peerReads += 1;
      return Promise.resolve(peerReads === 1
        ? response({ error: "forbidden" }, 403)
        : response([]));
    }
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  }));

  const firstAuthority = ready("owner-admin-1", combinedAuthority);
  const view = render(
    <FederationAdmin
      actorID="owner-admin-1"
      authorityState={firstAuthority}
      onRefreshAuthority={refreshAuthority}
    />,
  );

  expect(await screen.findByText("instance-key-1")).toBeTruthy();
  expect(await screen.findByText("No queued deliveries.")).toBeTruthy();
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Refresh permissions and retry peers" })).toBeTruthy();
  expect(refreshAuthority).toHaveBeenCalledTimes(1);
  expect(peerReads).toBe(1);

  fireEvent.click(screen.getByRole("button", { name: "Refresh permissions and retry peers" }));
  expect(peerReads).toBe(1);
  const refreshedAuthority = ready("owner-admin-1", { ...combinedAuthority });
  view.rerender(
    <FederationAdmin
      actorID="owner-admin-1"
      authorityState={refreshedAuthority}
      onRefreshAuthority={refreshAuthority}
    />,
  );

  expect(await screen.findByText("No peers have been configured.")).toBeTruthy();
  expect(screen.getByText("instance-key-1")).toBeTruthy();
  expect(peerReads).toBe(2);
  expect(calls.filter((url) => url === "/api/admin/federation/outbox")).toHaveLength(2);
});

it("lets a pre-claim administrator manage both identity and admin-only sections", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers" || url === "/api/admin/federation/outbox")
      return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  }));

  render(
    <FederationAdmin
      actorID="preclaim-admin"
      authorityState={ready("preclaim-admin", preclaimAdminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );

  expect(await screen.findByText("instance-key-1")).toBeTruthy();
  expect(await screen.findByText("No peers have been configured.")).toBeTruthy();
  expect(await screen.findByText("No queued deliveries.")).toBeTruthy();
  expect(calls).toContain("/api/admin/federation/identity");
  expect(calls).toContain("/api/admin/federation/peers");
  expect(calls).toContain("/api/admin/federation/outbox");
});

it("keeps admin sections when the capability-eligible identity endpoint is denied", async () => {
  const refreshAuthority = vi.fn();
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/admin/federation/identity")
      return Promise.resolve(response({ error: "forbidden" }, 403));
    if (url === "/api/admin/federation/peers") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  }));

  render(
    <FederationAdmin
      actorID="identity-denied-admin"
      authorityState={ready("identity-denied-admin", preclaimAdminAuthority)}
      onRefreshAuthority={refreshAuthority}
    />,
  );

  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(await screen.findByText("No peers have been configured.")).toBeTruthy();
  expect(await screen.findByText("No queued deliveries.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Refresh permissions and retry identity" })).toBeTruthy();
  expect(refreshAuthority).toHaveBeenCalledTimes(1);
});

it("preserves the disabled-peer, add-key, enable sequence", async () => {
  const calls: Array<{ url: string; method?: string; body?: string }> = [];
  const refreshAuthority = vi.fn();
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.startsWith("/api/admin/federation/"))
      calls.push({ url, method: init?.method, body: String(init?.body ?? "") });
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    return Promise.resolve(response({}));
  }));

  render(
    <FederationAdmin
      actorID="admin-owner-1"
      authorityState={ready("admin-owner-1", combinedAuthority)}
      onRefreshAuthority={refreshAuthority}
    />,
  );

  await screen.findByText("No peers have been configured.");
  fireEvent.change(screen.getByLabelText("Peer HTTPS origin"), {
    target: { value: "https://peer.example.test" },
  });
  fireEvent.change(screen.getByLabelText("Peer key ID"), { target: { value: "peer-key-1" } });
  fireEvent.change(screen.getByLabelText("Peer Ed25519 public key (base64)"), {
    target: { value: "cHVibGljLWtleQ==" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Approve and enable peer" }));

  expect(await screen.findByText("Peer approved and enabled.")).toBeTruthy();
  expect(calls.filter(({ method }) => method === "POST")).toEqual([
    {
      url: "/api/admin/federation/peers",
      method: "POST",
      body: JSON.stringify({ origin: "https://peer.example.test", enabled: false }),
    },
    {
      url: "/api/admin/federation/keys",
      method: "POST",
      body: JSON.stringify({ origin: "https://peer.example.test", key_id: "peer-key-1", public_key: "cHVibGljLWtleQ==" }),
    },
    {
      url: "/api/admin/federation/peers",
      method: "POST",
      body: JSON.stringify({ origin: "https://peer.example.test", enabled: true }),
    },
  ]);
  expect(refreshAuthority).toHaveBeenCalledTimes(1);
});

it("preserves stage and activate rotation payloads across an authority refresh", async () => {
  let staged = false;
  let activated = false;
  const calls: Array<{ url: string; method?: string; body?: string }> = [];
  const refreshAuthority = vi.fn();
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.startsWith("/api/admin/federation/"))
      calls.push({ url, method: init?.method, body: String(init?.body ?? "") });
    if (url === "/api/admin/federation/identity")
      return Promise.resolve(response(staged
        ? { ...identity, pending_key_id: "instance-key-2", pending_public_key: "pending-public-key" }
        : identity));
    if (url === "/api/admin/federation/identity/rotation" && init?.method === "POST") {
      const body = JSON.parse(String(init.body)) as { action: string };
      if (body.action === "stage") staged = true;
      if (body.action === "activate") activated = true;
      return Promise.resolve(response({}));
    }
    return Promise.resolve(response({}, 404));
  }));

  const firstAuthority = ready("rotation-owner", ownerAuthority);
  const view = render(
    <FederationAdmin
      actorID="rotation-owner"
      authorityState={firstAuthority}
      onRefreshAuthority={refreshAuthority}
    />,
  );
  await screen.findByRole("button", { name: "Stage signing-key rotation" });
  fireEvent.click(screen.getByRole("button", { name: "Stage signing-key rotation" }));
  expect(await screen.findByText(/A new signing key is staged/)).toBeTruthy();
  expect(calls).toContainEqual({
    url: "/api/admin/federation/identity/rotation",
    method: "POST",
    body: JSON.stringify({ action: "stage" }),
  });

  view.rerender(
    <FederationAdmin
      actorID="rotation-owner"
      authorityState={ready("rotation-owner", { ...ownerAuthority })}
      onRefreshAuthority={refreshAuthority}
    />,
  );
  expect(await screen.findByText("instance-key-2")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Activate staged key" }));
  expect(await screen.findByText(/Signing key activated/)).toBeTruthy();
  expect(calls).toContainEqual({
    url: "/api/admin/federation/identity/rotation",
    method: "POST",
    body: JSON.stringify({ action: "activate" }),
  });
  expect(activated).toBe(true);
  expect(refreshAuthority).toHaveBeenCalledTimes(2);
});

it("preserves peer key lifecycle, peer state changes, delivery view, retry and discard requests", async () => {
  const enabledPeer = {
    origin: "https://enabled.example.test",
    enabled: true,
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    keys: [{
      key_id: "active-key",
      public_key: "active-public-key",
      status: "active",
      created_at: "2026-11-01T10:00:00Z",
    }],
  };
  const disabledPeer = {
    ...enabledPeer,
    origin: "https://disabled.example.test",
    enabled: false,
    keys: [],
  };
  const delivery = {
    id: "delivery/one",
    peer_origin: enabledPeer.origin,
    event_id: "event-1",
    object_id: "recipe-1",
    sequence: 1,
    attempts: 1,
    next_attempt: 0,
    last_error: "Waiting for delivery.",
    created_at: "2026-11-03T10:00:00Z",
    payload: { type: "Update", id: "https://cookbook.example.test/objects/recipe-1" },
  };
  const calls: Array<{ url: string; method?: string; body?: string }> = [];
  const confirmMock = vi.fn(() => true);
  vi.stubGlobal("confirm", confirmMock);
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.startsWith("/api/admin/federation/"))
      calls.push({ url, method: init?.method, body: String(init?.body ?? "") });
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers") return Promise.resolve(response([enabledPeer, disabledPeer]));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([delivery]));
    if (url === "/api/admin/federation/outbox?id=delivery%2Fone")
      return Promise.resolve(response(delivery));
    return Promise.resolve(response({}));
  }));

  render(
    <FederationAdmin
      actorID="admin-lifecycle"
      authorityState={ready("admin-lifecycle", combinedAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  await screen.findByText("https://enabled.example.test · enabled");
  fireEvent.click(screen.getByText("https://enabled.example.test · enabled"));
  fireEvent.click(screen.getByRole("button", { name: "Revoke key" }));
  expect(await screen.findByText("Peer key revoked.")).toBeTruthy();
  fireEvent.change(screen.getAllByLabelText("Next key ID")[0], { target: { value: "next-key" } });
  fireEvent.change(screen.getAllByLabelText("Next Ed25519 public key (base64)")[0], {
    target: { value: "bmV4dC1wdWJsaWMta2V5" },
  });
  fireEvent.click(screen.getAllByRole("button", { name: "Add rotation key" })[0]);
  expect(await screen.findByText(/Peer key added/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Disable peer" }));
  expect(await screen.findByText("Peer disabled.")).toBeTruthy();
  fireEvent.click(screen.getByText("https://disabled.example.test · disabled"));
  fireEvent.click(screen.getByRole("button", { name: "Enable peer" }));
  expect(await screen.findByText("Peer enabled.")).toBeTruthy();

  fireEvent.click(screen.getByText(/https:\/\/enabled\.example\.test · sequence 1/));
  fireEvent.click(screen.getByRole("button", { name: "View event" }));
  expect(await screen.findByLabelText("Selected federation event")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("Delivery retry scheduled.")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Discard object events" }));
  expect(confirmMock).toHaveBeenCalledWith(
    "Discard every queued event for this recipe across all peers? A peer may keep the last copy it received.",
  );
  expect(await screen.findByText("Queued object events discarded.")).toBeTruthy();

  expect(calls).toContainEqual({
    url: "/api/admin/federation/keys",
    method: "DELETE",
    body: JSON.stringify({ origin: enabledPeer.origin, key_id: "active-key" }),
  });
  expect(calls).toContainEqual({
    url: "/api/admin/federation/keys",
    method: "POST",
    body: JSON.stringify({
      origin: enabledPeer.origin,
      key_id: "next-key",
      public_key: "bmV4dC1wdWJsaWMta2V5",
    }),
  });
  expect(calls).toContainEqual({
    url: "/api/admin/federation/peers",
    method: "DELETE",
    body: JSON.stringify({ origin: enabledPeer.origin }),
  });
  expect(calls).toContainEqual({
    url: "/api/admin/federation/peers",
    method: "POST",
    body: JSON.stringify({ origin: disabledPeer.origin, enabled: true }),
  });
  expect(calls).toContainEqual({
    url: "/api/admin/federation/outbox",
    method: "POST",
    body: JSON.stringify({ id: delivery.id }),
  });
  expect(calls).toContainEqual({
    url: "/api/admin/federation/outbox",
    method: "DELETE",
    body: JSON.stringify({ id: delivery.id }),
  });
});

it("refreshes the outbox list when a selected event has disappeared", async () => {
  const calls: string[] = [];
  const delivery = {
    id: "delivery-1",
    peer_origin: "https://peer.example.test",
    event_id: "event-1",
    object_id: "recipe-1",
    sequence: 1,
    attempts: 1,
    next_attempt: 0,
    last_error: "Waiting for delivery.",
    created_at: "2026-11-03T10:00:00Z",
  };
  let listReads = 0;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/outbox") {
      listReads += 1;
      return Promise.resolve(response(listReads === 1 ? [delivery] : []));
    }
    if (url === "/api/admin/federation/outbox?id=delivery-1")
      return Promise.resolve(response({ error: "not_found" }, 404));
    return Promise.resolve(response({}, 404));
  }));

  render(
    <FederationAdmin
      actorID="admin-owner-2"
      authorityState={ready("admin-owner-2", combinedAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );

  await screen.findByText("https://peer.example.test · sequence 1 · 1/12 attempts");
  fireEvent.click(screen.getByRole("button", { name: "View event" }));
  expect(await screen.findByText("This queued event no longer exists. The outbox was refreshed.")).toBeTruthy();
  expect(calls.filter((url) => url === "/api/admin/federation/outbox")).toHaveLength(2);
  expect(screen.queryByLabelText("Selected federation event")).toBeNull();
});

it("keeps identity visible and offers sign-in guidance when rotation needs recent authentication", async () => {
  const calls: Array<{ url: string; method?: string; body?: string }> = [];
  const refreshAuthority = vi.fn();
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, method: init?.method, body: String(init?.body ?? "") });
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/identity/rotation")
      return Promise.resolve(response({ error: "reauthentication_required" }, 403));
    return Promise.resolve(response({ error: "forbidden" }, 403));
  }));

  render(
    <FederationAdmin
      actorID="owner-recent-auth"
      authorityState={ready("owner-recent-auth", ownerAuthority)}
      onRefreshAuthority={refreshAuthority}
    />,
  );

  expect(await screen.findByText("instance-key-1")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Stage signing-key rotation" }));

  expect(await screen.findByText(/Sign in again; this action requires a recently issued session/)).toBeTruthy();
  expect(screen.getByRole("link", { name: "Sign in again" }).getAttribute("href")).toBe("/?signin=1");
  expect(screen.getByText("instance-key-1")).toBeTruthy();
  expect(refreshAuthority).not.toHaveBeenCalled();
  expect(calls).toContainEqual({
    url: "/api/admin/federation/identity/rotation",
    method: "POST",
    body: JSON.stringify({ action: "stage" }),
  });
});

it("saves an explicit peer profile with compare-and-set and separate owner permission copy", async () => {
  const peer = {
    origin: "https://profile-peer.example.test",
    enabled: false,
    exchange_profile: "v1",
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    keys: [],
  };
  let currentPeer = peer;
  const calls: Array<{ url: string; method?: string; body?: string }> = [];
  const confirmMock = vi.fn(() => true);
  vi.stubGlobal("confirm", confirmMock);
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.startsWith("/api/admin/federation/"))
      calls.push({ url, method: init?.method, body: String(init?.body ?? "") });
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers" && init?.method === "PATCH") {
      currentPeer = { ...peer, exchange_profile: "exchange-v2-retention" };
      return Promise.resolve(response(currentPeer));
    }
    if (url === "/api/admin/federation/peers") return Promise.resolve(response([currentPeer]));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  }));

  render(
    <FederationAdmin
      actorID="profile-admin"
      authorityState={ready("profile-admin", adminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  const profile = await screen.findByLabelText("Profile for https://profile-peer.example.test");
  fireEvent.change(profile, { target: { value: "exchange-v2-retention" } });
  fireEvent.click(screen.getByRole("button", { name: "Save exchange profile" }));
  expect(confirmMock).toHaveBeenCalledWith(expect.stringMatching(/protocol capability only.*recipe owner must separately permit personal retention/i));
  expect(await screen.findByText("Peer exchange profile saved.")).toBeTruthy();
  expect(calls).toContainEqual({
    url: "/api/admin/federation/peers",
    method: "PATCH",
    body: JSON.stringify({
      origin: peer.origin,
      expected_exchange_profile: "v1",
      exchange_profile: "exchange-v2-retention",
    }),
  });
  expect(calls.find(({ method }) => method === "PATCH")?.body).not.toMatch(/owner|grant|permission/i);
  expect(await screen.findByText("Current exchange profile: Exchange v2 with retention")).toBeTruthy();
});

it.each([true, false])("explains the downgrade effects before saving v1 for enabled=%s peer", async (enabled) => {
  const peer = {
    origin: "https://retained-peer.example.test",
    enabled,
    exchange_profile: "exchange-v2-retention",
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    keys: [],
  };
  const confirmMock = vi.fn(() => false);
  const calls: string[] = [];
  vi.stubGlobal("confirm", confirmMock);
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push(`${init?.method ?? "GET"} ${url}`);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers") return Promise.resolve(response([peer]));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  }));

  render(
    <FederationAdmin
      actorID="profile-admin"
      authorityState={ready("profile-admin", adminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  fireEvent.change(await screen.findByLabelText("Profile for https://retained-peer.example.test"), {
    target: { value: "v1" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save exchange profile" }));
  expect(confirmMock).toHaveBeenCalledWith(expect.stringMatching(
    enabled
      ? /New private snapshot capture eligibility.*queued v2 deliveries.*Private copies already retained remain.*in flight.*10 seconds.*peer is enabled.*queued for reconciliation as v1/i
      : /New private snapshot capture eligibility.*queued v2 deliveries.*Private copies already retained remain.*in flight.*10 seconds.*peer is disabled.*no reconciliation delivery will be queued now/i,
  ));
  expect(calls.some((call) => call.startsWith("PATCH "))).toBe(false);
});

it("does not offer a stale profile retry when conflict refresh fails", async () => {
  let peer = {
    origin: "https://conflict-refresh-peer.example.test",
    enabled: false,
    exchange_profile: "v1",
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    keys: [],
  };
  let getCount = 0;
  vi.stubGlobal("confirm", vi.fn(() => true));
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/peers" && init?.method === "PATCH") {
      peer = { ...peer, exchange_profile: "exchange-v2-retention" };
      return Promise.resolve(response({ error: "exchange_profile_conflict" }, 409));
    }
    if (url === "/api/admin/federation/peers") {
      getCount += 1;
      if (getCount === 2)
        return Promise.resolve(response({ error: "storage_error" }, 500));
      return Promise.resolve(response([peer]));
    }
    return Promise.resolve(response({}, 404));
  }));
  render(
    <FederationAdmin
      actorID="profile-admin"
      authorityState={ready("profile-admin", adminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  const profileLabel = "Profile for https://conflict-refresh-peer.example.test";
  fireEvent.change(await screen.findByLabelText(profileLabel), {
    target: { value: "exchange-v2-retention" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save exchange profile" }));

  expect((await screen.findByRole("alert")).textContent).toMatch(/Could not load trusted peers/i);
  expect(screen.queryByLabelText(profileLabel)).toBeNull();
  expect(screen.queryByText(/current peer list was refreshed/i)).toBeNull();
  expect(getCount).toBe(2);

  fireEvent.click(screen.getByRole("button", { name: "Retry peers" }));
  await waitFor(() => expect((screen.getByLabelText(profileLabel) as HTMLSelectElement).value).toBe("exchange-v2-retention"));
  expect(getCount).toBe(3);
});

it("reports an accepted profile change without a current projection until manual refresh succeeds", async () => {
  let peer = {
    origin: "https://post-save-refresh-peer.example.test",
    enabled: false,
    exchange_profile: "v1",
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    keys: [],
  };
  let getCount = 0;
  vi.stubGlobal("confirm", vi.fn(() => true));
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/peers" && init?.method === "PATCH") {
      peer = { ...peer, exchange_profile: "exchange-v2-retention" };
      return Promise.resolve(response(peer));
    }
    if (url === "/api/admin/federation/peers") {
      getCount += 1;
      if (getCount === 2) return Promise.resolve(response({ error: "storage_error" }, 500));
      return Promise.resolve(response([peer]));
    }
    return Promise.resolve(response({}, 404));
  }));
  render(
    <FederationAdmin
      actorID="profile-admin"
      authorityState={ready("profile-admin", adminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  const profileLabel = "Profile for https://post-save-refresh-peer.example.test";
  fireEvent.change(await screen.findByLabelText(profileLabel), {
    target: { value: "exchange-v2-retention" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save exchange profile" }));

  expect((await screen.findByRole("alert")).textContent).toMatch(/Could not load trusted peers/i);
  expect(await screen.findByText(/profile change was accepted, but the current peer list could not be loaded/i)).toBeTruthy();
  expect(screen.queryByLabelText(profileLabel)).toBeNull();
  expect(screen.queryByText("Peer exchange profile saved.")).toBeNull();
  expect(getCount).toBe(2);

  fireEvent.click(screen.getByRole("button", { name: "Retry peers" }));
  await screen.findByLabelText(profileLabel);
  expect((screen.getByLabelText(profileLabel) as HTMLSelectElement).value).toBe("exchange-v2-retention");
  expect(getCount).toBe(3);
  expect(screen.queryByText(/profile change was accepted, but the current peer list could not be loaded/i)).toBeNull();
});

it("shows old-server v1 metadata without mutation and fails closed for malformed profiles", async () => {
  const base = {
    origin: "https://profile-peer.example.test",
    enabled: false,
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    keys: [],
  };
  let peers: unknown[] = [base];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/peers") return Promise.resolve(response(peers));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  }));
  const view = render(
    <FederationAdmin
      actorID="profile-admin"
      authorityState={ready("profile-admin", adminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  expect(await screen.findByText(/v1 \(legacy server metadata\)/)).toBeTruthy();
  expect(screen.queryByLabelText("Profile for https://profile-peer.example.test")).toBeNull();
  peers = [{ ...base, exchange_profile: "future-profile" }];
  view.rerender(
    <FederationAdmin
      actorID="profile-admin"
      authorityState={ready("profile-admin", { ...adminAuthority })}
      onRefreshAuthority={vi.fn()}
    />,
  );
  expect((await screen.findByRole("alert")).textContent).toContain("Unrecognized exchange profile");
  expect(screen.queryByLabelText("Profile for https://profile-peer.example.test")).toBeNull();
});

it("reloads the current peer list after a compare conflict and requires a fresh profile choice", async () => {
  let peer = {
    origin: "https://conflict-peer.example.test",
    enabled: false,
    exchange_profile: "v1",
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    keys: [],
  };
  let getCount = 0;
  vi.stubGlobal("confirm", vi.fn(() => true));
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/federation/identity") return Promise.resolve(response(identity));
    if (url === "/api/admin/federation/outbox") return Promise.resolve(response([]));
    if (url === "/api/admin/federation/peers" && init?.method === "PATCH") {
      peer = { ...peer, exchange_profile: "exchange-v2-retention" };
      return Promise.resolve(response({ error: "exchange_profile_conflict" }, 409));
    }
    if (url === "/api/admin/federation/peers") {
      getCount += 1;
      return Promise.resolve(response([peer]));
    }
    return Promise.resolve(response({}, 404));
  }));
  render(
    <FederationAdmin
      actorID="profile-admin"
      authorityState={ready("profile-admin", adminAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  fireEvent.change(await screen.findByLabelText("Profile for https://conflict-peer.example.test"), {
    target: { value: "exchange-v2-retention" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Save exchange profile" }));
  expect(await screen.findByText(/The peer profile changed/)).toBeTruthy();
  await waitFor(() => expect(getCount).toBeGreaterThanOrEqual(2));
  expect((screen.getByLabelText("Profile for https://conflict-peer.example.test") as HTMLSelectElement).value).toBe("exchange-v2-retention");
  expect(screen.queryByText("Peer exchange profile saved.")).toBeNull();
});

it("does not render a previous actor identity after an actor switch", async () => {
  let finishIdentity: (value: Response) => void = () => undefined;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/admin/federation/identity")
      return new Promise<Response>((resolve) => { finishIdentity = resolve; });
    return Promise.resolve(response([], 200));
  }));

  const view = render(
    <FederationAdmin
      actorID="actor-one"
      authorityState={ready("actor-one", ownerAuthority)}
      onRefreshAuthority={vi.fn()}
    />,
  );
  view.rerender(
    <FederationAdmin
      actorID="actor-two"
      authorityState={{ status: "loading", userID: "actor-two" }}
      onRefreshAuthority={vi.fn()}
    />,
  );
  finishIdentity(response(identity));

  await waitFor(() => expect(screen.queryByText("instance-key-1")).toBeNull());
  expect(screen.queryByRole("heading", { name: "Instance signing identity" })).toBeNull();
});
