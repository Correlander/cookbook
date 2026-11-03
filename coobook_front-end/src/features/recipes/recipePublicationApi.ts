import type {
  RecipePublicationInput,
  RecipePublicationView,
} from "../../apiModels";

export interface RecipePublicationSnapshot {
  view: RecipePublicationView;
  etag: string;
}

export class RecipePublicationApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string) {
    super(code);
    this.name = "RecipePublicationApiError";
    this.status = status;
    this.code = code;
  }
}

const strongPublicationEtag = /^"sha256:[a-f0-9]{64}"$/;

function publicationEndpoint(recipeId: string): string {
  return `/api/recipes/${encodeURIComponent(recipeId)}/publication`;
}

async function readErrorCode(response: Response): Promise<string> {
  try {
    const body: unknown = await response.json();
    if (
      typeof body === "object" &&
      body !== null &&
      "error" in body &&
      typeof body.error === "string"
    ) {
      return body.error;
    }
  } catch {
    // Use the HTTP status when the response body is missing or malformed.
  }
  return "request_failed";
}

async function readSnapshot(
  response: Response,
): Promise<RecipePublicationSnapshot> {
  if (!response.ok) {
    throw new RecipePublicationApiError(
      response.status,
      await readErrorCode(response),
    );
  }

  const etag = response.headers.get("ETag");
  if (!etag || !strongPublicationEtag.test(etag)) {
    throw new RecipePublicationApiError(response.status, "invalid_etag");
  }

  const view: unknown = await response.json();
  return { view: view as RecipePublicationView, etag };
}

export async function fetchRecipePublication(
  recipeId: string,
  signal?: AbortSignal,
): Promise<RecipePublicationSnapshot> {
  const response = await fetch(publicationEndpoint(recipeId), {
    method: "GET",
    credentials: "same-origin",
    cache: "no-store",
    signal,
  });
  return readSnapshot(response);
}

export async function updateRecipePublication(
  recipeId: string,
  input: RecipePublicationInput,
  etag: string,
  signal?: AbortSignal,
): Promise<RecipePublicationSnapshot> {
  if (!strongPublicationEtag.test(etag)) {
    throw new RecipePublicationApiError(0, "invalid_etag");
  }

  const response = await fetch(publicationEndpoint(recipeId), {
    method: "PUT",
    credentials: "same-origin",
    cache: "no-store",
    signal,
    headers: {
      "Content-Type": "application/json",
      "If-Match": etag,
    },
    body: JSON.stringify({
      publication_state: input.publication_state,
      federation_mode: input.federation_mode,
      index_eligible: false,
    }),
  });
  return readSnapshot(response);
}
