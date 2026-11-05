import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { MenuEntryView, PrivateMenu } from "../../apiModels";
import MenuDetail from "./MenuDetail.tsx";

const menuID = "a".repeat(32);
const recipeID = "b".repeat(32);
const entryOneID = "c".repeat(32);
const entryTwoID = "d".repeat(32);

function entry(id: string, position: number, available = true): MenuEntryView {
  return available
    ? { id, position, label: "", note: "", serving_note: "", available: true, recipe: { id: recipeID, title: "Tomato soup" } }
    : { id, position, label: "Main", note: "Keep warm", serving_note: "Serve promptly", available: false };
}

function menu(version = 1, entries: MenuEntryView[] = [entry(entryOneID, 0), entry(entryTwoID, 1)]): PrivateMenu {
  return {
    id: menuID,
    title: "Dinner plan",
    note: "For Friday",
    version,
    created_at: "2026-11-04T20:00:00Z",
    updated_at: "2026-11-04T21:00:00Z",
    entries,
  };
}

function response(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function installMenuFetch(responses: Response[], recipes: Response = response([])) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).startsWith("/api/recipes")
    ? Promise.resolve(recipes)
    : Promise.resolve(responses.shift()!));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise; });
  return { promise, resolve, reject };
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("loads the authoritative menu and reorders duplicate recipe references with the returned version and stable focus", async () => {
  const starting = menu(4, [entry(entryOneID, 0), entry(entryTwoID, 1)]);
  const reordered = menu(5, [entry(entryTwoID, 0), entry(entryOneID, 1)]);
  const fetchMock = installMenuFetch([response(starting), response(reordered), response(menu(6, [entry(entryOneID, 0)]))]);
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  const move = (await screen.findAllByRole("button", { name: "Move Tomato soup down" }))[0]!;
  move.focus();
  fireEvent.click(move);
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/entries/order"))).toBe(true));
  const reorderCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith("/entries/order"));
  expect(JSON.parse(String(reorderCall?.[1]?.body))).toEqual({ version: 4, entry_ids: [entryTwoID, entryOneID] });
  await waitFor(() => expect(move.disabled).toBe(true));
  expect(document.activeElement).toBe((await screen.findAllByRole("button", { name: "Move Tomato soup up" }))[1]);
  expect(screen.getByRole("status").textContent).toBe("Menu order saved.");
  fireEvent.click(screen.getAllByRole("button", { name: "Remove entry" })[0]!);
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url) === `/api/menus/${menuID}/entries/${entryTwoID}?version=5`)).toBe(true));
});

it("preserves edit drafts on a version conflict and offers explicit reload without retrying", async () => {
  const fetchMock = installMenuFetch([response(menu(2)), response({ error: "version_conflict" }, 409), response(menu(7))]);
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  fireEvent.click(await screen.findAllByRole("button", { name: "Edit entry" }).then((buttons) => buttons[0]!));
  const note = screen.getByLabelText("Note") as HTMLTextAreaElement;
  fireEvent.change(note, { target: { value: "Draft survives conflict" } });
  fireEvent.click(screen.getByRole("button", { name: "Save entry" }));
  expect((await screen.findByRole("alert")).textContent).toContain("This menu changed elsewhere. Reload it before saving.");
  expect(note.value).toBe("Draft survives conflict");
  expect(fetchMock.mock.calls.filter(([url]) => String(url).includes(`/api/menus/${menuID}/entries/`))).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload latest menu" }));
  await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => String(url) === `/api/menus/${menuID}`).length).toBe(2));
  const updateCall = fetchMock.mock.calls.find(([url]) => String(url).includes(`/api/menus/${menuID}/entries/`));
  expect(JSON.parse(String(updateCall?.[1]?.body))).toEqual({ label: "", note: "Draft survives conflict", serving_note: "", version: 2 });
});

