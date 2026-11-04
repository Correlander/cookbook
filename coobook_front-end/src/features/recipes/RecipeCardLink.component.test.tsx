import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { Recipe } from "../../apiModels";
import RecipeCardLink from "./RecipeCardLink.tsx";

const recipe: Recipe = {
  id: "recipe-1",
  owner_id: "owner-1",
  creator_name: "Legacy Cook",
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

afterEach(() => {
  cleanup();
});

it("shows separate legacy and typed credits as plain text inside the whole-card link", () => {
  render(<RecipeCardLink recipe={recipe} currentUserID="reader-1" />);

  const card = screen.getByRole("link", { name: "Open Tomato soup" });
  expect(card.getAttribute("href")).toBe("/recipe?id=recipe-1");
  expect(within(card).getByText("By Legacy Cook")).toBeTruthy();
  expect(within(card).getByText("Avery Cook")).toBeTruthy();
  expect(within(card).getByText("Unverified creator claim: Avery Cook")).toBeTruthy();
  expect(card.querySelector("a")).toBeNull();
});
