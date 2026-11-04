import { expect, test } from "@playwright/test";
import type { APIResponse, Browser, BrowserContext, Page, Request, Response as PlaywrightResponse } from "@playwright/test";
import { signInWithSyntheticEmail } from "./auth";

const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:4178";
const fixtureOrigin = "https://snapshot-source.example.org";
const titles = {
  conditional: "Snapshot journey soup",
  reference: "Explicit capture stew",
  restricted: "Restricted snapshot tart",
  removed: "Removed snapshot bread",
  removable: "Removal snapshot rice",
};
const ids = {
  conditional: "10101010101010101010101010101010",
  reference: "20202020202020202020202020202020",
  restricted: "30303030303030303030303030303030",
  removed: "40404040404040404040404040404040",
  removable: "50505050505050505050505050505050",
};

type Session = { context: BrowserContext; page: Page };

async function responseErrorCode(response: { json: () => Promise<unknown> }): Promise<string> {
  const body: unknown = await response.json().catch(() => null);
  return typeof body === "object" && body !== null && "error" in body && typeof body.error === "string"
    ? body.error
    : "unavailable";
}

async function apiResponseContext(response: PlaywrightResponse, stage: string, endpoint: string): Promise<string> {
  const error = await responseErrorCode(response);
  const headers = await response.request().allHeaders();
  const refererOrigin = headers.referer ? new URL(headers.referer).origin : "unavailable";
  return JSON.stringify({
    stage,
    endpoint,
    method: response.request().method(),
    status: response.status(),
    error,
    origin: headers.origin ?? "unavailable",
    requestHost: new URL(response.url()).host,
    refererOrigin,
  });
}

async function expectAPIRequestStatus(
  response: APIResponse,
  expectedStatus: number,
  stage: string,
  method: string,
  endpoint: string,
): Promise<void> {
  const status = response.status();
  const error = status === expectedStatus ? "none" : await responseErrorCode(response);
  const context = JSON.stringify({ stage, method, endpoint, status, error });
  expect(status, `${stage} response: ${context}`).toBe(expectedStatus);
}

async function openContext(browser: Browser): Promise<Session> {
  const context = await browser.newContext({ baseURL });
  return { context, page: await context.newPage() };
}

function objectURI(id: string): string {
  return `${fixtureOrigin}/federation/recipes/${id}`;
}

async function saveReference(page: Page, recipeID: string): Promise<string> {
  const response = await page.request.post("/api/library/bookmarks", {
    headers: { Origin: baseURL },
    data: { target: { remote_object_uri: objectURI(recipeID) } },
  });
  await expectAPIRequestStatus(response, 201, "save-reference-fixture", "POST", "/api/library/bookmarks");
  const saved = (await response.json()) as { bookmark_id?: string; id?: string; save_state?: string };
  const bookmarkID = saved.bookmark_id ?? saved.id;
  expect(bookmarkID).toBeTruthy();
  return bookmarkID!;
}

async function captureReference(page: Page, bookmarkID: string): Promise<void> {
  const response = await page.request.post(`/api/library/bookmarks/${bookmarkID}/capture`, {
    headers: { Origin: baseURL },
  });
  await expectAPIRequestStatus(response, 200, "capture-reference-fixture", "POST", "/api/library/bookmarks/:id/capture");
}

async function moderate(page: Page, bookmarkID: string, outcome: "restricted" | "removed"): Promise<void> {
  const response = await page.request.patch(`/api/admin/library/snapshots/${bookmarkID}`, {
    headers: { Origin: baseURL },
    data: { outcome, reason_category: outcome === "removed" ? "privacy" : "policy" },
  });
  await expectAPIRequestStatus(response, 200, "moderate-snapshot-fixture", "PATCH", "/api/admin/library/snapshots/:id");
}

async function signIn(session: Session, email: string, name: string): Promise<void> {
  await session.page.goto("/");
  await signInWithSyntheticEmail(session.page, session.page.request, email, name);
}

