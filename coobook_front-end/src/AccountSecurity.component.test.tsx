import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AccountSecurity from "./AccountSecurity.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function setupImportResult(result: Record<string, number>) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input) === "/api/import"
    ? Promise.resolve(new Response(JSON.stringify(result), { status: 200 }))
    : Promise.resolve(new Response("[]", { status: 200 })));
  vi.stubGlobal("fetch", fetchMock);
  const alert = vi.fn();
  vi.stubGlobal("alert", alert);
  render(<AccountSecurity />);
  fireEvent.change(screen.getByLabelText("Import a Cookbook export (ZIP or legacy JSON)"), { target: { files: [new File(["archive"], "export.json", { type: "application/json" })] } });
  return alert;
}

it("explains course portability and reports v7 course counts and omitted links", async () => {
  const alert = setupImportResult({ recipes: 2, cookbooks: 1, courses: 2, course_lessons: 5, omitted_course_recipe_links: 3, course_bookmarks: 4 });
  expect(screen.getByText(/Exports include owned recipes, cookbooks, courses, and lesson text/)).toBeTruthy();
  expect(screen.getByText(/other links are omitted and can be relinked after import/)).toBeTruthy();
  expect(screen.getByText(/Saved course references remain references and are not mapped/)).toBeTruthy();
  await waitFor(() => expect(alert).toHaveBeenCalledWith("Imported 2 recipes and 1 cookbooks as private copies, 2 courses and 5 lessons; 3 course recipe links were omitted and need relinking, plus 4 saved course references."));
});

it("keeps legacy import summaries valid without v7 counters", async () => {
  const alert = setupImportResult({ recipes: 2, cookbooks: 1, course_bookmarks: 3 });
  await waitFor(() => expect(alert).toHaveBeenCalledWith("Imported 2 recipes and 1 cookbooks as private copies, plus 3 saved course references."));
});

it("defaults to reference-only and reports v8 snapshot counts without claiming trust", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input) === "/api/import"
    ? Promise.resolve(new Response(JSON.stringify({ recipes: 0, cookbooks: 0, snapshot_copies: 1, snapshot_references: 2 }), { status: 201 }))
    : Promise.resolve(new Response("[]", { status: 200 })));
  vi.stubGlobal("fetch", fetchMock);
  const alert = vi.fn();
  vi.stubGlobal("alert", alert);
  render(<AccountSecurity />);
  const choice = screen.getByLabelText(/I am restoring my own personal account/);
  expect((choice as HTMLInputElement).checked).toBe(false);
  fireEvent.change(screen.getByLabelText("Import a Cookbook export (ZIP or legacy JSON)"), { target: { files: [new File(["archive"], "export.zip")] } });

  await waitFor(() => expect(alert).toHaveBeenCalledWith("Imported 0 recipes and 0 cookbooks as private copies; 1 private saved copy preserved as unverified and 2 saved references imported as references."));
  const request = fetchMock.mock.calls.find(([input]) => String(input) === "/api/import");
  expect(request?.[1]?.headers).toEqual({ "Content-Type": "application/json" });
  expect((screen.getByLabelText(/I am restoring my own personal account/) as HTMLInputElement).checked).toBe(false);
});

it("sends the snapshot preservation header only after explicit affirmation", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input) === "/api/import"
    ? Promise.resolve(new Response(JSON.stringify({ recipes: 0, cookbooks: 0, snapshot_copies: 1, snapshot_references: 0 }), { status: 201 }))
    : Promise.resolve(new Response("[]", { status: 200 })));
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("alert", vi.fn());
  render(<AccountSecurity />);
  fireEvent.click(screen.getByLabelText(/I am restoring my own personal account/));
  fireEvent.change(screen.getByLabelText("Import a Cookbook export (ZIP or legacy JSON)"), { target: { files: [new File(["archive"], "export.zip")] } });

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/api/import", expect.objectContaining({
    headers: {
      "Content-Type": "application/json",
      "X-Cookbook-Preserve-Personal-Snapshots": "true",
    },
  })));
});

it("explains an existing-pin conflict without silently retrying", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input) === "/api/import"
    ? Promise.resolve(new Response(JSON.stringify({ error: "snapshot_already_pinned" }), { status: 409 }))
    : Promise.resolve(new Response("[]", { status: 200 })));
  vi.stubGlobal("fetch", fetchMock);
  render(<AccountSecurity />);
  fireEvent.change(screen.getByLabelText("Import a Cookbook export (ZIP or legacy JSON)"), { target: { files: [new File(["archive"], "export.zip")] } });

  expect((await screen.findByRole("alert")).textContent).toContain("A saved copy already exists in this account. The import stopped without replacing it.");
  expect(fetchMock.mock.calls.filter(([input]) => String(input) === "/api/import")).toHaveLength(1);
});

it("identifies restricted snapshot conflicts as destination account state", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input) === "/api/import"
    ? Promise.resolve(new Response(JSON.stringify({ error: "snapshot_restricted" }), { status: 409 }))
    : Promise.resolve(new Response("[]", { status: 200 }))));
  render(<AccountSecurity />);
  fireEvent.change(screen.getByLabelText("Import a Cookbook export (ZIP or legacy JSON)"), { target: { files: [new File(["archive"], "export.zip")] } });

  expect((await screen.findByRole("alert")).textContent).toContain("A restricted saved copy already exists in this account. The import stopped without substituting a reference.");
});
