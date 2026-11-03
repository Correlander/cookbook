import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
  AppSessionProvider,
  useAppSession,
} from "./AppSession.tsx";

function Probe() {
  const session = useAppSession();
  return (
    <div>
      <p role="status">{session.status}</p>
      {session.currentUser && <p>{session.currentUser.email}</p>}
      {session.errorMessage && <p role="alert">{session.errorMessage}</p>}
      <button onClick={() => void session.refreshSession()}>Retry</button>
      <button onClick={session.clearSession}>Clear session</button>
    </div>
  );
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function renderProbe() {
  return render(
    <AppSessionProvider>
      <Probe />
    </AppSessionProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("accepts the exact authenticated-user response without an admin-list timestamp", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() =>
      Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "cook@example.test",
          display_name: "Cook",
          is_admin: false,
        }),
      ),
    ),
  );

  renderProbe();

  expect(await screen.findByText("authenticated")).toBeTruthy();
  expect(screen.getByText("cook@example.test")).toBeTruthy();
  expect(screen.queryByText("error")).toBeNull();
});

it("treats only a 401 session response as anonymous", async () => {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResponse({}, 401))));

  renderProbe();

  expect(await screen.findByText("anonymous")).toBeTruthy();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("keeps server and network failures distinct from anonymous and retries", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(jsonResponse({}, 503))
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValueOnce(
      jsonResponse({
        id: "user-1",
        email: "cook@example.test",
        display_name: "Cook",
        is_admin: false,
      }),
    );
  vi.stubGlobal("fetch", fetchMock);

  renderProbe();

  expect((await screen.findByRole("alert")).textContent).toContain(
    "Could not check your session. Try again.",
  );
  expect(screen.getByText("error")).toBeTruthy();
  expect(screen.queryByText("anonymous")).toBeNull();

  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Could not check your session. Try again.",
  );
  expect(screen.queryByText("anonymous")).toBeNull();

  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("authenticated")).toBeTruthy();
  expect(screen.getByText("cook@example.test")).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(3);
});

it("retains a previously confirmed user when a later session refresh fails", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      jsonResponse({
        id: "user-1",
        email: "cook@example.test",
        display_name: "Cook",
        is_admin: false,
      }),
    )
    .mockResolvedValueOnce(jsonResponse({}, 500));
  vi.stubGlobal("fetch", fetchMock);

  renderProbe();
  expect(await screen.findByText("cook@example.test")).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.getByText("error")).toBeTruthy();
  expect(screen.getByText("cook@example.test")).toBeTruthy();
  expect(screen.queryByText("anonymous")).toBeNull();
});

it("clears a previously confirmed user when a later session check returns 401", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(
      jsonResponse({
        id: "user-1",
        email: "cook@example.test",
        display_name: "Cook",
        is_admin: false,
      }),
    )
    .mockResolvedValueOnce(jsonResponse({}, 401));
  vi.stubGlobal("fetch", fetchMock);

  renderProbe();
  expect(await screen.findByText("cook@example.test")).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("anonymous")).toBeTruthy();
  expect(screen.queryByText("cook@example.test")).toBeNull();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("does not let a stale refresh restore a user after explicit session clearing", async () => {
  const initialRequest = deferred<Response>();
  vi.stubGlobal("fetch", vi.fn(() => initialRequest.promise));

  renderProbe();
  fireEvent.click(screen.getByRole("button", { name: "Clear session" }));
  expect(screen.getByText("anonymous")).toBeTruthy();

  await act(async () => {
    initialRequest.resolve(
      jsonResponse({
        id: "stale-user",
        email: "stale@example.test",
        display_name: "Stale",
        is_admin: true,
      }),
    );
    await initialRequest.promise;
  });

  expect(screen.getByText("anonymous")).toBeTruthy();
  expect(screen.queryByText("stale@example.test")).toBeNull();
});

it("applies only the newest overlapping refresh response", async () => {
  const staleRequest = deferred<Response>();
  const currentRequest = deferred<Response>();
  let calls = 0;
  const fetchMock = vi.fn(() => {
    calls += 1;
    if (calls === 1)
      return Promise.resolve(
        jsonResponse({
          id: "user-1",
          email: "first@example.test",
          display_name: "First",
          is_admin: false,
        }),
      );
    return calls === 2 ? staleRequest.promise : currentRequest.promise;
  });
  vi.stubGlobal("fetch", fetchMock);

  renderProbe();
  expect(await screen.findByText("first@example.test")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));

  await act(async () => {
    currentRequest.resolve(
      jsonResponse({
        id: "user-2",
        email: "current@example.test",
        display_name: "Current",
        is_admin: false,
      }),
    );
    await currentRequest.promise;
  });
  expect(screen.getByText("current@example.test")).toBeTruthy();

  await act(async () => {
    staleRequest.resolve(
      jsonResponse({
        id: "stale-user",
        email: "stale@example.test",
        display_name: "Stale",
        is_admin: true,
      }),
    );
    await staleRequest.promise;
  });
  await waitFor(() => expect(screen.getByText("current@example.test")).toBeTruthy());
  expect(screen.queryByText("stale@example.test")).toBeNull();
});
