import { lazy } from "react";
import FeatureIsland from "../components/FeatureIsland.tsx";
import { AppSessionProvider, SessionCheckError, useAppSession } from "./AppSession.tsx";
import { AdminRequired, AppRouteFrame, SignInRequired } from "./RoutePages.tsx";

const AdminSettings = lazy(() => import("../AdminSettings.tsx"));

function AdminSettingsPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  return (
    <AppRouteFrame
      pathname="/admin/settings"
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
      ) : state.status === "anonymous" ? (
        <SignInRequired />
      ) : !currentUser?.is_admin ? (
        <AdminRequired />
      ) : (
        <FeatureIsland name="Instance settings" loading="instance settings">
          <AdminSettings />
        </FeatureIsland>
      )}
    </AppRouteFrame>
  );
}

export default function AdminSettingsRoot() {
  return (
    <AppSessionProvider>
      <AdminSettingsPage />
    </AppSessionProvider>
  );
}
