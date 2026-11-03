import type { PublicRecipeDetail, Recipe } from "../../apiModels";

export type RecipeDetailRead = Recipe | PublicRecipeDetail;

export async function fetchRecipeDetail(
  recipeID: string,
  signedIn: boolean,
): Promise<RecipeDetailRead> {
  const endpoint = signedIn ? "/api/recipes/" : "/api/public/recipes/";
  const response = await fetch(
    `${endpoint}${encodeURIComponent(recipeID)}`,
    { cache: "no-store" },
  );
  if (!response.ok) throw new Error("Recipe unavailable");
  return response.json() as Promise<RecipeDetailRead>;
}
