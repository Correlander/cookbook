import { expect, test } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

type CourseResponse = {
  id: string;
  owner_name: string;
  creator_name: string;
  creator_attributions?: Array<{ subject_kind: string; role: string; name_claim?: string }>;
};

test("course attribution capability controls create claims and keeps edits immutable", async ({ page, browser }) => {
  const origin = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";
  const suffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const courseIDs: string[] = [];
  const browserMutationHeaders: Promise<Record<string, string>>[] = [];
  let actorContext: Awaited<ReturnType<typeof browser.newContext>> | null = null;
  let originalCoursesEnabled: boolean | null = null;
  let restoreCoursePolicy = false;

  try {
    await signInWithSyntheticEmail(page, page.request, "admin@example.test", "E2E Admin");
    const settingsResponse = await page.request.get("/api/admin/settings");
    expect(settingsResponse.status()).toBe(200);
    const settings: unknown = await settingsResponse.json();
    expect(settings).toMatchObject({ courses_enabled: expect.any(Boolean) });
    originalCoursesEnabled = (settings as { courses_enabled: boolean }).courses_enabled;
    if (!originalCoursesEnabled) {
      restoreCoursePolicy = true;
      const enableCourses = await page.request.patch("/api/admin/settings", {
        headers: { Origin: origin },
        data: { courses_enabled: true },
      });
      expect(enableCourses.status()).toBe(200);
    }

    actorContext = await browser.newContext({ baseURL: origin });
    const actorPage = await actorContext.newPage();
    await signInWithSyntheticEmail(actorPage, actorContext.request, `course-attribution-${suffix}@example.test`, `Course editor ${suffix}`);
    actorPage.on("request", (browserRequest) => {
      if (browserRequest.url().includes("/api/courses") && ["POST", "PATCH", "DELETE"].includes(browserRequest.method()))
        browserMutationHeaders.push(browserRequest.allHeaders());
    });
    const request = actorContext.request;
    const configResponse = await request.get("/api/public/config");
    expect(configResponse.status()).toBe(200);
    const config: unknown = await configResponse.json();
    expect(config).toMatchObject({ course_creator_attribution: true });

    async function createCourse(title: string, creatorAttribution: unknown): Promise<CourseResponse> {
      const response = await request.post("/api/courses", {
        headers: { Origin: origin },
        data: { title, description: "Attribution E2E resource", visibility: "private", creator_attribution: creatorAttribution },
      });
      expect(response.status()).toBe(201);
      const course = await response.json() as CourseResponse;
      courseIDs.push(course.id);
      return course;
    }

    const local = await createCourse(`Course local ${suffix}`, { kind: "local_profile" });
    expect(local.owner_name).toContain(`Course editor ${suffix}`);
    expect(local.creator_attributions?.map((credit) => credit.subject_kind)).toEqual(["local"]);

    const claimed = await createCourse(`Course claim ${suffix}`, { kind: "name_claim", name: `Claimed teacher ${suffix}` });
    expect(claimed.creator_name).toBe(`Claimed teacher ${suffix}`);
    expect(claimed.creator_attributions ?? []).toEqual([]);

    const unknown = await createCourse(`Course unknown ${suffix}`, { kind: "unknown" });
    expect(unknown.creator_name).toBe("");
    expect(unknown.creator_attributions ?? []).toEqual([]);

    const patch = await request.patch(`/api/courses/${encodeURIComponent(claimed.id)}`, {
      headers: { Origin: origin },
      data: { title: `Course edited ${suffix}`, description: "Edited without touching credit", visibility: "private" },
    });
    expect(patch.status()).toBe(200);
    const edited = await patch.json() as CourseResponse;
    expect(edited.creator_name).toBe(`Claimed teacher ${suffix}`);

    await actorPage.goto("/create");
    await actorPage.getByRole("button", { name: "Course", exact: true }).click();
    const dialog = actorPage.getByRole("dialog", { name: "Create a course" });
    await expect(dialog.getByRole("combobox", { name: "Creator credit" })).toBeVisible();
    await dialog.getByLabel("Title").fill(`Course UI ${suffix}`);
    await dialog.getByLabel("Creator credit").selectOption("unknown");
    const browserCreate = actorPage.waitForResponse((response) => response.url().endsWith("/api/courses") && response.request().method() === "POST");
    await dialog.getByRole("button", { name: "Save course" }).click();
    const browserResponse = await browserCreate;
    expect(browserResponse.status()).toBe(201);
    const uiCourse = await browserResponse.json() as CourseResponse;
    courseIDs.push(uiCourse.id);
    expect(uiCourse.creator_name).toBe("");
    expect(uiCourse.creator_attributions ?? []).toEqual([]);
    expect(browserMutationHeaders.length).toBeGreaterThan(0);
    for (const headers of await Promise.all(browserMutationHeaders)) expect(headers.origin).toBe(origin);
  } finally {
    try {
      const listing = actorContext ? await actorContext.request.get("/api/courses?limit=100") : null;
      if (listing?.ok()) {
        const courses = await listing.json() as Array<{ id?: unknown; title?: unknown }>;
        const titles = [`Course local ${suffix}`, `Course claim ${suffix}`, `Course unknown ${suffix}`, `Course UI ${suffix}`, `Course edited ${suffix}`];
        for (const course of courses) {
          if (typeof course.id === "string" && typeof course.title === "string" && titles.includes(course.title)) courseIDs.push(course.id);
        }
      } else if (actorContext && courseIDs.length > 0) {
        expect(listing?.status()).toBe(200);
      }
      for (const id of new Set(courseIDs)) {
        if (!actorContext) continue;
        const response = await actorContext.request.delete(`/api/courses/${encodeURIComponent(id)}`, { headers: { Origin: origin } });
        expect([204, 404]).toContain(response.status());
      }
    } finally {
      try {
        if (restoreCoursePolicy && originalCoursesEnabled !== null) {
        const response = await page.request.patch("/api/admin/settings", {
          headers: { Origin: origin },
          data: { courses_enabled: originalCoursesEnabled },
        });
          expect(response.status()).toBe(200);
        }
      } finally {
        await actorContext?.close();
        await page.context().close();
      }
    }
  }
});
