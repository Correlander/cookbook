import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AdminSettingsModel } from "./apiModels";
import AdminSettings from "./AdminSettings.tsx";

const settings: AdminSettingsModel = {
  public_instance: false,
  courses_enabled: false,
  registration_policy: "open",
  revision_retention_days: 365,
  audit_retention_days: 365,
  federation_enabled: false,
  federation_origin: "",
  public_cookbooks: 0,
  public_recipes: 0,
  public_courses: 0,
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("clears a denied settings draft and reloads fresh state only after explicit retry", async () => {
  let settingsReads = 0;
  let writes = 0;
  let permissionRestored = false;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/settings" && init?.method === "PATCH") {
      writes += 1;
      return Promise.resolve(jsonResponse({ error: "forbidden" }, 403));
    }
    if (url === "/api/admin/settings") {
      settingsReads += 1;
      if (settingsReads > 1 && !permissionRestored)
        return Promise.resolve(jsonResponse({ error: "forbidden" }, 403));
      return Promise.resolve(jsonResponse(settings));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);

  expect(await screen.findByRole("navigation", { name: "Instance settings sections" })).toBeTruthy();
  const registration = await screen.findByRole("combobox", { name: "Account registration" });
  expect(screen.getByRole("link", { name: "Public browsing" }).getAttribute("href")).toBe("#visibility");
  expect(screen.getByRole("link", { name: "Courses" }).getAttribute("href")).toBe("#courses");
  expect(screen.getByRole("link", { name: "Data retention" }).getAttribute("href")).toBe("#retention");
  expect(screen.getByRole("heading", { name: "Accounts" })).toBeTruthy();
  fireEvent.change(registration, { target: { value: "disabled" } });
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));

  expect(await screen.findByRole("heading", { name: "Instance settings access changed" })).toBeTruthy();
  expect(screen.getByRole("alert").textContent).toContain("Your draft was cleared");
  expect(screen.queryByRole("combobox", { name: "Account registration" })).toBeNull();
  expect(settingsReads).toBe(1);
  expect(writes).toBe(1);

  fireEvent.click(screen.getByRole("button", { name: "Reload settings" }));
  expect(await screen.findByRole("heading", { name: "Instance settings access changed" })).toBeTruthy();
  expect(screen.queryByRole("combobox", { name: "Account registration" })).toBeNull();
  expect(settingsReads).toBe(2);

  permissionRestored = true;
  fireEvent.click(screen.getByRole("button", { name: "Reload settings" }));
  const reloadedRegistration = await screen.findByRole("combobox", { name: "Account registration" });

  expect(reloadedRegistration).toHaveProperty("value", "open");
  expect(settingsReads).toBe(3);
  expect(writes).toBe(1);
});

it("clears the editor on an expired session and offers sign-in and settings reload", async () => {
  let settingsReads = 0;
  let writes = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/settings" && init?.method === "PATCH") {
      writes += 1;
      return Promise.resolve(new Response(null, { status: 401 }));
    }
    if (url === "/api/admin/settings") {
      settingsReads += 1;
      return Promise.resolve(jsonResponse(settings));
    }
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);

  expect(await screen.findByRole("checkbox", { name: /Enable recipe federation/ })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));

  expect(await screen.findByRole("heading", { name: "Settings session expired" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Sign in" }).getAttribute("href")).toBe("/?signin=1");
  expect(screen.queryByRole("checkbox", { name: /Enable recipe federation/ })).toBeNull();
  expect(settingsReads).toBe(1);
  expect(writes).toBe(1);

  fireEvent.click(screen.getByRole("button", { name: "Reload settings" }));
  expect(await screen.findByRole("checkbox", { name: /Enable recipe federation/ })).toBeTruthy();
  expect(settingsReads).toBe(2);
  expect(writes).toBe(1);
});

it("explains a recent-authentication denial without retrying the write", async () => {
  let writes = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/settings" && init?.method === "PATCH") {
      writes += 1;
      return Promise.resolve(jsonResponse({ error: "reauthentication_required" }, 403));
    }
    if (url === "/api/admin/settings") return Promise.resolve(jsonResponse(settings));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);
  fireEvent.click(await screen.findByRole("button", { name: "Save instance settings" }));

  expect(await screen.findByRole("heading", { name: "Recent sign-in required" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Sign in" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Save instance settings" })).toBeNull();
  expect(writes).toBe(1);
});

it("keeps the draft and editor on validation failures", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/settings" && init?.method === "PATCH")
      return Promise.resolve(jsonResponse({ error: "federation_origin_required" }, 400));
    if (url === "/api/admin/settings") return Promise.resolve(jsonResponse(settings));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);

  const federation = await screen.findByRole("checkbox", { name: /Enable recipe federation/ });
  fireEvent.click(federation);
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));

  expect((await screen.findByRole("alert")).textContent).toContain(
    "Set the instance’s stable HTTPS federation origin before enabling federation.",
  );
  expect(screen.getByRole("checkbox", { name: /Enable recipe federation/ })).toHaveProperty("checked", true);
  expect(screen.getByRole("button", { name: "Save instance settings" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Reload settings" })).toBeNull();
});

it("keeps audience settings and confirmation after a generic publisher-profile denial", async () => {
  const populatedSettings = { ...settings, public_recipes: 1 };
  let writes = 0;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/api/admin/settings" && init?.method === "PATCH") {
      writes += 1;
      return Promise.resolve(jsonResponse({ error: "publisher_profile_required" }, 409));
    }
    if (String(input) === "/api/admin/settings") return Promise.resolve(jsonResponse(populatedSettings));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);
  fireEvent.click(await screen.findByRole("checkbox", { name: /Enable public browsing/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed the existing public content/ }));
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));

  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("Some owners of content affected by this change must complete a creator profile");
  expect(alert.textContent).not.toMatch(/administrator'?s profile|owner names|owner IDs|\d+ owners/i);
  expect(alert.textContent).not.toMatch(/administrator/i);
  expect(alert.textContent).not.toContain("Synthetic");
  expect(screen.getByRole("checkbox", { name: /Enable public browsing/ })).toHaveProperty("checked", true);
  expect(screen.getByRole("checkbox", { name: /I reviewed the existing public content/ })).toHaveProperty("checked", true);
  expect(screen.getByRole("button", { name: "Save instance settings" })).toBeTruthy();
  expect(writes).toBe(1);
});

