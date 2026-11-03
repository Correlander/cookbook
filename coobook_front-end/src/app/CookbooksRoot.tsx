import { useEffect, useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import { AppSessionProvider, useAppSession } from "./AppSession.tsx";
import { SessionCheckError } from "./AppSession.tsx";
import CookbookDashboard from "../features/cookbooks/CookbookDashboard.tsx";
import PublicCookbooks from "../PublicCookbooks.tsx";

function CookbooksPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  const [initialQuery, setInitialQuery] = useState<string | null>(null);

  useEffect(() => {
    setInitialQuery(new URLSearchParams(window.location.search).get("q") ?? "");
  }, []);

  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname="/cookbooks"
        sessionStatus={state.status}
        onRetrySession={() => void refreshSession()}
      />
      <main id="home" tabIndex={-1}>
        {state.status === "loading" || initialQuery === null ? (
          <p className="cookbook-dashboard" role="status">
            Checking your session…
          </p>
        ) : state.status === "error" ? (
          <SessionCheckError
            message={state.message}
            onRetry={() => void refreshSession()}
          />
        ) : state.status === "authenticated" ? (
          <CookbookDashboard user={state.currentUser} surface="page" />
        ) : (
          <PublicCookbooks surface="page" initialQuery={initialQuery} />
        )}
      </main>
    </div>
  );
}

export default function CookbooksRoot() {
  return (
    <AppSessionProvider>
      <CookbooksPage />
    </AppSessionProvider>
  );
}