it("keeps unavailable targets redacted and edits only owner text fields", async () => {
  const unresolved = menu(9, [entry(entryOneID, 0, false)]);
  const updated = menu(10, [{ ...entry(entryOneID, 0, false), label: "Dessert", note: "Chill first" }]);
  const fetchMock = installMenuFetch([response(unresolved), response(updated)]);
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  await screen.findByText("This recipe is unavailable. Its target and content are private.");
  expect(screen.queryByRole("link")).toBeNull();
  expect(document.body.textContent).not.toContain(recipeID);
  fireEvent.click(screen.getByRole("button", { name: "Edit entry" }));
  fireEvent.change(screen.getByLabelText("Label"), { target: { value: "Dessert" } });
  fireEvent.change(screen.getByLabelText("Note"), { target: { value: "Chill first" } });
  fireEvent.click(screen.getByRole("button", { name: "Save entry" }));
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url) === `/api/menus/${menuID}/entries/${entryOneID}`)).toBe(true));
  const updateCall = fetchMock.mock.calls.find(([url]) => String(url) === `/api/menus/${menuID}/entries/${entryOneID}`);
  expect(JSON.parse(String(updateCall?.[1]?.body))).toEqual({ label: "Dessert", note: "Chill first", serving_note: "Serve promptly", version: 9 });
  expect(JSON.parse(String(updateCall?.[1]?.body))).not.toHaveProperty("recipe_id");
});

it("adds a selected local recipe with explicit entry text and the current menu version", async () => {
  const added = menu(3, [entry(entryOneID, 0), { ...entry(entryTwoID, 1), recipe: { id: "e".repeat(32), title: "Fresh salad" } }]);
  const fetchMock = installMenuFetch([response(menu(2, [])), response(added, 201)], response([{ id: "e".repeat(32), title: "Fresh salad", ingredients: ["hidden"] }]));
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  fireEvent.click(await screen.findByRole("button", { name: "Add Fresh salad" }));
  fireEvent.change(screen.getByLabelText("Label"), { target: { value: "Side" } });
  fireEvent.click(screen.getByRole("button", { name: "Add recipe to menu" }));
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url) === `/api/menus/${menuID}/entries`)).toBe(true));
  expect(fetchMock.mock.calls[2]?.[0]).toBe(`/api/menus/${menuID}/entries`);
  expect(JSON.parse(String(fetchMock.mock.calls[2]?.[1]?.body))).toEqual({ recipe_id: "e".repeat(32), label: "Side", note: "", serving_note: "", version: 2 });
});

it("ignores a stale load and mutation after the actor or menu changes", async () => {
  const actorA = deferred<Response>();
  const actorB = deferred<Response>();
  const saveResponse = deferred<Response>();
  const menuReplies = [actorA.promise, actorB.promise, saveResponse.promise];
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).startsWith("/api/recipes")
    ? Promise.resolve(response([]))
    : menuReplies.shift()!);
  vi.stubGlobal("fetch", fetchMock);
  const view = render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  view.rerender(<MenuDetail actorID="actor-b" menuID={menuID} onDeleted={vi.fn()} />);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  actorB.resolve(response(menu(6)));
  await screen.findByRole("heading", { name: "Dinner plan" });
  fireEvent.click(screen.getAllByRole("button", { name: "Edit entry" })[0]!);
  fireEvent.change(screen.getByLabelText("Label"), { target: { value: "Actor B draft" } });
  fireEvent.click(screen.getByRole("button", { name: "Save entry" }));
  await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => String(url) === `/api/menus/${menuID}/entries/${entryOneID}`)).toBe(true));
  view.rerender(<MenuDetail actorID="actor-b" menuID={"f".repeat(32)} onDeleted={vi.fn()} />);
  actorA.resolve(response(menu(3)));
  saveResponse.resolve(response(menu(7)));
  await screen.findByRole("alert");
  expect(screen.queryByRole("heading", { name: "Dinner plan" })).toBeNull();
  expect(document.body.textContent).not.toContain("Actor B draft");
});

