import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CourseDetail from "./CourseDetail.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const course = { id: "course-1", owner_id: "owner-1", owner_name: "Current Owner", title: "Bread basics", description: "Lesson series", visibility: "public" as const, lesson_count: 0, created_at: "now", updated_at: "now" };
function json(body: unknown) { return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }); }

it("keeps modal semantics and labels current custody as Owner", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json([])));
  const close = vi.fn();
  render(<CourseDetail course={course} onClose={close} onEdit={vi.fn()} onChanged={async () => {}} />);
  expect(await screen.findByRole("dialog", { name: "Bread basics" })).toBeTruthy();
  expect(screen.getByText("public course · Owner: Current Owner")).toBeTruthy();
  expect(screen.getByRole("link", { name: "Open course page" }).getAttribute("href")).toBe("/course?id=course-1");
  await waitFor(() => expect(screen.getByRole("button", { name: "Close course" })).toBe(document.activeElement));
  fireEvent.keyDown(document, { key: "Escape" });
  expect(close).toHaveBeenCalled();
});

it("renders shared detail content inline without modal or owner edit controls", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json([])));
  render(<CourseDetail course={course} mode="page" currentUserID="reader" onClose={vi.fn()} onEdit={vi.fn()} onChanged={async () => {}} />);
  expect(await screen.findByRole("heading", { name: "Bread basics", level: 1 })).toBeTruthy();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByRole("button", { name: "Delete course" })).toBeNull();
  expect(screen.getByRole("link", { name: "Open course page" })).toBeTruthy();
});

it("keeps course custody, unverified name claim, and typed credits separate", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json([])));
  render(<CourseDetail course={{ ...course, creator_name: "Claimed teacher", creator_attributions: [{ subject_kind: "external_claim", name_claim: "Imported teacher", role: "contributor", provenance: "portable_claim" }] }} mode="page" currentUserID="reader" onClose={vi.fn()} onEdit={vi.fn()} onChanged={async () => {}} />);
  expect(await screen.findByText("public course · Owner: Current Owner")).toBeTruthy();
  expect(screen.getByText("Unverified creator name: Claimed teacher")).toBeTruthy();
  expect(screen.getByRole("region", { name: "Course creator credits" }).textContent).toContain("Unverified course creator claim: Imported teacher");
});

it.each([false, true])("preserves a public lesson %s draft after the exact publisher requirement", async (editing) => {
  const lesson = { id: "lesson-1", course_id: course.id, title: "Old lesson", content: "Old content", recipe_id: "recipe-1", position: 0, created_at: "now", updated_at: "now" };
  const recipe = {
    id: "recipe-1", creator_name: "", title: "Tomato soup", description: "", ingredients: [], steps: [],
    servings: 1, prep_time_minutes: 1, cook_time_minutes: 1, tags: [], version: 1,
    created_at: "now", updated_at: "now",
  };
  let writes = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/courses/course-1/lessons") {
      if (init?.method === "POST") {
        writes += 1;
        return Promise.resolve(new Response(JSON.stringify({ error: "publisher_profile_required" }), { status: 409 }));
      }
      return Promise.resolve(json(editing ? [lesson] : []));
    }
    if (url === "/api/courses/course-1/lessons/lesson-1" && init?.method === "PATCH") {
      writes += 1;
      return Promise.resolve(new Response(JSON.stringify({ error: "publisher_profile_required" }), { status: 409 }));
    }
    if (url === "/api/recipes?limit=100") return Promise.resolve(json([recipe]));
    return Promise.resolve(json([]));
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<CourseDetail course={course} onClose={vi.fn()} onEdit={vi.fn()} onChanged={async () => {}} />);
  if (editing) fireEvent.click(await screen.findByRole("button", { name: "Edit lesson" }));
  else fireEvent.click(await screen.findByRole("button", { name: "Add lesson" }));
  await screen.findByRole("option", { name: "Tomato soup" });
  fireEvent.change(screen.getByLabelText("Lesson title"), { target: { value: "Revised public lesson" } });
  fireEvent.change(screen.getByLabelText("Lesson content"), { target: { value: "Keep this lesson text" } });
  fireEvent.change(screen.getByLabelText("Link an accessible recipe (optional)"), { target: { value: "recipe-1" } });
  const lessonDialog = screen.getByRole("dialog", { name: editing ? "Edit lesson" : "Add a lesson" });
  fireEvent.click(within(lessonDialog).getByRole("button", { name: editing ? "Save lesson" : "Add lesson" }));

  expect(await screen.findByRole("link", { name: /opens in a new tab/i })).toBeTruthy();
  expect((screen.getByLabelText("Lesson title") as HTMLInputElement).value).toBe("Revised public lesson");
  expect((screen.getByLabelText("Lesson content") as HTMLTextAreaElement).value).toBe("Keep this lesson text");
  expect((screen.getByLabelText("Link an accessible recipe (optional)") as HTMLSelectElement).value).toBe("recipe-1");
  expect(writes).toBe(1);
  fireEvent.click(within(lessonDialog).getByRole("button", { name: editing ? "Save lesson" : "Add lesson" }));
  await waitFor(() => expect(writes).toBe(2));
});

it("keeps course custody, unverified name claim, and typed credits separate", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json([])));
  render(<CourseDetail course={{ ...course, creator_name: "Claimed teacher", creator_attributions: [{ subject_kind: "external_claim", name_claim: "Imported teacher", role: "contributor", provenance: "portable_claim" }] }} mode="page" currentUserID="reader" onClose={vi.fn()} onEdit={vi.fn()} onChanged={async () => {}} />);
  expect(await screen.findByText("public course · Owner: Current Owner")).toBeTruthy();
  expect(screen.getByText("Unverified creator name: Claimed teacher")).toBeTruthy();
  expect(screen.getByRole("region", { name: "Course creator credits" }).textContent).toContain("Unverified course creator claim: Imported teacher");
});
