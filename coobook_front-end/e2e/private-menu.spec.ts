import { expect, test } from "@playwright/test";
import type { Browser, BrowserContext, Page } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";

async function openContext(browser: Browser): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ baseURL });
  return { context, page: await context.newPage() };
}

async function createPrivateRecipe(page: Page, title: string): Promise<{ id: string }> {
  const response = await page.request.post("/api/recipes", {
    headers: { Origin: baseURL },
    data: {
      title,
      description: "Private menu journey fixture.",
      servings: 2,
      prep_time_minutes: 5,
      cook_time_minutes: 10,
      tags: [],
      visibility: "private",
      edit_policy: "owner_only",
      cookbook_ids: [],
      ingredients: [{ name: "Tomato", quantity: "2" }],
      steps: [{ text: "Cook the tomatoes." }, { text: "Season with salt." }],
      initial_publication: { publication_state: "unpublished", federation_mode: "off", index_eligible: false },
    },
  });
  expect(response.status()).toBe(200);
  const value: unknown = await response.json();
  if (!value || typeof value !== "object" || !("id" in value) || typeof value.id !== "string")
    throw new Error("The recipe API returned an invalid private recipe.");
  return { id: value.id };
}

test("ordinary owners create, order, reopen, and cook from private menus while others remain denied", async ({ browser }) => {
  const creator = await openContext(browser);
  let reader: { context: BrowserContext; page: Page } | undefined;
  let anonymous: { context: BrowserContext; page: Page } | undefined;
  let recipe: { id: string } | undefined;
  let menuID = "";
  let menuTitle = "";
  let menuCreateSucceeded = false;
  let menuCreateBody = "";
  let primaryFailure: unknown;
  const cleanupFailures: string[] = [];

  try {
    await creator.page.goto("/");
    const creatorUser = await signInWithSyntheticEmail(creator.page, creator.page.request, "private-menu-creator@example.test", "Private menu creator");
    expect(creatorUser.is_admin).toBe(false);
    recipe = await createPrivateRecipe(creator.page, `Private menu recipe ${Date.now()}`);

    await creator.page.goto("/create?type=menus");
    creator.page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/menus" && request.method() === "POST") menuCreateBody = request.postData() ?? "";
    });
    creator.page.on("response", (response) => {
      const request = response.request();
      if (new URL(response.url()).pathname === "/api/menus" && request.method() === "POST" && response.status() === 201)
        menuCreateSucceeded = true;
    });
    menuTitle = `Dinner menu ${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const menuForm = creator.page.getByRole("dialog").locator("form");
    await menuForm.getByLabel("Title", { exact: true }).fill(menuTitle);
    await menuForm.getByLabel("Note", { exact: true }).fill("Keep the courses in order.");
    await creator.page.getByRole("combobox", { name: "Creator credit" }).selectOption("local_profile");
    await creator.page.getByRole("button", { name: "Create menu" }).click();
    await expect(creator.page).toHaveURL(/\/menu\?id=[a-f0-9]{32}$/);
    menuID = new URL(creator.page.url()).searchParams.get("id") ?? "";
    expect(menuID).toMatch(/^[a-f0-9]{32}$/);
    await expect(creator.page.getByRole("heading", { name: menuTitle })).toBeVisible();
    expect(JSON.parse(menuCreateBody)).toMatchObject({ creator_attribution: { kind: "local_profile" } });
    const staticShell = await creator.page.request.get(`/menu?id=${menuID}`);
    expect(staticShell.status()).toBe(200);
    expect(staticShell.headers()["cache-control"]).toBe("no-store");
    expect(staticShell.headers()["content-security-policy"]).toContain("script-src 'self'");
    expect(await staticShell.text()).not.toContain("Dinner menu");

    const picker = creator.page.getByRole("searchbox", { name: "Search local recipes" });
    await picker.fill("Private menu recipe");
    await creator.page.getByRole("button", { name: new RegExp(`Add Private menu recipe`) }).click();
    const firstEntry = creator.page.getByRole("form", { name: /Add Private menu recipe/ });
    await firstEntry.getByLabel("Label", { exact: true }).fill("First course");
    await firstEntry.getByLabel("Note", { exact: true }).fill("Serve first.");
    await firstEntry.getByLabel("Serving note", { exact: true }).fill("Two portions.");
    await firstEntry.getByRole("button", { name: "Add recipe to menu" }).click();

    await picker.fill("Private menu recipe");
    await creator.page.getByRole("button", { name: new RegExp(`Add Private menu recipe`) }).click();
    const secondEntry = creator.page.getByRole("form", { name: /Add Private menu recipe/ });
    await secondEntry.getByLabel("Label", { exact: true }).fill("Second course");
    await secondEntry.getByLabel("Note", { exact: true }).fill("Serve second.");
    await secondEntry.getByRole("button", { name: "Add recipe to menu" }).click();
    await expect(creator.page.getByText("Serve first.")).toBeVisible();
    await expect(creator.page.getByText("Serve second.")).toBeVisible();
    await creator.page.getByRole("button", { name: "Move Second course up" }).click();
    const orderedEntries = creator.page.locator("article[aria-labelledby='private-menu-title'] ol > li");
    const secondCourse = orderedEntries.filter({ has: creator.page.getByRole("heading", { name: "Second course", exact: true }) });
    const firstCourse = orderedEntries.filter({ has: creator.page.getByRole("heading", { name: "First course", exact: true }) });
    await expect(secondCourse).toBeVisible();
    await expect(firstCourse).toBeVisible();
    await expect(secondCourse).toHaveCount(1);
    await expect(firstCourse).toHaveCount(1);
    expect(await secondCourse.evaluate((entry) => Array.from(entry.parentElement?.children ?? []).indexOf(entry))).toBe(0);
    expect(await firstCourse.evaluate((entry) => Array.from(entry.parentElement?.children ?? []).indexOf(entry))).toBe(1);

    await creator.page.getByRole("button", { name: "Edit menu" }).click();
    const editMenuForm = creator.page.getByRole("dialog").locator("form");
    await editMenuForm.getByLabel("Title", { exact: true }).fill("Dinner menu revised");
    await creator.page.getByRole("button", { name: "Save menu" }).click();
    await expect(creator.page.getByRole("heading", { name: "Dinner menu revised" })).toBeVisible();
    await creator.page.reload();
    await expect(creator.page.getByRole("heading", { name: "Dinner menu revised" })).toBeVisible();
    await expect(secondCourse.getByText("Serve second.", { exact: true })).toBeVisible();

    await creator.page.goto("/library?type=menus");
    await expect(creator.page.getByRole("link", { name: "Open Dinner menu revised" })).toBeVisible();
    await expect(creator.page.getByText("Owned by you · 2 recipes")).toBeVisible();
    await creator.page.getByRole("link", { name: "Open Dinner menu revised" }).click();
    await expect(creator.page).toHaveURL(new RegExp(`/menu\\?id=${menuID}$`));
    const recipeLink = firstCourse.getByRole("link", { name: /Private menu recipe/ });
    await expect(recipeLink).toHaveCount(1);
    await recipeLink.click();
    await expect(creator.page.getByRole("button", { name: "Start cooking mode" })).toBeVisible();
    await creator.page.getByRole("button", { name: "Start cooking mode" }).click();
    const cookingDialog = creator.page.getByRole("dialog").filter({
      has: creator.page.getByText("Cooking mode", { exact: true }),
    });
    await expect(cookingDialog).toBeVisible();
    await expect(cookingDialog).toHaveAccessibleName(/Private menu recipe/);
    await expect(cookingDialog.getByText("Cooking mode", { exact: true })).toBeVisible();
    await expect(cookingDialog.getByRole("button", { name: "Next step" })).toBeVisible();
    await cookingDialog.getByRole("button", { name: "Next step" }).click();
    await expect(cookingDialog.getByText("Step 2 of 2")).toBeVisible();
    await expect(cookingDialog.getByRole("heading", { name: "Season with salt." })).toBeVisible();
    await expect(cookingDialog.getByRole("button", { name: "Finished" })).toBeVisible();

    reader = await openContext(browser);
    await reader.page.goto("/");
    const readerUser = await signInWithSyntheticEmail(reader.page, reader.page.request, "private-menu-reader@example.test", "Private menu reader");
    expect(readerUser.is_admin).toBe(false);
    const readerAPI = await reader.page.request.get(`/api/menus/${menuID}`, { headers: { Origin: baseURL } });
    expect(readerAPI.status()).toBe(404);
    await reader.page.goto(`/menu?id=${menuID}`);
    await expect(reader.page.getByRole("alert")).toContainText(/unavailable|could not load/i);
    await expect(reader.page.getByRole("heading", { name: "Dinner menu revised" })).toHaveCount(0);

    anonymous = await openContext(browser);
    const anonymousAPI = await anonymous.page.request.get(`/api/menus/${menuID}`, { headers: { Origin: baseURL } });
    expect(anonymousAPI.status()).toBe(401);
    await anonymous.page.goto(`/menu?id=${menuID}`);
    await expect(anonymous.page.getByRole("heading", { name: "Sign in to view this private menu" })).toBeVisible();
    await expect(anonymous.page.getByRole("heading", { name: "Dinner menu revised" })).toHaveCount(0);
  } catch (error) {
    primaryFailure = error;
  } finally {
    const originHeaders = { Origin: baseURL };
    if (!menuID && menuCreateSucceeded) {
      try {
        const page = await creator.page.request.get("/api/library/menus?limit=100&offset=0", { headers: originHeaders });
        if (!page.ok()) cleanupFailures.push(`Could not rediscover created menu for cleanup (HTTP ${page.status()}).`);
        else {
          const value: unknown = await page.json();
          if (!value || typeof value !== "object" || !("menus" in value) || !Array.isArray(value.menus))
            cleanupFailures.push("Could not parse the owner menu list while rediscovering the created menu for cleanup.");
          else {
            const created = value.menus.find((menu) => menu && typeof menu === "object" && "title" in menu && menu.title === menuTitle);
            if (!created || typeof created !== "object" || !("id" in created) || typeof created.id !== "string")
              cleanupFailures.push("The successful menu creation was not found in the first owner menu page during cleanup.");
            else menuID = created.id;
          }
        }
      } catch (error) {
        cleanupFailures.push(error instanceof Error ? error.message : "Menu cleanup rediscovery failed.");
      }
    }
    if (menuID) {
      try {
        const latest = await creator.page.request.get(`/api/menus/${menuID}`, { headers: originHeaders });
        if (!latest.ok()) cleanupFailures.push(`Could not read created menu for cleanup (HTTP ${latest.status()}).`);
        else {
          const value: unknown = await latest.json();
          if (!value || typeof value !== "object" || !("version" in value) || typeof value.version !== "number")
            cleanupFailures.push("Could not parse created menu version for cleanup.");
          else {
            const deletion = await creator.page.request.delete(`/api/menus/${menuID}?version=${value.version}`, { headers: originHeaders });
            if (deletion.status() !== 200) cleanupFailures.push(`Created menu cleanup failed (HTTP ${deletion.status()}).`);
          }
        }
      } catch (error) {
        cleanupFailures.push(error instanceof Error ? error.message : "Menu cleanup failed.");
      }
    }
    if (recipe) {
      try {
        const deletion = await creator.page.request.delete(`/api/recipes/${recipe.id}`, { headers: originHeaders });
        if (!deletion.ok()) cleanupFailures.push(`Created recipe cleanup failed (HTTP ${deletion.status()}).`);
      } catch (error) {
        cleanupFailures.push(error instanceof Error ? error.message : "Recipe cleanup failed.");
      }
    }
    await anonymous?.context.close();
    await reader?.context.close();
    await creator.context.close();
  }
  if (primaryFailure) {
    if (cleanupFailures.length && primaryFailure instanceof Error)
      primaryFailure.message += `\nCleanup also reported: ${cleanupFailures.join("; ")}`;
    throw primaryFailure;
  }
  if (cleanupFailures.length) throw new Error(cleanupFailures.join("; "));
});
