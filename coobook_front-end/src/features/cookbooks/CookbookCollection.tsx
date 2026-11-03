import type { ReactNode } from "react";
import { useEffect, useRef, useState } from "react";
import type { AuthenticatedUser, Cookbook, PublicCookbook } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import Icon from "../recipes/RecipeIcon.tsx";
import CookbookForm from "./CookbookForm.tsx";
import CookbookMembers from "./CookbookMembers.tsx";
import { COOKBOOK_PAGE_SIZE, listCookbooks, listPublicCookbooks } from "./cookbookApi.ts";

type CollectionMode = "public" | "accessible";
type CollectionSurface = "home" | "page";
type CookbookEntry = Cookbook | PublicCookbook;

interface CookbookCollectionProps {
  mode: CollectionMode;
  surface?: CollectionSurface;
  user?: AuthenticatedUser | null;
  initialQuery?: string;
  enabled?: boolean;
  pending?: boolean;
  children?: ReactNode;
}

function cookbookHref(id: string, query: string, isPublic: boolean) {
  const params = new URLSearchParams({ id });
  if (isPublic && query.trim()) params.set("return_q", query.trim());
  return `/cookbook?${params}`;
}

export default function CookbookCollection({
  mode,
  surface = "home",
  user = null,
  initialQuery = "",
  enabled = true,
  pending = false,
  children,
}: CookbookCollectionProps) {
  const isPublic = mode === "public";
  const isPage = surface === "page";
  const [query, setQuery] = useState(initialQuery);
  const [cookbooks, setCookbooks] = useState<CookbookEntry[]>([]);
  const [offset, setOffset] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [editBook, setEditBook] = useState<Cookbook | null>(null);
  const [memberBook, setMemberBook] = useState<Cookbook | null>(null);
  const queryRef = useRef(query.trim());
  queryRef.current = query.trim();

  useEffect(() => {
    let active = true;
    if (!enabled) {
      setCookbooks([]);
      setHasMore(false);
      setError("");
      setLoading(false);
      return () => {
        active = false;
      };
    }
    setLoading(true);
    setError("");
    const request = isPublic
      ? listPublicCookbooks(query, 0, "Public cookbooks are unavailable.")
      : listCookbooks(0, "Could not load your cookbooks.");
    request
      .then((data) => {
        if (!active) return;
        setCookbooks(data);
        setOffset(data.length);
        setHasMore(data.length === COOKBOOK_PAGE_SIZE);
      })
      .catch((loadError: unknown) => {
        if (active) {
          setError(
            errorMessage(
              loadError,
              isPublic
                ? "Public cookbooks are unavailable."
                : "Could not load your cookbooks.",
            ),
          );
          setCookbooks([]);
          setHasMore(false);
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [enabled, isPublic, query]);

  function updateQuery(value: string) {
    setQuery(value);
    if (!isPage) return;
    const url = new URL(window.location.href);
    if (value.trim()) url.searchParams.set("q", value.trim());
    else url.searchParams.delete("q");
    window.history.replaceState(
      {},
      "",
      `${url.pathname}${url.search}${url.hash}`,
    );
  }

  async function loadMore() {
    if (loadingMore) return;
    const requestedQuery = queryRef.current;
    setLoadingMore(true);
    setError("");
    try {
      const data = isPublic
        ? await listPublicCookbooks(
            query,
            offset,
            "Could not load more cookbooks.",
          )
        : await listCookbooks(offset, "Could not load more cookbooks.");
      if (queryRef.current !== requestedQuery) return;
      setCookbooks((current) => {
        const known = new Set(current.map((book) => book.id));
        return [...current, ...data.filter((book) => !known.has(book.id))];
      });
      setOffset((current) => current + data.length);
      setHasMore(data.length === COOKBOOK_PAGE_SIZE);
    } catch (loadError: unknown) {
      setError(
        errorMessage(loadError, "Could not load more cookbooks."),
      );
    } finally {
      setLoadingMore(false);
    }
  }

  const sectionID = surface === "home" ? "cookbooks" : undefined;
  const headingID = isPublic ? "public-cookbooks-title" : "my-cookbooks-title";
  const Heading = isPage ? "h1" : "h2";

  return (
    <section
      className={isPublic ? "public-cookbooks" : "cookbook-dashboard"}
      id={sectionID}
      aria-labelledby={headingID}
    >
      <div
        className="section-heading"
        id={isPage ? undefined : "my-cookbooks"}
      >
        <div>
          <span className="eyebrow">
            {isPublic ? "Shared by their keepers" : "Your collection"}
          </span>
          <Heading id={headingID}>
            {isPublic ? "Browse public cookbooks" : "Cookbooks you can access"}
          </Heading>
        </div>
        {!enabled ? (
          <a className="text-link" href="/cookbooks">Open cookbooks</a>
        ) : isPublic ? (
          <label className="cookbook-search">
            <span className="visually-hidden">Search public cookbooks</span>
            <input
              value={query}
              onChange={(event) => updateQuery(event.target.value)}
              placeholder="Search cookbooks…"
            />
          </label>
        ) : (
          <button
            className="text-link"
            type="button"
            onClick={() => setCreateOpen(true)}
          >
            Create cookbook <Icon name="arrow" size={15} />
          </button>
        )}
      </div>
      {loading ? (
        <p className="cookbook-empty" role="status">
          {isPublic ? "Loading public cookbooks…" : "Loading cookbooks…"}
        </p>
      ) : !enabled ? (
        <p className="cookbook-empty" role="status">
          {pending
            ? "Checking your session…"
            : "Your session could not be confirmed. Use Cookbooks to continue."}
        </p>
      ) : error && cookbooks.length === 0 ? (
        <p className="form-message" role="alert">
          {error}
        </p>
      ) : cookbooks.length === 0 ? (
        <p className="cookbook-empty">
          {isPublic
            ? "No public cookbooks are available here yet."
            : "Your cookbooks will appear here. Create one to start gathering recipes."}
        </p>
      ) : (
        <div className="cookbook-grid">
          {cookbooks.map((book) => (
            <article className="cookbook-card" key={book.id}>
              <span className="notebook-banner__icon" aria-hidden="true">
                {isPublic ? "✿" : <Icon name="book" size={24} />}
              </span>
              <h3>{book.title}</h3>
              {book.description && <p>{book.description}</p>}
              {isPublic && "recipe_count" in book ? (
                <span className="recipe-card__category">
                  {book.recipe_count} public {book.recipe_count === 1 ? "recipe" : "recipes"}
                </span>
              ) : "visibility" in book ? (
                <span className="recipe-card__category">
                  {book.visibility} cookbook
                </span>
              ) : null}
              <a
                className="text-link"
                href={cookbookHref(book.id, query, isPublic)}
              >
                Open cookbook
              </a>
              {!isPublic &&
                "owner_id" in book &&
                book.owner_id === user?.id && (
                  <>
                    <button
                      className="text-link"
                      type="button"
                      onClick={() => setEditBook(book)}
                    >
                      Edit cookbook
                    </button>
                    <button
                      className="text-link"
                      type="button"
                      onClick={() => setMemberBook(book)}
                    >
                      Share and manage members
                    </button>
                  </>
                )}
            </article>
          ))}
        </div>
      )}
      {error && cookbooks.length > 0 && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {hasMore && (
        <button
          className="button button--green"
          type="button"
          onClick={loadMore}
          disabled={loadingMore}
        >
          {loadingMore ? "Loading…" : "Load more cookbooks"}
        </button>
      )}
      {createOpen && (
        <CookbookForm
          onClose={() => setCreateOpen(false)}
          onSaved={(book) => {
            setCookbooks((current) => [book, ...current]);
            setOffset((current) => current + 1);
            setCreateOpen(false);
          }}
        />
      )}
      {editBook && (
        <CookbookForm
          book={editBook}
          onClose={() => setEditBook(null)}
          onSaved={(book) => {
            setCookbooks((current) =>
              current.map((item) => (item.id === book.id ? book : item)),
            );
            setEditBook(null);
          }}
        />
      )}
      {memberBook && (
        <CookbookMembers
          book={memberBook}
          onClose={() => setMemberBook(null)}
        />
      )}
      {children}
    </section>
  );
}
