import { useEffect, useRef, useState } from "react";
import type { Cookbook, Recipe } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import useModalFocus from "../../useModalFocus.ts";
import Icon from "../recipes/RecipeIcon.tsx";
import RecipeCardContents from "../recipes/RecipeCardContents.tsx";
import { COOKBOOK_PAGE_SIZE, listCookbookRecipes } from "./cookbookApi.ts";

interface CookbookDetailProps {
  book: Cookbook;
  onClose: () => void;
  onOpenRecipe: (recipe: Recipe) => void;
}

export default function CookbookDetail({ book, onClose, onOpenRecipe }: CookbookDetailProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [query, setQuery] = useState(""),
    [sort, setSort] = useState<"recent" | "oldest" | "title">("recent"),
    [tag, setTag] = useState(""),
    [recipes, setRecipes] = useState<Recipe[]>([]),
    [offset, setOffset] = useState(0),
    [hasMore, setHasMore] = useState(false),
    [loading, setLoading] = useState(true),
    [loadingMore, setLoadingMore] = useState(false),
    [error, setError] = useState("");
  const filterKeyRef = useRef("");
  const filterKey = query.trim() + ":" + sort + ":" + tag.trim();
  filterKeyRef.current = filterKey;
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    listCookbookRecipes(
      book.id,
      { offset: 0, sort, query, tag },
      (response) =>
        response.status === 404
          ? "You no longer have access to this cookbook."
          : "Could not load this cookbook.",
    ).then((data) => {
        if (active) {
          setRecipes(data);
          setOffset(data.length);
          setHasMore(data.length === COOKBOOK_PAGE_SIZE);
        }
      })
      .catch((err: unknown) => {
        if (active) {
          setError(errorMessage(err, "Could not load this cookbook."));
          setRecipes([]);
          setHasMore(false);
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [book.id, query, sort, tag]);
  async function more() {
    if (loadingMore) return;
    const requestedFilterKey = filterKey;
    setLoadingMore(true);
    setError("");
    try {
      const data = await listCookbookRecipes(
        book.id,
        { offset, sort, query, tag },
        "Could not load more recipes.",
      );
      if (filterKeyRef.current !== requestedFilterKey) return;
      setRecipes((current) => [...current, ...data]);
      setOffset((value) => value + data.length);
      setHasMore(data.length === COOKBOOK_PAGE_SIZE);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not load more recipes."));
    } finally {
      setLoadingMore(false);
    }
  }
  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        className="cookbook-detail"
        role="dialog"
        aria-modal="true"
        aria-labelledby="cookbook-detail-title"
        ref={dialogRef}
        tabIndex={-1}
      >
        <button
          className="icon-button dialog-close"
          aria-label="Close cookbook"
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
        <span className="eyebrow">{book.visibility} cookbook</span>
        <h2 id="cookbook-detail-title">{book.title}</h2>
        {book.description && <p>{book.description}</p>}
        <div className="cookbook-filters">
          <label className="cookbook-search">
            <span>Search recipes</span>
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search this cookbook…"
            />
          </label>
          <label>
            <span>Filter by tag</span>
            <input
              value={tag}
              onChange={(event) => setTag(event.target.value)}
              placeholder="e.g. vegetarian"
              maxLength={40}
            />
          </label>
          <label>
            <span>Sort by</span>
            <select
              value={sort}
              onChange={(event) =>
                setSort(event.target.value as "recent" | "oldest" | "title")
              }
            >
              <option value="recent">Recently updated</option>
              <option value="oldest">Oldest updated</option>
              <option value="title">Title</option>
            </select>
          </label>
        </div>
        {loading ? (
          <p className="cookbook-empty">Loading recipes…</p>
        ) : error && !recipes.length ? (
          <p className="form-message" role="alert">
            {error}
          </p>
        ) : recipes.length ? (
          <div className="recipe-grid">
            {recipes.map((recipe) => (
              <button
                className="recipe-card"
                key={recipe.id}
                onClick={() => onOpenRecipe(recipe)}
                aria-label={`Open ${recipe.title}`}
              >
                <RecipeCardContents
                  title={recipe.title}
                  creatorName={recipe.creator_name}
                  image={recipe.image}
                  stepCount={recipe.steps?.length ?? 0}
                  relationshipLabel={`In ${book.title}`}
                />
              </button>
            ))}
          </div>
        ) : (
          <p className="cookbook-empty">
            No recipes are available in this cookbook.
          </p>
        )}
        {error && recipes.length > 0 && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
        {hasMore && (
          <button
            className="button button--green"
            type="button"
            disabled={loadingMore}
            onClick={more}
          >
            {loadingMore ? "Loading…" : "Load more recipes"}
          </button>
        )}
      </section>
    </div>
  );
}
