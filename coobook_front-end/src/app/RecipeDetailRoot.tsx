import { useEffect, useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, useAppSession } from "./AppSession.tsx";
import RecipeDialog from "../features/recipes/RecipeDialog.tsx";
import RecipeForm from "../features/recipes/RecipeForm.tsx";
import { fetchRecipeDetail } from "../features/recipes/recipeDetailApi.ts";
import type { Recipe } from "../apiModels";
import type { RecipeDetailRead } from "../features/recipes/recipeDetailApi.ts";
import useRecipeBookmarks from "../features/library/useRecipeBookmarks.ts";
import { SessionCheckError } from "./AppSession.tsx";

function RecipeDetailPage() {
  const { currentUser } = useAppSession();
  return <RecipeDetailSession key={currentUser?.id ?? "anonymous"} />;
}

function RecipeDetailSession() {
  const { state, currentUser, refreshSession, clearSession } = useAppSession();
  const [recipeID, setRecipeID] = useState("");
  const [recipe, setRecipe] = useState<RecipeDetailRead | null>(null);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [notice, setNotice] = useState("");
  const bookmarks = useRecipeBookmarks(
    state.status === "authenticated" ? state.currentUser.id : null,
    state.status === "authenticated" && Boolean(recipe),
  );

  useEffect(() => {
    setRecipeID(new URLSearchParams(window.location.search).get("id") ?? "");
  }, []);

  useEffect(() => {
    if (state.status === "loading" || state.status === "error") {
      setLoading(state.status === "loading");
      setUnavailable(false);
      setRecipe(null);
      return;
    }
    if (!recipeID) {
      setLoading(false);
      setUnavailable(true);
      setRecipe(null);
      return;
    }
    let active = true;
    setLoading(true);
    setUnavailable(false);
    setRecipe(null);
    void fetchRecipeDetail(recipeID, state.status === "authenticated")
      .then((result) => {
        if (active) setRecipe(result);
      })
      .catch(() => {
        if (active) setUnavailable(true);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [currentUser, recipeID, state.status]);

  const localRecipe = recipe && "can_edit" in recipe ? recipe : null;
  const isPublicRead = Boolean(recipe && !localRecipe);
  const bookmark = localRecipe
    ? bookmarks.items.find(
        (item) => item.target?.local_recipe_id === localRecipe.id,
      )
    : undefined;

  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname="/recipe"
        sessionStatus={state.status}
        onRetrySession={() => void refreshSession()}
      />
      <main id="home" tabIndex={-1}>
        <section className="cookbook-dashboard" aria-label="Recipe detail navigation">
          <a className="text-link" href="/library">Back to Library</a>
        </section>
        {state.status === "error" ? (
          <SessionCheckError
            message={state.message}
            onRetry={() => void refreshSession()}
          />
        ) : state.status === "loading" ? (
          <p className="cookbook-dashboard" role="status">
            Checking your session…
          </p>
        ) : loading ? (
          <p className="cookbook-dashboard" role="status">Loading recipe…</p>
        ) : unavailable || !recipe ? (
          <section className="cookbook-dashboard" aria-labelledby="recipe-unavailable-title">
            <h1 id="recipe-unavailable-title">Recipe unavailable</h1>
            <p>This recipe could not be found or is not available to you.</p>
          </section>
        ) : editOpen && localRecipe ? (
          <RecipeForm
            user={currentUser}
            recipe={localRecipe}
            onClose={() => setEditOpen(false)}
            onSignedOut={clearSession}
            onCreated={() => {}}
            onUpdated={(updated) => {
              setRecipe(updated);
              setEditOpen(false);
            }}
          />
        ) : (
          <>
            <h1 className="sr-only">Recipe: {recipe.title}</h1>
            {notice && <p className="cookbook-dashboard" role="status">{notice}</p>}
            <RecipeDialog
              recipe={recipe}
              currentUserID={currentUser?.id ?? null}
              presentation="page"
              publicRead={isPublicRead}
              onClose={() => window.location.assign("/library")}
              onEdit={() => setEditOpen(true)}
              onRestored={setRecipe}
              onDelete={async (item) => {
                const response = await fetch(`/api/recipes/${item.id}`, {
                  method: "DELETE",
                });
                if (!response.ok)
                  throw new Error("Could not move this recipe to trash.");
                window.location.assign("/library");
              }}
              onTransfer={() => window.location.assign("/library")}
              canEdit={Boolean(localRecipe?.can_edit)}
              canDelete={Boolean(localRecipe?.can_delete)}
              canFork={Boolean(currentUser && localRecipe)}
              bookmark={bookmark}
              onBookmarkChange={bookmarks.update}
              onCopied={(copy: Recipe) => {
                setNotice("A new private copy was created.");
                window.location.assign(`/recipe?id=${encodeURIComponent(copy.id)}`);
              }}
            />
          </>
        )}
      </main>
    </div>
  );
}

export default function RecipeDetailRoot() {
  return (
    <AppSessionProvider>
      <RecipeDetailPage />
    </AppSessionProvider>
  );
}
