import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CookbookBookmarkAction from "./CookbookBookmarkAction.tsx";

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const actionProps = {
  cookbookID: "book-1",
  title: "Family table",
  currentUserID: "reader-1",
};

const savedBookmark = {
  id: "bookmark-1",
  target_kind: "local_cookbook",
  target: { local_cookbook_id: "book-1" },
  availability: "available",
  cookbook: {
    id: "book-1",
    title: "Family table",
    description: "",
    visibility: "private",
    owner_id: "owner-1",
  },
  created_at: "2026-10-01T00:00:00Z",
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("keeps anonymous detail on sign-in without requesting bookmark status", () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookBookmarkAction {...actionProps} currentUserID={null} />);

  expect(
    screen
      .getByRole("link", { name: "Sign in to save this cookbook" })
      .getAttribute("href"),
  ).toBe("/?signin=1");
  expect(fetchMock).not.toHaveBeenCalled();
});

it("saves after an unsaved status and only changes to Remove after the response", async () => {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    if (url.includes("/status?"))
      return Promise.resolve(jsonResponse({ saved: false }));
    if (url === "/api/library/cookbook-bookmarks" && init?.method === "POST")
      return Promise.resolve(jsonResponse(savedBookmark, 201));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookBookmarkAction {...actionProps} />);

  const save = await screen.findByRole("button", {
    name: "Save Family table to Library",
  });
  expect(save.hasAttribute("aria-pressed")).toBe(false);
  fireEvent.click(save);
  expect(
    await screen.findByRole("button", {
      name: "Remove Family table from Library",
    }),
  ).toBeTruthy();
  expect(screen.getByRole("status").textContent).toContain(
    "saved to your Library",
  );
  expect(
    calls
      .filter((call) => call.url.includes("/status?"))
      .map((call) => call.url),
  ).toEqual([
    "/api/library/cookbook-bookmarks/status?local_cookbook_id=book-1",
  ]);
  expect(calls.find((call) => call.init?.method === "POST")?.init?.body).toBe(
    JSON.stringify({ local_cookbook_id: "book-1" }),
  );
  expect(JSON.stringify(calls)).not.toContain("reader-1");
});

it("keeps a saved action pending until the owner-scoped remove succeeds", async () => {
  const deletion = deferred<Response>();
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/status?"))
      return Promise.resolve(
        jsonResponse({ saved: true, bookmark_id: "bookmark-1" }),
      );
    if (url.endsWith("/bookmark-1") && init?.method === "DELETE")
      return deletion.promise;
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookBookmarkAction {...actionProps} />);

  fireEvent.click(
    await screen.findByRole("button", {
      name: "Remove Family table from Library",
    }),
  );
  expect(
    screen.getByRole("button", { name: "Removing…" }).hasAttribute("disabled"),
  ).toBe(true);
  expect(
    screen.queryByRole("button", { name: "Save Family table to Library" }),
  ).toBeNull();
  await act(async () => {
    deletion.resolve(new Response(null, { status: 204 }));
    await deletion.promise;
  });
  expect(
    await screen.findByRole("button", { name: "Save Family table to Library" }),
  ).toBeTruthy();
});

it("keeps 404 status unknown and lets the user retry without inferring unsaved", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(jsonResponse({}, 404))
    .mockResolvedValueOnce(jsonResponse({ saved: false }));
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookBookmarkAction {...actionProps} />);

  expect((await screen.findByRole("alert")).textContent).toContain(
    "Could not check saved status.",
  );
  expect(
    screen.queryByRole("button", { name: "Remove Family table from Library" }),
  ).toBeNull();
  expect(
    screen.getByRole("button", { name: "Save Family table to Library" }),
  ).toBeTruthy();
  expect(
    screen
      .getByRole("button", { name: "Save Family table to Library" })
      .hasAttribute("aria-pressed"),
  ).toBe(false);

  fireEvent.click(
    screen.getByRole("button", { name: "Retry saved status check" }),
  );
  expect(
    await screen.findByRole("button", { name: "Save Family table to Library" }),
  ).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it("rechecks state after a failed write without replaying the mutation", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(jsonResponse({ saved: false }))
    .mockResolvedValueOnce(jsonResponse({ error: "storage_error" }, 500))
    .mockResolvedValueOnce(
      jsonResponse({ saved: true, bookmark_id: "bookmark-1" }),
    );
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookBookmarkAction {...actionProps} />);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Save Family table to Library",
    }),
  );

  expect(
    await screen.findByRole("button", {
      name: "Remove Family table from Library",
    }),
  ).toBeTruthy();
  expect(
    fetchMock.mock.calls.filter(([, init]) => init?.method === "POST"),
  ).toHaveLength(1);
  expect(
    fetchMock.mock.calls.filter(([input]) =>
      String(input).includes("/status?"),
    ),
  ).toHaveLength(2);
});

it("ignores a status response after a save has completed", async () => {
  const initialStatus = deferred<Response>();
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("/status?")) return initialStatus.promise;
    if (url === "/api/library/cookbook-bookmarks" && init?.method === "POST")
      return Promise.resolve(jsonResponse(savedBookmark, 201));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookBookmarkAction {...actionProps} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Save Family table to Library" }),
  );
  expect(
    await screen.findByRole("button", {
      name: "Remove Family table from Library",
    }),
  ).toBeTruthy();

  await act(async () => {
    initialStatus.resolve(jsonResponse({ saved: false }));
    await initialStatus.promise;
  });
  expect(
    screen.getByRole("button", {
      name: "Remove Family table from Library",
    }),
  ).toBeTruthy();
});

it.each([
  {
    label: "actor",
    userA: "reader-a",
    userB: "reader-b",
    bookA: "book-1",
    bookB: "book-1",
  },
  {
    label: "target",
    userA: "reader-a",
    userB: "reader-a",
    bookA: "book-a",
    bookB: "book-b",
  },
])(
  "does not flash stale status across a $label change",
  async ({ userA, userB, bookA, bookB }) => {
    const staleStatus = deferred<Response>();
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes(`local_cookbook_id=${bookA}`))
        return staleStatus.promise;
      if (url.includes(`local_cookbook_id=${bookB}`))
        return Promise.resolve(jsonResponse({ saved: false }));
      return Promise.resolve(jsonResponse({}, 404));
    });
    vi.stubGlobal("fetch", fetchMock);

    const view = render(
      <CookbookBookmarkAction
        cookbookID={bookA}
        title={bookA}
        currentUserID={userA}
      />,
    );
    view.rerender(
      <CookbookBookmarkAction
        cookbookID={bookB}
        title={bookB}
        currentUserID={userB}
      />,
    );
    expect(
      await screen.findByRole("button", { name: `Save ${bookB} to Library` }),
    ).toBeTruthy();

    await act(async () => {
      staleStatus.resolve(
        jsonResponse({ saved: true, bookmark_id: "stale-bookmark" }),
      );
      await staleStatus.promise;
    });
    expect(
      screen.queryByRole("button", { name: `Remove ${bookB} from Library` }),
    ).toBeNull();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  },
);
