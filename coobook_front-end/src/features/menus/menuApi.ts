import type {
  MenuCreatorChoice,
  MenuEntryView,
  PrivateMenu,
  PrivateMenuPage,
} from "../../apiModels";
import { parseOptionalCreatorAttributions } from "../creator/creatorAttributionParser.ts";

export const MENU_PAGE_SIZE = 24;
export const MENU_MAX_PAGE_SIZE = 100;
export const MENU_MAX_OFFSET = 1_000_000;
export const MENU_MAX_ENTRIES = 64;

const MENU_LIST_PATH = "/api/library/menus";

export class MenuApiError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = "MenuApiError";
    this.status = status;
    this.code = code;
  }
}

export interface CreateMenuInput {
  title: string;
  note?: string;
  creator_attribution?: MenuCreatorChoice;
}

export interface UpdateMenuInput {
  title: string;
  note: string;
  version: number;
}

export interface MenuEntryFields {
  recipe_id: string;
  label?: string;
  note?: string;
  serving_note?: string;
}

export interface UpdateMenuEntryFields {
  recipe_id?: string;
  label?: string;
  note?: string;
  serving_note?: string;
}

export interface MenuEntryUpdateInput extends UpdateMenuEntryFields {
  version: number;
}

export interface MenuEntryAddInput extends MenuEntryFields {
  version: number;
}

export interface MenuOrderInput {
  version: number;
  entry_ids: string[];
}

export function parsePrivateMenu(value: unknown): PrivateMenu {
  if (!isRecord(value) ||
      !isOpaqueID(value.id) ||
      !isBoundedText(value.title, 160, 640, true) ||
      !isBoundedText(value.note, 4000, 16000, false) ||
      !isPositiveSafeInteger(value.version) ||
      !isTimestamp(value.created_at) ||
      !isTimestamp(value.updated_at) ||
      (value.creator_name !== undefined && !isBoundedText(value.creator_name, 80, 320, false)) ||
      !Array.isArray(value.entries) || value.entries.length > MENU_MAX_ENTRIES) {
    throw invalidMenuResponse();
  }

  let entries: MenuEntryView[];
  let creatorAttributions: ReturnType<typeof parseOptionalCreatorAttributions>;
  try {
    entries = value.entries.map((entry, index) => parseMenuEntry(entry, index));
    creatorAttributions = parseOptionalCreatorAttributions(value.creator_attributions, "Could not load this menu.");
  } catch {
    throw invalidMenuResponse();
  }

  if (new Set(entries.map((entry) => entry.id)).size !== entries.length) throw invalidMenuResponse();

  return {
    id: value.id,
    title: value.title,
    note: value.note,
    ...(typeof value.creator_name === "string" ? { creator_name: value.creator_name } : {}),
    version: value.version,
    created_at: value.created_at,
    updated_at: value.updated_at,
    ...creatorAttributions,
    entries,
  };
}

export function parsePrivateMenuPage(
  value: unknown,
  expectedOffset?: number,
  expectedLimit?: number,
): PrivateMenuPage {
  if (!isRecord(value) ||
      !Array.isArray(value.menus) ||
      !isNonnegativeSafeInteger(value.total) ||
      !isPositiveSafeInteger(value.limit) || value.limit > MENU_MAX_PAGE_SIZE ||
      !isBoundedOffset(value.offset) ||
      (expectedOffset !== undefined && value.offset !== expectedOffset) ||
      (expectedLimit !== undefined && value.limit !== expectedLimit) ||
      value.menus.length > value.limit) {
    throw new Error("Could not load your menus Library.");
  }
  try {
    return {
      menus: value.menus.map(parsePrivateMenu),
      total: value.total,
      limit: value.limit,
      offset: value.offset,
    };
  } catch {
    throw new Error("Could not load your menus Library.");
  }
}

export async function fetchPrivateMenuPage(
  offset = 0,
  limit = MENU_PAGE_SIZE,
): Promise<PrivateMenuPage> {
  validatePage(offset, limit);
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  const response = await fetch(`${MENU_LIST_PATH}?${params}`, {
    cache: "no-store",
    credentials: "same-origin",
  });
  const value = await readJson(response, "Could not load your menus Library.");
  return parsePrivateMenuPage(value, offset, limit);
}

export async function fetchPrivateMenu(menuID: string): Promise<PrivateMenu> {
  const response = await fetch(menuPath(menuID), {
    cache: "no-store",
    credentials: "same-origin",
  });
  return parsePrivateMenu(await readJson(response, "Could not load this menu."));
}

