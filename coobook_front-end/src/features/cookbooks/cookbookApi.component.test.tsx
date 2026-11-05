import { afterEach, expect, it, vi } from "vitest";
import {
  CookbookApiError,
  getCookbook,
  getPublicCookbook,
  getCookbookMembers,
  listCookbookRecipes,
  listPublicCookbookRecipes,
  saveCookbook,
} from "./cookbookApi.ts";

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => vi.unstubAllGlobals());

it("uses distinct no-store metadata endpoints for authenticated and public cookbooks", async () => {
  const fetchMock = vi.fn<typeof fetch>()
    .mockResolvedValueOnce(jsonResponse({ id: "book-1" }))
    .mockResolvedValueOnce(jsonResponse({ id: "book-2" }));
  vi.stubGlobal("fetch", fetchMock);

  await getCookbook("private/book", "Could not load cookbook.");
  await getPublicCookbook("public book", "Could not load cookbook.");

  expect(fetchMock.mock.calls).toEqual([
    ["/api/cookbooks/private%2Fbook", { cache: "no-store" }],
    ["/api/public/cookbooks/public%20book", { cache: "no-store" }],
  ]);
});

it("does not retry a denied authenticated metadata read through the public API", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({}, 404));
  vi.stubGlobal("fetch", fetchMock);

  await expect(getCookbook("private-book", "Cookbook unavailable.")).rejects.toThrow(
    "Cookbook unavailable.",
  );
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledWith("/api/cookbooks/private-book", {
    cache: "no-store",
  });
});

it("builds the public cookbook recipe query through the shared API boundary", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
    jsonResponse([
        {
          id: "recipe-1",
          creator_name: "Ada",
          title: "Soup",
          description: "Warm and simple.",
          ingredients: [],
          steps: [],
          servings: 2,
          prep_time_minutes: 5,
          cook_time_minutes: 10,
          tags: ["quick"],
          visibility: "public",
          version: 1,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
    ]),
  );
  vi.stubGlobal("fetch", fetchMock);

  const result = await listPublicCookbookRecipes(
    "book/one",
    { offset: 24, sort: "title", query: "  soup ", tag: " quick " },
    "Could not load recipes.",
  );

  expect(result[0]?.title).toBe("Soup");
  const [url] = fetchMock.mock.calls[0] ?? [];
  expect(String(url)).toBe(
    "/api/public/cookbooks/book%2Fone/recipes?limit=24&offset=24&sort=title&q=soup&tag=quick",
  );
});

it("uses the authenticated cookbook query and preserves its access-denial message", async () => {
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValue(jsonResponse({}, 404));
  vi.stubGlobal("fetch", fetchMock);

  await expect(
    listCookbookRecipes(
      "private-book",
      { offset: 0, sort: "recent", query: "", tag: "" },
      (response) =>
        response.status === 404
          ? "You no longer have access to this cookbook."
          : "Could not load this cookbook.",
    ),
  ).rejects.toThrow("You no longer have access to this cookbook.");
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
    "/api/recipes?cookbook_id=private-book&limit=24&offset=0&sort=recent",
  );
});

it("keeps cookbook edits on the existing PATCH contract without extra fields", async () => {
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValue(jsonResponse({ id: "book-1" }));
  vi.stubGlobal("fetch", fetchMock);

  await saveCookbook(
    {
      id: "book-1",
      title: "Old",
      description: "Old description",
      visibility: "private",
      owner_id: "owner-1",
    },
    { title: "New", description: "Updated", visibility: "public" },
  );

  const [, init] = fetchMock.mock.calls[0] ?? [];
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/cookbooks/book-1");
  expect(init?.method).toBe("PATCH");
  expect(JSON.parse(String(init?.body))).toEqual({
    title: "New",
    description: "Updated",
    visibility: "public",
  });
});

it("preserves the exact status and error code for a denied public cookbook edit", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
    jsonResponse({ error: "publisher_profile_required" }, 409),
  );
  vi.stubGlobal("fetch", fetchMock);

  await expect(saveCookbook(
    { id: "book-1", title: "Old", description: "Old", visibility: "private", owner_id: "owner-1" },
    { title: "New", description: "Description", visibility: "public" },
  )).rejects.toMatchObject({
    status: 409,
    code: "publisher_profile_required",
  } satisfies Partial<CookbookApiError>);
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

it("reports member-read denial from the shared API boundary", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(jsonResponse([], 403))
    .mockResolvedValueOnce(jsonResponse([]));
  vi.stubGlobal("fetch", fetchMock);

  await expect(getCookbookMembers("book-1")).rejects.toThrow(
    "Could not load cookbook members.",
  );
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