async function importArchive(session: Session, archive: Buffer, preserve: boolean): Promise<string> {
  await session.page.goto("/account/security");
  const checkbox = session.page.getByRole("checkbox", {
    name: "I am restoring my own personal account and want to preserve saved private copies from this export",
  });
  await expect(checkbox).not.toBeChecked();
  if (preserve) await checkbox.check();

  let alertText = "";
  const alertPromise = new Promise<void>((resolve) => {
    session.page.once("dialog", async (dialog) => {
      alertText = dialog.message();
      await dialog.accept();
      resolve();
    });
  });
  const importResponse = session.page.waitForResponse((response) =>
    response.url().endsWith("/api/import") && response.request().method() === "POST",
  );
  await session.page.locator("#cookbook-import").setInputFiles({
    name: "cookbook-export.zip",
    mimeType: "application/zip",
    buffer: archive,
  });
  const response = await importResponse;
  const importContext = await apiResponseContext(response, "explicit-snapshot-archive-import", "/api/import");
  expect(response.status(), `archive import response: ${importContext}`).toBe(201);
  const headers = await response.request().allHeaders();
  if (preserve) expect(headers["x-cookbook-preserve-personal-snapshots"]).toBe("true");
  else expect(headers["x-cookbook-preserve-personal-snapshots"]).toBeUndefined();
  await alertPromise;
  expect(alertText).toContain(preserve ? "1 private saved copy preserved as unverified" : "0 private saved copies preserved as unverified");
  expect(alertText).toContain(preserve ? "0 saved references imported as references" : "1 saved reference imported as reference");
  return alertText;
}

