import { lazy, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { errorMessage } from "../../apiModels";
import { useAppSession } from "../../app/AppSession.tsx";
import useModalFocus from "../../useModalFocus.ts";
import { signInWithPasskey, registerPasskey } from "../../passkeys.ts";
import usePublicAuthConfig from "./usePublicAuthConfig.ts";
import type { PublicAuthConfigState } from "./usePublicAuthConfig.ts";
import FeatureIsland from "../../components/FeatureIsland.tsx";
const PasskeySetupPrompt = lazy(() => import("../../PasskeySetupPrompt.tsx"));

type AuthIntent =
  | { kind: "confirm"; token: string }
  | { kind: "email"; invitation: string }
  | { kind: "missing" };

type AuthEntryProps =
  | {
      mode: "home";
      open: boolean;
      pathname?: string;
      onClose: () => void;
      publicConfigState?: PublicAuthConfigState;
      retryPublicConfig?: () => void;
    }
  | {
      mode: "confirm";
      onContinueHome: () => void;
      publicConfigState?: PublicAuthConfigState;
      retryPublicConfig?: () => void;
    };

function routeIntent(pathname: string): AuthIntent {
  const params = new URLSearchParams(window.location.search);
  if (pathname === "/auth/confirm") {
    const token = params.get("token") || "";
    if (token) return { kind: "confirm", token };
  }
  const invitation = params.get("invite") || "";
  if (invitation) return { kind: "email", invitation };
  if (params.get("signin")) return { kind: "email", invitation: "" };
  return { kind: "missing" };
}

function useAuthIntent(mode: "home" | "confirm", pathname: string) {
  const [intent, setIntent] = useState<AuthIntent>({ kind: "missing" });
  useEffect(() => {
    setIntent(routeIntent(mode === "confirm" ? "/auth/confirm" : pathname));
  }, [mode, pathname]);
  return [intent, setIntent] as const;
}

export default function AuthEntry(props: AuthEntryProps) {
  const mode = props.mode;
  const onContinueHome = mode === "confirm" ? props.onContinueHome : undefined;
  const pathname =
    mode === "confirm" ? "/auth/confirm" : (props.pathname ?? "/");
  const [intent, setIntent] = useAuthIntent(mode, pathname);
  const { currentUser, state: sessionState, refreshSession } = useAppSession();
  const hasProvidedConfig = props.publicConfigState !== undefined;
  const { state: fetchedAuthConfigState, retry: retryFetchedAuthConfig } =
    usePublicAuthConfig({ enabled: !hasProvidedConfig });
  const authConfigState =
    props.publicConfigState ?? fetchedAuthConfigState;
  const retryAuthConfig =
    props.retryPublicConfig
      ? props.retryPublicConfig
      : retryFetchedAuthConfig;
  const [email, setEmail] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [passkeyPrompt, setPasskeyPrompt] = useState(false);
  const [passkeyEligibility, setPasskeyEligibility] = useState<
    "idle" | "checking" | "eligible" | "ineligible"
  >("idle");
  const [confirmationComplete, setConfirmationComplete] = useState(false);
  const [passkeyName, setPasskeyName] = useState("");
  const [passkeyBusy, setPasskeyBusy] = useState(false);
  const [passkeyError, setPasskeyError] = useState("");
  const dialogRef = useRef<HTMLElement>(null);
  const actionSequence = useRef(0);
  const previousActorID = useRef<string | null>(currentUser?.id ?? null);
  const expectedSessionChange = useRef(false);
  const homeMode = mode === "home";
  const closeHome = homeMode ? props.onClose : undefined;
  const closeHomeRef = useRef(closeHome);
  closeHomeRef.current = closeHome;
  const homeOpen = homeMode && props.open;
  const dialogIntent = homeMode && pathname === "/auth/confirm" ? intent : null;
  const activeIntent = dialogIntent ?? intent;
  const shouldShowDialog =
    homeMode &&
    (homeOpen || Boolean(dialogIntent && dialogIntent.kind !== "missing"));
  useModalFocus(
    dialogRef,
    homeMode ? props.onClose : undefined,
    shouldShowDialog,
  );

  useEffect(() => {
    const actorID = currentUser?.id ?? null;
    if (actorID === previousActorID.current) return;
    previousActorID.current = actorID;
    if (expectedSessionChange.current) {
      expectedSessionChange.current = false;
      return;
    }
    actionSequence.current += 1;
    setEmail("");
    setMessage("");
    setBusy(false);
    setPasskeyPrompt(false);
    setPasskeyEligibility("idle");
    setConfirmationComplete(false);
    if (homeMode) closeHomeRef.current?.();
  }, [currentUser?.id, homeMode]);

  useEffect(() => {
    if (passkeyEligibility !== "eligible" || authConfigState.status !== "ready")
      return;
    if (authConfigState.config.auth_methods.passkey) setPasskeyPrompt(true);
    else setPasskeyEligibility("ineligible");
  }, [passkeyEligibility, authConfigState]);

  useEffect(() => {
    if (mode !== "confirm" || !confirmationComplete) return;
    if (
      passkeyEligibility === "checking" ||
      authConfigState.status === "loading"
    )
      return;
    if (passkeyEligibility === "eligible" && passkeyPrompt) return;
    if (
      passkeyEligibility === "eligible" &&
      authConfigState.status === "ready" &&
      authConfigState.config.auth_methods.passkey
    )
      return;
    onContinueHome?.();
  }, [
    mode,
    confirmationComplete,
    passkeyEligibility,
    authConfigState,
    passkeyPrompt,
    onContinueHome,
  ]);

  async function signInWithEmail(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const sequence = ++actionSequence.current;
    setMessage("");
    try {
      const response = await fetch("/api/auth/email", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          email,
          invitation:
            activeIntent.kind === "email" ? activeIntent.invitation : "",
        }),
      });
      const result = (await response.json()) as { status?: string };
      if (sequence !== actionSequence.current) return;
      if (!response.ok)
        throw new Error(
          response.status === 503
            ? "Email sign-in is not configured on this instance."
            : "Unable to request a sign-in link right now.",
        );
      if (result.status === "if_account_can_sign_in_link_sent")
        setMessage(
          "If this email can sign in, a one-time link will arrive shortly. Check your inbox.",
        );
      else setMessage("Unable to request a sign-in link right now.");
    } catch (error: unknown) {
      if (sequence === actionSequence.current)
        setMessage(
          errorMessage(
            error,
            "Unable to contact this Cookbook instance. Try again later.",
          ) || "Unable to contact this Cookbook instance. Try again later.",
        );
    }
  }

  async function signInWithCredential() {
    const sequence = ++actionSequence.current;
    setMessage("");
    try {
      await signInWithPasskey();
      if (sequence !== actionSequence.current) return;
      const previousID = currentUser?.id ?? null;
      expectedSessionChange.current = true;
      const user = await refreshSession();
      if (!user || user.id === previousID)
        expectedSessionChange.current = false;
      if (sequence !== actionSequence.current) return;
      if (homeMode) props.onClose();
      else if (user) onContinueHome?.();
    } catch (error: unknown) {
      if (sequence === actionSequence.current)
        setMessage(errorMessage(error, "Passkey sign-in failed."));
    }
  }

  async function confirmEmailSignIn() {
    if (activeIntent.kind !== "confirm" || busy) return;
    const token = activeIntent.token;
    const sequence = ++actionSequence.current;
    setBusy(true);
    setMessage("");
    try {
      const response = await fetch("/api/auth/consume", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
      if (!response.ok)
        throw new Error(
          "This sign-in link is invalid, expired, or already used. Request a new link.",
        );
      if (sequence !== actionSequence.current) return;
      window.history.replaceState(
        {},
        "",
        mode === "confirm" ? "/auth/confirm" : "/",
      );
      setIntent({ kind: "missing" });
      setPasskeyEligibility("checking");
      setConfirmationComplete(true);
      const previousID = currentUser?.id ?? null;
      expectedSessionChange.current = true;
      const user = await refreshSession();
      if (!user || user.id === previousID)
        expectedSessionChange.current = false;
      if (sequence !== actionSequence.current) return;
      if (user) {
        setPasskeyEligibility("checking");
        try {
          const passkeysResponse = await fetch("/api/auth/passkeys", {
            cache: "no-store",
          });
          if (sequence !== actionSequence.current) return;
          if (!passkeysResponse.ok) setPasskeyEligibility("ineligible");
          else {
            const registeredPasskeys: unknown = await passkeysResponse.json();
            if (sequence !== actionSequence.current) return;
            setPasskeyEligibility(
              Array.isArray(registeredPasskeys) &&
                registeredPasskeys.length === 0
                ? "eligible"
                : "ineligible",
            );
          }
        } catch {
          if (sequence === actionSequence.current)
            setPasskeyEligibility("ineligible");
        }
      } else {
        setPasskeyEligibility("ineligible");
      }
      if (homeMode) props.onClose();
    } catch (error: unknown) {
      if (sequence === actionSequence.current)
        setMessage(
          errorMessage(error, "This sign-in link could not be confirmed."),
        );
    } finally {
      if (sequence === actionSequence.current) setBusy(false);
    }
  }

  async function createFirstPasskey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const sequence = ++actionSequence.current;
    const actorID = currentUser?.id ?? null;
    setPasskeyBusy(true);
    setPasskeyError("");
    try {
      await registerPasskey(passkeyName.trim() || "Passkey");
      if (
        sequence !== actionSequence.current ||
        actorID !== (currentUser?.id ?? null)
      )
        return;
      onContinueHome?.();
    } catch (error: unknown) {
      if (
        sequence === actionSequence.current &&
        actorID === (currentUser?.id ?? null)
      )
        setPasskeyError(errorMessage(error, "Could not create this passkey."));
    } finally {
      if (sequence === actionSequence.current) setPasskeyBusy(false);
    }
  }

  if (mode === "confirm" && confirmationComplete) {
    return (
      <section
        className="signin-dialog auth-confirm-panel"
        aria-labelledby="auth-confirm-title"
      >
        <span className="eyebrow">Sign-in confirmed</span>
        <h2 id="auth-confirm-title">You’re signed in.</h2>
        <p role="status">
          Your session is ready. Continue to Home whenever you’re ready.
        </p>
        <button
          className="button button--green"
          type="button"
          onClick={onContinueHome}
        >
          Continue to Home
        </button>
        {passkeyEligibility === "checking" && (
          <p role="status">Checking registered passkeys…</p>
        )}
        {passkeyEligibility === "eligible" &&
          authConfigState.status === "loading" && (
            <p role="status">Checking whether passkey setup is available…</p>
          )}
        {passkeyEligibility === "eligible" &&
          authConfigState.status === "error" && (
            <div>
              <p role="alert">{authConfigState.message}</p>
              <button type="button" onClick={retryAuthConfig}>
                Retry sign-in options
              </button>
            </div>
          )}
        {passkeyEligibility === "eligible" &&
          passkeyPrompt &&
          authConfigState.status === "ready" &&
          authConfigState.config.auth_methods.passkey && (
            <section aria-labelledby="first-passkey-title">
              <h3 id="first-passkey-title">Add a passkey?</h3>
              <p>
                Use your device’s screen lock or security key next time. You can
                do this later in account security.
              </p>
              <form onSubmit={createFirstPasskey}>
                <label htmlFor="first-passkey-name">Name this passkey</label>
                <input
                  id="first-passkey-name"
                  value={passkeyName}
                  onChange={(event) => setPasskeyName(event.target.value)}
                  maxLength={80}
                  placeholder="e.g. Phone"
                />
                <button
                  className="button button--green"
                  type="submit"
                  disabled={passkeyBusy}
                >
                  {passkeyBusy ? "Waiting…" : "Create passkey"}
                </button>
              </form>
              <button
                className="text-link"
                type="button"
                onClick={onContinueHome}
              >
                Not now
              </button>
              {passkeyError && (
                <p className="form-message" role="alert">
                  {passkeyError}
                </p>
              )}
            </section>
          )}
      </section>
    );
  }

  if (mode === "confirm" && !shouldRenderIntent(activeIntent)) {
    return (
      <section
        className="signin-dialog auth-confirm-panel"
        aria-labelledby="auth-confirm-title"
      >
        <span className="eyebrow">Cookbook account</span>
        <h2 id="auth-confirm-title">Continue to Cookbook</h2>
        {sessionState.status === "error" && (
          <p role="alert">{sessionState.message}</p>
        )}
        {sessionState.status === "loading" && (
          <p role="status">Checking your session…</p>
        )}
        <p>
          This confirmation link is missing. Ask for a new sign-in link or
          continue to Home.
        </p>
        <a className="button button--green" href="/?signin=1">
          Sign in from Home
        </a>
      </section>
    );
  }

  const dialogContent = (
    <>
      {!homeMode && <span className="eyebrow">Cookbook account</span>}
      {activeIntent.kind === "confirm" ? (
        <>
          <h2 id={homeMode ? "signin-title" : "auth-confirm-title"}>
            Confirm your sign-in
          </h2>
          <p>
            Use this one-time link to sign in. Cookbook will only continue after
            you choose the button below.
          </p>
          <button
            className="button button--green"
            type="button"
            onClick={() => void confirmEmailSignIn()}
            disabled={busy}
          >
            {busy ? "Signing in…" : "Continue to Cookbook"}
          </button>
        </>
      ) : (
        <>
          <h2 id={homeMode ? "signin-title" : "auth-confirm-title"}>
            {activeIntent.kind === "email" && activeIntent.invitation
              ? "Accept your invitation"
              : authConfigState.status === "ready" &&
                  authConfigState.config.auth_methods.email_link
                ? "Sign in with email"
                : "Sign in"}
          </h2>
          <p>
            {authConfigState.status === "ready" &&
            authConfigState.config.auth_methods.email_link
              ? "We’ll send you a one-time link to get back to your cookbooks."
              : "Sign in to your cookbooks."}
          </p>
          {authConfigState.status === "loading" && (
            <p role="status">Loading sign-in options…</p>
          )}
          {authConfigState.status === "error" && (
            <div>
              <p role="alert">{authConfigState.message}</p>
              <button type="button" onClick={retryAuthConfig}>
                Retry
              </button>
            </div>
          )}
          {authConfigState.status === "ready" && (
            <>
              {authConfigState.config.auth_methods.email_link ? (
                <>
                  <p>
                    {authConfigState.config.registration_policy === "open"
                      ? "Anyone can create an account with email sign-in."
                      : authConfigState.config.registration_policy ===
                          "invite_only"
                        ? "New accounts need an invitation; existing accounts can still sign in."
                        : "New email registrations are disabled; existing accounts can still sign in."}
                  </p>
                  <form onSubmit={(event) => void signInWithEmail(event)}>
                    <label htmlFor="signin-email">Email address</label>
                    <input
                      id="signin-email"
                      type="email"
                      autoComplete="email"
                      value={email}
                      onChange={(event) => setEmail(event.target.value)}
                      placeholder="you@example.com"
                      required
                    />
                    <button className="button button--green" type="submit">
                      Send sign-in link
                    </button>
                  </form>
                </>
              ) : (
                <p>Email link sign-in is unavailable on this instance.</p>
              )}
              {authConfigState.config.auth_methods.passkey && (
                <button
                  className="text-link passkey-action"
                  type="button"
                  onClick={() => void signInWithCredential()}
                >
                  Sign in with a passkey
                </button>
              )}
              {!authConfigState.config.auth_methods.email_link &&
                !authConfigState.config.auth_methods.passkey && (
                  <p role="status">
                    Sign-in is currently unavailable on this instance.
                  </p>
                )}
            </>
          )}
        </>
      )}
      {message && (
        <p className="form-message" role="status">
          {message}
        </p>
      )}
      {!homeMode && (
        <button type="button" onClick={onContinueHome}>
          Return to Home
        </button>
      )}
    </>
  );

  if (homeMode) {
    return (
      <>
        {shouldShowDialog && (
          <div
            className="dialog-backdrop"
            role="presentation"
            onMouseDown={(event) =>
              event.target === event.currentTarget && props.onClose()
            }
          >
            <section
              className="signin-dialog"
              role="dialog"
              aria-modal="true"
              aria-labelledby="signin-title"
              ref={dialogRef}
              tabIndex={-1}
            >
              <button
                className="icon-button dialog-close"
                aria-label="Close sign in"
                onClick={props.onClose}
              >
                ×
              </button>
              <span className="brand-mark" aria-hidden="true">
                C
              </span>
              <span className="eyebrow">Welcome to your kitchen</span>
              {dialogContent}
            </section>
          </div>
        )}
        {passkeyPrompt && (
          <FeatureIsland name="Passkey setup" loading="passkey setup">
            <PasskeySetupPrompt onClose={() => setPasskeyPrompt(false)} />
          </FeatureIsland>
        )}
      </>
    );
  }

  return (
    <section
      className="signin-dialog auth-confirm-panel"
      ref={dialogRef}
      aria-labelledby="auth-confirm-title"
    >
      {dialogContent}
    </section>
  );
}

function shouldRenderIntent(intent: AuthIntent): boolean {
  return intent.kind !== "missing";
}
