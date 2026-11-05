import { useRef, useState } from "react";
import type { FormEvent } from "react";
import type { Cookbook } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import useModalFocus from "../../useModalFocus.ts";
import Icon from "../recipes/RecipeIcon.tsx";
import PublisherProfileSetupGuidance from "../creator/PublisherProfileSetupGuidance.tsx";
import { CookbookApiError, saveCookbook } from "./cookbookApi.ts";

interface CookbookFormProps {
  book?: Cookbook;
  onClose: () => void;
  onSaved: (book: Cookbook) => void;
}

export default function CookbookForm({ book, onClose, onSaved }: CookbookFormProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [title, setTitle] = useState(book?.title || ""),
    [description, setDescription] = useState(book?.description || ""),
    [visibility, setVisibility] = useState<Cookbook["visibility"]>(
      book?.visibility || "private",
    ),
    [error, setError] = useState(""),
    [publisherProfileRequired, setPublisherProfileRequired] = useState(false),
    [saving, setSaving] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setPublisherProfileRequired(false);
    setSaving(true);
    try {
      onSaved(await saveCookbook(book, { title, description, visibility }));
    } catch (err: unknown) {
      if (err instanceof CookbookApiError && err.status === 409 && err.code === "publisher_profile_required")
        setPublisherProfileRequired(true);
      else setError(errorMessage(err, "Could not save this cookbook. Try again."));
    } finally {
      setSaving(false);
    }
  }
  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        className="signin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="cookbook-form-title"
        ref={dialogRef}
        tabIndex={-1}
      >
        <button
          className="icon-button dialog-close"
          aria-label="Close"
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
        <span className="eyebrow">Your collection</span>
        <h2 id="cookbook-form-title">
          {book ? "Edit cookbook" : "Create a cookbook"}
        </h2>
        <p>
          {book
            ? "Update the title, description, or visibility."
            : "New cookbooks are private to you."}
        </p>
        <form onSubmit={submit}>
          <label htmlFor="cookbook-title">Title</label>
          <input
            id="cookbook-title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            maxLength={200}
            required
          />
          <label htmlFor="cookbook-description">Description</label>
          <textarea
            id="cookbook-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            maxLength={5000}
          />
          {book && (
            <>
              <label htmlFor="cookbook-visibility">Visibility</label>
              <select
                id="cookbook-visibility"
                value={visibility}
                onChange={(event) =>
                  setVisibility(event.target.value as Cookbook["visibility"])
                }
              >
                <option value="private">Private</option>
                <option value="unlisted">Unlisted</option>
                <option value="public">Public</option>
              </select>
            </>
          )}
          <button
            className="button button--green"
            type="submit"
            disabled={saving}
          >
            {saving
              ? "Saving…"
              : book
                ? "Save changes"
                : "Create private cookbook"}
          </button>
        </form>
        {publisherProfileRequired ? (
          <p className="form-message" role="alert">
            <PublisherProfileSetupGuidance />
          </p>
        ) : error && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
      </section>
    </div>
  );
}
