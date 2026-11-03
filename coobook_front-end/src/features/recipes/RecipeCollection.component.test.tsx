import { useState } from "react";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import RecipeCollection from "./RecipeCollection.tsx";
import useRecipeFeed from "./useRecipeFeed.ts";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function jsonResponse<T>(data: T): Response {
  return new Response(JSON.stringify(data), { status: 200 });
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

interface HarnessProps {
  initialSignedIn?: boolean;
  onOpenRecipe?: (recipe: { id: string; title: string }) => void;
}

function Harness({
  initialSignedIn = false,
  onOpenRecipe = () => {},
}: HarnessProps) {
  const [signedIn, setSignedIn] = useState(initialSignedIn);
  const [query, setQuery] = useState("");
  const feed = useRecipeFeed({ signedIn, query });
  return (
    <>
      <label>
        Signed in
        <input
          type="checkbox"
          checked={signedIn}
          onChange={(event) => setSignedIn(event.target.checked)}
        />
      </label>
      <label>
        Search recipes
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </label>
      <RecipeCollection
        signedIn={signedIn}
        query={query}
        feed={feed}
        onOpenRecipe={onOpenRecipe}
        onCreateRecipe={() => {}}
        renderRecipe={(recipe, onOpen) => (
          <button key={recipe.id} onClick={() => onOpen(recipe)}>
            {recipe.title}
          </button>
        )}
      />
    </>
  );
}

describe("RecipeCollection", () => {
  it("loads the public feed, switches to the signed-in endpoint, and resets on search", async () => {
    const requests: string[] = [];
    const onOpenRecipe = vi.fn();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        requests.push(url);
        return Promise.resolve(
          jsonResponse([{ id: requests.length, title: "Apple pie" }]),
        );
      }),
    );
    render(<Harness onOpenRecipe={onOpenRecipe} />);

    expect(screen.getByText("Loading recipes…")).toBeTruthy();
    const status = screen.getByRole("status");
    await waitFor(() => expect(status.textContent).toBe("1 recipe loaded."));
    expect(
      await screen.findByRole("button", { name: "Apple pie" }),
    ).toBeTruthy();
    expect(requests[0]).toBe("/api/public/recipes?limit=24&offset=0");
    fireEvent.click(screen.getByRole("button", { name: "Apple pie" }));
    expect(onOpenRecipe).toHaveBeenCalledWith({ id: 1, title: "Apple pie" });

    fireEvent.click(screen.getByRole("checkbox", { name: "Signed in" }));
    await waitFor(() =>
      expect(requests.at(-1)).toBe("/api/recipes?limit=24&offset=0"),
    );
    await screen.findAllByRole("button", { name: "Apple pie" });

    fireEvent.change(screen.getByRole("textbox", { name: "Search recipes" }), {
      target: { value: "  pie  " },
    });
    await waitFor(() =>
      expect(requests.at(-1)).toBe("/api/recipes?limit=24&offset=0&q=pie"),
    );
  });

  it("deduplicates later pages and ignores a page response after the search changes", async () => {
    const laterPage = deferred<Response>();
    const requests: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("q=next"))
          return Promise.resolve(
            jsonResponse([{ id: 50, title: "New search result" }]),
          );
        if (url.endsWith("offset=0")) {
          return Promise.resolve(
            jsonResponse(
              Array.from({ length: 24 }, (_, index) => ({
                id: index + 1,
                title: `Recipe ${index + 1}`,
              })),
            ),
          );
        }
        if (url.endsWith("offset=24")) return laterPage.promise;
        return Promise.resolve(jsonResponse([]));
      }),
    );
    render(<Harness />);
    await screen.findByRole("button", { name: "Recipe 1" });
    fireEvent.click(screen.getByRole("button", { name: "Load more recipes" }));
    await waitFor(() =>
      expect(requests).toContain("/api/public/recipes?limit=24&offset=24"),
    );

    fireEvent.change(screen.getByRole("textbox", { name: "Search recipes" }), {
      target: { value: "next" },
    });
    expect(
      await screen.findByRole("button", { name: "New search result" }),
    ).toBeTruthy();
    laterPage.resolve(
      jsonResponse([
        { id: 24, title: "Recipe 24 duplicate" },
        { id: 25, title: "Stale recipe" },
      ]),
    );
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Stale recipe" })).toBeNull(),
    );
    expect(
      screen.queryByRole("button", { name: "Recipe 24 duplicate" }),
    ).toBeNull();
    expect(
      screen.getByRole("button", { name: "New search result" }),
    ).toBeTruthy();
  });

  it("ignores a stale pagination rejection without clearing the new request loading state", async () => {
    const oldPage = deferred<Response>();
    const newPage = deferred<Response>();
    let defaultFeedLoads = 0;
    let paginationLoads = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("q=new") && url.includes("offset=0")) {
          return Promise.resolve(
            jsonResponse(
              Array.from({ length: 24 }, (_, index) => ({
                id: index + 101,
                title: `New recipe ${index + 1}`,
              })),
            ),
          );
        }
        if (url.endsWith("offset=0")) {
          defaultFeedLoads += 1;
          const titlePrefix =
            defaultFeedLoads === 1 ? "Recipe" : "Refreshed recipe";
          const firstID = defaultFeedLoads === 1 ? 1 : 201;
          return Promise.resolve(
            jsonResponse(
              Array.from({ length: 24 }, (_, index) => ({
                id: firstID + index,
                title: `${titlePrefix} ${index + 1}`,
              })),
            ),
          );
        }
        paginationLoads += 1;
        return paginationLoads === 1 ? oldPage.promise : newPage.promise;
      }),
    );
    render(<Harness />);
    await screen.findByRole("button", { name: "Recipe 1" });
    fireEvent.click(screen.getByRole("button", { name: "Load more recipes" }));

    fireEvent.change(screen.getByRole("textbox", { name: "Search recipes" }), {
      target: { value: "new" },
    });
    await screen.findByRole("button", { name: "New recipe 1" });

    fireEvent.change(screen.getByRole("textbox", { name: "Search recipes" }), {
      target: { value: "" },
    });
    await screen.findByRole("button", { name: "Refreshed recipe 1" });
    expect(
      (
        screen.getByRole("button", {
          name: "Load more recipes",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Load more recipes" }));
    expect(
      (screen.getByRole("button", { name: "Loading…" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    await act(async () => {
      oldPage.reject(new Error("Stale network failure"));
      await oldPage.promise.catch(() => {});
    });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(
      (screen.getByRole("button", { name: "Loading…" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);

    newPage.resolve(
      jsonResponse(
        Array.from({ length: 24 }, (_, index) => ({
          id: index + 225,
          title: `Refreshed recipe ${index + 25}`,
        })),
      ),
    );
    expect(
      await screen.findByRole("button", { name: "Refreshed recipe 25" }),
    ).toBeTruthy();
    await waitFor(() =>
      expect(
        (
          screen.getByRole("button", {
            name: "Load more recipes",
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(false),
    );
  });

  it("appends new page results while keeping duplicate recipe IDs once", async () => {
    const requests: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        requests.push(url);
        if (url.endsWith("offset=0")) {
          return Promise.resolve(
            jsonResponse(
              Array.from({ length: 24 }, (_, index) => ({
                id: index + 1,
                title: `Recipe ${index + 1}`,
              })),
            ),
          );
        }
        return Promise.resolve(
          jsonResponse([
            { id: 24, title: "Duplicate recipe" },
            { id: 25, title: "Recipe 25" },
          ]),
        );
      }),
    );
    render(<Harness />);
    await screen.findByRole("button", { name: "Recipe 1" });
    fireEvent.click(screen.getByRole("button", { name: "Load more recipes" }));
    expect(
      await screen.findByRole("button", { name: "Recipe 25" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("button", { name: "Duplicate recipe" }),
    ).toBeNull();
    expect(requests.at(-1)).toBe("/api/public/recipes?limit=24&offset=24");
  });

  it("renders an initial failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
    render(<Harness />);
    const status = screen.getByRole("status");
    await waitFor(() =>
      expect(status.textContent).toBe(
        "Recipes are unavailable right now. Please try again later.",
      ),
    );
  });

  it("reports a load-more failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          jsonResponse(
            Array.from({ length: 24 }, (_, index) => ({
              id: index + 1,
              title: `Recipe ${index + 1}`,
            })),
          ),
        )
        .mockResolvedValueOnce({ ok: false }),
    );
    render(<Harness />);
    await screen.findByRole("button", { name: "Recipe 1" });
    fireEvent.click(screen.getByRole("button", { name: "Load more recipes" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "Could not load more recipes.",
    );
  });

  it("renders the empty search state", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse([])));
    const { container } = render(<Harness />);
    const status = screen.getByRole("status");
    await waitFor(() =>
      expect(status.textContent).toBe("No recipes are available."),
    );
    expect(
      screen.getByText("No recipes match “”. Try another search."),
    ).toBeTruthy();
    expect(container.querySelectorAll('[role="status"]')).toHaveLength(1);
  });

  it("keeps one empty polite status region and announces a slow load then results", async () => {
    const result = deferred<Response>();
    vi.stubGlobal("fetch", vi.fn(() => result.promise));
    const { container } = render(<Harness />);
    const status = screen.getByRole("status");

    expect(status.textContent).toBe("");
    expect(container.querySelectorAll('[role="status"]')).toHaveLength(1);
    await waitFor(() => expect(status.textContent).toBe("Loading recipes…"));

    result.resolve(jsonResponse([{ id: 1, title: "Apple pie" }]));
    await waitFor(() => expect(status.textContent).toBe("1 recipe loaded."));
  });

  it("cancels delayed announcements from rapid edits and reports only the settled query", async () => {
    const oldQuery = deferred<Response>();
    const requests: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("q=old")) return oldQuery.promise;
        if (url.includes("q=new"))
          return Promise.resolve(jsonResponse([{ id: 2, title: "New recipe" }]));
        if (url.endsWith("offset=0"))
          return Promise.resolve(jsonResponse([{ id: 1, title: "Initial recipe" }]));
        return Promise.resolve(jsonResponse([]));
      }),
    );
    render(<Harness />);
    const status = screen.getByRole("status");
    await waitFor(() => expect(status.textContent).toBe("1 recipe loaded."));
    const search = screen.getByRole("textbox", { name: "Search recipes" });

    fireEvent.change(search, { target: { value: "o" } });
    fireEvent.change(search, { target: { value: "ol" } });
    fireEvent.change(search, { target: { value: "old" } });
    await waitFor(() =>
      expect(requests).toContain("/api/public/recipes?limit=24&offset=0&q=old"),
    );
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(status.textContent).toBe("1 recipe loaded.");

    fireEvent.change(search, { target: { value: "new" } });
    await screen.findByRole("button", { name: "New recipe" });
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(status.textContent).toBe("1 recipe loaded.");
    await waitFor(() =>
      expect(status.textContent).toBe("1 recipe found for new."),
    );
    expect(status.textContent).not.toContain("old");

    await act(async () => {
      oldQuery.resolve(jsonResponse([{ id: 3, title: "Old recipe" }]));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(status.textContent).toBe("1 recipe found for new.");
    expect(screen.queryByRole("button", { name: "Old recipe" })).toBeNull();
  });
});
