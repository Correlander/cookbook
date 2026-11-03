import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import type {
  Dispatch,
  FormEvent,
  SetStateAction,
} from "react";
import type {
  ApiFailure,
  FederationDelivery,
  FederationIdentity,
  FederationPeerAdmin,
} from "../../apiModels";
import type { InstanceAuthorityState } from "../auth/useInstanceAuthority";

type SectionName = "identity" | "peers" | "outbox";

interface RequestCounter {
  current: number;
}

interface SectionState<T> {
  actorID: string;
  authorityVersion: InstanceAuthorityState | null;
  loading: boolean;
  data: T | null;
  error: string;
  retryRequired: boolean;
}

interface OutboxData {
  deliveries: FederationDelivery[];
  selected: FederationDelivery | null;
}

interface NoticeState {
  actorID: string;
  section: SectionName;
  message: string;
}

interface BusyState {
  actorID: string;
  section: SectionName;
}

interface FederationAdminProps {
  actorID: string;
  authorityState: InstanceAuthorityState;
  onRefreshAuthority: () => void;
}

class FederationRequestError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "FederationRequestError";
    this.status = status;
    this.code = code;
  }
}

const staleRequest = new Error("Federation request belongs to stale state.");

function emptySection<T>(actorID = ""): SectionState<T> {
  return {
    actorID,
    authorityVersion: null,
    loading: true,
    data: null,
    error: "",
    retryRequired: false,
  };
}

async function federationRequest<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(path, {
    ...options,
    headers: {
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...options.headers,
    },
  });
  const data = (await response.json().catch(() => null)) as ApiFailure | null;
  if (!response.ok) {
    throw new FederationRequestError(
      response.status,
      data?.error ?? "",
      data?.message ?? "",
    );
  }
  return data as T;
}

function requestMessage(error: unknown): string {
  if (!(error instanceof FederationRequestError))
    return error instanceof Error ? error.message : "Federation request failed.";
  if (error.code === "reauthentication_required")
    return "Reauthenticate with a passkey before changing this federation setting. Sign in again to continue.";
  if (error.status === 401)
    return "Your session may have expired. Sign in again before retrying federation controls.";
  if (error.status === 403)
    return "Your access may have changed. Refresh permissions before retrying.";
  const messages: Record<string, string> = {
    active_peer_key_required:
      "Add an active public key before enabling this peer.",
    peer_not_found: "Create the peer before adding its key.",
    invalid_peer_key:
      "Check the peer origin, key ID, and 32-byte Ed25519 public key.",
    federation_rotation_already_pending:
      "A signing-key rotation is already staged.",
    pending_federation_key_required:
      "Stage a new signing key before activating it.",
    invalid_rotation_action: "Choose a valid signing-key action.",
  };
  return (
    messages[error.code] ||
    error.message ||
    `Federation request failed (${error.status}).`
  );
}

function sectionView<T>(
  state: SectionState<T>,
  actorID: string,
  authorityState: InstanceAuthorityState,
) {
  const sameActor = state.actorID === actorID;
  const currentVersion = state.authorityVersion === authorityState;
  const retryRequired = sameActor && state.retryRequired;
  return {
    loading:
      !sameActor ||
      (state.loading && currentVersion) ||
      (!currentVersion && !retryRequired),
    data: sameActor && currentVersion ? state.data : null,
    error:
      sameActor && (currentVersion || retryRequired) ? state.error : "",
    retryRequired,
  };
}

