import { expect, test } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";

test("administrators can explicitly change an isolated disabled peer profile", async ({ page }) => {
  const origin = new URL(baseURL).origin;
  const suffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const peerOrigin = `https://retention-ui-${suffix}.example.org`;
  let peerCreated = false;
  const browserMutations: Promise<Record<string, string>>[] = [];

  try {
    await signInWithSyntheticEmail(page, page.request, "admin-retention-peer-profile@example.test", "E2E Admin");
    const created = await page.request.post("/api/admin/federation/peers", {
      headers: { Origin: origin },
      data: { origin: peerOrigin, enabled: false },
    });
    expect(created.status()).toBe(200);
    peerCreated = true;

    const initialResponse = await page.request.get("/api/admin/federation/peers");
    expect(initialResponse.status()).toBe(200);
    const initialPeers = await initialResponse.json() as Array<{
      origin: string;
      enabled: boolean;
      exchange_profile: string;
    }>;
    expect(initialPeers.find((peer) => peer.origin === peerOrigin)).toMatchObject({
      enabled: false,
      exchange_profile: "v1",
    });

    page.on("request", (request) => {
      if (request.url().includes("/api/admin/federation/peers") && request.method() === "PATCH")
        browserMutations.push(request.allHeaders());
    });
    await page.goto("/admin");
    await expect(page.getByRole("heading", { name: "Trusted peers" })).toBeVisible();
    const peerDetails = page.locator("details.admin-settings-note")
      .filter({ hasText: peerOrigin });
    await expect(peerDetails).toHaveCount(1);
    await peerDetails.locator("summary").click();
    const profileSelect = peerDetails.getByLabel(`Profile for ${peerOrigin}`);
    await expect(profileSelect).toHaveValue("v1");
    await profileSelect.selectOption("exchange-v2-retention");

    page.once("dialog", async (dialog) => {
      expect(dialog.message()).toMatch(/protocol capability only.*recipe owner must separately permit personal retention/i);
      await dialog.accept();
    });
    const upgrade = page.waitForResponse((response) =>
      response.request().method() === "PATCH" &&
      new URL(response.url()).pathname === "/api/admin/federation/peers");
    await peerDetails.getByRole("button", { name: "Save exchange profile" }).click();
    expect((await upgrade).status()).toBe(200);
    await expect(peerDetails.getByText("Current exchange profile: Exchange v2 with retention")).toBeVisible();
    const upgraded = await page.request.get("/api/admin/federation/peers");
    expect(upgraded.status()).toBe(200);
    const upgradedPeers = await upgraded.json() as Array<{ origin: string; exchange_profile: string }>;
    expect(upgradedPeers.find((peer) => peer.origin === peerOrigin)?.exchange_profile).toBe("exchange-v2-retention");

    await profileSelect.selectOption("v1");
    page.once("dialog", async (dialog) => {
      expect(dialog.message()).toMatch(/New private snapshot capture eligibility.*public state.*Private copies already retained remain.*in flight/i);
      await dialog.accept();
    });
    const downgrade = page.waitForResponse((response) =>
      response.request().method() === "PATCH" &&
      new URL(response.url()).pathname === "/api/admin/federation/peers");
    await peerDetails.getByRole("button", { name: "Save exchange profile" }).click();
    expect((await downgrade).status()).toBe(200);
    await expect(peerDetails.getByText("Current exchange profile: v1")).toBeVisible();
    const downgraded = await page.request.get("/api/admin/federation/peers");
    expect(downgraded.status()).toBe(200);
    const downgradedPeers = await downgraded.json() as Array<{ origin: string; exchange_profile: string }>;
    expect(downgradedPeers.find((peer) => peer.origin === peerOrigin)?.exchange_profile).toBe("v1");
    expect((await Promise.all(browserMutations)).every((headers) => headers.origin === origin)).toBe(true);
  } finally {
    try {
      if (peerCreated) {
        const removed = await page.request.delete("/api/admin/federation/peers", {
          headers: { Origin: origin },
          data: { origin: peerOrigin },
        });
        expect([200, 204, 404]).toContain(removed.status());
      }
    } finally {
      await page.context().close();
    }
  }
});
