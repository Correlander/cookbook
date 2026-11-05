import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import type { MenuEntryView, PrivateMenu } from "../../apiModels";
import MenuForm from "./MenuForm.tsx";
import MenuRecipePicker from "./MenuRecipePicker.tsx";
import type { MenuRecipeOption } from "./MenuRecipePicker.tsx";
import {
  addPrivateMenuEntry,
  deletePrivateMenu,
  fetchPrivateMenu,
  removePrivateMenuEntry,
  reorderPrivateMenuEntries,
  updatePrivateMenuEntry,
} from "./menuApi.ts";
import { MenuApiError } from "./menuApi.ts";

interface MenuDetailProps {
  actorID: string;
  menuID: string;
  onDeleted: (menuID: string) => void | Promise<void>;
}

interface EntryDraft {
  label: string;
  note: string;
  servingNote: string;
}

const EMPTY_DRAFT: EntryDraft = { label: "", note: "", servingNote: "" };

export default function MenuDetail({ actorID, menuID, onDeleted }: MenuDetailProps) {
  const identityKey = `${actorID}\u0000${menuID}`;
  const identityRef = useRef(identityKey);
  identityRef.current = identityKey;
  const operationRef = useRef(0);
  const orderButtonsRef = useRef(new Map<string, HTMLButtonElement>());
  const orderFocusRef = useRef<{ entryID: string; direction: "up" | "down" } | null>(null);
  const [menu, setMenu] = useState<PrivateMenu | null>(null);
  const [loadedIdentity, setLoadedIdentity] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [hasConflict, setHasConflict] = useState(false);
  const [notice, setNotice] = useState("");
  const [pending, setPending] = useState(false);
  const [editingMetadata, setEditingMetadata] = useState(false);
  const [editingEntryID, setEditingEntryID] = useState("");
  const [entryDraft, setEntryDraft] = useState<EntryDraft>(EMPTY_DRAFT);
  const [addingRecipe, setAddingRecipe] = useState<MenuRecipeOption | null>(null);
  const [addDraft, setAddDraft] = useState<EntryDraft>(EMPTY_DRAFT);

  useEffect(() => {
    const identity = identityKey;
    const operation = ++operationRef.current;
    let active = true;
    orderFocusRef.current = null;
    setMenu(null);
    setLoadedIdentity("");
    setLoading(true);
    setError("");
    setHasConflict(false);
    setNotice("");
    setPending(false);
    setEditingMetadata(false);
    setEditingEntryID("");
    setEntryDraft(EMPTY_DRAFT);
    setAddingRecipe(null);
    setAddDraft(EMPTY_DRAFT);
    if (!actorID || !menuID) {
      setLoading(false);
      setError("Sign in to view this private menu.");
      return () => { active = false; };
    }
    void fetchPrivateMenu(menuID).then((loaded) => {
      if (active && operationRef.current === operation && identityRef.current === identity && loaded.id === menuID) {
        setMenu(loaded);
        setLoadedIdentity(identity);
      }
    }).catch((loadError: unknown) => {
      if (active && operationRef.current === operation && identityRef.current === identity) {
        setError(messageFor(loadError, "Could not load this private menu."));
      }
    }).finally(() => {
      if (active && operationRef.current === operation && identityRef.current === identity) setLoading(false);
    });
    return () => { active = false; };
  }, [actorID, menuID, identityKey]);

  useEffect(() => {
    const request = orderFocusRef.current;
    if (!request || !menu) return;
    orderButtonsRef.current.get(`${request.entryID}:${request.direction}`)?.focus();
    orderFocusRef.current = null;
  }, [menu]);

  async function applyMutation(
    request: (current: PrivateMenu) => Promise<PrivateMenu>,
    success: string,
    onSuccess?: (updated: PrivateMenu) => void,
  ): Promise<boolean> {
    const current = menu;
    if (!current || pending) return false;
    const identity = identityKey;
    const operation = ++operationRef.current;
    setPending(true);
    setError("");
    setHasConflict(false);
    setNotice("");
    try {
      const authoritative = await request(current);
      if (!isCurrent(identity, operation)) return false;
      if (authoritative.id !== current.id) throw new Error("The menu service returned a different menu.");
      onSuccess?.(authoritative);
      setMenu(authoritative);
      setNotice(success);
      return true;
    } catch (mutationError: unknown) {
      if (isCurrent(identity, operation)) {
        setError(messageFor(mutationError, "Could not update this menu."));
        setHasConflict(mutationError instanceof MenuApiError && mutationError.status === 409);
      }
      return false;
    } finally {
      if (isCurrent(identity, operation)) setPending(false);
    }
  }

  function isCurrent(identity: string, operation: number): boolean {
    return identityRef.current === identity && operationRef.current === operation;
  }

  async function reload(): Promise<void> {
    if (!actorID || !menuID || pending) return;
    const identity = identityKey;
    const operation = ++operationRef.current;
    setLoading(true);
    setError("");
    setHasConflict(false);
    setNotice("");
    try {
      const loaded = await fetchPrivateMenu(menuID);
      if (isCurrent(identity, operation) && loaded.id === menuID) {
        setMenu(loaded);
        setLoadedIdentity(identity);
      }
    } catch (loadError: unknown) {
      if (isCurrent(identity, operation)) setError(messageFor(loadError, "Could not reload this menu."));
    } finally {
      if (isCurrent(identity, operation)) setLoading(false);
    }
  }

  async function reloadForMetadataConflict(): Promise<PrivateMenu> {
    const identity = identityKey;
    const operation = ++operationRef.current;
    const latest = await fetchPrivateMenu(menuID);
    if (!isCurrent(identity, operation) || latest.id !== menuID) throw new Error("The menu context changed while reloading. Try again.");
    setMenu(latest);
    setLoadedIdentity(identity);
    return latest;
  }

  async function saveEntry(event: FormEvent<HTMLFormElement>, entry: MenuEntryView): Promise<void> {
    event.preventDefault();
    const draftError = validateEntryDraft(entryDraft);
    if (draftError) { setError(draftError); return; }
    const saved = await applyMutation((current) => updatePrivateMenuEntry(current.id, entry.id, {
      // Keep unresolved targets private: an omitted recipe_id leaves the target unchanged.
      label: entryDraft.label,
      note: entryDraft.note,
      serving_note: entryDraft.servingNote,
      version: current.version,
    }), "Menu entry saved.");
    if (saved) setEditingEntryID("");
  }

  async function saveNewEntry(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const recipe = addingRecipe;
    const current = menu;
    if (!recipe || !current || pending) return;
    const draftError = validateEntryDraft(addDraft);
    if (draftError) { setError(draftError); return; }
    const identity = identityKey;
    const operation = ++operationRef.current;
    setPending(true);
    setError("");
    setHasConflict(false);
    setNotice("");
    try {
      const authoritative = await addPrivateMenuEntry(current.id, {
        recipe_id: recipe.id,
        label: addDraft.label,
        note: addDraft.note,
        serving_note: addDraft.servingNote,
        version: current.version,
      });
      if (!isCurrent(identity, operation)) return;
      if (authoritative.id !== current.id) throw new Error("The menu service returned a different menu.");
      setMenu(authoritative);
      setAddingRecipe(null);
      setAddDraft(EMPTY_DRAFT);
      setNotice("Recipe added to menu.");
    } catch (mutationError: unknown) {
      if (isCurrent(identity, operation)) {
        setError(messageFor(mutationError, "Could not add this recipe."));
        setHasConflict(mutationError instanceof MenuApiError && mutationError.status === 409);
      }
    } finally {
      if (isCurrent(identity, operation)) setPending(false);
    }
  }

  async function moveEntry(entryID: string, direction: "up" | "down"): Promise<void> {
    if (!menu || pending) return;
    const index = menu.entries.findIndex((entry) => entry.id === entryID);
    const targetIndex = index + (direction === "up" ? -1 : 1);
    if (index < 0 || targetIndex < 0 || targetIndex >= menu.entries.length) return;
    await applyMutation((current) => {
      const from = current.entries.findIndex((entry) => entry.id === entryID);
      const to = from + (direction === "up" ? -1 : 1);
      if (from < 0 || to < 0 || to >= current.entries.length) throw new Error("The menu order changed. Reload the menu before moving this entry.");
      const entryIDs = current.entries.map((entry) => entry.id);
      [entryIDs[from], entryIDs[to]] = [entryIDs[to]!, entryIDs[from]!];
      return reorderPrivateMenuEntries(current.id, { version: current.version, entry_ids: entryIDs });
    }, "Menu order saved.", () => { orderFocusRef.current = { entryID, direction: direction === "up" ? "down" : "up" }; });
  }

  async function confirmDelete(): Promise<void> {
    if (!menu || pending || !window.confirm(`Delete “${menu.title}”? This cannot be undone.`)) return;
    const current = menu;
    const identity = identityKey;
    const operation = ++operationRef.current;
    setPending(true);
    setError("");
    setHasConflict(false);
    setNotice("");
    try {
      await deletePrivateMenu(current.id, current.version);
      if (isCurrent(identity, operation)) await onDeleted(current.id);
    } catch (deleteError: unknown) {
      if (isCurrent(identity, operation)) {
        setError(messageFor(deleteError, "Could not delete this menu."));
        setHasConflict(deleteError instanceof MenuApiError && deleteError.status === 409);
      }
    } finally {
      if (isCurrent(identity, operation)) setPending(false);
    }
  }

  if (loading) return <section aria-label="Private menu"><p role="status">Loading private menu…</p></section>;
  if (!menu || loadedIdentity !== identityKey) return <section aria-label="Private menu"><p role="alert">{error || "This menu is unavailable."}</p><button className="text-link" type="button" onClick={() => void reload()} disabled={pending}>Reload menu</button></section>;

  return (
    <article aria-labelledby="private-menu-title">
      <span className="eyebrow">Private menu</span>
      <h1 id="private-menu-title">{menu.title}</h1>
      {menu.note && <p>{menu.note}</p>}
      {menu.creator_name && <p>Unverified creator name: {menu.creator_name}</p>}
      <div className="course-actions">
        <button className="text-link" type="button" onClick={() => setEditingMetadata(true)} disabled={pending}>Edit menu</button>
        <button className="text-link" type="button" onClick={() => void confirmDelete()} disabled={pending}>Delete menu</button>
      </div>
      {error && <p className="form-message" role="alert">{error}</p>}
      {hasConflict && <button className="button" type="button" onClick={() => void reload()} disabled={pending}>Reload latest menu</button>}
      {notice && <p className="sr-only" role="status" aria-live="polite" aria-atomic="true">{notice}</p>}
      <section aria-labelledby="menu-entries-heading">
        <h2 id="menu-entries-heading">Recipes</h2>
        {menu.entries.length === 0 && <p>This menu has no recipes yet.</p>}
        <ol>
          {menu.entries.map((entry, index) => {
            const entryLabel = entry.label || (entry.available ? entry.recipe.title : "unavailable recipe");
            return <li key={entry.id}>
              <h3>{entry.label || (entry.available ? entry.recipe.title : "Unavailable recipe")}</h3>
              {entry.available
                ? <a className="text-link" href={`/recipe?id=${encodeURIComponent(entry.recipe.id)}`}>{entry.recipe.title}</a>
                : <p role="status">This recipe is unavailable. Its target and content are private.</p>}
              {entry.note && <p>{entry.note}</p>}
              {entry.serving_note && <p>{entry.serving_note}</p>}
              <div className="course-actions">
                <button className="text-link" type="button" onClick={() => { setEditingEntryID(entry.id); setEntryDraft({ label: entry.label, note: entry.note, servingNote: entry.serving_note }); setError(""); }} disabled={pending}>Edit entry</button>
                <button className="text-link" type="button" onClick={() => void applyMutation((current) => removePrivateMenuEntry(current.id, entry.id, current.version), "Menu entry removed.")} disabled={pending}>Remove entry</button>
                <button ref={(element) => { if (element) orderButtonsRef.current.set(`${entry.id}:up`, element); else orderButtonsRef.current.delete(`${entry.id}:up`); }} className="text-link" type="button" onClick={() => void moveEntry(entry.id, "up")} disabled={pending || index === 0} aria-label={`Move ${entryLabel} up`}>Move up</button>
                <button ref={(element) => { if (element) orderButtonsRef.current.set(`${entry.id}:down`, element); else orderButtonsRef.current.delete(`${entry.id}:down`); }} className="text-link" type="button" onClick={() => void moveEntry(entry.id, "down")} disabled={pending || index === menu.entries.length - 1} aria-label={`Move ${entryLabel} down`}>Move down</button>
              </div>
              {editingEntryID === entry.id && <form onSubmit={(event) => void saveEntry(event, entry)}>
                <EntryFields value={entryDraft} onChange={(draft) => { setEntryDraft(draft); setError(""); }} idPrefix={`entry-${entry.id}`} />
                <button className="button button--green" type="submit" disabled={pending}>{pending ? "Saving…" : "Save entry"}</button>
                <button className="text-link" type="button" onClick={() => setEditingEntryID("")} disabled={pending}>Cancel</button>
              </form>}
            </li>;
          })}
        </ol>
      </section>
      {menu.entries.length < 64 && !addingRecipe && <MenuRecipePicker actorID={actorID} onSelect={(recipe) => { setAddingRecipe(recipe); setAddDraft(EMPTY_DRAFT); setError(""); }} />}
      {addingRecipe && <form aria-label={`Add ${addingRecipe.title}`} onSubmit={(event) => void saveNewEntry(event)}>
        <h3>Add {addingRecipe.title}</h3>
        <EntryFields value={addDraft} onChange={(draft) => { setAddDraft(draft); setError(""); }} idPrefix="new-entry" />
        <button className="button button--green" type="submit" disabled={pending}>{pending ? "Adding…" : "Add recipe to menu"}</button>
        <button className="text-link" type="button" onClick={() => { setAddingRecipe(null); setAddDraft(EMPTY_DRAFT); }} disabled={pending}>Cancel</button>
      </form>}
      {editingMetadata && <MenuForm menu={menu} onClose={() => setEditingMetadata(false)} onReloadLatest={reloadForMetadataConflict} onSaved={(updated) => { if (identityRef.current !== identityKey || updated.id !== menuID) return; setMenu(updated); setLoadedIdentity(identityKey); setEditingMetadata(false); setNotice("Menu details saved."); }} />}
      <button className="text-link" type="button" onClick={() => void reload()} disabled={pending}>Reload menu</button>
    </article>
  );
}

