import { lazy } from "react";
import type { ReactNode } from "react";
import FeatureIsland from "../components/FeatureIsland.tsx";
import type { UserSummary } from "../apiModels";
import { useAppSession } from "./AppSession.tsx";
import AppNavigation from "./AppNavigation.tsx";

const AccountSecurity = lazy(() => import("../AccountSecurity.tsx"));
const AdminSettings = lazy(() => import("../AdminSettings.tsx"));

function AppRouteFrame({
  pathname,
  currentUser,
  children,
}: {
  pathname: string;
  currentUser: UserSummary | null;
  children: ReactNode;
}) {
  return (
    <div className="app-content">
      <AppNavigation currentUser={currentUser} pathname={pathname} />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome">
          <div>
            <span className="eyebrow">Your kitchen</span>
            <h1>{pathname.startsWith("/admin/") ? "Instance administration" : "Account security"}</h1>
            <p>
              {pathname.startsWith("/admin/")
                ? "Manage settings for this Cookbook instance."
                : "Review sign-in methods and your account data."}
            </p>
          </div>
        </section>
        {children}
      </main>
    </div>
  );
}

function SignInRequired() {
  return (
    <section className="cookbook-dashboard" aria-labelledby="signin-required-title">
      <h2 id="signin-required-title">Sign in to continue</h2>
      <p>This page uses your Cookbook account and browser session.</p>
      <a className="button button--green" href="/?signin=1">
        Sign in
      </a>
    </section>
  );
}

function AdminRequired() {
  return (
    <section className="cookbook-dashboard" aria-labelledby="admin-required-title">
      <h2 id="admin-required-title">Administrator access required</h2>
      <p>This page is available to instance administrators.</p>
      <a className="text-link" href="/#home">
        Return to Discover
      </a>
    </section>
  );
}

export function AccountSecurityPage() {
  const { currentUser, loading } = useAppSession();
  return (
    <AppRouteFrame pathname="/account/security" currentUser={currentUser}>
      {loading ? (
        <p className="cookbook-dashboard" role="status">Checking your session…</p>
      ) : currentUser ? (
        <FeatureIsland name="Account security" loading="account security">
          <AccountSecurity />
        </FeatureIsland>
      ) : (
        <SignInRequired />
      )}
    </AppRouteFrame>
  );
}

export function AdminSettingsPage() {
  const { currentUser, loading } = useAppSession();
  return (
    <AppRouteFrame pathname="/admin/settings" currentUser={currentUser}>
      {loading ? (
        <p className="cookbook-dashboard" role="status">Checking your session…</p>
      ) : !currentUser ? (
        <SignInRequired />
      ) : !currentUser.is_admin ? (
        <AdminRequired />
      ) : (
        <FeatureIsland name="Instance settings" loading="instance settings">
          <AdminSettings />
        </FeatureIsland>
      )}
    </AppRouteFrame>
  );
}
