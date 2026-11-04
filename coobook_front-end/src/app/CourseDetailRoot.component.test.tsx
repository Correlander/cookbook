import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CourseDetailRoot from "./CourseDetailRoot.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/course?id=course-1"); });
function json(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }); }

it("keeps disabled course content behind the feature gate and links to reference management", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(json({ id: "actor", email: "a@example.test", display_name: "A", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(json({ courses_enabled: false }));
    return Promise.resolve(json({}, 404));
  }));
  render(<CourseDetailRoot />);
  expect(await screen.findByText("Courses are disabled on this instance.")).toBeTruthy();
  expect(screen.getByRole("link", { name: "Go to your Courses Library" }).getAttribute("href")).toBe("/library?type=courses");
  expect(calls.some((url) => url.includes("/api/public/courses/") || url === "/api/courses/course-1")).toBe(false);
});

it("uses one authenticated read and never falls back to anonymous detail after denial", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(json({ id: "actor", email: "a@example.test", display_name: "A", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(json({ courses_enabled: true }));
    if (url === "/api/courses/course-1") return Promise.resolve(json({}, 404));
    return Promise.resolve(json({}, 404));
  }));
  render(<CourseDetailRoot />);
  expect(await screen.findByText("This course is unavailable or you cannot access it.")).toBeTruthy();
  await waitFor(() => expect(calls.filter((url) => url === "/api/courses/course-1")).toHaveLength(1));
  expect(calls.some((url) => url === "/api/public/courses/course-1")).toBe(false);
});
