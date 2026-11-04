import type {
  CreatorCookbookItem,
  CreatorCourseItem,
  CreatorCourseProfileList,
  CreatorProfile,
  CreatorProfileEnvelope,
  CreatorProfileList,
  CreatorRecipeItem,
} from "../../apiModels";

export const CREATOR_PROFILE_PAGE_SIZE = 24;

export class CreatorProfileApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
    this.name = "CreatorProfileApiError";
  }
}

export { CreatorProfileApiError as CreatorProfileRequestError };

async function requestJSON(url: string, init?: RequestInit): Promise<unknown> {
  const response = await fetch(url, { cache: "no-store", ...init });
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as {
      error?: string;
    };
    throw new CreatorProfileApiError(
      body.error ?? "Could not load this creator profile.",
      response.status,
      body.error,
    );
  }
  return response.json() as Promise<unknown>;
}

function isProfile(value: unknown): value is CreatorProfile {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const profile = value as Record<string, unknown>;
  const allowed = new Set([
    "id",
    "public_name",
    "bio",
    "visibility",
    "created_at",
    "updated_at",
  ]);
  return (
    Object.keys(profile).every((key) => allowed.has(key)) &&
    Object.keys(profile).length === allowed.size &&
    typeof profile.id === "string" &&
    typeof profile.public_name === "string" &&
    typeof profile.bio === "string" &&
    (profile.visibility === "private" || profile.visibility === "instance") &&
    typeof profile.created_at === "string" &&
    typeof profile.updated_at === "string"
  );
}

function isEnvelope(
  value: unknown,
  nullable: boolean,
): value is CreatorProfileEnvelope {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  return (
    Object.keys(record).length === 1 &&
    "profile" in record &&
    (record.profile === null ? nullable : isProfile(record.profile))
  );
}

export function isCreatorProfileEnvelope(
  value: unknown,
): value is CreatorProfileEnvelope {
  return isEnvelope(value, true);
}

function malformedResponse(): CreatorProfileApiError {
  return new CreatorProfileApiError(
    "The creator profile response was not valid.",
    502,
    "invalid_creator_profile_response",
  );
}

export async function getSelfCreatorProfile(): Promise<CreatorProfileEnvelope> {
  const result = await requestJSON("/api/account/creator-profile");
  if (!isEnvelope(result, true)) throw malformedResponse();
  return result;
}

export const getMyCreatorProfile = getSelfCreatorProfile;

export function saveSelfCreatorProfile(
  input: Pick<CreatorProfile, "public_name" | "bio" | "visibility">,
) {
  return requestJSON("/api/account/creator-profile", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  }).then((result) => {
    if (!isEnvelope(result, false)) throw malformedResponse();
    return result;
  });
}

export async function getCreatorProfile(
  profileID: string,
): Promise<{ profile: CreatorProfile }> {
  const result = await requestJSON(
    `/api/creator-profiles/${encodeURIComponent(profileID)}`,
  );
  if (!isEnvelope(result, false)) throw malformedResponse();
  return result as { profile: CreatorProfile };
}

function isProfileList<T>(
  value: unknown,
  isItem: (item: unknown) => item is T,
): value is CreatorProfileList<T> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const list = value as Record<string, unknown>;
  return (
    typeof list.profile === "object" &&
    isProfile(list.profile) &&
    Array.isArray(list.items) &&
    list.items.every(isItem) &&
    typeof list.limit === "number" &&
    typeof list.offset === "number"
  );
}

function isRecipeItem(item: unknown): item is CreatorRecipeItem {
  if (!item || typeof item !== "object" || Array.isArray(item)) return false;
  const value = item as Record<string, unknown>;
  return (
    typeof value.role === "string" &&
    typeof value.provenance === "string" &&
    Boolean(
      value.recipe &&
      typeof value.recipe === "object" &&
      typeof (value.recipe as Record<string, unknown>).id === "string" &&
      typeof (value.recipe as Record<string, unknown>).title === "string",
    )
  );
}

