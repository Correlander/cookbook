import { useEffect, useState } from 'react';

const PAGE_SIZE = 100;

export default function AdminUsers({ currentUser }) {
  const [users, setUsers] = useState([]);
  const [authorized, setAuthorized] = useState(null);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [savingID, setSavingID] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    fetch(`/api/admin/users?limit=${PAGE_SIZE}&offset=0`)
      .then(async (response) => {
        if (response.status === 401 || response.status === 403) {
          if (active) setAuthorized(false);
          return null;
        }
        if (!response.ok) throw new Error('Could not load instance accounts.');
        return response.json();
      })
      .then((items) => {
        if (!active || !items) return;
        setUsers(items);
        setHasMore(items.length === PAGE_SIZE);
        setAuthorized(true);
      })
      .catch((loadError) => { if (active) setError(loadError.message); });
    return () => { active = false; };
  }, []);

  async function loadMore() {
    if (loadingMore) return;
    setLoadingMore(true);
    setError('');
    try {
      const response = await fetch(`/api/admin/users?limit=${PAGE_SIZE}&offset=${users.length}`);
      if (!response.ok) throw new Error('Could not load more accounts.');
      const items = await response.json();
      setUsers((current) => [...current, ...items]);
      setHasMore(items.length === PAGE_SIZE);
    } catch (loadError) {
      setError(loadError.message);
    } finally {
      setLoadingMore(false);
    }
  }

  async function setRole(account) {
    const isAdmin = !account.is_admin;
    const action = isAdmin ? 'grant administrator access to' : 'remove administrator access from';
    if (!window.confirm(`Are you sure you want to ${action} ${account.display_name} (${account.email})?`)) return;
    setSavingID(account.id);
    setError('');
    try {
      const response = await fetch(`/api/admin/users/${account.id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ is_admin: isAdmin }),
      });
      if (response.status === 409) {
        throw new Error('The last administrator must remain assigned. Ask another administrator to make this change.');
      }
      if (!response.ok) throw new Error('Could not update this account role.');
      const updated = await response.json();
      setUsers((current) => current.map((item) => (item.id === updated.id ? updated : item)));
    } catch (saveError) {
      setError(saveError.message);
    } finally {
      setSavingID('');
    }
  }

  if (authorized === false || (authorized === null && !error)) return null;
  return <section className="cookbook-dashboard" aria-labelledby="admin-users-heading">
    <div className="section-heading"><div><span className="eyebrow">Instance access</span><h2 id="admin-users-heading">Accounts</h2></div></div>
    <p className="admin-settings-note">Administrator access includes account and instance settings. Another administrator must change your role.</p>
    {error && <p className="form-message" role="alert">{error}</p>}
    {users.map((account) => <div className="passkey-row" key={account.id}>
      <span>{account.display_name}{account.id === currentUser?.id ? ' · You' : ''}<small>{account.email} · {account.is_admin ? 'Administrator' : 'Member'} · joined {new Date(account.created_at).toLocaleDateString()}</small></span>
      {account.id !== currentUser?.id && <button className="text-link" type="button" disabled={Boolean(savingID)} onClick={() => setRole(account)}>
        {savingID === account.id ? 'Saving…' : account.is_admin ? 'Remove administrator' : 'Make administrator'}
      </button>}
    </div>)}
    {hasMore && <button className="text-link" type="button" disabled={loadingMore} onClick={loadMore}>{loadingMore ? 'Loading…' : 'Load more accounts'}</button>}
  </section>;
}
