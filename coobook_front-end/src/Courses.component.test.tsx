import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import Courses from "./Courses.tsx";
import { AppSessionProvider } from "./app/AppSession.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
it("labels the owner and links the existing course card to its static page", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input) === "/api/auth/me"
    ? Promise.resolve(new Response(JSON.stringify({ id: "u-1", email: "owner@example.test", display_name: "Owner", is_admin: false }), { status: 200 }))
    : Promise.resolve(new Response(JSON.stringify([{ id: "c-1", owner_id: "u-1", owner_name: "Custodian", title: "Pasta", description: "", visibility: "private", lesson_count: 1, created_at: "now", updated_at: "now", creator_name: "Claimed chef", creator_attributions: [{ subject_kind: "external_claim", name_claim: "Imported teacher", role: "creator", provenance: "portable_claim" }] }]), { status: 200 }))));
  render(<AppSessionProvider><Courses /></AppSessionProvider>);
  expect(await screen.findByRole("link", { name: "Pasta" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Pasta" }).getAttribute("href")).toBe("/course?id=c-1");
  expect(screen.getByText("Owner: Custodian")).toBeTruthy();
  expect(screen.getByText("Unverified creator name: Claimed chef")).toBeTruthy();
  expect(screen.getByText("Unverified course creator claim: Imported teacher")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Open course" })).toBeTruthy();
});
