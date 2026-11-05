import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import useRecipeFeed from "./useRecipeFeed.ts";

function Probe({ enabled }: { enabled: boolean }) {
  const feed = useRecipeFeed({ signedIn: false, query: "", enabled });
  return (
    <section>
      <p role="status">{feed.loading ? "Loading" : "Ready"}</p>
      <p>{feed.items.map((item) => item.title).join(", ")}</p>
      <button type="button" onClick={() => void feed.loadMore()}>
        Load more
      </button>
    </section>
  );
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("suppresses public recipe requests while disabled and clears stale results", async () => {
  const pending = deferred<Response>();
  const fetchMock = vi
    .fn()
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(jsonResponse([]));
  vi.stubGlobal("fetch", fetchMock);

  const view = render(<Probe enabled={false} />);
  expect(screen.getByRole("status").textContent).toBe("Ready");
  expect(fetchMock).not.toHaveBeenCalled();

  view.rerender(<Probe enabled />);
  expect(await screen.findByRole("status")).toHaveProperty("textContent", "Loading");
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/public/recipes?limit=24&offset=0");

  view.rerender(<Probe enabled={false} />);
  expect(screen.getByRole("status").textContent).toBe("Ready");
  await act(async () => pending.resolve(jsonResponse([{ id: "stale", title: "Stale" }])));
  expect(screen.queryByText("Stale")).toBeNull();
  expect(screen.getByRole("status").textContent).toBe("Ready");

  view.rerender(<Probe enabled />);
  await screen.findByRole("status");
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it("does not request more pages after the feed is disabled", async () => {
  const fetchMock = vi.fn(() => Promise.resolve(jsonResponse([])));
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<Probe enabled={false} />);
  view.rerender(<Probe enabled />);
  await screen.findByRole("status");
  view.rerender(<Probe enabled={false} />);
  screen.getByRole("button", { name: "Load more" }).click();
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
