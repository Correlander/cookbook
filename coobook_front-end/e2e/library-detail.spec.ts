import { expect, test } from "@playwright/test";
import type { Browser, BrowserContext, Page } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";

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

  try {
    await admin.page.goto("/");
    const adminUser = await signInWithSyntheticEmail(
      admin.page,
      admin.page.request,
      "admin@example.test",
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
    const publicRecipe = await createRecipe(
      admin.page,
      `Library public ${Date.now()}`,
      "public",
    );

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
    const typeNavigation = admin.page.getByRole("navigation", {
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

    const library = admin.page.locator(
      'section[aria-labelledby="library-collection-title"]',
    );
    const privateEntry = library
      .getByRole("link", { name: `Open ${privateRecipe.title}` })
      .locator("xpath=..");
    await expect(privateEntry).toBeVisible();
    await expect(privateEntry.getByText(/Owned by you/)).toBeVisible();
    await privateEntry.getByRole("button", { name: "Save to Library" }).click();
    await expect(
      privateEntry.getByRole("button", { name: "Remove from Library" }),
    ).toBeVisible();
    await expect(privateEntry.getByText(/Owned by you/)).toBeVisible();
    await expect(privateEntry.getByText(/Saved to Library/)).toBeVisible();
    await privateEntry.getByRole("button", { name: "Remove from Library" }).click();
    await expect(
      privateEntry.getByRole("button", { name: "Save to Library" }),
    ).toBeVisible();
    await expect(privateEntry.getByText(/Owned by you/)).toBeVisible();
    await expect(privateEntry.getByText(/Saved to Library/)).toHaveCount(0);
    expect((await admin.page.request.get(`/api/recipes/${privateRecipe.id}`)).status()).toBe(200);

    await privateEntry.getByRole("button", { name: "Save to Library" }).click();
    await expect(
      privateEntry.getByRole("button", { name: "Remove from Library" }),
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
  } finally {
    try {
      if (changedPublicInstance && originalPublicInstance === false)
        await setPublicInstance(admin.page, false);
    } finally {
      await Promise.all(
        [anonymous?.context, reader?.context, admin.context]
          .filter((context): context is BrowserContext => Boolean(context))
          .map((context) => context.close()),
      );
    }
  }
});
