import { lazy, useEffect, useRef, useState } from "react";
import type { AdminUser } from "./apiModels";
import AdminUsers from "./AdminUsers.tsx";
import AppNavigation from "./app/AppNavigation.tsx";
import FeatureIsland from "./components/FeatureIsland.tsx";
import {
  SessionCheckError,
  useAppSession,
} from "./app/AppSession.tsx";
import useInstanceAuthority from "./features/auth/useInstanceAuthority.ts";
import {
  acceptOwnerTransfer,
  cancelOwnerTransfer,
  createOwnerTransfer,
  OwnerTransferApiError,
} from "./features/instance/ownerTransferApi.ts";
import type { OwnerTransfer } from "./features/instance/ownerTransferApi.ts";
import useOwnerTransfers from "./features/instance/useOwnerTransfers.ts";

const AdminInvitations = lazy(() => import("./features/instance/AdminInvitations.tsx"));
const AdminAuditLog = lazy(() => import("./features/instance/AdminAuditLog.tsx"));

function SignInRequired() {
  return (
    <section className="cookbook-dashboard" aria-labelledby="instance-signin-title">
      <h2 id="instance-signin-title">Sign in to view instance access</h2>
      <p>Instance actions are tied to your Cookbook account.</p>
      <a className="button button--green" href="/?signin=1">Sign in</a>
    </section>
  );
}

function transferFailure(error: unknown): string {
  if (!(error instanceof OwnerTransferApiError))
    return "The request outcome could not be confirmed. Current state has been checked; review it before trying again.";
  if (error.code === "reauthentication_required")
    return "Sign out and sign in again before trying this action. The current session is not recent enough.";
  if (error.code === "owner_state_conflict")
    return "Instance ownership changed while you were acting. Current state has been refreshed.";
  if (error.code === "owner_transfer_expired")
    return "This ownership request expired. Current state has been refreshed.";
  if (error.status === 401)
    return "Your session may have expired. Check your session and sign in again if needed.";
  if (error.status === 403)
    return "Your instance permissions changed. Current state has been refreshed.";
  if (error.code === "invalid_transfer_target")
    return "Choose a different existing account for this transfer.";
  return "Could not complete the ownership request. Refresh current state before trying again.";
}

type TransferMutation =
  | { action: "create"; target: AdminUser }
  | { action: "accept" | "cancel"; transfer: OwnerTransfer };

function SignInAgainNotice() {
  return (
    <p className="form-message" role="alert">
      Sign out and sign back in, then return to <a href="/admin">Instance</a> to retry.
    </p>
  );
}

