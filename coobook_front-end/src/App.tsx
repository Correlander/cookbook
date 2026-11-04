import { lazy, useEffect, useRef, useState } from "react";
import RecipeCollection from "./features/recipes/RecipeCollection.tsx";
import useRecipeFeed from "./features/recipes/useRecipeFeed.ts";
import FeatureIsland from "./components/FeatureIsland.tsx";
import CookbookCollection from "./features/cookbooks/CookbookCollection.tsx";
import PublicCourses from "./PublicCourses.tsx";
import { useAppSession } from "./app/AppSession.tsx";
import AppNavigation from "./app/AppNavigation.tsx";
import RecipeDialog from "./features/recipes/RecipeDialog.tsx";
import RecipeForm from "./features/recipes/RecipeForm.tsx";
import Icon from "./features/recipes/RecipeIcon.tsx";
import RecipeCardContents from "./features/recipes/RecipeCardContents.tsx";
import type { RemoteRecipe } from "./features/recipes/recipeUiTypes.ts";
import type {
  PublicRecipeDetail,
  Recipe,
  RemoteRecipeListItem,
  TrashedRecipe,
} from "./apiModels";
import { errorMessage } from "./apiModels";
import { SessionCheckError } from "./app/AppSession.tsx";
import AuthEntry from "./features/auth/AuthEntry.tsx";

const Courses = lazy(() => import("./Courses.tsx"));
type RecipeCardItem = {
  id: string;
  title: string;
  creator_name: string;
  creator_attributions?: Recipe["creator_attributions"];
  owner_id?: string;
  source_recipe_id?: string;
  source_origin?: string;
  image?: string;
  steps: Array<{ text: string }>;
};

