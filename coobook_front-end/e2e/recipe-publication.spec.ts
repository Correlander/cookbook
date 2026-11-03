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
  let anonymousPage: Page | undefined;
  let title = "";
  let recipeId = "";

  try {
    await test.step("Admin setup and public browsing setting", async () => {
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
    });

    await test.step("Recipe owner sign-in and creation", async () => {
      await signInWithSyntheticEmail(
        page,
        page.request,
        `publication-owner-${Date.now()}@example.test`,
        "Synthetic Recipe Owner",
      );
      await page.getByRole("button", { name: "Add a recipe" }).last().click();
      await page.getByLabel("Title").fill(`Publication proof ${Date.now()}`);
      title = await page.getByLabel("Title").inputValue();
      await page.getByLabel("Ingredients (name | quantity | notes per line)").fill(
        "Synthetic tomatoes",
      );
      await page.getByLabel("Step 1", { exact: true }).fill("Slice the tomatoes.");
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
      recipeId = createdRecipe.id;

      anonymousContext = await browser.newContext({ baseURL });
      anonymousPage = await anonymousContext.newPage();
    });

    const anonPage = anonymousPage!;
    const anonymousPolicyRequests: string[] = [];
    await test.step("Confirm unpublished recipe stays anonymous-hidden", async () => {
      anonPage.on("request", (request) => {
        if (request.url().includes(`/api/recipes/${recipeId}/publication`))
          anonymousPolicyRequests.push(request.url());
      });
      const beforePublish = await anonPage.request.get(
        "/api/public/recipes?limit=24&offset=0",
      );
      expect(beforePublish.status()).toBe(200);
      const unpublishedRows = (await beforePublish.json()) as Array<{
        id: string;
        title: string;
      }>;
      expect(unpublishedRows.some((recipe) => recipe.id === recipeId)).toBe(false);
    });

    await test.step("Open owner recipe dialog", async () => {
      const recipeCard = page.getByRole("button", { name: `Open ${title}` });
      await expect(recipeCard).toBeVisible();
      await recipeCard.click({ timeout: 5_000 });
      await expect(page.getByRole("dialog", { name: title })).toBeVisible();
    });

    const publicationResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        new URL(response.url()).pathname ===
          `/api/recipes/${recipeId}/publication`,
    );
    await test.step("Open owner publication settings", async () => {
      const recipeDialog = page.getByRole("dialog", { name: title });
      const managePublication = recipeDialog.getByRole("button", {
        name: "Manage publication",
      });
      await expect(managePublication).toBeVisible();
      await expect(managePublication).toBeEnabled();
      await managePublication.click();
    });

    await test.step("Verify owner publication GET and strong ETag", async () => {
      const publicationResponse = await publicationResponsePromise;
      expect(publicationResponse.status()).toBe(200);
      const etag = publicationResponse.headers()["etag"] ?? "";
      expect(
        /^"sha256:[a-f0-9]{64}"$/.test(etag),
        "owner publication GET returns a strong ETag through the same-origin proxy",
      ).toBe(true);
    });

    await test.step("Confirm publication controls are ready", async () => {
      await expect(page.getByText("Current status")).toBeVisible();
      const currentStatus = page
        .locator("dl > div")
        .filter({ has: page.getByText("Current status", { exact: true }) })
        .locator("dd");
      await expect(currentStatus).toHaveText("Not published");
      const publicationState = page.getByLabel("Public browsing status");
      await expect(publicationState).toBeVisible();
      await expect(publicationState).toBeEnabled();
      await publicationState.selectOption("published");
    });

    await test.step("Save publication and verify owner access", async () => {
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
    });

    await test.step("Verify anonymous published list and detail", async () => {
      await anonPage.goto("/");
      await expect(
        anonPage.getByRole("button", { name: `Open ${title}` }),
      ).toBeVisible();
      await anonPage
        .getByRole("button", { name: `Open ${title}` })
        .click();
      await expect(
        anonPage.getByRole("dialog", { name: title }),
      ).toBeVisible();
      await expect(
        anonPage.getByRole("button", { name: "Manage publication" }),
      ).toHaveCount(0);
      expect(anonymousPolicyRequests).toEqual([]);
    });

    await test.step("Withdraw and preserve owner access", async () => {
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

      await anonPage.goto("/");
      await expect(
        anonPage.getByRole("button", { name: `Open ${title}` }),
      ).toHaveCount(0);
      expect(
        (await anonPage.request.get("/api/public/recipes?limit=24&offset=0")).status(),
      ).toBe(200);
      expect((await page.request.get(`/api/recipes/${recipeId}`)).status()).toBe(200);
    });
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
