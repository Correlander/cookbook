import { lazy, useEffect, useRef, useState } from "react";
import RecipeCollection from "./features/recipes/RecipeCollection.tsx";
import useRecipeFeed from "./features/recipes/useRecipeFeed.ts";
import { signInWithPasskey } from "./passkeys.ts";
import FeatureIsland from "./components/FeatureIsland.tsx";
import PublicCookbooks from "./PublicCookbooks.tsx";
import PublicCourses from "./PublicCourses.tsx";
import useModalFocus from "./useModalFocus.ts";
import { useAppSession } from "./app/AppSession.tsx";
import AppNavigation from "./app/AppNavigation.tsx";
import RecipeDialog from "./features/recipes/RecipeDialog.tsx";
import RecipeForm from "./features/recipes/RecipeForm.tsx";
import Icon from "./features/recipes/RecipeIcon.tsx";
import type { RemoteRecipe } from "./features/recipes/recipeUiTypes.ts";
import type {
  AdminInvitation,
  AdminInvitationCreated,
  AuditLogEntry,
  Cookbook,
  CookbookInvitation,
  CookbookMember,
  Recipe,
  RemoteRecipeListItem,
  TrashedRecipe,
  UserSummary,
} from "./apiModels";
import { errorMessage } from "./apiModels";

const PasskeySetupPrompt = lazy(() => import("./PasskeySetupPrompt.tsx"));
const FederationAdmin = lazy(() => import("./FederationAdmin.tsx"));
const AdminUsers = lazy(() => import("./AdminUsers.tsx"));
const Courses = lazy(() => import("./Courses.tsx"));
const AccountSecurityPage = lazy(() =>
  import("./app/RoutePages.tsx").then((module) => ({
    default: module.AccountSecurityPage,
  })),
);
const AdminSettingsPage = lazy(() =>
  import("./app/RoutePages.tsx").then((module) => ({
    default: module.AdminSettingsPage,
  })),
);

type RecipeCardItem = {
  id: string;
  title: string;
  creator_name: string;
  image?: string;
  steps: Array<{ text: string }>;
};

