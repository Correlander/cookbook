import { useCallback, useEffect, useRef, useState } from "react";
import { fetchRecipePage, RECIPE_PAGE_SIZE } from "../recipes/recipeFeedApi.ts";

export interface MenuRecipeOption {
  id: string;
  title: string;
}

interface MenuRecipePickerProps {
  actorID: string;
  onSelect: (recipe: MenuRecipeOption) => void;
}

interface PickerPageState {
  key: string;
  items: MenuRecipeOption[];
  loadedCount: number;
  hasMore: boolean;
  loading: boolean;
  loadingMore: boolean;
  error: string;
  loadMoreError: string;
}

function emptyPageState(key: string, loading: boolean): PickerPageState {
  return {
    key,
    items: [],
    loadedCount: 0,
    hasMore: false,
    loading,
    loadingMore: false,
    error: "",
    loadMoreError: "",
  };
}

export default function MenuRecipePicker({ actorID, onSelect }: MenuRecipePickerProps) {
  const [query, setQuery] = useState("");
  const [pageState, setPageState] = useState(() => emptyPageState("", false));
  const [retryCount, setRetryCount] = useState(0);
  const generationRef = useRef(0);
  const normalizedQuery = query.trim();
  const requestKey = `${actorID}\u0000${normalizedQuery}`;
  const requestKeyRef = useRef(requestKey);
  requestKeyRef.current = requestKey;

  useEffect(() => {
    const generation = ++generationRef.current;
    let active = true;
    const isCurrent = () =>
      active && generationRef.current === generation && requestKeyRef.current === requestKey;

    if (!actorID) {
      setPageState(emptyPageState(requestKey, false));
      return () => { active = false; };
    }

    setPageState(emptyPageState(requestKey, true));
    void fetchRecipePage({ signedIn: true, query: normalizedQuery, offset: 0 })
      .then((value: unknown) => {
        const items = parseLocalRecipeOptions(value);
        if (!isCurrent()) return;
        setPageState({
          ...emptyPageState(requestKey, false),
          items,
          loadedCount: items.length,
          hasMore: items.length === RECIPE_PAGE_SIZE,
        });
      })
      .catch((loadError: unknown) => {
        if (!isCurrent()) return;
        setPageState({
          ...emptyPageState(requestKey, false),
          error: loadError instanceof Error ? loadError.message : "Could not load local recipes.",
        });
      })
      .finally(() => {
        if (isCurrent()) {
          setPageState((current) => current.key === requestKey ? { ...current, loading: false } : current);
        }
      });

    return () => { active = false; };
  }, [actorID, normalizedQuery, requestKey, retryCount]);

  const visiblePage = pageState.key === requestKey ? pageState : emptyPageState(requestKey, Boolean(actorID));

  const loadMore = useCallback(async () => {
    if (!actorID || !visiblePage.hasMore || visiblePage.loadingMore) return;
    const generation = generationRef.current;
    const key = requestKey;
    const isCurrent = () => generationRef.current === generation && requestKeyRef.current === key;
    setPageState((current) => current.key === key
      ? { ...current, loadingMore: true, loadMoreError: "" }
      : current);
    try {
      const response: unknown = await fetchRecipePage({
        signedIn: true,
        query: normalizedQuery,
        offset: visiblePage.loadedCount,
      });
      const nextItems = parseLocalRecipeOptions(response);
      if (!isCurrent()) return;
      setPageState((current) => {
        if (current.key !== key) return current;
        const knownIDs = new Set(current.items.map((recipe) => recipe.id));
        return {
          ...current,
          items: [...current.items, ...nextItems.filter((recipe) => !knownIDs.has(recipe.id))],
          loadedCount: current.loadedCount + nextItems.length,
          hasMore: nextItems.length === RECIPE_PAGE_SIZE,
          loadingMore: false,
        };
      });
    } catch (loadError: unknown) {
      if (!isCurrent()) return;
      setPageState((current) => current.key === key
        ? {
            ...current,
            loadingMore: false,
            loadMoreError: loadError instanceof Error ? loadError.message : "Could not load more local recipes.",
          }
        : current);
    }
  }, [actorID, normalizedQuery, requestKey, visiblePage.hasMore, visiblePage.loadingMore, visiblePage.loadedCount]);

  if (!actorID) {
    return <p role="status">Sign in to choose a local recipe.</p>;
  }

  return (
    <section aria-labelledby="menu-recipe-picker-title">
      <h3 id="menu-recipe-picker-title">Choose a local recipe</h3>
      <label htmlFor="menu-recipe-search">Search local recipes</label>
      <input
        id="menu-recipe-search"
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        autoComplete="off"
      />
      {visiblePage.loading && <p role="status">Loading local recipes…</p>}
      {visiblePage.error && <>
        <p role="alert">{visiblePage.error}</p>
        <button className="text-link" type="button" onClick={() => setRetryCount((count) => count + 1)}>Try recipe search again</button>
      </>}
      {!visiblePage.loading && !visiblePage.error && visiblePage.items.length === 0 && <p>No local recipes found.</p>}
      {visiblePage.items.length > 0 && <ul aria-label="Local recipe results">
        {visiblePage.items.map((recipe) => (
          <li key={recipe.id}>
            <button className="text-link" type="button" aria-label={`Add ${recipe.title}`} onClick={() => onSelect(recipe)}>
              {recipe.title}
            </button>
          </li>
        ))}
      </ul>}
      {visiblePage.loadMoreError && <p role="alert">{visiblePage.loadMoreError}</p>}
      {visiblePage.hasMore && !visiblePage.loading && <button className="button" type="button" onClick={() => void loadMore()} disabled={visiblePage.loadingMore}>
        {visiblePage.loadingMore ? "Loading more recipes…" : "Load more local recipes"}
      </button>}
    </section>
  );
}

function parseLocalRecipeOptions(value: unknown): MenuRecipeOption[] {
  if (!Array.isArray(value) || value.length > RECIPE_PAGE_SIZE) throw new Error("Could not load local recipes.");
  const options: MenuRecipeOption[] = [];
  const knownIDs = new Set<string>();
  for (const item of value as unknown[]) {
    if (typeof item !== "object" || item === null || Array.isArray(item)) throw new Error("Could not load local recipes.");
    const record = item as Record<string, unknown>;
    if (typeof record.id !== "string" || !/^[a-f0-9]{32}$/.test(record.id) ||
        typeof record.title !== "string" || !record.title.trim() || record.title.includes("\0") || knownIDs.has(record.id)) {
      throw new Error("Could not load local recipes.");
    }
    knownIDs.add(record.id);
    options.push({ id: record.id, title: record.title });
  }
  return options;
}
