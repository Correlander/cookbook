import { lazy, useEffect, useState } from "react";
import type { Course } from "./apiModels";
import FeatureIsland from "./components/FeatureIsland.tsx";
import { useAppSession } from "./app/AppSession.tsx";

const CourseDetail = lazy(() => import("./features/courses/CourseDetail.tsx"));

export default function PublicCourses() {
  const { currentUser } = useAppSession();
  const [courses, setCourses] = useState<Course[]>([]);
  const [selected, setSelected] = useState<Course | null>(null);
  useEffect(() => {
    let active = true;
    fetch("/api/public/courses?limit=24")
      .then(async (response) => (response.ok ? response.json() : []))
      .then((items: Course[]) => {
        if (active) setCourses(items);
      })
      .catch(() => {
        if (active) setCourses([]);
      });
    return () => {
      active = false;
    };
  }, []);
  if (!courses.length) return null;
  return (
    <section
      className="cookbook-dashboard course-list"
      aria-labelledby="public-courses-heading"
    >
      <div className="section-heading">
        <div>
          <span className="eyebrow">Learn something new</span>
          <h2 id="public-courses-heading">Public courses</h2>
        </div>
      </div>
      <div className="cookbook-grid">
        {courses.map((course) => (
          <article className="cookbook-card" key={course.id}>
            <span className="recipe-card__category">
              {course.lesson_count} lessons
            </span>
            <h3><a href={`/course?id=${encodeURIComponent(course.id)}`}>{course.title}</a></h3>
            {course.description && <p>{course.description}</p>}
            <p className="course-owner">Owner: {course.owner_name}</p>
            <button
              className="text-link"
              type="button"
              onClick={() => setSelected(course)}
            >
              Open course
            </button>
          </article>
        ))}
      </div>
      {selected && (
        <FeatureIsland name="Course details" loading="course details">
          <CourseDetail
            course={selected}
            currentUserID={currentUser?.id ?? null}
            onClose={() => setSelected(null)}
            onEdit={() => {}}
            onChanged={async () => {}}
          />
        </FeatureIsland>
      )}
    </section>
  );
}
