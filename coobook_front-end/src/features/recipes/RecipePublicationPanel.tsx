import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type {
  FederationMode,
  PublicationState,
  RecipePublicationView,
} from "../../apiModels";
import {
  fetchRecipePublication,
  isPublisherProfileRequiredError,
  RecipePublicationApiError,
  updateRecipePublication,
} from "./recipePublicationApi";
import {
  queueRecipeRetention,
  RecipeRetentionApiError,
} from "./recipeRetentionApi";
import PublisherProfileSetupGuidance from "../creator/PublisherProfileSetupGuidance.tsx";

interface RecipePublicationPanelProps {
  recipeId: string;
  recipeVersion: number;
  isOwner: boolean;
  isRemote: boolean;
  open: boolean;
  onClose: () => void;
}

function allowedPublicationStates(
  current: PublicationState,
): PublicationState[] {
  if (current === "unpublished") return ["unpublished", "published"];
  if (current === "published") return ["published", "withdrawn"];
  return ["withdrawn", "published"];
}

function publicationStateLabel(state: PublicationState): string {
  switch (state) {
    case "published":
      return "Published for public browsing";
    case "withdrawn":
      return "Withdrawn";
    default:
      return "Not published";
  }
}

function formatDate(value: string | null): string {
  if (!value) return "Not published yet";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}

function saveErrorMessage(error: unknown): string {
  if (!(error instanceof RecipePublicationApiError)) {
    return "Could not save these settings. Your changes were not committed.";
  }

  if (error.status === 409 && error.code === "publication_unavailable") {
    return "Publishing requires a public recipe on an instance that allows public browsing.";
  }
  if (error.status === 409 && error.code === "federation_unavailable") {
    return "Follower distribution is unavailable. The recipe must be public and published, and federation must be enabled for this instance.";
  }
  if (error.status === 403 && error.code === "instance_private") {
    return "This instance does not currently allow public recipes.";
  }
  if (error.status === 503 || error.code === "storage_error") {
    return "The change could not be committed. Your selected settings are still here; try again later.";
  }
  return "Could not save these settings. Your changes were not committed.";
}

function retentionErrorMessage(error: unknown): string {
  if (!(error instanceof RecipeRetentionApiError))
    return "The request could not be confirmed. It may have been queued; refresh the recipe before retrying.";
  if (error.code === "federation_assertion_conflict")
    return "The recipe or federation eligibility changed. Close and reopen publication settings, then review eligibility before trying again. An enabled Exchange v2 peer is required.";
  if (error.code === "reauthentication_required")
    return "Sign in again; this action requires a recently issued session.";
  if (error.status === 401)
    return "Your session may have expired. Sign in again before trying this action.";
  if (error.code === "federation_queue_unavailable")
    return "The federation queue is unavailable. The permission was not queued; try again later.";
  if (error.code === "invalid_recipe_version")
    return "The recipe version is invalid. Close and reopen this recipe before trying again.";
  if (error.code === "storage_error")
    return "The server could not queue this permission because of a storage error.";
  if (error.status === 404 || error.status === 405)
    return "This server does not support personal retention requests yet.";
  if (error.code === "invalid_response")
    return "The server response could not be verified. The permission may have been queued; refresh the recipe before retrying.";
  return "The request could not be confirmed. It may have been queued; refresh the recipe before retrying.";
}

