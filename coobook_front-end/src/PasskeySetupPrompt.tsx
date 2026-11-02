import { useRef, useState } from "react";
import { registerPasskey } from "./passkeys.ts";
import useModalFocus from "./useModalFocus.ts";
import { errorMessage } from "./apiModels";

interface PasskeySetupPromptProps {
  onClose: () => void;
}

export default function PasskeySetupPrompt({
  onClose,
}: PasskeySetupPromptProps) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await registerPasskey(name.trim() || "Passkey");
      onClose();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not create this passkey."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="dialog-backdrop" role="presentation">
      <section
        ref={dialogRef}
        tabIndex={-1}
        className="signin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="passkey-prompt-title"
      >
        <span className="eyebrow">Sign in more easily</span>
        <h2 id="passkey-prompt-title">Add a passkey?</h2>
        <p>
          Use your device’s screen lock or security key next time. You can do
          this later in account security.
        </p>
        <form onSubmit={create}>
          <label htmlFor="first-passkey-name">Name this passkey</label>
          <input
            id="first-passkey-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            maxLength={80}
            placeholder="e.g. Phone"
          />
          <button
            className="button button--green"
            type="submit"
            disabled={busy}
          >
            {busy ? "Waiting…" : "Create passkey"}
          </button>
        </form>
        <button className="text-link" type="button" onClick={onClose}>
          Not now
        </button>
        {error && (
          <p className="form-message" role="alert">
            {error}
          </p>
        )}
      </section>
    </div>
  );
}
