import { useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, useAppSession } from "./AppSession.tsx";
import FeatureIsland from "../components/FeatureIsland.tsx";
import type { RecipeBookmarkView } from "../apiModels";
import RecipeDialog from "../features/recipes/RecipeDialog.tsx";
import RecipeCollection from "../features/recipes/RecipeCollection.tsx";
import RecipeCardContents from "../features/recipes/RecipeCardContents.tsx";
import RecipeCardLink from "../features/recipes/RecipeCardLink.tsx";
import useRecipeFeed from "../features/recipes/useRecipeFeed.ts";
import type { RemoteRecipe } from "../features/recipes/recipeUiTypes.ts";
import BookmarkAction from "../features/library/BookmarkAction.tsx";
import useRecipeBookmarks from "../features/library/useRecipeBookmarks.ts";
import { SessionCheckError } from "./AppSession.tsx";

function SignInRequired() {
  return (
    <section className="cookbook-dashboard" aria-labelledby="library-signin-title">
      <h2 id="library-signin-title">Sign in to see your Library</h2>
      <p>Saved recipes and your accessible recipe list are tied to your account.</p>
      <a className="button button--green" href="/?signin=1">
        Sign in
      </a>
    </section>
  );
}

function LibraryPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname="/library"
        sessionStatus={state.status}
        onRetrySession={() => void refreshSession()}
      />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome">
          <div>
            <span className="eyebrow">Your kitchen</span>
            <h1>Library</h1>
            <p>Recipes you can access and references you have saved.</p>
          </div>
        </section>
        {state.status === "loading" ? (
          <p className="cookbook-dashboard" role="status">
            Checking your session…
          </p>
        ) : state.status === "error" ? (
          <SessionCheckError
            message={state.message}
            onRetry={() => void refreshSession()}
          />
        ) : state.status === "authenticated" ? (
          <LibraryContent userID={state.currentUser.id} />
        ) : (
          <SignInRequired />
        )}
      </main>
    </div>
  );
}

function LibraryContent({ userID }: { userID: string }) {
  const feed = useRecipeFeed({ signedIn: true, query: "" });
  const bookmarks = useRecipeBookmarks(userID);
  const [selectedRemote, setSelectedRemote] = useState<{
    recipe: RemoteRecipe;
    bookmark: RecipeBookmarkView;
  } | null>(null);

  return (
    <>
      <RecipeCollection
        signedIn
        query=""
        feed={feed}
        heading="Recipes you can access"
        emptyMessage="No recipes are currently available to you."
        onOpenRecipe={() => {}}
        renderRecipe={(recipe) => (
          <div key={recipe.id}>
            <RecipeCardLink recipe={recipe} currentUserID={userID} />
            <BookmarkAction
              currentUserID={userID}
              target={{ local_recipe_id: recipe.id }}
              bookmark={bookmarks.items.find(
                (item) => item.target?.local_recipe_id === recipe.id,
              )}
              onChange={bookmarks.update}
            />
          </div>
        )}
      />
      <section className="cookbook-dashboard" aria-labelledby="saved-title">
        <div className="section-heading">
          <div>
            <span className="eyebrow">Personal references</span>
            <h2 id="saved-title">Saved</h2>
          </div>
        </div>
        {bookmarks.loading ? (
          <p role="status">Loading Saved recipes…</p>
        ) : bookmarks.error ? (
          <p className="form-message" role="alert">
            {bookmarks.error}
          </p>
        ) : bookmarks.items.length ? (
          <div className="cookbook-grid">
            {bookmarks.items.map((bookmark) => (
              <SavedBookmark
                key={bookmark.id}
                bookmark={bookmark}
                userID={userID}
                onChange={bookmarks.update}
                onOpenRemote={(recipe) => setSelectedRemote({ recipe, bookmark })}
              />
            ))}
          </div>
        ) : (
          <p className="cookbook-empty">
            Recipes you save will appear here. Saving keeps a reference; it does
            not copy or share the recipe.
          </p>
        )}
        {bookmarks.loadMoreError && (
          <p className="form-message" role="alert">
            {bookmarks.loadMoreError}
          </p>
        )}
        {bookmarks.hasMore && (
          <button
            className="button button--green"
            type="button"
            onClick={() => void bookmarks.loadMore()}
            disabled={bookmarks.loadingMore}
          >
            {bookmarks.loadingMore ? "Loading…" : "Load more Saved recipes"}
          </button>
        )}
      </section>
      {selectedRemote && (
        <FeatureIsland name="Saved remote recipe" loading="recipe details">
          <RecipeDialog
            recipe={selectedRemote.recipe}
            currentUserID={userID}
            onClose={() => setSelectedRemote(null)}
            onEdit={() => {}}
            onRestored={() => {}}
            onDelete={async () => {}}
            onTransfer={() => {}}
            canEdit={false}
            canDelete={false}
            canFork={false}
            bookmark={selectedRemote.bookmark}
            onBookmarkChange={bookmarks.update}
          />
        </FeatureIsland>
      )}
    </>
  );
}

function SavedBookmark({
  bookmark,
  userID,
  onChange,
  onOpenRemote,
}: {
  bookmark: RecipeBookmarkView;
  userID: string;
  onChange: (
    bookmark: RecipeBookmarkView | null,
    created?: boolean,
    removed?: RecipeBookmarkView,
  ) => void;
  onOpenRemote: (recipe: RemoteRecipe) => void;
}) {
  const action = (
    <BookmarkAction
      currentUserID={userID}
      target={bookmark.target ?? null}
      bookmark={bookmark}
      onChange={onChange}
    />
  );

  if (bookmark.availability !== "available")
    return (
      <article className="cookbook-card">
        <h3>Unavailable</h3>
        <p>
          Saved <time dateTime={bookmark.created_at}>{formatSavedDate(bookmark.created_at)}</time>
        </p>
        {action}
      </article>
    );

  if (bookmark.local_recipe)
    return (
      <article>
        <RecipeCardLink recipe={bookmark.local_recipe} currentUserID={userID} />
        {action}
      </article>
    );

  const remote = bookmark.remote_recipe;
  if (remote)
    return (
      <article>
        <button
          className="recipe-card"
          type="button"
          onClick={() =>
            onOpenRemote({
              ...remote.recipe,
              id: remote.object_uri,
              creator_name: remote.creator_name,
              source_origin: remote.source_origin,
              sequence: remote.sequence,
            })
          }
          aria-label={`Open saved recipe ${remote.recipe.title} from ${remote.source_origin}`}
        >
          <RecipeCardContents
            title={remote.recipe.title}
            creatorName={remote.creator_name}
            stepCount={remote.recipe.steps.length}
            relationshipLabel={`From ${remote.source_origin}`}
          />
        </button>
        {action}
      </article>
    );

  return (
    <article className="cookbook-card">
      <h3>Unavailable</h3>
      <p>
        Saved <time dateTime={bookmark.created_at}>{formatSavedDate(bookmark.created_at)}</time>
      </p>
      {action}
    </article>
  );
}

function formatSavedDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "on an unknown date" : date.toLocaleDateString();
}

export default function LibraryRoot() {
  return (
    <AppSessionProvider>
      <LibraryPage />
    </AppSessionProvider>
  );
}
