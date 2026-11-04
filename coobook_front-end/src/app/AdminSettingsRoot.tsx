import { lazy } from "react";
import FeatureIsland from "../components/FeatureIsland.tsx";
import { AppSessionProvider, SessionCheckError, useAppSession } from "./AppSession.tsx";
import useInstanceAuthority from "../features/auth/useInstanceAuthority.ts";
import { AppRouteFrame, SignInRequired } from "./RoutePages.tsx";

const AdminSettings = lazy(() => import("../AdminSettings.tsx"));

function AdminSettingsPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  const actorID = state.status === "authenticated" ? currentUser?.id ?? null : null;
  const authority = useInstanceAuthority(actorID, actorID !== null);

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
      ) : authority.state.status === "loading" ? (
        <p className="cookbook-dashboard" role="status">
          Checking instance settings access…
        </p>
      ) : authority.state.status === "error" ? (
        <section className="cookbook-dashboard" aria-labelledby="settings-authority-error">
          <h2 id="settings-authority-error">Could not check instance settings access</h2>
          <p className="form-message" role="alert">{authority.state.message}</p>
          <button className="button button--green" type="button" onClick={authority.retry}>
            Retry permissions
          </button>
        </section>
      ) : authority.state.status === "ready" &&
        typeof authority.state.authority.can_manage_instance_settings !== "boolean" ? (
        <section className="cookbook-dashboard" aria-labelledby="settings-authority-unavailable">
          <h2 id="settings-authority-unavailable">Instance settings access unavailable</h2>
          <p>
            This server did not report the instance settings capability. Settings are unavailable
            until the server provides it and permissions can be checked again.
          </p>
          <button className="button button--green" type="button" onClick={authority.retry}>
            Retry permissions
          </button>
        </section>
      ) : authority.state.status === "ready" &&
        authority.state.authority.can_manage_instance_settings !== true ? (
        <section className="cookbook-dashboard" aria-labelledby="settings-authority-denied">
          <h2 id="settings-authority-denied">Instance settings access required</h2>
          <p>Instance settings are available to the instance owner and administrators.</p>
          <a className="text-link" href="/admin">Return to instance access</a>
        </section>
      ) : authority.state.status === "ready" &&
        authority.state.authority.can_manage_instance_settings === true ? (
        <FeatureIsland name="Instance settings" loading="instance settings">
          <AdminSettings />
        </FeatureIsland>
      ) : null}
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
