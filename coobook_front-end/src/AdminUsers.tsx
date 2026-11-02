import { useEffect, useState } from "react";
import type { AdminUser } from "./apiModels";

const PAGE_SIZE = 100;

interface AdminUsersProps {
  currentUser?: Pick<AdminUser, "id"> | null;
}

export default function AdminUsers({ currentUser }: AdminUsersProps) {
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [authorized, setAuthorized] = useState<boolean | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [savingID, setSavingID] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    fetch(`/api/admin/users?limit=${PAGE_SIZE}&offset=0`)
      .then(async (response) => {
        if (response.status === 401 || response.status === 403) {
          if (active) setAuthorized(false);
          return null;
        }
        if (!response.ok) throw new Error("Could not load instance accounts.");
        return response.json() as Promise<AdminUser[]>;
      })
      .then((items) => {
        if (!active || !items) return;
        setUsers(items);
        setHasMore(items.length === PAGE_SIZE);
        setAuthorized(true);
      })
      .catch((loadError: unknown) => {
        if (active)
          setError(
            loadError instanceof Error
              ? loadError.message
              : "Could not load instance accounts.",
          );
      });
    return () => {
      active = false;
    };
  }, []);

  async function loadMore() {
    if (loadingMore) return;
    setLoadingMore(true);
    setError("");
    try {
      const response = await fetch(
        `/api/admin/users?limit=${PAGE_SIZE}&offset=${users.length}`,
      );
      if (!response.ok) throw new Error("Could not load more accounts.");
      const items = (await response.json()) as AdminUser[];
      setUsers((current) => [...current, ...items]);
      setHasMore(items.length === PAGE_SIZE);
    } catch (loadError) {
      setError(
        loadError instanceof Error
          ? loadError.message
          : "Could not load more accounts.",
      );
    } finally {
      setLoadingMore(false);
    }
  }

  async function setRole(account: AdminUser) {
    const isAdmin = !account.is_admin;
    const action = isAdmin
      ? "grant administrator access to"
      : "remove administrator access from";
    if (
      !window.confirm(
        `Are you sure you want to ${action} ${account.display_name} (${account.email})?`,
      )
    )
      return;
    setSavingID(account.id);
    setError("");
    try {
      const response = await fetch(`/api/admin/users/${account.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ is_admin: isAdmin }),
      });
      if (response.status === 409) {
        throw new Error(
          "The last administrator must remain assigned. Ask another administrator to make this change.",
        );
      }
      if (!response.ok) {
        const failure = await response.json().catch(() => null);
        throw new Error(
          failure?.error === "reauthentication_required"
            ? "Sign in again before changing administrator access."
            : "Could not update this account role.",
        );
      }
      const updated = (await response.json()) as AdminUser;
      setUsers((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
    } catch (saveError) {
      setError(
        saveError instanceof Error
          ? saveError.message
          : "Could not update this account role.",
      );
    } finally {
      setSavingID("");
    }
  }

  if (authorized === false || (authorized === null && !error)) return null;
  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="admin-users-heading"
    >
      <div className="section-heading">
        <div>
          <span className="eyebrow">Instance access</span>
          <h2 id="admin-users-heading">Accounts</h2>
        </div>
      </div>
      <p className="admin-settings-note">
        Administrator access includes account and instance settings. Another
        administrator must change your role.
      </p>
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {users.map((account) => (
        <div className="passkey-row" key={account.id}>
          <span>
            {account.display_name}
            {account.id === currentUser?.id ? " · You" : ""}
            <small>
              {account.email} · {account.is_admin ? "Administrator" : "Member"}{" "}
              · joined {new Date(account.created_at).toLocaleDateString()}
            </small>
          </span>
          {account.id !== currentUser?.id && (
            <button
              className="text-link"
              type="button"
              disabled={Boolean(savingID)}
              onClick={() => setRole(account)}
            >
              {savingID === account.id
                ? "Saving…"
                : account.is_admin
                  ? "Remove administrator"
                  : "Make administrator"}
            </button>
          )}
        </div>
      ))}
      {hasMore && (
        <button
          className="text-link"
          type="button"
          disabled={loadingMore}
          onClick={loadMore}
        >
          {loadingMore ? "Loading…" : "Load more accounts"}
        </button>
      )}
    </section>
  );
}
