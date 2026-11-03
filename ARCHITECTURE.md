# Cookbook architecture

This document separates shipped behavior from the approved, dependency-gated target. The implementation snapshot below is current through `4370d883` (`feat: add recipe publication lifecycle controls`); later smoke-runner changes do not alter the product boundaries described here. Planned capabilities are not claims of shipped behavior. See the [roadmap](docs/roadmap.md) and [recipe publication guide](docs/recipe-publication.md).

## Current implementation

Cookbook is an independently served Go API with SQLite and local media. Astro builds the frontend as static output; Caddy serves it and proxies same-origin `/api/*` and `/healthz` to Go. There is no Astro server runtime. The shared React application owns current authenticated and interactive browser state, while `/cookbooks` and `/cookbook?id=…` use independent route roots and hydrate only their page-specific cookbook UI. `/admin` also uses its own `InstanceWorkbenchRoot` and session provider rather than routing instance access through the shared Home application. These static shells contain no account, authority, transfer, or cookbook data and use the authenticated or public APIs after session resolution. The separate `marketing-site/` is outside the application build. Static standalone delivery remains the default; a request-time public renderer is not part of the shipped deployment.

SQLite uses WAL, foreign keys, one application connection, and an ordered, checksummed migration ledger. Migrations through v7 provide the legacy baseline, instance-owner authority, recipe publication policy, offline passkey enrollment, optional Person-actor identity persistence, personal-library bookmarks, and private/household creator profiles with typed local attribution and portable claims. Migration definitions are immutable after application; new schema belongs in later ordered migrations.

Local accounts authenticate through email links when email delivery is configured and through passkeys. Operators can use a read-only exact-email account lookup when the server is stopped, and can issue or revoke a one-time, short-lived initial passkey enrollment grant for an existing account without an email link. Instance-owner claim, transfer, and recovery are explicit account-ID operations. These controls do not infer ownership or role from email. Instance ownership, administrator authority, recipe ownership, and creator attribution remain distinct.

Migration v7 adds account-owned creator profiles with `private|instance` visibility and typed recipe/cookbook attribution. Authenticated profile reads never grant access to an attributed resource; profile catalog queries apply both profile visibility and the existing resource ACL. Recipe self-attribution is create-only, cookbook creation and fork adaptation are transactional, and legacy bylines remain unverified text. Portable export v4 preserves external attribution claims without binding them to the importer. Public creator profiles and cross-account credit workflows are not implemented. See [Local creator profiles](docs/creator-profiles.md).

Person-actor identity persistence is present: an existing account can have one optional actor with a pinned canonical origin, opaque stable ID, and Ed25519 key material. Public actor documents, actor routes, and ActivityPub publishing are not implemented. The actor-aware backup/recovery gate required before public actor publication is also not complete; current backup checksums and key fingerprints do not prove a database-to-key identity relationship.

Recipe reads use a typed query boundary. The API applies owner, cookbook-membership, instance, visibility, deletion, and publication policy before returning recipe data. Authenticated recipe read projections include `owner_id` only for the recipe owner; authorized non-owners retain their `can_edit`, `can_delete`, `can_manage_sharing`, cookbook, and source-link projections without receiving the owner account ID. These response fields do not change persisted ownership or authorize requests. Cookbook ownership alone does not grant access to another account’s private recipe; editor rights remain subject to the recipe’s edit policy. Admin status does not bypass content privacy. Attribution and source references are claims, not verified identity.

### Independent content controls

The API keeps these controls separate:

- **Visibility** controls the local audience (`private`, `unlisted`, or `public`). Unlisted recipes are not capability URLs. Anonymous reading requires a public instance, public visibility, an active recipe, and published lifecycle state.
- **Publication lifecycle** controls inclusion in anonymous local recipe lists and other public projections (`unpublished`, `published`, or `withdrawn`). It never changes visibility.
- **Federation consent** controls outbound Exchange v1 recipe delivery (`off` or `followers`). It defaults off and is independent of local public browsing. “Followers” is the policy value; current v1 delivery still uses administrator-enrolled peers, not open ActivityPub follows.
- **Index eligibility** is represented in the policy, but index submission/removal is not implemented. The API rejects `index_eligible: true`.
- **Discovery consumption** is a separate future choice about which sources a user or instance queries. A search result must not grant access to its source.

