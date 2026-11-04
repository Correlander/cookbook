import { expect, test } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

type ProfileCourseList = {
  profile: { id: string };
  items: Array<{
    course: { id: string; title: string };
    role: string;
    provenance: string;
  }>;
  total: number;
  limit: number;
  offset: number;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function readProfileID(value: unknown): string {
  if (!isRecord(value) || !isRecord(value.profile))
    throw new Error("creator profile response was malformed");
  const id = value.profile.id;
  if (typeof id !== "string" || id.length === 0)
    throw new Error("creator profile response did not include an ID");
  return id;
}

function isProfileCourseList(value: unknown): value is ProfileCourseList {
  if (!isRecord(value) || !isRecord(value.profile) || !Array.isArray(value.items))
    return false;
  if (
    typeof value.profile.id !== "string" ||
    typeof value.total !== "number" ||
    typeof value.limit !== "number" ||
    typeof value.offset !== "number"
  )
    return false;
  return value.items.every((item) => {
    if (!isRecord(item) || !isRecord(item.course)) return false;
    return (
      typeof item.course.id === "string" &&
      typeof item.course.title === "string" &&
      typeof item.role === "string" &&
      typeof item.provenance === "string"
    );
  });
}

function readString(value: unknown, key: string): string {
  if (!isRecord(value) || typeof value[key] !== "string")
    throw new Error(`course response did not include ${key}`);
  return value[key];
}

test("signed-in creator profile lists only its explicit readable course credits", async ({
  page,
}) => {
  const origin = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";
  const suffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const localTitle = `Profile course ${suffix}`;
  const claimTitle = `Name claim course ${suffix}`;
  const claimName = `Claimed teacher ${suffix}`;
  const titles = new Set([localTitle, claimTitle]);
  const courseIDs = new Set<string>();
  const profileCourseRequests: string[] = [];
  let originalCoursesEnabled: boolean | null = null;
  let currentCoursesEnabled: boolean | null = null;
  let primaryFailure: unknown;
  let bodyFailed = false;
  let cleanupFailure: unknown;

  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      url.pathname.startsWith("/api/creator-profiles/") &&
      url.pathname.endsWith("/courses")
    )
      profileCourseRequests.push(`${request.method()} ${url.pathname}${url.search}`);
  });

  async function setCoursesEnabled(enabled: boolean): Promise<void> {
    const response = await page.request.patch("/api/admin/settings", {
      headers: { Origin: origin },
      data: { courses_enabled: enabled },
    });
    expect(response.status()).toBe(200);
    currentCoursesEnabled = enabled;
  }

  try {
    const actor = await signInWithSyntheticEmail(
      page,
      page.request,
      "profile-courses-admin@example.test",
      "Profile Courses Admin",
    );
    expect(actor.is_admin).toBe(true);

    const settingsResponse = await page.request.get("/api/admin/settings");
    expect(settingsResponse.status()).toBe(200);
    const settings: unknown = await settingsResponse.json();
    if (!isRecord(settings) || typeof settings.courses_enabled !== "boolean")
      throw new Error("admin settings did not include courses_enabled");
    originalCoursesEnabled = settings.courses_enabled;
    currentCoursesEnabled = settings.courses_enabled;
    if (!currentCoursesEnabled) await setCoursesEnabled(true);

    const localCourseResponse = await page.request.post("/api/courses", {
      headers: { Origin: origin },
      data: {
        title: localTitle,
        description: "Explicit local creator profile credit",
        visibility: "private",
        creator_attribution: { kind: "local_profile" },
      },
    });
    expect(localCourseResponse.status()).toBe(201);
    const localCourse: unknown = await localCourseResponse.json();
    const localCourseID = readString(localCourse, "id");
    courseIDs.add(localCourseID);
    expect(localCourse).toMatchObject({ title: localTitle });

    const claimCourseResponse = await page.request.post("/api/courses", {
      headers: { Origin: origin },
      data: {
        title: claimTitle,
        description: "Unbound name claim",
        visibility: "private",
        creator_attribution: { kind: "name_claim", name: claimName },
      },
    });
    expect(claimCourseResponse.status()).toBe(201);
    const claimCourse: unknown = await claimCourseResponse.json();
    courseIDs.add(readString(claimCourse, "id"));
    expect(claimCourse).toMatchObject({ creator_name: claimName });
    expect(
      isRecord(claimCourse) && Array.isArray(claimCourse.creator_attributions)
        ? claimCourse.creator_attributions
        : [],
    ).toEqual([]);

    const selfProfileResponse = await page.request.get(
      "/api/account/creator-profile",
    );
    expect(selfProfileResponse.status()).toBe(200);
    const profileID = readProfileID(await selfProfileResponse.json());
    const profileCoursesResponse = await page.request.get(
      `/api/creator-profiles/${encodeURIComponent(profileID)}/courses?limit=24&offset=0`,
    );
    expect(profileCoursesResponse.status()).toBe(200);
    const profileCoursesValue: unknown = await profileCoursesResponse.json();
    if (!isProfileCourseList(profileCoursesValue))
      throw new Error("creator profile course response was malformed");
    expect(profileCoursesValue.profile.id).toBe(profileID);
    expect(profileCoursesValue.items).toEqual([
      {
        course: { id: localCourseID, title: localTitle },
        role: "creator",
        provenance: "self_asserted_at_creation",
      },
    ]);
    expect(profileCoursesValue.items.some((item) => item.course.title === claimTitle)).toBe(
      false,
    );

    profileCourseRequests.length = 0;
    await page.goto(`/creator?id=${encodeURIComponent(profileID)}`);
    const courseSection = page.locator(
      'section[aria-labelledby="creator-courses-title"]',
    );
    await expect(courseSection).toBeVisible();
    await expect(
      courseSection.getByRole("link", { name: localTitle, exact: true }),
    ).toHaveAttribute("href", `/course?id=${encodeURIComponent(localCourseID)}`);
    await expect(
      courseSection.getByRole("link", { name: claimTitle, exact: true }),
    ).toHaveCount(0);
    await expect(courseSection.getByText("Course creator", { exact: true })).toBeVisible();
    await expect(courseSection.getByText(/self-asserted when created/)).toBeVisible();
    await expect(
      courseSection.getByText(/Attribution does not show course ownership or grant editing access/),
    ).toBeVisible();
    await expect(page.getByRole("heading", { name: "Recipes", exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Cookbooks", exact: true })).toBeVisible();
    expect(profileCourseRequests.some((request) => request.includes(`/creator-profiles/${profileID}/courses?`))).toBe(true);

    await setCoursesEnabled(false);
    profileCourseRequests.length = 0;
    await page.reload();
    await expect(page.getByRole("heading", { name: "Creator profile unavailable" })).toHaveCount(0);
    await expect(page.locator('section[aria-labelledby="creator-courses-title"]')).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Recipes", exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Cookbooks", exact: true })).toBeVisible();
    expect(profileCourseRequests).toEqual([]);
  } catch (error) {
    primaryFailure = error;
    bodyFailed = true;
  } finally {
    try {
      if (currentCoursesEnabled === false) await setCoursesEnabled(true);
      if (currentCoursesEnabled === true) {
        const listed = await page.request.get("/api/courses?limit=100");
        if (listed.ok()) {
          const value: unknown = await listed.json();
          if (Array.isArray(value)) {
            for (const item of value) {
              if (
                isRecord(item) &&
                typeof item.id === "string" &&
                typeof item.title === "string" &&
                titles.has(item.title)
              )
                courseIDs.add(item.id);
            }
          }
        } else {
          cleanupFailure ??= new Error(
            `could not list profile-course fixtures for cleanup (${listed.status()})`,
          );
        }
        for (const id of courseIDs) {
          const deleted = await page.request.delete(
            `/api/courses/${encodeURIComponent(id)}`,
            { headers: { Origin: origin } },
          );
          if (![204, 404].includes(deleted.status()))
            cleanupFailure ??= new Error(
              `could not delete profile-course fixture (${deleted.status()})`,
            );
        }
      }
    } catch (error) {
      cleanupFailure ??= error;
    }
    try {
      if (
        originalCoursesEnabled !== null &&
        currentCoursesEnabled !== originalCoursesEnabled
      )
        await setCoursesEnabled(originalCoursesEnabled);
    } catch (error) {
      cleanupFailure ??= error;
    }
    try {
      await page.context().close();
    } catch (error) {
      cleanupFailure ??= error;
    }
  }

  if (bodyFailed) throw primaryFailure;
  if (cleanupFailure !== undefined) throw cleanupFailure;
});
