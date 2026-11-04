import { expect, test } from "@playwright/test";

const publicBook = {
  id: "synthetic-public-book",
  title: "Seasonal table",
  description: "A few public recipes.",
  recipe_count: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const publicRecipe = {
  id: "synthetic-public-recipe",
  creator_name: "Test cook",
  title: "Tomato salad",
  description: "Fresh and simple.",
  ingredients: [{ name: "Tomatoes" }],
  steps: [{ text: "Slice the tomatoes." }],
  servings: 2,
  prep_time_minutes: 5,
  cook_time_minutes: 0,
  tags: ["summer"],
  visibility: "public",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

test("static cookbook pages support direct load, refresh, navigation, and browser back", async ({ page }) => {
  const requests: string[] = [];
  await page.route("**/api/auth/me", async (route) => {
    requests.push("/api/auth/me");
    await route.fulfill({ status: 401, contentType: "application/json", body: "{}" });
  });
  await page.route("**/api/public/cookbooks?**", async (route) => {
    requests.push(new URL(route.request().url()).pathname + new URL(route.request().url()).search);
    await route.fulfill({ status: 200, contentType: "application/json", json: [publicBook] });
  });
  await page.route("**/api/public/cookbooks/synthetic-public-book", async (route) => {
    requests.push(new URL(route.request().url()).pathname);
    await route.fulfill({ status: 200, contentType: "application/json", json: publicBook });
  });
  await page.route("**/api/public/cookbooks/synthetic-public-book/recipes?**", async (route) => {
    requests.push(new URL(route.request().url()).pathname);
    await route.fulfill({ status: 200, contentType: "application/json", json: [publicRecipe] });
  });

  for (const alias of ["/cookbooks", "/cookbooks.html"]) {
    const response = await page.goto(`${alias}?q=summer`);
    expect(response?.status()).toBe(200);
    expect(response?.headers()["cache-control"]).toBe("no-store");
    await expect(page).toHaveTitle("Cookbooks — Cookbook");
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", "noindex,nofollow");
    await expect(page.getByRole("heading", { name: "Browse public cookbooks" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Home", exact: true })).toHaveAttribute("href", "/");
    await expect(page.getByRole("link", { name: "Discover", exact: true })).toHaveAttribute("href", "/discover");
    await expect(page.getByRole("link", { name: "Library", exact: true })).toHaveAttribute("href", "/library");
    await expect(page.getByRole("link", { name: "Cookbooks", exact: true })).toHaveCount(0);
    expect((await response?.text()) ?? "").not.toContain(publicBook.title);
    await page.reload();
    await expect(page.getByRole("heading", { name: "Browse public cookbooks" })).toBeVisible();
  }

  const openCookbook = page.getByRole("link", { name: "Open cookbook" });
  await openCookbook.focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/cookbook\?id=synthetic-public-book&return_q=summer$/);
  const detailResponse = await page.reload();
  expect(detailResponse?.status()).toBe(200);
  expect(detailResponse?.headers()["cache-control"]).toBe("no-store");
  expect((await detailResponse?.text()) ?? "").not.toContain(publicBook.title);
  await expect(page.getByRole("heading", { name: "Seasonal table" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Open Tomato salad" })).toHaveAttribute(
    "href",
    "/recipe?id=synthetic-public-recipe",
  );
  await page.getByRole("link", { name: "Back to cookbooks" }).click();
  await expect(page).toHaveURL(/\/cookbooks\?q=summer$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/cookbook\?id=synthetic-public-book&return_q=summer$/);
  await expect(page.getByRole("heading", { name: "Seasonal table" })).toBeVisible();
  const aliasResponse = await page.goto("/cookbook.html?id=synthetic-public-book&return_q=summer");
  expect(aliasResponse?.status()).toBe(200);
  expect(aliasResponse?.headers()["cache-control"]).toBe("no-store");
  await expect(page.getByRole("heading", { name: "Seasonal table" })).toBeVisible();

  expect(requests).toContain("/api/public/cookbooks?limit=24&offset=0&q=summer");
  expect(requests).toContain("/api/public/cookbooks/synthetic-public-book");
  expect(requests).toContain("/api/public/cookbooks/synthetic-public-book/recipes");
  expect(requests.some((path) => path.startsWith("/api/cookbooks"))).toBe(false);
});

test("signed-in legacy cookbook hashes land on the access collection without public reads", async ({ page }) => {
  const requests: string[] = [];
  const owner = {
    id: "synthetic-owner",
    email: "owner@example.test",
    display_name: "Owner",
    is_admin: false,
  };
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    requests.push(url.pathname + url.search);
    if (url.pathname === "/api/auth/me")
      return route.fulfill({ status: 200, contentType: "application/json", json: owner });
    if (url.pathname === "/api/auth/config")
      return route.fulfill({ status: 200, contentType: "application/json", json: {
        auth_methods: { email_link: true, passkey: true },
        registration_policy: "open",
        public_instance: false,
        courses_enabled: false,
      } });
    if (url.pathname === "/api/instance/authority")
      return route.fulfill({ status: 200, contentType: "application/json", json: {
        is_instance_owner: false,
        is_admin: false,
        owner_claimed: false,
        can_manage_instance_roles: false,
      } });
    if (url.pathname === "/api/cookbooks")
      return route.fulfill({ status: 200, contentType: "application/json", json: [{
        id: "home-book",
        owner_id: owner.id,
        title: "My kitchen book",
        description: "",
        visibility: "private",
      }] });
    if (url.pathname === "/api/recipes")
      return route.fulfill({ status: 200, contentType: "application/json", json: [] });
    return route.fulfill({ status: 404, contentType: "application/json", json: {} });
  });

  await page.goto("/#cookbooks");
  await expect(page).toHaveURL(/\/#cookbooks$/);
  const collection = page.locator("#cookbooks");
  await expect(collection.getByRole("heading", { name: "Cookbooks you can access" })).toBeVisible();
  await expect(collection.getByText("My kitchen book")).toBeVisible();
  await expect(collection.getByRole("link", { name: "Open cookbook" })).toHaveAttribute(
    "href",
    "/cookbook?id=home-book",
  );

  await page.goto("/#my-cookbooks");
  await expect(page).toHaveURL(/\/#my-cookbooks$/);
  const myCookbooksAnchor = page.locator("#my-cookbooks");
  await expect(myCookbooksAnchor.getByRole("heading", { name: "Cookbooks you can access" })).toBeVisible();
  await expect(collection.getByText("My kitchen book")).toBeVisible();
  expect(requests).toContain("/api/cookbooks?limit=24&offset=0");
  expect(requests.some((path) => path.startsWith("/api/public/cookbooks"))).toBe(false);
});
