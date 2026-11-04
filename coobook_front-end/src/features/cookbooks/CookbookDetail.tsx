import { useEffect, useRef, useState } from "react";
import type {
  Cookbook,
  PublicCookbook,
  PublicCookbookRecipe,
  Recipe,
} from "../../apiModels";
import { errorMessage } from "../../apiModels";
import CreatorCredits from "../creator/CreatorCredits.tsx";
import RecipeCardContents from "../recipes/RecipeCardContents.tsx";
import CookbookBookmarkAction from "./CookbookBookmarkAction.tsx";
import CookbookForm from "./CookbookForm.tsx";
import CookbookMembers from "./CookbookMembers.tsx";
import {
  COOKBOOK_PAGE_SIZE,
  listCookbookRecipes,
  listPublicCookbookRecipes,
} from "./cookbookApi.ts";

type CookbookView = Cookbook | PublicCookbook;
type CookbookRecipe = Recipe | PublicCookbookRecipe;
type CookbookViewMode = "public" | "authenticated";

function isPublicCookbook(book: CookbookView): book is PublicCookbook {
  return !("owner_id" in book);
}

interface CookbookDetailProps {
  initialBook: CookbookView;
  mode: CookbookViewMode;
  currentUserID: string | null;
  returnQuery?: string;
}

function backHref(query: string) {
  const params = new URLSearchParams();
  if (query.trim()) params.set("q", query.trim());
  return `/cookbooks${params.size ? `?${params}` : ""}`;
}

export default function CookbookDetail({
  initialBook,
  mode,
  currentUserID,
  returnQuery = "",
}: CookbookDetailProps) {
  const isPublic = mode === "public";
  const [book, setBook] = useState<CookbookView>(initialBook);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<"recent" | "oldest" | "title">("recent");
  const [tag, setTag] = useState("");
  const [recipes, setRecipes] = useState<CookbookRecipe[]>([]);
  const [offset, setOffset] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [editOpen, setEditOpen] = useState(false);
  const [membersOpen, setMembersOpen] = useState(false);
  const filterKeyRef = useRef("");
  const filterKey = query.trim() + ":" + sort + ":" + tag.trim();
  filterKeyRef.current = filterKey;
  const publicBook = isPublicCookbook(book) ? book : null;
  const ownerID = "owner_id" in book ? book.owner_id : null;

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    const failure = (response: Response) =>
      response.status === 404
        ? "This cookbook is unavailable."
        : "Could not load recipes in this cookbook.";
    const request = isPublic
      ? listPublicCookbookRecipes(
          book.id,
          { offset: 0, sort, query, tag },
          "Could not load recipes in this cookbook.",
        )
      : listCookbookRecipes(book.id, { offset: 0, sort, query, tag }, failure);
    request
      .then((data) => {
        if (!active) return;
        setRecipes(data);
        setOffset(data.length);
        setHasMore(data.length === COOKBOOK_PAGE_SIZE);
      })
      .catch((loadError: unknown) => {
        if (active) {
          setError(
            errorMessage(loadError, "Could not load recipes in this cookbook."),
          );
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
  }, [book.id, isPublic, query, sort, tag]);

  async function loadMore() {
    if (loadingMore) return;
    const requestedFilterKey = filterKey;
    setLoadingMore(true);
    setError("");
    try {
      const filters = { offset, sort, query, tag };
      const data = isPublic
        ? await listPublicCookbookRecipes(
            book.id,
            filters,
            "Could not load more recipes.",
          )
        : await listCookbookRecipes(
            book.id,
            filters,
            "Could not load more recipes.",
          );
      if (filterKeyRef.current !== requestedFilterKey) return;
      setRecipes((current) => [...current, ...data]);
      setOffset((current) => current + data.length);
      setHasMore(data.length === COOKBOOK_PAGE_SIZE);
    } catch (loadError: unknown) {
      setError(errorMessage(loadError, "Could not load more recipes."));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="cookbook-detail-title"
    >
      <div className="section-heading">
        <div>
          <span className="eyebrow">
            {isPublic && publicBook
              ? `${publicBook.recipe_count} public ${publicBook.recipe_count === 1 ? "recipe" : "recipes"}`
              : "visibility" in book
                ? `${book.visibility} cookbook`
                : "Public cookbook"}
          </span>
          <h1 id="cookbook-detail-title">{book.title}</h1>
        </div>
        {!isPublic && ownerID === currentUserID && (
          <div>
            <button
              className="text-link"
              type="button"
              onClick={() => setEditOpen(true)}
            >
              Edit cookbook
            </button>
            <button
              className="text-link"
              type="button"
              onClick={() => setMembersOpen(true)}
            >
              Share and manage members
            </button>
          </div>
        )}
      </div>
      <CookbookBookmarkAction
        cookbookID={book.id}
        title={book.title}
        currentUserID={currentUserID}
      />
      {book.description && <p>{book.description}</p>}
      {!isPublic && currentUserID && "owner_id" in book && (
        <CreatorCredits
          attributions={book.creator_attributions}
          resourceKind="cookbook"
          currentUserID={currentUserID}
        />
      )}
      <p className="cookbook-detail__navigation">
        <a className="text-link" href={backHref(returnQuery)}>
          Back to cookbooks
        </a>
      </p>
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
        <p className="cookbook-empty" role="status">
          Loading recipes…
        </p>
      ) : error && recipes.length === 0 ? (
        <p className="form-message" role="alert">
          {error}
        </p>
      ) : recipes.length ? (
        <div className="recipe-grid">
          {recipes.map((recipe) => (
            <a
              className="recipe-card block"
              href={`/recipe?id=${encodeURIComponent(recipe.id)}`}
              aria-label={`Open ${recipe.title}`}
              key={recipe.id}
            >
              <RecipeCardContents
                title={recipe.title}
                creatorName={recipe.creator_name}
                creatorAttributions={
                  !isPublic && "can_edit" in recipe
                    ? recipe.creator_attributions
                    : undefined
                }
                image={recipe.image}
                stepCount={recipe.steps.length}
                relationshipLabel={`In ${book.title}`}
              />
            </a>
          ))}
        </div>
      ) : (
        <p className="cookbook-empty">
          {isPublic
            ? "No public recipes are available in this cookbook."
            : "No recipes are currently available in this cookbook."}
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
          onClick={loadMore}
        >
          {loadingMore ? "Loading…" : "Load more recipes"}
        </button>
      )}
      {editOpen && "owner_id" in book && (
        <CookbookForm
          book={book}
          onClose={() => setEditOpen(false)}
          onSaved={(updated) => {
            setBook(updated);
            setEditOpen(false);
          }}
        />
      )}
      {membersOpen && "owner_id" in book && (
        <CookbookMembers book={book} onClose={() => setMembersOpen(false)} />
      )}
    </section>
  );
}
