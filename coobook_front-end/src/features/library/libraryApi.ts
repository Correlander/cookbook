import type {
  RecipeBookmarkPage,
  RecipeBookmarkTarget,
  RecipeBookmarkView,
} from "../../apiModels";

export const LIBRARY_PAGE_SIZE = 24;

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
