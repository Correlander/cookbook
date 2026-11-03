import { useEffect, useState } from "react";
import { errorMessage } from "../../apiModels";
import type { AuditLogEntry } from "../../apiModels";

export default function AdminAuditLog() {
  const [authorized, setAuthorized] = useState<boolean | null>(null),
    [items, setItems] = useState<AuditLogEntry[]>([]),
    [offset, setOffset] = useState(0),
    [hasMore, setHasMore] = useState(false),
    [loadingMore, setLoadingMore] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    fetch("/api/admin/audit?limit=24&offset=0")
      .then(async (response) => {
        if (response.status === 403) {
          if (active) setAuthorized(false);
          return;
        }
        if (!response.ok)
          throw new Error("Could not load the security audit log.");
        const data = (await response.json()) as AuditLogEntry[];
        if (active) {
          setAuthorized(true);
          setItems(data);
          setOffset(data.length);
          setHasMore(data.length === 24);
        }
      })
      .catch((err: unknown) => {
        if (active)
          setError(errorMessage(err, "Could not load the security audit log."));
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
        `/api/admin/audit?limit=24&offset=${offset}`,
      );
      if (!response.ok) throw new Error("Could not load more audit events.");
      const data = (await response.json()) as AuditLogEntry[];
      setItems((current) => [...current, ...data]);
      setOffset((value) => value + data.length);
      setHasMore(data.length === 24);
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not load more audit events."));
    } finally {
      setLoadingMore(false);
    }
  }
  if (authorized === false) return null;
  if (authorized === null && !error) return null;
  return (
    <section className="cookbook-dashboard">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Instance security</span>
          <h2>Audit log</h2>
        </div>
      </div>
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {authorized && items.length === 0 && !error && (
        <p className="cookbook-empty">
          No audit events have been recorded yet.
        </p>
      )}
      {items.map((item) => (
        <div className="passkey-row" key={item.id}>
          <span>
            {item.actor_name} · {item.action} {item.resource_type}
            {item.resource_id ? `/${item.resource_id}` : ""}
            <small>
              {new Date(item.created_at).toLocaleString()} · HTTP{" "}
              {item.response_status}
            </small>
          </span>
        </div>
      ))}
      {hasMore && (
        <button
          className="text-link"
          type="button"
          onClick={loadMore}
          disabled={loadingMore}
        >
          {loadingMore ? "Loading…" : "Load more audit events"}
        </button>
      )}
    </section>
  );
}
