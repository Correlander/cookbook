import { readFileSync } from "node:fs";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import SessionMenu from "./SessionMenu.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("shows sign in without a session and never exposes Instance", () => {
  render(<SessionMenu user={null} pathname="/" onSessionChange={vi.fn()} />);
  expect(screen.getByRole("link", { name: "Sign in" }).getAttribute("href")).toBe("/?signin=1");
  expect(screen.queryByRole("link", { name: "Instance" })).toBeNull();
});

it("keeps a long display name accessible while constraining its header label", () => {
  const displayName = "Avery with a deliberately long account display name for tablet widths";
  render(<SessionMenu user={{ id: "u2", email: "avery@example.test", display_name: displayName, is_admin: false }} pathname="/" onSessionChange={vi.fn()} />);
  const trigger = screen.getByRole("button", { name: `${displayName} account menu` });
  const label = trigger.querySelector(".session-menu__name");
  expect(label?.textContent).toBe(displayName);
  expect(label?.getAttribute("title")).toBe(displayName);
  const stylesheet = readFileSync("src/index.css", "utf8");
  expect(stylesheet).toMatch(/\.session-menu__name\{[^}]*max-width:min\(12rem,15vw\)[^}]*overflow:hidden[^}]*text-overflow:ellipsis[^}]*white-space:nowrap/);
});

it("provides personal account links and conditionally exposes Instance", async () => {
  const onSessionChange = vi.fn();
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 200 })));
  render(<SessionMenu user={{ id: "u1", email: "cook@example.test", display_name: "Avery Cook", is_admin: false }} instanceHref="/admin" pathname="/" onSessionChange={onSessionChange} />);
  fireEvent.click(screen.getByRole("button", { name: "Avery Cook account menu" }));
  expect(screen.getByRole("link", { name: "Account" }).getAttribute("href")).toBe("/account/security");
  expect(screen.getByRole("link", { name: "Creator profile" }).getAttribute("href")).toBe("/account/creator-profile");
  expect(screen.getByRole("link", { name: "Instance" }).getAttribute("href")).toBe("/admin");
  fireEvent.keyDown(window, { key: "Escape" });
  await waitFor(() => expect(screen.queryByRole("link", { name: "Instance" })).toBeNull());
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "Avery Cook account menu" }));
  fireEvent.click(screen.getByRole("button", { name: "Avery Cook account menu" }));
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  await waitFor(() => expect(onSessionChange).toHaveBeenCalledTimes(1));
});
