import { useEffect, useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, useAppSession } from "./AppSession.tsx";
import RecipeDialog, { type FrozenRemoteRecipe } from "../features/recipes/RecipeDialog.tsx";
import RecipeForm from "../features/recipes/RecipeForm.tsx";
import { fetchRecipeDetail } from "../features/recipes/recipeDetailApi.ts";
import type { Recipe, RecipeBookmarkView } from "../apiModels";
import type { RecipeDetailRead } from "../features/recipes/recipeDetailApi.ts";
import type { RemoteRecipe } from "../features/recipes/recipeUiTypes.ts";
import { fetchCachedRemoteRecipe, fetchPrivateSnapshot, PrivateSnapshotUnavailableError } from "../features/library/libraryApi.ts";
import { SessionCheckError } from "./AppSession.tsx";

type DetailTarget =
  | { kind: "local"; id: string }
  | { kind: "remote"; objectURI: string }
  | { kind: "snapshot"; bookmarkID: string }
  | null;

function RecipeDetailPage() {
  const { currentUser } = useAppSession();
  return <RecipeDetailSession key={currentUser?.id ?? "anonymous"} />;
}

function RecipeDetailSession() {
  const { state, currentUser, refreshSession, clearSession } = useAppSession();
  const [target, setTarget] = useState<DetailTarget>(null);
  const [recipe, setRecipe] = useState<RecipeDetailRead | RemoteRecipe | FrozenRemoteRecipe | null>(null);
  const [bookmark, setBookmark] = useState<RecipeBookmarkView | undefined>();
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);
  const [blockedState, setBlockedState] = useState<"restricted" | "removed" | null>(null);
  const [editOpen, setEditOpen] = useState(false);
  const [notice, setNotice] = useState("");

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const localIDs = params.getAll("id");
    const remoteURIs = params.getAll("remote_object_uri");
    const bookmarkIDs = params.getAll("bookmark_id");
    if (params.size !== 1 || localIDs.length + remoteURIs.length + bookmarkIDs.length !== 1) {
      setTarget(null);
    } else if (localIDs.length === 1 && localIDs[0]) {
      setTarget({ kind: "local", id: localIDs[0] });
    } else if (remoteURIs.length === 1 && remoteURIs[0]) {
      setTarget({ kind: "remote", objectURI: remoteURIs[0] });
    } else if (bookmarkIDs.length === 1 && bookmarkIDs[0]) {
      setTarget({ kind: "snapshot", bookmarkID: bookmarkIDs[0] });
    } else {
      setTarget(null);
    }
  }, []);

  useEffect(() => {
    if (state.status === "loading" || state.status === "error") {
      setLoading(state.status === "loading");
      setUnavailable(false);
      setBlockedState(null);
      setRecipe(null);
      setBookmark(undefined);
      return;
    }
    if (!target || (target.kind !== "local" && state.status !== "authenticated")) {
      setLoading(false);
      setUnavailable(true);
      setBlockedState(null);
      setRecipe(null);
      setBookmark(undefined);
      return;
    }
    let active = true;
    setLoading(true);
    setUnavailable(false);
    setBlockedState(null);
    setRecipe(null);
    setBookmark(undefined);
    const request: Promise<{ recipe: RecipeDetailRead | RemoteRecipe | FrozenRemoteRecipe; bookmark?: RecipeBookmarkView }> = target.kind === "local"
      ? fetchRecipeDetail(target.id, state.status === "authenticated").then((item) => ({ recipe: item }))
      : target.kind === "snapshot"
      ? fetchPrivateSnapshot(target.bookmarkID).then((snapshot) => ({
          recipe: {
            ...snapshot.recipe,
            id: snapshot.object_uri,
            creator_name: snapshot.creator_name,
            source_origin: snapshot.source_origin,
            ...(snapshot.sequence === undefined ? {} : { sequence: snapshot.sequence }),
          } as FrozenRemoteRecipe,
          bookmark: {
            id: target.bookmarkID,
            target_kind: "remote" as const,
            target: { remote_object_uri: snapshot.object_uri },
            availability: "available" as const,
            created_at: snapshot.received_at ?? snapshot.updated_at,
            snapshot_recipe: snapshot,
            save_state: "snapshot" as const,
            ...(snapshot.version_id ? { version_id: snapshot.version_id } : {}),
            ...(snapshot.source_state ? { source_state: snapshot.source_state } : {}),
            ...(snapshot.provenance_state ? { provenance_state: snapshot.provenance_state } : {}),
            ...(snapshot.publisher_retention_claim ? { publisher_retention_claim: snapshot.publisher_retention_claim } : {}),
          },
        }))
      : fetchCachedRemoteRecipe(target.objectURI).then((remote) => ({ recipe: {
          ...remote.recipe,
          id: remote.object_uri,
          creator_name: remote.creator_name,
          source_origin: remote.source_origin,
          sequence: remote.sequence,
        } }));
    void request
      .then((result) => {
        if (active) {
          setRecipe(result.recipe);
          setBookmark(result.bookmark);
        }
      })
      .catch((caught: unknown) => {
        if (active) {
          setUnavailable(true);
          setBlockedState(caught instanceof PrivateSnapshotUnavailableError ? caught.state : null);
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [currentUser, state.status, target]);

  const localRecipe = recipe && "can_edit" in recipe ? recipe : null;
  const isRemote = Boolean(recipe && "source_origin" in recipe);
  const isPublicRead = Boolean(recipe && !localRecipe && !isRemote);

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
            <h1 id="recipe-unavailable-title">{blockedState ? "Saved copy blocked" : "Recipe unavailable"}</h1>
            <p>{blockedState === "restricted"
              ? "This private copy is restricted by this instance and cannot be read. Its source cache will not be used as a fallback."
              : blockedState === "removed"
              ? "This private copy was removed by this instance and cannot be read. Its source cache will not be used as a fallback."
              : "This recipe could not be found or is not available to you."}</p>
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
              onBookmarkChange={(nextBookmark) => {
                if (!nextBookmark && bookmark?.save_state === "snapshot") {
                  setBookmark(undefined);
                  setRecipe(null);
                  setUnavailable(true);
                  setBlockedState(null);
                  return;
                }
                setBookmark(nextBookmark ?? undefined);
                if (nextBookmark?.snapshot_recipe) {
                  const snapshot = nextBookmark.snapshot_recipe;
                  setRecipe({
                    ...snapshot.recipe,
                    id: snapshot.object_uri,
                    creator_name: snapshot.creator_name,
                    source_origin: snapshot.source_origin,
                    ...(snapshot.sequence === undefined ? {} : { sequence: snapshot.sequence }),
                  } as FrozenRemoteRecipe);
                }
              }}
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
