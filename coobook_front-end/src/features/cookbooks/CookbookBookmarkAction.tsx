import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "../../apiModels";
import {
  fetchCookbookBookmarkStatus,
  removeCookbookBookmark,
  saveCookbookBookmark,
} from "../library/libraryApi.ts";

interface CookbookBookmarkActionProps {
  cookbookID: string;
  title: string;
  currentUserID: string | null;
  onMutation?: (saved: boolean) => void;
  onMutationUncertain?: () => void;
  libraryContext?: boolean;
  initialBookmarkID?: string | null;
}

type SavedState =
  | { kind: "checking" }
  | { kind: "unknown" }
  | { kind: "unsaved" }
  | { kind: "saved"; bookmarkID: string };

export default function CookbookBookmarkAction(
  props: CookbookBookmarkActionProps,
) {
  if (!props.currentUserID) {
    return (
      <a className="text-link" href="/?signin=1">
        Sign in to save this cookbook
      </a>
    );
  }

  return (
    <CookbookBookmarkActionForActor
      key={`${props.currentUserID}:${props.cookbookID}`}
      {...props}
      currentUserID={props.currentUserID}
    />
  );
}

function CookbookBookmarkActionForActor({
  cookbookID,
  title,
  currentUserID,
  onMutation,
  onMutationUncertain,
  libraryContext = false,
  initialBookmarkID,
}: CookbookBookmarkActionProps & { currentUserID: string }) {
  const [savedState, setSavedState] = useState<SavedState>({
    kind: "checking",
  });
  const [busy, setBusy] = useState(false);
  const [checkingAfterMutation, setCheckingAfterMutation] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const operation = useRef(0);

  const refreshStatus = useCallback(
    async (afterMutation = false): Promise<void> => {
      const request = ++operation.current;
      setSavedState({ kind: "checking" });
      setCheckingAfterMutation(afterMutation);
      setError("");
      setNotice("");
      try {
        const result = await fetchCookbookBookmarkStatus(cookbookID);
        if (operation.current !== request) return;
        setSavedState(
          result.saved
            ? { kind: "saved", bookmarkID: result.bookmark_id }
            : { kind: "unsaved" },
        );
        if (afterMutation) setNotice("Current saved status refreshed.");
      } catch (caught: unknown) {
        if (operation.current !== request) return;
        setSavedState({ kind: "unknown" });
        setError(errorMessage(caught, "Could not check saved status."));
      } finally {
        if (operation.current === request) setCheckingAfterMutation(false);
      }
    },
    [cookbookID],
  );

  useEffect(() => {
    if (initialBookmarkID !== undefined) {
      operation.current += 1;
      setSavedState(initialBookmarkID ? { kind: "saved", bookmarkID: initialBookmarkID } : { kind: "unsaved" });
      setError("");
      return;
    }
    void refreshStatus();
    return () => {
      operation.current += 1;
    };
  }, [currentUserID, initialBookmarkID, refreshStatus]);

  async function save(): Promise<void> {
    if (busy) return;
    const request = ++operation.current;
    setBusy(true);
    setError("");
    setNotice("");
    setCheckingAfterMutation(false);
    try {
      const result = await saveCookbookBookmark(cookbookID);
      if (operation.current !== request) return;
      setSavedState({ kind: "saved", bookmarkID: result.bookmark.id });
      setNotice(
        result.created
          ? "Cookbook saved to your Library."
          : "Cookbook is already saved in your Library.",
      );
      onMutation?.(true);
    } catch (caught: unknown) {
      if (operation.current !== request) return;
      setSavedState({ kind: "unknown" });
      setError(errorMessage(caught, "Could not update your Library."));
      onMutationUncertain?.();
      setBusy(false);
      void refreshStatus(true);
      return;
    } finally {
      if (operation.current === request) setBusy(false);
    }
  }

  async function remove(): Promise<void> {
    if (busy || savedState.kind !== "saved") return;
    const bookmarkID = savedState.bookmarkID;
    const request = ++operation.current;
    setBusy(true);
    setError("");
    setNotice("");
    setCheckingAfterMutation(false);
    try {
      await removeCookbookBookmark(bookmarkID);
      if (operation.current !== request) return;
      setSavedState({ kind: "unsaved" });
      onMutation?.(false);
      setNotice("Cookbook removed from your Library.");
    } catch (caught: unknown) {
      if (operation.current !== request) return;
      setSavedState({ kind: "unknown" });
      setError(errorMessage(caught, "Could not update your Library."));
      onMutationUncertain?.();
      setBusy(false);
      void refreshStatus(true);
      return;
    } finally {
      if (operation.current === request) setBusy(false);
    }
  }

  const isSaved = savedState.kind === "saved";
  const mayActWhileChecking =
    savedState.kind === "checking" && !checkingAfterMutation;
  const disabled =
    busy || (savedState.kind === "checking" && !mayActWhileChecking);

  return (
    <div className="library-action">
      <button
        className="text-link"
        type="button"
        disabled={disabled}
        onClick={() => void (isSaved ? remove() : save())}
      >
        {busy
          ? isSaved
            ? "Removing…"
            : "Saving…"
          : libraryContext
            ? isSaved ? "Remove bookmark" : "Bookmark"
            : isSaved
              ? `Remove ${title} from Library`
              : `Save ${title} to Library`}
      </button>
      {savedState.kind === "checking" && (
        <p role="status">Checking saved status…</p>
      )}
      {savedState.kind === "unknown" && !error && (
        <p role="status">Saved status is unknown.</p>
      )}
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {savedState.kind === "unknown" && error && (
        <button
          className="text-link"
          type="button"
          onClick={() => void refreshStatus()}
          disabled={busy}
        >
          Retry saved status check
        </button>
      )}
    </div>
  );
}
