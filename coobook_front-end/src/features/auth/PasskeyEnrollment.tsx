import { useEffect, useRef, useState } from "react";
import { enrollPasskeyWithGrant } from "../../passkeys.ts";

type EnrollmentStatus = "idle" | "busy" | "success" | "error" | "cancelled";

const unavailableMessage =
  "Passkeys need a supported browser and a secure connection. Open this page on a supported device over HTTPS, then try again.";
const recoveryMessage =
  "That enrollment code could not be used. It may be invalid, expired, or already used. Ask your Cookbook operator for a new code and try again.";
const cancelledMessage =
  "Setup was cancelled or its result could not be confirmed. Try signing in with your passkey; if that does not work, ask your Cookbook operator for a new code.";

function isAbortError(error: unknown): boolean {
  return (
    error instanceof DOMException &&
    (error.name === "AbortError" || error.name === "NotAllowedError")
  );
}

export default function PasskeyEnrollment() {
  const [token, setToken] = useState("");
  const [status, setStatus] = useState<EnrollmentStatus>("idle");
  const [feedback, setFeedback] = useState("");
  const abortController = useRef<AbortController | null>(null);
  const successHeading = useRef<HTMLHeadingElement>(null);

  useEffect(
    () => () => {
      abortController.current?.abort();
      abortController.current = null;
    },
    [],
  );

  useEffect(() => {
    if (status === "success") successHeading.current?.focus();
  }, [status]);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (status === "busy") return;

    const grant = token.trim();
    if (!grant) return;

    const controller = new AbortController();
    abortController.current = controller;
    setStatus("busy");
    setFeedback("");

    try {
      await enrollPasskeyWithGrant(grant, controller.signal);
      if (controller.signal.aborted) return;
      setToken("");
      setStatus("success");
    } catch (error: unknown) {
      if (controller.signal.aborted) return;
      setToken("");
      if (isAbortError(error)) {
        setStatus("cancelled");
        setFeedback(cancelledMessage);
      } else if (
        error instanceof Error &&
        error.message ===
          "Passkeys require a supported browser and a secure connection."
      ) {
        setStatus("error");
        setFeedback(unavailableMessage);
      } else {
        setStatus("error");
        setFeedback(recoveryMessage);
      }
    } finally {
      if (abortController.current === controller) {
        abortController.current = null;
      }
    }
  }

  function cancel() {
    abortController.current?.abort();
    abortController.current = null;
    setToken("");
    setStatus("cancelled");
    setFeedback(cancelledMessage);
  }

  if (status === "success") {
    return (
      <main
        id="home"
        className="mx-auto w-full max-w-2xl px-5 py-12 sm:px-8 sm:py-20"
      >
        <section className="rounded-lg border border-[var(--line)] bg-[var(--paper)] p-6 shadow-sm sm:p-10">
          <p className="eyebrow">Setup complete</p>
          <h1
            ref={successHeading}
            className="mt-4 font-serif text-4xl text-[var(--green)]"
            tabIndex={-1}
          >
            Your passkey is ready
          </h1>
          <p aria-live="polite" className="sr-only" role="status">
            Passkey setup is complete. You can now sign in with this passkey.
          </p>
          <p className="mt-4 leading-7 text-[var(--ink)]">
            Sign in to Cookbook with this passkey. This setup does not sign you
            in automatically.
          </p>
          <a className="button button--green mt-7" href="/">
            Continue to sign in
          </a>
        </section>
      </main>
    );
  }

  const message =
    status === "busy"
      ? "Waiting for your device to create a passkey…"
      : feedback;

  return (
    <main
      id="home"
      className="mx-auto w-full max-w-2xl px-5 py-12 sm:px-8 sm:py-20"
    >
      <section className="rounded-lg border border-[var(--line)] bg-[var(--paper)] p-6 shadow-sm sm:p-10">
        <p className="eyebrow">Secure account setup</p>
        <h1 className="mt-4 font-serif text-4xl text-[var(--green)]">
          Set up your first passkey
        </h1>
        <p className="mt-4 leading-7 text-[var(--ink)]">
          Enter the one-time enrollment code provided by your Cookbook
          operator. Codes expire after 15 minutes and can be used once.
        </p>
        <p id="enrollment-help" className="mt-3 text-sm leading-6 text-slate-700">
          The code is sent only in secure request bodies. It is not saved in
          browser storage or added to this page’s address.
        </p>

        <form className="mt-7 space-y-5" onSubmit={submit}>
          <div className="space-y-2">
            <label
              className="block text-sm font-semibold text-[var(--ink)]"
              htmlFor="enrollment-code"
            >
              One-time enrollment code
            </label>
            <input
              autoComplete="off"
              autoCapitalize="none"
              className="w-full rounded border border-[var(--line)] bg-white px-3 py-3 text-base text-[var(--ink)] focus-visible:outline-offset-2"
              id="enrollment-code"
              maxLength={97}
              onChange={(event) => setToken(event.target.value)}
              readOnly={status === "busy"}
              required
              spellCheck={false}
              type="password"
              value={token}
              aria-describedby="enrollment-help"
            />
          </div>

          <div className="flex flex-wrap items-center gap-4">
            <button
              className="button button--green"
              disabled={status === "busy"}
              type="submit"
            >
              {status === "busy" ? "Waiting…" : "Create passkey"}
            </button>
            {status === "busy" ? (
              <button className="text-link" onClick={cancel} type="button">
                Cancel enrollment
              </button>
            ) : token ? (
              <button
                className="text-link"
                onClick={() => {
                  setToken("");
                  setStatus("idle");
                }}
                type="button"
              >
                Clear code
              </button>
            ) : null}
          </div>
        </form>

        {message && (
          <p
            aria-live={status === "busy" ? "polite" : undefined}
            className="mt-5 leading-6 text-[var(--ink)]"
            role={status === "error" ? "alert" : "status"}
          >
            {status === "error" &&
            message === unavailableMessage
              ? `${unavailableMessage} ${recoveryMessage}`
              : status === "error"
                ? recoveryMessage
                : message}
          </p>
        )}
      </section>
    </main>
  );
}