export default function RecipePublicationPanel({
  recipeId,
  recipeVersion,
  isOwner,
  isRemote,
  open,
  onClose,
}: RecipePublicationPanelProps) {
  const id = useId();
  const scopeKey =
    open && isOwner && !isRemote ? JSON.stringify([recipeId, isOwner, isRemote]) : null;
  const headingRef = useRef<HTMLHeadingElement>(null);
  const staleMessageRef = useRef<HTMLDivElement>(null);
  const activeController = useRef<AbortController | null>(null);
  const activeRetentionController = useRef<AbortController | null>(null);
  const activeScopeKey = useRef<string | null>(scopeKey);
  const [view, setView] = useState<RecipePublicationView | null>(null);
  const [etag, setEtag] = useState<string | null>(null);
  const [publicationState, setPublicationState] =
    useState<PublicationState>("unpublished");
  const [federationMode, setFederationMode] = useState<FederationMode>("off");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState("");
  const [errorMessage, setErrorMessage] = useState("");
  const [publisherProfileRequired, setPublisherProfileRequired] = useState(false);
  const [withdrawConfirmed, setWithdrawConfirmed] = useState(false);
  const [federationConfirmed, setFederationConfirmed] = useState(false);
  const [freshReviewRequired, setFreshReviewRequired] = useState(false);
  const [freshReviewConfirmed, setFreshReviewConfirmed] = useState(false);
  const [retentionConfirmedVersion, setRetentionConfirmedVersion] = useState<number | null>(null);
  const [retentionSaving, setRetentionSaving] = useState(false);
  const [retentionStatus, setRetentionStatus] = useState("");
  const [retentionError, setRetentionError] = useState("");

  useLayoutEffect(() => {
    activeController.current?.abort();
    activeController.current = null;
    activeRetentionController.current?.abort();
    activeRetentionController.current = null;
    activeScopeKey.current = scopeKey;
    setView(null);
    setEtag(null);
    setPublicationState("unpublished");
    setFederationMode("off");
    setLoading(false);
    setSaving(false);
    setStatus("");
    setErrorMessage("");
    setPublisherProfileRequired(false);
    setWithdrawConfirmed(false);
    setFederationConfirmed(false);
    setFreshReviewRequired(false);
    setFreshReviewConfirmed(false);
    setRetentionConfirmedVersion(null);
    setRetentionSaving(false);
    setRetentionStatus("");
    setRetentionError("");

    return () => {
      if (activeScopeKey.current === scopeKey) {
        activeScopeKey.current = null;
        activeController.current?.abort();
        activeController.current = null;
        activeRetentionController.current?.abort();
        activeRetentionController.current = null;
      }
    };
  }, [scopeKey]);

  useEffect(() => {
    activeRetentionController.current?.abort();
    activeRetentionController.current = null;
    setRetentionConfirmedVersion(null);
    setRetentionSaving(false);
    setRetentionStatus("");
    setRetentionError("");
  }, [recipeVersion]);

  useEffect(() => {
    if (!open || !isOwner || isRemote) return;

    const requestScopeKey = scopeKey;
    const controller = new AbortController();
    activeController.current = controller;
    setLoading(true);
    setErrorMessage("");
    setPublisherProfileRequired(false);
    setStatus("Loading publication settings…");
    void fetchRecipePublication(recipeId, controller.signal)
      .then((snapshot) => {
        if (
          controller.signal.aborted ||
          activeScopeKey.current !== requestScopeKey
        ) {
          return;
        }
        setView(snapshot.view);
        setEtag(snapshot.etag);
        setPublicationState(snapshot.view.publication_state);
        setFederationMode(snapshot.view.federation_mode);
        setWithdrawConfirmed(false);
        setFederationConfirmed(false);
        setFreshReviewRequired(false);
        setFreshReviewConfirmed(false);
        setRetentionConfirmedVersion(null);
        setStatus("Publication settings loaded.");
      })
      .catch((error: unknown) => {
        if (
          controller.signal.aborted ||
          activeScopeKey.current !== requestScopeKey ||
          (error instanceof DOMException && error.name === "AbortError")
        ) {
          return;
        }
        setErrorMessage("Could not load publication settings. Try again.");
        setStatus("");
      })
      .finally(() => {
        if (
          !controller.signal.aborted &&
          activeScopeKey.current === requestScopeKey
        ) {
          setLoading(false);
        }
      });

    return () => {
      controller.abort();
      activeController.current?.abort();
      activeController.current = null;
    };
  }, [isOwner, isRemote, open, recipeId, scopeKey]);

  useEffect(() => {
    if (freshReviewRequired) staleMessageRef.current?.focus();
  }, [freshReviewRequired]);

  const changed = Boolean(
    view &&
      (publicationState !== view.publication_state ||
        federationMode !== view.federation_mode),
  );
  const withdrawing =
    view?.publication_state === "published" &&
    publicationState === "withdrawn";
  const enablingFederation =
    view?.federation_mode !== "followers" && federationMode === "followers";
  const needsFederationAck = enablingFederation;
  const canPublish =
    view?.visibility === "public" && view.public_instance;
  const federationGateOpen = Boolean(
    view &&
      publicationState === "published" &&
      view.visibility === "public" &&
      view.public_instance &&
      view.federation_enabled &&
      view.federation_origin,
  );
  const recipeVersionValid = Number.isSafeInteger(recipeVersion) && recipeVersion > 0;
  const retentionEligibilityOpen = Boolean(
    recipeVersionValid &&
      view?.visibility === "public" &&
      view.public_instance &&
      view.publication_state === "published" &&
      publicationState === "published" &&
      view.federation_enabled &&
      view.federation_origin &&
      view.federation_mode === "followers" &&
      federationMode === "followers",
  );
  const saveBlocked =
    !scopeKey ||
    activeScopeKey.current !== scopeKey ||
    !changed ||
    saving ||
    retentionSaving ||
    loading ||
    !etag ||
    (publicationState === "published" && view?.publication_state !== "published" && !canPublish) ||
    (federationMode === "followers" && !federationGateOpen) ||
    (withdrawing && !withdrawConfirmed) ||
    (needsFederationAck && !federationConfirmed) ||
    (freshReviewRequired && !freshReviewConfirmed);

  function clearConfirmations() {
    setWithdrawConfirmed(false);
    setFederationConfirmed(false);
    setFreshReviewConfirmed(false);
    setRetentionConfirmedVersion(null);
  }

  function requestIsCurrent(signal: AbortSignal, requestScopeKey: string | null) {
    return (
      !signal.aborted &&
      requestScopeKey !== null &&
      activeScopeKey.current === requestScopeKey
    );
  }

  function retentionRequestIsCurrent(
    signal: AbortSignal,
    requestScopeKey: string | null,
    expectedVersion: number,
  ) {
    return (
      requestIsCurrent(signal, requestScopeKey) &&
      recipeVersion === expectedVersion &&
      activeRetentionController.current?.signal === signal
    );
  }

  async function loadAfterConflict(
    signal: AbortSignal,
    requestScopeKey: string | null,
  ) {
    setLoading(true);
    setErrorMessage("");
    setStatus("Refreshing current publication settings…");
    try {
      const snapshot = await fetchRecipePublication(recipeId, signal);
      if (!requestIsCurrent(signal, requestScopeKey)) return;
      setView(snapshot.view);
      setEtag(snapshot.etag);
      setFreshReviewRequired(true);
      setFreshReviewConfirmed(false);
      setStatus("Current settings refreshed. Review and confirm your selected changes before saving again.");
    } catch (error) {
      if (
        error instanceof DOMException && error.name === "AbortError"
      ) return;
      if (requestIsCurrent(signal, requestScopeKey)) {
        setEtag(null);
        setFreshReviewRequired(true);
        setFreshReviewConfirmed(false);
        setErrorMessage("The settings changed elsewhere, but current values could not be loaded. Reload before trying again.");
        setStatus("");
      }
    } finally {
      if (requestIsCurrent(signal, requestScopeKey)) setLoading(false);
    }
  }

  async function reloadInitialSettings() {
    const requestScopeKey = scopeKey;
    if (requestScopeKey === null) return;
    const controller = new AbortController();
    activeController.current?.abort();
    activeController.current = controller;
    setLoading(true);
    setErrorMessage("");
    setStatus("Loading publication settings…");
    try {
      const snapshot = await fetchRecipePublication(recipeId, controller.signal);
      if (!requestIsCurrent(controller.signal, requestScopeKey)) return;
      setView(snapshot.view);
      setEtag(snapshot.etag);
      setPublicationState(snapshot.view.publication_state);
      setFederationMode(snapshot.view.federation_mode);
      setFreshReviewRequired(false);
      setFreshReviewConfirmed(false);
      setWithdrawConfirmed(false);
      setFederationConfirmed(false);
      setRetentionConfirmedVersion(null);
      setStatus("Publication settings loaded.");
    } catch (error) {
      if (
        error instanceof DOMException && error.name === "AbortError"
      ) return;
      if (requestIsCurrent(controller.signal, requestScopeKey)) {
        setErrorMessage("Could not load publication settings. Try again.");
        setStatus("");
      }
    } finally {
      if (requestIsCurrent(controller.signal, requestScopeKey)) setLoading(false);
    }
  }

  async function retryStaleRefresh() {
    const requestScopeKey = scopeKey;
    if (requestScopeKey === null) return;
    const controller = new AbortController();
    activeController.current?.abort();
    activeController.current = controller;
    await loadAfterConflict(controller.signal, requestScopeKey);
  }

  async function saveChanges() {
    const requestScopeKey = scopeKey;
    if (requestScopeKey === null || saveBlocked || !etag) return;

    const controller = new AbortController();
    activeController.current?.abort();
    activeController.current = controller;
    setSaving(true);
    setErrorMessage("");
    setPublisherProfileRequired(false);
    setStatus("Saving publication settings…");
    try {
      const snapshot = await updateRecipePublication(
        recipeId,
        {
          publication_state: publicationState,
          federation_mode:
            publicationState === "withdrawn" ? "off" : federationMode,
          index_eligible: false,
        },
        etag,
        controller.signal,
      );
      if (!requestIsCurrent(controller.signal, requestScopeKey)) return;
      setView(snapshot.view);
      setEtag(snapshot.etag);
      setPublicationState(snapshot.view.publication_state);
      setFederationMode(snapshot.view.federation_mode);
      setFreshReviewRequired(false);
      setFreshReviewConfirmed(false);
      setWithdrawConfirmed(false);
      setFederationConfirmed(false);
      setRetentionConfirmedVersion(null);
      setStatus("Publication settings saved.");
      headingRef.current?.focus();
    } catch (error) {
      if (!requestIsCurrent(controller.signal, requestScopeKey)) return;
      if (error instanceof RecipePublicationApiError && error.status === 412) {
        setStatus("Refreshing current publication settings…");
        setFreshReviewConfirmed(false);
        await loadAfterConflict(controller.signal, requestScopeKey);
      } else {
        if (isPublisherProfileRequiredError(error)) {
          setPublisherProfileRequired(true);
          setErrorMessage("");
          setStatus("");
          return;
        }
        setErrorMessage(saveErrorMessage(error));
        setStatus("");
      }
    } finally {
      if (requestIsCurrent(controller.signal, requestScopeKey)) setSaving(false);
    }
  }

  async function queueRetentionPermission() {
    const requestScopeKey = scopeKey;
    const expectedVersion = recipeVersion;
    if (
      requestScopeKey === null ||
      activeScopeKey.current !== requestScopeKey ||
      !retentionEligibilityOpen ||
      retentionConfirmedVersion !== expectedVersion ||
      retentionSaving
    )
      return;

    const consent = `Allow enabled connected peers using Exchange v2 to keep a private personal copy of version ${expectedVersion}, including after ordinary withdrawal. This also permits export and import for your own personal-account migration. It does not authorize public redistribution or adaptation, retain media, or verify authorship.`;
    if (!window.confirm(`${consent} Queue this permission for eligible peers now?`)) return;

    const controller = new AbortController();
    activeRetentionController.current?.abort();
    activeRetentionController.current = controller;
    setRetentionSaving(true);
    setRetentionStatus("");
    setRetentionError("");
    try {
      await queueRecipeRetention(recipeId, expectedVersion, controller.signal);
      if (!retentionRequestIsCurrent(controller.signal, requestScopeKey, expectedVersion))
        return;
      setRetentionConfirmedVersion(null);
      setRetentionStatus(
        `Permission for version ${expectedVersion} was queued for eligible peers. This confirms queueing only; it does not confirm receipt or personal capture.`,
      );
    } catch (error: unknown) {
      if (
        !retentionRequestIsCurrent(controller.signal, requestScopeKey, expectedVersion) ||
        (error instanceof DOMException && error.name === "AbortError")
      )
        return;
      setRetentionConfirmedVersion(null);
      setRetentionError(retentionErrorMessage(error));
    } finally {
      if (retentionRequestIsCurrent(controller.signal, requestScopeKey, expectedVersion)) {
        activeRetentionController.current = null;
        setRetentionSaving(false);
      }
    }
  }

  function handleClose() {
    activeController.current?.abort();
    activeController.current = null;
    activeRetentionController.current?.abort();
    activeRetentionController.current = null;
    activeScopeKey.current = null;
    onClose();
  }

  if (!open || !isOwner || isRemote) return null;

  const publicationId = `${id}-publication-state`;
  const federationId = `${id}-federation`;
  const withdrawAckId = `${id}-withdraw-ack`;
  const federationAckId = `${id}-federation-ack`;
  const freshAckId = `${id}-fresh-ack`;
  const retentionAckId = `${id}-retention-ack`;

  return (
    <section aria-labelledby={`${id}-heading`} className="recipe-publication-panel">
      <div className="section-heading">
        <h2 id={`${id}-heading`} ref={headingRef} tabIndex={-1}>
          Publication settings
        </h2>
        <button className="text-link" type="button" onClick={handleClose}>
          Close
        </button>
      </div>

      <p className="sr-only" aria-live="polite" aria-atomic="true" role="status">
        {status}
      </p>
      {publisherProfileRequired ? (
        <p className="form-message" role="alert">
          <PublisherProfileSetupGuidance />
        </p>
      ) : errorMessage && !(freshReviewRequired && !etag) && (
        <p className="form-message" role="alert">
          {errorMessage}
        </p>
      )}
      {freshReviewRequired && view && etag && (
        <div
          className="form-message"
          ref={staleMessageRef}
          role="alert"
          tabIndex={-1}
        >
          Settings changed elsewhere. Current values are refreshed; review your
          selected changes and confirm them before saving again.
          <label htmlFor={freshAckId}>
            <input
              id={freshAckId}
              type="checkbox"
              checked={freshReviewConfirmed}
              onChange={(event) => setFreshReviewConfirmed(event.target.checked)}
            />{" "}
            I reviewed the current settings and still want to apply my selections.
          </label>
        </div>
      )}
      {freshReviewRequired && view && !etag && (
        <div
          className="form-message"
          ref={staleMessageRef}
          role="alert"
          tabIndex={-1}
        >
          Settings changed elsewhere. The current values have not loaded yet;
          reload before trying again.
        </div>
      )}

      {loading && !view ? <p aria-hidden="true">Loading publication settings…</p> : null}
      {errorMessage && (!view || !etag) && (
        <button
          className="button button--green"
          type="button"
          onClick={() =>
            void (freshReviewRequired
              ? retryStaleRefresh()
              : reloadInitialSettings())
          }
        >
          {freshReviewRequired ? "Reload current settings" : "Reload settings"}
        </button>
      )}

      {view && (
        <>
          <dl>
            <div>
              <dt>Current status</dt>
              <dd>{publicationStateLabel(view.publication_state)}</dd>
            </div>
            <div>
              <dt>Visibility</dt>
              <dd>{view.visibility}</dd>
            </div>
            <div>
              <dt>Published</dt>
              <dd>{formatDate(view.published_at)}</dd>
            </div>
            <div>
              <dt>Public browsing on this instance</dt>
              <dd>{view.public_instance ? "Available" : "Unavailable"}</dd>
            </div>
          </dl>

          <fieldset disabled={saving || loading || retentionSaving}>
            <legend>Local publication</legend>
            <label htmlFor={publicationId}>Public browsing status</label>
            <select
              id={publicationId}
              value={publicationState}
              onChange={(event) => {
                const nextState = event.target.value as PublicationState;
                setPublicationState(nextState);
                if (nextState === "withdrawn") setFederationMode("off");
                clearConfirmations();
              }}
            >
              {allowedPublicationStates(view.publication_state).map((state) => (
                <option
                  key={state}
                  value={state}
                  disabled={
                    state === "published" &&
                    view.publication_state !== "published" &&
                    !canPublish
                  }
                >
                  {publicationStateLabel(state)}
                </option>
              ))}
            </select>
            {!canPublish && view.publication_state !== "published" && (
              <p className="dialog-note">
                Publishing is unavailable until this recipe is public and this
                instance allows public browsing.
              </p>
            )}
            {withdrawing && (
              <label htmlFor={withdrawAckId}>
                <input
                  id={withdrawAckId}
                  type="checkbox"
                  checked={withdrawConfirmed}
                  onChange={(event) => setWithdrawConfirmed(event.target.checked)}
                />{" "}
                I understand withdrawal stops new distribution but cannot remove
                copies already held elsewhere.
              </label>
            )}
          </fieldset>

          <fieldset disabled={saving || loading || retentionSaving}>
            <legend>External distribution</legend>
            <label htmlFor={federationId}>
              <input
                id={federationId}
                type="checkbox"
                checked={federationMode === "followers" && publicationState !== "withdrawn"}
                disabled={
                  federationMode !== "followers" && !federationGateOpen
                }
                onChange={(event) => {
                  setFederationMode(event.target.checked ? "followers" : "off");
                  setFederationConfirmed(false);
                  setFreshReviewConfirmed(false);
                  setRetentionConfirmedVersion(null);
                }}
              />{" "}
              Allow distribution to followers
            </label>
            {!federationGateOpen && federationMode !== "followers" && (
              <p className="dialog-note">
                Follower distribution requires a public, published recipe and
                federation enabled with a canonical origin on this instance.
              </p>
            )}
            {needsFederationAck && (
              <label htmlFor={federationAckId}>
                <input
                  id={federationAckId}
                  type="checkbox"
                  checked={federationConfirmed}
                  onChange={(event) => setFederationConfirmed(event.target.checked)}
                />{" "}
                I allow follower distribution and understand remote copies may
                remain after consent is turned off.
              </label>
            )}
            <p className="dialog-note">Index submission is not available.</p>
          </fieldset>

          <section aria-labelledby={`${id}-retention-heading`} className="admin-settings-note">
            <h3 id={`${id}-retention-heading`}>Private personal retention</h3>
            <p>
              This separate permission applies only to the recipe version currently
              open: version {recipeVersion}.
            </p>
            <p id={`${id}-retention-eligibility`} className="dialog-note">
              Queueing requires a public, published recipe, follower distribution,
              and instance federation configured. At least one enabled Exchange v2
              peer is also required; the server checks current peer eligibility when
              you submit.
            </p>
            <label htmlFor={retentionAckId}>
              <input
                id={retentionAckId}
                type="checkbox"
                aria-describedby={`${id}-retention-eligibility`}
                disabled={!retentionEligibilityOpen || saving || loading || retentionSaving}
                checked={retentionConfirmedVersion === recipeVersion}
                onChange={(event) => {
                  setRetentionConfirmedVersion(
                    event.target.checked ? recipeVersion : null,
                  );
                  setRetentionStatus("");
                  setRetentionError("");
                }}
              />{" "}
              I permit eligible connected peers using Exchange v2 to keep a private
              personal copy of version {recipeVersion}, including after ordinary
              withdrawal. This also permits export and import for my own
              personal-account migration. It does not authorize public redistribution
              or adaptation, retain media, or verify authorship.
            </label>
            <p className="dialog-note">
              This is your permission statement for this version. It does not verify
              authorship or legal rights, and does not report what any peer received
              or retained.
            </p>
            {retentionError && (
              <p className="form-message" role="alert">{retentionError}</p>
            )}
            {retentionStatus && (
              <p className="form-message" role="status">{retentionStatus}</p>
            )}
            <button
              className="button button--green"
              type="button"
              aria-describedby={`${id}-retention-eligibility`}
              disabled={
                !retentionEligibilityOpen ||
                retentionConfirmedVersion !== recipeVersion ||
                retentionSaving ||
                saving ||
                loading
              }
              onClick={() => void queueRetentionPermission()}
            >
              {retentionSaving
                ? "Queueing permission…"
                : `Queue permission for version ${recipeVersion}`}
            </button>
          </section>

          <div className="section-heading">
            <button
              className="button button--green"
              type="button"
              onClick={() => void saveChanges()}
              disabled={saveBlocked}
            >
              {saving ? "Saving…" : "Save publication settings"}
            </button>
            <button className="text-link" type="button" onClick={handleClose}>
              Cancel
            </button>
          </div>
        </>
      )}
    </section>
  );
}
