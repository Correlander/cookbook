import { lazy, useEffect, useRef, useState } from "react";
import FeatureIsland from "../../components/FeatureIsland.tsx";
import type { Recipe, RecipeRevision, RecipeRevisionDiff } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import { scaleQuantity } from "../../quantity.ts";
import useModalFocus from "../../useModalFocus.ts";
import Icon from "./RecipeIcon.tsx";
import type { RemoteRecipe } from "./recipeUiTypes.ts";

const CookingMode = lazy(() => import("../../CookingMode.tsx"));
const RecipePublicationPanel = lazy(
  () => import("./RecipePublicationPanel.tsx"),
);

async function apiFailure(
  response: Response,
  fallback: string,
): Promise<Error> {
  const body = await response
    .clone()
    .json()
    .catch(() => null);
  if (body?.error === "reauthentication_required")
    return new Error("Sign in again before transferring recipe ownership.");
  return new Error(fallback);
}
interface RecipeHistoryProps {
  recipe: Recipe;
  onRestored: (recipe: Recipe) => void;
}

function RecipeHistory({ recipe, onRestored }: RecipeHistoryProps) {
  const [revisions, setRevisions] = useState<RecipeRevision[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [restoring, setRestoring] = useState(false),
    [comparing, setComparing] = useState(false),
    [diff, setDiff] = useState<RecipeRevisionDiff | null>(null);
  useEffect(() => {
    let active = true;
    setLoading(true);
    setDiff(null);
    fetch(`/api/recipes/${recipe.id}/revisions`)
      .then(async (response) => {
        if (!response.ok) throw new Error("Could not load revision history.");
        const data = (await response.json()) as RecipeRevision[];
        if (active) setRevisions(data);
      })
      .catch((err: unknown) => {
        if (active)
          setError(errorMessage(err, "Could not load revision history."));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [recipe.id, recipe.version]);
  async function compare(version: number) {
    setError("");
    setComparing(true);
    try {
      const response = await fetch(
        `/api/recipes/${recipe.id}/revisions/diff?from=${version}&to=${recipe.version}`,
      );
      if (!response.ok) throw new Error("Could not compare these versions.");
      setDiff((await response.json()) as RecipeRevisionDiff);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not compare these versions."));
    } finally {
      setComparing(false);
    }
  }
  async function restore(version: number) {
    if (!window.confirm(`Restore version ${version} as a new revision?`))
      return;
    setError("");
    setRestoring(true);
    try {
      const response = await fetch(
        `/api/recipes/${recipe.id}/revisions/${version}/restore`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ expected_version: recipe.version }),
        },
      );
      if (response.status === 409)
        throw new Error(
          "This recipe changed elsewhere. Reload it before restoring.",
        );
      if (!response.ok) throw new Error("Could not restore this revision.");
      onRestored((await response.json()) as Recipe);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not restore this revision."));
    } finally {
      setRestoring(false);
    }
  }
  return (
    <div className="passkey-list" aria-label="Recipe version history">
      <h3>Version history</h3>
      {loading ? (
        <p>Loading history…</p>
      ) : (
        revisions.map((item) => (
          <div className="passkey-row" key={item.id}>
            <span>
              Version {item.version}: {item.title}
              <small>
                {item.editor_name} ·{" "}
                {new Date(item.created_at).toLocaleString()}
              </small>
            </span>
            {item.version < recipe.version && (
              <>
                <button
                  className="text-link"
                  type="button"
                  disabled={comparing}
                  onClick={() => compare(item.version)}
                >
                  Compare
                </button>
                <button
                  className="text-link"
                  type="button"
                  disabled={restoring}
                  onClick={() => restore(item.version)}
                >
                  Restore
                </button>
              </>
            )}
          </div>
        ))
      )}
      {diff && (
        <div>
          <h4>
            Changes from version {diff.from_version} to {diff.to_version}
          </h4>
          {Object.keys(diff.changes).length ? (
            Object.entries(diff.changes).map(([field, change]) => (
              <p key={field}>
                <strong>{field}:</strong> {JSON.stringify(change.before)} →{" "}
                {JSON.stringify(change.after)}
              </p>
            ))
          ) : (
            <p>No differences.</p>
          )}
        </div>
      )}
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
interface RecipeDialogProps {
  recipe: Recipe | RemoteRecipe | null;
  currentUserID: string | null;
  onClose: () => void;
  onEdit: (recipe: Recipe) => void;
  onRestored: (recipe: Recipe) => void;
  onDelete: (recipe: Recipe) => Promise<void>;
  onTransfer: (recipeID: string) => void;
  canEdit: boolean;
  canDelete: boolean;
  canFork: boolean;
}

function RecipeDialog({
  recipe,
  currentUserID,
  onClose,
  onEdit,
  onRestored,
  onDelete,
  onTransfer,
  canEdit,
  canDelete,
  canFork,
}: RecipeDialogProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(
    dialogRef,
    () => {
      if (!document.querySelector("[data-cooking-mode]")) onClose();
    },
    Boolean(recipe),
  );
  const [historyOpen, setHistoryOpen] = useState(false),
    [transferEmail, setTransferEmail] = useState(""),
    [transferring, setTransferring] = useState(false),
    [transferError, setTransferError] = useState(""),
    [cookingOpen, setCookingOpen] = useState(false),
    [deleteError, setDeleteError] = useState(""),
    [deleting, setDeleting] = useState(false),
    [targetServings, setTargetServings] = useState(""),
    [publicationOpen, setPublicationOpen] = useState(false);
  useEffect(() => {
    setHistoryOpen(false);
    setPublicationOpen(false);
    setDeleteError("");
    setTargetServings(recipe?.servings ? String(recipe.servings) : "");
  }, [recipe?.id, recipe?.servings]);
  const servingFactor =
    recipe && recipe.servings > 0 && Number(targetServings) > 0
      ? Number(targetServings) / recipe.servings
      : 1;
  const displayedIngredients =
    recipe?.ingredients?.map((item) => ({
      ...item,
      quantity: scaleQuantity(item.quantity ?? "", servingFactor),
    })) || [];
  if (!recipe) return null;
  const currentRecipe = recipe;
  async function moveToTrash(): Promise<void> {
    if (!("version" in currentRecipe)) return;
    if (!window.confirm(`Move “${currentRecipe.title}” to your trash?`)) return;
    setDeleting(true);
    setDeleteError("");
    try {
      await onDelete(currentRecipe);
    } catch (error: unknown) {
      setDeleteError(
        errorMessage(error, "Could not move this recipe to trash."),
      );
    } finally {
      setDeleting(false);
    }
  }
  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        ref={dialogRef}
        tabIndex={-1}
        className="recipe-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="dialog-title"
      >
        <button
          className="icon-button dialog-close"
          aria-label="Close recipe"
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
        {"image" in recipe && recipe.image ? (
          <img className="recipe-dialog__image" src={recipe.image} alt="" />
        ) : (
          <div className="recipe-dialog__placeholder" aria-hidden="true" />
        )}
        <div className="recipe-dialog__content">
          <span className="eyebrow">
            {recipe.creator_name || "Cookbook recipe"}
          </span>
          <h2 id="dialog-title">{recipe.title}</h2>
          <p>{recipe.description}</p>
          {"source_recipe_id" in recipe && recipe.source_recipe_id && (
            <p className="dialog-note">
              A copy of a recipe by {recipe.creator_name}.
            </p>
          )}
          {(recipe.prep_time_minutes || recipe.cook_time_minutes) && (
            <p className="dialog-note">
              {[
                recipe.prep_time_minutes &&
                  `Prep ${recipe.prep_time_minutes} min`,
                recipe.cook_time_minutes &&
                  `Cook ${recipe.cook_time_minutes} min`,
              ]
                .filter(Boolean)
                .join(" · ")}
            </p>
          )}
          {recipe.tags?.length > 0 && (
            <p>
              {recipe.tags.map((tag) => (
                <span className="recipe-card__category" key={tag}>
                  {tag}{" "}
                </span>
              ))}
            </p>
          )}
          {canEdit && "version" in recipe && (
            <button className="text-link" onClick={() => onEdit(recipe)}>
              Edit recipe
            </button>
          )}
          {"version" in recipe &&
            Boolean(currentUserID) &&
            recipe.owner_id === currentUserID && (
              <button
                className="text-link"
                type="button"
                onClick={() => setPublicationOpen(true)}
              >
                Manage publication
              </button>
            )}
          {publicationOpen &&
            "version" in recipe &&
            Boolean(currentUserID) &&
            recipe.owner_id === currentUserID && (
              <FeatureIsland name="Recipe publication" loading="publication settings">
                <RecipePublicationPanel
                  recipeId={recipe.id}
                  isOwner
                  isRemote={false}
                  open
                  onClose={() => setPublicationOpen(false)}
                />
              </FeatureIsland>
            )}
          {canFork && !canDelete && (
            <button
              className="text-link"
              type="button"
              onClick={async () => {
                try {
                  const response = await fetch(
                    `/api/recipes/${recipe.id}/fork`,
                    { method: "POST" },
                  );
                  if (!response.ok)
                    throw new Error("Could not save a copy of this recipe.");
                  onClose();
                  window.location.hash = "my-cookbooks";
                } catch (error: unknown) {
                  setDeleteError(
                    errorMessage(
                      error,
                      "Could not save a copy of this recipe.",
                    ),
                  );
                }
              }}
            >
              Save a copy
            </button>
          )}
          {recipe.steps?.length > 0 && (
            <button
              className="button button--green"
              type="button"
              onClick={() => setCookingOpen(true)}
            >
              Start cooking mode
            </button>
          )}
          {canEdit && (
            <button
              className="text-link"
              onClick={() => setHistoryOpen((value) => !value)}
            >
              Version history
            </button>
          )}
          {canDelete && (
            <button
              className="text-link"
              type="button"
              disabled={deleting}
              onClick={moveToTrash}
            >
              {deleting ? "Moving…" : "Move to trash"}
            </button>
          )}
          {canDelete && (
            <form
              className="ownership-transfer"
              onSubmit={async (event) => {
                event.preventDefault();
                setTransferError("");
                if (
                  !window.confirm(
                    `Transfer ownership of “${recipe.title}” to ${transferEmail}? This cannot be undone from this screen.`,
                  )
                )
                  return;
                setTransferring(true);
                try {
                  const response = await fetch(
                    `/api/recipes/${recipe.id}/transfer`,
                    {
                      method: "POST",
                      headers: { "Content-Type": "application/json" },
                      body: JSON.stringify({ email: transferEmail }),
                    },
                  );
                  if (!response.ok) {
                    if (response.status === 400)
                      throw new Error(
                        "Enter an email for an existing account.",
                      );
                    throw await apiFailure(
                      response,
                      "Could not transfer ownership.",
                    );
                  }
                  onTransfer(recipe.id);
                } catch (error: unknown) {
                  setTransferError(
                    errorMessage(error, "Could not transfer ownership."),
                  );
                } finally {
                  setTransferring(false);
                }
              }}
            >
              <label htmlFor="transfer-owner-email">
                Transfer ownership to an existing account
              </label>
              <input
                id="transfer-owner-email"
                type="email"
                autoComplete="off"
                value={transferEmail}
                onChange={(event) => setTransferEmail(event.target.value)}
                required
              />
              <button
                className="text-link"
                type="submit"
                disabled={transferring}
              >
                {transferring ? "Transferring…" : "Transfer recipe"}
              </button>
              {transferError && (
                <p className="form-message" role="alert">
                  {transferError}
                </p>
              )}
            </form>
          )}
          {deleteError && (
            <p className="form-message" role="alert">
              {deleteError}
            </p>
          )}
          {historyOpen && "version" in recipe && (
            <RecipeHistory recipe={recipe} onRestored={onRestored} />
          )}
          {recipe.servings > 0 && (
            <div className="servings-control">
              <label htmlFor="recipe-target-servings">Servings</label>
              <input
                id="recipe-target-servings"
                type="number"
                min="1"
                max="1000"
                step="1"
                inputMode="numeric"
                value={targetServings}
                onChange={(event) => setTargetServings(event.target.value)}
              />
              <span>Amounts adjust for the serving count you enter.</span>
            </div>
          )}
          <div className="recipe-columns">
            <div>
              <h3>Ingredients</h3>
              <ul>
                {displayedIngredients.map((item, index) => (
                  <li key={`${item.name}-${index}`}>
                    {[item.quantity, item.name].filter(Boolean).join(" ")}
                    {item.notes && <small> — {item.notes}</small>}
                  </li>
                ))}
              </ul>
            </div>
            <div>
              <h3>Instructions</h3>
              <ol>
                {(recipe.steps || []).map((step, index) => (
                  <li key={index}>{step.text}</li>
                ))}
              </ol>
            </div>
          </div>
          <p className="dialog-note">
            {recipe.source_origin
              ? `Public copy from ${recipe.source_origin}.`
              : "Recipe details from this Cookbook instance."}
          </p>
        </div>
        {cookingOpen && (
          <FeatureIsland name="Cooking mode" loading="cooking mode">
            <CookingMode
              recipe={{
                ...recipe,
                servings: Number(targetServings) || recipe.servings,
                ingredients: displayedIngredients,
              }}
              onClose={() => setCookingOpen(false)}
            />
          </FeatureIsland>
        )}
      </section>
    </div>
  );
}

export default RecipeDialog;