function EntryFields({ value, onChange, idPrefix }: { value: EntryDraft; onChange: (draft: EntryDraft) => void; idPrefix: string }) {
  return <fieldset>
    <legend>Menu entry notes</legend>
    <label htmlFor={`${idPrefix}-label`}>Label</label>
    <input id={`${idPrefix}-label`} value={value.label} aria-describedby={`${idPrefix}-label-help`} onChange={(event) => onChange({ ...value, label: event.target.value })} />
    <small id={`${idPrefix}-label-help`}>Up to 80 characters.</small>
    <label htmlFor={`${idPrefix}-note`}>Note</label>
    <textarea id={`${idPrefix}-note`} value={value.note} aria-describedby={`${idPrefix}-note-help`} onChange={(event) => onChange({ ...value, note: event.target.value })} />
    <small id={`${idPrefix}-note-help`}>Up to 2,000 characters.</small>
    <label htmlFor={`${idPrefix}-serving-note`}>Serving note</label>
    <input id={`${idPrefix}-serving-note`} value={value.servingNote} aria-describedby={`${idPrefix}-serving-note-help`} onChange={(event) => onChange({ ...value, servingNote: event.target.value })} />
    <small id={`${idPrefix}-serving-note-help`}>Up to 160 characters.</small>
  </fieldset>;
}

function validateEntryDraft(draft: EntryDraft): string {
  if (!isWithinTextBounds(draft.label, 80, 320)) return "Keep the entry label to 80 characters or fewer.";
  if (!isWithinTextBounds(draft.note, 2000, 8000)) return "Keep the entry note to 2,000 characters or fewer.";
  if (!isWithinTextBounds(draft.servingNote, 160, 640)) return "Keep the serving note to 160 characters or fewer.";
  return "";
}

function isWithinTextBounds(value: string, maxRunes: number, maxBytes: number): boolean {
  return !value.includes("\0") && Array.from(value).length <= maxRunes && new TextEncoder().encode(value).length <= maxBytes;
}

function messageFor(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}
