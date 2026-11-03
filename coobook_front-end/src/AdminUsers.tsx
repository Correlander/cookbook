import { useEffect, useRef, useState } from "react";
import type { AdminUser } from "./apiModels";
import type { InstanceAuthorityState } from "./features/auth/useInstanceAuthority";

const PAGE_SIZE = 100;

interface AdminUsersProps {
  currentUser?: Pick<AdminUser, "id"> | null;
  authorityState: InstanceAuthorityState;
  onRetryAuthority: () => void;
  onRoleChanged: () => void;
}

export default function AdminUsers({
  currentUser,
  authorityState,
  onRetryAuthority,
  onRoleChanged,
}: AdminUsersProps) {
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [authorized, setAuthorized] = useState<boolean | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [savingID, setSavingID] = useState("");
  const [error, setError] = useState("");
  const [usersForUserID, setUsersForUserID] = useState("");
  const listRequestID = useRef(0);
  const userID = currentUser?.id ?? "";
  const currentListReady = usersForUserID === userID;
  const visibleUsers = currentListReady ? users : [];
  const canManageRoles = Boolean(
    currentUser &&
      authorityState.status === "ready" &&
      authorityState.userID === currentUser.id &&
      authorityState.authority.can_manage_instance_roles,
  );

  useEffect(() => {
    const requestID = ++listRequestID.current;
    setUsers([]);
    setUsersForUserID("");
    setAuthorized(null);
    setHasMore(false);
    setLoadingMore(false);
    setError("");
    let active = true;
    fetch(`/api/admin/users?limit=${PAGE_SIZE}&offset=0`)
      .then(async (response) => {
        if (response.status === 401 || response.status === 403) {
          if (active && listRequestID.current === requestID)
            setAuthorized(false);
          return null;
        }
        if (!response.ok) throw new Error("Could not load instance accounts.");
        return response.json() as Promise<AdminUser[]>;
      })
      .then((items) => {
        if (!active || listRequestID.current !== requestID || !items) return;
        setUsers(items);
        setUsersForUserID(userID);
        setHasMore(items.length === PAGE_SIZE);
        setAuthorized(true);
      })
      .catch((loadError: unknown) => {
        if (active && listRequestID.current === requestID)
          setError(
            loadError instanceof Error
              ? loadError.message
              : "Could not load instance accounts.",
          );
      });
    return () => {
      active = false;
      if (listRequestID.current === requestID)
        listRequestID.current += 1;
    };
  }, [userID]);

  async function loadMore() {
    if (loadingMore || !currentListReady || !userID) return;
    const requestID = listRequestID.current;
    setLoadingMore(true);
    setError("");
    try {
      const response = await fetch(
        `/api/admin/users?limit=${PAGE_SIZE}&offset=${users.length}`,
      );
      if (!response.ok) throw new Error("Could not load more accounts.");
      const items = (await response.json()) as AdminUser[];
      if (listRequestID.current !== requestID) return;
      setUsers((current) => [...current, ...items]);
      setHasMore(items.length === PAGE_SIZE);
    } catch (loadError) {
      if (listRequestID.current === requestID)
        setError(
          loadError instanceof Error
            ? loadError.message
            : "Could not load more accounts.",
        );
    } finally {
      if (listRequestID.current === requestID) setLoadingMore(false);
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
          "At least one administrator must remain assigned. Ask another eligible account to make this change.",
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
      onRoleChanged();
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

  if (
    authorized === false ||
    (!currentListReady && !error) ||
    (authorized === null && !error)
  )
    return null;
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
        {canManageRoles
          ? "You can change account roles under the current instance policy."
          : authorityState.status === "ready" &&
              authorityState.userID === userID
            ? "This account cannot change roles under the current instance authority."
            : "Role permissions are not confirmed yet; role controls are unavailable while they are checked."}
      </p>
      {authorityState.status === "loading" && (
        <p role="status">Checking role permissions…</p>
      )}
      {authorityState.status === "error" && (
        <div>
          <p className="form-message" role="alert">
            {authorityState.message}
          </p>
          <button
            className="text-link"
            type="button"
            onClick={onRetryAuthority}
          >
            Retry role permissions
          </button>
        </div>
      )}
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {visibleUsers.map((account) => (
        <div className="passkey-row" key={account.id}>
          <span>
            {account.display_name}
            {account.id === currentUser?.id ? " · You" : ""}
            <small>
              {account.email} · {account.is_admin ? "Administrator" : "Member"}{" "}
              · joined {new Date(account.created_at).toLocaleDateString()}
            </small>
          </span>
          {account.id !== currentUser?.id &&
            canManageRoles && (
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
      {currentListReady && hasMore && (
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
