import { expect } from "@playwright/test";
import type { APIRequestContext, APIResponse, Page } from "@playwright/test";
import { readFile, stat, unlink } from "node:fs/promises";
import type { AuthenticatedUser } from "../src/apiModels";

function isAuthenticatedUser(value: unknown): value is AuthenticatedUser {
  if (!value || typeof value !== "object") return false;
  const user = value as Partial<AuthenticatedUser>;
  return (
    typeof user.id === "string" &&
    typeof user.email === "string" &&
    typeof user.display_name === "string" &&
    typeof user.is_admin === "boolean"
  );
}

async function readAuthenticatedUser(
  request: APIRequestContext,
): Promise<AuthenticatedUser> {
  const response = await request.get("/api/auth/me");
  expect(response.status()).toBe(200);
  const value: unknown = await response.json();
  if (!isAuthenticatedUser(value))
    throw new Error("/api/auth/me returned an invalid authenticated-user response");
  return value;
}

export async function signInWithSyntheticEmail(
  page: Page,
  request: APIRequestContext,
  email: string,
  displayName: string,
): Promise<{ is_admin: boolean }> {
  const origin = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";
  const response = await request.post("/api/auth/email", {
    headers: { Origin: origin },
    data: { email, display_name: displayName },
  });
  expect(response.status()).toBe(200);

  const capturePath = process.env.E2E_EMAIL_CAPTURE;
  if (!capturePath) throw new Error("E2E_EMAIL_CAPTURE is missing");
  let payload: { sign_in_url: string } | undefined;
  for (let attempt = 0; attempt < 50; attempt++) {
    try {
      await stat(capturePath);
      payload = JSON.parse(await readFile(capturePath, "utf8")) as {
        sign_in_url: string;
      };
      break;
    } catch {
      await page.waitForTimeout(50);
    }
  }
  if (!payload?.sign_in_url)
    throw new Error("synthetic sign-in link was not captured");
  await unlink(capturePath);

  const configResponse = await request.get("/api/public/config");
  let passkeySignInEnabled = false;
  if (configResponse.ok()) {
    const config: unknown = await configResponse.json();
    if (config && typeof config === "object" && "auth_methods" in config) {
      const authMethods = config.auth_methods;
      passkeySignInEnabled =
        authMethods !== null &&
        typeof authMethods === "object" &&
        "passkey" in authMethods &&
        authMethods.passkey === true;
    }
  }

  let releasePasskeyCheck!: () => void;
  const passkeyCheckGate = new Promise<void>((resolve) => {
    releasePasskeyCheck = resolve;
  });
  let resolvePasskeyResponse!: (response: APIResponse) => void;
  let rejectPasskeyResponse!: (error: unknown) => void;
  const passkeyResponsePromise = new Promise<APIResponse>((resolve, reject) => {
    resolvePasskeyResponse = resolve;
    rejectPasskeyResponse = reject;
  });
  await page.route("**/api/auth/passkeys", async (route) => {
    try {
      const passkeysResponse = await route.fetch();
      resolvePasskeyResponse(passkeysResponse);
      await passkeyCheckGate;
      await route.fulfill({ response: passkeysResponse });
    } catch (error: unknown) {
      rejectPasskeyResponse(error);
      throw error;
    }
  });

  try {
    await page.goto(payload.sign_in_url);
    await expect(
      page.getByRole("heading", { name: "Sign-in confirmation", level: 1 }),
    ).toBeVisible();
    await page.getByRole("button", { name: /continue to cookbook/i }).click();
    await expect
      .poll(async () => (await page.request.get("/api/auth/me")).status())
      .toBe(200);
    const confirmedUser = await readAuthenticatedUser(page.request);
    const expectedEmail = email.trim().toLowerCase();
    expect(confirmedUser.email).toBe(expectedEmail);

    const passkeysResponse = await passkeyResponsePromise;
    let hasValidEmptyPasskeyList = false;
    if (passkeysResponse.ok()) {
      const registeredPasskeys: unknown = await passkeysResponse.json();
      hasValidEmptyPasskeyList =
        Array.isArray(registeredPasskeys) && registeredPasskeys.length === 0;
    }
    const shouldOfferPasskeySetup =
      passkeySignInEnabled && hasValidEmptyPasskeyList;
    const continueHome = page.getByRole("button", {
      name: "Continue to Home",
    });
    await expect(continueHome).toBeVisible();

    if (shouldOfferPasskeySetup) {
      releasePasskeyCheck();
      const passkeyPrompt = page.getByRole("region", {
        name: "Add a passkey?",
      });
      await expect(
        page.getByRole("heading", { name: "Add a passkey?" }),
      ).toBeVisible();
      await expect(passkeyPrompt).toBeVisible();
      await passkeyPrompt.getByRole("button", { name: "Not now" }).click();
    } else {
      await continueHome.click();
      releasePasskeyCheck();
    }

    await expect(page).toHaveURL(/\/$/);
    const homeUser = await readAuthenticatedUser(page.request);
    expect(homeUser.id).toBe(confirmedUser.id);
    expect(homeUser.email).toBe(expectedEmail);
    const expectedDisplayName = homeUser.display_name || "cook";
    await expect(
      page.getByRole("heading", {
        name: `Welcome back, ${expectedDisplayName}.`,
        level: 1,
      }),
    ).toBeVisible();
    return { is_admin: homeUser.is_admin };
  } finally {
    releasePasskeyCheck();
    await page.unroute("**/api/auth/passkeys");
  }
}
