# Federation protocol comparison and threat model

Status: research and design only. No federation endpoints, peer calls, or background delivery are enabled or implemented. Federation remains off by default. The proposed version 1 wire and delivery profile is in [FEDERATION_PROTOCOL.md](FEDERATION_PROTOCOL.md); its design does not authorize enabling federation before the remaining gates pass.

Research checked 2026-10-31 against the linked primary specifications.

## Scope being evaluated

The first federation milestone should answer a narrow product question: can two independently operated Cookbook instances exchange explicitly public recipe snapshots while preserving source attribution, revision order, and deletion signals? This is separate from public browsing and import/export. It does not include shared editing, private cookbook membership, account federation, recommendations, or an open social graph.

## Protocol comparison

| | ActivityPub | Signed Cookbook exchange |
|---|---|---|
| Standard base | W3C Recommendation; server-to-server federation and an ActivityStreams 2.0 data model with actors, activities, inboxes, outboxes, and collections. | A small Cookbook-specific JSON event envelope delivered over HTTPS. Use RFC 9421 HTTP Message Signatures and sign an RFC 9530 `Content-Digest` field so the signature covers the body bytes. |
| Interoperability | Strongest when interoperating with the wider ActivityPub ecosystem. ActivityStreams allows shared objects and vocabularies to be extended, but Cookbook-specific recipe and revision semantics would still need an agreed vocabulary and consistent implementation. | Interoperates only among implementations of the Cookbook protocol. The schema can match recipes, attribution, ordered revisions, and tombstones directly, with less ambiguity for this product. |
| Security model | ActivityPub describes actor identity and delivery, but does not normatively select one message-signature mechanism. The specification recommends validating received content and points to signatures as a stronger option. Implementers must choose and consistently apply authentication, replay protection, and fetch policy. | HTTP signature and digest formats are standardized. Cookbook must still define key enrollment/rotation, peer authorization, freshness/replay rules, canonical event IDs, payload validation, and delivery policy. Static administrator-approved peers make the initial trust model explicit. |
| Runtime and operations | Requires actors, actor/object URLs, inbox processing, delivery retries, recipient rules, collections, and interoperable treatment of activities. It also creates a larger dereferencing and compatibility surface. | Can be limited to one outbound event queue and one inbound endpoint per peer, with a fixed set of event types, a bounded retry policy, and no automatic discovery. Smaller implementation surface, but a custom protocol to maintain. |
| Recipe lifecycle fit | Create, Update, and Delete activities can express changes, but exact recipe schema, revision ordering, and remote deletion retention need application conventions. | Explicit `recipe.snapshot`, `recipe.update`, and `recipe.tombstone` events can carry source ID, monotonically increasing sequence, attribution, and schema version. |
| Suitable first use | Choose if broad social discovery and cross-application participation are explicit product requirements. | Better fit for the first limited instance-to-instance public recipe exchange, subject to the safeguards and tests below. |

