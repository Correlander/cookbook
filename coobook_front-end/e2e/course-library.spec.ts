import { expect, test } from "@playwright/test";

test("static course page checks access before lessons and saves an explicit reference", async ({ page }) => {
  const actor = { id: "actor-course", email: "reader@example.test", display_name: "Reader", is_admin: false };
  const course = { id: "course-e2e", owner_id: "actor-course", owner_name: "Reader", title: "Bread basics", description: "Starter course", visibility: "public", lesson_count: 1, created_at: "now", updated_at: "now" };
  let saved = false;
  const calls: string[] = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request(); const url = new URL(request.url()); calls.push(`${request.method()} ${url.pathname}${url.search}`);
    if (url.pathname === "/api/auth/me") return route.fulfill({ json: actor });
    if (url.pathname === "/api/public/config") return route.fulfill({ json: { courses_enabled: true } });
    if (url.pathname === "/api/courses/course-e2e" && request.method() === "GET") return route.fulfill({ json: course });
    if (url.pathname === "/api/courses/course-e2e/lessons") return route.fulfill({ json: [{ id: "lesson-1", course_id: course.id, title: "Mix dough", content: "Combine flour and water.", position: 0 }] });
    if (url.pathname === "/api/library/course-bookmarks/status") return route.fulfill({ json: saved ? { saved: true, bookmark_id: "bookmark-course" } : { saved: false } });
    if (url.pathname === "/api/library/course-bookmarks" && request.method() === "POST") { saved = true; return route.fulfill({ status: 201, json: { bookmark_id: "bookmark-course", course_id: course.id, created_at: "now" } }); }
    if (url.pathname === "/api/library/course-bookmarks/bookmark-course" && request.method() === "DELETE") { saved = false; return route.fulfill({ status: 204 }); }
    return route.fulfill({ status: 404, json: { error: "not_found" } });
  });
  await page.goto("/course?id=course-e2e");
  await expect(page.getByRole("heading", { name: "Bread basics" })).toBeVisible();
  await expect(page.getByText("Owner: Reader")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Mix dough" })).toBeVisible();
  await page.getByRole("button", { name: "Save Bread basics to Library" }).click();
  await expect(page.getByRole("button", { name: "Remove Bread basics from Library" })).toBeVisible();
  expect(calls).toContain("GET /api/courses/course-e2e");
  expect(calls).toContain("GET /api/courses/course-e2e/lessons");
  expect(calls).not.toContain("GET /api/public/courses/course-e2e");
});
