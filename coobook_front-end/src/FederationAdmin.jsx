import { useCallback, useEffect, useState } from 'react';

async function federationRequest(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers },
  });
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    if (response.status === 401 || data?.error === 'reauthentication_required') throw new Error('Reauthenticate before changing federation settings.');
    if (response.status === 403) throw new Error('Administrator access is required.');
    const messages = {
      active_peer_key_required: 'Add an active public key before enabling this peer.',
      peer_not_found: 'Create the peer before adding its key.',
      invalid_peer_key: 'Check the peer origin, key ID, and 32-byte Ed25519 public key.',
      federation_rotation_already_pending: 'A signing-key rotation is already staged.',
      pending_federation_key_required: 'Stage a new signing key before activating it.',
      invalid_rotation_action: 'Choose a valid signing-key action.',
    };
    throw new Error(messages[data?.error] || `Federation request failed (${response.status}).`);
  }
  return data;
}

export default function FederationAdmin() {
  const [identity, setIdentity] = useState(null);
  const [peers, setPeers] = useState([]);
  const [deliveries, setDeliveries] = useState([]);
  const [origin, setOrigin] = useState('');
  const [keyID, setKeyID] = useState('');
  const [publicKey, setPublicKey] = useState('');
  const [selectedDelivery, setSelectedDelivery] = useState(null);
  const [busy, setBusy] = useState(false);
  const [authorized, setAuthorized] = useState(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const refresh = useCallback(async () => {
    const [localIdentity, configuredPeers, outbox] = await Promise.all([
      federationRequest('/api/admin/federation/identity'),
      federationRequest('/api/admin/federation/peers'),
      federationRequest('/api/admin/federation/outbox'),
    ]);
    setIdentity(localIdentity);
    setPeers(configuredPeers);
    setDeliveries(outbox);
    setAuthorized(true);
  }, []);

  useEffect(() => {
    let current = true;
    Promise.all([
      federationRequest('/api/admin/federation/identity'),
      federationRequest('/api/admin/federation/peers'),
      federationRequest('/api/admin/federation/outbox'),
    ]).then(([localIdentity, configuredPeers, outbox]) => {
      if (!current) return;
      setIdentity(localIdentity);
      setPeers(configuredPeers);
      setDeliveries(outbox);
      setAuthorized(true);
    }).catch((loadError) => {
      if (!current) return;
      if (loadError.message === 'Administrator access is required.') setAuthorized(false);
      else setError(loadError.message);
    });
    return () => { current = false; };
  }, []);

  if (authorized === false || (authorized === null && !error)) return null;

  async function run(action, successMessage) {
    setBusy(true);
    setError('');
    setNotice('');
    try {
      await action();
      await refresh();
      setNotice(successMessage);
    } catch (actionError) {
      if (actionError.message === 'Administrator access is required.') setAuthorized(false);
      setError(actionError.message);
    } finally {
      setBusy(false);
    }
  }

  function addPeer(event) {
    event.preventDefault();
    run(async () => {
      await federationRequest('/api/admin/federation/peers', { method: 'POST', body: JSON.stringify({ origin, enabled: false }) });
      await federationRequest('/api/admin/federation/keys', { method: 'POST', body: JSON.stringify({ origin, key_id: keyID, public_key: publicKey }) });
      await federationRequest('/api/admin/federation/peers', { method: 'POST', body: JSON.stringify({ origin, enabled: true }) });
      setOrigin('');
      setKeyID('');
      setPublicKey('');
    }, 'Peer approved and enabled.');
  }

  function addRotatedKey(peerOrigin, rotatedKeyID, rotatedPublicKey) {
    run(async () => {
      await federationRequest('/api/admin/federation/keys', { method: 'POST', body: JSON.stringify({ origin: peerOrigin, key_id: rotatedKeyID, public_key: rotatedPublicKey }) });
    }, 'Peer key added. The previous key remains active until revoked.');
  }

  return (
    <section className="cookbook-dashboard" aria-labelledby="federation-admin-title">
      <div className="section-heading">
        <div><span className="eyebrow">Federation administration</span><h2 id="federation-admin-title">Trusted peers</h2></div>
      </div>
      {error && <p className="form-message" role="alert">{error}</p>}
      {notice && <p className="form-message" role="status">{notice}</p>}
      {identity && <details className="admin-settings-note">
        <summary>Your public signing identity</summary>
        <p>Origin: {identity.origin || 'Configure COOKBOOK_FEDERATION_ORIGIN before enabling federation.'}</p>
        <p>Key ID: <code>{identity.key_id}</code></p>
        <label htmlFor="federation-public-key">Ed25519 public key</label>
        <textarea id="federation-public-key" readOnly value={identity.public_key} rows={2} />
        <small>Share this public key with a peer administrator. The private key stays in the server’s persistent secret directory.</small>
        {identity.pending_key_id ? <div>
          <p>Staged rotation key: <code>{identity.pending_key_id}</code></p>
          <label htmlFor="federation-pending-public-key">Pending Ed25519 public key</label>
          <textarea id="federation-pending-public-key" readOnly value={identity.pending_public_key} rows={2} />
          <p>Add this key at every peer before activation. Their old key can remain active during the overlap and be revoked after they confirm the new key works.</p>
          <button className="button button--green" type="button" disabled={busy} onClick={() => run(
            () => federationRequest('/api/admin/federation/identity/rotation', { method: 'POST', body: JSON.stringify({ action: 'activate' }) }),
            'Signing key activated. Confirm delivery at each peer, then revoke the old key there.',
          )}>Activate staged key</button>
        </div> : <button className="text-link" type="button" disabled={busy} onClick={() => run(
          () => federationRequest('/api/admin/federation/identity/rotation', { method: 'POST', body: JSON.stringify({ action: 'stage' }) }),
          'A new signing key is staged. Add its public key at every peer before activation.',
        )}>Stage signing-key rotation</button>}
      </details>}
      <form className="admin-settings-form" onSubmit={addPeer}>
        <h3>Add a trusted peer</h3>
        <label htmlFor="federation-peer-origin">Peer HTTPS origin</label>
        <input id="federation-peer-origin" required value={origin} onChange={(event) => setOrigin(event.target.value)} placeholder="https://cookbook.example.org" />
        <label htmlFor="federation-peer-key-id">Peer key ID</label>
        <input id="federation-peer-key-id" required value={keyID} onChange={(event) => setKeyID(event.target.value)} />
        <label htmlFor="federation-peer-public-key">Peer Ed25519 public key (base64)</label>
        <input id="federation-peer-public-key" required value={publicKey} onChange={(event) => setPublicKey(event.target.value)} />
        <button className="button button--green" type="submit" disabled={busy}>Approve and enable peer</button>
      </form>
      <div className="section-heading"><div><span className="eyebrow">Approved origins and verification keys</span><h3>Peers</h3></div></div>
      {peers.length === 0 ? <p className="empty-state">No peers have been configured.</p> : peers.map((peer) => (
        <details className="admin-settings-note" key={peer.origin}>
          <summary>{peer.origin} · {peer.enabled ? 'enabled' : 'disabled'}</summary>
          {peer.keys.map((key) => <div key={key.key_id}>
            <p><code>{key.key_id}</code> · {key.status}</p>
            {key.status === 'active' && <button className="text-link" type="button" disabled={busy} onClick={() => run(() => federationRequest('/api/admin/federation/keys', { method: 'DELETE', body: JSON.stringify({ origin: peer.origin, key_id: key.key_id }) }), 'Peer key revoked.')}>Revoke key</button>}
          </div>)}
          <form onSubmit={(event) => { event.preventDefault(); const form = new FormData(event.currentTarget); addRotatedKey(peer.origin, form.get('key_id'), form.get('public_key')); event.currentTarget.reset(); }}>
            <label>Next key ID <input name="key_id" required /></label>
            <label>Next Ed25519 public key (base64) <input name="public_key" required /></label>
            <button className="text-link" type="submit" disabled={busy}>Add rotation key</button>
          </form>
          <button className="text-link" type="button" disabled={busy} onClick={() => run(async () => {
            if (peer.enabled) await federationRequest('/api/admin/federation/peers', { method: 'DELETE', body: JSON.stringify({ origin: peer.origin }) });
            else await federationRequest('/api/admin/federation/peers', { method: 'POST', body: JSON.stringify({ origin: peer.origin, enabled: true }) });
          }, peer.enabled ? 'Peer disabled.' : 'Peer enabled.')}>{peer.enabled ? 'Disable peer' : 'Enable peer'}</button>
        </details>
      ))}
      <div className="section-heading"><div><span className="eyebrow">Bounded durable delivery</span><h3>Outbox</h3></div><button className="text-link" type="button" disabled={busy} onClick={() => run(refresh, 'Outbox refreshed.')}>Refresh</button></div>
      {deliveries.length === 0 ? <p className="empty-state">No queued deliveries.</p> : deliveries.map((delivery) => (
        <details className="admin-settings-note" key={delivery.id}>
          <summary>{delivery.peer_origin} · sequence {delivery.sequence} · {delivery.attempts}/12 attempts</summary>
          <p>{delivery.last_error || 'Waiting for delivery.'}</p>
          <button className="text-link" type="button" disabled={busy} onClick={async () => {
            try { setSelectedDelivery(await federationRequest(`/api/admin/federation/outbox?id=${encodeURIComponent(delivery.id)}`)); setError(''); }
            catch (detailError) { setError(detailError.message); }
          }}>View event</button>
          <button className="text-link" type="button" disabled={busy} onClick={() => run(() => federationRequest('/api/admin/federation/outbox', { method: 'POST', body: JSON.stringify({ id: delivery.id }) }), 'Delivery retry scheduled.')}>Retry</button>
          <button className="text-link" type="button" disabled={busy} onClick={() => {
            if (!window.confirm('Discard every queued event for this recipe across all peers? A peer may keep the last copy it received.')) return;
            run(() => federationRequest('/api/admin/federation/outbox', { method: 'DELETE', body: JSON.stringify({ id: delivery.id }) }), 'Queued object events discarded.');
          }}>Discard object events</button>
        </details>
      ))}
      {selectedDelivery && <pre className="admin-settings-note" aria-label="Selected federation event">{JSON.stringify(selectedDelivery.payload, null, 2)}</pre>}
    </section>
  );
}
