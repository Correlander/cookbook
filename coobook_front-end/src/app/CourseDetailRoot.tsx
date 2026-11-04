import { useEffect, useState } from "react";
import type { Course } from "../apiModels";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, useAppSession } from "./AppSession.tsx";
import { SessionCheckError } from "./AppSession.tsx";
import CourseDetail from "../features/courses/CourseDetail.tsx";
import { parseCourse } from "../features/courses/courseApi.ts";

function CourseDetailPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  const [routeID, setRouteID] = useState<string | null>(null);
  const [course, setCourse] = useState<Course | null>(null);
  const [courseKey, setCourseKey] = useState("");
  const [coursesEnabled, setCoursesEnabled] = useState<boolean | null>(null);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  useEffect(() => {
    const read = () => setRouteID(new URLSearchParams(window.location.search).get("id") ?? "");
    read(); window.addEventListener("popstate", read); return () => window.removeEventListener("popstate", read);
  }, []);
  useEffect(() => {
    if (state.status === "loading" || state.status === "error" || routeID === null) return;
    let active = true;
    const actor = state.status === "authenticated" ? state.currentUser.id : "anonymous";
    setCourse(null); setCourseKey(""); setUnavailable(false); setLoading(true);
    fetch("/api/public/config", { cache: "no-store" })
      .then(async (response) => response.ok ? response.json() as Promise<{ courses_enabled?: boolean }> : { courses_enabled: false })
      .then(async (config) => {
        if (!active) return;
        const enabled = config.courses_enabled === true;
        setCoursesEnabled(enabled);
        if (!enabled || !routeID.trim()) { setUnavailable(true); return; }
        const endpoint = state.status === "authenticated" ? `/api/courses/${encodeURIComponent(routeID)}` : `/api/public/courses/${encodeURIComponent(routeID)}`;
        const response = await fetch(endpoint, { cache: "no-store", credentials: "same-origin" });
        if (!response.ok) throw new Error("Course unavailable");
        const result = parseCourse(await response.json());
        if (active && (state.status === "anonymous" || state.currentUser.id === actor)) { setCourse(result); setCourseKey(`${actor}:${routeID}`); }
      })
      .catch(() => { if (active) setUnavailable(true); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [currentUser?.id, routeID, state]);
  const actorKey = state.status === "authenticated" ? state.currentUser.id : "anonymous";
  const routeKey = `${actorKey}:${routeID ?? ""}`;
  return <div className="app-content"><AppNavigation currentUser={currentUser} pathname="/course" sessionStatus={state.status} onRetrySession={() => void refreshSession()} /><main id="home" tabIndex={-1}>{state.status === "error" ? <SessionCheckError message={state.message} onRetry={() => void refreshSession()} /> : state.status === "loading" || routeID === null || loading || (course !== null && courseKey !== routeKey) ? <p className="cookbook-dashboard" role="status">Checking course availability…</p> : unavailable || !course ? <section className="cookbook-dashboard"><h1>Course unavailable</h1><p>{coursesEnabled === false ? "Courses are disabled on this instance." : "This course is unavailable or you cannot access it."}</p><a className="text-link" href="/library?type=courses">Go to your Courses Library</a></section> : <CourseDetail key={`${currentUser?.id ?? "anonymous"}:${course.id}`} course={course} currentUserID={state.status === "authenticated" ? state.currentUser.id : null} mode="page" onClose={() => {}} onEdit={() => {}} onChanged={async () => {}} />}</main></div>;
}

export default function CourseDetailRoot() { return <AppSessionProvider><CourseDetailPage /></AppSessionProvider>; }
