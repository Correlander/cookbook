import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { Cookbook } from "../../apiModels";
import CookbookForm from "./CookbookForm.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const cookbook: Cookbook = {
  id: "book-1", title: "Private table", description: "Old notes", visibility: "private", owner_id: "owner-1",
};

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

it("retains a public cookbook edit after the exact publisher requirement and waits for an explicit resubmit", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(json({ error: "publisher_profile_required" }, 409));
  vi.stubGlobal("fetch", fetchMock);
  render(<CookbookForm book={cookbook} onClose={vi.fn()} onSaved={vi.fn()} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Revised table" } });
  fireEvent.change(screen.getByLabelText("Description"), { target: { value: "New notes" } });
  fireEvent.change(screen.getByLabelText("Visibility"), { target: { value: "public" } });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

  expect(await screen.findByRole("link", { name: /opens in a new tab/i })).toBeTruthy();
  expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("Revised table");
  expect((screen.getByLabelText("Description") as HTMLTextAreaElement).value).toBe("New notes");
  expect((screen.getByLabelText("Visibility") as HTMLSelectElement).value).toBe("public");
  expect(fetchMock).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
});

it("keeps private cookbook creation available without a publisher profile", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(json({ ...cookbook, visibility: "private" }));
  vi.stubGlobal("fetch", fetchMock);
  const saved = vi.fn();
  render(<CookbookForm onClose={vi.fn()} onSaved={saved} />);
  fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Private starter" } });
  fireEvent.click(screen.getByRole("button", { name: "Create private cookbook" }));
  await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  expect(fetchMock).toHaveBeenCalledWith("/api/cookbooks", expect.objectContaining({ method: "POST" }));
});

it("keeps unrelated cookbook conflicts on the ordinary error path", async () => {
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(json({ error: "version_conflict" }, 409)));
  render(<CookbookForm book={cookbook} onClose={vi.fn()} onSaved={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.queryByRole("link", { name: /creator profile/i })).toBeNull();
});
