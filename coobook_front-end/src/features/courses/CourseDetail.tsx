import { useEffect, useRef, useState } from "react";
import useModalFocus from "../../useModalFocus.ts";
import { moveStep } from "../../steps.ts";
import type { Course, CourseLesson, PublicRecipe } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import CourseBookmarkAction from "./CourseBookmarkAction.tsx";
import CreatorCredits from "../creator/CreatorCredits.tsx";

interface CourseDetailProps {
  course: Course;
  onClose: () => void;
  onEdit: () => void;
  onOpenRecipe?: (recipe: PublicRecipe) => void;
  onChanged: () => Promise<void>;
  mode?: "modal" | "page";
  currentUserID?: string | null;
}

export default function CourseDetail({
  course,
  onClose,
  onEdit,
  onOpenRecipe,
  onChanged,
  mode = "modal",
  currentUserID = null,
}: CourseDetailProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose, mode === "modal");
  const [lessons, setLessons] = useState<CourseLesson[]>([]);
  const [error, setError] = useState("");
  const [lessonOpen, setLessonOpen] = useState<false | true | CourseLesson>(
    false,
  );
  const owner = Boolean(course.owner_id);
  const Heading = mode === "modal" ? "h2" : "h1";
  const LessonHeading = mode === "modal" ? "h3" : "h2";
  const lessonsEndpoint = owner
    ? `/api/courses/${course.id}/lessons`
    : `/api/public/courses/${course.id}/lessons`;

  async function reload() {
    const response = await fetch(lessonsEndpoint);
    if (!response.ok) throw new Error("Could not load course lessons.");
    setLessons((await response.json()) as CourseLesson[]);
  }
  useEffect(() => {
    let active = true;
    fetch(lessonsEndpoint)
      .then(async (response) => {
        if (!response.ok) throw new Error("Could not load course lessons.");
        return response.json() as Promise<CourseLesson[]>;
      })
      .then((items) => {
        if (active) setLessons(items);
      })
      .catch((loadError: unknown) => {
        if (active)
          setError(errorMessage(loadError, "Could not load course lessons."));
      });
    return () => {
      active = false;
    };
  }, [lessonsEndpoint]);

  async function removeLesson(lesson: CourseLesson) {
    if (!window.confirm(`Remove “${lesson.title}” from this course?`)) return;
    const response = await fetch(
      `/api/courses/${course.id}/lessons/${lesson.id}`,
      { method: "DELETE" },
    );
    if (!response.ok) {
      setError("Could not remove this lesson.");
      return;
    }
    await reload();
    await onChanged();
  }

  async function moveLesson(index: number, offset: number) {
    const ordered = moveStep(lessons, index, offset);
    if (ordered === lessons) return;
    try {
      for (let position = 0; position < ordered.length; position += 1) {
        const response = await fetch(
          `/api/courses/${course.id}/lessons/${ordered[position].id}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ position }),
          },
        );
        if (!response.ok) throw new Error("Could not reorder these lessons.");
      }
      setLessons(ordered.map((lesson, position) => ({ ...lesson, position })));
      await onChanged();
    } catch (moveError: unknown) {
      setError(errorMessage(moveError, "Could not reorder these lessons."));
      await reload();
    }
  }

  const content = (
    <>
        <span className="eyebrow">{course.visibility} course · Owner: {course.owner_name}</span>
        <Heading id="course-title-heading">{course.title}</Heading>
        {course.description && <p>{course.description}</p>}
        {course.creator_name && <p>Unverified creator name: {course.creator_name}</p>}
        <CreatorCredits attributions={course.creator_attributions} resourceKind="course" currentUserID={currentUserID} />
        {currentUserID && <CourseBookmarkAction courseID={course.id} title={course.title} currentUserID={currentUserID} />}
        <a className="text-link" href={`/course?id=${encodeURIComponent(course.id)}`}>Open course page</a>
        {owner && mode === "modal" && <div className="course-actions"><button className="text-link" type="button" onClick={onEdit}>Edit course</button><button className="text-link" type="button" onClick={() => setLessonOpen(true)}>Add lesson</button><button className="text-link" type="button" onClick={async () => { if (!window.confirm(`Delete “${course.title}” and all its lessons?`)) return; const response = await fetch(`/api/courses/${course.id}`, { method: "DELETE" }); if (!response.ok) { setError("Could not delete this course."); return; } onClose(); await onChanged(); }}>Delete course</button></div>}
        {error && <p className="form-message" role="alert">{error}</p>}
        {lessons.length ? <ol className="course-lessons">{lessons.map((lesson, index) => <li key={lesson.id}><span className="eyebrow">Lesson {index + 1}</span><LessonHeading>{lesson.title}</LessonHeading>{lesson.content && <p>{lesson.content}</p>}{lesson.recipe_id && onOpenRecipe && <button className="text-link" type="button" onClick={async () => { const response = await fetch(`/api/recipes/${lesson.recipe_id}`); if (response.ok) onOpenRecipe?.(await response.json()); }}>Open linked recipe</button>}{owner && mode === "modal" && <div className="course-actions"><button className="text-link" type="button" disabled={index === 0} onClick={() => moveLesson(index, -1)}>Move up</button><button className="text-link" type="button" disabled={index === lessons.length - 1} onClick={() => moveLesson(index, 1)}>Move down</button><button className="text-link" type="button" onClick={() => setLessonOpen(lesson)}>Edit lesson</button><button className="text-link" type="button" onClick={() => void removeLesson(lesson)}>Remove lesson</button></div>}</li>)}</ol> : <p className="cookbook-empty">This course has no lessons yet.</p>}
    </>
  );
  if (mode === "page") return <section className="cookbook-dashboard" aria-labelledby="course-title-heading">{content}</section>;
  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        ref={dialogRef}
        tabIndex={-1}
        className="signin-dialog course-detail"
        role="dialog"
        aria-modal="true"
        aria-labelledby="course-title-heading"
      >
        <button
          className="icon-button dialog-close"
          type="button"
          aria-label="Close course"
          onClick={onClose}
        >
          ×
        </button>
        {content}
        {lessonOpen && (
          <LessonForm
            course={course}
            lesson={lessonOpen === true ? null : lessonOpen}
            position={
              typeof lessonOpen === "object"
                ? lessonOpen.position
                : lessons.reduce(
                    (maximum, lesson) => Math.max(maximum, lesson.position),
                    -1,
                  ) + 1
            }
            onClose={() => setLessonOpen(false)}
            onSaved={async () => {
              setLessonOpen(false);
              await reload();
              await onChanged();
            }}
          />
        )}
      </section>
    </div>
  );
}

interface LessonFormProps {
  course: Course;
  lesson: CourseLesson | null;
  position: number;
  onClose: () => void;
  onSaved: () => Promise<void>;
}

function LessonForm({
  course,
  lesson,
  position,
  onClose,
  onSaved,
}: LessonFormProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [title, setTitle] = useState(lesson?.title || ""),
    [content, setContent] = useState(lesson?.content || ""),
    [recipeID, setRecipeID] = useState(lesson?.recipe_id || ""),
    [recipes, setRecipes] = useState<PublicRecipe[]>([]),
    [error, setError] = useState(""),
    [saving, setSaving] = useState(false);
  useEffect(() => {
    fetch("/api/recipes?limit=100")
      .then((response) =>
        response.ok ? (response.json() as Promise<PublicRecipe[]>) : [],
      )
      .then(setRecipes)
      .catch(() => setRecipes([]));
  }, []);
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      const response = await fetch(
        lesson
          ? `/api/courses/${course.id}/lessons/${lesson.id}`
          : `/api/courses/${course.id}/lessons`,
        {
          method: lesson ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            title,
            content,
            recipe_id: recipeID,
            position,
          }),
        },
      );
      if (!response.ok) throw new Error("Could not save this lesson.");
      await onSaved();
    } catch (saveError: unknown) {
      setError(errorMessage(saveError, "Could not save this lesson."));
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
        aria-labelledby="lesson-form-title"
      >
        <button
          className="icon-button dialog-close"
          type="button"
          aria-label="Close lesson form"
          onClick={onClose}
        >
          ×
        </button>
        <span className="eyebrow">{lesson ? "Edit" : "New"} course lesson</span>
        <h2 id="lesson-form-title">
          {lesson ? "Edit lesson" : "Add a lesson"}
        </h2>
        <form onSubmit={submit}>
          <label htmlFor="lesson-title">Lesson title</label>
          <input
            id="lesson-title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            maxLength={200}
            required
          />
          <label htmlFor="lesson-content">Lesson content</label>
          <textarea
            id="lesson-content"
            value={content}
            onChange={(event) => setContent(event.target.value)}
            maxLength={20000}
          />
          <label htmlFor="lesson-recipe">
            Link an accessible recipe (optional)
          </label>
          <select
            id="lesson-recipe"
            value={recipeID}
            onChange={(event) => setRecipeID(event.target.value)}
          >
            <option value="">No linked recipe</option>
            {recipes.map((recipe) => (
              <option key={recipe.id} value={recipe.id}>
                {recipe.title}
              </option>
            ))}
          </select>
          <button
            className="button button--green"
            type="submit"
            disabled={saving}
          >
            {saving ? "Saving…" : lesson ? "Save lesson" : "Add lesson"}
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
