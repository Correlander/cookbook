import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import AuthConfirmRoot from "../../app/AuthConfirmRoot.tsx";
import { AppSessionProvider, useAppSession } from "../../app/AppSession.tsx";
import AuthEntry from "./AuthEntry.tsx";

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function config(passkey: boolean) {
  return {
    auth_methods: { email_link: true, passkey },
    registration_policy: "invite_only",
    public_instance: false,
    courses_enabled: false,
  };
}

afterEach(() => {
  cleanup();
  window.history.replaceState({}, "", "/");
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("honors confirmation intent over invitation/sign-in and keeps the token until explicit Continue", async () => {
  const consume = deferred<Response>();
  const navigateHome = vi.fn();
  let sessionChecks = 0;
  const fetchMock = vi.fn<
    (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>
  >((input) => {
    const url = String(input);
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(
        sessionChecks === 1
          ? response({}, 401)
          : response({
              id: "user-1",
              email: "cook@example.test",
              display_name: "Cook",
              is_admin: false,
            }),
      );
    }
    if (url === "/api/public/config")
      return Promise.resolve(response(config(true)));
    if (url === "/api/auth/consume") return consume.promise;
    if (url === "/api/auth/passkeys") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState(
    {},
    "",
    "/auth/confirm?token=one-time-token&invite=invite-token&signin=1",
  );

  render(<AuthConfirmRoot onNavigateHome={navigateHome} />);

  const continueButton = await screen.findByRole("button", {
    name: "Continue to Cookbook",
  });
  expect(window.location.search).toContain("token=one-time-token");
  expect(
    fetchMock.mock.calls.some(
      ([input]) => String(input) === "/api/auth/consume",
    ),
  ).toBe(false);
  expect(
    fetchMock.mock.calls.some(([input]) =>
      /\/api\/(recipes|cookbooks|courses)/.test(String(input)),
    ),
  ).toBe(false);
  fireEvent.click(continueButton);
  fireEvent.click(continueButton);
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.filter(
        ([input]) => String(input) === "/api/auth/consume",
      ),
    ).toHaveLength(1),
  );
  expect((continueButton as HTMLButtonElement).disabled).toBe(true);
  expect(window.location.search).toContain("token=one-time-token");
  expect(
    JSON.parse(
      String(
        fetchMock.mock.calls.find(
          ([input]) => String(input) === "/api/auth/consume",
        )?.[1]?.body,
      ),
    ),
  ).toEqual({ token: "one-time-token" });

  await act(async () => {
    consume.resolve(response({}));
    await consume.promise;
  });

  expect(
    await screen.findByRole("heading", { name: "You’re signed in." }),
  ).toBeTruthy();
  expect(window.location.pathname).toBe("/auth/confirm");
  expect(window.location.search).toBe("");
  expect(navigateHome).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Continue to Home" }));
  expect(navigateHome).toHaveBeenCalledTimes(1);
});

it("keeps a Home escape available while optional auth config is still pending, then offers passkey setup", async () => {
  const authConfig = deferred<Response>();
  const navigateHome = vi.fn();
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/public/config") return authConfig.promise;
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(
        sessionChecks === 1
          ? response({}, 401)
          : response({
              id: "user-1",
              email: "cook@example.test",
              display_name: "Cook",
              is_admin: false,
            }),
      );
    }
    if (url === "/api/auth/consume") return Promise.resolve(response({}));
    if (url === "/api/auth/passkeys") return Promise.resolve(response([]));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=one-time-token");

  render(<AuthConfirmRoot onNavigateHome={navigateHome} />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Continue to Cookbook" }),
  );

  expect(
    await screen.findByRole("heading", { name: "You’re signed in." }),
  ).toBeTruthy();
  expect(window.location.pathname).toBe("/auth/confirm");
  expect(window.location.search).toBe("");
  const continueHome = screen.getByRole("button", { name: "Continue to Home" });
  expect(continueHome).toBeTruthy();
  expect(
    screen.getByText("Checking whether passkey setup is available…"),
  ).toBeTruthy();
  fireEvent.click(continueHome);
  expect(navigateHome).toHaveBeenCalledTimes(1);
  await act(async () => {
    authConfig.resolve(response(config(true)));
    await authConfig.promise;
  });
  expect(
    await screen.findByRole("heading", { name: "Add a passkey?" }),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "Continue to Home" })).toBeTruthy();
});

