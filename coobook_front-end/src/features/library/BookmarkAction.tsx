import { useEffect, useRef, useState } from "react";
import type {
  RecipeBookmarkTarget,
  RecipeBookmarkView,
} from "../../apiModels";
import { errorMessage } from "../../apiModels";
import {
  fetchRecipeBookmarkStatus,
  removeRecipeBookmark,
  saveRecipeBookmark,
} from "./libraryApi.ts";

interface BookmarkActionProps {
  currentUserID: string | null;
  target: RecipeBookmarkTarget | null;
  bookmark?: RecipeBookmarkView;
  onChange?: (
    bookmark: RecipeBookmarkView | null,
    created?: boolean,
    removed?: RecipeBookmarkView,
  ) => void;
  busy?: boolean;
  initialStatus?: "saved" | "unsaved";
  onMutation?: (bookmark: RecipeBookmarkView | null) => void;
  onMutationUncertain?: () => void;
  libraryContext?: boolean;
}

export function BookmarkActionPresentation({ label, accessibleLabel, disabled, retryDisabled = disabled, error, onActivate, onRetry }: { label: string; accessibleLabel?: string; disabled: boolean; retryDisabled?: boolean; error?: string; onActivate: () => void; onRetry?: () => void }) {
  return <div className="library-action"><button className="text-link" type="button" aria-label={accessibleLabel} disabled={disabled} onClick={onActivate}>{label}</button>{error && <span className="form-message" role="alert">{error}</span>}{error && onRetry && <button className="text-link" type="button" disabled={retryDisabled} onClick={onRetry}>Retry saved status</button>}</div>;
}

export default function BookmarkAction({
  currentUserID,
  target,
  bookmark,
  onChange,
  busy = false,
  initialStatus,
  onMutation,
  onMutationUncertain,
  libraryContext = false,
}: BookmarkActionProps) {
  const [saved, setSaved] = useState(bookmark ?? null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState<"checking" | "unknown" | "known">(
    bookmark ? "known" : "checking",
  );
  const [loadedKey, setLoadedKey] = useState("");
  const [retry, setRetry] = useState(0);
  const request = useRef(0);

  const localID = target?.local_recipe_id ?? "";
  const remoteURI = target?.remote_object_uri ?? "";
  const currentKey = `${currentUserID ?? "anonymous"}|${localID}|${remoteURI}`;
  const keyMatches = loadedKey === currentKey;

  useEffect(() => {
    const currentRequest = ++request.current;
    setLoadedKey(currentKey);
    if (!currentUserID || (!localID && !remoteURI) || (initialStatus !== undefined && retry === 0) || (bookmark && retry === 0)) {
      setSaved(bookmark ?? null);
      setStatus("known");
      setError("");
      return () => { request.current += 1; };
    }
    let active = true;
    setSaved(null);
    setStatus("checking");
    setError("");
    const exactTarget = localID
      ? { local_recipe_id: localID }
      : { remote_object_uri: remoteURI };
    void fetchRecipeBookmarkStatus(exactTarget).then((result) => {
      if (!active || request.current !== currentRequest) return;
      setSaved(result.saved ? {
        id: result.bookmark_id,
        target_kind: localID ? "local" : "remote",
        target: exactTarget,
        availability: "available",
        created_at: "",
      } : null);
      setStatus("known");
    }).catch(() => {
      if (!active || request.current !== currentRequest) return;
      setStatus("unknown");
      setError("Could not check saved status.");
    });
    return () => {
      active = false;
      request.current += 1;
    };
  }, [bookmark, currentKey, currentUserID, initialStatus, localID, remoteURI, retry]);

  if (!currentUserID)
    return (
      <a className="text-link" href="/?signin=1">
        Save to Library
      </a>
    );

  async function toggle(): Promise<void> {
    if (!keyMatches || status !== "known") return;
    const operation = ++request.current;
    setSaving(true);
    setError("");
    try {
      if (saved) {
        await removeRecipeBookmark(saved.id);
        if (request.current !== operation) return;
        setSaved(null);
        setStatus("known");
        onChange?.(null, false, saved);
        onMutation?.(null);
      } else {
        if (!target) return;
        const result = await saveRecipeBookmark(target);
        if (request.current !== operation) return;
        setSaved(result.bookmark);
        setStatus("known");
        onChange?.(result.bookmark, result.created);
        onMutation?.(result.bookmark);
      }
    } catch (caught: unknown) {
      if (request.current !== operation) return;
      setError(errorMessage(caught, "Could not update your Library."));
      setStatus("unknown");
      setRetry((value) => value + 1);
      onMutationUncertain?.();
    } finally {
      if (request.current === operation) setSaving(false);
    }
  }

  const label = !keyMatches || status === "checking" ? "Checking saved status…" : status === "unknown" ? "Saved status unavailable" : saving ? (saved ? "Removing…" : "Saving…") : libraryContext ? saved ? "Remove bookmark" : "Bookmark" : saved ? "Remove from Library" : "Save to Library";
  return <BookmarkActionPresentation label={label} disabled={saving || busy || !keyMatches || status !== "known" || (!saved && !target)} retryDisabled={busy || saving} error={keyMatches ? error : undefined} onActivate={() => void toggle()} onRetry={keyMatches && status === "unknown" ? () => setRetry((value) => value + 1) : undefined} />;
}
