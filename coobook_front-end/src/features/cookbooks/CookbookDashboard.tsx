import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import type { AuthenticatedUser, Cookbook, Recipe } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import Icon from "../recipes/RecipeIcon.tsx";
import CookbookDetail from "./CookbookDetail.tsx";
import CookbookForm from "./CookbookForm.tsx";
import CookbookMembers from "./CookbookMembers.tsx";
import { COOKBOOK_PAGE_SIZE, listCookbooks } from "./cookbookApi.ts";

interface CookbookDashboardProps {
  user: AuthenticatedUser | null;
  children: ReactNode;
  onOpenRecipe: (recipe: Recipe) => void;
}

export default function CookbookDashboard({
  user,
  children,
  onOpenRecipe,
}: CookbookDashboardProps) {
  const [cookbooks, setCookbooks] = useState<Cookbook[]>([]),
    [offset, setOffset] = useState(0),
    [hasMore, setHasMore] = useState(false),
    [loadingMore, setLoadingMore] = useState(false),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [createOpen, setCreateOpen] = useState(false),
    [editBook, setEditBook] = useState<Cookbook | null>(null),
    [memberBook, setMemberBook] = useState<Cookbook | null>(null),
    [detailBook, setDetailBook] = useState<Cookbook | null>(null);
  useEffect(() => {
    let active = true;
    listCookbooks(0, "Could not load your cookbooks.")
      .then((data) => {
        if (active) {
          setCookbooks(data);
          setOffset(data.length);
          setHasMore(data.length === COOKBOOK_PAGE_SIZE);
        }
      })
      .catch((err: unknown) => {
        if (active)
          setError(errorMessage(err, "Could not load your cookbooks."));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);
  async function loadMore() {
    if (loadingMore) return;
    setLoadingMore(true);
    setError("");
    try {
      const data = await listCookbooks(offset, "Could not load more cookbooks.");
      setCookbooks((current) => {
        const known = new Set(current.map((book) => book.id));
        return [...current, ...data.filter((book) => !known.has(book.id))];
      });
      setOffset((value) => value + data.length);
      setHasMore(data.length === COOKBOOK_PAGE_SIZE);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not load more cookbooks."));
    } finally {
      setLoadingMore(false);
    }
  }
  return (
    <section className="cookbook-dashboard" id="my-cookbooks">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Your collection</span>
          <h2>Cookbooks you can access</h2>
        </div>
        <button className="text-link" onClick={() => setCreateOpen(true)}>
          Create cookbook <Icon name="arrow" size={15} />
        </button>
      </div>
      {loading ? (
        <p>Loading cookbooks…</p>
      ) : error && !cookbooks.length ? (
        <p className="form-message" role="alert">
          {error}
        </p>
      ) : cookbooks.length ? (
        <>
          <div className="cookbook-grid">
            {cookbooks.map((book) => (
              <article className="cookbook-card" key={book.id}>
                <span className="notebook-banner__icon">
                  <Icon name="book" size={24} />
                </span>
                <h3>{book.title}</h3>
                {book.description && <p>{book.description}</p>}
                <span className="recipe-card__category">
                  {book.visibility} cookbook
                </span>
                <button
                  className="text-link"
                  type="button"
                  onClick={() => setDetailBook(book)}
                >
                  Open cookbook
                </button>
                {book.owner_id === user?.id && (
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
          {error && (
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
        </>
      ) : (
        <p className="cookbook-empty">
          Your cookbooks will appear here. Create one to start gathering
          recipes.
        </p>
      )}
      {createOpen && (
        <CookbookForm
          onClose={() => setCreateOpen(false)}
          onSaved={(book) => {
            setCookbooks((current) => [book, ...current]);
            setOffset((value) => value + 1);
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
      {detailBook && (
        <CookbookDetail
          book={detailBook}
          onClose={() => setDetailBook(null)}
          onOpenRecipe={onOpenRecipe}
        />
      )}
      {children}
    </section>
  );
}
