import type { ReactNode } from "react";
import type { AuthenticatedUser } from "../apiModels";
import type { AppSessionStatus } from "./AppSession.tsx";
import AppNavigation from "./AppNavigation.tsx";

export function AppRouteFrame({
  pathname,
  currentUser,
  sessionStatus,
  onRetrySession,
  children,
}: {
  pathname: string;
  currentUser: AuthenticatedUser | null;
  sessionStatus: AppSessionStatus;
  onRetrySession: () => void;
  children: ReactNode;
}) {
  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname={pathname}
        sessionStatus={sessionStatus}
        onRetrySession={onRetrySession}
      />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome">
          <div>
            <span className="eyebrow">Your kitchen</span>
            <h1>
              {pathname.startsWith("/admin/")
                ? "Instance administration"
                : "Account security"}
            </h1>
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

export function SignInRequired() {
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

export function AdminRequired() {
  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="admin-required-title"
    >
      <h2 id="admin-required-title">Administrator access required</h2>
      <p>This page is available to instance administrators.</p>
      <a className="text-link" href="/#home">
        Return to Discover
      </a>
    </section>
  );
}
