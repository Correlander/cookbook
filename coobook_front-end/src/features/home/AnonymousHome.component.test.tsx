import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import AnonymousHome from "./AnonymousHome.tsx";

afterEach(cleanup);

it("leads with personal kitchen value and a sign-in path for library-first", () => {
  const onSignIn = vi.fn();
  render(
    <AnonymousHome
      preset="library_first"
      publicInstance
      onSignIn={onSignIn}
    />,
  );

  expect(screen.getByRole("heading", { name: /home for the recipes/i })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: /sign in or create/i }));
  expect(onSignIn).toHaveBeenCalledOnce();
  expect(screen.queryByRole("link", { name: "Browse recipes" })).toBeNull();
});

it("leads to existing Browse for discovery-first without embedding the Browse experience", () => {
  render(
    <AnonymousHome
      preset="discovery_first"
      publicInstance
      onSignIn={() => {}}
    />,
  );

  const browse = screen.getByRole("link", { name: "Browse recipes" });
  expect(browse.getAttribute("href")).toBe("/discover");
  expect(screen.getByRole("heading", { name: /find something good/i })).toBeTruthy();
  expect(screen.queryByRole("search")).toBeNull();
});

it("shows only a private-instance sign-in state for a stored discovery preset", () => {
  render(
    <AnonymousHome
      preset="discovery_first"
      publicInstance={false}
      onSignIn={() => {}}
    />,
  );

  expect(screen.getByText(/this instance keeps its recipes private/i)).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Browse recipes" })).toBeNull();
});
