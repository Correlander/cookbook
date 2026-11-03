import { useEffect, useRef } from "react";
import type { AuthenticatedUser } from "../apiModels";
import type { AppSessionStatus } from "./AppSession.tsx";
import useInstanceAuthority from "../features/auth/useInstanceAuthority.ts";
import useOwnerTransfers from "../features/instance/useOwnerTransfers.ts";

interface AppNavigationProps {
  currentUser: AuthenticatedUser | null;
  sessionStatus: AppSessionStatus;
  onRetrySession: () => void;
  pathname: string;
  query?: string;
  onQueryChange?: (query: string) => void;
  actionLabel?: string;
  onAction?: () => void;
}

function ChefMark() {
  return (
    <svg
      aria-hidden="true"
      width="25"
      height="25"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M6 13v7h12v-7M4 9a4 4 0 0 1 4-4 5 5 0 0 1 8 0 4 4 0 0 1 4 4c0 2-1 4-4 4H8c-3 0-4-2-4-4Z" />
      <path d="M10 20v-4h4v4" />
    </svg>
  );
}

export default function AppNavigation({
  currentUser,
  sessionStatus,
  onRetrySession,
  pathname,
  query,
  onQueryChange,
  actionLabel,
  onAction,
}: AppNavigationProps) {
  const home = pathname === "/" || pathname === "/auth/confirm";
  const authenticated = sessionStatus === "authenticated" && Boolean(currentUser);
  const authority = useInstanceAuthority(
    authenticated ? currentUser?.id ?? null : null,
    authenticated,
  );
  const ownerTransfers = useOwnerTransfers(
    authenticated ? currentUser?.id ?? null : null,
    authenticated,
    true,
  );
  const authorityForCurrentUser = authority.state.status === "ready" &&
    authority.state.userID === currentUser?.id
    ? authority.state.authority
    : null;
  const hasIncomingTransfer = ownerTransfers.state.status === "ready" &&
    ownerTransfers.state.userID === currentUser?.id &&
    ownerTransfers.state.transfers.some((transfer) => transfer.direction === "incoming");
  const canVisitInstance = Boolean(
    authenticated && currentUser &&
      (authorityForCurrentUser?.is_admin ||
        authorityForCurrentUser?.is_instance_owner ||
        authorityForCurrentUser?.can_manage_instance_roles ||
        hasIncomingTransfer),
  );
  const instanceCheckFailed = authenticated &&
    ((authority.state.status === "error" && authority.state.userID === currentUser?.id) ||
      (ownerTransfers.state.status === "error" && ownerTransfers.state.userID === currentUser?.id));
  const retryAuthorityRef = useRef(authority.retry);
  retryAuthorityRef.current = authority.retry;

  useEffect(() => {
    if (!authenticated) return;
    const checkAuthorityOnFocus = () => retryAuthorityRef.current();
    window.addEventListener("focus", checkAuthorityOnFocus);
    return () => window.removeEventListener("focus", checkAuthorityOnFocus);
  }, [authenticated]);

  function retryInstanceChecks() {
    authority.retry();
    void ownerTransfers.refresh();
  }
  return (
    <header className="topbar">
      <a href="/#home" className="brand" aria-label="Cookbook home">
        <span className="brand-mark">
          <ChefMark />
        </span>
        <span>Cookbook</span>
      </a>
      <nav className="main-nav" aria-label="Main navigation">
        <a className={home ? "main-nav__active" : undefined} href="/#home">
          Discover
        </a>
        <a
          className={pathname === "/library" || pathname === "/recipe" ? "main-nav__active" : undefined}
          href="/library"
        >
          Library
        </a>
        <a
          className={pathname === "/cookbooks" || pathname === "/cookbook" ? "main-nav__active" : undefined}
          href="/cookbooks"
        >
          Cookbooks
        </a>
        <a href="/#recipes">Recipes</a>
        {currentUser && (
          <a
            className={pathname === "/account/security" || pathname === "/account/creator-profile" ? "main-nav__active" : undefined}
            href="/account/security"
          >
            Account
          </a>
        )}
        {canVisitInstance && (
          <a
            className={pathname === "/admin" || pathname === "/admin/settings" ? "main-nav__active" : undefined}
            href="/admin"
          >
            Instance
          </a>
        )}
      </nav>
      {instanceCheckFailed && (
        <button className="text-link" type="button" onClick={retryInstanceChecks}>
          Retry Instance check
        </button>
      )}
      {onQueryChange ? (
        <label className="top-search">
          <svg
            aria-hidden="true"
            width="17"
            height="17"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.7"
            strokeLinecap="round"
          >
            <circle cx="11" cy="11" r="7" />
            <path d="m20 20-4-4" />
          </svg>
          <input
            value={query ?? ""}
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder="Search…"
            aria-label="Search recipes"
          />
        </label>
      ) : null}
      {sessionStatus === "loading" ? (
        <button className="signin-button" type="button" disabled>
          Checking session…
        </button>
      ) : sessionStatus === "error" ? (
        <button
          className="signin-button"
          type="button"
          onClick={onRetrySession}
        >
          Retry session
        </button>
      ) : actionLabel && onAction ? (
        <button className="signin-button" type="button" onClick={onAction}>
          {actionLabel}
        </button>
      ) : sessionStatus === "anonymous" ? (
        <a className="signin-button" href="/?signin=1">
          Sign in
        </a>
      ) : null}
    </header>
  );
}
