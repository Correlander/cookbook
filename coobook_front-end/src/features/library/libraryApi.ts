import type {
  CookbookBookmarkPage,
  CookbookBookmarkStatus,
  CookbookBookmarkView,
  Cookbook,
  CreatorAttribution,
  LibraryCookbookItem,
  LibraryPage,
  LibraryRecipeItem,
  LibraryType,
  PublicRecipeContent,
  Recipe,
  RecipeBookmarkStatus,
  RemoteRecipeReadView,
  RecipeBookmarkPage,
  RecipeBookmarkTarget,
  RecipeBookmarkView,
} from "../../apiModels";

export const LIBRARY_PAGE_SIZE = 24;

export function fetchLibraryPage(type: "recipes", offset?: number): Promise<LibraryPage<LibraryRecipeItem>>;
export function fetchLibraryPage(type: "cookbooks", offset?: number): Promise<LibraryPage<LibraryCookbookItem>>;
export async function fetchLibraryPage(
  type: LibraryType,
  offset = 0,
): Promise<LibraryPage<LibraryRecipeItem> | LibraryPage<LibraryCookbookItem>> {
  const params = new URLSearchParams({
    limit: String(LIBRARY_PAGE_SIZE),
    offset: String(offset),
  });
  const response = await fetch(`/api/library/${type}?${params}`, {
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) throw new Error(`Could not load your ${type} Library.`);
  const value: unknown = await response.json();
  const page = parseLibraryPage(value, type);
  if (page.offset !== offset || page.limit > 100)
    throw new Error(`Could not load your ${type} Library.`);
  return page;
}

export async function fetchCachedRemoteRecipe(
  remoteObjectURI: string,
): Promise<RemoteRecipeReadView> {
  if (!remoteObjectURI) throw new Error("Recipe unavailable");
  const params = new URLSearchParams({ remote_object_uri: remoteObjectURI });
  const response = await fetch(`/api/library/recipes/remote?${params}`, {
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) throw new Error("Recipe unavailable");
  const remote = parseRemoteRecipe(await response.json());
  if (remote.object_uri !== remoteObjectURI) throw new Error("Recipe unavailable");
  return remote;
}

export async function fetchRecipeBookmarkStatus(
  target: RecipeBookmarkTarget,
): Promise<RecipeBookmarkStatus> {
  const hasLocal = typeof target.local_recipe_id === "string" && Boolean(target.local_recipe_id);
  const hasRemote = typeof target.remote_object_uri === "string" && Boolean(target.remote_object_uri);
  if (hasLocal === hasRemote) throw new Error("Could not check saved status.");
  const params = new URLSearchParams(
    hasLocal
      ? { local_recipe_id: target.local_recipe_id! }
      : { remote_object_uri: target.remote_object_uri! },
  );
  const response = await fetch(`/api/library/bookmarks/status?${params}`, {
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) throw new Error("Could not check saved status.");
  return parseBookmarkStatus(await response.json());
}

export interface RecipeBookmarkSaveResult {
  bookmark: RecipeBookmarkView;
  created: boolean;
}

export async function fetchRecipeBookmarks(
  offset = 0,
): Promise<RecipeBookmarkPage> {
  const params = new URLSearchParams({
    limit: String(LIBRARY_PAGE_SIZE),
    offset: String(offset),
  });
  const response = await fetch(`/api/library/bookmarks?${params}`, {
    cache: "no-store",
  });
  if (!response.ok) throw new Error("Could not load Saved recipes.");
  return response.json() as Promise<RecipeBookmarkPage>;
}

export async function saveRecipeBookmark(
  target: RecipeBookmarkTarget,
): Promise<RecipeBookmarkSaveResult> {
  const response = await fetch("/api/library/bookmarks", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ target }),
  });
  if (!response.ok) {
    const failure = (await response.json().catch(() => ({}))) as {
      error?: string;
    };
    if (failure.error === "bookmark_limit_reached")
      throw new Error("Your Library has reached its saved recipe limit.");
    throw new Error("Could not save this recipe to your Library.");
  }
  return {
    bookmark: (await response.json()) as RecipeBookmarkView,
    created: response.status === 201,
  };
}

