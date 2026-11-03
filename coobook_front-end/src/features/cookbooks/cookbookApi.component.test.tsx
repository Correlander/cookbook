import { afterEach, expect, it, vi } from "vitest";
import {
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
