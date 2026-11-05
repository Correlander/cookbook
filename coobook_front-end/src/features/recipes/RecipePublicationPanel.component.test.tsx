import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { RecipePublicationView } from "../../apiModels";
import RecipePublicationPanel from "./RecipePublicationPanel.tsx";
import {
  fetchRecipePublication,
  RecipePublicationApiError,
  updateRecipePublication,
} from "./recipePublicationApi.ts";

const etag = `"sha256:${"a".repeat(64)}"`;

function makeView(
  overrides: Partial<RecipePublicationView> = {},
): RecipePublicationView {
  return {
    recipe_id: "recipe/one",
    publication_state: "unpublished",
    federation_mode: "off",
    index_eligible: false,
    updated_by: "private-owner-id",
    updated_at: "2026-11-03T12:00:00.000000000Z",
    published_at: null,
    visibility: "public",
    public_instance: true,
    federation_enabled: true,
    federation_origin: "https://cookbook.example",
    ...overrides,
  };
}

function jsonResponse(
  body: unknown,
  responseEtag = etag,
  status = 200,
): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ETag: responseEtag },
  });
}

function errorResponse(status: number, code: string): Response {
  return new Response(JSON.stringify({ error: code }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function renderOwnerPanel(onClose = vi.fn(), recipeVersion = 7) {
  return render(
    <RecipePublicationPanel
      recipeId="recipe/one"
      recipeVersion={recipeVersion}
      isOwner
      isRemote={false}
      open
      onClose={onClose}
    />,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("recipePublicationApi", () => {
  it("loads the owner publication resource without caching and preserves its strong ETag", async () => {
    const view = makeView();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(view));
    vi.stubGlobal("fetch", fetchMock);

    const result = await fetchRecipePublication("recipe/one");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/recipes/recipe%2Fone/publication",
      expect.objectContaining({
        method: "GET",
        credentials: "same-origin",
        cache: "no-store",
      }),
    );
    expect(result).toEqual({ view, etag });
  });

  it("sends the exact If-Match value and always serializes index eligibility as false", async () => {
    const view = makeView({ publication_state: "published" });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(view));
    vi.stubGlobal("fetch", fetchMock);

    await updateRecipePublication(
      "recipe-1",
      {
        publication_state: "published",
        federation_mode: "followers",
        index_eligible: false,
      },
      etag,
    );

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.method).toBe("PUT");
    expect(init.credentials).toBe("same-origin");
    expect(init.cache).toBe("no-store");
    expect(new Headers(init.headers).get("If-Match")).toBe(etag);
    expect(JSON.parse(String(init.body))).toEqual({
      publication_state: "published",
      federation_mode: "followers",
      index_eligible: false,
    });
  });

  it("rejects weak or malformed entity tags and exposes HTTP errors", async () => {
    await expect(
      updateRecipePublication(
        "recipe-1",
        {
          publication_state: "published",
          federation_mode: "off",
          index_eligible: false,
        },
        `W/${etag}`,
      ),
    ).rejects.toMatchObject({ status: 0, code: "invalid_etag" });

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(412, "stale_precondition")));
    const staleRead = fetchRecipePublication("recipe-1");
    await expect(staleRead).rejects.toBeInstanceOf(RecipePublicationApiError);
    await expect(staleRead).rejects.toMatchObject({
      status: 412,
      code: "stale_precondition",
    });

    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(makeView(), `W/${etag}`)),
    );
    await expect(fetchRecipePublication("recipe-1")).rejects.toMatchObject({
      status: 200,
      code: "invalid_etag",
    });
  });
});

