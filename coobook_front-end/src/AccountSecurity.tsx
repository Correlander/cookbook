import { useEffect, useState } from "react";
import { registerPasskey } from "./passkeys.ts";
import type { BrowserSession, Passkey } from "./apiModels";
import { errorMessage } from "./apiModels";

async function failureMessage(
  response: Response,
  fallback: string,
): Promise<string> {
  const body = (await response
    .clone()
    .json()
    .catch(() => null)) as { error?: string } | null;
  if (body?.error === "reauthentication_required")
    return "Sign in again before changing account security settings.";
  return fallback;
}

export default function AccountSecurity() {
  const [items, setItems] = useState<Passkey[]>([]);
  const [sessions, setSessions] = useState<BrowserSession[]>([]);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [importBusy, setImportBusy] = useState(false);

  async function reload() {
    const [passkeysResponse, sessionsResponse] = await Promise.all([
      fetch("/api/auth/passkeys"),
      fetch("/api/auth/sessions"),
    ]);
    if (!passkeysResponse.ok || !sessionsResponse.ok)
      throw new Error("Could not load account security details.");
    const [passkeys, activeSessions] = (await Promise.all([
      passkeysResponse.json(),
      sessionsResponse.json(),
    ])) as [Passkey[], BrowserSession[]];
    setItems(passkeys);
    setSessions(activeSessions);
  }

  useEffect(() => {
    let active = true;
    Promise.all([fetch("/api/auth/passkeys"), fetch("/api/auth/sessions")])
      .then(async (responses) => {
        if (responses.some((response) => !response.ok))
          throw new Error("Could not load account security details.");
        const [passkeys, activeSessions] = (await Promise.all(
          responses.map((response) => response.json()),
        )) as [Passkey[], BrowserSession[]];
        if (active) {
          setItems(passkeys);
          setSessions(activeSessions);
        }
      })
      .catch((err: unknown) => {
        if (active)
          setError(
            errorMessage(err, "Could not load account security details."),
          );
      });
    return () => {
      active = false;
    };
  }, []);

  async function add(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await registerPasskey(name.trim() || "Passkey");
      setName("");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not add this passkey."));
    } finally {
      setBusy(false);
    }
  }

  async function remove(item: Passkey) {
    if (!window.confirm(`Remove the passkey “${item.name}”?`)) return;
    setError("");
    try {
      const response = await fetch(`/api/auth/passkeys/${item.id}`, {
        method: "DELETE",
      });
      if (!response.ok)
        throw new Error(
          await failureMessage(response, "Could not remove this passkey."),
        );
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not remove this passkey."));
    }
  }

  async function revokeSession(session: BrowserSession) {
    if (!window.confirm("Sign out this browser session?")) return;
    setError("");
    try {
      const response = await fetch(`/api/auth/sessions/${session.id}`, {
        method: "DELETE",
      });
      if (!response.ok)
        throw new Error(
          await failureMessage(
            response,
            "Could not revoke this browser session.",
          ),
        );
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not revoke this session."));
    }
  }

  return (
    <section
      className="cookbook-dashboard"
      aria-label="Account security settings"
    >
      <span className="eyebrow">Your account</span>
      <p>Passkeys let you sign in without a link from your email.</p>
      <h3>Browser sessions</h3>
      <p>Review your active sign-ins and revoke access from another browser.</p>
      {sessions.map((session) => (
        <div className="passkey-row" key={session.id}>
          <span>
            {session.current
              ? "This browser"
              : `Browser session · ${session.id.slice(0, 8)}`}
            {session.user_agent && (
              <small className="session-agent" title={session.user_agent}>
                {session.user_agent}
              </small>
            )}
            <small>
              {session.current
                ? "Current session"
                : `Signed in ${new Date(session.created_at).toLocaleDateString()}`}{" "}
              · expires {new Date(session.expires_at).toLocaleDateString()}
            </small>
          </span>
          {!session.current && (
            <button
              className="text-link"
              type="button"
              onClick={() => revokeSession(session)}
            >
              Revoke
            </button>
          )}
        </div>
      ))}
      {!sessions.length && !error && <p>No active sessions.</p>}
      <h3>Passkeys</h3>
      {items.map((item) => (
        <div className="passkey-row" key={item.id}>
          <span>
            {item.name}
            <small>
              Added {new Date(item.created_at).toLocaleDateString()}
              {item.last_used_at
                ? ` · Last used ${new Date(item.last_used_at).toLocaleDateString()}`
                : ""}
            </small>
          </span>
          <button
            className="text-link"
            type="button"
            onClick={() => remove(item)}
          >
            Remove
          </button>
        </div>
      ))}
      {!items.length && !error && <p>No passkeys added yet.</p>}
      <div className="account-portability">
        <h3>Your recipes, cookbooks, and saved references</h3>
        <p>
          Exports include owned recipes, cookbooks, courses, and lesson text, plus saved reference identities. A lesson recipe link is included only when that recipe is in the same export; other links are omitted and can be relinked after import. Imports create private copies and do not include revision history. Saved course references remain references and are not mapped to copied courses.
        </p>
        <a className="button button--green" href="/api/export">
          Download export
        </a>
        <label htmlFor="cookbook-import">
          Import a Cookbook export (ZIP or legacy JSON)
        </label>
        <input
          id="cookbook-import"
          type="file"
          accept="application/zip,.zip,application/json,.json"
          disabled={importBusy}
          onChange={async (event: React.ChangeEvent<HTMLInputElement>) => {
            const file = event.target.files?.[0];
            if (!file) return;
            setImportBusy(true);
            setError("");
            try {
              if (file.size > 100 * 1024 * 1024)
                throw new Error("Export files must be 100 MiB or smaller.");
              const response = await fetch("/api/import", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: file,
              });
              if (!response.ok)
                throw new Error(
                  "This export could not be imported. Check that it is a valid Cookbook export.",
                );
              const result = (await response.json()) as {
                recipes: number;
                cookbooks: number;
                course_bookmarks?: number;
                courses?: number;
                course_lessons?: number;
                omitted_course_recipe_links?: number;
              };
              window.alert(
                `Imported ${result.recipes} recipes and ${result.cookbooks} cookbooks as private copies${typeof result.courses === "number" ? `, ${result.courses} courses and ${result.course_lessons ?? 0} lessons` : ""}${typeof result.omitted_course_recipe_links === "number" && result.omitted_course_recipe_links > 0 ? `; ${result.omitted_course_recipe_links} course recipe links were omitted and need relinking` : ""}${typeof result.course_bookmarks === "number" ? `, plus ${result.course_bookmarks} saved course references` : ""}.`,
              );
            } catch (err: unknown) {
              setError(errorMessage(err, "This export could not be imported."));
            } finally {
              setImportBusy(false);
              event.target.value = "";
            }
          }}
        />
      </div>
      <form className="inline-invite" onSubmit={add}>
        <label htmlFor="passkey-name">Add a passkey</label>
        <input
          id="passkey-name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={80}
          placeholder="e.g. Kitchen laptop"
        />
        <button className="button button--green" type="submit" disabled={busy}>
          {busy ? "Waiting…" : "Add passkey"}
        </button>
      </form>
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
    </section>
  );
}
