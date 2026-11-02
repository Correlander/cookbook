import { useEffect, useRef, useState } from "react";
import useModalFocus from "./useModalFocus.ts";
import type { Course, PublicRecipe } from "./apiModels";
import { errorMessage } from "./apiModels";
import CourseDetail from "./features/courses/CourseDetail.tsx";

interface CoursesProps {
  onOpenRecipe?: (recipe: PublicRecipe) => void;
}
type EditingCourse = Course | "new";

export default function Courses({ onOpenRecipe }: CoursesProps) {
  const [courses, setCourses] = useState<Course[]>([]);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<EditingCourse | null>(null);
  const [openCourse, setOpenCourse] = useState<Course | null>(null);

  async function reload() {
    const response = await fetch("/api/courses?limit=100");
    if (response.status === 404) {
      setEnabled(false);
      return;
    }
    if (!response.ok) throw new Error("Could not load courses.");
    setCourses((await response.json()) as Course[]);
    setEnabled(true);
  }

  useEffect(() => {
    let active = true;
    fetch("/api/courses?limit=100")
      .then(async (response) => {
        if (response.status === 404) {
          if (active) setEnabled(false);
          return null;
        }
        if (!response.ok) throw new Error("Could not load courses.");
        return response.json() as Promise<Course[]>;
      })
      .then((items) => {
        if (active && items) {
          setCourses(items);
          setEnabled(true);
        }
      })
      .catch((loadError: unknown) => {
        if (active) {
          setEnabled(true);
          setError(errorMessage(loadError, "Could not load courses."));
        }
      });
    return () => {
      active = false;
    };
  }, []);

  if (enabled === false || enabled === null) return null;
  return (
    <section
      className="cookbook-dashboard course-list"
      aria-labelledby="courses-heading"
    >
      <div className="section-heading">
        <div>
          <span className="eyebrow">Learning together</span>
          <h2 id="courses-heading">Courses</h2>
        </div>
        <button
          className="text-link"
          type="button"
          onClick={() => setEditing("new")}
        >
          Create course
        </button>
      </div>
      <p className="course-intro">
        Courses are authored lessons. Everyday recipe steps stay with their
        recipes.
      </p>
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {courses.length ? (
        <div className="cookbook-grid">
          {courses.map((course) => (
            <article className="cookbook-card" key={course.id}>
              <span className="recipe-card__category">
                {course.visibility} course · {course.lesson_count} lessons
              </span>
              <h3>{course.title}</h3>
              {course.description && <p>{course.description}</p>}
              <p className="course-owner">By {course.owner_name}</p>
              <button
                type="button"
                className="text-link"
                onClick={() => setOpenCourse(course)}
              >
                Open course
              </button>
              {course.owner_id && (
                <button
                  type="button"
                  className="text-link"
                  onClick={() => setEditing(course)}
                >
                  Edit course
                </button>
              )}
            </article>
          ))}
        </div>
      ) : (
        <p className="cookbook-empty">
          No courses yet. Create one for a set of lessons.
        </p>
      )}
      {editing && (
        <CourseForm
          course={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await reload();
          }}
        />
      )}
      {openCourse && (
        <CourseDetail
          course={openCourse}
          onClose={() => setOpenCourse(null)}
          onEdit={() => {
            setEditing(openCourse);
            setOpenCourse(null);
          }}
          onOpenRecipe={onOpenRecipe}
          onChanged={reload}
        />
      )}
    </section>
  );
}

interface CourseFormProps {
  course: Course | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}

function CourseForm({ course, onClose, onSaved }: CourseFormProps) {
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
