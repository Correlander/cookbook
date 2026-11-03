import { useEffect, useRef, useState } from "react";
import type {
  CreatorCookbookItem,
  CreatorProfile,
  CreatorProfileList,
  CreatorRecipeItem,
} from "../../apiModels";
import {
  CREATOR_PROFILE_PAGE_SIZE,
  CreatorProfileApiError,
  getCreatorProfile,
  listCreatorCookbooks,
  listCreatorRecipes,
} from "./creatorProfileApi.ts";

interface CollectionPage<T> {
  items: T[];
  offset: number;
  hasMore: boolean;
  loadingMore: boolean;
  error: string;
}

function emptyPage<T>(): CollectionPage<T> {
  return {
    items: [],
    offset: 0,
    hasMore: false,
    loadingMore: false,
    error: "",
  };
}

function asPage<T>(page: CreatorProfileList<T>): CollectionPage<T> {
  return {
    items: page.items,
    offset: page.offset + page.items.length,
    hasMore: page.items.length === page.limit,
    loadingMore: false,
    error: "",
  };
}

function roleLabel(role: string, target: "recipe" | "cookbook") {
  if (role === "creator" && target === "cookbook") return "Cookbook creator";
  if (role === "creator") return "Creator";
  if (role === "adapter") return "Adapter";
  if (role === "contributor") return "Contributor";
  return `Other role: ${role}`;
}

function provenanceLabel(provenance: string) {
  if (provenance === "self_asserted_at_creation")
    return "self-asserted when created";
  if (provenance === "local_record_creation")
    return "recorded when cookbook was created";
  if (provenance === "adapter_source")
    return "adaptation with source preserved";
  if (provenance === "external_claim") return "unverified external claim";
  return `provenance: ${provenance}`;
}

