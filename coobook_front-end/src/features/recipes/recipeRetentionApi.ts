export class RecipeRetentionApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string) {
    super(code);
    this.name = "RecipeRetentionApiError";
    this.status = status;
    this.code = code;
  }
}

async function readErrorCode(response: Response): Promise<string> {
  try {
    const body: unknown = await response.json();
    if (
      typeof body === "object" &&
      body !== null &&
      "error" in body &&
      typeof body.error === "string"
    )
      return body.error;
  } catch {
    // Use the HTTP status when the response body is missing or malformed.
  }
  return "request_failed";
}

export async function queueRecipeRetention(
  recipeId: string,
  expectedRecipeVersion: number,
  signal?: AbortSignal,
): Promise<{ status: "queued" }> {
  if (!Number.isSafeInteger(expectedRecipeVersion) || expectedRecipeVersion < 1)
    throw new RecipeRetentionApiError(400, "invalid_recipe_version");

  const response = await fetch(
    `/api/recipes/${encodeURIComponent(recipeId)}/federation-retention`,
    {
      method: "POST",
      credentials: "same-origin",
      cache: "no-store",
      signal,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ expected_recipe_version: expectedRecipeVersion }),
    },
  );

  if (response.status !== 202)
    throw new RecipeRetentionApiError(response.status, await readErrorCode(response));

  let body: unknown;
  try {
    body = await response.json();
  } catch {
    throw new RecipeRetentionApiError(202, "invalid_response");
  }
  if (
    typeof body !== "object" ||
    body === null ||
    !("status" in body) ||
    body.status !== "queued"
  )
    throw new RecipeRetentionApiError(202, "invalid_response");

  return { status: "queued" };
}
