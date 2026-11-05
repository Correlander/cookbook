import { useEffect, useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, SessionCheckError, useAppSession } from "./AppSession.tsx";
import MenuDetail from "../features/menus/MenuDetail.tsx";
import usePublicAuthConfig from "../features/auth/usePublicAuthConfig.ts";

const MENU_ID_PATTERN = /^[a-f0-9]{32}$/;

function MenuUnavailable() {
  return (
    <section className="cookbook-dashboard" aria-labelledby="menu-unavailable-title">
      <h1 id="menu-unavailable-title">Menu unavailable</h1>
      <p>This private menu could not be found or is not available to you.</p>
      <a className="text-link" href="/library?type=menus">Back to My Library</a>
    </section>
  );
}

function MenuDetailPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  const { state: config, retry } = usePublicAuthConfig();
  const [menuID, setMenuID] = useState<string | null>(null);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const ids = params.getAll("id");
    setMenuID(params.size === 1 && ids.length === 1 && MENU_ID_PATTERN.test(ids[0] ?? "") ? ids[0]! : "");
  }, []);

  const ready = config.status === "ready";
  const supported = ready && config.config.menu_api === true;
  const unavailable = menuID === "" || (ready && !supported);

  return (
    <div className="app-content menu-detail-root">
      <AppNavigation currentUser={currentUser} pathname="/menu" sessionStatus={state.status} onRetrySession={() => void refreshSession()} publicConfigState={config} />
      <main id="home" tabIndex={-1}>
        {state.status === "error" ? <SessionCheckError message={state.message} onRetry={() => void refreshSession()} /> :
          state.status === "loading" || menuID === null || config.status === "loading" ? <p className="cookbook-dashboard" role="status">Checking access to this private menu…</p> :
            config.status === "error" ? <section className="cookbook-dashboard"><p role="alert">Could not confirm menu support.</p><button type="button" className="text-link" onClick={retry}>Retry</button></section> :
              state.status === "anonymous" ? <section className="cookbook-dashboard"><h1>Sign in to view this private menu</h1><a className="button button--green" href="/?signin=1">Sign in</a></section> :
                unavailable ? <MenuUnavailable /> :
                  <MenuDetail actorID={state.currentUser.id} menuID={menuID} onDeleted={() => window.location.assign("/library?type=menus")} />}
      </main>
    </div>
  );
}

export default function MenuDetailRoot() {
  return <AppSessionProvider><MenuDetailPage /></AppSessionProvider>;
}