function RecipeCard<T extends RecipeCardItem>({
  recipe,
  onOpen,
  currentUserID,
  relationshipLabel,
}: {
  recipe: T;
  onOpen: (recipe: T) => void;
  currentUserID?: string | null;
  relationshipLabel?: string;
}) {
  const relationship =
    relationshipLabel ??
    (recipe.source_origin
      ? `From ${recipe.source_origin}`
      : currentUserID && recipe.owner_id === currentUserID
        ? recipe.source_recipe_id
          ? "Owned by you · Your copy"
          : "Owned by you"
        : currentUserID
          ? "Available to you"
          : "Public on this instance");
  return (
    <button
      className="recipe-card"
      onClick={() => onOpen(recipe)}
      aria-label={`Open ${recipe.title}`}
    >
      <RecipeCardContents
        title={recipe.title}
        creatorName={recipe.creator_name}
        creatorAttributions={
          currentUserID ? recipe.creator_attributions : undefined
        }
        image={recipe.image}
        stepCount={recipe.steps?.length ?? 0}
        relationshipLabel={relationship}
      />
    </button>
  );
}
function HomeApp({ pathname }: { pathname: string }) {
  const [query, setQuery] = useState(""),
    [selected, setSelected] = useState<Recipe | PublicRecipeDetail | null>(
      null,
    ),
    [selectedRemote, setSelectedRemote] = useState<RemoteRecipe | null>(null),
    [remoteRecipes, setRemoteRecipes] = useState<RemoteRecipe[]>([]),
    [remoteVisible, setRemoteVisible] = useState(false),
    [authOpen, setAuthOpen] = useState(false),
    [editRecipe, setEditRecipe] = useState<Recipe | null>(null),
    [editActorID, setEditActorID] = useState<string | null>(null),
    [createOpen, setCreateOpen] = useState(false);
  const {
    state: sessionState,
    currentUser,
    refreshSession,
    clearSession,
  } = useAppSession();
  const currentUserIDRef = useRef<string | null>(currentUser?.id ?? null);
  currentUserIDRef.current = currentUser?.id ?? null;
  const signedIn = Boolean(currentUser);
  const formActorID = currentUser?.id ?? null;
  const activeEditRecipe = editActorID === formActorID ? editRecipe : null;
  const feed = useRecipeFeed({ signedIn, query });
  useEffect(() => {
    if (editRecipe && editActorID !== formActorID) {
      setEditRecipe(null);
      setEditActorID(null);
    }
  }, [editActorID, editRecipe, formActorID]);
  function handleSignedOut() {
    clearSession();
    setCreateOpen(false);
    setEditRecipe(null);
    setEditActorID(null);
    setSelected(null);
    setSelectedRemote(null);
    setAuthOpen(false);
  }
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (pathname === "/" || pathname === "/auth/confirm")
      setAuthOpen(
        Boolean(
          params.get("invite") ||
          params.get("signin") ||
          (pathname === "/auth/confirm" && params.get("token")),
        ),
      );
  }, [pathname]);
  useEffect(() => {
    let active = true;
    fetch("/api/public/remote-recipes?limit=24&offset=0")
      .then(async (response) => {
        if (!response.ok) return null;
        const items = (await response.json()) as RemoteRecipeListItem[];
        if (active) {
          setRemoteVisible(true);
          setRemoteRecipes(
            items.map((item) => ({
              ...item.recipe,
              id: item.object_id,
              creator_name: item.creator_name,
              source_origin: item.source_origin,
              sequence: item.sequence,
            })),
          );
        }
      })
      .catch(() => {
        if (active) setRemoteRecipes([]);
      });
    return () => {
      active = false;
    };
  }, []);
  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        sessionStatus={sessionState.status}
        onRetrySession={() => void refreshSession()}
        pathname={pathname}
        query={query}
        onQueryChange={setQuery}
        actionLabel={signedIn ? "Add a recipe" : "Sign in"}
        onAction={() => (signedIn ? setCreateOpen(true) : setAuthOpen(true))}
      />
      <main id="home" tabIndex={-1}>
        {sessionState.status === "error" && (
          <SessionCheckError
            message={sessionState.message}
            onRetry={() => void refreshSession()}
          />
        )}
        {signedIn ? (
          <section className="dashboard-welcome">
            <div>
              <span className="eyebrow">Your kitchen</span>
              <h1>Welcome back, {currentUser?.display_name || "cook"}.</h1>
              <p>Your cookbooks and recipes are ready when you are.</p>
            </div>
            <button
              className="button button--green"
              onClick={() => setCreateOpen(true)}
            >
              Add a recipe <Icon name="arrow" size={16} />
            </button>
          </section>
        ) : (
          <>
            <section className="hero">
              <div className="hero__copy">
                <span className="eyebrow">
                  <Icon name="leaf" size={15} /> Good food, gathered
                </span>
                <h1>
                  A little more
                  <br />
                  <em>home</em> in every meal.
                </h1>
                <p>
                  Keep the recipes you love, organize them into cookbooks, and
                  pass the good ones along.
                </p>
                <a className="button button--light" href="#recipes">
                  Explore recipes <Icon name="arrow" size={16} />
                </a>
              </div>
              <div
                className="hero__image"
                role="img"
                aria-label="A table filled with a fresh seasonal meal"
              >
                <div className="hero__caption">
                  Made to be shared <span>✳</span>
                </div>
              </div>
              <div className="hero__stamp">
                <span>THE JOY OF</span>
                <strong>
                  home
                  <br />
                  cooking
                </strong>
                <Icon name="leaf" size={22} />
              </div>
            </section>
            <section className="intro-row" id="about">
              <div>
                <span className="eyebrow">From the kitchen</span>
                <h2>
                  Recipes worth
                  <br />
                  coming back to.
                </h2>
              </div>
              <p>
                Start with a favorite, find something new, and make each recipe
                your own. Your kitchen notebook is waiting.
              </p>
            </section>
          </>
        )}
        {signedIn && (
          <FeatureIsland name="Courses" loading="courses">
            <Courses onOpenRecipe={setSelected} />
          </FeatureIsland>
        )}
        <CookbookCollection
          mode={
            sessionState.status === "authenticated" ? "accessible" : "public"
          }
          surface="home"
          user={currentUser}
          enabled={
            sessionState.status === "authenticated" ||
            sessionState.status === "anonymous"
          }
          pending={sessionState.status === "loading"}
        >
          {sessionState.status === "authenticated" && (
            <RecipeTrash onRestored={(recipe) => feed.restoreRecipe(recipe)} />
          )}
        </CookbookCollection>
        <FeatureIsland name="Public courses" loading="public courses">
          <PublicCourses />
        </FeatureIsland>
        {remoteVisible && (
          <section
            className="recipe-section"
            aria-labelledby="remote-recipes-title"
          >
            <div className="section-heading">
              <div>
                <span className="eyebrow">Trusted kitchens</span>
                <h2 id="remote-recipes-title">Recipes from other Cookbooks</h2>
              </div>
            </div>
            {remoteRecipes.length ? (
              <div className="recipe-grid">
                {remoteRecipes.map((recipe) => (
                  <RecipeCard
                    key={recipe.id}
                    recipe={recipe}
                    onOpen={setSelectedRemote}
                    relationshipLabel={`From ${recipe.source_origin}`}
                  />
                ))}
              </div>
            ) : (
              <p className="empty-state">No recipes from trusted peers yet.</p>
            )}
          </section>
        )}
        <RecipeCollection
          signedIn={signedIn}
          query={query}
          feed={feed}
          onOpenRecipe={setSelected}
          onCreateRecipe={() => setCreateOpen(true)}
          addIcon={<Icon name="arrow" size={15} />}
          searchIcon={<Icon name="search" size={25} />}
          renderRecipe={(recipe, onOpen) => (
            <RecipeCard
              key={recipe.id}
              recipe={recipe}
              onOpen={onOpen}
              currentUserID={currentUser?.id}
            />
          )}
        />
        {!signedIn && (
          <section className="notebook-banner">
            <div className="notebook-banner__icon">
              <Icon name="book" size={27} />
            </div>
            <div>
              <span className="eyebrow">A place for your favorites</span>
              <h2>
                Your own little corner
                <br />
                of the kitchen.
              </h2>
            </div>
            <p>
              Create personal cookbooks, gather family favorites, and keep every
              recipe close at hand.
            </p>
            <button
              className="button button--green"
              onClick={() => setAuthOpen(true)}
            >
              Get started <Icon name="arrow" size={16} />
            </button>
          </section>
        )}
      </main>
      <RecipeDialog
        recipe={selectedRemote || selected}
        currentUserID={currentUser?.id ?? null}
        onTransfer={(id) => {
          feed.removeRecipe(id);
          setSelected(null);
        }}
        canFork={Boolean(signedIn && !selectedRemote)}
        canEdit={Boolean(
          signedIn &&
          !selectedRemote &&
          selected &&
          "can_edit" in selected &&
          selected.can_edit,
        )}
        canDelete={Boolean(
          signedIn &&
          !selectedRemote &&
          selected &&
          "can_delete" in selected &&
          selected.can_delete,
        )}
        onDelete={async (recipe) => {
          const response = await fetch(`/api/recipes/${recipe.id}`, {
            method: "DELETE",
          });
          if (!response.ok)
            throw new Error("Could not move this recipe to trash.");
          feed.removeRecipe(recipe.id);
          setSelected(null);
        }}
        onRestored={(recipe) => {
          feed.replaceRecipe(recipe);
          setSelected(recipe);
        }}
        onClose={() => {
          setSelected(null);
          setSelectedRemote(null);
        }}
        onEdit={(recipe) => {
          setSelected(null);
          setEditRecipe(recipe);
          setEditActorID(formActorID);
        }}
      />
      {(createOpen || activeEditRecipe) && (
        <RecipeForm
          key={
            activeEditRecipe
              ? `edit:${formActorID ?? "anonymous"}:${activeEditRecipe.id}`
              : `create:${formActorID ?? "anonymous"}`
          }
          user={currentUser}
          recipe={activeEditRecipe}
          onClose={() => {
            setCreateOpen(false);
            setEditRecipe(null);
            setEditActorID(null);
          }}
          onSignedOut={handleSignedOut}
          onCreated={(recipe) => {
            if (currentUserIDRef.current !== formActorID) return;
            feed.addRecipe(recipe);
            setCreateOpen(false);
          }}
          onUpdated={(recipe) => {
            if (currentUserIDRef.current !== formActorID) return;
            feed.replaceRecipe(recipe);
            setEditRecipe(null);
            setEditActorID(null);
          }}
        />
      )}
      <AuthEntry
        mode="home"
        open={authOpen}
        pathname={pathname}
        onClose={() => setAuthOpen(false)}
      />
    </div>
  );
}

