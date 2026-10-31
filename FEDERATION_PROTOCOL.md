# Cookbook exchange protocol v1

Status: opt-in federation is implemented end to end. This includes a signed inbox, strict event validation, replay-safe receipts, separate remote recipe state, public remote recipe listing, peer/key administration, staged local signing-key rotation, a bounded transactional outbox and retry worker, and administrator queue inspection/recovery. The admin UI is available to administrators. Egress pins validated public DNS results, preserves TLS hostname checks, rejects redirects, and limits acknowledgement reads. Backend tests cover independent sender/receiver databases, publish/update/unpublish/delete, duplicate delivery, replay rejection, offline retry, revocation, redirect rejection, and DNS changes. Federation remains disabled by default. See [FEDERATION_RESEARCH.md](FEDERATION_RESEARCH.md) for the threat model and verification notes.

## Scope and identities

Protocol v1 exchanges public recipe snapshots between instances whose administrators have approved one another. It does not federate accounts, cookbook membership, private or unlisted content, edits to locally owned recipes, images, or arbitrary URLs.

Each instance configures one HTTPS `federation_origin` before federation is enabled. The application canonicalizes and persists it; once federation is enabled, the persisted origin is locked even before the first event. This conservative lock prevents identity drift without needing to infer whether a queue or peer already observed an event. The origin is a scheme and authority only: no credentials, path, query, fragment, IP literal, localhost, or non-default port. An origin/ID migration must have its own rollback and compatibility tests. Recipe object IDs are the origin plus `/federation/recipes/` and the existing 32-character lowercase hex recipe ID. Database IDs remain stable across ordinary schema migrations.

An administrator adds a peer with its HTTPS origin and active key ID/public key through the administrator panel. Peer URLs and keys never come from an event. Peer records and key changes are audited. Disabling a peer clears queued deliveries immediately while preserving per-peer publication state; re-enabling it queues current snapshots and tombstones for content it previously received. Revoking a peer's last active key also disables outbound delivery and clears that peer's queue. Key rotation allows a short, explicit overlap between the old and new key IDs; revocation removes the old key from verification rather than accepting it indefinitely.

