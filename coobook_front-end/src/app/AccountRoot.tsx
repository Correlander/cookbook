import { AppSessionProvider, SessionCheckError, useAppSession } from "./AppSession.tsx";
import AppNavigation from "./AppNavigation.tsx";
import AccountNavigation from "./AccountNavigation.tsx";
import { SignInRequired } from "./RoutePages.tsx";

function AccountPage() {
  const { state, currentUser, refreshSession } = useAppSession();
  return <div className="app-content">
    <AppNavigation currentUser={currentUser} pathname="/account" sessionStatus={state.status} onRetrySession={() => void refreshSession()} />
    <main id="home" tabIndex={-1}>
      <section className="dashboard-welcome"><div><span className="eyebrow">Your kitchen</span><h1>Account</h1><p>Manage your creator profile and account security.</p></div></section>
      {state.status === "loading" ? <p className="cookbook-dashboard" role="status">Checking your session…</p> :
        state.status === "error" ? <SessionCheckError message={state.message} onRetry={() => void refreshSession()} /> :
          state.status === "anonymous" ? <SignInRequired /> : <>
            <AccountNavigation pathname="/account" />
            <section className="account-overview" aria-label="Account settings">
              <a className="account-overview__card" href="/account/creator-profile"><span className="eyebrow">Profile</span><h2>Creator profile</h2><p>Choose a public name, write a bio, and manage profile visibility.</p><span className="text-link">Manage creator profile</span></a>
              <a className="account-overview__card" href="/account/security"><span className="eyebrow">Sign-in and data</span><h2>Account security</h2><p>Review sign-in methods, browser sessions, and personal data.</p><span className="text-link">Manage account security</span></a>
            </section>
          </>}
    </main>
  </div>;
}

export default function AccountRoot() { return <AppSessionProvider><AccountPage /></AppSessionProvider>; }