export async function createPrivateMenu(input: CreateMenuInput): Promise<PrivateMenu> {
  validateCreateMenuInput(input);
  const response = await fetch("/api/menus", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    cache: "no-store",
    body: JSON.stringify(input),
  });
  return parsePrivateMenu(await readJson(response, "Could not create this menu.", 201));
}

export async function updatePrivateMenu(menuID: string, input: UpdateMenuInput): Promise<PrivateMenu> {
  validateMenuID(menuID);
  validateVersion(input.version);
  if (!isBoundedText(input.title.trim(), 160, 640, true) || !isBoundedText(input.note, 4000, 16000, false)) throw invalidInput();
  const response = await fetch(menuPath(menuID), jsonMutation("PUT", input));
  return parsePrivateMenu(await readJson(response, "Could not update this menu."));
}

export async function addPrivateMenuEntry(menuID: string, input: MenuEntryAddInput): Promise<PrivateMenu> {
  validateMenuID(menuID);
  validateVersion(input.version);
  validateEntryFields(input);
  validateRecipeID(input.recipe_id);
  const response = await fetch(`${menuPath(menuID)}/entries`, jsonMutation("POST", input));
  return parsePrivateMenu(await readJson(response, "Could not add this recipe to the menu.", 201));
}

export async function updatePrivateMenuEntry(
  menuID: string,
  entryID: string,
  input: MenuEntryUpdateInput,
): Promise<PrivateMenu> {
  validateMenuID(menuID);
  validateEntryID(entryID);
  validateVersion(input.version);
  validateEntryFields(input);
  if (input.recipe_id !== undefined) validateRecipeID(input.recipe_id);
  const response = await fetch(`${menuPath(menuID)}/entries/${encodeURIComponent(entryID)}`, jsonMutation("PUT", input));
  return parsePrivateMenu(await readJson(response, "Could not update this menu entry."));
}

export async function removePrivateMenuEntry(menuID: string, entryID: string, version: number): Promise<PrivateMenu> {
  validateMenuID(menuID);
  validateEntryID(entryID);
  validateVersion(version);
  const response = await fetch(versionedPath(`${menuPath(menuID)}/entries/${encodeURIComponent(entryID)}`, version), {
    method: "DELETE",
    credentials: "same-origin",
    cache: "no-store",
  });
  return parsePrivateMenu(await readJson(response, "Could not remove this menu entry."));
}

export async function reorderPrivateMenuEntries(menuID: string, input: MenuOrderInput): Promise<PrivateMenu> {
  validateMenuID(menuID);
  validateVersion(input.version);
  if (!Array.isArray(input.entry_ids) || input.entry_ids.length > MENU_MAX_ENTRIES || input.entry_ids.some((id) => !isOpaqueID(id)) || new Set(input.entry_ids).size !== input.entry_ids.length) throw invalidInput();
  const response = await fetch(`${menuPath(menuID)}/entries/order`, jsonMutation("PUT", input));
  return parsePrivateMenu(await readJson(response, "Could not reorder this menu."));
}

export async function deletePrivateMenu(menuID: string, version: number): Promise<void> {
  validateMenuID(menuID);
  validateVersion(version);
  const response = await fetch(versionedPath(menuPath(menuID), version), {
    method: "DELETE",
    credentials: "same-origin",
    cache: "no-store",
  });
  const value = await readJson(response, "Could not delete this menu.");
  if (!isRecord(value) || value.status !== "menu_deleted") throw invalidResponse("Could not delete this menu.", response.status);
}

function parseMenuEntry(value: unknown, expectedPosition: number): MenuEntryView {
  if (!isRecord(value) || !isOpaqueID(value.id) || value.position !== expectedPosition ||
      !isBoundedText(value.label, 80, 320, false) ||
      !isBoundedText(value.note, 2000, 8000, false) ||
      !isBoundedText(value.serving_note, 160, 640, false) ||
      typeof value.available !== "boolean") throw invalidMenuResponse();

  const allowedFields = ["id", "position", "label", "note", "serving_note", "available", "recipe"];
  if (Object.keys(value).some((key) => !allowedFields.includes(key))) throw invalidMenuResponse();

  if (!value.available) {
    if ("recipe" in value) throw invalidMenuResponse();
    return {
      id: value.id,
      position: value.position,
      label: value.label,
      note: value.note,
      serving_note: value.serving_note,
      available: false,
    };
  }

  if (!isRecord(value.recipe) ||
      Object.keys(value.recipe).some((key) => key !== "id" && key !== "title") ||
      !isOpaqueID(value.recipe.id) ||
      !isBoundedText(value.recipe.title, 160, 640, true)) throw invalidMenuResponse();
  return {
    id: value.id,
    position: value.position,
    label: value.label,
    note: value.note,
    serving_note: value.serving_note,
    available: true,
    recipe: { id: value.recipe.id, title: value.recipe.title },
  };
}

