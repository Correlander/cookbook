# Product architecture roadmap

This is the dependency-gated plan for the approved target direction. It distinguishes implemented foundations from work that remains. Product status is described against source through `4370d883` (`feat: add recipe publication lifecycle controls`). Later tooling changes do not change these product milestones.

## Delivered foundations

- Ordered, checksummed SQLite migrations through v5: legacy baseline, instance-owner authority, recipe publication policy, offline passkey enrollment, and Person-actor identity persistence.
- Explicit instance-owner claim/transfer/recovery controls; exact-email local account lookup for operators; and a one-time offline initial-passkey enrollment path for existing accounts when email delivery is unavailable.
- Typed local recipe reads and sharing authorization that preserve owner/member/privacy boundaries.
- Recipe publication policy and owner-only publication/lifecycle API, including public-create confirmation, separate visibility confirmation, audit/outbox transactions, and conservative legacy consent defaults.
- Exchange v1 egress enforcement using explicit owner federation consent.
- Optional Person-actor IDs and key persistence. Public actor routes, ActivityPub publishing, and the actor-aware backup/recovery gate are not complete.

The publication controls are backend capabilities. The publication panel and recipe create/edit form integration are **not shipped**. Local search, a public recipe detail endpoint, and request-time public rendering are also not delivered.

## Phase 0 — Prove contracts and recovery

The pinned SQLite driver passed an FTS5 capability proof. Application FTS queries, ACL-filtered snippets/counts, and backfill remain future work. The local public-rendering and identity/object contracts are documented, but that does not deliver request-time rendering or public actor routes. Signing-key backup and restore support is implemented; see [Backup and recovery](backup-and-recovery.md). Archives are plaintext and their colocated checksums are unkeyed; existing federation key fingerprints establish byte consistency, not an authenticated manifest or database-to-key identity proof. The actor-aware verifier must compare complete actor state and key bindings against both the archive database and restored database before any public actor publication. A real operational restore rehearsal remains separately authorized.

**Gate:** recovery validation must fail closed on missing/malformed/mismatched actor manifests or keys, preserve compatible pre-v5 archives with no actors, and verify the same manifest before restore and before service restart. A local proof does not authorize production rollout or lock public IDs and wire formats.

## Phase 1 — Local ownership, policy, and data foundations

The migration and owner-control foundation is implemented through v5. Recipe access visibility, local publication lifecycle, federation consent, and index eligibility now have separate stored policy. Complete the local library model: typed saved references, copies, adaptations, and provenance that do not convert legacy attribution into verified identity. Keep user export distinct from full-instance backup.

**Gate:** fresh/old upgrade and restart fixtures preserve IDs, owners, visibility, revisions, memberships, and media; denied-actor, recovery, reference, and import/export cases pass. No operational backup/restore rehearsal without separate authorization and an agreed location and scope.

## Phase 2 — Coherent standalone product journeys

Deliver Discover, Library, recipe/cookbook, Account, and Administration journeys with route-specific interactive entrypoints. Preserve invite, confirmation, and existing URL behavior. Show owned/shared items, references, copies, and adaptations with source and editability. Publication API integration into owner controls and the create/edit forms remains pending. Add the public recipe detail endpoint before claiming direct public recipe pages. Keep creator profiles, bounded meal menus, homepage choices, and other future journeys separately scoped; courses retain their educational meaning. A private install must work without external identity, indexes, federation, or public rendering.

**Gate:** private-household and public-publisher journeys, direct-route/auth/API checks, responsive and keyboard coverage, and no private build data.

## Phase 3 — Local discovery and controlled publication

Owner publication/lifecycle API and owner-consent Exchange v1 egress are implemented. Local anonymous listing remains gated by public instance, public visibility, active row, and `published` state. Federation consent remains a separate opt-in and is off for migrated legacy recipes. There is no index submission/removal adapter, so `index_eligible` remains false and the API rejects attempts to enable it.

Add FTS5 behind typed local queries, initially for recipes and cookbooks. Maintain the projection transactionally, backfill existing records, and apply ACLs before snippets, counts, or facets. Local results remain available when external sources are disabled. An official index can start as a configured link; ingestion needs an operator, admission/moderation, removal, and incident processes.

**Gate:** actor-positive/negative privacy, rebuild/restart, exact-ID deduplication, query disclosure, and bounded source-failure cases pass; public visibility never silently publishes externally. A hosted index requires separate operating/cost approval.

## Phase 4 — Public federation

Exchange v1 with administrator-enrolled peers remains the current compatibility path, with owner consent required for recipe egress. After protocol, public actor, and recovery proofs pass, migrate toward ActivityPub/ActivityStreams actors, follows, and object lifecycle with a narrow Cookbook vocabulary. The actor identity tables exist, but there are no public actor routes or completed recovery gate. Keep Exchange v1 until migration parity; do not build parallel permanent subscription systems. Normal public follows need no admin enrollment by default in the future profile. Optional restricted pairing is revocable and grants no resource access; federation remains independent of index availability.

**Gate:** two-instance TLS interoperability, malformed/replayed/unauthorized and out-of-order events, SSRF/redirect defenses, key rotation, blocks, deletion, and actor-aware backup/recovery. Do not auto-republish received objects or promise erasure of recipient copies.

## Phase 5 — Private interoperability and portability

Only after public delivery and resource authorization stabilize, add scoped private grants with receiver authentication, expiry, and revocation. Identity continuity, trust, and access stay distinct. Account/profile migration requires proof of control and recovery, never matching email. Cross-instance editing waits for a single-authority and conflict model.

**Gate:** positive/negative ACL, revocation, lost-key/domain-move recovery, and clear copy/deletion guarantees.

## Phase 6 — Evidence-led hosted scale

Measure contention, search latency, queue age, media use, availability, and restore objectives before adding PostgreSQL, object storage, or shared workers. Keep SQLite/local media as the standalone profile. Clustering (one installation across nodes) is distinct from federation (independent operators exchanging objects); multi-node sessions, replay protection, rate limits, and jobs need shared durable state.

**Gate:** workload/cost evidence, comparative tests, operational ownership, and restore rehearsal. The roadmap alone authorizes no paid service or deployment.

## Invariants and primary references

Go remains the authorization boundary; admins do not bypass private-content checks. Never migrate account ownership to the instance or treat attribution as verified identity. Keep visibility, publication, federation consent, index eligibility, and discovery consumption independently selectable. Preserve standalone use, current API/security contracts, and historical IDs through immutable, fixture-tested migrations. Use maintained protocol/crypto implementations; no custom cryptography or blanket post-quantum claim.

- [Recipe publication and lifecycle contract](recipe-publication.md)
- [Backup and recovery](backup-and-recovery.md)
- [Exchange v1 protocol](../FEDERATION_PROTOCOL.md) and [federation research](../FEDERATION_RESEARCH.md)

Primary external specifications: W3C [ActivityPub](https://www.w3.org/TR/activitypub/) and [ActivityStreams Vocabulary](https://www.w3.org/TR/activitystreams-vocabulary/); IETF [WebFinger RFC 7033](https://www.rfc-editor.org/rfc/rfc7033.html); SQLite [FTS5](https://www.sqlite.org/fts5.html) and [Appropriate Uses](https://www.sqlite.org/whentouse.html); Astro [on-demand rendering](https://docs.astro.build/en/guides/on-demand-rendering/); OpenID Foundation [OpenID Connect Core](https://openid.net/specs/openid-connect-core-1_0.html). These inform the target; none implies that the corresponding Cookbook feature is implemented.