it("does not offer first-passkey setup for a malformed successful passkey response", async () => {
  const navigateHome = vi.fn();
  let sessionChecks = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/public/config")
      return Promise.resolve(response(config(true)));
    if (url === "/api/auth/me") {
      sessionChecks += 1;
      return Promise.resolve(
        sessionChecks === 1
          ? response({}, 401)
          : response({
              id: "user-1",
              email: "cook@example.test",
              display_name: "Cook",
              is_admin: false,
            }),
      );
    }
    if (url === "/api/auth/consume") return Promise.resolve(response({}));
    if (url === "/api/auth/passkeys") return Promise.resolve(response({}));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=one-time-token");

  render(<AuthConfirmRoot onNavigateHome={navigateHome} />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Continue to Cookbook" }),
  );

  expect(
    await screen.findByRole("heading", { name: "You’re signed in." }),
  ).toBeTruthy();
  await waitFor(() => expect(navigateHome).toHaveBeenCalledTimes(1));
  expect(screen.queryByRole("heading", { name: "Add a passkey?" })).toBeNull();
  expect(screen.queryByLabelText("Name this passkey")).toBeNull();
});

it.each([
  { label: "external actor change", nextActor: "actor-b" },
  { label: "external sign-out", nextActor: null },
])(
  "ignores a pending consume completion after $label",
  async ({ nextActor }) => {
    const consume = deferred<Response>();
    const navigateHome = vi.fn();
    let sessionChecks = 0;
    const actorA = {
      id: "actor-a",
      email: "a@example.test",
      display_name: "Cook A",
      is_admin: false,
    };
    const actorB = {
      id: "actor-b",
      email: "b@example.test",
      display_name: "Cook B",
      is_admin: false,
    };
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/auth/me") {
        sessionChecks += 1;
        if (sessionChecks === 1) return Promise.resolve(response(actorA));
        return Promise.resolve(
          nextActor ? response(actorB) : response({}, 401),
        );
      }
      if (url === "/api/public/config")
        return Promise.resolve(response(config(true)));
      if (url === "/api/auth/consume") return consume.promise;
      if (url === "/api/auth/passkeys") return Promise.resolve(response([]));
      return Promise.resolve(response({}, 404));
    });
    vi.stubGlobal("fetch", fetchMock);
    window.history.replaceState({}, "", "/auth/confirm?token=pending-token");

    function SessionStatus() {
      const { state } = useAppSession();
      const actor = state.currentUser?.id ?? "none";
      return (
        <span data-testid="session-status">{`${state.status}:${actor}`}</span>
      );
    }

    function RefreshSessionButton() {
      const { refreshSession } = useAppSession();
      return (
        <button type="button" onClick={() => void refreshSession()}>
          Refresh session
        </button>
      );
    }

    render(
      <AppSessionProvider>
        <SessionStatus />
        <RefreshSessionButton />
        <AuthEntry mode="confirm" onContinueHome={navigateHome} />
      </AppSessionProvider>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("session-status").textContent).toBe(
        "authenticated:actor-a",
      ),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Continue to Cookbook" }),
    );
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(
          ([input]) => String(input) === "/api/auth/consume",
        ),
      ).toHaveLength(1),
    );

    fireEvent.click(screen.getByRole("button", { name: "Refresh session" }));
    const expectedSession = nextActor
      ? "authenticated:actor-b"
      : "anonymous:none";
    await waitFor(() =>
      expect(screen.getByTestId("session-status").textContent).toBe(
        expectedSession,
      ),
    );

    await act(async () => {
      consume.resolve(response({}));
      await consume.promise;
    });

    expect(window.location.pathname).toBe("/auth/confirm");
    expect(window.location.search).toBe("?token=pending-token");
    expect(
      screen.queryByRole("heading", { name: "You’re signed in." }),
    ).toBeNull();
    expect(navigateHome).not.toHaveBeenCalled();
    expect(
      fetchMock.mock.calls.some(
        ([input]) => String(input) === "/api/auth/passkeys",
      ),
    ).toBe(false);
  },
);