function RecipeCard<T extends RecipeCardItem>({
  recipe,
  onOpen,
}: {
  recipe: T;
  onOpen: (recipe: T) => void;
}) {
  return (
    <button
      className="recipe-card"
      onClick={() => onOpen(recipe)}
      aria-label={`Open ${recipe.title}`}
    >
      {recipe.image ? (
        <img src={recipe.image} alt="" loading="lazy" />
      ) : (
        <div className="recipe-card__placeholder" aria-hidden="true">
          <Icon name="leaf" size={34} />
        </div>
      )}
      <span className="recipe-card__body">
        <span className="recipe-card__category">
          {recipe.creator_name || "Recipe"}
        </span>
        <strong>{recipe.title}</strong>
        <span className="recipe-card__meta">
          <Icon name="clock" size={15} />
          {recipe.steps?.length ? `${recipe.steps.length} steps` : "Recipe"}
        </span>
      </span>
    </button>
  );
}
function HomeApp({ pathname }: { pathname: string }) {
  const [query, setQuery] = useState(""),
    [selected, setSelected] = useState<Recipe | null>(null),
    [selectedRemote, setSelectedRemote] = useState<RemoteRecipe | null>(null),
    [remoteRecipes, setRemoteRecipes] = useState<RemoteRecipe[]>([]),
    [remoteVisible, setRemoteVisible] = useState(false),
    [emailOpen, setEmailOpen] = useState(false),
    [email, setEmail] = useState(""),
    [message, setMessage] = useState(""),
    [confirming, setConfirming] = useState(false),
    [confirmationToken, setConfirmationToken] = useState(""),
    [editRecipe, setEditRecipe] = useState<Recipe | null>(null),
    [createOpen, setCreateOpen] = useState(false),
    [inviteToken, setInviteToken] = useState(""),
    [passkeyPrompt, setPasskeyPrompt] = useState(false);
  const { currentUser, refreshSession, clearSession } = useAppSession();
  const signedIn = Boolean(currentUser);
  const feed = useRecipeFeed({ signedIn, query });
  const emailDialogRef = useRef<HTMLElement>(null);
  function handleSignedOut() {
    clearSession();
    setCreateOpen(false);
    setEditRecipe(null);
    setSelected(null);
    setSelectedRemote(null);
    setEmailOpen(false);
    setPasskeyPrompt(false);
  }
  useModalFocus(emailDialogRef, () => setEmailOpen(false), emailOpen);
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const confirmation =
      window.location.pathname === "/auth/confirm"
        ? params.get("token") || ""
        : "";
    const invitation = params.get("invite") || "";
    setConfirmationToken(confirmation);
    setInviteToken(invitation);
    setEmailOpen(Boolean(confirmation || invitation || params.get("signin")));
  }, []);
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
  async function passkeySignIn(): Promise<void> {
    setMessage("");
    try {
      await signInWithPasskey();
      await refreshSession();
      setEmailOpen(false);
    } catch (error: unknown) {
      setMessage(errorMessage(error, "Passkey sign-in failed."));
    }
  }
  async function signIn(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setMessage("");
    try {
      const response = await fetch("/api/auth/email", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, invitation: inviteToken }),
      });
      const result = (await response.json()) as { status?: string };
      if (!response.ok)
        throw new Error(
          response.status === 503
            ? "Email sign-in is not configured on this instance."
            : "Unable to request a sign-in link right now.",
        );
      if (result.status === "if_account_can_sign_in_link_sent")
        setMessage(
          "If this email can sign in, a one-time link will arrive shortly. Check your inbox.",
        );
      else setMessage("Unable to request a sign-in link right now.");
    } catch (error: unknown) {
      setMessage(
        errorMessage(
          error,
          "Unable to contact this Cookbook instance. Try again later.",
        ) || "Unable to contact this Cookbook instance. Try again later.",
      );
    }
  }
  async function confirmEmailSignIn(): Promise<void> {
    if (!confirmationToken || confirming) return;
    setConfirming(true);
    setMessage("");
    try {
      const response = await fetch("/api/auth/consume", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: confirmationToken }),
      });
      if (!response.ok)
        throw new Error(
          "This sign-in link is invalid, expired, or already used. Request a new link.",
        );
      window.history.replaceState({}, "", "/");
      setConfirmationToken("");
      const user = await refreshSession();
      if (user) {
        const passkeysResponse = await fetch("/api/auth/passkeys");
        if (passkeysResponse.ok && !(await passkeysResponse.json()).length)
          setPasskeyPrompt(true);
      }
      setEmailOpen(false);
    } catch (error: unknown) {
      setMessage(
        errorMessage(error, "This sign-in link could not be confirmed."),
      );
    } finally {
      setConfirming(false);
    }
  }
  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname={pathname}
        query={query}
        onQueryChange={setQuery}
        actionLabel={signedIn ? "Add a recipe" : "Sign in"}
        onAction={() => (signedIn ? setCreateOpen(true) : setEmailOpen(true))}
      />
      <main id="home" tabIndex={-1}>
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
          <FeatureIsland
            name="Federation settings"
            loading="federation settings"
          >
            <FederationAdmin />
          </FeatureIsland>
        )}
        {signedIn && (
          <FeatureIsland
            name="User administration"
            loading="user administration"
          >
            <AdminUsers currentUser={currentUser} />
          </FeatureIsland>
        )}
        {signedIn && (
          <FeatureIsland name="Courses" loading="courses">
            <Courses onOpenRecipe={setSelected} />
          </FeatureIsland>
        )}
        {signedIn && <AdminInvitations />}
        {signedIn && <AdminAuditLog />}
        {signedIn && (
          <CookbookDashboard
            user={currentUser}
            onOpenRecipe={setSelected}
            onRecipeRestored={(recipe: Recipe) => {
              feed.restoreRecipe(recipe);
            }}
          />
        )}
        <PublicCookbooks />
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
            <RecipeCard key={recipe.id} recipe={recipe} onOpen={onOpen} />
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
              onClick={() => setEmailOpen(true)}
            >
              Get started <Icon name="arrow" size={16} />
            </button>
          </section>
        )}
      </main>
      {passkeyPrompt && (
        <FeatureIsland name="Passkey setup" loading="passkey setup">
          <PasskeySetupPrompt onClose={() => setPasskeyPrompt(false)} />
        </FeatureIsland>
      )}
      <RecipeDialog
        recipe={selectedRemote || selected}
        currentUserID={currentUser?.id ?? null}
        onTransfer={(id) => {
          feed.removeRecipe(id);
          setSelected(null);
        }}
        canFork={Boolean(signedIn && !selectedRemote)}
        canEdit={Boolean(signedIn && !selectedRemote && selected?.can_edit)}
        canDelete={Boolean(signedIn && !selectedRemote && selected?.can_delete)}
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
        }}
      />
      {(createOpen || editRecipe) && (
        <RecipeForm
          user={currentUser}
          recipe={editRecipe}
          onClose={() => {
            setCreateOpen(false);
            setEditRecipe(null);
          }}
          onSignedOut={handleSignedOut}
          onCreated={(recipe) => {
            feed.addRecipe(recipe);
            setCreateOpen(false);
          }}
          onUpdated={(recipe) => {
            feed.replaceRecipe(recipe);
            setEditRecipe(null);
          }}
        />
      )}
      {emailOpen && (
        <div
          className="dialog-backdrop"
          role="presentation"
          onMouseDown={(e) =>
            e.target === e.currentTarget && setEmailOpen(false)
          }
        >
          <section
            className="signin-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="signin-title"
            ref={emailDialogRef}
            tabIndex={-1}
          >
            <button
              className="icon-button dialog-close"
              aria-label="Close sign in"
              onClick={() => setEmailOpen(false)}
            >
              <Icon name="close" />
            </button>
            <span className="brand-mark">
              <Icon name="chef" size={26} />
            </span>
            <span className="eyebrow">Welcome to your kitchen</span>
            {confirmationToken ? (
              <>
                <h2 id="signin-title">Confirm your sign-in</h2>
                <p>
                  Use this one-time link to sign in. Cookbook will only continue
                  after you choose the button below.
                </p>
                <button
                  className="button button--green"
                  type="button"
                  onClick={confirmEmailSignIn}
                  disabled={confirming}
                >
                  {confirming ? "Signing in…" : "Continue to Cookbook"}{" "}
                  <Icon name="arrow" size={16} />
                </button>
              </>
            ) : (
              <>
                <h2 id="signin-title">
                  {inviteToken
                    ? "Accept your invitation"
                    : "Sign in with email"}
                </h2>
                <p>
                  We’ll send you a one-time link to get back to your cookbooks.
                </p>
                <form onSubmit={signIn}>
                  <label htmlFor="signin-email">Email address</label>
                  <input
                    id="signin-email"
                    type="email"
                    autoComplete="email"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    placeholder="you@example.com"
                    required
                  />
                  <button className="button button--green" type="submit">
                    Send sign-in link <Icon name="arrow" size={16} />
                  </button>
                </form>
                <button
                  className="text-link passkey-action"
                  type="button"
                  onClick={passkeySignIn}
                >
                  Sign in with a passkey
                </button>
              </>
            )}
            {message && (
              <p className="form-message" role="status">
                {message}
              </p>
            )}
          </section>
        </div>
      )}
    </div>
  );
}

