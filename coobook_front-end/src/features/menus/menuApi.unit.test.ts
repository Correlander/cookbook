import { afterEach, expect, it, vi } from "vitest";
import {
  addPrivateMenuEntry,
  createPrivateMenu,
  deletePrivateMenu,
  fetchPrivateMenu,
  fetchPrivateMenuPage,
  MenuApiError,
  parsePrivateMenu,
  parsePrivateMenuPage,
  removePrivateMenuEntry,
  reorderPrivateMenuEntries,
  updatePrivateMenu,
  updatePrivateMenuEntry,
} from "./menuApi.ts";

const menuID = "a".repeat(32);
const firstEntryID = "b".repeat(32);
const secondEntryID = "c".repeat(32);
const recipeID = "d".repeat(32);

function availableEntry(id = firstEntryID, position = 0, recipe = recipeID) {
  return {
    id,
    position,
    label: "Main",
    note: "Make ahead",
    serving_note: "Serve warm",
    available: true,
    recipe: { id: recipe, title: "Soup" },
  };
}

function unavailableEntry(id = firstEntryID, position = 0) {
  return {
    id,
    position,
    label: "Optional",
    note: "Owner note",
    serving_note: "",
    available: false,
  };
}

function menu(entries: unknown[] = [availableEntry()]) {
  return {
    id: menuID,
    title: "Weeknight meals",
    note: "Keep it simple",
    creator_name: "An explicit claim",
    version: 3,
    created_at: "2026-11-04T20:00:00Z",
    updated_at: "2026-11-04T21:00:00Z",
    creator_attributions: [{
      subject_kind: "external_claim",
      name_claim: "An explicit claim",
      role: "creator",
      provenance: "self_asserted_at_creation",
    }],
    entries,
  };
}

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

it("parses an ordered page and retains repeated recipe references as distinct entries", () => {
  const result = parsePrivateMenuPage({
    menus: [menu([availableEntry(firstEntryID, 0), availableEntry(secondEntryID, 1)])],
    total: 1,
    limit: 24,
    offset: 0,
  }, 0);
  expect(result.menus[0]?.entries.map((entry) => entry.id)).toEqual([firstEntryID, secondEntryID]);
  expect(result.menus[0]?.entries.map((entry) => entry.available && entry.recipe.id)).toEqual([recipeID, recipeID]);
  expect(result.menus[0]?.creator_name).toBe("An explicit claim");
});

it("parses unavailable slots with owner notes and no recipe identity", () => {
  expect(parsePrivateMenu(menu([unavailableEntry()])).entries[0]).toEqual({
    id: firstEntryID,
    position: 0,
    label: "Optional",
    note: "Owner note",
    serving_note: "",
    available: false,
  });
});

it.each([
  ["recipe ID on unavailable slot", { ...unavailableEntry(), recipe_id: recipeID }],
  ["instance ID on unavailable slot", { ...unavailableEntry(), source_instance_id: menuID }],
  ["recipe body on available slot", { ...availableEntry(), recipe: { id: recipeID, title: "Soup", ingredients: ["secret"] } }],
  ["recipe ID and title on unavailable slot", { ...unavailableEntry(), recipe: { id: recipeID, title: "Stale title" } }],
  ["noncontiguous position", { ...availableEntry(), position: 1 }],
  ["duplicate entry identity", availableEntry(firstEntryID, 1)],
  ["oversized entry list", Array.from({ length: 65 }, (_, position) => availableEntry(position.toString(16).padStart(32, "0"), position))],
])("rejects a menu containing %s", (_description, entryOrEntries) => {
  const entries = Array.isArray(entryOrEntries) ? entryOrEntries : [entryOrEntries];
  expect(() => parsePrivateMenu(menu(entries))).toThrow("Could not load this menu.");
});

it("rejects malformed menu, entry, creator attribution, and page bounds", () => {
  expect(() => parsePrivateMenu({ ...menu(), version: 0 })).toThrow("Could not load this menu.");
  expect(() => parsePrivateMenu({ ...menu(), title: "x".repeat(641) })).toThrow("Could not load this menu.");
  expect(() => parsePrivateMenu({ ...menu(), creator_attributions: [{ subject_kind: "local", role: "creator" }] })).toThrow("Could not load this menu.");
  expect(() => parsePrivateMenuPage({ menus: [menu()], total: 1, limit: 101, offset: 0 })).toThrow("Could not load your menus Library.");
  expect(() => parsePrivateMenuPage({ menus: [menu()], total: 1, limit: 24, offset: 1_000_001 })).toThrow("Could not load your menus Library.");
  expect(() => parsePrivateMenuPage({ menus: [menu()], total: 1, limit: 24, offset: 1 }, 0)).toThrow("Could not load your menus Library.");
  expect(() => parsePrivateMenuPage({ menus: [], total: 0, limit: 100, offset: 0 }, 0, 24)).toThrow("Could not load your menus Library.");
});

it("reads the owned Library endpoint with same-origin private no-store options", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ menus: [menu()], total: 1, limit: 24, offset: 0 }));
  vi.stubGlobal("fetch", fetchMock);
  const result = await fetchPrivateMenuPage();
  expect(fetchMock).toHaveBeenCalledWith("/api/library/menus?limit=24&offset=0", { cache: "no-store", credentials: "same-origin" });
  expect(result.menus).toHaveLength(1);
});

