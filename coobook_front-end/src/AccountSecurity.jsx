import { useEffect, useState } from 'react'
import { registerPasskey } from './passkeys.js'

export default function AccountSecurity() {
  const [items, setItems] = useState([])
  const [sessions, setSessions] = useState([])
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [importBusy, setImportBusy] = useState(false)

  async function reload() {
    const [passkeysResponse, sessionsResponse] = await Promise.all([
      fetch('/api/auth/passkeys'),
      fetch('/api/auth/sessions'),
    ])
    if (!passkeysResponse.ok || !sessionsResponse.ok) throw new Error('Could not load account security details.')
    const [passkeys, activeSessions] = await Promise.all([passkeysResponse.json(), sessionsResponse.json()])
    setItems(passkeys)
    setSessions(activeSessions)
  }

  useEffect(() => {
    let active = true
    Promise.all([fetch('/api/auth/passkeys'), fetch('/api/auth/sessions')]).then(async responses => {
      if (responses.some(response => !response.ok)) throw new Error('Could not load account security details.')
      const [passkeys, activeSessions] = await Promise.all(responses.map(response => response.json()))
      if (active) { setItems(passkeys); setSessions(activeSessions) }
    }).catch(err => { if (active) setError(err.message) })
    return () => { active = false }
  }, [])

  async function add(event) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      await registerPasskey(name.trim() || 'Passkey')
      setName('')
      await reload()
    } catch (err) { setError(err.message) } finally { setBusy(false) }
  }

  async function remove(item) {
    if (!window.confirm(`Remove the passkey “${item.name}”?`)) return
    setError('')
    try {
      const response = await fetch(`/api/auth/passkeys/${item.id}`, { method: 'DELETE' })
      if (!response.ok) throw new Error('Could not remove this passkey.')
      await reload()
    } catch (err) { setError(err.message) }
  }

  async function revokeSession(session) {
    if (!window.confirm('Sign out this browser session?')) return
    setError('')
    try {
      const response = await fetch(`/api/auth/sessions/${session.id}`, { method: 'DELETE' })
      if (!response.ok) throw new Error('Could not revoke this browser session.')
      await reload()
    } catch (err) { setError(err.message) }
  }

  return <section className="cookbook-dashboard" aria-labelledby="account-security-title">
    <div className="section-heading"><div><span className="eyebrow">Your account</span><h2 id="account-security-title">Account security</h2></div></div>
    <p>Passkeys let you sign in without a link from your email.</p>
    <h3>Browser sessions</h3>
    <p>Review your active sign-ins and revoke access from another browser.</p>
    {sessions.map(session => <div className="passkey-row" key={session.id}><span>{session.current ? 'This browser' : `Browser session · ${session.id.slice(0, 8)}`}{session.user_agent && <small className="session-agent" title={session.user_agent}>{session.user_agent}</small>}<small>{session.current ? 'Current session' : `Signed in ${new Date(session.created_at).toLocaleDateString()}`} · expires {new Date(session.expires_at).toLocaleDateString()}</small></span>{!session.current && <button className="text-link" type="button" onClick={() => revokeSession(session)}>Revoke</button>}</div>)}
    {!sessions.length && !error && <p>No active sessions.</p>}
    <h3>Passkeys</h3>
    {items.map(item => <div className="passkey-row" key={item.id}><span>{item.name}<small>Added {new Date(item.created_at).toLocaleDateString()}{item.last_used_at ? ` · Last used ${new Date(item.last_used_at).toLocaleDateString()}` : ''}</small></span><button className="text-link" type="button" onClick={() => remove(item)}>Remove</button></div>)}
    {!items.length && !error && <p>No passkeys added yet.</p>}
    <div className="account-portability"><h3>Your recipes and cookbooks</h3><p>Export your owned recipes and cookbooks, or import a previous export. Imports are private copies and do not include revision history.</p><a className="button button--green" href="/api/export">Download export</a><label htmlFor="cookbook-import">Import a Cookbook export (ZIP or legacy JSON)</label><input id="cookbook-import" type="file" accept="application/zip,.zip,application/json,.json" disabled={importBusy} onChange={async event => { const file = event.target.files?.[0]; if (!file) return; setImportBusy(true); setError(''); try { if (file.size > 100 * 1024 * 1024) throw new Error('Export files must be 100 MiB or smaller.'); const response = await fetch('/api/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: file }); if (!response.ok) throw new Error('This export could not be imported. Check that it is a valid Cookbook export.'); const result = await response.json(); window.alert(`Imported ${result.recipes} recipes and ${result.cookbooks} cookbooks as private copies.`); } catch (err) { setError(err.message) } finally { setImportBusy(false); event.target.value = '' } }} /></div>
    <form className="inline-invite" onSubmit={add}><label htmlFor="passkey-name">Add a passkey</label><input id="passkey-name" value={name} onChange={event => setName(event.target.value)} maxLength={80} placeholder="e.g. Kitchen laptop"/><button className="button button--green" type="submit" disabled={busy}>{busy ? 'Waiting…' : 'Add passkey'}</button></form>
    {error && <p className="form-message" role="alert">{error}</p>}
  </section>
}
