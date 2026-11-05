import { useCallback, useEffect, useRef, useState } from "react";
import type { AdminSettingsModel, RegistrationPolicy } from "./apiModels";

type SettingsAccessFailure = "session" | "recent-authentication" | "permission";

interface AdminSettingsProps {
  onSaved?: (settings: AdminSettingsModel) => void;
}

export default function AdminSettings({ onSaved }: AdminSettingsProps) {
  const [settings, setSettings] = useState<AdminSettingsModel | null>(null);
  const [publicInstance, setPublicInstance] = useState(false);
  const [coursesEnabled, setCoursesEnabled] = useState(false);
  const [registrationPolicy, setRegistrationPolicy] =
    useState<RegistrationPolicy>("invite_only");
  const [revisionRetention, setRevisionRetention] = useState(365);
  const [auditRetention, setAuditRetention] = useState(365);
  const [federationEnabled, setFederationEnabled] = useState(false);
  const [confirmPublication, setConfirmPublication] = useState(false);
  const [loadingSettings, setLoadingSettings] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [accessFailure, setAccessFailure] = useState<SettingsAccessFailure | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const loadSequence = useRef(0);

  const clearSettings = useCallback(() => {
    setSettings(null);
    setPublicInstance(false);
    setCoursesEnabled(false);
    setRegistrationPolicy("invite_only");
    setRevisionRetention(365);
    setAuditRetention(365);
    setFederationEnabled(false);
    setConfirmPublication(false);
    setError("");
    setNotice("");
  }, []);

  const loadSettings = useCallback(async () => {
    const request = ++loadSequence.current;
    const isCurrent = () => loadSequence.current === request;
    clearSettings();
    setAccessFailure(null);
    setLoadError("");
    setLoadingSettings(true);
    try {
      const response = await fetch("/api/admin/settings");
      if (!isCurrent()) return;
      if (response.status === 401 || response.status === 403) {
        setAccessFailure(response.status === 401 ? "session" : "permission");
        return;
      }
      if (!response.ok) throw new Error("Could not load instance settings.");
      const data = (await response.json()) as AdminSettingsModel;
      if (!isCurrent()) return;
      setSettings(data);
      setPublicInstance(data.public_instance);
      setCoursesEnabled(data.courses_enabled);
      setRegistrationPolicy(data.registration_policy);
      setRevisionRetention(data.revision_retention_days);
      setAuditRetention(data.audit_retention_days);
      setFederationEnabled(data.federation_enabled);
    } catch (loadError) {
      if (isCurrent())
        setLoadError(
          loadError instanceof Error
            ? loadError.message
            : "Could not load instance settings.",
        );
    } finally {
      if (isCurrent()) setLoadingSettings(false);
    }
  }, [clearSettings]);

  useEffect(() => {
    void loadSettings();
    return () => {
      loadSequence.current += 1;
    };
  }, [loadSettings]);

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!settings || saving) return;
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const response = await fetch("/api/admin/settings", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          public_instance: publicInstance,
          courses_enabled: coursesEnabled,
          registration_policy: registrationPolicy,
          revision_retention_days: Number(revisionRetention),
          audit_retention_days: Number(auditRetention),
          federation_enabled: federationEnabled,
          confirm_publication: confirmPublication,
        }),
      });
      let updated: (AdminSettingsModel & { error?: string }) | null = null;
      if (response.status !== 401) {
        try {
          updated = (await response.json()) as AdminSettingsModel & {
            error?: string;
          };
        } catch {
          if (response.status !== 403)
            throw new Error("Could not save instance settings.");
        }
      }
      if (response.status === 401 || response.status === 403) {
        clearSettings();
        setAccessFailure(
          response.status === 401
            ? "session"
            : updated?.error === "reauthentication_required"
              ? "recent-authentication"
              : "permission",
        );
        return;
      }
      if (!response.ok) {
        const messages = {
          reauthentication_required:
            "Sign in again before changing administrator settings.",
          publication_confirmation_required:
            "Review the existing public content, then confirm before enabling public browsing or courses.",
          federation_identity_unavailable:
            "The federation signing key is unavailable. Check the persistent key directory and its permissions.",
          federation_origin_required:
            "Set the instance’s stable HTTPS federation origin before enabling federation.",
          enabled_peer_required:
            "Add and enable at least one peer with an approved public key before enabling federation.",
        };
        const reason = updated?.error;
        if (response.status === 409 && reason === "publisher_profile_required")
          throw new Error("Some owners of content affected by this change must complete a creator profile before the audience can expand. No owner details are shown here, and these settings were not changed.");
        throw new Error(
          (reason && reason in messages
            ? messages[reason as keyof typeof messages]
            : undefined) || "Could not save instance settings.",
        );
      }
      if (!updated) throw new Error("Could not save instance settings.");
      setSettings(updated);
      setPublicInstance(updated.public_instance);
      setCoursesEnabled(updated.courses_enabled);
      setRegistrationPolicy(updated.registration_policy);
      setRevisionRetention(updated.revision_retention_days);
      setAuditRetention(updated.audit_retention_days);
      setFederationEnabled(updated.federation_enabled);
      setConfirmPublication(false);
      setNotice("Instance settings saved.");
      onSaved?.(updated);
    } catch (saveError) {
      setError(
        saveError instanceof Error
          ? saveError.message
          : "Could not save instance settings.",
      );
    } finally {
      setSaving(false);
    }
  }

  if (accessFailure) {
    const copy = {
      session: {
        title: "Settings session expired",
        message: "Your session expired. Sign in again, then reload settings before continuing.",
        showSignIn: true,
      },
      "recent-authentication": {
        title: "Recent sign-in required",
        message: "Sign in again before changing instance settings. Your draft was cleared; reload settings to start a fresh edit.",
        showSignIn: true,
      },
      permission: {
        title: "Instance settings access changed",
        message: "The server denied this request. Your draft was cleared; reload settings to check current access.",
        showSignIn: false,
      },
    }[accessFailure];
    return (
      <section className="cookbook-dashboard" aria-labelledby="settings-access-failure">
        <h2 id="settings-access-failure">{copy.title}</h2>
        <p className="form-message" role="alert">{copy.message}</p>
        {copy.showSignIn && <a className="text-link" href="/?signin=1">Sign in</a>}
        <button className="button button--green" type="button" onClick={() => void loadSettings()}>
          Reload settings
        </button>
      </section>
    );
  }
  if (loadingSettings)
    return <p className="cookbook-dashboard" role="status">Loading instance settings…</p>;
  if (loadError)
    return (
      <section className="cookbook-dashboard" aria-labelledby="settings-load-error">
        <h2 id="settings-load-error">Could not load instance settings</h2>
        <p className="form-message" role="alert">{loadError}</p>
        <button className="button button--green" type="button" onClick={() => void loadSettings()}>
          Retry settings load
        </button>
      </section>
    );
  if (!settings) return null;

  const publicResourceCount =
    (settings?.public_cookbooks || 0) +
    (settings?.public_recipes || 0) +
    (settings?.public_courses || 0);
  const enablingCoursesWithPublicContent =
    coursesEnabled &&
    !settings?.courses_enabled &&
    settings?.public_instance &&
    (settings?.public_courses || 0) > 0;
  const needsPublicationReview =
    (publicInstance && !settings?.public_instance && publicResourceCount > 0) ||
    enablingCoursesWithPublicContent;

  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="instance-settings-title"
    >
      <div className="section-heading">
        <div>
          <span className="eyebrow">Instance administration</span>
          <h2 id="instance-settings-title">Settings</h2>
        </div>
      </div>
      <nav className="instance-section-nav" aria-label="Instance settings sections">
        <a href="#visibility">Public browsing</a>
        <a href="#courses">Courses</a>
        <a href="#accounts">Accounts</a>
        <a href="#retention">Data retention</a>
        <a href="#federation">Federation</a>
      </nav>
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
      {settings && (
        <form className="admin-settings-form" onSubmit={save}>
          <section className="instance-settings-section" id="visibility" aria-labelledby="visibility-heading">
          <h3 id="visibility-heading">Public browsing</h3>
          <label className="admin-settings-option" htmlFor="public-instance">
            <input
              id="public-instance"
              type="checkbox"
              checked={publicInstance}
              onChange={(event) => setPublicInstance(event.target.checked)}
            />
            <span>
              <strong>Enable public browsing</strong>
              <small>
                Only cookbooks, recipes, and enabled courses explicitly marked
                Public can be read anonymously. Private content stays private.
              </small>
            </span>
          </label>
          <p className="admin-settings-note">
            Currently marked Public: {settings.public_cookbooks} cookbooks,{" "}
            {settings.public_recipes} active recipes, and{" "}
            {settings.public_courses} courses. Turning public browsing off hides
            them from anonymous visitors.
          </p>
          {needsPublicationReview && (
            <label
              className="admin-settings-option admin-settings-option--review"
              htmlFor="review-public-content"
            >
              <input
                id="review-public-content"
                type="checkbox"
                checked={confirmPublication}
                onChange={(event) =>
                  setConfirmPublication(event.target.checked)
                }
              />
              <span>
                <strong>I reviewed the existing public content</strong>
                <small>
                  Confirm that the {publicResourceCount} listed public resources
                  may be read anonymously under these settings.
                </small>
              </span>
            </label>
          )}
          </section>
          <section className="instance-settings-section" id="accounts" aria-labelledby="accounts-heading">
          <h3 id="accounts-heading">Accounts</h3>
          <label htmlFor="registration-policy">Account registration</label>
          <select
            id="registration-policy"
            value={registrationPolicy}
            onChange={(event) =>
              setRegistrationPolicy(event.target.value as RegistrationPolicy)
            }
          >
            <option value="invite_only">Invite only</option>
            <option value="open">Open registration</option>
            <option value="disabled">Disabled</option>
          </select>
          </section>
          <section className="instance-settings-section" id="retention" aria-labelledby="retention-heading">
          <h3 id="retention-heading">Data retention</h3>
          <label htmlFor="revision-retention">
            Recipe revision history retention
          </label>
          <select
            id="revision-retention"
            value={revisionRetention}
            onChange={(event) =>
              setRevisionRetention(Number(event.target.value))
            }
          >
            <option value="0">Unlimited</option>
            <option value="7">7 days</option>
            <option value="30">30 days</option>
            <option value="90">90 days</option>
            <option value="365">365 days</option>
          </select>
          <p className="admin-settings-note">
            Expired revisions are removed in small batches. The current recipe
            snapshot is always kept.
          </p>
          <label htmlFor="audit-retention">Security audit retention</label>
          <select
            id="audit-retention"
            value={auditRetention}
            onChange={(event) => setAuditRetention(Number(event.target.value))}
          >
            <option value="0">Unlimited</option>
            <option value="7">7 days</option>
            <option value="30">30 days</option>
            <option value="90">90 days</option>
            <option value="365">365 days</option>
          </select>
          </section>
          <section className="instance-settings-section" id="courses" aria-labelledby="courses-heading">
          <h3 id="courses-heading">Courses</h3>
          <label className="admin-settings-option" htmlFor="courses-enabled">
            <input
              id="courses-enabled"
              type="checkbox"
              checked={coursesEnabled}
              onChange={(event) => setCoursesEnabled(event.target.checked)}
            />
            <span>
              <strong>Enable courses</strong>
              <small>
                Courses are separate multi-lesson learning content. When
                disabled, course pages are hidden and course API routes are
                unavailable.
              </small>
            </span>
          </label>
          </section>
          <section className="instance-settings-section" id="federation" aria-labelledby="federation-heading">
          <h3 id="federation-heading">Federation</h3>
          <label className="admin-settings-option" htmlFor="federation-enabled">
            <input
              id="federation-enabled"
              type="checkbox"
              checked={federationEnabled}
              onChange={(event) => setFederationEnabled(event.target.checked)}
            />
            <span>
              <strong>Enable recipe federation</strong>
              <small>
                Exchange public recipe snapshots with peers you approve. A
                stable HTTPS origin and at least one enabled peer key are
                required. Private and unlisted recipes are never sent.
              </small>
            </span>
          </label>
          <p className="admin-settings-note">
            Configured origin: {settings.federation_origin || "not configured"}.
            Federation is {settings.federation_enabled ? "enabled" : "disabled"}
            .
          </p>
          </section>
          <button
            className="button button--green"
            type="submit"
            disabled={saving || (needsPublicationReview && !confirmPublication)}
          >
            {saving ? "Saving…" : "Save instance settings"}
          </button>
        </form>
      )}
      {notice && (
        <p className="form-message" role="status">
          {notice}
        </p>
      )}
    </section>
  );
}