describe("RecipePublicationPanel", () => {
  it("clears recipe A values and the draft before recipe B fails to load", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(makeView({ recipe_id: "recipe-a" })))
      .mockResolvedValueOnce(errorResponse(500, "storage_error"));
    vi.stubGlobal("fetch", fetchMock);
    const onClose = vi.fn();
    const { rerender } = render(
      <RecipePublicationPanel
        recipeId="recipe-a"
        recipeVersion={1}
        isOwner
        isRemote={false}
        open
        onClose={onClose}
      />,
    );

    await screen.findByText("Not published yet");
    fireEvent.change(screen.getByLabelText("Public browsing status"), {
      target: { value: "published" },
    });
    rerender(
      <RecipePublicationPanel
        recipeId="recipe-b"
        recipeVersion={1}
        isOwner
        isRemote={false}
        open
        onClose={onClose}
      />,
    );

    await screen.findByRole("button", { name: "Reload settings" });
    expect(screen.queryByLabelText("Public browsing status")).toBeNull();
    expect(screen.queryByText("Not published yet")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Save publication settings" }),
    ).toBeNull();
    expect(fetchMock.mock.calls.map(([url]) => String(url))).toEqual([
      "/api/recipes/recipe-a/publication",
      "/api/recipes/recipe-b/publication",
    ]);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(0);
  });

  it("ignores an old recipe response that resolves after the panel switches resources", async () => {
    let resolveOld!: (response: Response) => void;
    const oldResponse = new Promise<Response>((resolve) => {
      resolveOld = resolve;
    });
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      void init;
      const url = String(input);
      if (url.includes("recipe-a")) return oldResponse;
      return Promise.resolve(
        jsonResponse(
          makeView({ recipe_id: "recipe-b", publication_state: "published", published_at: "B-only-date" }),
          `"sha256:${"b".repeat(64)}"`,
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    const onClose = vi.fn();
    const { rerender } = render(
      <RecipePublicationPanel
        recipeId="recipe-a"
        recipeVersion={1}
        isOwner
        isRemote={false}
        open
        onClose={onClose}
      />,
    );
    const [, oldRequest] = fetchMock.mock.calls[0];

    rerender(
      <RecipePublicationPanel
        recipeId="recipe-b"
        recipeVersion={1}
        isOwner
        isRemote={false}
        open
        onClose={onClose}
      />,
    );
    expect(oldRequest?.signal?.aborted).toBe(true);
    expect(await screen.findByText("B-only-date")).toBeTruthy();

    await act(async () => {
      resolveOld(
        jsonResponse(
          makeView({ recipe_id: "recipe-a", publication_state: "published", published_at: "A-only-date" }),
        ),
      );
      await oldResponse;
      await Promise.resolve();
    });

    expect(screen.getByText("B-only-date")).toBeTruthy();
    expect(screen.queryByText("A-only-date")).toBeNull();
    expect(screen.getByRole("status").textContent).toBe("Publication settings loaded.");
  });

  it("does not render or request policy for non-owners or remote recipes", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { rerender, container } = render(
      <RecipePublicationPanel
        recipeId="recipe-1"
        recipeVersion={1}
        isOwner={false}
        isRemote={false}
        open
        onClose={() => {}}
      />,
    );
    expect(container.innerHTML).toBe("");
    expect(fetchMock).not.toHaveBeenCalled();

    rerender(
      <RecipePublicationPanel
        recipeId="recipe-1"
        recipeVersion={1}
        isOwner
        isRemote
        open
        onClose={() => {}}
      />,
    );
    expect(container.innerHTML).toBe("");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows the current policy without exposing audit identity or offering index controls", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(makeView())));
    renderOwnerPanel();

    expect(await screen.findByText("Not published yet")).toBeTruthy();
    expect(screen.getByText("Index submission is not available.")).toBeTruthy();
    expect(screen.queryByLabelText(/index/i)).toBeNull();
    expect(document.body.textContent).not.toContain("private-owner-id");
    expect(screen.getByRole("status").textContent).toBe("Publication settings loaded.");
  });

  it.each([
    { name: "private recipe", view: makeView({ visibility: "private" }) },
    { name: "unpublished recipe", view: makeView() },
    {
      name: "disabled follower distribution",
      view: makeView({ publication_state: "published", federation_mode: "off" }),
    },
    {
      name: "federation disabled on the instance",
      view: makeView({ publication_state: "published", federation_mode: "followers", federation_enabled: false }),
    },
  ])("explains and disables retention permission for a $name", async ({ view }) => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(view));
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();

    expect(await screen.findByText(/At least one enabled Exchange v2 peer is also required/i)).toBeTruthy();
    expect((screen.getByLabelText(/I permit eligible connected peers/) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Queue permission for version 7" }) as HTMLButtonElement).disabled).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("federation-retention"))).toBe(false);
  });

  it("queues one explicit permission for the recipe version and reports only queued status", async () => {
    const eligibleView = makeView({
      publication_state: "published",
      federation_mode: "followers",
    });
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).includes("federation-retention"))
        return Promise.resolve(new Response(JSON.stringify({ status: "queued" }), { status: 202 }));
      void init;
      return Promise.resolve(jsonResponse(eligibleView));
    });
    const confirmMock = vi.fn(() => true);
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("confirm", confirmMock);
    renderOwnerPanel(vi.fn(), 7);

    fireEvent.click(await screen.findByLabelText(/I permit eligible connected peers/));
    const queueButton = screen.getByRole("button", { name: "Queue permission for version 7" });
    expect((queueButton as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(queueButton);

    expect(confirmMock).toHaveBeenCalledWith(expect.stringMatching(
      /version 7.*ordinary withdrawal.*personal-account migration.*public redistribution.*adaptation.*retain media.*verify authorship/i,
    ));
    expect(await screen.findByText(/Permission for version 7 was queued for eligible peers/)).toBeTruthy();
    expect(screen.getByText(/Permission for version 7 was queued for eligible peers/).textContent)
      .toMatch(/does not confirm receipt or personal capture/);
    expect(screen.queryByText(/received or retained this recipe/)).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/recipes/recipe%2Fone/federation-retention",
      expect.objectContaining({
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        body: JSON.stringify({ expected_recipe_version: 7 }),
      }),
    );
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  });

  it.each([
    [409, "federation_assertion_conflict", /eligibility changed.*Close and reopen.*enabled Exchange v2 peer/i],
    [403, "reauthentication_required", /Sign in again.*recently issued session/i],
    [503, "federation_queue_unavailable", /queue is unavailable.*not queued/i],
    [400, "invalid_recipe_version", /version is invalid.*Close and reopen this recipe/i],
    [404, "not_found", /server does not support personal retention/i],
    [500, "storage_error", /storage error/i],
    [502, "gateway_error", /could not be confirmed.*may have been queued/i],
  ])("explains retention request failure %s %s", async (status, code, message) => {
    const eligibleView = makeView({
      publication_state: "published",
      federation_mode: "followers",
    });
    const fetchMock = vi.fn((input: RequestInfo | URL) =>
      String(input).includes("federation-retention")
        ? Promise.resolve(errorResponse(status, code))
        : Promise.resolve(jsonResponse(eligibleView)),
    );
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("confirm", vi.fn(() => true));
    renderOwnerPanel();
    fireEvent.click(await screen.findByLabelText(/I permit eligible connected peers/));
    fireEvent.click(screen.getByRole("button", { name: "Queue permission for version 7" }));

    expect((await screen.findByRole("alert")).textContent).toMatch(message);
    expect(screen.queryByText(/was queued for eligible peers/)).toBeNull();
  });

  it("clears version consent when the dialog switches to a newer recipe version", async () => {
    const eligibleView = makeView({
      publication_state: "published",
      federation_mode: "followers",
    });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(eligibleView));
    vi.stubGlobal("fetch", fetchMock);
    const props = (recipeVersion: number) => (
      <RecipePublicationPanel
        recipeId="recipe/one"
        recipeVersion={recipeVersion}
        isOwner
        isRemote={false}
        open
        onClose={vi.fn()}
      />
    );
    const { rerender } = render(props(7));
    fireEvent.click(await screen.findByLabelText(/I permit eligible connected peers.*version 7/));
    rerender(props(8));

    expect((screen.getByLabelText(/I permit eligible connected peers.*version 8/) as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("button", { name: "Queue permission for version 8" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Queue permission for version 8" }));
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("federation-retention"))).toBe(false);
  });

  it("keeps publication and follower distribution separate and saves with a fresh ETag", async () => {
    const initial = makeView();
    const saved = makeView({
      publication_state: "published",
      published_at: "2026-11-03T12:01:00.000000000Z",
    });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(initial))
      .mockResolvedValueOnce(jsonResponse(saved, `"sha256:${"b".repeat(64)}"`));
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();

    await screen.findByText("Not published yet");
    fireEvent.change(screen.getByLabelText("Public browsing status"), {
      target: { value: "published" },
    });
    expect(screen.getByLabelText("Allow distribution to followers")).toBeTruthy();
    expect(
      (screen.getByRole("button", {
        name: "Save publication settings",
      }) as HTMLButtonElement).disabled,
    ).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Save publication settings" }));
    await screen.findByText("Publication settings saved.");

    const [, request] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(new Headers(request.headers).get("If-Match")).toBe(etag);
    expect(JSON.parse(String(request.body))).toEqual({
      publication_state: "published",
      federation_mode: "off",
      index_eligible: false,
    });
    expect(document.activeElement).toBe(
      screen.getByRole("heading", { name: "Publication settings" }),
    );
  });

  it("requires separate consent before enabling follower distribution", async () => {
    const initial = makeView({ publication_state: "published" });
    const saved = makeView({
      publication_state: "published",
      federation_mode: "followers",
    });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(initial))
      .mockResolvedValueOnce(jsonResponse(saved));
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();

    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toBe(
        "Publication settings loaded.",
      ),
    );
    fireEvent.click(screen.getByLabelText("Allow distribution to followers"));
    const save = screen.getByRole("button", {
      name: "Save publication settings",
    }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.click(
      screen.getByLabelText(/allow follower distribution and understand/i),
    );
    expect(save.disabled).toBe(false);
    fireEvent.click(save);

    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toBe(
        "Publication settings saved.",
      ),
    );
    expect(
      JSON.parse(String((fetchMock.mock.calls[1]?.[1] as RequestInit).body)),
    ).toEqual({
      publication_state: "published",
      federation_mode: "followers",
      index_eligible: false,
    });
  });

  it("withdraws by clearing follower consent and requires explicit confirmation", async () => {
    const initial = makeView({
      publication_state: "published",
      federation_mode: "followers",
      published_at: "2026-11-03T12:00:00.000000000Z",
    });
    const withdrawn = makeView({
      publication_state: "withdrawn",
      federation_mode: "off",
      published_at: initial.published_at,
    });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(initial))
      .mockResolvedValueOnce(jsonResponse(withdrawn));
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();

    await waitFor(() =>
      expect(screen.getByRole("status").textContent).toBe(
        "Publication settings loaded.",
      ),
    );
    fireEvent.change(screen.getByLabelText("Public browsing status"), {
      target: { value: "withdrawn" },
    });
    expect(
      (screen.getByLabelText(
        "Allow distribution to followers",
      ) as HTMLInputElement).checked,
    ).toBe(false);
    const save = screen.getByRole("button", { name: "Save publication settings" });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(
      screen.getByLabelText(/understand withdrawal stops new distribution/i),
    );
    expect((save as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(save);
    await screen.findByText("Publication settings saved.");
    expect(JSON.parse(String((fetchMock.mock.calls[1]?.[1] as RequestInit).body))).toEqual({
      publication_state: "withdrawn",
      federation_mode: "off",
      index_eligible: false,
    });
    expect(screen.getByText("Published", { exact: true }).nextElementSibling?.textContent).toContain("2026");
  });

  it("refreshes after 412 without retrying until the owner reviews and confirms again", async () => {
    const initial = makeView();
    const current = makeView({
      publication_state: "published",
      federation_mode: "followers",
    });
    const freshEtag = `"sha256:${"c".repeat(64)}"`;
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(initial))
      .mockResolvedValueOnce(errorResponse(412, "stale_precondition"))
      .mockResolvedValueOnce(jsonResponse(current, freshEtag))
      .mockResolvedValueOnce(jsonResponse(current, `"sha256:${"d".repeat(64)}"`));
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();

    await screen.findByText("Not published yet");
    fireEvent.change(screen.getByLabelText("Public browsing status"), {
      target: { value: "published" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save publication settings" }));

    const staleNotice = await screen.findByRole("alert");
    expect(staleNotice.textContent).toMatch(/settings changed elsewhere/i);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(
      (screen.getByRole("button", {
        name: "Save publication settings",
      }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(1);

    fireEvent.click(
      screen.getByLabelText(/reviewed the current settings and still want/i),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save publication settings" }));
    await screen.findByText("Publication settings saved.");
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(2);
    const [, retry] = fetchMock.mock.calls[3] as [string, RequestInit];
    expect(new Headers(retry.headers).get("If-Match")).toBe(freshEtag);
  });

  it("keeps the proposed settings and offers a refresh retry if the 412 reload fails", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(makeView()))
      .mockResolvedValueOnce(errorResponse(412, "stale_precondition"))
      .mockResolvedValueOnce(errorResponse(503, "storage_error"))
      .mockResolvedValueOnce(
        jsonResponse(
          makeView({
            publication_state: "published",
            federation_mode: "followers",
          }),
          `"sha256:${"e".repeat(64)}"`,
        ),
      );
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();

    await screen.findByText("Not published yet");
    fireEvent.change(screen.getByLabelText("Public browsing status"), {
      target: { value: "published" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save publication settings" }));

    await screen.findByRole("button", { name: "Reload current settings" });
    expect((screen.getByLabelText("Public browsing status") as HTMLSelectElement).value).toBe(
      "published",
    );
    fireEvent.click(screen.getByRole("button", { name: "Reload current settings" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4));
    expect(screen.getByRole("alert").textContent).toMatch(/settings changed elsewhere/i);
    expect((screen.getByLabelText("Public browsing status") as HTMLSelectElement).value).toBe(
      "published",
    );
    expect(
      (screen.getByRole("button", {
        name: "Save publication settings",
      }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("explains disabled follower gates and aborts an in-flight load on cancel", async () => {
    const pending = new Promise<Response>(() => {});
    const fetchMock = vi.fn().mockReturnValue(pending);
    vi.stubGlobal("fetch", fetchMock);
    const onClose = vi.fn();
    renderOwnerPanel(onClose);
    const [, request] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(request.signal?.aborted).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledOnce();
    expect(request.signal?.aborted).toBe(true);
  });

  it("aborts an in-flight publication read when the panel unmounts", () => {
    const pending = new Promise<Response>(() => {});
    const fetchMock = vi.fn().mockReturnValue(pending);
    vi.stubGlobal("fetch", fetchMock);
    const { unmount } = renderOwnerPanel();
    const [, request] = fetchMock.mock.calls[0] as [string, RequestInit];

    unmount();

    expect(request.signal?.aborted).toBe(true);
  });

  it("aborts an in-flight publication update when the panel unmounts", async () => {
    const pending = new Promise<Response>(() => {});
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(makeView()))
      .mockReturnValueOnce(pending);
    vi.stubGlobal("fetch", fetchMock);
    const { unmount } = renderOwnerPanel();
    await screen.findByText("Not published yet");
    fireEvent.change(screen.getByLabelText("Public browsing status"), {
      target: { value: "published" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save publication settings" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const [, request] = fetchMock.mock.calls[1] as [string, RequestInit];

    unmount();

    expect(request.signal?.aborted).toBe(true);
  });

  it("retries an initial load without treating it as a stale-write confirmation", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(errorResponse(500, "storage_error"))
      .mockResolvedValueOnce(jsonResponse(makeView()));
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();

    fireEvent.click(await screen.findByRole("button", { name: "Reload settings" }));

    await screen.findByText("Not published yet");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByLabelText(/reviewed the current settings/i)).toBeNull();
  });

  it("explains unavailable federation gates after loading the policy", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          makeView({
            visibility: "private",
            public_instance: false,
            federation_enabled: false,
            federation_origin: "",
          }),
        ),
      ),
    );
    renderOwnerPanel();

    await screen.findByText(/follower distribution requires a public, published recipe/i);
    expect(
      (screen.getByLabelText(
        "Allow distribution to followers",
      ) as HTMLInputElement).disabled,
    ).toBe(true);
  });

  it("keeps publication state and ETag after an exact publisher-profile denial", async () => {
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(jsonResponse(makeView()))
      .mockResolvedValueOnce(errorResponse(409, "publisher_profile_required"));
    vi.stubGlobal("fetch", fetchMock);
    renderOwnerPanel();
    await screen.findByText("Not published yet");
    fireEvent.change(screen.getByLabelText("Public browsing status"), { target: { value: "published" } });
    fireEvent.click(screen.getByRole("button", { name: "Save publication settings" }));

    expect(await screen.findByRole("link", { name: /opens in a new tab/i })).toBeTruthy();
    expect((screen.getByLabelText("Public browsing status") as HTMLSelectElement).value).toBe("published");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const [, init] = fetchMock.mock.calls[1] ?? [];
    expect(new Headers(init?.headers).get("If-Match")).toBe(etag);
  });
});
