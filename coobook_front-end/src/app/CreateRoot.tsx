import { useEffect, useState } from "react";
import type { Recipe } from "../apiModels";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, SessionCheckError, useAppSession } from "./AppSession.tsx";
import RecipeForm from "../features/recipes/RecipeForm.tsx";
import CookbookForm from "../features/cookbooks/CookbookForm.tsx";
import CourseForm from "../features/courses/CourseForm.tsx";
import MenuForm from "../features/menus/MenuForm.tsx";
import type { PrivateMenu } from "../apiModels";

type CreateType = "recipes" | "cookbooks" | "courses" | "menus";

function CreatePage({ navigate }: { navigate: (path: string) => void }) {
  const { state, currentUser, refreshSession, clearSession } = useAppSession();
  const [courseEnabled, setCourseEnabled] = useState<boolean | null>(null);
  const [menuEnabled, setMenuEnabled] = useState(false);
  const [configError, setConfigError] = useState(false);
  const [selected, setSelected] = useState<CreateType | null>(null);

  useEffect(() => {
    const requested = new URLSearchParams(window.location.search).get("type");
    if (requested === "recipes" || requested === "cookbooks") setSelected(requested);
    if (requested === "menus" && menuEnabled) setSelected("menus");
  }, [menuEnabled]);

  useEffect(() => {
    if (state.status !== "authenticated") return;
    let active = true;
    setCourseEnabled(null);
    setMenuEnabled(false);
    setConfigError(false);
    fetch("/api/public/config", { cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error("Could not check available features.");
        return response.json() as Promise<unknown>;
      })
      .then((value) => {
        if (!active) return;
        const config = isRecord(value) ? value : {};
        setCourseEnabled(config.courses_enabled === true);
        setMenuEnabled(config.menu_api === true);
      })
      .catch(() => { if (active) { setConfigError(true); setCourseEnabled(false); setMenuEnabled(false); } });
    return () => { active = false; };
  }, [state.status, currentUser?.id]);

  const leave = navigate;

  return (
    <div className="app-content">
      <AppNavigation currentUser={currentUser} pathname="/create" activeArea="create" sessionStatus={state.status} onRetrySession={() => void refreshSession()} />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome"><div><span className="eyebrow">Your workspace</span><h1>Create</h1><p>Choose a type to start a new item.</p></div></section>
        {state.status === "loading" ? <p className="cookbook-dashboard" role="status">Checking your session…</p> :
          state.status === "error" ? <SessionCheckError message={state.message} onRetry={() => void refreshSession()} /> :
            state.status === "anonymous" ? <section className="cookbook-dashboard"><h2>Sign in to create</h2><p>Creating content is available to signed-in account holders.</p><a className="button button--green" href="/?signin=1">Sign in</a></section> :
              <>
                {configError && <p className="form-message" role="alert">Could not check enabled features. Course and menu creation are unavailable until server support can be confirmed.</p>}
                {!selected ? <section className="cookbook-dashboard" aria-label="Create content type">
                  <div className="section-heading"><h2>New content</h2></div>
                  <div className="section-heading">
                    <button className="button button--green" onClick={() => setSelected("recipes")}>Recipe</button>
                    <button className="button button--green" onClick={() => setSelected("cookbooks")}>Cookbook</button>
                    {courseEnabled && <button className="button button--green" onClick={() => setSelected("courses")}>Course</button>}
                    {menuEnabled && <button className="button button--green" onClick={() => setSelected("menus")}>Menu</button>}
                  </div>
                  {courseEnabled === null && !configError && <p role="status">Checking enabled features…</p>}
                </section> : <section className="cookbook-dashboard"><button className="text-link" onClick={() => setSelected(null)}>Choose another type</button><h2>{selected === "courses" ? "Course details" : selected === "menus" ? "Create a private menu" : `Create ${selected === "recipes" ? "a recipe" : "a cookbook"}`}</h2></section>}
                {selected === "recipes" && currentUser && <RecipeForm user={currentUser} recipe={null} onClose={() => leave("/library?type=recipes")} onSignedOut={() => { clearSession(); setSelected(null); }} onUpdated={() => {}} onCreated={(recipe: Recipe) => leave(`/recipe?id=${encodeURIComponent(recipe.id)}`)} />}
                {selected === "cookbooks" && <CookbookForm onClose={() => leave("/library?type=cookbooks")} onSaved={(book) => leave(`/cookbook?id=${encodeURIComponent(book.id)}`)} />}
                {selected === "courses" && courseEnabled && <CourseForm course={null} onClose={() => setSelected(null)} onSaved={async () => leave("/")} />}
                {selected === "menus" && menuEnabled && <MenuForm menu={null} onClose={() => setSelected(null)} onSaved={(menu: PrivateMenu) => leave(`/menu?id=${encodeURIComponent(menu.id)}`)} />}
              </>}
      </main>
    </div>
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export default function CreateRoot({ navigate = (path) => window.location.assign(path) }: { navigate?: (path: string) => void }) {
  return <AppSessionProvider><CreatePage navigate={navigate} /></AppSessionProvider>;
}