it("saves a presentation-only change without sending access or federation settings", async () => {
  const presetSettings = { ...settings, anonymous_home_preset: "library_first" as const };
  let patchBody: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/api/admin/settings" && init?.method === "PATCH") {
      patchBody = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(jsonResponse({ ...presetSettings, ...patchBody }));
    }
    if (String(input) === "/api/admin/settings")
      return Promise.resolve(jsonResponse(presetSettings));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);
  const preset = await screen.findByRole("combobox", { name: /homepage presentation/i });
  fireEvent.change(preset, { target: { value: "discovery_first" } });
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));

  expect(await screen.findByRole("status")).toBeTruthy();
  expect(patchBody).toEqual({ anonymous_home_preset: "discovery_first" });
  expect(screen.getByRole("combobox", { name: /homepage presentation/i })).toHaveProperty("value", "discovery_first");
});

it("does not send an unsupported homepage preset to an older settings API", async () => {
  let patchBody: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/api/admin/settings" && init?.method === "PATCH") {
      patchBody = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(jsonResponse(settings));
    }
    if (String(input) === "/api/admin/settings")
      return Promise.resolve(jsonResponse(settings));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);
  expect(await screen.findByText(/does not support choosing an anonymous homepage/i)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));
  await screen.findByRole("status");
  expect(patchBody).toEqual({});
});

it("saves only a supported changed site name and resets it explicitly to Cookbook", async () => {
  const brandedSettings = { ...settings, site_name: "My Kitchen" };
  const patchBodies: Record<string, unknown>[] = [];
  let current = brandedSettings;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/api/admin/settings" && init?.method === "PATCH") {
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      patchBodies.push(body);
      current = { ...current, ...body };
      return Promise.resolve(jsonResponse(current));
    }
    if (String(input) === "/api/admin/settings") return Promise.resolve(jsonResponse(current));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);
  const nameInput = await screen.findByRole("textbox", { name: "Instance name in navigation" });
  fireEvent.change(nameInput, { target: { value: "New Kitchen" } });
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));
  expect(await screen.findByRole("status")).toBeTruthy();
  expect(patchBodies[0]).toEqual({ site_name: "New Kitchen" });

  fireEvent.click(screen.getByRole("button", { name: "Reset site name" }));
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));
  await screen.findByRole("status");
  expect(patchBodies[1]).toEqual({ site_name: "Cookbook" });
  expect(screen.getByRole("textbox", { name: "Instance name in navigation" })).toHaveProperty("value", "Cookbook");
});

it("omits the site name field and reset control when an older settings API omits support", async () => {
  let patchBody: Record<string, unknown> | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/api/admin/settings" && init?.method === "PATCH") {
      patchBody = JSON.parse(String(init.body)) as Record<string, unknown>;
      return Promise.resolve(jsonResponse(settings));
    }
    if (String(input) === "/api/admin/settings") return Promise.resolve(jsonResponse(settings));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);
  expect(await screen.findByRole("textbox", { name: "Instance name in navigation" })).toHaveProperty("disabled", true);
  expect(screen.getByRole("button", { name: "Reset site name" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));
  await screen.findByRole("status");
  expect(patchBody).toEqual({});
});

it("does not show publisher setup guidance when the server uses that code with a non-409 status", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    String(input) === "/api/admin/settings" && init?.method === "PATCH"
      ? Promise.resolve(jsonResponse({ error: "publisher_profile_required" }, 400))
      : Promise.resolve(jsonResponse(settings)),
  );
  vi.stubGlobal("fetch", fetchMock);
  render(<AdminSettings />);
  fireEvent.click(await screen.findByRole("button", { name: "Save instance settings" }));

  expect((await screen.findByRole("alert")).textContent).toContain("Could not save instance settings.");
  expect(screen.queryByText(/Some owners of content affected/)).toBeNull();
  expect(screen.queryByRole("link", { name: /creator profile/i })).toBeNull();
});

it("keeps the editor active after an allowed save", async () => {
  let writes = 0;
  const savedSettings = { ...settings, federation_enabled: true };
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/admin/settings" && init?.method === "PATCH") {
      writes += 1;
      return Promise.resolve(jsonResponse(savedSettings));
    }
    if (url === "/api/admin/settings") return Promise.resolve(jsonResponse(settings));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(<AdminSettings />);
  fireEvent.click(await screen.findByRole("checkbox", { name: /Enable recipe federation/ }));
  fireEvent.click(screen.getByRole("button", { name: "Save instance settings" }));

  expect((await screen.findByRole("status")).textContent).toContain("Instance settings saved.");
  expect(screen.getByRole("checkbox", { name: /Enable recipe federation/ })).toHaveProperty("checked", true);
  expect(screen.getByRole("button", { name: "Save instance settings" })).toBeTruthy();
  expect(writes).toBe(1);
});
