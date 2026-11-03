import { useEffect, useState } from "react";
import type {
  RecipeBookmarkTarget,
  RecipeBookmarkView,
} from "../../apiModels";
import { errorMessage } from "../../apiModels";
import { removeRecipeBookmark, saveRecipeBookmark } from "./libraryApi.ts";

interface BookmarkActionProps {
  currentUserID: string | null;
  target: RecipeBookmarkTarget | null;
  bookmark?: RecipeBookmarkView;
  onChange?: (
    bookmark: RecipeBookmarkView | null,
    created?: boolean,
    removed?: RecipeBookmarkView,
  ) => void;
}

export default function BookmarkAction({
  currentUserID,
  target,
  bookmark,
  onChange,
}: BookmarkActionProps) {
  const [saved, setSaved] = useState(bookmark ?? null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    setSaved(bookmark ?? null);
    setError("");
  }, [bookmark]);

  if (!currentUserID)
    return (
      <a className="text-link" href="/?signin=1">
        Save to Library
      </a>
    );

  async function toggle(): Promise<void> {
    setSaving(true);
    setError("");
    try {
      if (saved) {
        await removeRecipeBookmark(saved.id);
        setSaved(null);
        onChange?.(null, false, saved);
      } else {
        if (!target) return;
        const result = await saveRecipeBookmark(target);
        setSaved(result.bookmark);
        onChange?.(result.bookmark, result.created);
      }
    } catch (caught: unknown) {
      setError(errorMessage(caught, "Could not update your Library."));
    } finally {
      setSaving(false);
    }
  }

  return (
    <span className="library-action">
      <button
        className="text-link"
        type="button"
        disabled={saving || (!saved && !target)}
        onClick={() => void toggle()}
      >
        {saving
          ? saved
            ? "Removing…"
            : "Saving…"
          : saved
            ? "Remove from Library"
            : "Save to Library"}
      </button>
      {error && (
        <span className="form-message" role="alert">
          {error}
        </span>
      )}
    </span>
  );
}
