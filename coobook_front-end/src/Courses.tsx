import { useEffect, useState } from "react";
import type { Course, PublicRecipe } from "./apiModels";
import { errorMessage } from "./apiModels";
import CourseDetail from "./features/courses/CourseDetail.tsx";
import CourseForm from "./features/courses/CourseForm.tsx";
import { useAppSession } from "./app/AppSession.tsx";

interface CoursesProps {
  onOpenRecipe?: (recipe: PublicRecipe) => void;
}
type EditingCourse = Course | "new";

export default function Courses({ onOpenRecipe }: CoursesProps) {
  const { currentUser } = useAppSession();
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
              <h3><a href={`/course?id=${encodeURIComponent(course.id)}`}>{course.title}</a></h3>
              {course.description && <p>{course.description}</p>}
              <p className="course-owner">Owner: {course.owner_name}</p>
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
          currentUserID={currentUser?.id ?? null}
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
