import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import PublicCourses from "./PublicCourses.tsx";
import { AppSessionProvider } from "./app/AppSession.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
it("shows an Owner label and an ordinary detail link while retaining modal action", async () => {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input) === "/api/auth/me"
    ? Promise.resolve(new Response(null, { status: 401 }))
    : Promise.resolve(new Response(JSON.stringify([{ id: "c-2", owner_name: "Custodian", title: "Noodles", description: "", visibility: "public", lesson_count: 2, created_at: "now", updated_at: "now", creator_name: "Claimed chef", creator_attributions: [{ subject_kind: "external_claim", name_claim: "Imported teacher", role: "creator", provenance: "portable_claim" }] }]), { status: 200 }))));
  render(<AppSessionProvider><PublicCourses /></AppSessionProvider>);
  expect(await screen.findByRole("link", { name: "Noodles" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Noodles" }).getAttribute("href")).toBe("/course?id=c-2");
  expect(screen.getByText("Owner: Custodian")).toBeTruthy();
  expect(screen.getByText("Unverified creator name: Claimed chef")).toBeTruthy();
  expect(screen.getByText("Unverified course creator claim: Imported teacher")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Open course" })).toBeTruthy();
});