it("deletes only after confirmation and invokes the parent callback after API success", async () => {
  const confirm = vi.fn().mockReturnValue(true);
  vi.stubGlobal("confirm", confirm);
  const onDeleted = vi.fn();
  const fetchMock = installMenuFetch([response(menu(8)), response({ status: "menu_deleted" })]);
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={onDeleted} />);
  fireEvent.click(await screen.findByRole("button", { name: "Delete menu" }));
  await waitFor(() => expect(onDeleted).toHaveBeenCalledWith(menuID));
  expect(confirm).toHaveBeenCalledOnce();
  expect(fetchMock.mock.calls.some(([url]) => String(url) === `/api/menus/${menuID}?version=8`)).toBe(true);
});

it("reuses the accepted metadata form and applies the returned menu version", async () => {
  const changed = { ...menu(2), title: "Updated dinner plan" };
  const fetchMock = installMenuFetch([response(menu(1)), response(changed)]);
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  fireEvent.click(await screen.findByRole("button", { name: "Edit menu" }));
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Updated dinner plan" } });
  fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
  await screen.findByRole("heading", { name: "Updated dinner plan" });
  const updateCall = fetchMock.mock.calls.find(([url, init]) => String(url) === `/api/menus/${menuID}` && init?.method === "PUT");
  expect(JSON.parse(String(updateCall?.[1]?.body))).toEqual({ title: "Updated dinner plan", note: "For Friday", version: 1 });
});

it("reloads a metadata conflict inside the form and resubmits the preserved draft with the latest version", async () => {
  const updated = { ...menu(9), title: "Draft title", note: "Draft note" };
  const replies: Response[] = [response(menu(2)), response({ error: "version_conflict" }, 409), response(menu(8)), response(updated)];
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).startsWith("/api/recipes")
    ? Promise.resolve(response([]))
    : Promise.resolve(replies.shift()!));
  vi.stubGlobal("fetch", fetchMock);
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  fireEvent.click(await screen.findByRole("button", { name: "Edit menu" }));
  const title = screen.getByLabelText("Title") as HTMLInputElement;
  const note = screen.getByLabelText("Note") as HTMLTextAreaElement;
  fireEvent.change(title, { target: { value: "Draft title" } });
  fireEvent.change(note, { target: { value: "Draft note" } });
  fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "Load latest menu version" }));
  expect((await screen.findByRole("status")).textContent).toContain("Your title and note draft are unchanged");
  expect(title.value).toBe("Draft title");
  expect(note.value).toBe("Draft note");
  fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
  await screen.findByRole("heading", { name: "Draft title" });
  const menuWrites = fetchMock.mock.calls.filter(([url, init]) => String(url) === `/api/menus/${menuID}` && init?.method === "PUT");
  expect(menuWrites).toHaveLength(2);
  expect(JSON.parse(String(menuWrites[0]?.[1]?.body))).toEqual({ title: "Draft title", note: "Draft note", version: 2 });
  expect(JSON.parse(String(menuWrites[1]?.[1]?.body))).toEqual({ title: "Draft title", note: "Draft note", version: 8 });
});

it("does not apply focus from a failed reorder after a later explicit reload", async () => {
  const reloadResponse = deferred<Response>();
  const replies: Array<Promise<Response>> = [
    Promise.resolve(response(menu(2))),
    Promise.resolve(response({ error: "version_conflict" }, 409)),
    reloadResponse.promise,
  ];
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input).startsWith("/api/recipes")
    ? Promise.resolve(response([]))
    : replies.shift()!);
  vi.stubGlobal("fetch", fetchMock);
  render(<MenuDetail actorID="actor-a" menuID={menuID} onDeleted={vi.fn()} />);
  fireEvent.click((await screen.findAllByRole("button", { name: "Move Tomato soup down" }))[0]!);
  await screen.findByRole("alert");
  const reloadButton = screen.getByRole("button", { name: "Reload latest menu" });
  reloadButton.focus();
  fireEvent.click(reloadButton);
  reloadResponse.resolve(response(menu(6)));
  await screen.findByRole("heading", { name: "Dinner plan" });
  const entryMoveButtons = screen.getAllByRole("button", { name: /Move Tomato soup (up|down)/ });
  expect(entryMoveButtons).not.toContain(document.activeElement);
});
