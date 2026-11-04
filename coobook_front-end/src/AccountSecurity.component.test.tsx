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
