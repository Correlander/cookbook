# Product architecture roadmap

This is the dependency-gated delivery plan for the approved target direction. It distinguishes current foundations from planned features. Current foundations are migration ledger v1 (`3de95c3`) and the typed recipe detail query (`89e2921`). They do not deliver owner lifecycle, publication, ActivityPub, local FTS search or a request-time public renderer.

## Phase 0 — Prove contracts and recovery

Finish bounded proofs for public federation, request-time public rendering, and identity/object mapping. The pinned SQLite driver passed an FTS5 capability proof; application projection, ACL-filtered search and backfill are still future work. Signing-key backup and restore support is implemented; see [Backup and recovery](backup-and-recovery.md). Archives are plaintext and their colocated checksums are unkeyed; fingerprints cover key-file bytes but do not prove a database-to-key identity relationship. A real operational restore rehearsal remains separately authorized. Record negative cases, cache/header behavior, ownership mappings and cleanup. A local proof does not authorize production rollout or lock public IDs and wire formats.

## Phase 1 — Local ownership and data foundations

Add operator-claimed instance ownership, limited capabilities, accepted transfer and audited recovery. Version 2 uses stable local user IDs and does not depend on public profile mapping; no first-login promotion or email-based reassignment. Version 3 is reserved for publication/provenance policy, with source actor nullable until identity mapping exists. Add typed references, copies and adaptations without converting legacy attribution into verified identity; separate access from publication/index consent. Keep user export distinct from full-instance backup.

**Gate:** representative fresh/old upgrade and restart fixtures preserve IDs, owners, visibility, revisions, memberships and media; denied-actor, recovery and import/export cases pass. No operational backup/restore rehearsal without separate authorization and an agreed location and scope.

## Phase 2 — Coherent standalone product

Deliver Discover, Library, recipe/cookbook, Account and Administration journeys with route-specific interactive entrypoints. Preserve invite, confirmation and existing URL behavior. Show owned/shared items, references, copies and adaptations with source and editability. Add creator profiles, bounded meal menus and homepage choices; keep education courses distinct. A private install must work without index, identity provider or federation services.

**Gate:** private-household and public-publisher user journeys, direct-route/auth/API checks, responsive and keyboard coverage, and no private build data.

## Phase 3 — Local discovery and controlled publication

Add FTS5 behind typed local queries, initially for recipes and cookbooks. Maintain the projection transactionally, backfill existing records and apply ACLs before snippets, counts or facets. Local results remain available when external sources are disabled. Publication state and outbound field consent stay separate from visibility. An official index can start as a configured link; ingestion needs an operator, admission/moderation, removal and incident processes.

**Gate:** actor-positive/negative privacy, rebuild/restart, exact-ID deduplication, query disclosure and bounded source-failure cases pass; public visibility never silently publishes externally. A hosted index requires separate operating/cost approval.

## Phase 4 — Public federation

After protocol proof, with signing-key backup/restore support implemented, migrate toward ActivityPub/ActivityStreams actors, follows and object lifecycle with a narrow Cookbook vocabulary. Keep Exchange v1 as a compatibility adapter until migration parity; avoid parallel permanent subscription systems. Normal public follows need no admin enrollment by default. Optional restricted pairing is revocable and grants no resource access; federation remains independent of index availability.

**Gate:** two-instance TLS interoperability, malformed/replayed/unauthorized and out-of-order events, SSRF/redirect defenses, key rotation, blocks and deletion. Do not auto-republish received objects or promise erasure of recipient copies.

## Phase 5 — Private interoperability and portability

Only after public delivery and resource authorization stabilize, add scoped private grants with receiver authentication, expiry and revocation. Identity continuity, trust and access stay distinct. Account/profile migration requires proof of control and recovery, never matching email. Cross-instance editing waits for a single-authority and conflict model.

**Gate:** positive/negative ACL, revocation, lost-key/domain-move recovery, and clear copy/deletion guarantees.

## Phase 6 — Evidence-led hosted scale

Measure contention, search latency, queue age, media use, availability and restore objectives before adding PostgreSQL, object storage or shared workers. Keep SQLite/local media as the standalone profile. Clustering (one installation across nodes) is distinct from federation (independent operators exchanging objects); multi-node sessions, replay protection, rate limits and jobs need shared durable state.

**Gate:** workload/cost evidence, comparative tests, operational ownership and restore rehearsal. The roadmap alone authorizes no paid service or deployment.

## Invariants and primary references

Go remains the authorization boundary; admins do not bypass private-content checks. Never migrate account ownership to the instance or treat attribution as verified identity. Keep publication, indexing, federation and discovery consumption independently selectable. Preserve standalone use, current API/security contracts and historical IDs through immutable, fixture-tested migrations. Use maintained protocol/crypto implementations; no custom cryptography or blanket post-quantum claim.

Primary specifications: W3C [ActivityPub](https://www.w3.org/TR/activitypub/) and [ActivityStreams Vocabulary](https://www.w3.org/TR/activitystreams-vocabulary/); IETF [WebFinger RFC 7033](https://www.rfc-editor.org/rfc/rfc7033.html); SQLite [FTS5](https://www.sqlite.org/fts5.html) and [Appropriate Uses](https://www.sqlite.org/whentouse.html); Astro [on-demand rendering](https://docs.astro.build/en/guides/on-demand-rendering/); OpenID Foundation [OpenID Connect Core](https://openid.net/specs/openid-connect-core-1_0.html). These inform the target; none implies the corresponding Cookbook feature is implemented.
