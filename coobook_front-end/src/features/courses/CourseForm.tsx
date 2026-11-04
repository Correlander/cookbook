import { useRef, useState } from "react";
import useModalFocus from "../../useModalFocus.ts";
import type { Course } from "../../apiModels";
import { errorMessage } from "../../apiModels";

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
  const [saving, setSaving] = useState(false);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      const response = await fetch(
        course ? `/api/courses/${course.id}` : "/api/courses",
        {
          method: course ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ title, description, visibility }),
        },
      );
      if (!response.ok)
        throw new Error(
          "Could not save this course. Check instance visibility settings and try again.",
        );
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
          <button
            className="button button--green"
            type="submit"
            disabled={saving}
          >
            {saving ? "Saving…" : "Save course"}
          </button>
        </form>
        {error && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
      </section>
    </div>
  );
}
