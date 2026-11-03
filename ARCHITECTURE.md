# Cookbook architecture

This records the current implementation separately from the approved, dependency-gated target. Planned capabilities below are not claims of shipped behavior; see the [roadmap](docs/roadmap.md) for phases and gates.

## Current implementation

Cookbook is a Go API service with SQLite and local media. Astro builds the frontend independently as static output; Caddy serves it and proxies same-origin `/api/*` and `/healthz` to Go. Astro has no server runtime. React owns authenticated and interactive browser state. The separate `marketing-site/` is not part of the application build.

SQLite uses WAL, foreign keys and one application connection. Migration v1 records an ordered schema ledger and transactionally adopts fresh and recognized legacy schemas; compatibility repairs no longer replay on every startup. Recipe detail and read-after-write responses use a typed query boundary. These foundations do not provide the target identity, library or search model.

Current records include local users, sessions, email challenges and passkeys; account-owned recipes, revisions, cookbooks, memberships and invitations; optional courses and lessons; and separate federation peers, receipts, remote recipes and delivery outbox. `creator_name` and `source_recipe_id` are claims/source references, not verified identity. Copies and imports are private local records; imports do not transfer accounts, memberships, revisions or audit events.

The Go API is the authorization boundary. Anonymous reads require both an enabled public instance and explicit public visibility; unlisted resources are not readable by link. A private recipe is readable by its owner or a member of a cookbook containing it. Cookbook ownership alone does not grant access to another account’s private recipe. Viewers cannot edit; editors can edit linked recipes only when `edit_policy` permits; owners can edit their recipes. Revision reads require edit rights. Admin status never bypasses content privacy. Returned recipe cookbook IDs are filtered to the requester’s access.

### HTTP and privacy contracts

- `/api/auth/*`: email-link authentication, session management and passkey ceremonies/security. Tokens are stored as hashes; sensitive changes require recent authentication where specified.
- `/api/recipes`, `/api/recipes/{id}`: list/create and detail/update/delete. `GET /api/public/recipes`, `/api/public/cookbooks`, `/api/public/cookbooks/{id}/recipes` and `/api/public/courses*` provide visibility-checked public listings. Related recipe routes provide revisions, trash/restore, forks, ownership transfer and visibility-checked images; image writes are owner-only.
- `/api/cookbooks*`, `/api/cookbooks/{id}/members*`: cookbook details, owner-controlled membership and invitations. Membership does not transfer recipe ownership.
- `/api/export`, `/api/import`, `/api/admin/*`, `/api/courses*`: owner-scoped portability, instance/user/audit administration, and optional educational courses. Course routes return not found while disabled.
- `POST /api/federation/inbox`, `/api/admin/federation/*`, `GET /api/public/remote-recipes`: signed Exchange v1 delivery, peer/key/outbox administration and separate remote-recipe reads.

API JSON and token-bearing confirmation/invitation responses are `no-store`. Browser mutations retain same-origin/CSRF checks, secure host-only cookies, bounded input and recent-auth checks. The federation inbox uses its signed server-to-server profile. Static-page CSP hashes come from the frontend build; Go owns API security headers. API/health paths must not fall through to static HTML.

Exchange v1 is an opt-in, administrator-enrolled peer protocol for allowlisted public recipe snapshots, updates and tombstones. It uses signatures, replay/order checks and a transactional retrying outbox. It does not implement ActivityPub actors, open follows, private sharing, cookbook/course subscriptions or cross-instance/central-index discovery. See [FEDERATION_PROTOCOL.md](FEDERATION_PROTOCOL.md) and [FEDERATION_RESEARCH.md](FEDERATION_RESEARCH.md) for current wire behavior and threat limits.

## Approved target direction — staged, not shipped

Keep a modular Go application with explicit HTTP adapters, typed use cases/queries, authoritative local policy and persistence-owned SQL. Extract seams incrementally; do not introduce microservices or a second application database without evidence. Recipe changes, revisions and required outbox events remain transactionally consistent.

Keep local account IDs as authorization principals. Credentials and optional issuer-scoped sign-in bindings are separate; never auto-link by email. Instance-owner authority is distinct from admin and content ownership. Version 2 may add operator-claimed owner, accepted transfer and recovery keyed by local user IDs; it does not wait on public profile mapping. Public actor mapping is a separate later migration after its wire identity is defined. Version 3 is reserved for content publication/provenance; source actor references may stay nullable until then. Legacy attribution remains an unverified claim.

Represent owned items, shared items, saved remote references, copies and adaptations distinctly because their editing, update, deletion and provenance behavior differs. Collections reference content rather than duplicate it. Courses retain their educational meaning; meal menus are a separate future type. Account ownership and cookbook membership remain the initial custody model unless a concrete group-custody need is approved.

Access, publication, federation, index submission, discovery consumption and homepage choice are independent policies. Public visibility does not silently enable external publishing. Local search may include private content, but ACLs must filter hits, snippets, facets and counts before output. External sources receive no private fields or account identity; query terms are visible/configurable. A result never grants access, and opening it rechecks the source.

Standalone remains one Go service, SQLite and local media, usable without external identity, indexes, federation or public rendering. Hosted scale is optional and evidence-led. A future on-demand renderer may serve only explicitly public projections through Go’s policy boundary; it must preserve same-origin API routing and never cache private or personalized HTML. Static workspace delivery remains supported.

Primary specifications and delivery gates are linked from the [roadmap](docs/roadmap.md). Current behavior is defined by the API and federation protocol documents above.
