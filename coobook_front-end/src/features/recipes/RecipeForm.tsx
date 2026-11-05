import { lazy, useEffect, useRef, useState } from "react";
import FeatureIsland from "../../components/FeatureIsland.tsx";
import type {
  Cookbook,
  InitialPublicationInput,
  Recipe,
  RecipeCreatePayload,
  RecipeWritePayload,
  AuthenticatedUser,
} from "../../apiModels";
import { errorMessage } from "../../apiModels";
import { createStep } from "../../steps.ts";
import useModalFocus from "../../useModalFocus.ts";
import Icon from "./RecipeIcon.tsx";
import type { RecipeCreatorAttribution } from "../../apiModels";
import { CreatorProfileRequestError, getMyCreatorProfile } from "../creator/creatorProfileApi.ts";
import CreatorAttributionFields from "../creator/CreatorAttributionFields.tsx";
import PublisherProfileSetupGuidance from "../creator/PublisherProfileSetupGuidance.tsx";

const StepListEditor = lazy(() => import("../../StepListEditor.tsx"));

interface RecipeFormProps {
  user: AuthenticatedUser | null;
  recipe: Recipe | null;
  onClose: () => void;
  onCreated: (recipe: Recipe) => void;
  onUpdated: (recipe: Recipe) => void;
  onSignedOut: () => void;
}

function RecipeForm({
  user,
  recipe,
  onClose,
  onCreated,
  onUpdated,
  onSignedOut,
}: RecipeFormProps) {
  return (
    <RecipeFormInstance
      key={
        recipe
          ? `edit:${user?.id ?? "anonymous"}:${recipe.id}`
          : `create:${user?.id ?? "anonymous"}`
      }
      user={user}
      recipe={recipe}
      onClose={onClose}
      onCreated={onCreated}
      onUpdated={onUpdated}
      onSignedOut={onSignedOut}
    />
  );
}

