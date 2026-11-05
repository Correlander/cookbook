import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import useModalFocus from "../../useModalFocus.ts";
import type { MenuCreatorChoice, PrivateMenu } from "../../apiModels";
import {
  createPrivateMenu,
  fetchPrivateMenu,
  MenuApiError,
  updatePrivateMenu,
} from "./menuApi.ts";
import CreatorAttributionFields from "../creator/CreatorAttributionFields.tsx";

interface MenuFormProps {
  menu: PrivateMenu | null;
  onClose: () => void;
  onSaved: (menu: PrivateMenu) => void | Promise<void>;
  onReloadLatest?: () => Promise<PrivateMenu>;
}

type CreatorChoice = MenuCreatorChoice["kind"] | "";

export default function MenuForm({ menu, onClose, onSaved, onReloadLatest }: MenuFormProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [title, setTitle] = useState(menu?.title ?? "");
  const [note, setNote] = useState(menu?.note ?? "");
  const [creatorChoice, setCreatorChoice] = useState<CreatorChoice>("");
  const [creatorName, setCreatorName] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [baseMenu, setBaseMenu] = useState(menu);
  const [canReloadLatest, setCanReloadLatest] = useState(false);
  const [reloading, setReloading] = useState(false);

  useEffect(() => { setBaseMenu(menu); }, [menu]);

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const normalizedTitle = title.trim();
    const normalizedCreatorName = creatorName.trim();
    if (!isValidText(normalizedTitle, 160, 640, true)) {
      setError("Enter a menu title of up to 160 characters.");
      return;
    }
    if (!isValidText(note, 4000, 16000, false)) {
      setError("Keep the menu note to 4,000 characters or fewer.");
      return;
    }
    if (!baseMenu && !creatorChoice) {
      setError("Choose who created this menu before saving.");
      return;
    }
    if (!baseMenu && creatorChoice === "name_claim" && !isValidText(normalizedCreatorName, 80, 320, true)) {
      setError("Enter an unverified creator name of up to 80 characters.");
      return;
    }

    setSaving(true);
    setError("");
    setCanReloadLatest(false);
    try {
      const savedMenu = baseMenu
        ? await updatePrivateMenu(baseMenu.id, { title: normalizedTitle, note, version: baseMenu.version })
        : await createPrivateMenu({
            title: normalizedTitle,
            note,
            creator_attribution: creatorAttribution(creatorChoice, normalizedCreatorName),
          });
      await onSaved(savedMenu);
    } catch (saveError: unknown) {
      setError(saveError instanceof Error ? saveError.message : "Could not save this menu.");
      setCanReloadLatest(saveError instanceof MenuApiError && saveError.status === 409 && baseMenu !== null);
    } finally {
      setSaving(false);
    }
  }

  async function reloadLatestVersion(): Promise<void> {
    if (!baseMenu || saving || reloading) return;
    setReloading(true);
    setError("");
    try {
      const latest = await (onReloadLatest ? onReloadLatest() : fetchPrivateMenu(baseMenu.id));
      if (latest.id !== baseMenu.id) throw new Error("The menu service returned a different menu.");
      setBaseMenu(latest);
      setCanReloadLatest(false);
      setError("Latest menu version loaded. Your title and note draft are unchanged; review them and save again.");
    } catch (reloadError: unknown) {
      setError(reloadError instanceof Error ? reloadError.message : "Could not reload the latest menu version.");
    } finally {
      setReloading(false);
    }
  }

  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && !saving && onClose()}
    >
      <section
        ref={dialogRef}
        tabIndex={-1}
        className="signin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="menu-form-title"
      >
        <button
          className="icon-button dialog-close"
          type="button"
          aria-label="Close menu form"
          disabled={saving || reloading}
          onClick={onClose}
        >
          ×
        </button>
        <span className="eyebrow">Private menus</span>
        <h2 id="menu-form-title">{baseMenu ? "Edit menu" : "Create a private menu"}</h2>
        <form noValidate onSubmit={(event) => void submit(event)}>
          <label htmlFor="menu-title">Title</label>
          <input
            id="menu-title"
            value={title}
            onChange={(event) => { setTitle(event.target.value); setError(""); }}
            required
            aria-describedby="menu-title-help"
          />
          <p id="menu-title-help" className="dialog-note">Up to 160 characters.</p>
          <label htmlFor="menu-note">Note</label>
          <textarea
            id="menu-note"
            value={note}
            onChange={(event) => { setNote(event.target.value); setError(""); }}
            aria-describedby="menu-note-help"
          />
          <p id="menu-note-help" className="dialog-note">Optional; up to 4,000 characters.</p>
          {!baseMenu && <fieldset className="form-choice-group">
            <CreatorAttributionFields
              resourceKind="menu"
              choice={creatorChoice}
              name={creatorName}
              onChoiceChange={(choice) => { setCreatorChoice(choice); setError(""); }}
              onNameChange={(name) => { setCreatorName(name); setError(""); }}
            />
          </fieldset>}
          <p className="dialog-note">Menus are private. Creator credit is a separate choice and is never inferred from who saves the menu.</p>
          <button className="button button--green" type="submit" disabled={saving || reloading}>
            {saving ? "Saving…" : baseMenu ? "Save menu" : "Create menu"}
          </button>
        </form>
        {canReloadLatest && <button className="text-link" type="button" onClick={() => void reloadLatestVersion()} disabled={saving || reloading}>{reloading ? "Loading latest version…" : "Load latest menu version"}</button>}
        {error.startsWith("Latest menu version loaded.")
          ? <p role="status" aria-live="polite">{error}</p>
          : error && <p className="form-message" role="alert">{error}</p>}
      </section>
    </div>
  );
}

function isValidText(value: string, maxRunes: number, maxBytes: number, required: boolean): boolean {
  if (value.includes("\0") || Array.from(value).length > maxRunes || new TextEncoder().encode(value).length > maxBytes) return false;
  return !required || (value.length > 0 && value.trim().length > 0);
}

function creatorAttribution(choice: CreatorChoice, name: string): MenuCreatorChoice {
  switch (choice) {
    case "local_profile": return { kind: "local_profile" };
    case "name_claim": return { kind: "name_claim", name };
    case "unknown": return { kind: "unknown" };
    default: throw new Error("Choose who created this menu before saving.");
  }
}
