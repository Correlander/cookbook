/// <reference types="node" />

import { expect, test } from "@playwright/test";
import type { Browser, BrowserContext, Page } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";

interface ProfileFields {
  id?: string;
  public_name: string;
  bio: string;
  visibility: "private" | "instance";
}

async function readSelfProfile(page: Page): Promise<ProfileFields | null> {
  const response = await page.request.get("/api/account/creator-profile");
  expect(response.status()).toBe(200);
  const body = (await response.json()) as { profile: ProfileFields | null };
  return body.profile ? {
    id: body.profile.id,
    public_name: body.profile.public_name,
    bio: body.profile.bio,
    visibility: body.profile.visibility,
  } : null;
}

async function writeSelfProfile(page: Page, profile: ProfileFields): Promise<void> {
  const response = await page.request.put("/api/account/creator-profile", {
    headers: { Origin: baseURL },
    data: { public_name: profile.public_name, bio: profile.bio, visibility: profile.visibility },
  });
  expect(response.status()).toBe(200);
}

async function attemptCleanup(
  failures: string[],
  label: string,
  operation: () => Promise<void>,
): Promise<void> {
  try {
    await operation();
  } catch (error) {
    failures.push(`${label}: ${error instanceof Error ? error.message : "cleanup failed"}`);
  }
}

async function openContext(browser: Browser): Promise<{
  context: BrowserContext;
  page: Page;
}> {
  const context = await browser.newContext({ baseURL });
  return { context, page: await context.newPage() };
}

async function setPublicInstance(page: Page, enabled: boolean): Promise<void> {
  const response = await page.request.patch("/api/admin/settings", {
    headers: { Origin: baseURL },
    data: {
      public_instance: enabled,
      ...(enabled ? { confirm_publication: true } : {}),
    },
  });
  expect(response.status()).toBe(200);
}

async function createRecipe(
  page: Page,
  title: string,
  visibility: "private" | "public",
): Promise<{ id: string; title: string }> {
  const response = await page.request.post("/api/recipes", {
    headers: { Origin: baseURL },
    data: {
      title,
      description: "Synthetic Library route fixture.",
      servings: 2,
      prep_time_minutes: 5,
      cook_time_minutes: 10,
      tags: [],
      visibility,
      edit_policy: "owner_only",
      cookbook_ids: [],
      ingredients: [{ name: "Tomato", quantity: "2", notes: "ripe" }],
      steps: [{ text: "Cook the tomatoes." }],
      ...(visibility === "public"
        ? { confirm_public_access: true }
        : {}),
      initial_publication: {
        publication_state: visibility === "public" ? "published" : "unpublished",
        federation_mode: "off",
        index_eligible: false,
      },
    },
  });
  expect(response.status()).toBe(200);
  return (await response.json()) as { id: string; title: string };
}

