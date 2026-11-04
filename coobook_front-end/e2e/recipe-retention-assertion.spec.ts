import { expect, test } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";

test("owner sees retention eligibility and API refuses a private recipe version", async ({ page }) => {
  const origin = new URL(baseURL).origin;
  const suffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const title = `Private retention eligibility ${suffix}`;
  let recipeID = "";

  try {
    await signInWithSyntheticEmail(
      page,
      page.request,
      `retention-owner-${suffix}@example.test`,
      "Synthetic Recipe Owner",
    );

    await page.getByRole("button", { name: "Add a recipe" }).last().click();
    const recipeForm = page.getByRole("dialog", { name: "Add a recipe" });
    await recipeForm.getByLabel("Title").fill(title);
    await recipeForm.getByLabel("Ingredients (name | quantity | notes per line)").fill(
      "Synthetic beans",
    );
    await recipeForm.getByLabel("Step 1", { exact: true }).fill("Rinse the beans.");
    await recipeForm.getByLabel("Visibility").selectOption("private");
    const anyoneCanRead = recipeForm.getByRole("checkbox", { name: /anyone can read/i });
    if (await anyoneCanRead.isChecked()) await anyoneCanRead.uncheck();

    const createResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === "/api/recipes",
    );
    await recipeForm.getByRole("button", { name: "Save recipe" }).click();
    const createResponse = await createResponsePromise;
    expect(createResponse.status()).toBe(200);
    const createdRecipe = (await createResponse.json()) as { id: string };
    recipeID = createdRecipe.id;

    const recipeCard = page.getByRole("button", { name: `Open ${title}` });
    await expect(recipeCard).toBeVisible();
    await recipeCard.click();
    const recipeDialog = page.getByRole("dialog", { name: title });
    await expect(recipeDialog).toBeVisible();
    await recipeDialog.getByRole("button", { name: "Manage publication" }).click();

    const retentionSection = page
      .getByRole("heading", { name: "Private personal retention" })
      .locator("xpath=..");
    await expect(retentionSection).toBeVisible();
    await expect(
      retentionSection.getByText(/public, published recipe, follower distribution/i),
    ).toBeVisible();
    await expect(
      retentionSection.getByText(/At least one enabled Exchange v2 peer is also required/i),
    ).toBeVisible();
    await expect(
      retentionSection.getByRole("button", { name: /Queue permission for version/ }),
    ).toBeDisabled();

    const recipeResponse = await page.request.get(`/api/recipes/${encodeURIComponent(recipeID)}`);
    expect(recipeResponse.status()).toBe(200);
    const recipe = (await recipeResponse.json()) as { version: number };
    expect(Number.isSafeInteger(recipe.version) && recipe.version > 0).toBe(true);
    await expect(
      retentionSection.getByRole("button", {
        name: `Queue permission for version ${recipe.version}`,
      }),
    ).toBeDisabled();
    const assertionResponse = await page.request.post(
      `/api/recipes/${encodeURIComponent(recipeID)}/federation-retention`,
      {
        headers: { Origin: origin },
        data: { expected_recipe_version: recipe.version },
      },
    );
    expect(assertionResponse.status()).toBe(409);
    expect(await assertionResponse.json()).toEqual({ error: "federation_assertion_conflict" });
    await expect(retentionSection.getByRole("alert")).toHaveCount(0);
    await expect(retentionSection.getByRole("status")).toHaveCount(0);
  } finally {
    try {
      if (recipeID) {
        const removed = await page.request.delete(
          `/api/recipes/${encodeURIComponent(recipeID)}`,
          { headers: { Origin: origin } },
        );
        expect([200, 404]).toContain(removed.status());
      }
    } finally {
      await page.context().close();
    }
  }
});