function validateCreateMenuInput(input: CreateMenuInput): void {
  if (!isRecord(input) || !isBoundedText(input.title.trim(), 160, 640, true) ||
      (input.note !== undefined && !isBoundedText(input.note, 4000, 16000, false))) throw invalidInput();
  const choice = input.creator_attribution;
  if (choice === undefined) return;
  if (!isRecord(choice)) throw invalidInput();
  if (choice.kind === "local_profile" || choice.kind === "unknown") return;
  if (choice.kind === "name_claim" && isBoundedText(choice.name.trim(), 80, 320, true)) return;
  throw invalidInput();
}

function validateEntryFields(input: UpdateMenuEntryFields): void {
  if ((input.label !== undefined && !isBoundedText(input.label, 80, 320, false)) ||
      (input.note !== undefined && !isBoundedText(input.note, 2000, 8000, false)) ||
      (input.serving_note !== undefined && !isBoundedText(input.serving_note, 160, 640, false))) throw invalidInput();
}

function validatePage(offset: number, limit: number): void {
  if (!isBoundedOffset(offset) || !isPositiveSafeInteger(limit) || limit > MENU_MAX_PAGE_SIZE) {
    throw new MenuApiError("The menu page request was invalid.", 400, "invalid_pagination");
  }
}

function validateVersion(version: number): void {
  if (!isPositiveSafeInteger(version)) throw invalidInput();
}

function validateRecipeID(id: string): void {
  if (!isOpaqueID(id)) throw invalidInput();
}

function validateMenuID(id: string): void {
  if (!isOpaqueID(id)) throw invalidInput();
}

function validateEntryID(id: string): void {
  if (!isOpaqueID(id)) throw invalidInput();
}

function menuPath(id: string): string {
  validateMenuID(id);
  return `/api/menus/${encodeURIComponent(id)}`;
}

function versionedPath(path: string, version: number): string {
  const params = new URLSearchParams({ version: String(version) });
  return `${path}?${params}`;
}

function jsonMutation(method: "POST" | "PUT", body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    cache: "no-store",
    body: JSON.stringify(body),
  };
}

async function readJson(response: Response, fallback: string, expectedStatus = 200): Promise<unknown> {
  if (response.status !== expectedStatus) {
    const error = await response.clone().json().catch(() => null) as unknown;
    const code = isRecord(error) && typeof error.error === "string" ? error.error : undefined;
    throw menuError(response.status, code, fallback);
  }
  try {
    return await response.json() as unknown;
  } catch {
    throw invalidResponse(fallback, response.status);
  }
}

function menuError(status: number, code: string | undefined, fallback: string): MenuApiError {
  const messages: Record<string, string> = {
    unauthorized: "Sign in to manage your private menus.",
    invalid_json: "The menu request was invalid.",
    invalid_menu: "The menu request was invalid.",
    invalid_pagination: "The menu page request was invalid.",
    not_found: "This menu is unavailable.",
    version_conflict: "This menu changed elsewhere. Reload it before saving.",
    menu_limit_reached: "The menu or entry limit has been reached.",
    recipe_unavailable: "This recipe is no longer available to add.",
    method_not_allowed: "This menu action is unavailable.",
    storage_error: "The menu service is unavailable. Try again later.",
  };
  return new MenuApiError(code ? (messages[code] ?? fallback) : fallback, status, code);
}

function invalidResponse(message: string, status = 200): MenuApiError {
  return new MenuApiError(message, status);
}

function invalidMenuResponse(): MenuApiError {
  return invalidResponse("Could not load this menu.");
}

function invalidInput(): MenuApiError {
  return new MenuApiError("The menu request was invalid.", 400, "invalid_menu");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isOpaqueID(value: unknown): value is string {
  return typeof value === "string" && /^[a-f0-9]{32}$/.test(value);
}

function isPositiveSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function isNonnegativeSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function isBoundedOffset(value: unknown): value is number {
  return isNonnegativeSafeInteger(value) && value <= MENU_MAX_OFFSET;
}

function isTimestamp(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && Number.isFinite(Date.parse(value));
}

function isBoundedText(value: unknown, maxRunes: number, maxBytes: number, required: boolean): value is string {
  if (typeof value !== "string" || value.includes("\0") || Array.from(value).length > maxRunes || new TextEncoder().encode(value).length > maxBytes) return false;
  return !required || (value.length > 0 && value.trim().length > 0);
}