it("encodes the detail path and sends explicit versioned mutations to the published routes", async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse(menu()))
    .mockResolvedValueOnce(jsonResponse(menu(), 201))
    .mockResolvedValueOnce(jsonResponse(menu(), 200))
    .mockResolvedValueOnce(jsonResponse(menu(), 201))
    .mockResolvedValueOnce(jsonResponse(menu()))
    .mockResolvedValueOnce(jsonResponse(menu()))
    .mockResolvedValueOnce(jsonResponse(menu()))
    .mockResolvedValueOnce(jsonResponse({ status: "menu_deleted" }));
  vi.stubGlobal("fetch", fetchMock);

  await fetchPrivateMenu(menuID);
  await createPrivateMenu({ title: "Weeknight meals", note: "Keep it simple", creator_attribution: { kind: "name_claim", name: "A cook" } });
  await updatePrivateMenu(menuID, { title: "Updated meals", note: "New note", version: 3 });
  await addPrivateMenuEntry(menuID, { version: 3, recipe_id: recipeID, label: "Main", note: "Tonight" });
  await updatePrivateMenuEntry(menuID, firstEntryID, { version: 4, note: "Keep target unchanged" });
  await removePrivateMenuEntry(menuID, firstEntryID, 5);
  await reorderPrivateMenuEntries(menuID, { version: 6, entry_ids: [firstEntryID, secondEntryID] });
  await deletePrivateMenu(menuID, 7);

  expect(fetchMock.mock.calls.map(([url, init]) => [String(url), init?.method ?? "GET"])).toEqual([
    [`/api/menus/${menuID}`, "GET"],
    ["/api/menus", "POST"],
    [`/api/menus/${menuID}`, "PUT"],
    [`/api/menus/${menuID}/entries`, "POST"],
    [`/api/menus/${menuID}/entries/${firstEntryID}`, "PUT"],
    [`/api/menus/${menuID}/entries/${firstEntryID}?version=5`, "DELETE"],
    [`/api/menus/${menuID}/entries/order`, "PUT"],
    [`/api/menus/${menuID}?version=7`, "DELETE"],
  ]);
  expect(JSON.parse(fetchMock.mock.calls[2]?.[1]?.body as string)).toEqual({ title: "Updated meals", note: "New note", version: 3 });
  expect(JSON.parse(fetchMock.mock.calls[3]?.[1]?.body as string)).toMatchObject({ version: 3, recipe_id: recipeID });
  expect(JSON.parse(fetchMock.mock.calls[4]?.[1]?.body as string)).toEqual({ version: 4, note: "Keep target unchanged" });
  expect(JSON.parse(fetchMock.mock.calls[6]?.[1]?.body as string)).toEqual({ version: 6, entry_ids: [firstEntryID, secondEntryID] });
});

it("preserves backend status and error codes for conflicts, limits, and unavailable targets", async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse({ error: "version_conflict" }, 409))
    .mockResolvedValueOnce(jsonResponse({ error: "menu_limit_reached" }, 409))
    .mockResolvedValueOnce(jsonResponse({ error: "recipe_unavailable" }, 422));
  vi.stubGlobal("fetch", fetchMock);

  await expect(updatePrivateMenu(menuID, { title: "Updated", note: "", version: 3 })).rejects.toMatchObject({
    name: "MenuApiError", status: 409, code: "version_conflict",
    message: "This menu changed elsewhere. Reload it before saving.",
  } satisfies Partial<MenuApiError>);
  await expect(createPrivateMenu({ title: "A menu" })).rejects.toMatchObject({ status: 409, code: "menu_limit_reached" });
  await expect(addPrivateMenuEntry(menuID, { version: 3, recipe_id: recipeID })).rejects.toMatchObject({ status: 422, code: "recipe_unavailable" });
});

it("rejects malformed successful responses and locally invalid inputs without fallback requests", async () => {
  const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ menus: "invalid" }));
  vi.stubGlobal("fetch", fetchMock);
  await expect(fetchPrivateMenuPage()).rejects.toThrow("Could not load your menus Library.");
  await expect(createPrivateMenu({ title: "  " })).rejects.toBeInstanceOf(MenuApiError);
  await expect(updatePrivateMenu(menuID, { title: "x".repeat(161), note: "", version: 1 })).rejects.toBeInstanceOf(MenuApiError);
  await expect(addPrivateMenuEntry(menuID, { version: 1, recipe_id: "remote://recipe" })).rejects.toBeInstanceOf(MenuApiError);
  await expect(reorderPrivateMenuEntries(menuID, { version: 1, entry_ids: [firstEntryID, firstEntryID] })).rejects.toBeInstanceOf(MenuApiError);
  await expect(deletePrivateMenu(menuID, 0)).rejects.toBeInstanceOf(MenuApiError);
  await expect(fetchPrivateMenuPage(1_000_001)).rejects.toMatchObject({ status: 400, code: "invalid_pagination" });
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