### HTTP, privacy, and publication routes

- `/api/auth/*` provides email-link authentication, sessions and passkey ceremonies. Tokens are hashed; sensitive changes have recent-authentication requirements where specified.
- `POST /api/recipes` creates an owned recipe. New records default to private visibility and unpublished/off/no-index policy. Public visibility requires `confirm_public_access: true`; an optional `initial_publication` chooses initial local lifecycle and federation consent in the same transaction.
- `/api/recipes` and `/api/recipes/{id}` provide authenticated recipe lists, detail, edits, and soft deletion. Related routes handle revisions, trash/restore, forks, owner transfer, publication policy, and visibility-checked images. Recipe owners alone can manage publication policy and transfer ownership.
- `GET` and `PUT /api/recipes/{id}/publication` are owner-only. The response is private/no-store and includes a strong ETag; PUT requires the exact current ETag in `If-Match`. This endpoint does not edit recipe body or local visibility. Details and error handling are in [Recipe publication](docs/recipe-publication.md).
- `GET /api/public/recipes`, `/api/public/cookbooks*`, and public course routes expose policy-filtered listings. There is no anonymous `GET /api/public/recipes/{id}` detail endpoint yet; `/api/recipes/{id}` remains behind session middleware. Direct public recipe pages and a public renderer therefore remain pending.
- `/api/cookbooks*`, `/api/admin/*`, `/api/instance/*`, `/api/export`, and `/api/import` provide membership/invitation management, administration, explicit owner operations, and portability.

API JSON and token-bearing authentication responses are `no-store`. Browser mutations retain same-origin/CSRF checks, secure host-only cookies, bounded input, and required recent-auth checks. The federation inbox uses its signed server-to-server profile. Static-page CSP hashes come from the frontend build; Go owns API security headers. API/health paths must not fall through to static HTML.

### Federation and delivery

Exchange v1 remains an opt-in, administrator-enrolled peer protocol for allowlisted public recipe snapshots, updates, and tombstones. It uses signatures, replay/order checks, and a transactional retrying outbox. The v1 sender now requires current public-instance, public-visibility, active, published, and explicit owner `followers` consent before emitting recipe-bearing events. Owner policy changes and required tombstones share a transaction; stale content is suppressed at delivery/retry boundaries. Consent defaults off, including for legacy recipes. V1 is a compatibility profile, not ActivityPub or private sharing. See [FEDERATION_PROTOCOL.md](FEDERATION_PROTOCOL.md) and [FEDERATION_RESEARCH.md](FEDERATION_RESEARCH.md) for wire behavior and threat limits.

## Approved target direction — staged, not shipped

Keep a modular Go application with explicit HTTP adapters, typed use cases/queries, authoritative local policy, and persistence-owned SQL. Extract seams incrementally; do not introduce microservices or a second application database without evidence. Recipe changes, revisions, and required outbox events remain transactionally consistent.

Keep local account IDs as authorization principals. Credentials, instance-owner authority, optional public actors, recipe owners, and attribution are separate. The v5 actor tables provide persistence only: public actor resolution/signing endpoints and the backup/recovery proof are required before publishing those identities.

Complete typed library references, copies, adaptations, and provenance without changing legacy claims into verified identity. Collections reference content rather than duplicate it. Courses retain their educational meaning; meal menus are a separate future type. Account ownership and cookbook membership remain the current custody model unless a concrete group-custody need is approved.

Local FTS5 capability has been proven, but the user-facing local search implementation remains future work. Apply ACLs before returning hits, snippets, facets, counts, or images. External sources receive no private fields or account identity; query terms are visible/configurable. A result never grants access, and opening it rechecks the source.

Standalone remains one Go service, SQLite, local media, and an independently served static frontend. A future on-demand renderer is an optional profile only: it may serve explicitly public projections through Go’s policy boundary, preserve same-origin API routing, and never cache private or personalized HTML. It must not become mandatory for standalone use. Production deployment or account/infrastructure changes require separate authorization.

Architecture decisions record the problem, evidence, alternatives, compatibility impact, tests, and migration sequence in the project decision register. Primary specs and delivery gates are linked from the [roadmap](docs/roadmap.md).
