import { useEffect, useRef, useState } from "react";
import type { RecipeBookmarkView } from "../../apiModels";
import { fetchRecipeBookmarks } from "./libraryApi.ts";

export interface RecipeBookmarksState {
  items: RecipeBookmarkView[];
  loading: boolean;
  loadingMore: boolean;
  error: string;
  loadMoreError: string;
  hasMore: boolean;
  loadMore: () => Promise<void>;
  update: (
    bookmark: RecipeBookmarkView | null,
    created?: boolean,
    removed?: RecipeBookmarkView,
  ) => void;
}

export default function useRecipeBookmarks(
  userID: string | null,
  enabled = true,
): RecipeBookmarksState {
  const [items, setItems] = useState<RecipeBookmarkView[]>([]);
  const [offset, setOffset] = useState(0);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [loadMoreError, setLoadMoreError] = useState("");
  const requestID = useRef(0);
  const userIDRef = useRef(userID);
  const itemsRef = useRef(items);
  itemsRef.current = items;
  userIDRef.current = userID;

  useEffect(() => {
    const currentRequest = ++requestID.current;
    if (!userID || !enabled) {
      setItems([]);
      setOffset(0);
      setTotal(0);
      setLoading(false);
      setLoadingMore(false);
      setError("");
      setLoadMoreError("");
      return;
    }
    let active = true;
    setLoading(true);
    setLoadingMore(false);
    setError("");
    setLoadMoreError("");
    setItems([]);
    setOffset(0);
    setTotal(0);
    fetchRecipeBookmarks(0)
      .then((page) => {
        if (!active || requestID.current !== currentRequest) return;
        setItems(page.items);
        setOffset(page.offset + page.items.length);
        setTotal(page.total);
      })
      .catch((caught: unknown) => {
        if (!active || requestID.current !== currentRequest) return;
        setError(
          caught instanceof Error ? caught.message : "Could not load Saved recipes.",
        );
      })
      .finally(() => {
        if (active && requestID.current === currentRequest) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [enabled, userID]);

  async function loadMore(): Promise<void> {
    if (!userID || !enabled || loadingMore || offset >= total) return;
    const currentRequest = requestID.current;
    const currentUser = userID;
    setLoadingMore(true);
    setLoadMoreError("");
    try {
      const page = await fetchRecipeBookmarks(offset);
      if (
        requestID.current !== currentRequest ||
        userIDRef.current !== currentUser
      )
        return;
      setItems((current) => {
        const known = new Set(current.map((item) => item.id));
        return [...current, ...page.items.filter((item) => !known.has(item.id))];
      });
      setOffset(page.offset + page.items.length);
      setTotal(page.total);
    } catch (caught: unknown) {
      if (
        requestID.current === currentRequest &&
        userIDRef.current === currentUser
      )
        setLoadMoreError(
          caught instanceof Error
            ? caught.message
            : "Could not load more Saved recipes.",
        );
    } finally {
      if (
        requestID.current === currentRequest &&
        userIDRef.current === currentUser
      )
        setLoadingMore(false);
    }
  }

  function update(
    bookmark: RecipeBookmarkView | null,
    created = false,
    removed?: RecipeBookmarkView,
  ): void {
    if (bookmark) {
      const alreadyLoaded = itemsRef.current.some(
        (item) => item.id === bookmark.id,
      );
      setItems((current) =>
        [...current.filter((item) => item.id !== bookmark.id), bookmark].sort(
          (left, right) => right.created_at.localeCompare(left.created_at),
        ),
      );
      if (created) setTotal((current) => current + 1);
      if (created && !alreadyLoaded) setOffset((current) => current + 1);
      return;
    }
    if (!removed) return;
    const wasLoaded = itemsRef.current.some((item) => item.id === removed.id);
    if (!wasLoaded) return;
    setItems((current) => current.filter((item) => item.id !== removed.id));
    setTotal((current) => Math.max(0, current - 1));
    setOffset((current) => Math.max(0, current - 1));
  }

  return {
    items,
    loading,
    loadingMore,
    error,
    loadMoreError,
    hasMore: offset < total,
    loadMore,
    update,
  };
}
