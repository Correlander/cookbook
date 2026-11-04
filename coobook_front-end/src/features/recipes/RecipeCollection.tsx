import { useEffect, useState, type ReactNode } from "react";
import type { RecipeFeed } from "./useRecipeFeed.ts";
import type { RecipeFeedEntry } from "./recipeFeedApi.ts";

interface RecipeCollectionProps {
  signedIn: boolean;
  query: string;
  feed: RecipeFeed;
  onOpenRecipe: (recipe: RecipeFeedEntry) => void;
  onCreateRecipe?: () => void;
  heading?: string;
  emptyMessage?: string;
  renderRecipe: (
    recipe: RecipeFeedEntry,
    onOpen: (recipe: RecipeFeedEntry) => void,
  ) => ReactNode;
  addIcon?: ReactNode;
  searchIcon?: ReactNode;
}

export default function RecipeCollection({
  signedIn,
  query,
  feed,
  onOpenRecipe,
  onCreateRecipe,
  heading,
  emptyMessage,
  renderRecipe,
  addIcon,
  searchIcon,
}: RecipeCollectionProps) {
  const [announcement, setAnnouncement] = useState("");
  const normalizedQuery = query.trim();

  useEffect(() => {
    const message = feed.loading
      ? "Loading recipes…"
      : feed.loadError
        ? "Recipes are unavailable right now. Please try again later."
        : feed.items.length
          ? `${feed.items.length} ${feed.items.length === 1 ? "recipe" : "recipes"} ${normalizedQuery ? `found for ${normalizedQuery}` : "loaded"}.`
          : normalizedQuery
            ? `No recipes match “${normalizedQuery}”.`
            : "No recipes are available.";
    const timeout = window.setTimeout(
      () => setAnnouncement(message),
      feed.loading ? 600 : 400,
    );
    return () => window.clearTimeout(timeout);
  }, [feed.items.length, feed.loadError, feed.loading, normalizedQuery]);

  return (
    <section className="recipe-section" id="recipes">
      <p className="sr-only" role="status" aria-live="polite" aria-atomic="true">
        {announcement}
      </p>
      <div className="section-heading">
        <div>
          <span className="eyebrow">
            {signedIn ? "On this Cookbook" : "Public on this Cookbook"}
          </span>
          <h2>{heading ?? (signedIn ? "Recipes you can access" : "Find your next favorite")}</h2>
        </div>
        {signedIn && onCreateRecipe && (
          <button className="text-link" onClick={onCreateRecipe}>
            Add a recipe {addIcon}
          </button>
        )}
      </div>
      {feed.loading ? (
        <p className="empty-state">Loading recipes…</p>
      ) : feed.loadError ? (
        <div className="empty-state">
          <p>Recipes are unavailable right now. Please try again later.</p>
        </div>
      ) : feed.items.length ? (
        <>
          <div className="recipe-grid">
            {feed.items.map((recipe) => renderRecipe(recipe, onOpenRecipe))}
          </div>
          {feed.loadMoreError && (
            <p className="form-message" role="alert">
              {feed.loadMoreError}
            </p>
          )}
          {feed.hasMore && (
            <button
              className="button button--green"
              type="button"
              onClick={feed.loadMore}
              disabled={feed.loadingMore}
            >
              {feed.loadingMore ? "Loading…" : "Load more recipes"}
            </button>
          )}
        </>
      ) : (
        <div className="empty-state">
          {searchIcon}
          <p>{emptyMessage ?? `No recipes match “${query}”. Try another search.`}</p>
        </div>
      )}
    </section>
  );
}
