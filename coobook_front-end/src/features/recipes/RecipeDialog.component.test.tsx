import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { PublicRecipeDetail, Recipe } from "../../apiModels";
import RecipeDialog from "./RecipeDialog.tsx";

const recipe: Recipe = {
  id: "recipe-1",
  owner_id: "owner-1",
  creator_name: "Avery Cook",
  creator_attributions: [
    {
      subject_kind: "local",
      profile: { id: "profile-1", public_name: "Avery Cook", visibility: "instance" },
      role: "creator",
      provenance: "self_asserted_at_creation",
    },
    {
      subject_kind: "external_claim",
      name_claim: "Avery Cook",
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
  can_edit: false,
  can_delete: false,
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const publicRecipe: PublicRecipeDetail = {
  id: "public-recipe",
  creator_name: "Legacy Cook",
  title: "Tomato soup",
  description: "A warm soup.",
  ingredients: [{ name: "Tomato" }],
  steps: [{ text: "Cook." }],
  servings: 2,
  prep_time_minutes: 5,
  cook_time_minutes: 10,
  tags: [],
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderRecipeDialog(currentRecipe: Recipe | PublicRecipeDetail, currentUserID: string | null) {
  return render(
    <RecipeDialog
      recipe={currentRecipe}
      currentUserID={currentUserID}
      onClose={vi.fn()}
      onEdit={vi.fn()}
      onRestored={vi.fn()}
      onDelete={vi.fn(async () => {})}
      onTransfer={vi.fn()}
      canEdit={false}
      canDelete={false}
      canFork={false}
      presentation="page"
    />,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("keeps legacy text separate while linking only explicit local typed attribution", () => {
  renderRecipeDialog(recipe, "reader-1");

  expect(screen.getByText("By Avery Cook")).toBeTruthy();
  const credits = screen.getByRole("region", { name: "Recipe creator credits" });
  expect(within(credits).getByRole("link", { name: "Avery Cook" }).getAttribute("href"))
    .toBe("/creator?id=profile-1");
  expect(within(credits).getByText("Unverified creator claim: Avery Cook")).toBeTruthy();
  expect(within(credits).getByText("Creator · Self-reported when added")).toBeTruthy();
  expect(within(credits).getByText("Contributor · Imported claim")).toBeTruthy();
  expect(within(credits).getAllByRole("link")).toHaveLength(1);
});

it("does not render typed profile links for the anonymous public projection", () => {
  renderRecipeDialog(publicRecipe, null);

  expect(screen.getByText("By Legacy Cook")).toBeTruthy();
  expect(screen.queryByRole("region", { name: "Recipe creator credits" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Legacy Cook" })).toBeNull();
});
