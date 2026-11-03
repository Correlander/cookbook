import { expect, test } from "@playwright/test";
import type { Browser, BrowserContext, Page } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";

async function updatePublicInstance(
  page: Page,
  publicInstance: boolean,
): Promise<void> {
  const response = await page.request.patch("/api/admin/settings", {
    headers: { Origin: baseURL },
    data: {
      public_instance: publicInstance,
      ...(publicInstance ? { confirm_publication: true } : {}),
    },
  });
  expect(response.status()).toBe(200);
}

async function openSignedInContext(browser: Browser): Promise<{
  context: BrowserContext;
  page: Page;
}> {
  const context = await browser.newContext({ baseURL });
  return { context, page: await context.newPage() };
}

test("owners can publish and withdraw a public recipe without losing owner access", async ({
  browser,
  page,
}) => {
  const admin = await openSignedInContext(browser);
  let adminContextChangedPublicSetting = false;
  let originalPublicInstance: boolean | undefined;
  let anonymousContext: BrowserContext | undefined;

  try {
    await admin.page.goto("/");
    const adminUser = await signInWithSyntheticEmail(
      admin.page,
      admin.page.request,
      "admin@example.test",
      "Synthetic Admin",
    );
    expect(adminUser.is_admin).toBe(true);
    const settingsResponse = await admin.page.request.get("/api/admin/settings");
    expect(settingsResponse.status()).toBe(200);
    const settings = (await settingsResponse.json()) as {
      public_instance: boolean;
    };
    originalPublicInstance = settings.public_instance;
    if (!originalPublicInstance) {
      await updatePublicInstance(admin.page, true);
      adminContextChangedPublicSetting = true;
    }

    await signInWithSyntheticEmail(
      page,
      page.request,
      `publication-owner-${Date.now()}@example.test`,
      "Synthetic Recipe Owner",
    );
    await page.getByRole("button", { name: "Add a recipe" }).last().click();
    await page.getByLabel("Title").fill(`Publication proof ${Date.now()}`);
    const title = await page.getByLabel("Title").inputValue();
    await page.getByLabel("Ingredients (name | quantity | notes per line)").fill(
      "Synthetic tomatoes",
    );
    await page.getByLabel("Step 1").fill("Slice the tomatoes.");
    await page.getByLabel("Visibility").selectOption("public");
    await page.getByRole("checkbox", { name: /anyone can read/i }).check();
    await page
      .getByRole("checkbox", { name: /not appear in anonymous browsing/i })
      .check();
    const createResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === "/api/recipes",
    );
    await page.getByRole("button", { name: "Save recipe" }).click();
    const createResponse = await createResponsePromise;
    expect(createResponse.status()).toBe(200);
    const createdRecipe = (await createResponse.json()) as {
      id: string;
      title: string;
    };
    expect(createdRecipe.title).toBe(title);
    const recipeId = createdRecipe.id;

    anonymousContext = await browser.newContext({ baseURL });
    const anonymousPage = await anonymousContext.newPage();
    const anonymousPolicyRequests: string[] = [];
    anonymousPage.on("request", (request) => {
      if (request.url().includes(`/api/recipes/${recipeId}/publication`))
        anonymousPolicyRequests.push(request.url());
    });
    const beforePublish = await anonymousPage.request.get(
      "/api/public/recipes?limit=24&offset=0",
    );
    expect(beforePublish.status()).toBe(200);
    const unpublishedRows = (await beforePublish.json()) as Array<{
      id: string;
      title: string;
    }>;
    expect(unpublishedRows.some((recipe) => recipe.id === recipeId)).toBe(false);

    await page.getByRole("button", { name: `Open ${title}` }).click();
    await page.getByRole("button", { name: "Manage publication" }).click();
    await expect(page.getByText("Current status")).toBeVisible();
    await expect(page.getByText("Not published", { exact: true })).toBeVisible();
    await page.getByLabel("Public browsing status").selectOption("published");
    const publishResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "PUT" &&
        new URL(response.url()).pathname ===
          `/api/recipes/${recipeId}/publication`,
    );
    await page.getByRole("button", { name: "Save publication settings" }).click();
    expect((await publishResponsePromise).status()).toBe(200);
    const ownerRead = await page.request.get(`/api/recipes/${recipeId}`);
    expect(ownerRead.status()).toBe(200);

    await anonymousPage.goto("/");
    await expect(
      anonymousPage.getByRole("button", { name: `Open ${title}` }),
    ).toBeVisible();
    await anonymousPage
      .getByRole("button", { name: `Open ${title}` })
      .click();
    await expect(
      anonymousPage.getByRole("dialog", { name: title }),
    ).toBeVisible();
    await expect(
      anonymousPage.getByRole("button", { name: "Manage publication" }),
    ).toHaveCount(0);
    expect(anonymousPolicyRequests).toEqual([]);

    await page.getByLabel("Public browsing status").selectOption("withdrawn");
    await page
      .getByRole("checkbox", { name: /withdrawal stops new distribution/i })
      .check();
    const withdrawResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "PUT" &&
        new URL(response.url()).pathname ===
          `/api/recipes/${recipeId}/publication`,
    );
    await page.getByRole("button", { name: "Save publication settings" }).click();
    expect((await withdrawResponsePromise).status()).toBe(200);

    await anonymousPage.goto("/");
    await expect(
      anonymousPage.getByRole("button", { name: `Open ${title}` }),
    ).toHaveCount(0);
    expect(
      (await anonymousPage.request.get("/api/public/recipes?limit=24&offset=0")).status(),
    ).toBe(200);
    expect((await page.request.get(`/api/recipes/${recipeId}`)).status()).toBe(200);
  } finally {
    try {
      if (adminContextChangedPublicSetting && originalPublicInstance === false) {
        await updatePublicInstance(admin.page, false);
      }
    } finally {
      await Promise.all(
        [anonymousContext, admin.context]
          .filter((context): context is BrowserContext => Boolean(context))
          .map((context) => context.close()),
      );
    }
  }
});
