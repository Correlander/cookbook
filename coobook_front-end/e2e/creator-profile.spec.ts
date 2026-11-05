import { expect, test } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

test("creator profile static roots are private shells and require a confirmed session", async ({
  page,
}) => {
  const calls: string[] = [];
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    calls.push(url.pathname);
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      json: { error: "unauthorized" },
    });
  });
  for (const path of [
    "/account/creator-profile",
    "/account/creator-profile.html",
  ]) {
    const response = await page.goto(path);
    expect(response?.status()).toBe(200);
    expect(response?.headers()["cache-control"]).toBe("no-store");
    await expect(page.locator('meta[name="robots"]')).toHaveAttribute(
      "content",
      "noindex,nofollow",
    );
    await expect(
      page.getByRole("heading", { name: "Sign in to continue" }),
    ).toBeVisible();
    const shell = (await response?.text()) ?? "";
    expect(shell).not.toContain("private-creator-id");
    expect(shell).not.toContain("creator profile bio");
  }
  const response = await page.goto("/creator?id=private-creator-id");
  expect(response?.status()).toBe(200);
  expect(response?.headers()["cache-control"]).toBe("no-store");
  await expect(
    page.getByRole("heading", { name: "Sign in to continue" }),
  ).toBeVisible();
  expect(calls).toContain("/api/auth/me");
  expect(calls.some((path) => path.startsWith("/api/creator-profiles/"))).toBe(
    false,
  );
  expect(calls).toContain("/api/public/config");
  expect(
    calls.filter(
      (path) => path.startsWith("/api/public/") && path !== "/api/public/config",
    ),
  ).toEqual([]);
});

test("authenticated instance profile renders typed attribution and a 404 stays generic", async ({
  page,
}) => {
  await page.route("**/api/auth/me", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      json: {
        id: "reader",
        email: "reader@example.test",
        display_name: "Reader",
        is_admin: false,
      },
    }),
  );
  const calls: string[] = [];
  await page.route("**/api/creator-profiles/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    calls.push(path);
    if (path.endsWith("/missing")) {
      await route.fulfill({
        status: 404,
        contentType: "application/json",
        json: { error: "not_found" },
      });
      return;
    }
    const profile = {
      id: "p1",
      public_name: "Baker",
      bio: "Local bio",
      visibility: "instance",
      created_at: "",
      updated_at: "",
    };
    if (path.endsWith("/recipes"))
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: {
          profile,
          items: [
            {
              recipe: { id: "r1", title: "Soup" },
              role: "creator",
              provenance: "self_asserted_at_creation",
            },
          ],
          limit: 24,
          offset: 0,
        },
      });
    else if (path.endsWith("/cookbooks"))
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: { profile, items: [], limit: 24, offset: 0 },
      });
    else
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: { profile },
      });
  });
  await page.route("**/api/account/creator-profile", (route) =>
    route.fulfill({
      status: 404,
      contentType: "application/json",
      json: { error: "not_found" },
    }),
  );
  await page.goto("/creator?id=p1");
  await expect(page.getByRole("heading", { name: "Baker" })).toBeVisible();
  await expect(
    page.getByText("Same-instance profile · signed-in viewers"),
  ).toBeVisible();
  await expect(
    page.getByText(/Creator · self-asserted when created/),
  ).toBeVisible();
  expect(calls).toContain("/api/creator-profiles/p1/recipes");
  expect(calls).toContain("/api/creator-profiles/p1/cookbooks");

  await page.goto("/creator?id=missing");
  await expect(
    page.getByRole("heading", { name: "Creator profile unavailable" }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "This profile could not be found or is not available to you.",
    ),
  ).toBeVisible();
  expect(
    calls.filter((path) => path === "/api/creator-profiles/missing"),
  ).toHaveLength(1);
  expect(calls.some((path) => path.startsWith("/api/public/"))).toBe(false);
});

test("self-profile API rejects anonymous reads and hostile-origin writes without mutation", async ({
  page,
  request,
}) => {
  const anonymous = await request.get("/api/account/creator-profile");
  expect(anonymous.status()).toBe(401);
  expect((await anonymous.json()).error).toBe("unauthorized");

  await signInWithSyntheticEmail(
    page,
    request,
    "creator-profile-csrf@example.test",
    "Creator Profile CSRF",
  );
  const before = await page.request.get("/api/account/creator-profile");
  expect(before.status()).toBe(200);
  expect((await before.json()).profile).toBeNull();

  const hostileOriginWrite = await page.request.put(
    "/api/account/creator-profile",
    {
      headers: { Origin: "http://attacker.invalid" },
      data: {
        public_name: "Attempted profile",
        bio: "",
        visibility: "private",
      },
    },
  );
  expect(hostileOriginWrite.status()).toBe(403);
  expect((await hostileOriginWrite.json()).error).toBe("csrf_origin_mismatch");

  const afterHostileOrigin = await page.request.get(
    "/api/account/creator-profile",
  );
  expect(afterHostileOrigin.status()).toBe(200);
  expect((await afterHostileOrigin.json()).profile).toBeNull();

  const attempted = await page.evaluate(async () => {
    const response = await fetch("/api/account/creator-profile", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        public_name: "",
        bio: "",
        visibility: "instance",
      }),
    });
    return {
      status: response.status,
      body: (await response.json()) as { error?: string },
    };
  });
  expect(attempted.status).toBe(400);
  expect(attempted.body.error).toBe("invalid_creator_profile");

  const after = await page.request.get("/api/account/creator-profile");
  expect(after.status()).toBe(200);
  expect((await after.json()).profile).toBeNull();
});
