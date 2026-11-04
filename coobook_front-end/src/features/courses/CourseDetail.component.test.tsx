import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
