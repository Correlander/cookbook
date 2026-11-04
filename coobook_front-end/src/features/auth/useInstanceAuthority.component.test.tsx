import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import useInstanceAuthority from "./useInstanceAuthority.ts";

const ownerAuthority = {
  is_instance_owner: true,
  is_admin: false,
  owner_claimed: true,
  can_manage_instance_roles: true,
  can_manage_instance_settings: true,
};

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

function Probe({
  userID,
  authenticated = true,
}: {
  userID: string | null;
  authenticated?: boolean;
}) {
  const { state, retry } = useInstanceAuthority(userID, authenticated);
  if (state.status === "idle") return <p>Not authenticated</p>;
  if (state.status === "loading")
    return <p role="status">Loading role permissions</p>;
  if (state.status === "error")
    return (
      <>
        <p role="alert">{state.message}</p>
        <button onClick={retry}>Retry role permissions</button>
      </>
    );
  return (
    <>
      <p>
        {state.authority.can_manage_instance_roles
          ? "Can manage roles"
          : "Cannot manage roles"}
      </p>
      <p>
        {state.authority.can_manage_instance_settings === true
          ? "Can manage settings"
          : state.authority.can_manage_instance_settings === false
            ? "Cannot manage settings"
            : "Settings permission unavailable"}
      </p>
    </>
  );
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("loads the exact server authority after authentication and retries failures", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response({}, 503))
    .mockResolvedValueOnce(response(ownerAuthority));
  vi.stubGlobal("fetch", fetchMock);

  render(<Probe userID="owner" />);

  expect(await screen.findByRole("alert")).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Retry role permissions" }),
  );
  expect(await screen.findByText("Can manage roles")).toBeTruthy();
  expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/instance/authority", {
    cache: "no-store",
  });
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it("does not infer settings access from owner status when the server omits the capability", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response({
    is_instance_owner: true,
    is_admin: false,
    owner_claimed: true,
    can_manage_instance_roles: true,
  }));
  vi.stubGlobal("fetch", fetchMock);

  render(<Probe userID="owner" />);

  expect(await screen.findByText("Can manage roles")).toBeTruthy();
  expect(screen.getByText("Settings permission unavailable")).toBeTruthy();
});

it("does not fetch while unauthenticated and clears authority on logout", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response(ownerAuthority));
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<Probe userID={null} authenticated={false} />);
  expect(screen.getByText("Not authenticated")).toBeTruthy();
  expect(fetchMock).not.toHaveBeenCalled();

  view.rerender(<Probe userID="owner" />);
  expect(await screen.findByText("Can manage roles")).toBeTruthy();
  view.rerender(<Probe userID={null} authenticated={false} />);
  expect(screen.getByText("Not authenticated")).toBeTruthy();
});

it("ignores a late capability response after an account switch", async () => {
  const firstResponse = deferred<Response>();
  const fetchMock = vi
    .fn()
    .mockReturnValueOnce(firstResponse.promise)
    .mockResolvedValueOnce(
      response({
        is_instance_owner: false,
        is_admin: true,
        owner_claimed: true,
        can_manage_instance_roles: false,
        can_manage_instance_settings: false,
      }),
    );
  vi.stubGlobal("fetch", fetchMock);

  const view = render(<Probe userID="first" />);
  view.rerender(<Probe userID="second" />);
  expect(await screen.findByText("Cannot manage roles")).toBeTruthy();
  expect(screen.getByText("Cannot manage settings")).toBeTruthy();

  await act(async () => {
    firstResponse.resolve(response(ownerAuthority));
    await firstResponse.promise;
  });
  expect(screen.getByText("Cannot manage roles")).toBeTruthy();
  expect(screen.getByText("Cannot manage settings")).toBeTruthy();
  expect(screen.queryByText("Can manage roles")).toBeNull();
});
