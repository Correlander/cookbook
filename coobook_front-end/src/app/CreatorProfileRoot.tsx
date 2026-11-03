import { useEffect, useState } from "react";
import AppNavigation from "./AppNavigation.tsx";
import AccountNavigation from "./AccountNavigation.tsx";
import {
  AppSessionProvider,
  SessionCheckError,
  useAppSession,
} from "./AppSession.tsx";
import CreatorProfileEditor from "../features/creator/CreatorProfileEditor.tsx";
import CreatorProfileView from "../features/creator/CreatorProfileView.tsx";

export default function CreatorProfileRoot({
  mode,
}: {
  mode: "editor" | "view";
}) {
  return (
    <AppSessionProvider>
      <CreatorProfilePage mode={mode} />
    </AppSessionProvider>
  );
}

function CreatorProfilePage({ mode }: { mode: "editor" | "view" }) {
  const { state, currentUser, refreshSession } = useAppSession();
  const [profileID, setProfileID] = useState<string | null>(
    mode === "editor" ? "" : null,
  );
  const pathname = mode === "editor" ? "/account/creator-profile" : "/creator";

  useEffect(() => {
    if (mode === "view") {
      setProfileID(new URLSearchParams(window.location.search).get("id") ?? "");
    }
  }, [mode]);

  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname={pathname}
        sessionStatus={state.status}
        onRetrySession={() => void refreshSession()}
      />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome">
          <div>
            <span className="eyebrow">Your kitchen</span>
            <h1>Creator profile</h1>
            <p>A local profile for recipes you explicitly credit.</p>
          </div>
        </section>
        {mode === "editor" && state.status === "authenticated" && (
          <AccountNavigation pathname={pathname} />
        )}
        {state.status === "loading" ? (
          <p className="cookbook-dashboard" role="status">
            Checking your session…
          </p>
        ) : state.status === "error" ? (
          <SessionCheckError
            message={state.message}
            onRetry={() => void refreshSession()}
          />
        ) : state.status === "anonymous" ? (
          <SignInRequired />
        ) : state.status === "authenticated" && mode === "editor" ? (
          <CreatorProfileEditor
            key={state.currentUser.id}
            actorID={state.currentUser.id}
            onSessionExpired={() => void refreshSession()}
          />
        ) : state.status !== "authenticated" ? (
          <SignInRequired />
        ) : profileID === null ? (
          <p className="cookbook-dashboard" role="status">
            Reading profile link…
          </p>
        ) : (
          <CreatorProfileView
            key={`${state.currentUser.id}:${profileID}`}
            actorID={state.currentUser.id}
            profileID={profileID}
            onSessionExpired={() => void refreshSession()}
          />
        )}
      </main>
    </div>
  );
}

function SignInRequired() {
  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="signin-required-title"
    >
      <h2 id="signin-required-title">Sign in to continue</h2>
      <p>This page uses your Cookbook account and browser session.</p>
      <a className="button button--green" href="/?signin=1">
        Sign in
      </a>
    </section>
  );
}
