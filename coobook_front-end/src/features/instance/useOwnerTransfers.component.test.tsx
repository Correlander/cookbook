import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { readOwnerTransfers } from "./ownerTransferApi.ts";
import useOwnerTransfers from "./useOwnerTransfers.ts";

function response(transfers: unknown, status = 200): Response {
  return new Response(JSON.stringify({ transfers }), { status });
}

function Probe({ userID, authenticated }: { userID: string | null; authenticated: boolean }) {
  const { state, refresh } = useOwnerTransfers(userID, authenticated);
  return (
    <div>
      <span data-testid="state">{state.status}</span>
      {state.status === "ready" && state.transfers.map((item) => (
        <span key={item.id} data-testid="counterparty">{item.counterparty_display_name}</span>
      ))}
      <button type="button" onClick={() => void refresh()}>Refresh requests</button>
    </div>
  );
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("uses no-store reads and prevents a previous account transfer from rendering after an identity switch", async () => {
  const oldRead = deferred<Response>();
  let reads = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input) === "/api/instance/owner-transfers") {
      reads += 1;
      return reads === 1 ? oldRead.promise : Promise.resolve(response([]));
    }
    return Promise.resolve(response([]));
  });
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<Probe userID="owner-old" authenticated />);
  view.rerender(<Probe userID="reader-new" authenticated />);
  await screen.findByText("ready");

  await act(async () => {
    oldRead.resolve(response([{
      id: "transfer-secret-old",
      direction: "incoming",
      state: "pending",
      created_at: "2026-11-03T10:00:00Z",
      expires_at: "2026-11-10T10:00:00Z",
      counterparty_display_name: "Old Sender PII",
    }]));
    await oldRead.promise;
  });

  expect(screen.queryByText("Old Sender PII")).toBeNull();
  expect(document.body.textContent).not.toContain("transfer-secret-old");
  expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/instance/owner-transfers", { cache: "no-store" });
  expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/instance/owner-transfers", { cache: "no-store" });
});

it("keeps a failed discovery retryable and refreshes it on demand", async () => {
  let reads = 0;
  const fetchMock = vi.fn(() => {
    reads += 1;
    return Promise.resolve(reads === 1 ? response([], 503) : response([]));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<Probe userID="reader-1" authenticated />);

  await screen.findByText("error");
  expect(document.body.textContent).not.toContain("No instance");
  await act(async () => {
    screen.getByRole("button", { name: "Refresh requests" }).click();
  });
  await screen.findByText("ready");
  expect(reads).toBe(2);
});

it("clears transfer state while unauthenticated", () => {
  vi.stubGlobal("fetch", vi.fn());
  render(<Probe userID={null} authenticated={false} />);
  expect(screen.getByTestId("state").textContent).toBe("idle");
  expect(fetch).not.toHaveBeenCalled();
});

it("sanitizes undocumented PII from the transfer read and uses no-store", async () => {
  const fetchMock = vi.fn(() => Promise.resolve(response([{
    id: "opaque-transfer-id",
    direction: "incoming",
    state: "pending",
    created_at: "2026-11-03T10:00:00Z",
    expires_at: "2026-11-10T10:00:00Z",
    counterparty_display_name: "Owner",
    counterparty_email: "private@example.test",
    account_id: "private-account-id",
  }])));
  vi.stubGlobal("fetch", fetchMock);

  const transfers = await readOwnerTransfers();

  expect(transfers).toEqual([{
    id: "opaque-transfer-id",
    direction: "incoming",
    state: "pending",
    created_at: "2026-11-03T10:00:00Z",
    expires_at: "2026-11-10T10:00:00Z",
    counterparty_display_name: "Owner",
  }]);
  expect(fetchMock).toHaveBeenCalledWith("/api/instance/owner-transfers", { cache: "no-store" });
});
