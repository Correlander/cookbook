import type { PublicRecipeContent } from "../../apiModels";

export type RemoteRecipe = PublicRecipeContent & {
  id: string;
  creator_name: string;
  source_origin: string;
  sequence: number;
};
