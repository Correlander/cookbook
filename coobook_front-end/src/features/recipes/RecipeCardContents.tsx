import Icon from "./RecipeIcon.tsx";
import type { CreatorAttribution } from "../../apiModels";
import CreatorCredits from "../creator/CreatorCredits.tsx";

export interface RecipeCardContentsProps {
  title: string;
  creatorName: string;
  creatorAttributions?: CreatorAttribution[];
  image?: string;
  stepCount: number;
  relationshipLabel: string;
}

export default function RecipeCardContents({
  title,
  creatorName,
  creatorAttributions,
  image,
  stepCount,
  relationshipLabel,
}: RecipeCardContentsProps) {
  return (
    <>
      {image ? (
        <img src={image} alt="" loading="lazy" />
      ) : (
        <span className="recipe-card__placeholder" aria-hidden="true">
          <Icon name="leaf" size={34} />
        </span>
      )}
      <span className="recipe-card__body">
        <span className="recipe-card__category">
          {creatorName ? `By ${creatorName}` : "Recipe"}
        </span>
        <CreatorCredits
          attributions={creatorAttributions}
          resourceKind="recipe"
          variant="compact"
        />
        <strong>{title}</strong>
        <span className="recipe-card__category">{relationshipLabel}</span>
        <span className="recipe-card__meta">
          <Icon name="clock" size={15} />
          {stepCount ? `${stepCount} steps` : "Recipe"}
        </span>
      </span>
    </>
  );
}
