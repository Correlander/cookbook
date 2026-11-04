import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import LibraryCollection from "./LibraryCollection.tsx";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("labels owner, creator, membership and saved facts separately", () => {
  const onBookmarkChange = vi.fn();
  const onRemoveUnavailable = vi.fn();
  render(<LibraryCollection type="recipes" userID="reader" busyBookmarkID="" onBookmarkChange={onBookmarkChange} onMutationUncertain={vi.fn()} onRemoveUnavailable={onRemoveUnavailable} page={{
    total: 1, limit: 24, offset: 0, items: [{
      availability: "available", target_kind: "local", relationships: ["owned", "shared", "saved"], membership_role: "editor", in_owned_cookbook: true,
      recipe: { id: "local-1", owner_id: "reader", creator_name: "Original author", title: "Adapted stew", description: "", ingredients: [], steps: [{ text: "Cook." }], servings: 1, prep_time_minutes: 0, cook_time_minutes: 1, tags: [], visibility: "private", can_edit: true, can_delete: true, version: 1, created_at: "2026-01-01", updated_at: "2026-01-01" },
    }],
  }} />);
  expect(screen.getByRole("link", { name: "Open Adapted stew" }).getAttribute("href")).toBe("/recipe?id=local-1");
  expect(screen.getByText("Owned by you · Shared with you · editor · Saved to Library · In a cookbook you own")).toBeTruthy();
  expect(screen.getByText("By Original author")).toBeTruthy();
});

it("renders owned-cookbook context without claiming a save, membership, or creator credit", () => {
  render(<LibraryCollection type="recipes" userID="reader" busyBookmarkID="" onBookmarkChange={vi.fn()} onMutationUncertain={vi.fn()} onRemoveUnavailable={vi.fn()} page={{
    total: 1, limit: 24, offset: 0, items: [{
      availability: "available", target_kind: "local", relationships: ["in_owned_cookbook"],
      in_owned_cookbook: true, cookbook_contexts: [{ id: "owner-book" }],
      recipe: { id: "linked-1", owner_id: "cookbook-owner", title: "Cookbook soup", description: "", creator_name: "", ingredients: [], steps: [{ text: "Cook." }], servings: 1, prep_time_minutes: 0, cook_time_minutes: 1, tags: [], visibility: "private", can_edit: false, can_delete: false, version: 1, created_at: "2026-01-01", updated_at: "2026-01-01" },
    }],
  }} />);
  expect(screen.getByText("In a cookbook you own")).toBeTruthy();
  expect(screen.getByText("Cookbook context")).toBeTruthy();
  expect(screen.queryByText("Saved to Library")).toBeNull();
  expect(screen.queryByText("Shared with you")).toBeNull();
  expect(screen.queryByText("By cookbook-owner")).toBeNull();
  expect(screen.getByRole("button", { name: "Save to Library" })).toBeTruthy();
});

it("links remote recipes to their exact attributed cached detail snapshot", () => {
  render(<LibraryCollection type="recipes" userID="reader" busyBookmarkID="" onBookmarkChange={vi.fn()} onMutationUncertain={vi.fn()} onRemoveUnavailable={vi.fn()} page={{
    total: 1, limit: 24, offset: 0, items: [{ availability: "available", target_kind: "remote", relationships: ["saved"], bookmark_id: "remote-save", remote_recipe: {
      object_uri: "https://source.example/federation/recipes/abc", source_origin: "https://source.example", source_recipe_id: "abc", creator_name: "Remote chef", sequence: 2, updated_at: "2026-10-01", recipe: { title: "Remote stew", description: "Snapshot", ingredients: [], steps: [{ text: "Simmer." }], servings: 2, prep_time_minutes: 0, cook_time_minutes: 20, tags: [] },
    } }],
  }} />);
  expect(screen.getByText("Saved remote snapshot")).toBeTruthy();
  expect(screen.getByText(/Cached snapshot from https:\/\/source.example;.*This is not a live feed\./)).toBeTruthy();
  expect(screen.getByText("By Remote chef")).toBeTruthy();
  expect(screen.getByRole("link", { name: "Open Remote stew" }).getAttribute("href")).toBe("/recipe?remote_object_uri=https%3A%2F%2Fsource.example%2Ffederation%2Frecipes%2Fabc");
});

it("removes unavailable references through the parent without exposing stale content", () => {
  const remove = vi.fn();
  render(<LibraryCollection type="cookbooks" userID="reader" busyBookmarkID="" onBookmarkChange={vi.fn()} onMutationUncertain={vi.fn()} onRemoveUnavailable={remove} page={{
    total: 1, limit: 24, offset: 0, items: [{ availability: "unavailable", bookmark_id: "opaque-1", created_at: "2026-10-01" }],
  }} />);
  expect(document.body.textContent).not.toContain("Private old title");
  fireEvent.click(screen.getByRole("button", { name: "Remove unavailable cookbook reference" }));
  expect(remove).toHaveBeenCalledWith("opaque-1");
});
