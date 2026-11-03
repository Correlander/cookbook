import type { UserSummary } from "../apiModels";

interface AppNavigationProps {
  currentUser: UserSummary | null;
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
  pathname,
  query,
  onQueryChange,
  actionLabel,
  onAction,
}: AppNavigationProps) {
  const home = pathname === "/" || pathname === "/auth/confirm";
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
        <a href="/#cookbooks">Cookbooks</a>
        <a href="/#recipes">Recipes</a>
        {currentUser && (
          <a
            className={pathname === "/account/security" ? "main-nav__active" : undefined}
            href="/account/security"
          >
            Account
          </a>
        )}
        {currentUser?.is_admin && (
          <a
            className={pathname === "/admin/settings" ? "main-nav__active" : undefined}
            href="/admin/settings"
          >
            Administration
          </a>
        )}
      </nav>
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
      {actionLabel && onAction ? (
        <button className="signin-button" type="button" onClick={onAction}>
          {actionLabel}
        </button>
      ) : !currentUser ? (
        <a className="signin-button" href="/?signin=1">
          Sign in
        </a>
      ) : null}
    </header>
  );
}
