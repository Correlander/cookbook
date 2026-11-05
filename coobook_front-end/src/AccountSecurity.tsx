import { useEffect, useState } from "react";
import { registerPasskey } from "./passkeys.ts";
import type { BrowserSession, Passkey } from "./apiModels";
import { errorMessage } from "./apiModels";

interface ImportSummary {
  recipes: number;
  cookbooks: number;
  courses?: number;
  course_lessons?: number;
  omitted_course_recipe_links?: number;
  course_bookmarks?: number;
  snapshot_copies?: number;
  snapshot_references?: number;
  menus?: number;
  omitted_menu_recipe_links?: number;
}

function parseImportSummary(value: unknown): ImportSummary | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const recipes = record.recipes;
  const cookbooks = record.cookbooks;
  if (!isCount(recipes) || !isCount(cookbooks)) return null;

  const countFields = [
    "courses",
    "course_lessons",
    "omitted_course_recipe_links",
    "course_bookmarks",
    "snapshot_copies",
    "snapshot_references",
    "menus",
    "omitted_menu_recipe_links",
  ] as const;
  for (const field of countFields) {
    if (field in record && !isCount(record[field])) return null;
  }

  const courses = optionalCount(record, "courses");
  const courseLessons = optionalCount(record, "course_lessons");
  const omittedCourseLinks = optionalCount(record, "omitted_course_recipe_links");
  const courseBookmarks = optionalCount(record, "course_bookmarks");
  const snapshotCopies = optionalCount(record, "snapshot_copies");
  const snapshotReferences = optionalCount(record, "snapshot_references");
  const menus = optionalCount(record, "menus");
  const omittedMenuLinks = optionalCount(record, "omitted_menu_recipe_links");

  return {
    recipes,
    cookbooks,
    ...(courses === undefined ? {} : { courses }),
    ...(courseLessons === undefined ? {} : { course_lessons: courseLessons }),
    ...(omittedCourseLinks === undefined ? {} : { omitted_course_recipe_links: omittedCourseLinks }),
    ...(courseBookmarks === undefined ? {} : { course_bookmarks: courseBookmarks }),
    ...(snapshotCopies === undefined ? {} : { snapshot_copies: snapshotCopies }),
    ...(snapshotReferences === undefined ? {} : { snapshot_references: snapshotReferences }),
    ...(menus === undefined ? {} : { menus }),
    ...(omittedMenuLinks === undefined ? {} : { omitted_menu_recipe_links: omittedMenuLinks }),
  };
}

function isCount(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function optionalCount(record: Record<string, unknown>, field: string): number | undefined {
  const value = record[field];
  return isCount(value) ? value : undefined;
}

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
  const [preservePersonalSnapshots, setPreservePersonalSnapshots] = useState(false);

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
        <h3>Your content and saved references</h3>
        <p>
          Exports include owned recipes, cookbooks, courses, and lesson text, plus saved reference identities. Backends that support menu portability also include your owned private menus. Recipe links are included only when their recipes are in the same export; other links are omitted and can be relinked after import. Imports create private copies and do not include revision history. Saved course references remain references and are not mapped to copied courses.
        </p>
        <a className="button button--green" href="/api/export">
          Download export
        </a>
        <p>
          Exports stop if a saved copy is restricted or removed; the server will
          show that error without substituting another copy.
        </p>
        <label htmlFor="cookbook-import">
          Import a Cookbook export (ZIP or legacy JSON)
        </label>
        <div>
          <input
            id="preserve-personal-snapshots"
            type="checkbox"
            checked={preservePersonalSnapshots}
            disabled={importBusy}
            onChange={(event) => setPreservePersonalSnapshots(event.target.checked)}
          />
          <label htmlFor="preserve-personal-snapshots">
            I am restoring my own personal account and want to preserve saved
            private copies from this export
          </label>
          <p>
            Preserved copies remain private and unverified. This choice does not
            prove source permission, authorship, or account identity. Leave this
            unchecked to import saved copies as references only.
          </p>
        </div>
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
              const headers: Record<string, string> = {
                "Content-Type": "application/json",
              };
              if (preservePersonalSnapshots)
                headers["X-Cookbook-Preserve-Personal-Snapshots"] = "true";
              const response = await fetch("/api/import", {
                method: "POST",
                headers,
                body: file,
              });
              if (!response.ok) {
                const body = (await response.clone().json().catch(() => null)) as
                  | { error?: string }
                  | null;
                if (body?.error === "snapshot_already_pinned")
                  throw new Error(
                    "A saved copy already exists in this account. The import stopped without replacing it.",
                  );
                if (body?.error === "snapshot_restricted")
                  throw new Error(
                    "A restricted saved copy already exists in this account. The import stopped without substituting a reference.",
                  );
                if (body?.error === "snapshot_removed")
                  throw new Error(
                    "A removed saved copy already exists in this account. The import stopped without substituting a reference.",
                  );
                throw new Error(
                  "This export could not be imported. Check that it is a valid Cookbook export.",
                );
              }
              let summary: ImportSummary | null = null;
              try {
                summary = parseImportSummary(await response.json());
              } catch {
                // The import may already have committed; do not invite a retry.
              }
              if (!summary) {
                window.alert(
                  "Import completed, but its summary could not be read. Check your Library before importing again.",
                );
                return;
              }
              const result = summary;
              const snapshotSummary =
                Number.isInteger(result.snapshot_copies) &&
                Number.isInteger(result.snapshot_references)
                  ? `; ${result.snapshot_copies} private saved ${result.snapshot_copies === 1 ? "copy" : "copies"} preserved as unverified and ${result.snapshot_references} saved ${result.snapshot_references === 1 ? "reference" : "references"} imported as references`
                  : "";
              const menuSummary =
                result.menus === undefined
                  ? ""
                  : `, ${result.menus} private ${result.menus === 1 ? "menu" : "menus"} imported`;
              const omittedMenuLinksSummary =
                result.omitted_menu_recipe_links === undefined
                  ? ""
                  : result.omitted_menu_recipe_links === 0
                    ? "; no menu recipe links were omitted"
                    : `; ${result.omitted_menu_recipe_links} menu recipe ${result.omitted_menu_recipe_links === 1 ? "link was" : "links were"} omitted and need relinking`;
              window.alert(
                `Imported ${result.recipes} recipes and ${result.cookbooks} cookbooks as private copies${typeof result.courses === "number" ? `, ${result.courses} courses and ${result.course_lessons ?? 0} lessons` : ""}${typeof result.omitted_course_recipe_links === "number" && result.omitted_course_recipe_links > 0 ? `; ${result.omitted_course_recipe_links} course recipe links were omitted and need relinking` : ""}${typeof result.course_bookmarks === "number" ? `, plus ${result.course_bookmarks} saved course references` : ""}${menuSummary}${omittedMenuLinksSummary}${snapshotSummary}.`,
              );
            } catch (err: unknown) {
              setError(errorMessage(err, "This export could not be imported."));
            } finally {
              setImportBusy(false);
              setPreservePersonalSnapshots(false);
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