Each instance's signing key is Ed25519, generated locally and stored as PKCS#8 PEM in a persistent secret directory with owner-only permissions. It is never stored in SQLite, exposed by the API, or passed in command-line arguments. Startup fails closed if the private key is malformed or broadly readable. The administrator's federation identity endpoint displays only the origin, key ID, and public key; the private key remains in the configured secret directory. For local rotation, stage a new key in the administration panel, add that public key at every peer, activate it, verify delivery, then revoke the old key at each peer after the overlap. Activation is atomic, and the worker reloads the active key for each delivery. Revoked key IDs cannot be reactivated. Backups that include this directory must receive the same protection as database backups. RFC 9421 defines EdDSA using Ed25519 in [Section 3.3.6](https://www.rfc-editor.org/rfc/rfc9421.html#section-3.3.6); the peer profile fixes this algorithm and does not negotiate algorithms from request data.

## Event envelope

All events are UTF-8 JSON with a 2 MiB maximum body, matching the bounded local recipe payload with headroom for JSON encoding. Unknown fields and event types are rejected. Duplicate JSON object keys, invalid UTF-8, trailing values, and values outside the documented bounds are rejected. The version 1 envelope is:

```json
{
  "protocol": "cookbook-exchange",
  "schema_version": 1,
  "event_id": "32 lowercase hex characters",
  "event_type": "recipe.snapshot",
  "source_origin": "https://cookbook.example",
  "object_id": "https://cookbook.example/federation/recipes/0123456789abcdef0123456789abcdef",
  "sequence": 1,
  "occurred_at": "2026-10-31T12:00:00Z",
  "attribution": {"creator_name": "A. Cook"},
  "recipe": {
    "title": "Tomato soup",
    "description": "A short note",
    "ingredients": [{"name": "tomatoes", "quantity": "4", "notes": "ripe"}],
    "steps": [{"text": "Simmer."}],
    "servings": 4,
    "prep_time_minutes": 10,
    "cook_time_minutes": 25,
    "tags": ["soup"]
  }
}
```

The allowlist is exhaustive: no local owner/account IDs, cookbook IDs/names, member identities, internal revisions, audit data, visibility flags, image paths, or remote media URLs. Text is plain text and length/count limits match recipe creation. `recipe.snapshot` is used for initial publish and explicit republish. `recipe.update` carries the same complete normalized public snapshot. `recipe.tombstone` has no `recipe` or `attribution` field and signals unpublish, deletion, or peer revocation. Every event has a fresh event ID and a sequence strictly greater than the previous sequence for that source object. Re-publishing after a tombstone uses the next sequence; it never resets the counter.

`occurred_at` is informational and cannot authorize an update. Receivers order by the monotonic `sequence`, scoped to the approved peer and object ID. Receivers store remote copies separately from local recipes, preserve the source attribution and origin, and never apply an inbound event to a locally owned recipe.

## HTTP authentication and idempotency

Instances send `POST /api/federation/inbox` with `Content-Type: application/json`, an explicit `Content-Length`, RFC 9530 `Content-Digest: sha-256=:...:`, and RFC 9421 `Signature-Input`/`Signature` fields. The `cookbook-exchange-v1` signature tag covers, in order, `@method`, `@target-uri`, `content-type`, `content-length`, and `content-digest`. Signature parameters require `created`, `keyid`, and a cryptographically random `nonce`; the client-side `alg` parameter is forbidden. The key ID must resolve to a currently approved Ed25519 public key for the origin named in the event. The receiver verifies the exact raw body digest and signature before JSON decoding. Each nonce is atomically marked seen and retained beyond the five-minute signature window; duplicate nonces are rejected. The implementation uses `github.com/common-fate/httpsig` v0.2.1, whose signer and verifier support Ed25519, RFC 9530 body digesting, strict covered-component checks, and pluggable key/nonce stores. Requests without a length or over 2 MiB are rejected before signature verification.

To configure peers, set `COOKBOOK_FEDERATION_ORIGIN`, then obtain the local public identity from `GET /api/admin/federation/identity`. Create a remote peer disabled with `POST /api/admin/federation/peers`, add its public key with `POST /api/admin/federation/keys`, enable the peer with `POST /api/admin/federation/peers`, then opt in through `PATCH /api/admin/settings` with `federation_enabled: true`. These mutations require a recent administrator authentication. The administrator panel provides these controls and key rotation. `GET /api/public/remote-recipes` lists received copies when the local instance is public. `/api/admin/federation/outbox` supports queue inspection and recovery; discarding an object's queued delivery events removes its queued successors, may leave peers with the last copy they received, and makes the next public change send a fresh snapshot.

The signature parameters require `created`, `keyid`, and a fresh nonce; `created` must be within five minutes of the receiver's clock. Each delivery attempt signs again with a fresh timestamp and nonce. After authentication, the receiver validates the origin/object-ID relationship, event schema, bounds, publication allowlist, and sequence before writing any state.

Receipt, event-ID deduplication, sequence advancement, and remote recipe/tombstone changes commit in one SQLite transaction. A repeated event ID with the same digest returns the original success response without reapplying it; the same event ID with a different digest is rejected. A different event ID with a sequence less than or equal to the latest accepted sequence is rejected as stale. Accepted events return `202` with a bounded acknowledgement body. Failed validation writes no application state.

## Queue, retry, and peer egress

Local public recipe mutations enqueue an allowlisted immutable event in the same transaction as the recipe/revision change. Unpublish and deletion enqueue a tombstone before future content events can be created. The setting is disabled by default, and publishing requires both the instance federation switch and explicit public recipe visibility. A transition to private/unlisted or a deleted recipe prevents new snapshots immediately.

The queue is durable in SQLite, capped at 1,000 pending events per peer, 10,000 total, 16 MiB per peer, and 64 MiB total. A single bounded worker processes deliveries sequentially and preserves per-peer, per-object sequence order. Each attempt has a 10-second total timeout, does not follow redirects, and reads at most 64 KiB of response data. Failures retry with exponential backoff from one minute to six hours, with jitter, for at most 12 attempts; exhausted events remain inspectable and require an administrator retry or discard. One slow/offline peer cannot delay recipe writes or another peer. Queue payloads and peer key material are never exposed through public APIs.

Outbound requests go only to administrator-configured HTTPS origins. URL parsing rejects userinfo, fragments, paths beyond the fixed inbox route, IP literals, localhost, and non-HTTPS schemes. Resolution rejects the destination if any returned address is loopback, private, link-local, multicast, unspecified, or otherwise non-global-unicast. The transport pins the validated resolved address for that request while preserving hostname-based TLS verification; it revalidates on every new connection, uses no proxy environment variables, and disables redirects. Production configuration has no private-address exception. Two-instance tests use an injected test transport rather than weakening production checks.

## Verification coverage and limits

Backend tests cover origin stability, public-field allowlisting, malformed and oversized input, signatures and digest mismatch, unknown/revoked keys, stale signatures, replay and event-ID conflicts, atomic receipt/sequence updates, bounded queue behavior, retries, peer revocation, redirect rejection, public/private and mixed DNS answers, and a DNS-change check at each new connection. Separate SQLite databases exercise signed publish, update, duplicate delivery, unpublish, republish, and deletion; retry recovery and revoked-key rejection have focused tests. The frontend test, lint, and production build checks cover the UI source.

The two-database sender/receiver test uses an in-process HTTP transport to keep it deterministic; it does not exercise a public network or a live TLS handshake. Before enabling federation between real instances, operators should exchange keys over a trusted channel, configure both sides, and verify delivery and TLS through their actual reverse-proxy setup. Ordinary installations keep federation disabled until an administrator configures it.

## Design limits

Federation communicates public copies only. A source can send a tombstone and stop future delivery, but cannot guarantee erasure of a peer's prior copies or backups. Operators must configure trusted peers and protect private signing keys. Protocol v1 has no shared editing, group delivery, automatic peer discovery, or image transfer.