it("retains a replayed token and reports the explicit confirmation failure", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({}, 401));
    if (url === "/api/public/config")
      return Promise.resolve(response(config(false)));
    if (url === "/api/auth/consume")
      return Promise.resolve(response({ error: "invalid_or_used" }, 400));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=replayed-token");

  render(<AuthConfirmRoot />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Continue to Cookbook" }),
  );

  expect(
    await screen.findByText(/invalid, expired, or already used/i),
  ).toBeTruthy();
  expect(window.location.search).toBe("?token=replayed-token");
  expect(
    screen.getByRole("button", { name: "Continue to Cookbook" }),
  ).toBeTruthy();
});

it("keeps invitation intent ahead of sign-in intent on the confirmation route", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({}, 401));
    if (url === "/api/public/config")
      return Promise.resolve(response(config(false)));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState(
    {},
    "",
    "/auth/confirm?invite=invite-token&signin=1",
  );

  render(<AuthConfirmRoot />);

  expect(
    await screen.findByRole("heading", { name: "Accept your invitation" }),
  ).toBeTruthy();
  expect(
    fetchMock.mock.calls.some(
      ([input]) => String(input) === "/api/auth/consume",
    ),
  ).toBe(false);
  expect(window.location.search).toBe("?invite=invite-token&signin=1");
});

it("renders an explicit sign-in query without consuming a token", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({}, 401));
    if (url === "/api/public/config")
      return Promise.resolve(response(config(true)));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?signin=1");

  render(<AuthConfirmRoot />);

  expect(
    await screen.findByRole("heading", { name: "Sign in with email" }),
  ).toBeTruthy();
  expect(
    fetchMock.mock.calls.some(
      ([input]) => String(input) === "/api/auth/consume",
    ),
  ).toBe(false);
  expect(window.location.search).toBe("?signin=1");
});

it("keeps the confirmation token on a network failure", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({}, 401));
    if (url === "/api/public/config")
      return Promise.resolve(response(config(false)));
    if (url === "/api/auth/consume")
      return Promise.reject(new TypeError("offline"));
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/auth/confirm?token=network-token");

  render(<AuthConfirmRoot />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Continue to Cookbook" }),
  );

  expect(await screen.findByRole("status")).toHaveProperty(
    "textContent",
    "offline",
  );
  expect(window.location.search).toBe("?token=network-token");
});

it("keeps Home modal focus and invitation query behavior in the shared presentation", async () => {
  const fetchMock = vi.fn<
    (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>
  >((input) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(response({}, 401));
    if (url === "/api/public/config")
      return Promise.resolve(response(config(false)));
    if (url === "/api/auth/email")
      return Promise.resolve(
        response({ status: "if_account_can_sign_in_link_sent" }),
      );
    return Promise.resolve(response({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  window.history.replaceState({}, "", "/?invite=invitation-token");

  render(
    <AppSessionProvider>
      <AuthEntry mode="home" open pathname="/" onClose={() => undefined} />
    </AppSessionProvider>,
  );

  const dialog = await screen.findByRole("dialog", {
    name: "Accept your invitation",
  });
  expect(window.location.search).toBe("?invite=invitation-token");
  fireEvent.change(await screen.findByLabelText("Email address"), {
    target: { value: "cook@example.test" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Send sign-in link" }));
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.some(
        ([input]) => String(input) === "/api/auth/email",
      ),
    ).toBe(true),
  );
  const emailRequest = fetchMock.mock.calls.find(
    ([input]) => String(input) === "/api/auth/email",
  );
  expect(JSON.parse(String(emailRequest?.[1]?.body))).toEqual({
    email: "cook@example.test",
    invitation: "invitation-token",
  });
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  expect(document.activeElement).toBeTruthy();
});

it("traps Home auth focus and restores it when Escape closes the dialog", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/auth/me") return Promise.resolve(response({}, 401));
      if (url === "/api/public/config")
        return Promise.resolve(response(config(false)));
      return Promise.resolve(response({}, 404));
    }),
  );
  function HomeHarness() {
    const [open, setOpen] = useState(false);
    return (
      <AppSessionProvider>
        <button type="button" onClick={() => setOpen(true)}>
          Open sign in
        </button>
        <AuthEntry mode="home" open={open} onClose={() => setOpen(false)} />
      </AppSessionProvider>
    );
  }

  render(<HomeHarness />);
  const opener = screen.getByRole("button", { name: "Open sign in" });
  opener.focus();
  fireEvent.click(opener);
  const close = await screen.findByRole("button", { name: "Close sign in" });
  expect(document.activeElement).toBe(close);
  fireEvent.keyDown(document, { key: "Escape" });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(document.activeElement).toBe(opener);
});
