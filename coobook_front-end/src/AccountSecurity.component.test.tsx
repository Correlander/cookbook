import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AccountSecurity from "./AccountSecurity.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
it("explains saved-reference export limits and displays a supplied course import count", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => String(input) === "/api/import"
    ? Promise.resolve(new Response(JSON.stringify({ recipes: 2, cookbooks: 1, course_bookmarks: 3 }), { status: 200 }))
    : Promise.resolve(new Response("[]", { status: 200 })));
  vi.stubGlobal("fetch", fetchMock);
  const alert = vi.fn(); vi.stubGlobal("alert", alert);
  render(<AccountSecurity />);
  expect(screen.getByText(/Course lessons and course content are not exported/)).toBeTruthy();
  expect(screen.getByText(/saved course references remain references and are not mapped/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Import a Cookbook export (ZIP or legacy JSON)"), { target: { files: [new File(["archive"], "export.json", { type: "application/json" })] } });
  await waitFor(() => expect(alert).toHaveBeenCalledWith("Imported 2 recipes and 1 cookbooks as private copies, plus 3 saved course references."));
});
