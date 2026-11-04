import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AuthenticatedUser, Cookbook, PublicCookbook } from "../../apiModels";
import CookbookCollection from "./CookbookCollection.tsx";

const user: AuthenticatedUser = {
  id: "reader-1",
  email: "reader@example.test",
  display_name: "Reader",
  is_admin: false,
};

function response(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } });
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("keeps anonymous Discover on the bounded public catalog without management actions", async () => {
  const publicBook: PublicCookbook = {
    id: "public-1", title: "Public soups", description: "A public collection.",
    recipe_count: 2, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  };
  const fetchMock = vi.fn().mockResolvedValue(response([publicBook]));
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookCollection mode="public" surface="page" readOnly />);

  expect(await screen.findByRole("heading", { name: "Public soups" })).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith("/api/public/cookbooks?limit=24&offset=0", undefined);
  expect(screen.queryByRole("button", { name: /create cookbook/i })).toBeNull();
  expect(screen.queryByRole("button", { name: /edit cookbook/i })).toBeNull();
  expect(screen.queryByRole("button", { name: /manage members/i })).toBeNull();
});

it("shows current actor-readable owner and member cookbooks without management actions", async () => {
  const books: Cookbook[] = [
    { id: "own-1", owner_id: user.id, owner_name: "Reader", title: "My private book", description: "Own.", visibility: "private" },
    { id: "member-1", owner_id: "owner-2", owner_name: "Owner", title: "Shared with me", description: "Member access.", visibility: "private" },
  ];
  const fetchMock = vi.fn().mockResolvedValue(response(books));
  vi.stubGlobal("fetch", fetchMock);

  render(<CookbookCollection mode="accessible" surface="page" user={user} readOnly />);

  expect(await screen.findByRole("heading", { name: "My private book" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Shared with me" })).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith("/api/cookbooks?limit=24&offset=0", undefined);
  expect(screen.queryByRole("button", { name: /create cookbook/i })).toBeNull();
  expect(screen.queryByRole("button", { name: /edit cookbook/i })).toBeNull();
  expect(screen.queryByRole("button", { name: /manage members/i })).toBeNull();
});

it("preserves the default owner management controls for existing callers", async () => {
  const ownBook: Cookbook = {
    id: "own-1", owner_id: user.id, owner_name: "Reader", title: "My book", description: "Own.", visibility: "private",
  };
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response([ownBook])));

  render(<CookbookCollection mode="accessible" surface="page" user={user} />);

  expect(await screen.findByRole("button", { name: /create cookbook/i })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Edit cookbook" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Share and manage members" })).toBeTruthy();
});