ActivityPub is based on ActivityStreams 2.0 and defines both client-to-server and server-to-server protocols. Its actor and inbox/outbox model is designed for the social web, while server operators may implement the federated server profile independently of the client protocol. [ActivityPub Recommendation](https://www.w3.org/TR/activitypub/)

ActivityStreams has extensible object and activity types, which can represent recipe records, but implementations would need to agree on the recipe vocabulary and revision semantics for consistent cross-application behavior. [ActivityStreams 2.0 Core](https://www.w3.org/TR/activitystreams-core/)

ActivityPub says servers should validate received content and notes that signatures can help, but it does not authoritatively specify a verification mechanism. [ActivityPub object validation guidance](https://www.w3.org/TR/activitypub/#security-considerations)

RFC 9421 defines signing and verifying HTTP message components. RFC 9530 defines `Content-Digest` for HTTP message content integrity; a Cookbook profile should require that digest and include it in the signature's covered components. [RFC 9421](https://www.rfc-editor.org/rfc/rfc9421.html), [RFC 9530](https://www.rfc-editor.org/rfc/rfc9530.html)

## Provisional recommendation

If the goal remains opt-in sharing of public recipes between Cookbook instances, begin with a signed Cookbook exchange over HTTPS and keep ActivityPub as a later interoperability option. This keeps the first release aligned with recipe snapshots, ordered revisions, and tombstones, without committing the application to a general social graph. This recommendation is an inference from the stated product scope and the protocol capabilities above; it should be revisited if broad ActivityPub interoperability becomes a requirement.

The initial exchange profile should have these boundaries:

- Administrators manually configure and approve each remote peer and its public key. Do not accept peer URLs or keys from recipe content and do not perform automatic peer discovery.
- Publish only normalized recipes whose visibility is explicitly `public` on an instance where public publishing is enabled. Private and unlisted recipes, private cookbook names, member identities, session data, audit data, and internal revision notes never enter an event.
- Use absolute source IDs derived from the configured public origin and local stable IDs. Include protocol/schema version, event ID, source object ID, increasing sequence, event time, source attribution, and normalized public fields.
- Send only public snapshots, updates, and tombstones. Do not support remote editing, arbitrary activities, follower graphs, or public-to-private transitions.
- Sign the HTTP method, target URI, creation time, key ID, and `Content-Digest`; verify all required covered components and the body digest before decoding or acting on the payload.
- Maintain a durable outbound queue with bounded retries and idempotent receiver storage keyed by peer and event ID. Reject replays and stale/out-of-order updates; retain tombstones long enough to prevent deleted recipes from reappearing.
- Do not fetch remote image URLs in the first exchange. Recipe image transfer can be designed separately with explicit size/type limits and the same peer trust rules.

## Threat model

| Threat | Required control and verification evidence |
|---|---|
| A private, unlisted, or deleted recipe leaks through export or a queued event | Build the outbound representation from the same centralized visibility policy as local public views; serialize an explicit allowlist of fields. Test private, unlisted, trashed, and public recipes against event creation, retries, and tombstones. |
| Forged origin or modified body | Require an administrator-approved peer key; verify RFC 9421 signatures and an RFC 9530 `Content-Digest` before parsing. Test bad key, missing signed fields, altered digest/body, unknown key ID, and key revocation. |
| Replay, duplicate delivery, and out-of-order updates | Require signed creation time within a configured skew, event ID uniqueness, and monotonic per-object sequence. Persist deduplication state atomically with application. Test repeat, stale timestamp, lower sequence, and a valid later sequence. |
| SSRF, DNS rebinding, redirects, or local-network probing | Deliver only to configured HTTPS peer origins; never follow URLs from event content. Disable redirects, validate resolved and connected addresses against loopback/private/link-local ranges, and apply an egress allowlist. Test loopback, private IPv4/IPv6, link-local, mixed DNS answers, redirects, and rebinding. |
| Malformed or oversized content exhausts the service | Enforce a small body limit before decoding; strict schema, field-length, ingredient/step/tag, and image rules; bounded worker concurrency and queue length. Test malformed JSON, unknown event types, oversized fields, deep structures, and load shedding. |
| Recipe text becomes executable or unsafe markup | Treat recipe content as text and use the existing escaped UI rendering. Never evaluate remote HTML, scripts, JSON-LD contexts, or arbitrary linked media. Add stored-XSS payload cases to receiving and display tests. |
| Source changes visibility, revokes a peer, or deletes content while the peer is offline | Send tombstones and stop future delivery immediately after unpublish/revocation; keep deletion state through retry windows. Document that offline receivers can retain already copied public data and that deletion cannot guarantee erasure from a peer's backups. Test reconnect, retry, revocation, and tombstone ordering. |
| Peer is unavailable or slow | Keep delivery asynchronous, bounded, retry with capped backoff, and isolate each peer. A failed peer must not delay local recipe saves or block other peers. Test timeouts, repeated failures, queue restart, and recovery. |
| Attribution or revisions are lost or overwritten | Preserve source instance and creator attribution; store remote copies separately from local ownership and local revision history. Never let an incoming event change a local recipe. Test attribution across update, delete, and restore/import paths. |

## Gate before any implementation

Do not implement federation until all of these are true:

1. Public origin and stable globally addressable IDs are configured and migration-tested.
2. The local public/private/unlisted and deletion policies have explicit tests covering serialized output.
3. A durable queue, bounded worker model, peer key lifecycle, and SSRF/egress policy are designed.
4. The exact event schema, ordering, replay window, duplicate behavior, and tombstone retention are versioned in [FEDERATION_PROTOCOL.md](FEDERATION_PROTOCOL.md).
5. Two local test instances can exercise publish, update, duplicate, replay, offline retry, unpublish, revocation, and deletion without enabling federation in normal installations.
6. Federation remains disabled by default, and operators must explicitly enable it and configure each peer.

The code now validates and persists an optional canonical HTTPS federation origin, locks it when the federation flag is enabled, migrates older settings rows, and derives recipe object IDs from the existing stable recipe IDs. Those paths have migration/restart tests. No public origin is configured for this checkout, so the deployment-specific portion of gate 1 remains open. A pure public-event serializer and tests now enforce explicit fields and reject private, unlisted, deleted, or disabled-instance snapshots; source-to-queue integration remains future work. Gate 3's queue, key lifecycle, and SSRF controls have a proposed design, but none are implemented. Two-instance verification (gate 5) remains open.
