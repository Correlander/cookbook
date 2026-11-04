import { useEffect, useRef, useState } from "react";
import type {
  RecipeBookmarkTarget,
  RecipeBookmarkView,
} from "../../apiModels";
import { errorMessage } from "../../apiModels";
import {
  fetchRecipeBookmarkStatus,
  captureRecipeBookmark,
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
  const [capturing, setCapturing] = useState(false);
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
    const needsRemoteEligibility = bookmark?.target_kind === "remote" && bookmark.save_state === "reference" && bookmark.can_capture_snapshot === undefined;
    if (!currentUserID || (!localID && !remoteURI) || (initialStatus !== undefined && retry === 0 && !needsRemoteEligibility) || (bookmark && !needsRemoteEligibility && retry === 0)) {
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
      setSaved(result.saved ? result.bookmark ?? {
        id: result.bookmark_id,
        target_kind: localID ? "local" : "remote",
        target: exactTarget,
        availability: "available",
        created_at: "",
        save_state: "reference",
        can_capture_snapshot: result.can_capture_snapshot,
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
        const result = await saveRecipeBookmark(
          target,
          target.remote_object_uri ? "conditional" : undefined,
        );
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

  async function keepThisVersion(): Promise<void> {
    if (!keyMatches || status !== "known" || !saved || !saved.can_capture_snapshot || saving || capturing) return;
    const operation = ++request.current;
    setCapturing(true);
    setError("");
    try {
      const captured = await captureRecipeBookmark(saved.id);
      if (request.current !== operation) return;
      setSaved(captured);
      setStatus("known");
      onChange?.(captured, false);
      onMutation?.(captured);
    } catch (caught: unknown) {
      if (request.current !== operation) return;
      setError(errorMessage(caught, "Could not save a private copy of this version."));
      setStatus("known");
    } finally {
      if (request.current === operation) setCapturing(false);
    }
  }

  const label = !keyMatches || status === "checking" ? "Checking saved status…" : status === "unknown" ? "Saved status unavailable" : saving ? (saved ? "Removing…" : "Saving…") : libraryContext ? saved ? "Remove bookmark" : "Bookmark" : saved ? "Remove from Library" : "Save to Library";
  const canCapture = saved?.target_kind === "remote" && saved.save_state === "reference" && saved.can_capture_snapshot === true;
  return <div className="library-action-group">
    {saved?.save_state === "snapshot" && !libraryContext && <p className="dialog-note">Private copy saved. It remains fixed to the captured version.</p>}
    {canCapture && <button className="text-link" type="button" disabled={capturing || saving || busy || !keyMatches} onClick={() => void keepThisVersion()}>{capturing ? "Saving private copy…" : "Keep this version"}</button>}
    <BookmarkActionPresentation label={label} disabled={saving || capturing || busy || !keyMatches || status !== "known" || (!saved && !target)} retryDisabled={busy || saving || capturing} error={keyMatches ? error : undefined} onActivate={() => void toggle()} onRetry={keyMatches && status === "unknown" ? () => setRetry((value) => value + 1) : undefined} />
  </div>;
}
