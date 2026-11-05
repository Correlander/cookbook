import { useCallback, useRef } from "react";
import AppNavigation from "./AppNavigation.tsx";
import {
  AppSessionProvider,
  SessionCheckError,
  useAppSession,
} from "./AppSession.tsx";
import AuthEntry from "../features/auth/AuthEntry.tsx";
import usePublicAuthConfig from "../features/auth/usePublicAuthConfig.ts";

function AuthConfirmPage({ onContinueHome }: { onContinueHome: () => void }) {
  const { state, currentUser, refreshSession } = useAppSession();
  const { state: publicConfigState, retry: retryPublicConfig } = usePublicAuthConfig();

  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        sessionStatus={state.status}
        onRetrySession={() => void refreshSession()}
        pathname="/auth/confirm"
        publicConfigState={publicConfigState}
      />
      <main id="home" tabIndex={-1}>
        <h1>Sign-in confirmation</h1>
        {state.status === "error" && (
          <SessionCheckError
            message={state.message}
            onRetry={() => void refreshSession()}
          />
        )}
        <AuthEntry mode="confirm" onContinueHome={onContinueHome} publicConfigState={publicConfigState} retryPublicConfig={retryPublicConfig} />
      </main>
    </div>
  );
}

export default function AuthConfirmRoot({
  onNavigateHome,
}: {
  onNavigateHome?: () => void;
} = {}) {
  const navigationStarted = useRef(false);
  const continueToHome = useCallback(() => {
    if (navigationStarted.current) return;
    navigationStarted.current = true;
    if (onNavigateHome) onNavigateHome();
    else window.location.assign("/");
  }, [onNavigateHome]);

  return (
    <AppSessionProvider>
      <AuthConfirmPage onContinueHome={continueToHome} />
    </AppSessionProvider>
  );
}
