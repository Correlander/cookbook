import { useEffect, useRef, useState } from "react";
import type { AuthenticatedUser } from "../apiModels";

export default function SessionMenu({
  user,
  instanceHref,
  pathname,
  onSessionChange,
}: {
  user: AuthenticatedUser | null;
  instanceHref?: string;
  pathname: string;
  onSessionChange: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
        trigger.current?.focus();
      }
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const closeOutside = (event: PointerEvent) => {
      if (event.target instanceof Node && !menu.current?.contains(event.target))
        setOpen(false);
    };
    window.addEventListener("pointerdown", closeOutside);
    return () => window.removeEventListener("pointerdown", closeOutside);
  }, [open]);

  async function signOut() {
    setError("");
    try {
      const response = await fetch("/api/auth/logout", { method: "POST", credentials: "same-origin" });
      if (!response.ok) throw new Error("Could not sign out. Try again.");
      setOpen(false);
      onSessionChange();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Could not sign out. Try again.");
    }
  }

  if (!user)
    return <a className="signin-button" href="/?signin=1">Sign in</a>;

  const name = user.display_name || "Account";
  const initials = name.trim().split(/\s+/).slice(0, 2).map((part) => part[0]?.toUpperCase() ?? "").join("") || "A";
  return (
    <div className="session-menu" ref={menu}>
      <button
        ref={trigger}
        className="session-menu__trigger"
        type="button"
        aria-expanded={open}
        aria-controls="session-menu-panel"
        aria-label={`${name} account menu`}
        onClick={() => setOpen((current) => !current)}
      >
        <span className="session-menu__avatar" aria-hidden="true">{initials}</span>
        <span className="session-menu__name" title={name}>{name}</span>
        <span aria-hidden="true">⌄</span>
      </button>
      {open && (
        <div className="session-menu__panel" id="session-menu-panel" aria-label="Account menu">
          <span className="session-menu__identity">{user.email}</span>
          <a href="/account" aria-current={pathname.startsWith("/account") ? "page" : undefined}>Account</a>
          <a href="/account/creator-profile" aria-current={pathname === "/account/creator-profile" ? "page" : undefined}>Creator profile</a>
          {instanceHref && <a href={instanceHref} aria-current={pathname.startsWith("/admin") ? "page" : undefined}>Instance</a>}
          {error && <p role="alert">{error}</p>}
          <button type="button" onClick={() => void signOut()}>Sign out</button>
        </div>
      )}
    </div>
  );
}