export default function CreatorProfileView({
  profileID,
  actorID,
  onSessionExpired,
}: {
  profileID: string;
  actorID: string;
  onSessionExpired: () => void;
}) {
  const [profile, setProfile] = useState<CreatorProfile | null>(null);
  const [recipes, setRecipes] =
    useState<CollectionPage<CreatorRecipeItem>>(emptyPage);
  const [cookbooks, setCookbooks] =
    useState<CollectionPage<CreatorCookbookItem>>(emptyPage);
  const [state, setState] = useState<
    "loading" | "ready" | "unavailable" | "session-error"
  >("loading");
  const [retry, setRetry] = useState(0);
  const requestGeneration = useRef(0);

  useEffect(() => {
    let active = true;
    const generation = ++requestGeneration.current;
    setState("loading");
    setProfile(null);
    setRecipes(emptyPage());
    setCookbooks(emptyPage());
    if (!profileID) {
      setState("unavailable");
      return () => {
        active = false;
      };
    }
    void (async () => {
      try {
        const data = await getCreatorProfile(profileID);
        if (!active) return;
        setProfile(data.profile);
        const [recipeResult, cookbookResult] = await Promise.allSettled([
          listCreatorRecipes(profileID),
          listCreatorCookbooks(profileID),
        ]);
        if (!active || requestGeneration.current !== generation) return;
        const unavailable = [recipeResult, cookbookResult].some(
          (result) =>
            result.status === "rejected" &&
            result.reason instanceof CreatorProfileApiError &&
            result.reason.status === 404,
        );
        if (unavailable) {
          setState("unavailable");
          setProfile(null);
          return;
        }
        const unauthorized = [recipeResult, cookbookResult].some(
          (result) =>
            result.status === "rejected" &&
            result.reason instanceof CreatorProfileApiError &&
            result.reason.status === 401,
        );
        if (unauthorized) {
          setState("session-error");
          setProfile(null);
          return;
        }
        if (recipeResult.status === "fulfilled") {
          setRecipes(asPage(recipeResult.value));
        } else {
          setRecipes({
            ...emptyPage<CreatorRecipeItem>(),
            error: "Could not load attributed recipes. Retry this list.",
          });
        }
        if (cookbookResult.status === "fulfilled") {
          setCookbooks(asPage(cookbookResult.value));
        } else {
          setCookbooks({
            ...emptyPage<CreatorCookbookItem>(),
            error: "Could not load attributed cookbooks. Retry this list.",
          });
        }
        setState("ready");
      } catch (cause) {
        if (!active || requestGeneration.current !== generation) return;
        if (cause instanceof CreatorProfileApiError && cause.status === 401) {
          setState("session-error");
        } else {
          setState("unavailable");
        }
      }
    })();
    return () => {
      active = false;
    };
  }, [profileID, actorID, retry]);

  async function loadMore(kind: "recipes" | "cookbooks") {
    const page = kind === "recipes" ? recipes : cookbooks;
    if ((!page.hasMore && !page.error) || page.loadingMore) return;
    const offset = page.error && page.items.length === 0 ? 0 : page.offset;
    const generation = requestGeneration.current;
    if (kind === "recipes") {
      setRecipes((current) => ({ ...current, loadingMore: true, error: "" }));
    } else {
      setCookbooks((current) => ({ ...current, loadingMore: true, error: "" }));
    }
    try {
      if (kind === "recipes") {
        const result = await listCreatorRecipes(profileID, offset);
        if (generation !== requestGeneration.current) return;
        if (result.offset !== offset) {
          throw new CreatorProfileApiError(
            "The creator list page did not match the requested offset.",
            502,
            "invalid_pagination_response",
          );
        }
        setRecipes((current) => ({
          ...current,
          items: [...current.items, ...result.items],
          offset: result.offset + result.items.length,
          hasMore: result.items.length === result.limit,
          loadingMore: false,
          error: "",
        }));
      } else {
        const result = await listCreatorCookbooks(profileID, offset);
        if (generation !== requestGeneration.current) return;
        if (result.offset !== offset) {
          throw new CreatorProfileApiError(
            "The creator list page did not match the requested offset.",
            502,
            "invalid_pagination_response",
          );
        }
        setCookbooks((current) => ({
          ...current,
          items: [...current.items, ...result.items],
          offset: result.offset + result.items.length,
          hasMore: result.items.length === result.limit,
          loadingMore: false,
          error: "",
        }));
      }
    } catch (cause) {
      if (generation !== requestGeneration.current) return;
      if (cause instanceof CreatorProfileApiError && cause.status === 401) {
        setProfile(null);
        setState("session-error");
        return;
      }
      if (cause instanceof CreatorProfileApiError && cause.status === 404) {
        setProfile(null);
        setState("unavailable");
        return;
      }
      if (kind === "recipes") {
        setRecipes((current) => ({
          ...current,
          loadingMore: false,
          error: "Could not load more recipes. Retry this list.",
        }));
      } else {
        setCookbooks((current) => ({
          ...current,
          loadingMore: false,
          error: "Could not load more cookbooks. Retry this list.",
        }));
      }
    }
  }

  if (state === "loading")
    return (
      <p className="cookbook-dashboard" role="status">
        Loading creator profile…
      </p>
    );
  if (state === "session-error")
    return (
      <section className="cookbook-dashboard">
        <h2>Session check needed</h2>
        <p>
          Your session could not read this profile. Retry the session check
          before continuing.
        </p>
        <button type="button" onClick={onSessionExpired}>
          Retry session
        </button>
      </section>
    );
  if (state === "unavailable" || !profile)
    return (
      <section
        className="cookbook-dashboard"
        aria-labelledby="creator-unavailable-title"
      >
        <h2 id="creator-unavailable-title">Creator profile unavailable</h2>
        <p>This profile could not be found or is not available to you.</p>
        <button type="button" onClick={() => setRetry((value) => value + 1)}>
          Retry profile
        </button>
      </section>
    );

  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="creator-profile-title"
    >
      <span className="eyebrow">
        {profile.visibility === "instance"
          ? "Same-instance profile · signed-in viewers"
          : "Private profile preview"}
      </span>
      <h2 id="creator-profile-title">
        {profile.public_name || "Unnamed creator"}
      </h2>
      {profile.bio && <p>{profile.bio}</p>}
      {profile.visibility === "private" ? (
        <p className="dialog-note">
          This is your private profile preview. Only you can view it.
        </p>
      ) : (
        <p className="dialog-note">
          Share this same-instance link with signed-in people on this Cookbook
          instance:{" "}
          <a
            href={`/creator?id=${encodeURIComponent(profile.id)}`}
          >{`/creator?id=${encodeURIComponent(profile.id)}`}</a>
          . Recipes and cookbooks keep their own access rules.
        </p>
      )}
      <p className="dialog-note">
        Profile visibility does not grant access to recipes or cookbooks. Each
        item follows its own access rules.
      </p>
      <section aria-labelledby="creator-recipes-title">
        <h3 id="creator-recipes-title">Recipes</h3>
        {recipes.error ? (
          <p role="alert" className="form-message">
            {recipes.error}
          </p>
        ) : recipes.items.length ? (
          <ul>
            {recipes.items.map(({ recipe, role, provenance }) => (
              <li key={`${recipe.id}:${role}`}>
                <a href={`/recipe?id=${encodeURIComponent(recipe.id)}`}>
                  {recipe.title}
                </a>
                <span>
                  {" "}
                  —{" "}
                  <span className="creator-attribution__role">
                    {roleLabel(role, "recipe")}
                  </span>
                  <span className="creator-attribution__provenance">
                    {" "}
                    · {provenanceLabel(provenance)}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <p>No readable attributed recipes.</p>
        )}
        {(recipes.hasMore || recipes.error) && (
          <button
            type="button"
            disabled={recipes.loadingMore}
            onClick={() => void loadMore("recipes")}
          >
            {recipes.loadingMore
              ? "Loading more recipes…"
              : recipes.error
                ? "Retry loading recipes"
                : "Load more recipes"}
          </button>
        )}
      </section>
      <section aria-labelledby="creator-cookbooks-title">
        <h3 id="creator-cookbooks-title">Cookbooks</h3>
        {cookbooks.error ? (
          <p role="alert" className="form-message">
            {cookbooks.error}
          </p>
        ) : cookbooks.items.length ? (
          <ul>
            {cookbooks.items.map(({ cookbook, role, provenance }) => (
              <li key={`${cookbook.id}:${role}`}>
                <a href={`/cookbook?id=${encodeURIComponent(cookbook.id)}`}>
                  {cookbook.title}
                </a>
                <span>
                  {" "}
                  —{" "}
                  <span className="creator-attribution__role">
                    {roleLabel(role, "cookbook")}
                  </span>
                  <span className="creator-attribution__provenance">
                    {" "}
                    · {provenanceLabel(provenance)}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <p>No readable attributed cookbooks.</p>
        )}
        {(cookbooks.hasMore || cookbooks.error) && (
          <button
            type="button"
            disabled={cookbooks.loadingMore}
            onClick={() => void loadMore("cookbooks")}
          >
            {cookbooks.loadingMore
              ? "Loading more cookbooks…"
              : cookbooks.error
                ? "Retry loading cookbooks"
                : "Load more cookbooks"}
          </button>
        )}
      </section>
      <span className="sr-only">
        Each list loads up to {CREATOR_PROFILE_PAGE_SIZE} items at a time.
      </span>
    </section>
  );
}