export default function App({ pathname = "/" }: { pathname?: string }) {
  return <HomeApp pathname={pathname} />;
}

function RecipeTrash({ onRestored }: { onRestored: (recipe: Recipe) => void }) {
  const [open, setOpen] = useState(false),
    [items, setItems] = useState<TrashedRecipe[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [loadingMore, setLoadingMore] = useState(false),
    [restoring, setRestoring] = useState(""),
    [offset, setOffset] = useState(0),
    [hasMore, setHasMore] = useState(false);
  useEffect(() => {
    if (!open) return undefined;
    let active = true;
    setLoading(true);
    setError("");
    fetch("/api/recipes/trash?limit=24&offset=0")
      .then(async (response) => {
        if (!response.ok) throw new Error("Could not load recipe trash.");
        const data = (await response.json()) as TrashedRecipe[];
        if (active) {
          setItems(data);
          setOffset(data.length);
          setHasMore(data.length === 24);
        }
      })
      .catch((err: unknown) => {
        if (active) setError(errorMessage(err, "Could not load recipe trash."));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [open]);
  async function loadMore() {
    if (loadingMore) return;
    setLoadingMore(true);
    setError("");
    try {
      const response = await fetch(
        `/api/recipes/trash?limit=24&offset=${offset}`,
      );
      if (!response.ok) throw new Error("Could not load more trashed recipes.");
      const data = (await response.json()) as TrashedRecipe[];
      setItems((current) => [...current, ...data]);
      setOffset((value) => value + data.length);
      setHasMore(data.length === 24);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not load more trashed recipes."));
    } finally {
      setLoadingMore(false);
    }
  }
  async function restore(recipe: TrashedRecipe) {
    setError("");
    setRestoring(recipe.id);
    try {
      const response = await fetch(`/api/recipes/${recipe.id}/restore`, {
        method: "POST",
      });
      if (!response.ok) throw new Error("Could not restore this recipe.");
      const restored = (await response.json()) as Recipe;
      setItems((current) => current.filter((item) => item.id !== recipe.id));
      setOffset((value) => Math.max(0, value - 1));
      onRestored(restored);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not restore this recipe."));
    } finally {
      setRestoring("");
    }
  }
  return (
    <div className="recipe-trash">
      <button
        className="text-link"
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        {open ? "Hide recipe trash" : "View recipe trash"}
      </button>
      {open && (
        <div className="passkey-list">
          <h3>Trashed recipes</h3>
          {loading ? (
            <p>Loading trash…</p>
          ) : items.length ? (
            items.map((item) => (
              <div className="passkey-row" key={item.id}>
                <span>
                  {item.title}
                  <small>
                    Moved {new Date(item.deleted_at).toLocaleDateString()}
                  </small>
                </span>
                <button
                  className="text-link"
                  type="button"
                  disabled={Boolean(restoring)}
                  onClick={() => restore(item)}
                >
                  {restoring === item.id ? "Restoring…" : "Restore"}
                </button>
              </div>
            ))
          ) : (
            !error && <p>Your trash is empty.</p>
          )}
          {hasMore && (
            <button
              className="text-link"
              type="button"
              disabled={loadingMore}
              onClick={loadMore}
            >
              {loadingMore ? "Loading…" : "Load more trash"}
            </button>
          )}
          {error && (
            <p className="form-message" role="alert">
              {error}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
