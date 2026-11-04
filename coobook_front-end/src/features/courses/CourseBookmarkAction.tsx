import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "../../apiModels";
import { BookmarkActionPresentation } from "../library/BookmarkAction.tsx";
import { fetchCourseBookmarkStatus, removeCourseBookmark, saveCourseBookmark } from "../library/libraryApi.ts";

type SavedState = { kind: "checking" } | { kind: "unknown" } | { kind: "unsaved" } | { kind: "saved"; bookmarkID: string };

export default function CourseBookmarkAction({ courseID, title, currentUserID, initialBookmarkID, onMutation, onMutationUncertain }: { courseID: string; title: string; currentUserID: string | null; initialBookmarkID?: string | null; onMutation?: (saved: boolean) => void; onMutationUncertain?: () => void }) {
  if (!currentUserID) return <a className="text-link" href="/?signin=1">Sign in to save this course</a>;
  return <CourseBookmarkActionForActor key={`${currentUserID}:${courseID}`} courseID={courseID} title={title} currentUserID={currentUserID} initialBookmarkID={initialBookmarkID} onMutation={onMutation} onMutationUncertain={onMutationUncertain} />;
}

function CourseBookmarkActionForActor({ courseID, title, currentUserID, initialBookmarkID, onMutation, onMutationUncertain }: { courseID: string; title: string; currentUserID: string; initialBookmarkID?: string | null; onMutation?: (saved: boolean) => void; onMutationUncertain?: () => void }) {
  const [saved, setSaved] = useState<SavedState>({ kind: "checking" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const operation = useRef(0);
  const refresh = useCallback(async () => {
    const id = ++operation.current;
    setSaved({ kind: "checking" }); setError("");
    try { const result = await fetchCourseBookmarkStatus(courseID); if (operation.current === id) setSaved(result.saved ? { kind: "saved", bookmarkID: result.bookmark_id } : { kind: "unsaved" }); }
    catch (caught: unknown) { if (operation.current === id) { setSaved({ kind: "unknown" }); setError(errorMessage(caught, "Could not check saved status.")); } }
  }, [courseID]);
  useEffect(() => {
    if (initialBookmarkID !== undefined) { operation.current += 1; setSaved(initialBookmarkID ? { kind: "saved", bookmarkID: initialBookmarkID } : { kind: "unsaved" }); setError(""); return; }
    void refresh(); return () => { operation.current += 1; };
  }, [currentUserID, initialBookmarkID, refresh]);
  async function toggle() {
    if (busy || saved.kind === "checking" || saved.kind === "unknown") return;
    const request = ++operation.current; setBusy(true); setError("");
    try {
      if (saved.kind === "saved") { await removeCourseBookmark(saved.bookmarkID); if (operation.current !== request) return; setSaved({ kind: "unsaved" }); onMutation?.(false); }
      else { const result = await saveCourseBookmark(courseID); if (operation.current !== request) return; setSaved({ kind: "saved", bookmarkID: result.bookmarkID }); onMutation?.(true); }
    } catch (caught: unknown) { if (operation.current === request) { setSaved({ kind: "unknown" }); setError(errorMessage(caught, "Could not update your Library.")); onMutationUncertain?.(); } }
    finally { if (operation.current === request) setBusy(false); }
  }
  const label = busy ? "Updating…" : saved.kind === "saved" ? "Remove bookmark" : saved.kind === "checking" ? "Checking saved status…" : saved.kind === "unknown" ? "Saved status unavailable" : "Bookmark";
  const accessibleLabel = busy ? `Updating ${title} bookmark` : saved.kind === "saved" ? `Remove ${title} bookmark` : saved.kind === "checking" ? `Checking ${title} saved status` : saved.kind === "unknown" ? `${title} saved status unavailable` : `Bookmark ${title}`;
  return <BookmarkActionPresentation label={label} accessibleLabel={accessibleLabel} disabled={busy || saved.kind === "checking" || saved.kind === "unknown"} retryDisabled={busy} error={error} onActivate={() => void toggle()} onRetry={saved.kind === "unknown" ? () => void refresh() : undefined} />;
}
