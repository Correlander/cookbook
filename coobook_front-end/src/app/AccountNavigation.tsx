export default function AccountNavigation({ pathname }: { pathname: string }) {
  const active =
    pathname === "/account/creator-profile" ? "creator-profile" : "security";
  return (
    <nav
      className="cookbook-dashboard account-subnav"
      aria-label="Account pages"
    >
      <a
        aria-current={active === "creator-profile" ? "page" : undefined}
        href="/account/creator-profile"
      >
        Creator profile
      </a>
      <a
        aria-current={active === "security" ? "page" : undefined}
        href="/account/security"
      >
        Account security
      </a>
    </nav>
  );
}