export async function removeRecipeBookmark(bookmarkID: string): Promise<void> {
  const response = await fetch(
    `/api/library/bookmarks/${encodeURIComponent(bookmarkID)}`,
    { method: "DELETE" },
  );
  if (!response.ok) throw new Error("Could not remove this saved recipe.");
}

export async function fetchCookbookBookmarks(
  offset = 0,
): Promise<CookbookBookmarkPage> {
  const params = new URLSearchParams({
    limit: String(LIBRARY_PAGE_SIZE),
    offset: String(offset),
  });
  const response = await fetch(`/api/library/cookbook-bookmarks?${params}`, {
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) throw new Error("Could not load saved cookbooks.");
  const value: unknown = await response.json();
  const page = parseCookbookBookmarkPage(value);
  if (page.offset !== offset)
    throw new Error("Could not load saved cookbooks.");
  return page;
}

export async function fetchCookbookBookmarkStatus(
  cookbookID: string,
): Promise<CookbookBookmarkStatus> {
  const params = new URLSearchParams({ local_cookbook_id: cookbookID });
  const response = await fetch(
    `/api/library/cookbook-bookmarks/status?${params}`,
    { cache: "no-store", credentials: "same-origin" },
  );
  if (!response.ok) throw new Error("Could not check saved status.");
  const value: unknown = await response.json();
  if (!isRecord(value) || typeof value.saved !== "boolean")
    throw new Error("Could not check saved status.");
  if (!value.saved) return { saved: false };
  if (typeof value.bookmark_id !== "string" || !value.bookmark_id)
    throw new Error("Could not check saved status.");
  return { saved: true, bookmark_id: value.bookmark_id };
}

export interface CookbookBookmarkSaveResult {
  bookmark: CookbookBookmarkView;
  created: boolean;
}

export async function saveCookbookBookmark(
  cookbookID: string,
): Promise<CookbookBookmarkSaveResult> {
  const response = await fetch("/api/library/cookbook-bookmarks", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ local_cookbook_id: cookbookID }),
  });
  if (!response.ok) {
    const failure = (await response.json().catch(() => ({}))) as {
      error?: string;
    };
    if (failure.error === "cookbook_bookmark_limit_reached")
      throw new Error("Your Library has reached its saved cookbook limit.");
    throw new Error("Could not save this cookbook to your Library.");
  }
  if (response.status !== 200 && response.status !== 201)
    throw new Error("Could not save this cookbook to your Library.");
  const value: unknown = await response.json();
  const bookmark = parseCookbookBookmarkView(value);
  if (bookmark.availability !== "available")
    throw new Error("Could not save this cookbook to your Library.");
  return { bookmark, created: response.status === 201 };
}

export async function removeCookbookBookmark(
  bookmarkID: string,
): Promise<void> {
  const response = await fetch(
    `/api/library/cookbook-bookmarks/${encodeURIComponent(bookmarkID)}`,
    { method: "DELETE", credentials: "same-origin" },
  );
  if (response.status !== 204)
    throw new Error("Could not remove this saved cookbook.");
}

function parseCookbookBookmarkPage(value: unknown): CookbookBookmarkPage {
  if (
    !isRecord(value) ||
    !Array.isArray(value.items) ||
    !isNonnegativeInteger(value.total) ||
    !isPositiveInteger(value.limit) ||
    !isNonnegativeInteger(value.offset)
  )
    throw new Error("Could not load saved cookbooks.");
  return {
    items: value.items.map(parseCookbookBookmarkView),
    total: value.total,
    limit: value.limit,
    offset: value.offset,
  };
}

function parseCookbookBookmarkView(value: unknown): CookbookBookmarkView {
  if (
    !isRecord(value) ||
    typeof value.id !== "string" ||
    !value.id ||
    value.target_kind !== "local_cookbook" ||
    typeof value.created_at !== "string" ||
    (value.availability !== "available" && value.availability !== "unavailable")
  )
    throw new Error("Could not load saved cookbooks.");

  if (value.availability === "unavailable") {
    return {
      id: value.id,
      target_kind: "local_cookbook",
      availability: "unavailable",
      created_at: value.created_at,
    };
  }

  if (
    !isRecord(value.target) ||
    typeof value.target.local_cookbook_id !== "string" ||
    !isRecord(value.cookbook) ||
    typeof value.cookbook.id !== "string" ||
    value.cookbook.id !== value.target.local_cookbook_id ||
    typeof value.cookbook.title !== "string"
  )
    throw new Error("Could not load saved cookbooks.");

  return {
    id: value.id,
    target_kind: "local_cookbook",
    target: { local_cookbook_id: value.target.local_cookbook_id },
    availability: "available",
    cookbook: parseCookbook(value.cookbook),
    created_at: value.created_at,
  };
}

