import type { Recipe } from "../../apiModels";
import RecipeCardContents from "./RecipeCardContents.tsx";

export default function RecipeCardLink({
  recipe,
  currentUserID,
}: {
  recipe: Recipe;
  currentUserID: string;
}) {
  const relationshipLabel =
    recipe.owner_id === currentUserID
      ? recipe.source_recipe_id
        ? "Owned by you · Your copy"
        : "Owned by you"
      : "Available to you";
  return (
    <a
      className="recipe-card block"
      href={`/recipe?id=${encodeURIComponent(recipe.id)}`}
      aria-label={`Open ${recipe.title}`}
    >
      <RecipeCardContents
        title={recipe.title}
        creatorName={recipe.creator_name}
        creatorAttributions={recipe.creator_attributions}
        image={recipe.image}
        stepCount={recipe.steps?.length ?? 0}
        relationshipLabel={relationshipLabel}
      />
    </a>
  );
}