test("private snapshots use signed captures, stay actor-bound, and import only by explicit choice", async ({ browser }) => {
  test.setTimeout(60_000);
  test.skip(process.env.COOKBOOK_E2E_PRIVATE_SNAPSHOT !== "1", "requires the runner's explicit signed fixture opt-in");

  const owner = await openContext(browser);
  let referenceImporter: Session | undefined;
  let copyImporter: Session | undefined;
  let releaseHeldRead: (() => void) | undefined;
  try {
    await signIn(owner, "admin@example.test", "Snapshot Owner");

    const conditionalRoute = `/recipe?remote_object_uri=${encodeURIComponent(objectURI(ids.conditional))}`;
    await owner.page.goto(conditionalRoute);
    const conditionalResponsePromise = owner.page.waitForResponse((response) =>
      response.url().endsWith("/api/library/bookmarks") && response.request().method() === "POST",
    );
    await owner.page.getByRole("button", { name: "Save to Library" }).click();
    const conditionalResponse = await conditionalResponsePromise;
    const responseBody: unknown = await conditionalResponse.json().catch(() => null);
    const responseError = typeof responseBody === "object" && responseBody !== null && "error" in responseBody && typeof responseBody.error === "string"
      ? responseBody.error
      : "unavailable";
    const requestHeaders = await conditionalResponse.request().allHeaders();
    const refererOrigin = requestHeaders.referer ? new URL(requestHeaders.referer).origin : "unavailable";
    const responseContext = JSON.stringify({
      status: conditionalResponse.status(),
      error: responseError,
      origin: requestHeaders.origin ?? "unavailable",
      requestHost: new URL(conditionalResponse.url()).host,
      refererOrigin,
    });
    expect(conditionalResponse.status(), `conditional save response: ${responseContext}`).toBe(201);
    expect((await conditionalResponse.request().postDataJSON()).save_mode).toBe("conditional");
    const conditional = responseBody as { bookmark_id?: string; id?: string; save_state?: string };
    const conditionalBookmarkID = conditional.bookmark_id ?? conditional.id;
    expect(conditionalBookmarkID).toBeTruthy();
    expect(conditional.save_state).toBe("snapshot");
    expect(conditionalResponse.headers()["cache-control"]).toBe("private, no-store");
    expect(conditionalResponse.headers().vary).toBe("Cookie");
    expect(conditionalResponse.headers()["referrer-policy"]).toBe("no-referrer");

    const detailResponses: string[] = [];
    owner.page.on("response", (response) => {
      if (response.url().includes("/api/library/bookmarks/") && response.url().endsWith("/snapshot")) detailResponses.push(response.url());
    });
    const staticResponse = await owner.page.goto(`/recipe?bookmark_id=${encodeURIComponent(conditionalBookmarkID!)}`);
    expect(staticResponse?.headers()["cache-control"]).toBe("no-store");
    expect(staticResponse?.headers()["referrer-policy"]).toBe("no-referrer");
    const privateRead = owner.page.waitForResponse((response) => response.url().endsWith(`/api/library/bookmarks/${conditionalBookmarkID}/snapshot`));
    await owner.page.reload();
    const readResponse = await privateRead;
    const readContext = await apiResponseContext(readResponse, "private-snapshot-read", "/api/library/bookmarks/:id/snapshot");
    expect(readResponse.status(), `private snapshot read response: ${readContext}`).toBe(200);
    expect(readResponse.headers()["cache-control"]).toBe("private, no-store");
    expect(readResponse.headers().vary).toBe("Cookie");
    expect(readResponse.headers()["referrer-policy"]).toBe("no-referrer");
    await expect(owner.page.getByText("Tomato", { exact: false }).first()).toBeVisible();
    await expect(owner.page.getByText("Simmer the synthetic ingredients.")).toBeVisible();
    expect(detailResponses.length).toBeGreaterThan(0);

    await owner.page.getByRole("button", { name: "Start cooking mode" }).click();
    const cookingMode = owner.page.getByRole("dialog", { name: titles.conditional });
    await expect(cookingMode).toBeVisible();
    await expect(cookingMode.getByRole("heading", { name: "Simmer the synthetic ingredients." })).toBeVisible();
    await expect(cookingMode.locator(".cooking-step").getByText("Step 1 of 1", { exact: true })).toBeVisible();
    const cookingIngredient = cookingMode.getByRole("checkbox");
    await cookingIngredient.check();
    await expect(cookingIngredient).toBeChecked();
    await cookingMode.getByRole("button", { name: "Close cooking mode" }).click();
    await expect(cookingMode).toHaveCount(0);

    const referenceBookmarkID = await saveReference(owner.page, ids.reference);
    await owner.page.goto("/library");
    const captureButton = owner.page.getByRole("button", { name: "Keep this version" });
    await expect(captureButton).toBeVisible();
    await captureButton.focus();
    const captureResponsePromise = owner.page.waitForResponse((response) =>
      response.url().endsWith(`/api/library/bookmarks/${referenceBookmarkID}/capture`) && response.request().method() === "POST",
    );
    await captureButton.press("Enter");
    const captureResponse = await captureResponsePromise;
    const captureContext = await apiResponseContext(captureResponse, "explicit-snapshot-capture", "/api/library/bookmarks/:id/capture");
    expect(captureResponse.status(), `explicit capture response: ${captureContext}`).toBe(200);
    await expect(owner.page.getByRole("status").filter({ hasText: "Bookmark updated." })).toBeVisible();
    await expect(owner.page.getByRole("link", { name: `Open ${titles.reference}` })).toBeVisible();

    const restrictedBookmarkID = await saveReference(owner.page, ids.restricted);
    const removedBookmarkID = await saveReference(owner.page, ids.removed);
    const removableBookmarkID = await saveReference(owner.page, ids.removable);
    await captureReference(owner.page, restrictedBookmarkID);
    await captureReference(owner.page, removedBookmarkID);
    await captureReference(owner.page, removableBookmarkID);
    await moderate(owner.page, restrictedBookmarkID, "restricted");
    await moderate(owner.page, removedBookmarkID, "removed");

    await owner.page.goto("/library");
    await expect(owner.page.getByText(/restricted/i).first()).toBeVisible();
    await expect(owner.page.getByText(/removed/i).first()).toBeVisible();
    await expect(owner.page.getByText(titles.restricted, { exact: true })).toHaveCount(0);
    await expect(owner.page.getByText(titles.removed, { exact: true })).toHaveCount(0);

    for (const [bookmarkID, message] of [
      [restrictedBookmarkID, "restricted by this instance"],
      [removedBookmarkID, "was removed by this instance"],
    ] as const) {
      let remoteCacheFallbacks = 0;
      owner.page.on("request", (request: Request) => {
        if (request.url().includes("/api/library/recipes/remote?")) remoteCacheFallbacks += 1;
      });
      await owner.page.goto(`/recipe?bookmark_id=${encodeURIComponent(bookmarkID)}`);
      await expect(owner.page.getByRole("heading", { name: "Saved copy blocked" })).toBeVisible();
      await expect(owner.page.getByText(new RegExp(message))).toBeVisible();
      expect(remoteCacheFallbacks).toBe(0);
    }

    await owner.page.goto(`/recipe?bookmark_id=${encodeURIComponent(removableBookmarkID)}`);
    await expect(owner.page.getByRole("heading", { name: `Recipe: ${titles.removable}` })).toBeVisible();
    await owner.page.getByRole("button", { name: "Remove from Library" }).click();
    await expect(owner.page.getByRole("heading", { name: "Recipe unavailable" })).toBeVisible();
    const removedRead = await owner.page.request.get(`/api/library/bookmarks/${removableBookmarkID}/snapshot`);
    expect(removedRead.status()).toBe(404);
    expect(removedRead.headers()["cache-control"]).toBe("private, no-store");

    for (const bookmarkID of [referenceBookmarkID, restrictedBookmarkID, removedBookmarkID]) {
      const response = await owner.page.request.delete(`/api/library/bookmarks/${bookmarkID}`, {
        headers: { Origin: baseURL },
      });
      await expectAPIRequestStatus(response, 204, "remove-reference-fixture", "DELETE", "/api/library/bookmarks/:id");
    }

    const exported = await owner.page.request.get("/api/export");
    expect(exported.status()).toBe(200);
    const archive = await exported.body();
    expect(archive.length).toBeGreaterThan(0);

    // Hold a real API response at the browser boundary, sign out through the UI,
    // then release that response and ensure old account data stays cleared.
    let reportRead!: () => void;
    const allowRead = new Promise<void>((resolve) => { releaseHeldRead = resolve; });
    const readStarted = new Promise<void>((resolve) => { reportRead = resolve; });
    await owner.page.route(`**/api/library/bookmarks/${conditionalBookmarkID}/snapshot`, async (route) => {
      const realResponse = await route.fetch();
      reportRead();
      await allowRead;
      await route.fulfill({ response: realResponse });
    });
    await owner.page.goto(`/recipe?bookmark_id=${encodeURIComponent(conditionalBookmarkID!)}`);
    await readStarted;
    await owner.page.getByRole("button", { name: /account menu$/i }).click();
    await owner.page.getByRole("button", { name: "Sign out" }).click();
    await expect(owner.page.getByRole("heading", { name: "Recipe unavailable" })).toBeVisible();
    releaseHeldRead?.();
    await expect(owner.page.getByText(titles.conditional, { exact: true })).toHaveCount(0);
    await owner.page.unroute(`**/api/library/bookmarks/${conditionalBookmarkID}/snapshot`);

    await signIn(owner, "snapshot-switched-account@example.test", "Switched Account");
    const switchedRead = owner.page.waitForResponse((response) => response.url().endsWith(`/api/library/bookmarks/${conditionalBookmarkID}/snapshot`));
    await owner.page.goto(`/recipe?bookmark_id=${encodeURIComponent(conditionalBookmarkID!)}`);
    expect((await switchedRead).status()).toBe(404);
    await expect(owner.page.getByRole("heading", { name: "Recipe unavailable" })).toBeVisible();
    await expect(owner.page.getByText(titles.conditional, { exact: true })).toHaveCount(0);

    referenceImporter = await openContext(browser);
    await signIn(referenceImporter, "snapshot-reference-import@example.test", "Reference Importer");
    const referenceSummary = await importArchive(referenceImporter, archive, false);
    expect(referenceSummary).toContain("0 private saved copies preserved as unverified");
    await referenceImporter.page.goto("/library");
    await saveReference(referenceImporter.page, ids.reference);
    await referenceImporter.page.goto("/library");
    const unrelatedReference = referenceImporter.page.getByRole("link", { name: `Open ${titles.reference}` });
    const importedReference = referenceImporter.page.getByRole("link", { name: `Open ${titles.conditional}` });
    const importedReferenceCard = referenceImporter.page.getByRole("article").filter({ has: importedReference });
    await expect(unrelatedReference).toBeVisible();
    await expect(importedReference).toBeVisible();
    await expect(importedReferenceCard.getByText("Saved reference", { exact: true })).toBeVisible();

    copyImporter = await openContext(browser);
    await signIn(copyImporter, "snapshot-copy-import@example.test", "Copy Importer");
    const copySummary = await importArchive(copyImporter, archive, true);
    expect(copySummary).toContain("1 private saved copy preserved as unverified");
    await copyImporter.page.goto("/library");
    const importedLink = copyImporter.page.getByRole("link", { name: `Open ${titles.conditional}` });
    const importedCopyCard = copyImporter.page.getByRole("article").filter({ has: importedLink });
    await expect(importedCopyCard.getByText("Imported copy · source not verified. This label does not verify authorship. Source changes do not update this copy.")).toBeVisible();
    await importedLink.click();
    await expect(copyImporter.page.getByText(titles.conditional, { exact: true })).toBeVisible();
  } finally {
    releaseHeldRead?.();
    await Promise.all([
      owner.context.close(),
      referenceImporter?.context.close(),
      copyImporter?.context.close(),
    ]);
  }
});
