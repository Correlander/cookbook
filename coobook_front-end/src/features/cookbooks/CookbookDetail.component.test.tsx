import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { Cookbook, PublicCookbook, PublicCookbookRecipe, Recipe } from "../../apiModels";
import CookbookDetail from "./CookbookDetail.tsx";

const cookbook: Cookbook = {
  id: "cookbook-1",
  title: "Family table",
  description: "Meals we keep.",
  visibility: "private",
  owner_id: "owner-1",
  creator_attributions: [{
    subject_kind: "local",
    profile: { id: "book-profile", public_name: "Collection keeper", visibility: "private" },
    role: "creator",
    provenance: "local_record_creation",
  }],
};

const recipe: Recipe = {
  id: "recipe-1",
  owner_id: "owner-1",
  creator_name: "Legacy Cook",
  creator_attributions: [
    {
      subject_kind: "local",
      profile: { id: "recipe-profile", public_name: "Recipe Adapter", visibility: "instance" },
      role: "adapter",
      provenance: "adapter_source",
    },
    {
      subject_kind: "external_claim",
      name_claim: "Recipe Adapter",
      role: "contributor",
      provenance: "portable_claim",
    },
  ],
  title: "Tomato soup",
  description: "A warm soup.",
  ingredients: [{ name: "Tomato" }],
  steps: [{ text: "Cook." }],
  servings: 2,
  prep_time_minutes: 5,
  cook_time_minutes: 10,
  tags: [],
  visibility: "private",
  can_edit: true,
  can_delete: true,
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const publicCookbook: PublicCookbook = {
  id: "public-book",
  title: "Summer table",
  description: "Seasonal recipes.",
  recipe_count: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const publicRecipe: PublicCookbookRecipe = {
  id: "public-recipe",
  creator_name: "Legacy Cook",
  title: "Tomato salad",
  description: "Fresh and simple.",
  ingredients: [{ name: "Tomatoes" }],
  steps: [{ text: "Slice." }],
  servings: 2,
  prep_time_minutes: 5,
  cook_time_minutes: 0,
  tags: [],
  visibility: "public",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("separates collection creator from recipe credits and keeps recipe cards non-interactive inside anchors", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input).startsWith("/api/recipes?cookbook_id=cookbook-1"))
      return Promise.resolve(jsonResponse([recipe]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(
    <CookbookDetail
      initialBook={cookbook}
      mode="authenticated"
      currentUserID="reader-1"
    />,
  );

  const collectionCredits = await screen.findByRole("region", {
    name: "Cookbook creator credits",
  });
  expect(within(collectionCredits).getByText("Collection creator · Recorded at cookbook creation"))
    .toBeTruthy();
  expect(within(collectionCredits).getByRole("link", { name: "Collection keeper" }).getAttribute("href"))
    .toBe("/creator?id=book-profile");

  const recipeCard = await screen.findByRole("link", { name: "Open Tomato soup" });
  expect(recipeCard.textContent).toContain("By Legacy Cook");
  expect(recipeCard.textContent).toContain("Adapter · Recorded as a source adapter");
  expect(recipeCard.textContent).toContain("Unverified creator claim: Recipe Adapter");
  expect(recipeCard.querySelector("a")).toBeNull();
  expect(screen.queryByRole("link", { name: "Recipe Adapter" })).toBeNull();
});

it("keeps public cookbook recipes on their independent legacy text projection", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input).startsWith("/api/public/cookbooks/public-book/recipes?"))
      return Promise.resolve(jsonResponse([publicRecipe]));
    return Promise.resolve(jsonResponse({}, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  render(
    <CookbookDetail
      initialBook={publicCookbook}
      mode="public"
      currentUserID={null}
    />,
  );

  const recipeCard = await screen.findByRole("link", { name: "Open Tomato salad" });
  expect(recipeCard.textContent).toContain("By Legacy Cook");
  expect(screen.queryByRole("region", { name: "Cookbook creator credits" })).toBeNull();
  expect(screen.queryByRole("region", { name: "Recipe creator credits" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Legacy Cook" })).toBeNull();
});