type AppRoute = "home" | "account-security" | "admin-settings";

export default function App({
  route = "home",
  pathname = "/",
}: {
  route?: AppRoute;
  pathname?: string;
}) {
  if (route === "account-security") {
    return (
      <FeatureIsland name="Account security page" loading="account security page">
        <AccountSecurityPage />
      </FeatureIsland>
    );
  }
  if (route === "admin-settings") {
    return (
      <FeatureIsland name="Instance settings page" loading="instance settings page">
        <AdminSettingsPage />
      </FeatureIsland>
    );
  }
  return <HomeApp pathname={pathname} />;
}

interface CookbookDashboardProps {
  user: UserSummary | null;
  onRecipeRestored: (recipe: Recipe) => void;
  onOpenRecipe: (recipe: Recipe) => void;
}

function CookbookDashboard({
  user,
  onRecipeRestored,
  onOpenRecipe,
}: CookbookDashboardProps) {
  const [cookbooks, setCookbooks] = useState<Cookbook[]>([]),
    [offset, setOffset] = useState(0),
    [hasMore, setHasMore] = useState(false),
    [loadingMore, setLoadingMore] = useState(false),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [createOpen, setCreateOpen] = useState(false),
    [editBook, setEditBook] = useState<Cookbook | null>(null),
    [memberBook, setMemberBook] = useState<Cookbook | null>(null),
    [detailBook, setDetailBook] = useState<Cookbook | null>(null);
  useEffect(() => {
    let active = true;
    fetch("/api/cookbooks?limit=24&offset=0")
      .then(async (response) => {
        if (!response.ok) throw new Error("Could not load your cookbooks.");
        const data = (await response.json()) as Cookbook[];
        if (active) {
          setCookbooks(data);
          setOffset(data.length);
          setHasMore(data.length === 24);
        }
      })
      .catch((err: unknown) => {
        if (active)
          setError(errorMessage(err, "Could not load your cookbooks."));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);
  async function loadMore() {
    if (loadingMore) return;
    setLoadingMore(true);
    setError("");
    try {
      const response = await fetch(`/api/cookbooks?limit=24&offset=${offset}`);
      if (!response.ok) throw new Error("Could not load more cookbooks.");
      const data = (await response.json()) as Cookbook[];
      setCookbooks((current) => {
        const known = new Set(current.map((book) => book.id));
        return [...current, ...data.filter((book) => !known.has(book.id))];
      });
      setOffset((value) => value + data.length);
      setHasMore(data.length === 24);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not load more cookbooks."));
    } finally {
      setLoadingMore(false);
    }
  }
  return (
    <section className="cookbook-dashboard" id="my-cookbooks">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Your collection</span>
          <h2>My cookbooks</h2>
        </div>
        <button className="text-link" onClick={() => setCreateOpen(true)}>
          Create cookbook <Icon name="arrow" size={15} />
        </button>
      </div>
      {loading ? (
        <p>Loading cookbooks…</p>
      ) : error && !cookbooks.length ? (
        <p className="form-message" role="alert">
          {error}
        </p>
      ) : cookbooks.length ? (
        <>
          <div className="cookbook-grid">
            {cookbooks.map((book) => (
              <article className="cookbook-card" key={book.id}>
                <span className="notebook-banner__icon">
                  <Icon name="book" size={24} />
                </span>
                <h3>{book.title}</h3>
                {book.description && <p>{book.description}</p>}
                <span className="recipe-card__category">
                  {book.visibility} cookbook
                </span>
                <button
                  className="text-link"
                  type="button"
                  onClick={() => setDetailBook(book)}
                >
                  Open cookbook
                </button>
                {book.owner_id === user?.id && (
                  <>
                    <button
                      className="text-link"
                      type="button"
                      onClick={() => setEditBook(book)}
                    >
                      Edit cookbook
                    </button>
                    <button
                      className="text-link"
                      type="button"
                      onClick={() => setMemberBook(book)}
                    >
                      Share and manage members
                    </button>
                  </>
                )}
              </article>
            ))}
          </div>
          {error && (
            <p className="form-message" role="alert">
              {error}
            </p>
          )}
          {hasMore && (
            <button
              className="button button--green"
              type="button"
              onClick={loadMore}
              disabled={loadingMore}
            >
              {loadingMore ? "Loading…" : "Load more cookbooks"}
            </button>
          )}
        </>
      ) : (
        <p className="cookbook-empty">
          Your cookbooks will appear here. Create one to start gathering
          recipes.
        </p>
      )}
      {createOpen && (
        <CookbookForm
          onClose={() => setCreateOpen(false)}
          onSaved={(book) => {
            setCookbooks((current) => [book, ...current]);
            setOffset((value) => value + 1);
            setCreateOpen(false);
          }}
        />
      )}
      {editBook && (
        <CookbookForm
          book={editBook}
          onClose={() => setEditBook(null)}
          onSaved={(book) => {
            setCookbooks((current) =>
              current.map((item) => (item.id === book.id ? book : item)),
            );
            setEditBook(null);
          }}
        />
      )}
      {memberBook && (
        <CookbookMembers
          book={memberBook}
          onClose={() => setMemberBook(null)}
        />
      )}
      {detailBook && (
        <CookbookDetail
          book={detailBook}
          onClose={() => setDetailBook(null)}
          onOpenRecipe={onOpenRecipe}
        />
      )}
      <RecipeTrash onRestored={onRecipeRestored} />
    </section>
  );
}

interface CookbookDetailProps {
  book: Cookbook;
  onClose: () => void;
  onOpenRecipe: (recipe: Recipe) => void;
}

function CookbookDetail({ book, onClose, onOpenRecipe }: CookbookDetailProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [query, setQuery] = useState(""),
    [sort, setSort] = useState("recent"),
    [tag, setTag] = useState(""),
    [recipes, setRecipes] = useState<Recipe[]>([]),
    [offset, setOffset] = useState(0),
    [hasMore, setHasMore] = useState(false),
    [loading, setLoading] = useState(true),
    [loadingMore, setLoadingMore] = useState(false),
    [error, setError] = useState("");
  const filterKeyRef = useRef("");
  const filterKey = query.trim() + ":" + sort + ":" + tag.trim();
  filterKeyRef.current = filterKey;
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    const params = new URLSearchParams({
      cookbook_id: book.id,
      limit: "24",
      offset: "0",
      sort,
    });
    if (query.trim()) params.set("q", query.trim());
    if (tag.trim()) params.set("tag", tag.trim());
    fetch(`/api/recipes?${params}`)
      .then(async (response) => {
        if (!response.ok)
          throw new Error(
            response.status === 404
              ? "You no longer have access to this cookbook."
              : "Could not load this cookbook.",
          );
        return response.json() as Promise<Recipe[]>;
      })
      .then((data: Recipe[]) => {
        if (active) {
          setRecipes(data);
          setOffset(data.length);
          setHasMore(data.length === 24);
        }
      })
      .catch((err: unknown) => {
        if (active) {
          setError(errorMessage(err, "Could not load this cookbook."));
          setRecipes([]);
          setHasMore(false);
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [book.id, query, sort, tag]);
  async function more() {
    if (loadingMore) return;
    const requestedFilterKey = filterKey;
    setLoadingMore(true);
    setError("");
    try {
      const params = new URLSearchParams({
        cookbook_id: book.id,
        limit: "24",
        offset: String(offset),
        sort,
      });
      if (query.trim()) params.set("q", query.trim());
      if (tag.trim()) params.set("tag", tag.trim());
      const response = await fetch(`/api/recipes?${params}`);
      if (!response.ok) throw new Error("Could not load more recipes.");
      const data = (await response.json()) as Recipe[];
      if (filterKeyRef.current !== requestedFilterKey) return;
      setRecipes((current) => [...current, ...data]);
      setOffset((value) => value + data.length);
      setHasMore(data.length === 24);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not load more recipes."));
    } finally {
      setLoadingMore(false);
    }
  }
  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        className="cookbook-detail"
        role="dialog"
        aria-modal="true"
        aria-labelledby="cookbook-detail-title"
        ref={dialogRef}
        tabIndex={-1}
      >
        <button
          className="icon-button dialog-close"
          aria-label="Close cookbook"
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
        <span className="eyebrow">{book.visibility} cookbook</span>
        <h2 id="cookbook-detail-title">{book.title}</h2>
        {book.description && <p>{book.description}</p>}
        <div className="cookbook-filters">
          <label className="cookbook-search">
            <span>Search recipes</span>
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search this cookbook…"
            />
          </label>
          <label>
            <span>Filter by tag</span>
            <input
              value={tag}
              onChange={(event) => setTag(event.target.value)}
              placeholder="e.g. vegetarian"
              maxLength={40}
            />
          </label>
          <label>
            <span>Sort by</span>
            <select
              value={sort}
              onChange={(event) =>
                setSort(event.target.value as "recent" | "oldest" | "title")
              }
            >
              <option value="recent">Recently updated</option>
              <option value="oldest">Oldest updated</option>
              <option value="title">Title</option>
            </select>
          </label>
        </div>
        {loading ? (
          <p className="cookbook-empty">Loading recipes…</p>
        ) : error && !recipes.length ? (
          <p className="form-message" role="alert">
            {error}
          </p>
        ) : recipes.length ? (
          <div className="recipe-grid">
            {recipes.map((recipe) => (
              <RecipeCard
                key={recipe.id}
                recipe={recipe}
                onOpen={onOpenRecipe}
              />
            ))}
          </div>
        ) : (
          <p className="cookbook-empty">
            No recipes are available in this cookbook.
          </p>
        )}
        {error && recipes.length > 0 && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
        {hasMore && (
          <button
            className="button button--green"
            type="button"
            disabled={loadingMore}
            onClick={more}
          >
            {loadingMore ? "Loading…" : "Load more recipes"}
          </button>
        )}
      </section>
    </div>
  );
}

interface CookbookFormProps {
  book?: Cookbook;
  onClose: () => void;
  onSaved: (book: Cookbook) => void;
}

function CookbookForm({ book, onClose, onSaved }: CookbookFormProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [title, setTitle] = useState(book?.title || ""),
    [description, setDescription] = useState(book?.description || ""),
    [visibility, setVisibility] = useState<Cookbook["visibility"]>(
      book?.visibility || "private",
    ),
    [error, setError] = useState(""),
    [saving, setSaving] = useState(false);
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setSaving(true);
    try {
      const response = await fetch(
        book ? `/api/cookbooks/${book.id}` : "/api/cookbooks",
        {
          method: book ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            title,
            description,
            visibility: book ? visibility : "private",
          }),
        },
      );
      if (!response.ok)
        throw new Error(
          response.status === 403
            ? "This instance does not allow that visibility or you cannot edit this cookbook."
            : "Could not save this cookbook. Try again.",
        );
      onSaved((await response.json()) as Cookbook);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not save this cookbook. Try again."));
    } finally {
      setSaving(false);
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
        aria-labelledby="cookbook-form-title"
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
        <span className="eyebrow">Your collection</span>
        <h2 id="cookbook-form-title">
          {book ? "Edit cookbook" : "Create a cookbook"}
        </h2>
        <p>
          {book
            ? "Update the title, description, or visibility."
            : "New cookbooks are private to you."}
        </p>
        <form onSubmit={submit}>
          <label htmlFor="cookbook-title">Title</label>
          <input
            id="cookbook-title"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            maxLength={200}
            required
          />
          <label htmlFor="cookbook-description">Description</label>
          <textarea
            id="cookbook-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            maxLength={5000}
          />
          {book && (
            <>
              <label htmlFor="cookbook-visibility">Visibility</label>
              <select
                id="cookbook-visibility"
                value={visibility}
                onChange={(event) =>
                  setVisibility(event.target.value as Cookbook["visibility"])
                }
              >
                <option value="private">Private</option>
                <option value="unlisted">Unlisted</option>
                <option value="public">Public</option>
              </select>
            </>
          )}
          <button
            className="button button--green"
            type="submit"
            disabled={saving}
          >
            {saving
              ? "Saving…"
              : book
                ? "Save changes"
                : "Create private cookbook"}
          </button>
        </form>
        {error && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
      </section>
    </div>
  );
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
function AdminAuditLog() {
  const [authorized, setAuthorized] = useState<boolean | null>(null),
    [items, setItems] = useState<AuditLogEntry[]>([]),
    [offset, setOffset] = useState(0),
    [hasMore, setHasMore] = useState(false),
    [loadingMore, setLoadingMore] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    fetch("/api/admin/audit?limit=24&offset=0")
      .then(async (response) => {
        if (response.status === 403) {
          if (active) setAuthorized(false);
          return;
        }
        if (!response.ok)
          throw new Error("Could not load the security audit log.");
        const data = (await response.json()) as AuditLogEntry[];
        if (active) {
          setAuthorized(true);
          setItems(data);
          setOffset(data.length);
          setHasMore(data.length === 24);
        }
      })
      .catch((err: unknown) => {
        if (active)
          setError(errorMessage(err, "Could not load the security audit log."));
      });
    return () => {
      active = false;
    };
  }, []);
  async function loadMore() {
    if (loadingMore) return;
    setLoadingMore(true);
    setError("");
    try {
      const response = await fetch(
        `/api/admin/audit?limit=24&offset=${offset}`,
      );
      if (!response.ok) throw new Error("Could not load more audit events.");
      const data = (await response.json()) as AuditLogEntry[];
      setItems((current) => [...current, ...data]);
      setOffset((value) => value + data.length);
      setHasMore(data.length === 24);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not load more audit events."));
    } finally {
      setLoadingMore(false);
    }
  }
  if (authorized === false) return null;
  if (authorized === null && !error) return null;
  return (
    <section className="cookbook-dashboard">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Instance security</span>
          <h2>Audit log</h2>
        </div>
      </div>
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {authorized && items.length === 0 && !error && (
        <p className="cookbook-empty">
          No audit events have been recorded yet.
        </p>
      )}
      {items.map((item) => (
        <div className="passkey-row" key={item.id}>
          <span>
            {item.actor_name} · {item.action} {item.resource_type}
            {item.resource_id ? `/${item.resource_id}` : ""}
            <small>
              {new Date(item.created_at).toLocaleString()} · HTTP{" "}
              {item.response_status}
            </small>
          </span>
        </div>
      ))}
      {hasMore && (
        <button
          className="text-link"
          type="button"
          onClick={loadMore}
          disabled={loadingMore}
        >
          {loadingMore ? "Loading…" : "Load more audit events"}
        </button>
      )}
    </section>
  );
}

function AdminInvitations() {
  const [authorized, setAuthorized] = useState<boolean | null>(null),
    [items, setItems] = useState<AdminInvitation[]>([]),
    [email, setEmail] = useState(""),
    [error, setError] = useState(""),
    [inviteURL, setInviteURL] = useState("");
  async function reload() {
    const response = await fetch("/api/admin/invitations");
    if (response.status === 403) {
      setAuthorized(false);
      return;
    }
    if (!response.ok) throw new Error("Could not load invitations.");
    setItems((await response.json()) as AdminInvitation[]);
    setAuthorized(true);
  }
  useEffect(() => {
    reload().catch((err: unknown) =>
      setError(errorMessage(err, "Could not load invitations.")),
    );
  }, []);
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    try {
      const response = await fetch("/api/admin/invitations", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      });
      if (!response.ok) throw new Error("Could not create an invitation.");
      const invite = (await response.json()) as AdminInvitationCreated;
      setInviteURL(
        `${window.location.origin}/?invite=${encodeURIComponent(invite.token)}`,
      );
      setEmail("");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not create an invitation."));
    }
  }
  async function revoke(id: string) {
    try {
      const response = await fetch(
        `/api/admin/invitations?id=${encodeURIComponent(id)}`,
        { method: "DELETE" },
      );
      if (!response.ok) throw new Error("Could not revoke this invitation.");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not revoke this invitation."));
    }
  }
  if (authorized === false) return null;
  return (
    <section className="cookbook-dashboard">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Instance access</span>
          <h2>Invitations</h2>
        </div>
      </div>
      <form className="inline-invite" onSubmit={create}>
        <label htmlFor="invite-email">Invite an email address</label>
        <input
          id="invite-email"
          type="email"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          required
        />
        <button className="button button--green" type="submit">
          Create invitation
        </button>
      </form>
      {inviteURL && (
        <p role="status">
          Invitation link (copy and send securely):{" "}
          <a href={inviteURL}>{inviteURL}</a>
        </p>
      )}
      {items.map((item) => (
        <div className="passkey-row" key={item.id}>
          <span>
            {item.email}
            <small>
              Expires {new Date(item.expires_at).toLocaleDateString()}
            </small>
          </span>
          <button
            className="text-link"
            type="button"
            onClick={() => revoke(item.id)}
          >
            Revoke
          </button>
        </div>
      ))}
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
    </section>
  );
}

function CookbookMembers({
  book,
  onClose,
}: {
  book: Cookbook;
  onClose: () => void;
}) {
  const dialogRef = useRef(null);
  useModalFocus(dialogRef, onClose);
  const [members, setMembers] = useState<CookbookMember[]>([]),
    [invitations, setInvitations] = useState<CookbookInvitation[]>([]),
    [email, setEmail] = useState(""),
    [role, setRole] = useState<CookbookInvitation["role"]>("viewer"),
    [inviteURL, setInviteURL] = useState(""),
    [error, setError] = useState("");
  async function reload() {
    const [membersResponse, inviteResponse] = await Promise.all([
      fetch(`/api/cookbooks/${book.id}/members`),
      fetch(`/api/cookbooks/${book.id}/members/invitations`),
    ]);
    if (!membersResponse.ok || !inviteResponse.ok)
      throw new Error("Could not load cookbook members.");
    setMembers((await membersResponse.json()) as CookbookMember[]);
    setInvitations((await inviteResponse.json()) as CookbookInvitation[]);
  }
  useEffect(() => {
    let active = true;
    Promise.all([
      fetch(`/api/cookbooks/${book.id}/members`),
      fetch(`/api/cookbooks/${book.id}/members/invitations`),
    ])
      .then(async (responses) => {
        if (responses.some((response) => !response.ok))
          throw new Error("Could not load cookbook members.");
        const [memberData, inviteData] = await Promise.all([
          responses[0].json() as Promise<CookbookMember[]>,
          responses[1].json() as Promise<CookbookInvitation[]>,
        ]);
        if (active) {
          setMembers(memberData);
          setInvitations(inviteData);
        }
      })
      .catch((err: unknown) => {
        if (active)
          setError(errorMessage(err, "Could not load cookbook members."));
      });
    return () => {
      active = false;
    };
  }, [book.id]);
  async function invite(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    try {
      const response = await fetch(
        `/api/cookbooks/${book.id}/members/invitations`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email, role }),
        },
      );
      if (!response.ok) throw new Error("Could not invite this person.");
      const item = (await response.json()) as CookbookInvitation & {
        token: string;
      };
      setInviteURL(
        `${window.location.origin}/?invite=${encodeURIComponent(item.token)}`,
      );
      setEmail("");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not invite this person."));
    }
  }
  async function updateMember(
    userID: string,
    nextRole: CookbookInvitation["role"],
  ) {
    try {
      const response = await fetch(`/api/cookbooks/${book.id}/members`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ user_id: userID, role: nextRole }),
      });
      if (!response.ok) throw new Error("Could not update this member.");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not update this member."));
    }
  }
  async function removeMember(userID: string) {
    try {
      const response = await fetch(
        `/api/cookbooks/${book.id}/members?user_id=${encodeURIComponent(userID)}`,
        { method: "DELETE" },
      );
      if (!response.ok) throw new Error("Could not remove this member.");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not remove this member."));
    }
  }
  async function revoke(id: string) {
    try {
      const response = await fetch(
        `/api/cookbooks/${book.id}/members/invitations?id=${encodeURIComponent(id)}`,
        { method: "DELETE" },
      );
      if (!response.ok) throw new Error("Could not revoke this invitation.");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not revoke this invitation."));
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
        aria-labelledby="members-title"
        ref={dialogRef}
        tabIndex={-1}
      >
        <button
          className="icon-button dialog-close"
          aria-label="Close members"
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
        <span className="eyebrow">{book.title}</span>
        <h2 id="members-title">Cookbook members</h2>
        <div className="passkey-list">
          {members.map((member) => (
            <div className="passkey-row" key={member.user_id}>
              <span>
                {member.display_name}
                <small>
                  {member.email} · {member.role}
                </small>
              </span>
              {member.role !== "owner" && (
                <>
                  <select
                    aria-label={`Role for ${member.display_name}`}
                    value={member.role}
                    onChange={(event) =>
                      updateMember(
                        member.user_id,
                        event.target.value as CookbookInvitation["role"],
                      )
                    }
                  >
                    <option value="viewer">Viewer</option>
                    <option value="editor">Editor</option>
                  </select>
                  <button
                    type="button"
                    className="text-link"
                    onClick={() => removeMember(member.user_id)}
                  >
                    Remove
                  </button>
                </>
              )}
            </div>
          ))}
        </div>
        <form onSubmit={invite}>
          <label htmlFor="member-email">Invite by email</label>
          <input
            id="member-email"
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            required
          />
          <label htmlFor="member-role">Role</label>
          <select
            id="member-role"
            value={role}
            onChange={(event) =>
              setRole(event.target.value as CookbookInvitation["role"])
            }
          >
            <option value="viewer">Viewer</option>
            <option value="editor">Editor</option>
          </select>
          <button className="button button--green" type="submit">
            Create invitation
          </button>
        </form>
        {inviteURL && (
          <p role="status">
            Invitation link (send securely): <a href={inviteURL}>{inviteURL}</a>
          </p>
        )}
        {invitations.map((item) => (
          <div className="passkey-row" key={item.id}>
            <span>
              {item.email}
              <small>
                {item.role} · expires{" "}
                {new Date(item.expires_at).toLocaleDateString()}
              </small>
            </span>
            <button
              type="button"
              className="text-link"
              onClick={() => revoke(item.id)}
            >
              Revoke
            </button>
          </div>
        ))}
        {error && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
      </section>
    </div>
  );
}