function parseLibraryPage(
  value: unknown,
  type: LibraryType,
): LibraryPage<LibraryRecipeItem> | LibraryPage<LibraryCookbookItem> {
  if (
    !isRecord(value) ||
    !Array.isArray(value.items) ||
    !isNonnegativeInteger(value.total) ||
    !isPositiveInteger(value.limit) ||
    !isNonnegativeInteger(value.offset)
  ) throw new Error(`Could not load your ${type} Library.`);
  return {
    items: value.items.map((item) =>
      type === "recipes" ? parseLibraryRecipe(item) : parseLibraryCookbook(item),
    ),
    total: value.total,
    limit: value.limit,
    offset: value.offset,
  };
}

function parseLibraryRecipe(value: unknown): LibraryRecipeItem {
  if (!isRecord(value) || !isAvailability(value.availability) ||
      (value.target_kind !== "local" && value.target_kind !== "remote"))
    throw new Error("Could not load your recipes Library.");
  if (value.availability === "unavailable") {
    if (typeof value.bookmark_id !== "string" || !value.bookmark_id || typeof value.created_at !== "string")
      throw new Error("Could not load your recipes Library.");
    return { availability: "unavailable", target_kind: value.target_kind, bookmark_id: value.bookmark_id, created_at: value.created_at };
  }
  if (!Array.isArray(value.relationships) || !value.relationships.every(isLibraryRelationship))
    throw new Error("Could not load your recipes Library.");
  if (value.target_kind === "local") {
    if (value.relationships.includes("saved") !== (typeof value.bookmark_id === "string" && Boolean(value.bookmark_id)))
      throw new Error("Could not load your recipes Library.");
    const recipe = parseRecipe(value.recipe);
    return {
      availability: "available", target_kind: "local", relationships: value.relationships,
      ...(typeof value.bookmark_id === "string" ? { bookmark_id: value.bookmark_id } : {}),
      ...(typeof value.membership_role === "string" ? { membership_role: value.membership_role } : {}),
      ...(typeof value.in_owned_cookbook === "boolean" ? { in_owned_cookbook: value.in_owned_cookbook } : {}),
      ...(Array.isArray(value.cookbook_contexts) ? { cookbook_contexts: value.cookbook_contexts.map(parseCookbookContext) } : {}),
      recipe,
    };
  }
  if (value.relationships.length !== 1 || value.relationships[0] !== "saved")
    throw new Error("Could not load your recipes Library.");
  const remote = parseRemoteRecipe(value.remote_recipe);
  return { availability: "available", target_kind: "remote", relationships: value.relationships, bookmark_id: requiredString(value.bookmark_id), remote_recipe: remote };
}

function parseLibraryCookbook(value: unknown): LibraryCookbookItem {
  if (!isRecord(value) || !isAvailability(value.availability))
    throw new Error("Could not load your cookbooks Library.");
  if (value.availability === "unavailable") {
    if (typeof value.bookmark_id !== "string" || !value.bookmark_id || typeof value.created_at !== "string")
      throw new Error("Could not load your cookbooks Library.");
    return { availability: "unavailable", bookmark_id: value.bookmark_id, created_at: value.created_at };
  }
  if (!Array.isArray(value.relationships) || !value.relationships.every(isLibraryRelationship))
    throw new Error("Could not load your cookbooks Library.");
  if (value.relationships.includes("saved") !== (typeof value.bookmark_id === "string" && Boolean(value.bookmark_id)))
    throw new Error("Could not load your cookbooks Library.");
  return {
    availability: "available", relationships: value.relationships,
    ...(typeof value.bookmark_id === "string" ? { bookmark_id: value.bookmark_id } : {}),
    ...(typeof value.membership_role === "string" ? { membership_role: value.membership_role } : {}),
    cookbook: parseCookbook(value.cookbook),
  };
}