function isCookbookItem(item: unknown): item is CreatorCookbookItem {
  if (!item || typeof item !== "object" || Array.isArray(item)) return false;
  const value = item as Record<string, unknown>;
  const book = value.cookbook as Record<string, unknown> | undefined;
  return (
    typeof value.role === "string" &&
    typeof value.provenance === "string" &&
    Boolean(
      book &&
      typeof book.id === "string" &&
      typeof book.title === "string" &&
      typeof book.description === "string" &&
      (book.visibility === "private" ||
        book.visibility === "unlisted" ||
        book.visibility === "public") &&
      typeof book.created_at === "string" &&
      typeof book.updated_at === "string",
    )
  );
}

function isCreatorCourseItem(item: unknown): item is CreatorCourseItem {
  if (!item || typeof item !== "object" || Array.isArray(item)) return false;
  const value = item as Record<string, unknown>;
  const course = value.course;
  if (!course || typeof course !== "object" || Array.isArray(course))
    return false;
  const record = course as Record<string, unknown>;
  return (
    Object.keys(value).length === 3 &&
    Object.keys(value).every((key) =>
      ["course", "role", "provenance"].includes(key),
    ) &&
    Object.keys(record).length === 2 &&
    Object.keys(record).every((key) => ["id", "title"].includes(key)) &&
    typeof record.id === "string" &&
    record.id.length > 0 &&
    typeof record.title === "string" &&
    record.title.length > 0 &&
    value.role === "creator" &&
    value.provenance === "self_asserted_at_creation"
  );
}

function isCreatorCourseProfileList(
  value: unknown,
): value is CreatorCourseProfileList {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const list = value as Record<string, unknown>;
  return (
    Object.keys(list).length === 5 &&
    Object.keys(list).every((key) =>
      ["profile", "items", "total", "limit", "offset"].includes(key),
    ) &&
    isProfile(list.profile) &&
    Array.isArray(list.items) &&
    list.items.every(isCreatorCourseItem) &&
    typeof list.total === "number" &&
    Number.isSafeInteger(list.total) &&
    list.total >= 0 &&
    typeof list.limit === "number" &&
    Number.isSafeInteger(list.limit) &&
    list.limit > 0 &&
    list.limit <= 100 &&
    typeof list.offset === "number" &&
    Number.isSafeInteger(list.offset) &&
    list.offset >= 0 &&
    list.offset <= 1_000_000 &&
    list.items.length <= list.limit
  );
}

export async function listCreatorRecipes(
  profileID: string,
  offset = 0,
): Promise<CreatorProfileList<CreatorRecipeItem>> {
  const query = new URLSearchParams({
    limit: String(CREATOR_PROFILE_PAGE_SIZE),
    offset: String(offset),
  });
  const result = await requestJSON(
    `/api/creator-profiles/${encodeURIComponent(profileID)}/recipes?${query}`,
  );
  if (!isProfileList(result, isRecipeItem)) throw malformedResponse();
  return result;
}

export async function listCreatorCookbooks(
  profileID: string,
  offset = 0,
): Promise<CreatorProfileList<CreatorCookbookItem>> {
  const query = new URLSearchParams({
    limit: String(CREATOR_PROFILE_PAGE_SIZE),
    offset: String(offset),
  });
  const result = await requestJSON(
    `/api/creator-profiles/${encodeURIComponent(profileID)}/cookbooks?${query}`,
  );
  if (!isProfileList(result, isCookbookItem)) throw malformedResponse();
  return result;
}

export async function listCreatorCourses(
  profileID: string,
  offset = 0,
): Promise<CreatorCourseProfileList> {
  const query = new URLSearchParams({
    limit: String(CREATOR_PROFILE_PAGE_SIZE),
    offset: String(offset),
  });
  const result = await requestJSON(
    `/api/creator-profiles/${encodeURIComponent(profileID)}/courses?${query}`,
  );
  if (!isCreatorCourseProfileList(result)) throw malformedResponse();
  return result;
}
