import { useEffect, useState } from 'react';

export default function AdminSettings() {
  const [settings, setSettings] = useState(null);
  const [publicInstance, setPublicInstance] = useState(false);
  const [registrationPolicy, setRegistrationPolicy] = useState('invite_only');
  const [confirmPublication, setConfirmPublication] = useState(false);
  const [authorized, setAuthorized] = useState(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  useEffect(() => {
    let active = true;
    fetch('/api/admin/settings')
      .then(async (response) => {
        if (response.status === 403 || response.status === 401) {
          if (active) setAuthorized(false);
          return null;
        }
        if (!response.ok) throw new Error('Could not load instance settings.');
        return response.json();
      })
      .then((data) => {
        if (!active || !data) return;
        setSettings(data);
        setPublicInstance(data.public_instance);
        setRegistrationPolicy(data.registration_policy);
        setAuthorized(true);
      })
      .catch((loadError) => {
        if (active) setError(loadError.message);
      });
    return () => { active = false; };
  }, []);

  async function save(event) {
    event.preventDefault();
    setSaving(true);
    setError('');
    setNotice('');
    try {
      const response = await fetch('/api/admin/settings', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          public_instance: publicInstance,
          registration_policy: registrationPolicy,
          confirm_publication: confirmPublication,
        }),
      });
      if (response.status === 409) {
        throw new Error('Review the existing public cookbooks and recipes, then confirm before enabling public browsing.');
      }
      if (!response.ok) throw new Error('Could not save instance settings.');
      const updated = await response.json();
      setSettings(updated);
      setPublicInstance(updated.public_instance);
      setRegistrationPolicy(updated.registration_policy);
      setConfirmPublication(false);
      setNotice('Instance settings saved.');
    } catch (saveError) {
      setError(saveError.message);
    } finally {
      setSaving(false);
    }
  }

  if (authorized === false) return null;
  if (!settings && !error) return null;

  const publicResourceCount = (settings?.public_cookbooks || 0) + (settings?.public_recipes || 0);
  const needsPublicationReview = publicInstance && !settings?.public_instance && publicResourceCount > 0;

  return (
    <section className="cookbook-dashboard" aria-labelledby="instance-settings-title">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Instance administration</span>
          <h2 id="instance-settings-title">Settings</h2>
        </div>
      </div>
      {error && <p className="form-message" role="alert">{error}</p>}
      {settings && <form className="admin-settings-form" onSubmit={save}>
        <label className="admin-settings-option" htmlFor="public-instance">
          <input
            id="public-instance"
            type="checkbox"
            checked={publicInstance}
            onChange={(event) => setPublicInstance(event.target.checked)}
          />
          <span>
            <strong>Enable public browsing</strong>
            <small>Only cookbooks and recipes explicitly marked Public can be read anonymously. Private content stays private.</small>
          </span>
        </label>
        <p className="admin-settings-note">
          Currently marked Public: {settings.public_cookbooks} cookbooks and {settings.public_recipes} active recipes.
          Turning public browsing off hides them from anonymous visitors.
        </p>
        {needsPublicationReview && <label className="admin-settings-option admin-settings-option--review" htmlFor="review-public-content">
          <input
            id="review-public-content"
            type="checkbox"
            checked={confirmPublication}
            onChange={(event) => setConfirmPublication(event.target.checked)}
          />
          <span>
            <strong>I reviewed the existing public content</strong>
            <small>Enabling public browsing will make the {publicResourceCount} listed public resources readable anonymously.</small>
          </span>
        </label>}
        <label htmlFor="registration-policy">Account registration</label>
        <select
          id="registration-policy"
          value={registrationPolicy}
          onChange={(event) => setRegistrationPolicy(event.target.value)}
        >
          <option value="invite_only">Invite only</option>
          <option value="open">Open registration</option>
          <option value="disabled">Disabled</option>
        </select>
        <p className="admin-settings-note">Courses and federation remain disabled.</p>
        <button className="button button--green" type="submit" disabled={saving || (needsPublicationReview && !confirmPublication)}>
          {saving ? 'Saving…' : 'Save instance settings'}
        </button>
      </form>}
      {notice && <p className="form-message" role="status">{notice}</p>}
    </section>
  );
}
