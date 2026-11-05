import { useRef, useState } from "react";
import useModalFocus from "../../useModalFocus.ts";
import type { Course, CourseCreatorAttribution } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import usePublicAuthConfig from "../auth/usePublicAuthConfig.ts";
import CreatorAttributionFields from "../creator/CreatorAttributionFields.tsx";
import PublisherProfileSetupGuidance from "../creator/PublisherProfileSetupGuidance.tsx";

interface CourseFormProps {
  course: Course | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}

export default function CourseForm({ course, onClose, onSaved }: CourseFormProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [title, setTitle] = useState(course?.title || "");
  const [description, setDescription] = useState(course?.description || "");
  const [visibility, setVisibility] = useState<"private" | "public">(
    course?.visibility || "private",
  );
  const [error, setError] = useState("");
  const [publisherProfileRequired, setPublisherProfileRequired] = useState(false);
  const [saving, setSaving] = useState(false);
  const { state: configState } = usePublicAuthConfig();
  const attributionEnabled = !course && configState.status === "ready" && configState.config.course_creator_attribution === true;
  const configChecking = !course && configState.status === "loading";
  const [creatorChoice, setCreatorChoice] = useState<CourseCreatorAttribution["kind"] | "">("");
  const [creatorName, setCreatorName] = useState("");

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!course && attributionEnabled && !creatorChoice) {
      setError("Choose who created this course before saving.");
      return;
    }
    const trimmedCreatorName = creatorName.trim();
    if (attributionEnabled && creatorChoice === "name_claim" &&
        (Array.from(trimmedCreatorName).length < 1 || Array.from(trimmedCreatorName).length > 80 || new TextEncoder().encode(trimmedCreatorName).length > 320)) {
      setError("Enter a creator name of up to 80 characters.");
      return;
    }
    setSaving(true);
    setError("");
    setPublisherProfileRequired(false);
    try {
      const response = await fetch(
        course ? `/api/courses/${course.id}` : "/api/courses",
        {
          method: course ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            title,
            description,
            visibility,
            ...(!course && attributionEnabled && creatorChoice
              ? { creator_attribution: creatorChoice === "name_claim" ? { kind: "name_claim", name: trimmedCreatorName } : { kind: creatorChoice } }
              : {}),
          }),
        },
      );
      if (!response.ok) {
        const body: unknown = await response.json().catch(() => null);
        const code = body && typeof body === "object" && "error" in body &&
          typeof body.error === "string" ? body.error : "";
        if (response.status === 409 && code === "publisher_profile_required") {
          setPublisherProfileRequired(true);
          return;
        }
        throw new Error(
          "Could not save this course. Check instance visibility settings and try again.",
        );
      }
      await onSaved();
    } catch (saveError: unknown) {
      setError(errorMessage(saveError, "Could not save this course."));
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
        ref={dialogRef}
        tabIndex={-1}
        className="signin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="course-form-title"
      >
        <button
          className="icon-button dialog-close"
          type="button"
          aria-label="Close course form"
          onClick={onClose}
        >
          ×
        </button>
        <span className="eyebrow">Courses</span>
        <h2 id="course-form-title">
          {course ? "Edit course" : "Create a course"}
        </h2>
        <form onSubmit={submit}>
          <label htmlFor="course-title">Title</label>
          <input
            id="course-title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            maxLength={200}
            required
          />
          <label htmlFor="course-description">Description</label>
          <textarea
            id="course-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            maxLength={5000}
          />
          <label htmlFor="course-visibility">Visibility</label>
          <select
            id="course-visibility"
            value={visibility}
            onChange={(event) =>
              setVisibility(event.target.value as "private" | "public")
            }
          >
            <option value="private">Private</option>
            <option value="public">Public</option>
          </select>
          <p className="course-intro">
            Public courses can be read anonymously only when public browsing is
            enabled for this instance.
          </p>
          {attributionEnabled && <fieldset className="form-choice-group">
            <CreatorAttributionFields
              resourceKind="course"
              choice={creatorChoice}
              name={creatorName}
              onChoiceChange={(choice) => { setCreatorChoice(choice); setError(""); }}
              onNameChange={setCreatorName}
            />
          </fieldset>}
          {configChecking && <p role="status">Checking creator-credit support…</p>}
          <button
            className="button button--green"
            type="submit"
            disabled={saving || configChecking}
          >
            {saving ? "Saving…" : "Save course"}
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