function parseCookbookContext(value: unknown) {
  if (!isRecord(value) || typeof value.id !== "string" || !value.id ||
      (value.membership_role !== undefined && typeof value.membership_role !== "string"))
    throw new Error("Could not load your recipes Library.");
  return { id: value.id, ...(typeof value.membership_role === "string" ? { membership_role: value.membership_role } : {}) };
}

function parseRecipe(value: unknown): Recipe {
  if (!isRecord(value) || typeof value.id !== "string" || !value.id ||
      typeof value.title !== "string" || typeof value.description !== "string" ||
      typeof value.creator_name !== "string" || typeof value.can_edit !== "boolean" ||
      typeof value.can_delete !== "boolean" || !isNonnegativeInteger(value.version) ||
      !isNonnegativeInteger(value.servings) || !isNonnegativeInteger(value.prep_time_minutes) ||
      !isNonnegativeInteger(value.cook_time_minutes) || !Array.isArray(value.ingredients) ||
      !value.ingredients.every(isRecipeIngredient) ||
      !Array.isArray(value.steps) || !value.steps.every(isRecipeStep) ||
      !Array.isArray(value.tags) || !value.tags.every((x) => typeof x === "string") ||
      (value.visibility !== "private" && value.visibility !== "unlisted" && value.visibility !== "public") ||
      typeof value.created_at !== "string" || typeof value.updated_at !== "string")
    throw new Error("Could not load your recipes Library.");
  const ingredients = parseRecipeIngredients(value.ingredients);
  const steps = parseRecipeSteps(value.steps);
  const tags = parseStringList(value.tags, "Could not load your recipes Library.");
  return {
    id: value.id, title: value.title, description: value.description,
    creator_name: value.creator_name, can_edit: value.can_edit, can_delete: value.can_delete,
    version: value.version, servings: value.servings, prep_time_minutes: value.prep_time_minutes,
    cook_time_minutes: value.cook_time_minutes, ingredients,
    steps,
    tags, visibility: value.visibility, created_at: value.created_at, updated_at: value.updated_at,
    ...(value.owner_id === undefined ? {} : { owner_id: requiredString(value.owner_id) }),
    ...parseOptionalCreatorAttributions(value.creator_attributions),
    ...(typeof value.image === "string" ? { image: value.image } : {}),
  };
}

function parseCookbook(value: unknown): Cookbook {
  if (!isRecord(value) || typeof value.id !== "string" || !value.id ||
      typeof value.title !== "string" || typeof value.description !== "string" ||
      typeof value.owner_id !== "string" || !value.owner_id ||
      (value.visibility !== "private" && value.visibility !== "unlisted" && value.visibility !== "public"))
    throw new Error("Could not load your cookbooks Library.");
  return { id: value.id, title: value.title, description: value.description, owner_id: value.owner_id, visibility: value.visibility,
    ...(typeof value.owner_name === "string" ? { owner_name: value.owner_name } : {}),
    ...parseOptionalCreatorAttributions(value.creator_attributions),
    ...(isNonnegativeInteger(value.recipe_count) ? { recipe_count: value.recipe_count } : {}),
    ...(typeof value.created_at === "string" ? { created_at: value.created_at } : {}),
    ...(typeof value.updated_at === "string" ? { updated_at: value.updated_at } : {}) };
}

function parseRemoteRecipe(value: unknown): RemoteRecipeReadView {
  if (!isRecord(value) || !isPositiveInteger(value.sequence))
    throw new Error("Could not load your recipes Library.");
  const recipe = parsePublicRecipeContent(isRecord(value.recipe) ? value.recipe : null);
  return {
    object_uri: requiredString(value.object_uri),
    source_origin: requiredString(value.source_origin),
    source_recipe_id: requiredString(value.source_recipe_id),
    creator_name: requiredString(value.creator_name),
    sequence: value.sequence,
    updated_at: requiredString(value.updated_at),
    recipe,
  };
}

function parsePublicRecipeContent(value: Record<string, unknown> | null): PublicRecipeContent {
  if (!value || !isNonnegativeInteger(value.servings) || !isNonnegativeInteger(value.prep_time_minutes) || !isNonnegativeInteger(value.cook_time_minutes))
    throw new Error("Could not load your recipes Library.");
  return {
    title: requiredString(value.title),
    description: requiredString(value.description),
    servings: value.servings,
    prep_time_minutes: value.prep_time_minutes,
    cook_time_minutes: value.cook_time_minutes,
    ingredients: parseRecipeIngredients(value.ingredients),
    steps: parseRecipeSteps(value.steps),
    tags: parseStringList(value.tags, "Could not load your recipes Library."),
  };
}