export default function FederationAdmin({
  actorID,
  authorityState,
  onRefreshAuthority,
}: FederationAdminProps) {
  const [identityState, setIdentityState] = useState<SectionState<FederationIdentity>>(
    () => emptySection(),
  );
  const [peerState, setPeerState] = useState<SectionState<FederationPeerAdmin[]>>(
    () => emptySection(),
  );
  const [outboxState, setOutboxState] = useState<SectionState<OutboxData>>(
    () => emptySection(),
  );
  const [originState, setOriginState] = useState({ actorID: "", value: "" });
  const [keyIDState, setKeyIDState] = useState({ actorID: "", value: "" });
  const [publicKeyState, setPublicKeyState] = useState({ actorID: "", value: "" });
  const [busyState, setBusyState] = useState<BusyState | null>(null);
  const [noticeState, setNoticeState] = useState<NoticeState | null>(null);
  const identityStateRef = useRef(identityState);
  const peerStateRef = useRef(peerState);
  const outboxStateRef = useRef(outboxState);
  const actorRef = useRef(actorID);
  const authorityRef = useRef(authorityState);
  const refreshAuthorityRef = useRef(onRefreshAuthority);
  const identityRequests = useRef(0);
  const peerRequests = useRef(0);
  const outboxRequests = useRef(0);
  const outboxDetailRequests = useRef(0);

  actorRef.current = actorID;
  identityStateRef.current = identityState;
  peerStateRef.current = peerState;
  outboxStateRef.current = outboxState;
  authorityRef.current = authorityState;
  refreshAuthorityRef.current = onRefreshAuthority;

  const authority =
    authorityState.status === "ready" && authorityState.userID === actorID
      ? authorityState.authority
      : null;
  const hasPermission = useCallback(
    (section: SectionName, expectedActorID: string) => {
      const current = authorityRef.current;
      if (
        actorRef.current !== expectedActorID ||
        current.status !== "ready" ||
        current.userID !== expectedActorID
      )
        return false;
      return section === "identity"
        ? current.authority.can_manage_instance_roles
        : current.authority.is_admin;
    },
    [],
  );

  const loadSection = useCallback(
    async <T,>(
      section: SectionName,
      path: string,
      requestCounter: RequestCounter,
      setSection: Dispatch<SetStateAction<SectionState<T>>>,
      fallbackMessage: string,
    ): Promise<void> => {
      const requestActorID = actorID;
      const version = authorityRef.current;
      if (!hasPermission(section, requestActorID)) return;
      const requestID = ++requestCounter.current;
      const isCurrent = () =>
        requestCounter.current === requestID &&
        actorRef.current === requestActorID &&
        authorityRef.current === version &&
        hasPermission(section, requestActorID);

      setSection((current) => ({
        actorID: requestActorID,
        authorityVersion: version,
        loading: true,
        data:
          current.actorID === requestActorID && !current.retryRequired
            ? current.data
            : null,
        error: "",
        retryRequired: false,
      }));
      try {
        const data = await federationRequest<T>(path);
        if (!isCurrent()) return;
        setSection({
          actorID: requestActorID,
          authorityVersion: version,
          loading: false,
          data,
          error: "",
          retryRequired: false,
        });
      } catch (loadError: unknown) {
        if (!isCurrent()) return;
        const forbidden =
          loadError instanceof FederationRequestError &&
          loadError.status === 403;
        setSection((current) => ({
          actorID: requestActorID,
          authorityVersion: version,
          loading: false,
          data: forbidden
            ? null
            : current.actorID === requestActorID
              ? current.data
              : null,
          error: requestMessage(loadError) || fallbackMessage,
          retryRequired: forbidden,
        }));
        if (forbidden) refreshAuthorityRef.current();
      }
    },
    [actorID, hasPermission],
  );

  const loadIdentity = useCallback(
    () =>
      loadSection(
        "identity",
        "/api/admin/federation/identity",
        identityRequests,
        setIdentityState,
        "Could not load the instance signing identity.",
      ),
    [loadSection],
  );
  const loadPeers = useCallback(
    () =>
      loadSection(
        "peers",
        "/api/admin/federation/peers",
        peerRequests,
        setPeerState,
        "Could not load trusted peers.",
      ),
    [loadSection],
  );
  const loadOutbox = useCallback(async () => {
    const requestActorID = actorID;
    const version = authorityRef.current;
    if (!hasPermission("outbox", requestActorID)) return;
    const requestID = ++outboxRequests.current;
    const isCurrent = () =>
      outboxRequests.current === requestID &&
      actorRef.current === requestActorID &&
      authorityRef.current === version &&
      hasPermission("outbox", requestActorID);
    setOutboxState((current) => ({
      actorID: requestActorID,
      authorityVersion: version,
      loading: true,
      data:
        current.actorID === requestActorID && !current.retryRequired
          ? current.data
          : null,
      error: "",
      retryRequired: false,
    }));
    try {
      const deliveries = await federationRequest<FederationDelivery[]>(
        "/api/admin/federation/outbox",
      );
      if (!isCurrent()) return;
      setOutboxState({
        actorID: requestActorID,
        authorityVersion: version,
        loading: false,
        data: { deliveries, selected: null },
        error: "",
        retryRequired: false,
      });
    } catch (loadError: unknown) {
      if (!isCurrent()) return;
      const forbidden =
        loadError instanceof FederationRequestError && loadError.status === 403;
      setOutboxState((current) => ({
        actorID: requestActorID,
        authorityVersion: version,
        loading: false,
        data: forbidden
          ? null
          : current.actorID === requestActorID
            ? current.data
            : null,
        error: requestMessage(loadError) || "Could not load the delivery outbox.",
        retryRequired: forbidden,
      }));
      if (forbidden) refreshAuthorityRef.current();
    }
  }, [actorID, hasPermission]);

  useEffect(() => {
    if (
      authorityState.status !== "ready" ||
      authorityState.userID !== actorID
    ) {
      identityRequests.current += 1;
      peerRequests.current += 1;
      outboxRequests.current += 1;
      outboxDetailRequests.current += 1;
      return;
    }

    const currentAuthority = authorityState.authority;
    if (currentAuthority.can_manage_instance_roles) {
      const current = identityStateRef.current;
      if (
        current.actorID !== actorID ||
        (!current.retryRequired && current.authorityVersion !== authorityState)
      )
        void loadIdentity();
    } else {
      identityRequests.current += 1;
      setIdentityState(emptySection(actorID));
    }

    if (currentAuthority.is_admin) {
      const currentPeers = peerStateRef.current;
      if (
        currentPeers.actorID !== actorID ||
        (!currentPeers.retryRequired && currentPeers.authorityVersion !== authorityState)
      )
        void loadPeers();
      const currentOutbox = outboxStateRef.current;
      if (
        currentOutbox.actorID !== actorID ||
        (!currentOutbox.retryRequired && currentOutbox.authorityVersion !== authorityState)
      )
        void loadOutbox();
    } else {
      peerRequests.current += 1;
      outboxRequests.current += 1;
      setPeerState(emptySection(actorID));
      setOutboxState(emptySection(actorID));
    }
  }, [actorID, authorityState, loadIdentity, loadOutbox, loadPeers]);

  useEffect(
    () => () => {
      identityRequests.current += 1;
      peerRequests.current += 1;
      outboxRequests.current += 1;
      outboxDetailRequests.current += 1;
    },
    [],
  );

  const identityView = sectionView(identityState, actorID, authorityState);
  const peersView = sectionView(peerState, actorID, authorityState);
  const outboxView = sectionView(outboxState, actorID, authorityState);
  const origin = originState.actorID === actorID ? originState.value : "";
  const keyID = keyIDState.actorID === actorID ? keyIDState.value : "";
  const publicKey = publicKeyState.actorID === actorID ? publicKeyState.value : "";
  const busyFor = (section: SectionName) =>
    busyState?.actorID === actorID && busyState.section === section;
  const noticeFor = (section: SectionName) =>
    noticeState?.actorID === actorID && noticeState.section === section
      ? noticeState.message
      : "";

  function permissionRetry<T>(
    setSection: Dispatch<SetStateAction<SectionState<T>>>,
  ) {
    setSection((current) => ({
      ...current,
      actorID,
      error: "",
      retryRequired: false,
      loading: true,
    }));
    refreshAuthorityRef.current();
  }

  function setFailure<T>(
    setSection: Dispatch<SetStateAction<SectionState<T>>>,
    error: unknown,
    version: InstanceAuthorityState,
  ) {
    const forbidden =
      error instanceof FederationRequestError &&
      error.status === 403 &&
      error.code !== "reauthentication_required";
    setSection((current) => ({
      actorID,
      authorityVersion: version,
      loading: false,
      data: forbidden
        ? null
        : current.actorID === actorID
          ? current.data
          : null,
      error: requestMessage(error),
      retryRequired: forbidden,
    }));
    if (forbidden) refreshAuthorityRef.current();
  }

  type Requester = <T,>(path: string, options?: RequestInit) => Promise<T>;

  async function runMutation(
    section: SectionName,
    action: (request: Requester) => Promise<void>,
    successMessage: string,
  ) {
    const version = authorityState;
    if (
      !authority ||
      (section === "identity"
        ? !authority.can_manage_instance_roles
        : !authority.is_admin)
    )
      return;
    const requestActorID = actorID;
    const isCurrent = () =>
      actorRef.current === requestActorID &&
      authorityRef.current === version &&
      hasPermission(section, requestActorID);
    const requester: Requester = async <T,>(path: string, options?: RequestInit) => {
      if (!isCurrent()) throw staleRequest;
      const value = await federationRequest<T>(path, options);
      if (!isCurrent()) throw staleRequest;
      return value;
    };

    setBusyState({ actorID: requestActorID, section });
    setNoticeState(null);
    if (section === "identity")
      setIdentityState((current) => ({ ...current, error: "" }));
    else if (section === "peers")
      setPeerState((current) => ({ ...current, error: "" }));
    else setOutboxState((current) => ({ ...current, error: "" }));

    try {
      await action(requester);
      if (!isCurrent()) return;
      setNoticeState({ actorID: requestActorID, section, message: successMessage });
      refreshAuthorityRef.current();
    } catch (actionError: unknown) {
      if (actionError === staleRequest || actorRef.current !== requestActorID)
        return;
      if (section === "identity")
        setFailure(setIdentityState, actionError, version);
      else if (section === "peers")
        setFailure(setPeerState, actionError, version);
      else setFailure(setOutboxState, actionError, version);
    } finally {
      if (actorRef.current === requestActorID) setBusyState(null);
    }
  }

  async function viewDelivery(delivery: FederationDelivery) {
    const version = authorityState;
    const requestActorID = actorID;
    const requestID = ++outboxDetailRequests.current;
    const isCurrent = () =>
      outboxDetailRequests.current === requestID &&
      actorRef.current === requestActorID &&
      authorityRef.current === version &&
      hasPermission("outbox", requestActorID);
    try {
      const selected = await federationRequest<FederationDelivery>(
        `/api/admin/federation/outbox?id=${encodeURIComponent(delivery.id)}`,
      );
      if (!isCurrent()) return;
      setOutboxState((current) => ({
        ...current,
        actorID: requestActorID,
        authorityVersion: version,
        data: current.data
          ? { ...current.data, selected }
          : { deliveries: [], selected },
        error: "",
      }));
    } catch (detailError: unknown) {
      if (!isCurrent()) return;
      if (
        detailError instanceof FederationRequestError &&
        detailError.status === 404
      ) {
        await loadOutbox();
        if (!isCurrent()) return;
        setOutboxState((current) => ({
          ...current,
          error: "This queued event no longer exists. The outbox was refreshed.",
          data: current.data
            ? { ...current.data, selected: null }
            : current.data,
        }));
        return;
      }
      setFailure(
        setOutboxState,
        detailError,
        version,
      );
    }
  }

  const ready = authority !== null;
  if (!ready) return null;
  if (!authority.can_manage_instance_roles && !authority.is_admin) return null;

  const identityUnavailable = !authority.can_manage_instance_roles;
  const identityBusy = busyFor("identity");
  const peerBusy = busyFor("peers");
  const outboxBusy = busyFor("outbox");

  return (
    <>
      {authority.is_admin && (
        <section className="cookbook-dashboard" aria-labelledby="federation-peers-title">
          <div className="section-heading">
            <div>
              <span className="eyebrow">Federation administration</span>
              <h2 id="federation-peers-title">Trusted peers</h2>
            </div>
          </div>
          {peersView.loading && !peersView.data && (
            <p role="status">Loading trusted peers…</p>
          )}
          {peersView.error && (
            <div>
              <p className="form-message" role="alert">{peersView.error}</p>
              {peersView.retryRequired ? (
                <button
                  className="text-link"
                  type="button"
                  onClick={() => permissionRetry(setPeerState)}
                >
                  Refresh permissions and retry peers
                </button>
              ) : (
                <button className="text-link" type="button" onClick={() => void loadPeers()}>
                  Retry peers
                </button>
              )}
              {peersView.error.includes("Sign in again") && (
                <p><a href="/?signin=1">Sign in again</a></p>
              )}
            </div>
          )}
          {noticeFor("peers") && (
            <p className="form-message" role="status">{noticeFor("peers")}</p>
          )}
          {!peersView.retryRequired && (
            <form
              className="admin-settings-form"
              onSubmit={(event: FormEvent<HTMLFormElement>) => {
                event.preventDefault();
                void runMutation(
                  "peers",
                  async (request) => {
                    await request("/api/admin/federation/peers", {
                      method: "POST",
                      body: JSON.stringify({ origin, enabled: false }),
                    });
                    await request("/api/admin/federation/keys", {
                      method: "POST",
                      body: JSON.stringify({ origin, key_id: keyID, public_key: publicKey }),
                    });
                    await request("/api/admin/federation/peers", {
                      method: "POST",
                      body: JSON.stringify({ origin, enabled: true }),
                    });
                    setOriginState({ actorID, value: "" });
                    setKeyIDState({ actorID, value: "" });
                    setPublicKeyState({ actorID, value: "" });
                  },
                  "Peer approved and enabled.",
                );
              }}
            >
              <h3>Add a trusted peer</h3>
              <label htmlFor="federation-peer-origin">Peer HTTPS origin</label>
              <input
                id="federation-peer-origin"
                required
                value={origin}
                onChange={(event) => setOriginState({ actorID, value: event.target.value })}
                placeholder="https://cookbook.example.org"
                disabled={peerBusy}
              />
              <label htmlFor="federation-peer-key-id">Peer key ID</label>
              <input
                id="federation-peer-key-id"
                required
                value={keyID}
                onChange={(event) => setKeyIDState({ actorID, value: event.target.value })}
                disabled={peerBusy}
              />
              <label htmlFor="federation-peer-public-key">
                Peer Ed25519 public key (base64)
              </label>
              <input
                id="federation-peer-public-key"
                required
                value={publicKey}
                onChange={(event) => setPublicKeyState({ actorID, value: event.target.value })}
                disabled={peerBusy}
              />
              <button className="button button--green" type="submit" disabled={peerBusy}>
                {peerBusy ? "Approving peer…" : "Approve and enable peer"}
              </button>
            </form>
          )}
          {peersView.data?.length === 0 && (
            <p className="empty-state">No peers have been configured.</p>
          )}
          {peersView.data?.map((peer) => (
            <details className="admin-settings-note" key={peer.origin}>
              <summary>
                {peer.origin} · {peer.enabled ? "enabled" : "disabled"}
              </summary>
              {peer.keys.map((key) => (
                <div key={key.key_id}>
                  <p><code>{key.key_id}</code> · {key.status}</p>
                  {key.status === "active" && (
                    <button
                      className="text-link"
                      type="button"
                      disabled={peerBusy}
                      onClick={() => void runMutation(
                        "peers",
                        async (request) => {
                          await request("/api/admin/federation/keys", {
                            method: "DELETE",
                            body: JSON.stringify({ origin: peer.origin, key_id: key.key_id }),
                          });
                        },
                        "Peer key revoked.",
                      )}
                    >
                      Revoke key
                    </button>
                  )}
                </div>
              ))}
              <form
                onSubmit={(event) => {
                  event.preventDefault();
                  const form = new FormData(event.currentTarget);
                  const rotatedKeyID = form.get("key_id");
                  const rotatedPublicKey = form.get("public_key");
                  if (typeof rotatedKeyID !== "string" || typeof rotatedPublicKey !== "string")
                    return;
                  void runMutation(
                    "peers",
                    async (request) => {
                      await request("/api/admin/federation/keys", {
                        method: "POST",
                        body: JSON.stringify({
                          origin: peer.origin,
                          key_id: rotatedKeyID,
                          public_key: rotatedPublicKey,
                        }),
                      });
                    },
                    "Peer key added. The previous key remains active until revoked.",
                  );
                  event.currentTarget.reset();
                }}
              >
                <label>Next key ID <input name="key_id" required disabled={peerBusy} /></label>
                <label>
                  Next Ed25519 public key (base64) <input name="public_key" required disabled={peerBusy} />
                </label>
                <button className="text-link" type="submit" disabled={peerBusy}>
                  Add rotation key
                </button>
              </form>
              <button
                className="text-link"
                type="button"
                disabled={peerBusy}
                onClick={() => void runMutation(
                  "peers",
                  async (request) => {
                    if (peer.enabled)
                      await request("/api/admin/federation/peers", {
                        method: "DELETE",
                        body: JSON.stringify({ origin: peer.origin }),
                      });
                    else
                      await request("/api/admin/federation/peers", {
                        method: "POST",
                        body: JSON.stringify({ origin: peer.origin, enabled: true }),
                      });
                  },
                  peer.enabled ? "Peer disabled." : "Peer enabled.",
                )}
              >
                {peer.enabled ? "Disable peer" : "Enable peer"}
              </button>
            </details>
          ))}
        </section>
      )}

      {authority.is_admin && (
        <section className="cookbook-dashboard" aria-labelledby="federation-outbox-title">
          <div className="section-heading">
            <div>
              <span className="eyebrow">Bounded durable delivery</span>
              <h2 id="federation-outbox-title">Outbox</h2>
            </div>
            <button
              className="text-link"
              type="button"
              disabled={outboxBusy}
              onClick={() => {
                setNoticeState(null);
                refreshAuthorityRef.current();
              }}
            >
              Refresh
            </button>
          </div>
          {outboxView.loading && !outboxView.data && (
            <p role="status">Loading the delivery outbox…</p>
          )}
          {outboxView.error && (
            <div>
              <p className="form-message" role="alert">{outboxView.error}</p>
              {outboxView.retryRequired ? (
                <button
                  className="text-link"
                  type="button"
                  onClick={() => permissionRetry(setOutboxState)}
                >
                  Refresh permissions and retry outbox
                </button>
              ) : (
                <button className="text-link" type="button" onClick={() => void loadOutbox()}>
                  Retry outbox
                </button>
              )}
              {outboxView.error.includes("Sign in again") && (
                <p><a href="/?signin=1">Sign in again</a></p>
              )}
            </div>
          )}
          {noticeFor("outbox") && (
            <p className="form-message" role="status">{noticeFor("outbox")}</p>
          )}
          {outboxView.data?.deliveries.length === 0 && (
            <p className="empty-state">No queued deliveries.</p>
          )}
          {outboxView.data?.deliveries.map((delivery) => (
            <details className="admin-settings-note" key={delivery.id}>
              <summary>
                {delivery.peer_origin} · sequence {delivery.sequence} · {delivery.attempts}/12 attempts
              </summary>
              <p>{delivery.last_error || "Waiting for delivery."}</p>
              <button
                className="text-link"
                type="button"
                disabled={outboxBusy}
                onClick={() => void viewDelivery(delivery)}
              >
                View event
              </button>
              <button
                className="text-link"
                type="button"
                disabled={outboxBusy}
                onClick={() => void runMutation(
                  "outbox",
                  async (request) => {
                    await request("/api/admin/federation/outbox", {
                      method: "POST",
                      body: JSON.stringify({ id: delivery.id }),
                    });
                  },
                  "Delivery retry scheduled.",
                )}
              >
                Retry
              </button>
              <button
                className="text-link"
                type="button"
                disabled={outboxBusy}
                onClick={() => {
                  if (!window.confirm(
                    "Discard every queued event for this recipe across all peers? A peer may keep the last copy it received.",
                  )) return;
                  void runMutation(
                    "outbox",
                    async (request) => {
                      await request("/api/admin/federation/outbox", {
                        method: "DELETE",
                        body: JSON.stringify({ id: delivery.id }),
                      });
                    },
                    "Queued object events discarded.",
                  );
                }}
              >
                Discard object events
              </button>
            </details>
          ))}
          {outboxView.data?.selected && (
            <pre className="admin-settings-note" aria-label="Selected federation event">
              {JSON.stringify(outboxView.data.selected.payload, null, 2)}
            </pre>
          )}
        </section>
      )}

      {identityUnavailable && authority.is_admin && (
        <section className="cookbook-dashboard" aria-labelledby="federation-identity-unavailable">
          <span className="eyebrow">Instance transport identity</span>
          <h2 id="federation-identity-unavailable">Instance signing identity unavailable</h2>
          <p>Only the current instance owner can manage the signing identity after ownership is claimed.</p>
        </section>
      )}

      {authority.can_manage_instance_roles && (
        <section className="cookbook-dashboard" aria-labelledby="federation-identity-title">
          <div className="section-heading">
            <div>
              <span className="eyebrow">Instance transport identity</span>
              <h2 id="federation-identity-title">Instance signing identity</h2>
            </div>
          </div>
          <p>This key signs this instance’s federation traffic. It is not a personal account or passkey identity.</p>
          {identityView.loading && !identityView.data && (
            <p role="status">Loading the instance signing identity…</p>
          )}
          {identityView.error && (
            <div>
              <p className="form-message" role="alert">{identityView.error}</p>
              {identityView.retryRequired ? (
                <button
                  className="text-link"
                  type="button"
                  onClick={() => permissionRetry(setIdentityState)}
                >
                  Refresh permissions and retry identity
                </button>
              ) : (
                <button className="text-link" type="button" onClick={() => void loadIdentity()}>
                  Retry identity
                </button>
              )}
              {identityView.error.includes("Sign in again") && (
                <p><a href="/?signin=1">Sign in again</a></p>
              )}
            </div>
          )}
          {noticeFor("identity") && (
            <p className="form-message" role="status">{noticeFor("identity")}</p>
          )}
          {identityView.data && (
            <details className="admin-settings-note" open>
              <summary>Public signing key</summary>
              <p>
                Origin: {identityView.data.origin || "Configure COOKBOOK_FEDERATION_ORIGIN before enabling federation."}
              </p>
              <p>Key ID: <code>{identityView.data.key_id}</code></p>
              <label htmlFor="federation-public-key">Ed25519 public key</label>
              <textarea
                id="federation-public-key"
                readOnly
                value={identityView.data.public_key}
                rows={2}
              />
              <small>The private key stays in the server’s persistent secret directory.</small>
              {identityView.data.pending_key_id ? (
                <div>
                  <p>Staged rotation key: <code>{identityView.data.pending_key_id}</code></p>
                  <label htmlFor="federation-pending-public-key">Pending Ed25519 public key</label>
                  <textarea
                    id="federation-pending-public-key"
                    readOnly
                    value={identityView.data.pending_public_key ?? ""}
                    rows={2}
                  />
                  <p>
                    Add this key at every peer before activation. Their old key can remain active during the overlap and be revoked after they confirm the new key works.
                  </p>
                  <button
                    className="button button--green"
                    type="button"
                    disabled={identityBusy}
                    onClick={() => void runMutation(
                      "identity",
                      async (request) => {
                        await request("/api/admin/federation/identity/rotation", {
                          method: "POST",
                          body: JSON.stringify({ action: "activate" }),
                        });
                      },
                      "Signing key activated. Confirm delivery at each peer, then revoke the old key there.",
                    )}
                  >
                    {identityBusy ? "Activating…" : "Activate staged key"}
                  </button>
                </div>
              ) : (
                <button
                  className="text-link"
                  type="button"
                  disabled={identityBusy}
                  onClick={() => void runMutation(
                    "identity",
                    async (request) => {
                      await request("/api/admin/federation/identity/rotation", {
                        method: "POST",
                        body: JSON.stringify({ action: "stage" }),
                      });
                    },
                    "A new signing key is staged. Add its public key at every peer before activation.",
                  )}
                >
                  {identityBusy ? "Staging…" : "Stage signing-key rotation"}
                </button>
              )}
            </details>
          )}
        </section>
      )}
    </>
  );
}
