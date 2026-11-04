import { useEffect, useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, SessionCheckError, useAppSession } from "./AppSession.tsx";
import RecipeCollection from "../features/recipes/RecipeCollection.tsx";
import useRecipeFeed from "../features/recipes/useRecipeFeed.ts";
import RecipeCardContents from "../features/recipes/RecipeCardContents.tsx";
import CookbookCollection from "../features/cookbooks/CookbookCollection.tsx";
import PublicCourses from "../PublicCourses.tsx";

type DiscoverType = "recipes" | "cookbooks" | "courses";
const discoverTypes: DiscoverType[] = ["recipes", "cookbooks", "courses"];
type PublicConfig = { courses_enabled: boolean; public_instance: boolean };

function routeFromSearch(search: string) {
  const params = new URLSearchParams(search);
  const candidate = params.get("type");
  return {
    type: discoverTypes.includes(candidate as DiscoverType) ? candidate as DiscoverType : "recipes" as DiscoverType,
    query: params.get("q") ?? "",
  };
}

function DiscoverPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  const [route, setRoute] = useState({ type: "recipes" as DiscoverType, query: "" });
  const [coursesEnabled, setCoursesEnabled] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    fetch("/api/public/config", { cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error("Could not check enabled features.");
        return response.json() as Promise<PublicConfig>;
      })
      .then((config) => { if (active) setCoursesEnabled(config.courses_enabled === true && config.public_instance === true); })
      .catch(() => { if (active) setCoursesEnabled(false); });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    const syncRoute = () => setRoute(routeFromSearch(window.location.search));
    syncRoute();
    window.addEventListener("popstate", syncRoute);
    return () => window.removeEventListener("popstate", syncRoute);
  }, []);

  function updateRoute(next: Partial<typeof route>, push = true) {
    const value = { ...route, ...next };
    const params = new URLSearchParams();
    params.set("type", value.type);
    if (value.query.trim()) params.set("q", value.query.trim());
    const method = push ? "pushState" : "replaceState";
    window.history[method]({}, "", `/discover?${params}`);
    setRoute(value);
  }

  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname="/discover"
        activeArea="discover"
        sessionStatus={state.status}
        onRetrySession={() => void refreshSession()}
        query={route.type === "recipes" ? route.query : undefined}
        onQueryChange={route.type === "recipes" ? (query) => updateRoute({ query }, false) : undefined}
      />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome">
          <div><span className="eyebrow">Explore this Cookbook</span><h1>Discover</h1><p>Browse recipes, cookbooks, and enabled public courses.</p></div>
        </section>
        <div className="section-heading" aria-label="Discover content type">
          {discoverTypes.filter((type) => type !== "courses" || coursesEnabled === true).map((type) => (
            <button
              className={route.type === type ? "button button--green" : "text-link"}
              type="button"
              key={type}
              aria-pressed={route.type === type}
              onClick={() => updateRoute({ type, query: type === "recipes" ? route.query : "" })}
            >{type[0].toUpperCase() + type.slice(1)}</button>
          ))}
        </div>
        {state.status === "loading" ? <p className="cookbook-dashboard" role="status">Checking your session…</p> :
          state.status === "error" ? <SessionCheckError message={state.message} onRetry={() => void refreshSession()} /> :
            route.type === "recipes" ? <DiscoverRecipes signedIn={state.status === "authenticated"} currentUserID={currentUser?.id ?? null} query={route.query} /> :
              route.type === "cookbooks" ? <CookbookCollection mode={state.status === "authenticated" ? "accessible" : "public"} user={currentUser} surface="page" readOnly /> :
                coursesEnabled === null ? <p role="status">Checking enabled features…</p> :
                  coursesEnabled ? <PublicCourses /> : <p className="cookbook-empty">Public courses are not enabled for this instance.</p>}
      </main>
    </div>
  );
}

function DiscoverRecipes({ signedIn, currentUserID, query }: { signedIn: boolean; currentUserID: string | null; query: string }) {
  const feed = useRecipeFeed({ signedIn, query });
  return <RecipeCollection
    signedIn={signedIn}
    query={query}
    feed={feed}
    heading="Recipes"
    emptyMessage="No recipes are available in this catalog."
    onOpenRecipe={() => {}}
    renderRecipe={(recipe) => {
      const provenance = recipe.source_origin ? `From ${recipe.source_origin}` : currentUserID && recipe.owner_id === currentUserID ? "Owned by you" : currentUserID ? "Available to you" : "Public on this instance";
      return <a key={recipe.id} className="recipe-card block" href={`/recipe?id=${encodeURIComponent(recipe.id)}`} aria-label={`Open ${recipe.title}`}><RecipeCardContents title={recipe.title} creatorName={recipe.creator_name} creatorAttributions={currentUserID ? recipe.creator_attributions : undefined} image={recipe.image} stepCount={recipe.steps?.length ?? 0} relationshipLabel={provenance} /></a>;
    }}
  />;
}

export default function DiscoverRoot() {
  return <AppSessionProvider><DiscoverPage /></AppSessionProvider>;
}
