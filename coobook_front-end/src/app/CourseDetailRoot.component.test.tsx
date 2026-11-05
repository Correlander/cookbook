import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import CourseDetailRoot from "./CourseDetailRoot.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.history.replaceState(null, "", "/course?id=course-1"); });
function json(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }); }
function publicConfig(courses_enabled: boolean) {
  return { auth_methods: { email_link: true, passkey: false }, registration_policy: "invite_only", public_instance: false, courses_enabled };
}

it("keeps disabled course content behind the feature gate and links to reference management", async () => {
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input); calls.push(url);
    if (url === "/api/auth/me") return Promise.resolve(json({ id: "actor", email: "a@example.test", display_name: "A", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(json(publicConfig(false)));
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
    if (url === "/api/public/config") return Promise.resolve(json(publicConfig(true)));
    if (url === "/api/courses/course-1") return Promise.resolve(json({}, 404));
    return Promise.resolve(json({}, 404));
  }));
  render(<CourseDetailRoot />);
  expect(await screen.findByText("This course is unavailable or you cannot access it.")).toBeTruthy();
  await waitFor(() => expect(calls.filter((url) => url === "/api/courses/course-1")).toHaveLength(1));
  expect(calls.some((url) => url === "/api/public/courses/course-1")).toBe(false);
});

it("parses an accepted authenticated course detail and keeps creator data separate from owner", async () => {
  const course = { id: "course-1", owner_id: "actor", owner_name: "Custodian", title: "Bread", description: "", visibility: "private", lesson_count: 0, created_at: "then", updated_at: "then", creator_name: "Claimed teacher", creator_attributions: [{ subject_kind: "external_claim", name_claim: "Imported teacher", role: "contributor", provenance: "portable_claim" }] };
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/auth/me") return Promise.resolve(json({ id: "actor", email: "a@example.test", display_name: "A", is_admin: false }));
    if (url === "/api/public/config") return Promise.resolve(json(publicConfig(true)));
    if (url === "/api/courses/course-1") return Promise.resolve(json(course));
    if (url === "/api/courses/course-1/lessons") return Promise.resolve(json([]));
    return Promise.resolve(json({}, 404));
  }));
  render(<CourseDetailRoot />);
  expect(await screen.findByRole("heading", { name: "Bread" })).toBeTruthy();
  expect(screen.getByText("private course · Owner: Custodian")).toBeTruthy();
  expect(screen.getByText("Unverified creator name: Claimed teacher")).toBeTruthy();
  expect(screen.getByText("Unverified course creator claim: Imported teacher")).toBeTruthy();
});