test("Library and recipe detail preserve account, public, saved, unavailable, and copy boundaries", async ({
  browser,
}) => {
  const admin = await openContext(browser);
  const cspConsoleErrors: string[] = [];
  admin.page.on("console", (message) => {
    if (
      message.type() === "error" &&
      /content security policy|refused to apply inline style|blocked by csp/i.test(
        message.text(),
      )
    )
      cspConsoleErrors.push(message.text());
  });
  let anonymous: { context: BrowserContext; page: Page } | undefined;
  let reader: { context: BrowserContext; page: Page } | undefined;
  let originalPublicInstance: boolean | undefined;
  let changedPublicInstance = false;
  let originalProfile: ProfileFields | null = null;
  let changedProfile = false;
  let publisherProfileID: string | undefined;
  let privateRecipeID = "";
  let publicRecipeID = "";
  let privateRecipeDeleted = false;
  let privateCopyID = "";
  let primaryFailure: unknown;

  try {
    await admin.page.goto("/");
    const adminUser = await signInWithSyntheticEmail(
      admin.page,
      admin.page.request,
      "admin-library-detail@example.test",
      "Library route admin",
    );
    expect(adminUser.is_admin).toBe(true);

    const settingsResponse = await admin.page.request.get("/api/admin/settings");
    expect(settingsResponse.status()).toBe(200);
    originalPublicInstance = (
      (await settingsResponse.json()) as { public_instance: boolean }
    ).public_instance;
    if (!originalPublicInstance) {
      await setPublicInstance(admin.page, true);
      changedPublicInstance = true;
    }

    const privateRecipe = await createRecipe(
      admin.page,
      `Library private ${Date.now()}`,
      "private",
    );
    privateRecipeID = privateRecipe.id;
    originalProfile = await readSelfProfile(admin.page);
    changedProfile = true;
    await writeSelfProfile(admin.page, {
      public_name: `Private library publisher ${Date.now()}`,
      bio: originalProfile?.bio ?? "",
      visibility: "private",
    });
    const privatePublisherProfile = await readSelfProfile(admin.page);
    expect(privatePublisherProfile?.visibility).toBe("private");
    publisherProfileID = privatePublisherProfile?.id ?? "";
    const publicRecipe = await createRecipe(
      admin.page,
      `Library public ${Date.now()}`,
      "public",
    );
    publicRecipeID = publicRecipe.id;

    const libraryReads: string[] = [];
    const legacyRecipeFeeds: string[] = [];
    admin.page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname.startsWith("/api/library/"))
        libraryReads.push(`${url.pathname}${url.search}`);
      if (url.pathname === "/api/recipes" && request.method() === "GET")
        legacyRecipeFeeds.push(url.pathname);
    });
    const libraryResponse = await admin.page.goto("/library");
    expect(libraryResponse?.status()).toBe(200);
    expect(libraryResponse?.headers()["cache-control"]).toBe("no-store");
    expect(libraryResponse?.headers()["content-security-policy"]).toContain(
      "script-src 'self'",
    );
    const libraryHTML = (await libraryResponse?.body())?.toString() ?? "";
    expect(libraryHTML).not.toContain(privateRecipe.title);
    await expect(
      admin.page.getByRole("heading", { name: "Your recipes" }),
    ).toBeVisible();
    const typeNavigation = admin.page.getByRole("group", {
      name: "Library type",
    });
    await expect(typeNavigation.getByRole("button", { name: "Recipes" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(typeNavigation.getByRole("button", { name: "Cookbooks" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    expect(libraryReads).toContain("/api/library/recipes?limit=24&offset=0");
    expect(libraryReads.some((path) => path.startsWith("/api/library/cookbooks?"))).toBe(false);
    expect(legacyRecipeFeeds).toEqual([]);

    const search = admin.page.getByRole("searchbox", { name: "Search your recipes" });
    await search.fill("Library private");
    await expect(admin.page).toHaveURL(/\/library\?type=recipes&q=Library\+private$/);
    await expect(admin.page.getByRole("link", { name: `Open ${privateRecipe.title}` })).toBeVisible();
    await admin.page.reload();
    await expect(admin.page.getByRole("searchbox", { name: "Search your recipes" })).toHaveValue("Library private");
    await expect(admin.page.getByRole("link", { name: `Open ${privateRecipe.title}` })).toBeVisible();
    await admin.page.getByRole("button", { name: "Cookbooks" }).click();
    await expect(admin.page).toHaveURL(/\/library\?type=cookbooks$/);
    await expect(admin.page.getByRole("searchbox", { name: "Search your recipes" })).toHaveCount(0);
    await admin.page.goBack();
    await expect(admin.page).toHaveURL(/\/library\?type=recipes&q=Library\+private$/);
    await expect(admin.page.getByRole("searchbox", { name: "Search your recipes" })).toHaveValue("Library private");
    await expect(admin.page.getByRole("link", { name: `Open ${privateRecipe.title}` })).toBeVisible();
    await admin.page.goForward();
    await expect(admin.page.getByRole("searchbox", { name: "Search your recipes" })).toHaveCount(0);
    await admin.page.goBack();
    await expect(admin.page.getByRole("searchbox", { name: "Search your recipes" })).toHaveValue("Library private");
    await admin.page.getByRole("searchbox", { name: "Search your recipes" }).fill("");

    const library = admin.page.locator(
      'section[aria-labelledby="library-collection-title"]',
    );
    const privateEntry = library
      .getByRole("link", { name: `Open ${privateRecipe.title}` })
      .locator("xpath=..");
    await expect(privateEntry).toBeVisible();
    await expect(privateEntry.getByText(/Owned by you/)).toBeVisible();
    await privateEntry.getByRole("button", { name: "Bookmark" }).click();
    await expect(
      privateEntry.getByRole("button", { name: "Remove bookmark" }),
    ).toBeVisible();
    await expect(privateEntry.getByText(/Owned by you/)).toBeVisible();
    await expect(privateEntry.getByText(/Saved to Library/)).toBeVisible();
    await privateEntry.getByRole("button", { name: "Remove bookmark" }).click();
    await expect(
      privateEntry.getByRole("button", { name: "Bookmark" }),
    ).toBeVisible();
    await expect(privateEntry.getByText(/Owned by you/)).toBeVisible();
    await expect(privateEntry.getByText(/Saved to Library/)).toHaveCount(0);
    expect((await admin.page.request.get(`/api/recipes/${privateRecipe.id}`)).status()).toBe(200);

    await privateEntry.getByRole("button", { name: "Bookmark" }).click();
    await expect(
      privateEntry.getByRole("button", { name: "Remove bookmark" }),
    ).toBeVisible();
    await privateEntry.getByRole("link", { name: `Open ${privateRecipe.title}` }).click();
    await expect(admin.page).toHaveURL(new RegExp(`/recipe\\?id=${privateRecipe.id}$`));
    await expect(
      admin.page.getByRole("heading", { name: privateRecipe.title, exact: true }),
    ).toBeVisible();
    await expect(admin.page.getByText("Owned by you")).toBeVisible();
    await expect(admin.page.getByRole("button", { name: "Edit recipe" })).toBeVisible();

    anonymous = await openContext(browser);
    await anonymous.page.goto("/");
    if (publisherProfileID)
      expect((await anonymous.page.request.get(`/api/creator-profiles/${publisherProfileID}`)).status()).toBe(404);
    const anonymousLibraryRequests: string[] = [];
    anonymous.page.on("request", (request) => {
      if (new URL(request.url()).pathname.startsWith("/api/library/"))
        anonymousLibraryRequests.push(request.url());
    });
    const anonymousLibraryResponse = await anonymous.page.goto("/library");
    expect(anonymousLibraryResponse?.status()).toBe(200);
    await expect(
      anonymous.page.getByRole("heading", { name: "Sign in to see your Library" }),
    ).toBeVisible();
    const privateRequests: string[] = [];
    anonymous.page.on("request", (request) => {
      if (new URL(request.url()).pathname.endsWith(privateRecipe.id))
        privateRequests.push(new URL(request.url()).pathname);
    });
    await anonymous.page.goto(`/recipe?id=${privateRecipe.id}`);
    await expect(
      anonymous.page.getByRole("heading", { name: "Recipe unavailable" }),
    ).toBeVisible();
    expect(privateRequests).toContain(`/api/public/recipes/${privateRecipe.id}`);
    expect(privateRequests).not.toContain(`/api/recipes/${privateRecipe.id}`);
    expect(anonymousLibraryRequests).toEqual([]);

    const publicResponse = await anonymous.page.goto(`/recipe?id=${publicRecipe.id}`);
    expect(publicResponse?.headers()["cache-control"]).toBe("no-store");
    await expect(
      anonymous.page.getByRole("heading", { name: publicRecipe.title, exact: true }),
    ).toBeVisible();
    await expect(anonymous.page.getByText("Public on this instance")).toBeVisible();
    await expect(anonymous.page.getByRole("button", { name: "Edit recipe" })).toHaveCount(0);
    await expect(anonymous.page.getByRole("link", { name: "Save to Library" })).toHaveAttribute(
      "href",
      "/?signin=1",
    );

    reader = await openContext(browser);
    await reader.page.goto("/");
    const readerUser = await signInWithSyntheticEmail(
      reader.page,
      reader.page.request,
      `library-reader-${Date.now()}@example.test`,
      "Library reader",
    );
    expect(readerUser.is_admin).toBe(false);
    await reader.page.goto(`/recipe?id=${publicRecipe.id}`);
    await expect(reader.page.getByRole("button", { name: "Create a copy" })).toBeVisible();
    await reader.page.getByRole("button", { name: "Create a copy" }).click();
    await expect(reader.page).toHaveURL(/\/recipe\?id=/);
    await expect(reader.page.getByText("Owned by you · Your copy", { exact: true })).toBeVisible();
    const copyID = new URL(reader.page.url()).searchParams.get("id");
    expect(copyID).toBeTruthy();
    privateCopyID = copyID ?? "";
    const copied = await reader.page.request.get(`/api/recipes/${copyID}`);
    expect(copied.status()).toBe(200);
    const copiedRecipe = (await copied.json()) as {
      owner_id: string;
      source_recipe_id?: string;
      visibility: string;
    };
    expect(copiedRecipe.owner_id).toBe((await reader.page.request.get("/api/auth/me").then((r) => r.json())).id);
    expect(copiedRecipe.source_recipe_id).toBe(publicRecipe.id);
    expect(copiedRecipe.visibility).toBe("private");

    const deletePrivate = await admin.page.request.delete(
      `/api/recipes/${privateRecipe.id}`,
      { headers: { Origin: baseURL } },
    );
    expect(deletePrivate.status()).toBe(200);
    privateRecipeDeleted = true;
    await admin.page.goto("/library");
    const unavailable = admin.page.locator(
      'section[aria-labelledby="library-collection-title"]',
    );
    const unavailableCard = unavailable
      .getByRole("heading", { name: "Unavailable recipe" })
      .locator("xpath=..");
    await expect(unavailableCard).toBeVisible();
    await expect(unavailableCard.getByText(privateRecipe.title)).toHaveCount(0);
    await expect(unavailableCard.getByText(privateRecipe.id, { exact: true })).toHaveCount(0);
    await expect(unavailableCard.getByRole("link")).toHaveCount(0);
    await expect(unavailableCard.getByRole("button")).toHaveCount(1);
    await expect(
      unavailableCard.getByRole("button", {
        name: "Remove unavailable recipe reference",
      }),
    ).toBeVisible();
    expect(cspConsoleErrors).toEqual([]);
  } catch (error) {
    primaryFailure = error;
  }
  const cleanupFailures: string[] = [];
  if (publicRecipeID)
    await attemptCleanup(cleanupFailures, "Delete public recipe fixture", async () => {
      const response = await admin.page.request.delete(`/api/recipes/${publicRecipeID}`, { headers: { Origin: baseURL } });
      expect(response.status()).toBe(200);
    });
  if (privateRecipeID && !privateRecipeDeleted)
    await attemptCleanup(cleanupFailures, "Delete private recipe fixture", async () => {
      const response = await admin.page.request.delete(`/api/recipes/${privateRecipeID}`, { headers: { Origin: baseURL } });
      expect(response.status()).toBe(200);
    });
  const readerFixture = reader;
  if (privateCopyID && readerFixture)
    await attemptCleanup(cleanupFailures, "Delete reader recipe copy", async () => {
      const response = await readerFixture.page.request.delete(`/api/recipes/${privateCopyID}`, { headers: { Origin: baseURL } });
      expect(response.status()).toBe(200);
    });
  if (changedProfile)
    await attemptCleanup(cleanupFailures, "Restore admin profile", async () => {
      await writeSelfProfile(admin.page, originalProfile ?? { public_name: "", bio: "", visibility: "private" });
    });
  if (changedPublicInstance && originalPublicInstance === false)
    await attemptCleanup(cleanupFailures, "Restore public-instance setting", async () => setPublicInstance(admin.page, false));
  const contexts = [anonymous?.context, reader?.context, admin.context].filter((context): context is BrowserContext => Boolean(context));
  const closeResults = await Promise.allSettled(contexts.map((context) => context.close()));
  closeResults.forEach((result) => {
    if (result.status === "rejected") cleanupFailures.push(`Close browser context: ${result.reason instanceof Error ? result.reason.message : "cleanup failed"}`);
  });
  if (primaryFailure) {
    if (primaryFailure instanceof Error && cleanupFailures.length)
      primaryFailure.message += `\nCleanup also reported: ${cleanupFailures.join("; ")}`;
    throw primaryFailure;
  }
  if (cleanupFailures.length) throw new Error(cleanupFailures.join("; "));
});
