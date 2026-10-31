import { useEffect, useState } from 'react'
import { registerPasskey } from './passkeys.js'

export default function AccountSecurity() {
  const [items, setItems] = useState([])
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function reload() {
    const response = await fetch('/api/auth/passkeys')
    if (!response.ok) throw new Error('Could not load passkeys.')
    setItems(await response.json())
  }

  useEffect(() => {
    let active = true
    fetch('/api/auth/passkeys').then(async response => {
      if (!response.ok) throw new Error('Could not load passkeys.')
      const data = await response.json()
      if (active) setItems(data)
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

  return <section className="cookbook-dashboard" aria-labelledby="account-security-title">
    <div className="section-heading"><div><span className="eyebrow">Your account</span><h2 id="account-security-title">Account security</h2></div></div>
    <p>Passkeys let you sign in without a link from your email.</p>
    {items.map(item => <div className="passkey-row" key={item.id}><span>{item.name}<small>Added {new Date(item.created_at).toLocaleDateString()}{item.last_used_at ? ` · Last used ${new Date(item.last_used_at).toLocaleDateString()}` : ''}</small></span><button className="text-link" type="button" onClick={() => remove(item)}>Remove</button></div>)}
    {!items.length && !error && <p>No passkeys added yet.</p>}
    <form className="inline-invite" onSubmit={add}><label htmlFor="passkey-name">Add a passkey</label><input id="passkey-name" value={name} onChange={event => setName(event.target.value)} maxLength={80} placeholder="e.g. Kitchen laptop"/><button className="button button--green" type="submit" disabled={busy}>{busy ? 'Waiting…' : 'Add passkey'}</button></form>
    {error && <p className="form-message" role="alert">{error}</p>}
  </section>
}