function isRecipeIngredient(value: unknown): boolean {
  return isRecord(value) && typeof value.name === "string" &&
    (value.quantity === undefined || typeof value.quantity === "string") &&
    (value.notes === undefined || typeof value.notes === "string");
}

function parseRecipeIngredient(value: unknown) {
  if (!isRecord(value) || typeof value.name !== "string") throw new Error("Could not load your recipes Library.");
  return { name: value.name, ...(typeof value.quantity === "string" ? { quantity: value.quantity } : {}), ...(typeof value.notes === "string" ? { notes: value.notes } : {}) };
}

function isRecipeStep(value: unknown): boolean { return isRecord(value) && typeof value.text === "string"; }
function parseRecipeStep(value: unknown) {
  if (!isRecord(value) || typeof value.text !== "string") throw new Error("Could not load your recipes Library.");
  return { text: value.text };
}

function parseRecipeIngredients(value: unknown) {
  if (!Array.isArray(value) || !value.every(isRecipeIngredient)) throw new Error("Could not load your recipes Library.");
  return value.map(parseRecipeIngredient);
}

function parseRecipeSteps(value: unknown) {
  if (!Array.isArray(value) || !value.every(isRecipeStep)) throw new Error("Could not load your recipes Library.");
  return value.map(parseRecipeStep);
}

function parseStringList(value: unknown, message: string): string[] {
  if (!Array.isArray(value) || !value.every(isString)) throw new Error(message);
  return value;
}

function isString(value: unknown): value is string { return typeof value === "string"; }

function parseCreatorAttribution(value: unknown): CreatorAttribution {
  if (!isRecord(value) || (value.role !== "creator" && value.role !== "contributor" && value.role !== "adapter") || typeof value.provenance !== "string")
    throw new Error("Could not load Library creator attribution.");
  if (value.subject_kind === "external_claim") {
    if (value.name_claim !== undefined && typeof value.name_claim !== "string") throw new Error("Could not load Library creator attribution.");
    return { subject_kind: "external_claim", role: value.role, provenance: value.provenance, ...(typeof value.name_claim === "string" ? { name_claim: value.name_claim } : {}) };
  }
  if (value.subject_kind !== "local" || !isRecord(value.profile) || typeof value.profile.id !== "string" ||
      typeof value.profile.public_name !== "string" || (value.profile.visibility !== "private" && value.profile.visibility !== "instance"))
    throw new Error("Could not load Library creator attribution.");
  return { subject_kind: "local", role: value.role, provenance: value.provenance, profile: { id: value.profile.id, public_name: value.profile.public_name, visibility: value.profile.visibility } };
}

function parseOptionalCreatorAttributions(value: unknown): { creator_attributions?: CreatorAttribution[] } {
  if (value === undefined) return {};
  if (!Array.isArray(value)) throw new Error("Could not load Library creator attribution.");
  return { creator_attributions: value.map(parseCreatorAttribution) };
}

function parseBookmarkStatus(value: unknown): RecipeBookmarkStatus {
  if (!isRecord(value) || typeof value.saved !== "boolean") throw new Error("Could not check saved status.");
  if (!value.saved) return { saved: false };
  if (typeof value.bookmark_id !== "string" || !value.bookmark_id) throw new Error("Could not check saved status.");
  return { saved: true, bookmark_id: value.bookmark_id };
}

function requiredString(value: unknown): string {
  if (typeof value !== "string" || !value) throw new Error("Could not load your recipes Library.");
  return value;
}
function isAvailability(value: unknown): value is "available" | "unavailable" { return value === "available" || value === "unavailable"; }
function isLibraryRelationship(value: unknown): value is "owned" | "shared" | "saved" { return value === "owned" || value === "shared" || value === "saved"; }

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isNonnegativeInteger(value: unknown): value is number {
  return Number.isInteger(value) && typeof value === "number" && value >= 0;
}

function isPositiveInteger(value: unknown): value is number {
  return Number.isInteger(value) && typeof value === "number" && value > 0;
}