function RecipeFormInstance({
  user,
  recipe,
  onClose,
  onCreated,
  onUpdated,
  onSignedOut,
}: RecipeFormProps) {
  const dialogRef = useRef(null);
  useModalFocus(dialogRef, onClose);
  const [title, setTitle] = useState(recipe?.title || ""),
    [description, setDescription] = useState(recipe?.description || ""),
    [ingredients, setIngredients] = useState(
      (recipe?.ingredients || [])
        .map((item) =>
          [item.name, item.quantity || "", item.notes || ""].join(" | "),
        )
        .join("\n"),
    ),
    [steps, setSteps] = useState<ReturnType<typeof createStep>[]>(() =>
      recipe?.steps?.length
        ? recipe.steps.map((step) => createStep(step.text))
        : [createStep()],
    ),
    [servings, setServings] = useState(String(recipe?.servings || "")),
    [prepTime, setPrepTime] = useState(String(recipe?.prep_time_minutes || "")),
    [cookTime, setCookTime] = useState(String(recipe?.cook_time_minutes || "")),
    [tags, setTags] = useState((recipe?.tags || []).join(", ")),
    [visibility, setVisibility] = useState(recipe?.visibility || "private"),
    [initialPublicationState, setInitialPublicationState] = useState<
      InitialPublicationInput["publication_state"]
    >("unpublished"),
    [publicAccessConfirmed, setPublicAccessConfirmed] = useState(false),
    [unpublishedPublicAcknowledged, setUnpublishedPublicAcknowledged] =
      useState(false),
    [cookbooks, setCookbooks] = useState<Cookbook[]>([]),
    [cookbookIDs, setCookbookIDs] = useState<string[]>(
      recipe?.cookbook_ids || [],
    ),
    [imageFile, setImageFile] = useState<File | null>(null),
    [error, setError] = useState(""),
    [publisherProfileRequired, setPublisherProfileRequired] = useState(false),
    [saving, setSaving] = useState(false);
  const [creatorSupport, setCreatorSupport] = useState<"checking" | "ready" | "unsupported" | "error">(recipe ? "ready" : "checking");
  const [creatorChoice, setCreatorChoice] = useState<RecipeCreatorAttribution["kind"] | "">("");
  const [creatorName, setCreatorName] = useState("");
  const [legacyCreationConfirmed, setLegacyCreationConfirmed] = useState(false);
  const operationGeneration = useRef(0);
  const supportCheckGeneration = useRef(0);
  const sharingCapabilityKnown =
    !recipe || "can_manage_sharing" in recipe;
  const canManageCookbookSharing =
    !recipe ||
    recipe.can_manage_sharing === true ||
    (!sharingCapabilityKnown && recipe.owner_id === user?.id);
  const legacySharingSafetyBlocked =
    Boolean(recipe && !sharingCapabilityKnown && !canManageCookbookSharing);
  useEffect(
    () => () => {
      operationGeneration.current += 1;
      supportCheckGeneration.current += 1;
    },
    [],
  );
  useEffect(() => {
    if (!canManageCookbookSharing) return;
    fetch("/api/cookbooks")
      .then((response) => (response.ok ? response.json() : []))
      .then(setCookbooks)
      .catch(() => setCookbooks([]));
  }, [canManageCookbookSharing]);
  useEffect(() => {
    if (recipe) return;
    let active = true;
    const generation = ++supportCheckGeneration.current;
    setCreatorSupport("checking");
    void getMyCreatorProfile().then(() => {
      if (active && generation === supportCheckGeneration.current)
        setCreatorSupport("ready");
    }).catch((cause: unknown) => {
      if (!active || generation !== supportCheckGeneration.current) return;
      if (cause instanceof CreatorProfileRequestError && cause.status === 404) setCreatorSupport("unsupported");
      else setCreatorSupport("error");
    });
    return () => {
      active = false;
      if (generation === supportCheckGeneration.current)
        supportCheckGeneration.current += 1;
    };
  }, [recipe, user?.id]);

  async function retryCreatorSupport() {
    const generation = ++supportCheckGeneration.current;
    setCreatorSupport("checking");
    try {
      await getMyCreatorProfile();
      if (generation !== supportCheckGeneration.current) return;
      setCreatorSupport("ready");
    } catch (cause) {
      if (generation !== supportCheckGeneration.current) return;
      setCreatorSupport(cause instanceof CreatorProfileRequestError && cause.status === 404 ? "unsupported" : "error");
    }
  }
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setPublisherProfileRequired(false);
    if (legacySharingSafetyBlocked) {
      setError(
        "This server does not confirm safe cookbook sharing permissions. Update it before editing this recipe.",
      );
      return;
    }
    if (!recipe && creatorSupport === "checking") {
      setError("Checking whether this server supports typed recipe credit. Please wait or retry.");
      return;
    }
    if (!recipe && creatorSupport === "error") {
      setError("Could not check creator-profile support. Retry the check before saving.");
      return;
    }
    if (!recipe && creatorSupport === "ready" && !creatorChoice) {
      setError("Choose who created this recipe before saving.");
      return;
    }
    if (!recipe && creatorSupport === "unsupported" && !legacyCreationConfirmed) {
      setError("Confirm that you understand the legacy byline disclosure before saving.");
      return;
    }
    const trimmedCreatorName = creatorName.trim();
    if (!recipe && creatorChoice === "name_claim" && (Array.from(trimmedCreatorName).length < 1 || Array.from(trimmedCreatorName).length > 80 || new TextEncoder().encode(trimmedCreatorName).length > 320)) {
      setError("Enter a creator name of up to 80 characters.");
      return;
    }
    const confirmsPublicAccess =
      (!recipe && visibility === "public") ||
      Boolean(
        recipe && recipe.visibility !== "public" && visibility === "public",
      );
    if (confirmsPublicAccess && !publicAccessConfirmed) {
      setError("Confirm that anyone can read this recipe on a public instance.");
      return;
    }
    if (
      !recipe &&
      visibility === "public" &&
      initialPublicationState === "unpublished" &&
      !unpublishedPublicAcknowledged
    ) {
      setError("Confirm that this public recipe will stay hidden from anonymous browsing until you publish it.");
      return;
    }
    setSaving(true);
    const generation = operationGeneration.current;
    try {
      const payload: Omit<RecipeWritePayload, "cookbook_ids"> &
        Partial<Pick<RecipeWritePayload, "cookbook_ids">> = {
        title,
        description,
        servings: Number(servings) || 0,
        prep_time_minutes: Number(prepTime) || 0,
        cook_time_minutes: Number(cookTime) || 0,
        tags: tags
          .split(",")
          .map((tag) => tag.trim())
          .filter(Boolean),
        visibility,
        edit_policy: recipe?.edit_policy || "owner_only",
        ...(canManageCookbookSharing ? { cookbook_ids: cookbookIDs } : {}),
        ingredients: ingredients
          .split("\n")
          .map((line) => {
            const [name, quantity = "", notes = ""] = line
              .split("|")
              .map((part) => part.trim());
            return { name, quantity, notes };
          })
          .filter((item) => item.name),
        steps: steps
          .map((step) => ({ text: step.text.trim() }))
          .filter((item) => item.text),
        ...(confirmsPublicAccess && publicAccessConfirmed
          ? { confirm_public_access: true }
          : {}),
      };
      let requestPayload: RecipeWritePayload | RecipeCreatePayload = payload;
      if (recipe) payload.version = recipe.version;
      else {
        requestPayload = {
          ...payload,
          initial_publication: {
            publication_state: initialPublicationState,
            federation_mode: "off",
            index_eligible: false,
          },
          ...(creatorSupport === "ready" && creatorChoice
            ? { creator_attribution: creatorChoice === "name_claim" ? { kind: "name_claim", name: trimmedCreatorName } : { kind: creatorChoice } }
            : {}),
        };
      }
      const response = await fetch(
        recipe ? `/api/recipes/${recipe.id}` : "/api/recipes",
        {
          method: recipe ? "PUT" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(requestPayload),
        },
      );
      if (generation !== operationGeneration.current) return;
      if (!response.ok) {
        const failure: unknown = await response.json().catch(() => null);
        if (generation !== operationGeneration.current) return;
        const failureCode =
          failure && typeof failure === "object" && "error" in failure &&
          typeof failure.error === "string" ? failure.error : "";
        if (response.status === 409 && failureCode === "publisher_profile_required") {
          setPublisherProfileRequired(true);
          return;
        }
        if (failureCode === "version_conflict")
          throw new Error(
            "This recipe changed elsewhere. Reload it before saving.",
          );
        if (failureCode === "publication_confirmation_required")
          throw new Error(
            "Confirm that anyone can read this recipe on a public instance before saving.",
          );
        if (failureCode === "publication_unavailable")
          throw new Error(
            "Publishing requires public visibility and an instance that allows public browsing.",
          );
        if (failureCode === "instance_private")
          throw new Error(
            "Public visibility is disabled for this instance. Ask an administrator to enable public browsing.",
          );
        if (failureCode === "visibility_owner_only")
          throw new Error("Only the recipe owner can change its visibility.");
        throw new Error(
          "Could not save this recipe. Check the details and try again.",
        );
      }
      let saved = (await response.json()) as Recipe;
      if (generation !== operationGeneration.current) return;
      if (imageFile) {
        const formData = new FormData();
        formData.append("image", imageFile);
        const imageResponse = await fetch(`/api/recipes/${saved.id}/image`, {
          method: "PUT",
          body: formData,
        });
        if (generation !== operationGeneration.current) return;
        if (!imageResponse.ok) {
          if (!recipe && generation === operationGeneration.current)
            onCreated(saved);
          throw new Error("Recipe saved, but its image could not be uploaded.");
        }
        const imageResult = (await imageResponse.json()) as {
          image: string;
          version: number;
        };
        if (generation !== operationGeneration.current) return;
        saved = {
          ...saved,
          image: imageResult.image,
          version: imageResult.version,
        };
      }
      if (generation !== operationGeneration.current) return;
      if (recipe) onUpdated(saved);
      else onCreated(saved);
    } catch (err: unknown) {
      if (generation === operationGeneration.current)
        setError(errorMessage(err, "Could not save this recipe."));
    } finally {
      if (generation === operationGeneration.current) setSaving(false);
    }
  }
  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        className="signin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="recipe-form-title"
        ref={dialogRef}
        tabIndex={-1}
      >
        <button
          className="icon-button dialog-close"
          aria-label="Close"
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
        <span className="eyebrow">Your kitchen</span>
        <h2 id="recipe-form-title">
          {recipe ? "Edit recipe" : "Add a recipe"}
        </h2>
        <button
          className="text-link"
          type="button"
          onClick={async () => {
            await fetch("/api/auth/logout", { method: "POST" });
            onSignedOut();
          }}
        >
          Sign out
        </button>
        <p>
          Recipes are private by default. Add this recipe to any of your
          cookbooks.
        </p>
        <form onSubmit={submit}>
          <label htmlFor="recipe-image">
            Recipe photo (JPEG, PNG, or WebP; max 5 MiB)
          </label>
          <input
            id="recipe-image"
            type="file"
            accept="image/jpeg,image/png,image/webp"
            onChange={(event) => setImageFile(event.target.files?.[0] || null)}
          />
          {recipe?.image && (
            <img
              className="recipe-dialog__image"
              src={recipe.image}
              alt="Current recipe"
            />
          )}
          <label htmlFor="recipe-title">Title</label>
          <input
            id="recipe-title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            maxLength={200}
            required
          />
          <label htmlFor="recipe-description">Description</label>
          <textarea
            id="recipe-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            maxLength={5000}
          />
          <label htmlFor="recipe-servings">Servings</label>
          <input
            id="recipe-servings"
            type="number"
            min="0"
            max="1000"
            value={servings}
            onChange={(event) => setServings(event.target.value)}
          />
          <label htmlFor="recipe-prep-time">Prep time (minutes)</label>
          <input
            id="recipe-prep-time"
            type="number"
            min="0"
            max="1440"
            value={prepTime}
            onChange={(event) => setPrepTime(event.target.value)}
          />
          <label htmlFor="recipe-cook-time">Cook time (minutes)</label>
          <input
            id="recipe-cook-time"
            type="number"
            min="0"
            max="1440"
            value={cookTime}
            onChange={(event) => setCookTime(event.target.value)}
          />
          <label htmlFor="recipe-tags">Tags (comma separated)</label>
          <input
            id="recipe-tags"
            value={tags}
            onChange={(event) => setTags(event.target.value)}
            placeholder="weeknight, vegetarian"
          />
          {!recipe || recipe.owner_id === user?.id ? (
            <>
              <label htmlFor="recipe-visibility">Visibility</label>
              <select
                id="recipe-visibility"
                value={visibility}
                onChange={(event) => {
                  const nextVisibility = event.target.value as Recipe["visibility"];
                  setVisibility(nextVisibility);
                  setPublicAccessConfirmed(false);
                  setUnpublishedPublicAcknowledged(false);
                  if (nextVisibility !== "public")
                    setInitialPublicationState("unpublished");
                }}
              >
                <option value="private">Private</option>
                <option value="unlisted">Unlisted</option>
                <option value="public">Public</option>
              </select>
            </>
          ) : (
            <p className="dialog-note">
              Only the recipe owner can change visibility.
            </p>
          )}
          {!recipe && (
            <fieldset className="form-choice-group">
              {creatorSupport === "checking" && <p role="status">Checking creator-credit support…</p>}
              {creatorSupport === "error" && <div role="alert"><p>Could not check creator-credit support. No recipe has been sent.</p><button type="button" onClick={() => void retryCreatorSupport()}>Retry support check</button></div>}
              {creatorSupport === "unsupported" && <>
                <legend>Creator credit unavailable</legend>
                <p>This server does not support typed creator credit. Older servers may use your account display name as an unverified recipe byline; that byline follows this recipe’s visibility.</p>
                <label><input type="checkbox" checked={legacyCreationConfirmed} onChange={(event) => setLegacyCreationConfirmed(event.target.checked)} /> I understand the legacy byline disclosure and want to continue</label>
              </>}
              {creatorSupport === "ready" && <CreatorAttributionFields
                resourceKind="recipe"
                choice={creatorChoice}
                name={creatorName}
                onChoiceChange={(choice) => { setCreatorChoice(choice); setError(""); }}
                onNameChange={setCreatorName}
              />}
            </fieldset>
          )}
          {!recipe && (
            <fieldset className="form-choice-group">
              <legend>Initial publication</legend>
              <label htmlFor="initial-publication">Initial publication status</label>
              <select
                id="initial-publication"
                value={initialPublicationState}
                disabled={visibility !== "public"}
                onChange={(event) =>
                  {
                    setInitialPublicationState(
                      event.target.value as InitialPublicationInput["publication_state"],
                    );
                    setUnpublishedPublicAcknowledged(false);
                  }
                }
              >
                <option value="unpublished">Save without publishing</option>
                <option value="published">Publish for public browsing</option>
              </select>
              <p className="dialog-note">
                Publication is separate from visibility. Follower distribution and index submission remain off.
              </p>
            </fieldset>
          )}
          {visibility === "public" && (!recipe || recipe.visibility !== "public") && (
            <div className="form-choice-group">
              <label>
                <input
                  type="checkbox"
                  checked={publicAccessConfirmed}
                  onChange={(event) => setPublicAccessConfirmed(event.target.checked)}
                />
                Anyone can read this recipe on a public instance.
              </label>
              {recipe && (
                <p className="dialog-note">
                  If follower distribution is enabled, making this recipe public can resume delivery.
                </p>
              )}
              {!recipe && initialPublicationState === "unpublished" && (
                <label>
                  <input
                    type="checkbox"
                    checked={unpublishedPublicAcknowledged}
                    onChange={(event) => setUnpublishedPublicAcknowledged(event.target.checked)}
                  />
                  This recipe will not appear in anonymous browsing until I publish it.
                </label>
              )}
            </div>
          )}
          {canManageCookbookSharing && (
            <>
              <label htmlFor="recipe-cookbooks">Add to cookbooks</label>
              <select
                id="recipe-cookbooks"
                multiple
                value={cookbookIDs}
                onChange={(event) =>
                  setCookbookIDs(
                    Array.from(
                      event.target.selectedOptions,
                      (option) => option.value,
                    ),
                  )
                }
              >
                {cookbooks
                  .filter((book) => book.owner_id === user?.id)
                  .map((book) => (
                    <option key={book.id} value={book.id}>
                      {book.title}
                    </option>
                  ))}
              </select>
            </>
          )}
          {legacySharingSafetyBlocked && (
            <p className="dialog-note" role="status">
              This server does not confirm safe cookbook sharing permissions.
              Update it before editing this recipe so existing cookbook links
              stay intact.
            </p>
          )}
          <label htmlFor="recipe-ingredients">
            Ingredients (name | quantity | notes per line)
          </label>
          <textarea
            id="recipe-ingredients"
            value={ingredients}
            onChange={(event) => setIngredients(event.target.value)}
            required
          />
          <FeatureIsland name="Recipe steps" loading="recipe step editor">
            <StepListEditor steps={steps} onChange={setSteps} />
          </FeatureIsland>
          <button
            className="button button--green"
            type="submit"
            disabled={saving || legacySharingSafetyBlocked}
          >
            {saving
              ? "Saving…"
              : recipe
                ? "Save changes"
                : "Save recipe"}
          </button>
        </form>
        {publisherProfileRequired ? (
          <p className="form-message" role="alert">
            <PublisherProfileSetupGuidance />
          </p>
        ) : error && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
      </section>
    </div>
  );
}

export default RecipeForm;
