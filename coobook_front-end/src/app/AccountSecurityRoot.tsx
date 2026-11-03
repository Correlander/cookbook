import { lazy } from "react";
import FeatureIsland from "../components/FeatureIsland.tsx";
import { AppSessionProvider, SessionCheckError, useAppSession } from "./AppSession.tsx";
import AccountNavigation from "./AccountNavigation.tsx";
import { AppRouteFrame, SignInRequired } from "./RoutePages.tsx";

const AccountSecurity = lazy(() => import("../AccountSecurity.tsx"));

function AccountSecurityPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  return (
    <AppRouteFrame
      pathname="/account/security"
      currentUser={currentUser}
      sessionStatus={state.status}
      onRetrySession={() => void refreshSession()}
    >
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
        <>
          <AccountNavigation pathname="/account/security" />
          <FeatureIsland name="Account security" loading="account security">
            <AccountSecurity />
          </FeatureIsland>
        </>
      ) : (
        <SignInRequired />
      )}
    </AppRouteFrame>
  );
}

export default function AccountSecurityRoot() {
  return (
    <AppSessionProvider>
      <AccountSecurityPage />
    </AppSessionProvider>
  );
}
