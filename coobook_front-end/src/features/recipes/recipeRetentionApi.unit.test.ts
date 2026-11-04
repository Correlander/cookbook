import { afterEach, describe, expect, it, vi } from "vitest";
import {
  queueRecipeRetention,
  RecipeRetentionApiError,
} from "./recipeRetentionApi.ts";

function response(body: unknown, status = 202): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => vi.unstubAllGlobals());

describe("queueRecipeRetention", () => {
  it("posts only the expected current recipe version without caching", async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({ status: "queued" }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(queueRecipeRetention("recipe/one", 7)).resolves.toEqual({
      status: "queued",
    });
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/recipes/recipe%2Fone/federation-retention",
      expect.objectContaining({
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ expected_recipe_version: 7 }),
      }),
    );
  });

  it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    "rejects invalid expected versions before making a request (%s)",
    async (version) => {
      const fetchMock = vi.fn();
      vi.stubGlobal("fetch", fetchMock);

      await expect(queueRecipeRetention("recipe-1", version)).rejects.toMatchObject({
        name: "RecipeRetentionApiError",
        code: "invalid_recipe_version",
      });
      expect(fetchMock).not.toHaveBeenCalled();
    },
  );

  it.each([
    [409, "federation_assertion_conflict"],
    [403, "reauthentication_required"],
    [503, "federation_queue_unavailable"],
    [404, "not_found"],
    [500, "storage_error"],
  ])("preserves the server error code %s %s", async (status, code) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ error: code }, status)));

    await expect(queueRecipeRetention("recipe-1", 3)).rejects.toMatchObject({
      name: "RecipeRetentionApiError",
      status,
      code,
    });
  });

  it("requires HTTP 202 and the exact queued status before reporting success", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ status: "sent" })));
    await expect(queueRecipeRetention("recipe-1", 3)).rejects.toBeInstanceOf(
      RecipeRetentionApiError,
    );

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ status: "queued" }, 200)));
    await expect(queueRecipeRetention("recipe-1", 3)).rejects.toMatchObject({
      status: 200,
      code: "request_failed",
    });
  });
});
