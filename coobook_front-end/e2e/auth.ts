import { expect } from "@playwright/test";
import type { APIRequestContext, Page } from "@playwright/test";
import { readFile, stat, unlink } from "node:fs/promises";

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
  await page.goto(payload.sign_in_url);
  await page.getByRole("button", { name: /continue to cookbook/i }).click();
  await expect
    .poll(async () => (await page.request.get("/api/auth/me")).status())
    .toBe(200);
  return (await page.request.get("/api/auth/me").then((result) =>
    result.json(),
  )) as { is_admin: boolean };
}
