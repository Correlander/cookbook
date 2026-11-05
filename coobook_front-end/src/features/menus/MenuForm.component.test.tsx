import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { PrivateMenu } from "../../apiModels";
import MenuForm from "./MenuForm.tsx";

const menuID = "a".repeat(32);

function menu(version = 2): PrivateMenu {
  return {
    id: menuID,
    title: "Weeknight meals",
    note: "Keep it simple",
    version,
    created_at: "2026-11-04T20:00:00Z",
    updated_at: "2026-11-04T21:00:00Z",
    entries: [],
  };
}

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function installFetch(...responses: Response[]) {
  const fetchMock = vi.fn(() => Promise.resolve(responses.shift() ?? jsonResponse({}, 500)));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("requires an explicit creator choice before creating and sends the selected unknown choice", async () => {
  const saved = vi.fn();
  const fetchMock = installFetch(jsonResponse(menu(1), 201));
  render(<MenuForm menu={null} onClose={vi.fn()} onSaved={saved} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "  Simple meals  " } });
  fireEvent.change(screen.getByLabelText("Note"), { target: { value: "Prep ahead" } });
  fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
  expect((await screen.findByRole("alert")).textContent).toBe("Choose who created this menu before saving.");
  expect(fetchMock).not.toHaveBeenCalled();

  fireEvent.change(screen.getByRole("combobox", { name: "Creator credit" }), { target: { value: "unknown" } });
  fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
  await waitFor(() => expect(saved).toHaveBeenCalledWith(expect.objectContaining({ version: 1 })));
  expect(fetchMock).toHaveBeenCalledWith("/api/menus", expect.objectContaining({ method: "POST" }));
  expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
    title: "Simple meals",
    note: "Prep ahead",
    creator_attribution: { kind: "unknown" },
  });
});

it("sends an explicit unverified name claim and validates the backend character and byte limits", async () => {
  const fetchMock = installFetch(jsonResponse(menu(1), 201));
  render(<MenuForm menu={null} onClose={vi.fn()} onSaved={vi.fn()} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "A" } });
  fireEvent.change(screen.getByRole("combobox", { name: "Creator credit" }), { target: { value: "name_claim" } });
  fireEvent.change(screen.getByLabelText("Creator name (unverified)"), { target: { value: "  A cook  " } });
  fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toMatchObject({
    creator_attribution: { kind: "name_claim", name: "A cook" },
  });

  cleanup();
  const invalidFetch = installFetch(jsonResponse(menu(1), 201));
  render(<MenuForm menu={null} onClose={vi.fn()} onSaved={vi.fn()} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "x".repeat(161) } });
  fireEvent.change(screen.getByRole("combobox", { name: "Creator credit" }), { target: { value: "local_profile" } });
  fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
  expect((await screen.findByRole("alert")).textContent).toContain("up to 160 characters");
  expect(invalidFetch).not.toHaveBeenCalled();

  cleanup();
  const invalidNameFetch = installFetch(jsonResponse(menu(1), 201));
  render(<MenuForm menu={null} onClose={vi.fn()} onSaved={vi.fn()} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "A" } });
  fireEvent.change(screen.getByRole("combobox", { name: "Creator credit" }), { target: { value: "name_claim" } });
  fireEvent.change(screen.getByLabelText("Creator name (unverified)"), { target: { value: "x".repeat(81) } });
  fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
  expect((await screen.findByRole("alert")).textContent).toContain("up to 80 characters");
  expect(invalidNameFetch).not.toHaveBeenCalled();
});

it("edits metadata with the current version and never submits creator fields", async () => {
  const fetchMock = installFetch(jsonResponse(menu(3)));
  const saved = vi.fn();
  render(<MenuForm menu={menu()} onClose={vi.fn()} onSaved={saved} />);
  expect(screen.queryByRole("combobox", { name: "Creator credit" })).toBeNull();
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Updated meals" } });
  fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
  await waitFor(() => expect(saved).toHaveBeenCalledWith(expect.objectContaining({ version: 3 })));
  expect(fetchMock).toHaveBeenCalledWith(`/api/menus/${menuID}`, expect.objectContaining({ method: "PUT" }));
  expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({ title: "Updated meals", note: "Keep it simple", version: 2 });
});

it("keeps the edit draft and explains a stale version conflict without retrying", async () => {
  const fetchMock = installFetch(jsonResponse({ error: "version_conflict" }, 409), jsonResponse(menu(8)), jsonResponse(menu(9)));
  render(<MenuForm menu={menu()} onClose={vi.fn()} onSaved={vi.fn()} />);
  const title = screen.getByLabelText("Title") as HTMLInputElement;
  const note = screen.getByLabelText("Note") as HTMLTextAreaElement;
  fireEvent.change(title, { target: { value: "Draft title" } });
  fireEvent.change(note, { target: { value: "Draft note" } });
  fireEvent.click(screen.getByRole("button", { name: "Save menu" }));

  expect((await screen.findByRole("alert")).textContent).toContain("This menu changed elsewhere. Reload it before saving.");
  expect(title.value).toBe("Draft title");
  expect(note.value).toBe("Draft note");
  expect(fetchMock).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Load latest menu version" }));
  expect((await screen.findByRole("status")).textContent).toContain("Your title and note draft are unchanged");
  expect(title.value).toBe("Draft title");
  expect(note.value).toBe("Draft note");
  fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
  expect(fetchMock.mock.calls[1]?.[0]).toBe(`/api/menus/${menuID}`);
  expect(fetchMock.mock.calls[2]?.[0]).toBe(`/api/menus/${menuID}`);
  expect(JSON.parse(String(fetchMock.mock.calls[2]?.[1]?.body))).toEqual({ title: "Draft title", note: "Draft note", version: 8 });
});

it("keeps an unexpected save error inline with title and note intact", async () => {
  const fetchMock = installFetch(jsonResponse({ error: "storage_error" }, 500));
  render(<MenuForm menu={menu()} onClose={vi.fn()} onSaved={vi.fn()} />);
  const title = screen.getByLabelText("Title") as HTMLInputElement;
  const note = screen.getByLabelText("Note") as HTMLTextAreaElement;
  fireEvent.change(title, { target: { value: "Retry later" } });
  fireEvent.change(note, { target: { value: "Keep this note" } });
  fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
  expect((await screen.findByRole("alert")).textContent).toContain("The menu service is unavailable. Try again later.");
  expect(title.value).toBe("Retry later");
  expect(note.value).toBe("Keep this note");
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