export default function InstanceWorkbench() {
  const { state: sessionState, currentUser, refreshSession } = useAppSession();
  const actorID = sessionState.status === "authenticated"
    ? sessionState.currentUser.id
    : null;
  const authority = useInstanceAuthority(actorID, actorID !== null);
  const transfers = useOwnerTransfers(actorID, actorID !== null, true);
  const actorIDRef = useRef(actorID);
  actorIDRef.current = actorID;
  const retryAuthorityRef = useRef(authority.retry);
  retryAuthorityRef.current = authority.retry;
  const [noticeState, setNoticeState] = useState<{ userID: string; message: string } | null>(null);
  const [errorState, setErrorState] = useState<{ userID: string; message: string } | null>(null);
  const [busyState, setBusyState] = useState<{ userID: string; action: string } | null>(null);
  const notice = noticeState?.userID === actorID ? noticeState.message : "";
  const error = errorState?.userID === actorID ? errorState.message : "";
  const busyAction = busyState?.userID === actorID ? busyState.action : "";

  useEffect(() => {
    setNoticeState(null);
    setErrorState(null);
    setBusyState(null);
  }, [actorID]);

  useEffect(() => {
    if (!actorID) return;
    const refreshAuthorityOnFocus = () => retryAuthorityRef.current();
    window.addEventListener("focus", refreshAuthorityOnFocus);
    return () => window.removeEventListener("focus", refreshAuthorityOnFocus);
  }, [actorID]);

  async function refreshAfterMutation(accepted: boolean) {
    if (actorIDRef.current === actorID) {
      authority.retry();
      await transfers.refresh();
    }
    if (accepted && actorIDRef.current === actorID)
      await refreshSession();
  }

  async function handleMutation(request: TransferMutation) {
    const { action } = request;
    if (!actorID || busyAction) return;

    let confirmation: string;
    if (action === "create") {
      const { target } = request;
      if (target.id === actorID) return;
      confirmation = `Transfer instance ownership to ${target.display_name} (${target.email})? They must accept the request before ownership changes. The request expires in seven days. Administrator roles remain separate, and no email will be sent.`;
    } else {
      const { transfer } = request;
      confirmation = action === "accept"
        ? `Accept the ownership transfer from ${transfer.counterparty_display_name}? You will become instance owner, but this will not make you an administrator. The request expires ${new Date(transfer.expires_at).toLocaleString()}.`
        : `Cancel the ownership request to ${transfer.counterparty_display_name}? They will no longer be able to accept this request.`;
    }
    if (!window.confirm(confirmation)) return;

    setBusyState({ userID: actorID, action });
    setErrorState(null);
    setNoticeState(null);
    try {
      if (request.action === "create") await createOwnerTransfer(request.target.id);
      else if (request.action === "accept") await acceptOwnerTransfer(request.transfer.id);
      else await cancelOwnerTransfer(request.transfer.id);
      if (actorIDRef.current !== actorID) return;
      await refreshAfterMutation(action === "accept");
      if (actorIDRef.current === actorID) {
        setNoticeState({
          userID: actorID,
          message: action === "create"
            ? "Ownership request created. Ownership changes only if the recipient accepts."
            : action === "accept"
              ? "Ownership transfer accepted. Your account is now the instance owner."
              : "Ownership request cancelled.",
        });
      }
    } catch (mutationError) {
      if (actorIDRef.current !== actorID) return;
      if (
        !(mutationError instanceof OwnerTransferApiError) ||
        mutationError.code === "owner_state_conflict" ||
        mutationError.code === "owner_transfer_expired" ||
        mutationError.status === 403
      ) {
        authority.retry();
        await transfers.refresh();
      }
      if (mutationError instanceof OwnerTransferApiError && mutationError.status === 401)
        await refreshSession();
      if (actorIDRef.current !== actorID) return;
      setErrorState({ userID: actorID, message: transferFailure(mutationError) });
    } finally {
      if (actorIDRef.current === actorID)
        setBusyState(null);
    }
  }

  const authorityValue = authority.state.status === "ready" && authority.state.userID === actorID
    ? authority.state.authority
    : null;
  const transferItems = transfers.state.status === "ready" && transfers.state.userID === actorID
    ? transfers.state.transfers
    : null;
  const incoming = transferItems?.find((item) => item.direction === "incoming");
  const outgoing = transferItems?.find((item) => item.direction === "outgoing");
  const canViewAccounts = Boolean(authorityValue?.is_admin || authorityValue?.is_instance_owner);
  const canChooseTransferTarget = Boolean(authorityValue?.is_instance_owner && !outgoing && transferItems);

  return (
    <div className="app-content">
      <AppNavigation
        currentUser={currentUser}
        pathname="/admin"
        sessionStatus={sessionState.status}
        onRetrySession={() => void refreshSession()}
      />
      <main id="home" tabIndex={-1}>
        <section className="dashboard-welcome">
          <div>
            <span className="eyebrow">Instance</span>
            <h1>Instance access</h1>
            <p>Review administrator access and ownership requests.</p>
          </div>
        </section>

        {sessionState.status === "loading" ? (
          <p className="cookbook-dashboard" role="status">Checking your session…</p>
        ) : sessionState.status === "error" ? (
          <SessionCheckError message={sessionState.message} onRetry={() => void refreshSession()} />
        ) : sessionState.status === "anonymous" ? (
          <SignInRequired />
        ) : authority.state.status === "loading" ? (
          <p className="cookbook-dashboard" role="status">Checking instance permissions…</p>
        ) : authority.state.status === "error" ? (
          <section className="cookbook-dashboard" aria-labelledby="instance-authority-error">
            <h2 id="instance-authority-error">Could not check instance permissions</h2>
            <p className="form-message" role="alert">{authority.state.message}</p>
            <button className="button button--green" type="button" onClick={authority.retry}>
              Retry permissions
            </button>
          </section>
        ) : authorityValue ? (
          <>
            {notice && <p className="form-message" role="status">{notice}</p>}
            {error && !error.includes("Sign out and sign in again") && (
              <p className="form-message" role="alert">{error}</p>
            )}
            {error.includes("Sign out and sign in again") && <SignInAgainNotice />}

            {!authorityValue.owner_claimed && authorityValue.is_admin && (
              <section className="cookbook-dashboard" aria-labelledby="owner-claim-heading">
                <h2 id="owner-claim-heading">Instance owner not assigned</h2>
                <p>
                  Administrators can manage roles until an operator claims the first owner account
                  with the server stopped using <code>instance owner claim --account-id</code>.
                  This action is performed by the host operator, not from this page.
                </p>
              </section>
            )}

            {authorityValue.is_instance_owner && (
              <section className="cookbook-dashboard" aria-labelledby="owner-requests-heading">
                <div className="section-heading">
                  <div>
                    <span className="eyebrow">Ownership</span>
                    <h2 id="owner-requests-heading">Transfer ownership</h2>
                  </div>
                  <button
                    className="text-link"
                    type="button"
                    disabled={busyAction === "refresh"}
                    onClick={async () => {
                      setBusyState({ userID: actorID!, action: "refresh" });
                      authority.retry();
                      await transfers.refresh();
                      if (actorIDRef.current === actorID) setBusyState(null);
                    }}
                  >
                    Refresh
                  </button>
                </div>
                {transfers.state.status === "loading" && (
                  <p role="status">Checking ownership requests…</p>
                )}
                {transfers.state.status === "error" && (
                  <div>
                    <p className="form-message" role="alert">{transfers.state.message}</p>
                    <button className="text-link" type="button" onClick={() => void transfers.refresh()}>
                      Retry ownership requests
                    </button>
                  </div>
                )}
                {outgoing && (
                  <div className="passkey-row">
                    <span>
                      Request sent to {outgoing.counterparty_display_name}
                      <small>Expires {new Date(outgoing.expires_at).toLocaleString()}</small>
                    </span>
                    <button
                      className="text-link"
                      type="button"
                      disabled={Boolean(busyAction)}
                      onClick={() => void handleMutation({ action: "cancel", transfer: outgoing })}
                    >
                      {busyAction === "cancel" ? "Cancelling…" : "Cancel request"}
                    </button>
                  </div>
                )}
                {transferItems?.length === 0 && (
                  <p>No ownership request is pending.</p>
                )}
                {outgoing && (
                  <p className="admin-settings-note">
                    Ownership changes only if the recipient accepts. Administrator roles remain
                    separate, the request expires after seven days, and no email is sent.
                  </p>
                )}
              </section>
            )}

            {incoming && (
              <section className="cookbook-dashboard" aria-labelledby="incoming-transfer-heading">
                <span className="eyebrow">Incoming ownership request</span>
                <h2 id="incoming-transfer-heading">
                  {incoming.counterparty_display_name} wants to transfer ownership to you
                </h2>
                <p>
                  Accepting makes you instance owner. It does not make you an administrator.
                  This request expires {new Date(incoming.expires_at).toLocaleString()}.
                </p>
                <button
                  className="button button--green"
                  type="button"
                  disabled={Boolean(busyAction)}
                  onClick={() => void handleMutation({ action: "accept", transfer: incoming })}
                >
                  {busyAction === "accept" ? "Accepting…" : "Accept ownership"}
                </button>
              </section>
            )}

            {canViewAccounts && (
              <AdminUsers
                currentUser={sessionState.currentUser}
                authorityState={authority.state}
                onRetryAuthority={authority.retry}
                onRoleChanged={() => {
                  void refreshSession();
                  authority.retry();
                  void transfers.refresh();
                }}
                onTransferTarget={canChooseTransferTarget
                  ? (account) => void handleMutation({ action: "create", target: account })
                  : undefined}
              />
            )}

            {!authorityValue.is_admin && authorityValue.is_instance_owner && (
              <p className="admin-settings-note">
                General instance settings remain administrator-only. You can appoint a separate
                administrator to manage them.
              </p>
            )}
            {authorityValue.is_admin && (
              <p className="admin-settings-note">
                <a className="text-link" href="/admin/settings">Open Admin Settings</a>
              </p>
            )}

            {authorityValue.is_admin && (
              <>
                <FeatureIsland name="Invitations" loading="invitations">
                  <AdminInvitations />
                </FeatureIsland>
                <FeatureIsland name="Audit log" loading="audit log">
                  <AdminAuditLog />
                </FeatureIsland>
              </>
            )}

            {!canViewAccounts && !incoming && transfers.state.status === "ready" && (
              <section className="cookbook-dashboard" aria-labelledby="no-instance-actions">
                <h2 id="no-instance-actions">No instance actions are available</h2>
                <p>This account has no instance access to manage.</p>
              </section>
            )}
            {!canViewAccounts && transfers.state.status === "error" && (
              <section className="cookbook-dashboard" aria-labelledby="transfer-check-error">
                <h2 id="transfer-check-error">Could not check ownership requests</h2>
                <p className="form-message" role="alert">{transfers.state.message}</p>
                <button className="text-link" type="button" onClick={() => void transfers.refresh()}>
                  Retry ownership requests
                </button>
              </section>
            )}
            {!canViewAccounts && transfers.state.status === "loading" && (
              <p className="cookbook-dashboard" role="status">Checking ownership requests…</p>
            )}
          </>
        ) : null}
      </main>
    </div>
  );
}
