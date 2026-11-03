import { useEffect, useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, useAppSession } from "./AppSession.tsx";
import { SessionCheckError } from "./AppSession.tsx";
import type { Cookbook, PublicCookbook } from "../apiModels";
import CookbookDetail from "../features/cookbooks/CookbookDetail.tsx";
import { getCookbook, getPublicCookbook } from "../features/cookbooks/cookbookApi.ts";

type CookbookRead = Cookbook | PublicCookbook;

function CookbookUnavailable() {
  return (
    <section className="cookbook-dashboard" aria-labelledby="cookbook-unavailable-title">
      <h1 id="cookbook-unavailable-title">Cookbook unavailable</h1>
      <p>This cookbook could not be found or is not available to you.</p>
      <a className="text-link" href="/cookbooks">Back to cookbooks</a>
    </section>
  );
}

function CookbookDetailPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  const [route, setRoute] = useState<{ id: string; returnQuery: string } | null>(null);
  const [book, setBook] = useState<CookbookRead | null>(null);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    setRoute({
      id: params.get("id") ?? "",
      returnQuery: params.get("return_q") ?? "",
    });
  }, []);

  useEffect(() => {
    if (state.status === "loading" || state.status === "error" || route === null) {
      setLoading(state.status === "loading" || route === null);
      setBook(null);
      setUnavailable(false);
      return;
    }
    if (!route.id.trim()) {
      setBook(null);
      setUnavailable(true);
      setLoading(false);
      return;
    }
    let active = true;
    setLoading(true);
    setBook(null);
    setUnavailable(false);
    const request =
      state.status === "authenticated"
        ? getCookbook(route.id, "Cookbook unavailable.")
        : getPublicCookbook(route.id, "Cookbook unavailable.");
    request
      .then((result) => {
        if (active) setBook(result);
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
  }, [currentUser, route, state.status]);

  const authenticated = state.status === "authenticated";

  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname="/cookbook"
        sessionStatus={state.status}
        onRetrySession={() => void refreshSession()}
      />
      <main id="home" tabIndex={-1}>
        {state.status === "error" ? (
          <SessionCheckError
            message={state.message}
            onRetry={() => void refreshSession()}
          />
        ) : state.status === "loading" || route === null ? (
          <p className="cookbook-dashboard" role="status">
            Checking your session…
          </p>
        ) : loading ? (
          <p className="cookbook-dashboard" role="status">Loading cookbook…</p>
        ) : unavailable || !book ? (
          <CookbookUnavailable />
        ) : (
          <CookbookDetail
            initialBook={book}
            mode={authenticated ? "authenticated" : "public"}
            currentUserID={authenticated ? state.currentUser.id : null}
            returnQuery={route.returnQuery}
          />
        )}
      </main>
    </div>
  );
}

export default function CookbookDetailRoot() {
  return (
    <AppSessionProvider>
      <CookbookDetailPage />
    </AppSessionProvider>
  );
}
