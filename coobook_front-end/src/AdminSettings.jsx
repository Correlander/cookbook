import { useEffect, useState } from 'react';

export default function AdminSettings({ onSaved }) {
  const [settings, setSettings] = useState(null);
  const [publicInstance, setPublicInstance] = useState(false);
  const [coursesEnabled, setCoursesEnabled] = useState(false);
  const [registrationPolicy, setRegistrationPolicy] = useState('invite_only');
  const [revisionRetention, setRevisionRetention] = useState(365);
  const [auditRetention, setAuditRetention] = useState(365);
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
        setCoursesEnabled(data.courses_enabled);
        setRegistrationPolicy(data.registration_policy);
        setRevisionRetention(data.revision_retention_days);
        setAuditRetention(data.audit_retention_days);
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
          courses_enabled: coursesEnabled,
          registration_policy: registrationPolicy,
          revision_retention_days: Number(revisionRetention),
          audit_retention_days: Number(auditRetention),
          confirm_publication: confirmPublication,
        }),
      });
      if (response.status === 409) {
        throw new Error('Review the existing public content, then confirm before enabling public browsing or courses.');
      }
      if (!response.ok) throw new Error('Could not save instance settings.');
      const updated = await response.json();
      setSettings(updated);
      setPublicInstance(updated.public_instance);
      setCoursesEnabled(updated.courses_enabled);
      setRegistrationPolicy(updated.registration_policy);
      setRevisionRetention(updated.revision_retention_days);
      setAuditRetention(updated.audit_retention_days);
      setConfirmPublication(false);
      setNotice('Instance settings saved.');
      onSaved?.(updated);
    } catch (saveError) {
      setError(saveError.message);
    } finally {
      setSaving(false);
    }
  }

  if (authorized === false) return null;
  if (!settings && !error) return null;

  const publicResourceCount = (settings?.public_cookbooks || 0) + (settings?.public_recipes || 0) + (settings?.public_courses || 0);
  const enablingCoursesWithPublicContent = coursesEnabled && !settings?.courses_enabled && settings?.public_instance && (settings?.public_courses || 0) > 0;
  const needsPublicationReview = (publicInstance && !settings?.public_instance && publicResourceCount > 0) || enablingCoursesWithPublicContent;

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
            <small>Only cookbooks, recipes, and enabled courses explicitly marked Public can be read anonymously. Private content stays private.</small>
          </span>
        </label>
        <p className="admin-settings-note">
          Currently marked Public: {settings.public_cookbooks} cookbooks, {settings.public_recipes} active recipes, and {settings.public_courses} courses.
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
            <small>Confirm that the {publicResourceCount} listed public resources may be read anonymously under these settings.</small>
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
        <label htmlFor="revision-retention">Recipe revision history retention</label>
        <select id="revision-retention" value={revisionRetention} onChange={(event) => setRevisionRetention(event.target.value)}>
          <option value="0">Unlimited</option>
          <option value="7">7 days</option>
          <option value="30">30 days</option>
          <option value="90">90 days</option>
          <option value="365">365 days</option>
        </select>
        <p className="admin-settings-note">Expired revisions are removed in small batches. The current recipe snapshot is always kept.</p>
        <label htmlFor="audit-retention">Security audit retention</label>
        <select id="audit-retention" value={auditRetention} onChange={(event) => setAuditRetention(event.target.value)}>
          <option value="0">Unlimited</option>
          <option value="7">7 days</option>
          <option value="30">30 days</option>
          <option value="90">90 days</option>
          <option value="365">365 days</option>
        </select>
        <label className="admin-settings-option" htmlFor="courses-enabled">
          <input id="courses-enabled" type="checkbox" checked={coursesEnabled} onChange={(event) => setCoursesEnabled(event.target.checked)} />
          <span>
            <strong>Enable courses</strong>
            <small>Courses are separate multi-lesson learning content. When disabled, course pages are hidden and course API routes are unavailable.</small>
          </span>
        </label>
        <p className="admin-settings-note">Federation remains disabled and has no active routes.</p>
        <button className="button button--green" type="submit" disabled={saving || (needsPublicationReview && !confirmPublication)}>
          {saving ? 'Saving…' : 'Save instance settings'}
        </button>
      </form>}
      {notice && <p className="form-message" role="status">{notice}</p>}
    </section>
  );
}
