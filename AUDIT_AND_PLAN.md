# Cookbook repository audit and implementation plan

Audit date: 2026-10-30  
Repository: `Correlander/cookbook`, cloned at this workspace's `cookbook-repo` directory.  
Source brief and four visual references are in the sibling `cookbook_agent_handoff_federation/` directory.

## Current state

At audit time the repository was an early prototype. Implementation has since added the foundation and partial local-core functionality described below; the milestones are not complete as a whole.

- **Backend:** Go 1.23.4, standard `net/http`, `golang-jwt/jwt/v4`, `golang.org/x/oauth2`, and `x/time`. `main.go` exposes OAuth initiation/callback and `/auth`; there is no persistence, recipe or cookbook model, CRUD API, migration system, or test suite.
- **Authentication:** Google OAuth is the only login path, contrary to the passwordless email/passkey requirement. OAuth client credentials are hard-coded in `utils/oauth.go`. State is held in a process-global map without expiry; concurrent map reads/writes are not consistently locked, and there is no CSRF-bound browser session. JWT signing secret is randomly regenerated on each process start, making all issued JWTs invalid after restart. JWT claims are parsed without issuer/audience requirements. Refresh tokens originate from Google and are kept in cookies, but both cookies set `Secure: false`, omit SameSite, and refresh handling emits token/provider errors. The limiter is global across users and `/auth` prints diagnostic messages. CORS and frontend callback origins are hard-coded to localhost.
- **Frontend:** React 19, Vite 7 and Tailwind 4. It renders a static welcome screen and placeholder search. Navigation controls are not wired, fixed minimum width harms mobile layout, starter Vite styles remain, and no API client or tests exist. Package lock is committed; dependencies are not installed in the clone.
- **Operations:** no database, configuration example, Docker setup, health endpoint, backup guidance, deployment docs, or CI found. README is a brief note describing the project as unfinished.
- **History:** recent commits are starter-level README and repository cleanup changes.

## Implementation progress (2026-10-30)

- M1 foundation: Go configuration, health endpoint, security headers, SQLite startup schema, and backend verification are in place. The Go service serves embedded frontend assets, and a multi-stage Dockerfile/Compose setup persists SQLite and health-checks the app; the Docker image build is not verified on this host because no Docker daemon is running.
- M2 authentication: passwordless email links, hashed one-time challenges, durable sessions, same-origin checks, WebAuthn passkeys, and frontend request/confirmation/management are implemented. First-administrator bootstrap and admin-issued email-bound account invitations are implemented. Invitations are accepted only after mailbox verification. Broader abuse controls and in-app admin role management remain open.
- M3 local core: private-first users/cookbooks/recipes/membership schema, API authorization, structured recipes and metadata, public recipe listing, private recipe creation, and basic cookbook listing/creation/editing/assignment UI are implemented. Cookbook owners can invite members as viewers/editors, change roles, revoke pending invitations, and remove members. Recipe discovery and cookbook listing have server-side search/paging. Recipe images use local private storage, access-checked routes, and revision snapshots. Owners can edit recipes through the version-checked API; metadata is captured in revision history and restored with prior versions. The dashboard also manages instance account invitations.
- M4 history: recipe snapshots are written atomically at create/update, including servings, preparation/cook time, tags, and images. Updates use optimistic versions, authorized users can read history and compare two versions, and restore-as-new-version uses an optimistic version check. Owner-only trash/restore and configurable retention cleanup are implemented. Successful authenticated API mutations and authenticated 403 denials are recorded separately in an administrator-only, paged security audit log.
- Frontend dependencies were updated within existing semver ranges; the ESLint 10 migration and a minimatch override clear the current audit findings. Go tests/vet and frontend lint/build have been run successfully at recent checkpoints.
- WebAuthn uses `github.com/go-webauthn/webauthn` v0.14.0 and raises the backend minimum to Go 1.24. Automated tests cover RP configuration, option creation, origin checks, and one-time ceremony use; a real browser/authenticator enrollment and assertion has not been exercised.

These notes describe repository state, not a claim that any milestone is complete. Recheck implementation and verification evidence before release.

## Reuse and immediate risks

Keep Go + React/Vite as the established base. The OAuth/JWT implementation is not suitable to preserve as the account/session foundation: it conflicts with the explicit allowed sign-in methods and carries credential, state, cookie, process-restart, and error-disclosure issues. Replace it in a migration rather than extending OAuth. Build authorization into backend domain/service boundaries from the first persistent resource; frontend visibility is not security enforcement.

The repository contains a Google OAuth client secret in tracked source. Treat it as exposed: revoke/rotate it in Google Cloud before any public deployment. Do not reproduce it in docs or logs.

## Visual reference inspection

All four provided PNGs were inspected. Reference 02 is the visual north star: warm ivory, subtle paper texture, editorial serif headings, forest-green controls, terracotta accents, botanical details, rounded cards, and restrained density. Reference 01 supplies a clear hierarchy for home, cookbook and recipe screens. Reference 03 offers useful cookbook/recipe/profile patterns; avoid social-network features in the brief. Reference 04 informs simple roles and sharing controls; do not treat link visibility as authorization or adopt password-protected link sharing without a deliberate capability-token design. Mockups include excluded meal planning, ratings/follows, and other out-of-scope items; do not implement those by implication.

## Architecture decisions and open boundaries

1. **Local core:** Go HTTP JSON API, SQLite, React/Vite SPA. A single Go service should eventually serve embedded or built frontend assets with SQLite and local media for straightforward self-hosting.
2. **Authentication:** remove Google OAuth and implement verified email one-time link/code plus WebAuthn passkeys. Use a maintained Go WebAuthn library. Store hashed, expiring, single-use email challenges and durable sessions. Secure cookie/session behavior, CSRF defenses, rate limits and origin/RP configuration are required. Email provider and initial admin bootstrap configuration need deployment decisions during their milestone.
3. **Data/authorization:** model users, instance settings, cookbooks, memberships, invitations, recipes, cookbook-recipe references, revisions/audit, and media. Central Go policy checks combine instance policy, resource visibility, ownership, identity, and membership. Default private instance and private cookbook; public instance never implicitly publishes existing content. Cookbook membership does not transfer recipe ownership. Use owner-only editing by default and explicit policy for collaborator edits.
4. **Recipe API:** structured ingredients and ordered steps; stable local IDs that can later become globally unique resource IDs. Optimistic version checks; recipe revision write plus canonical update in one transaction. Separate revision history from security/admin audit.
5. **Media:** local filesystem behind a storage interface, strict size/type limits, private media authorized by API. Add object storage adapter only when hosting requires it.
6. **Courses:** optional instance flag, disabled by default; separate entities/routes/module and consistent API enforcement. Defer implementation until core recipes work.
7. **Federation:** disabled by default and isolated behind asynchronous adapters/queues. The first protocol comparison and threat model are in [FEDERATION_RESEARCH.md](FEDERATION_RESEARCH.md). Its provisional choice is a manually peered signed Cookbook exchange for public recipe snapshots, with ActivityPub reserved for a later explicit social-interoperability goal. Research is complete; implementation remains gated on the listed identity, privacy, key lifecycle, SSRF, queue, and two-instance test requirements. Public browsing/export are not federation. Do not implement federation before local IDs, visibility, attribution, auth and authorization are sound.
8. **Marketing:** separate static site/deployment, later than core app. Public app pages initially remain client rendered; document SEO/social preview tradeoffs.

## Milestones

### M0 — Audit and decision record (complete)
- Deliver this factual audit, architecture direction, risks, and staged plan.
- Record baseline verification limitations and repository state.

### M1 — Secure application foundation (partial)
- Add configuration validation, structured HTTP routing, health endpoint, predictable JSON errors, security headers, request size limits, and CI-friendly tests.
- Remove embedded OAuth secret and legacy OAuth routes/code. Require environment configuration; fail safely if required values are absent.
- Add SQLite schema/migrations and startup integration with foreign keys, WAL/busy timeout where appropriate.
- Acceptance: clean startup/config failures, health check, migration tests, `go test`, `go vet`.

### M2 — Passwordless authentication (partial)
- Implement email challenges and sessions with storage interfaces, expiry, one-time consumption, rate limiting, enumeration-resistant responses, secure cookies/CSRF strategy, and email delivery configuration.
- Add WebAuthn enrollment/assertion using maintained library, passkey list/revoke, and recovery via verified email.
- Acceptance: expiry/replay/rate/session tests, WebAuthn origin/challenge tests, no password/OAuth endpoints, frontend sign-in and optional passkey prompt.

### M3 — Cookbook and recipe local core (partial)
- Schema and APIs for users, cookbooks, memberships/invites, recipes, structured ingredients/steps, attribution and cookbook references.
- Centralized policy checks for private/public instance and resource visibility; owner-only edit default; pagination and indexed search.
- Responsive core UI for cookbook dashboard, cookbook details, recipe details/editor, sharing controls, based on references 01/02.
- Acceptance: anonymous/private/public, member/nonmember, cross-cookbook, ownership and visibility tests; frontend build/lint.
- Cookbook listing now uses the shared bounded `limit`/`offset` contract, stable ordering, and a dashboard load-more control. Backend tests cover page boundaries, ordering, and invalid limits.
- Recipe images are stored as generated local files (JPEG, PNG, or WebP; 5 MiB maximum), verified by decoding image headers, served through recipe visibility checks, and preserved in revision snapshots. Compose persists them under `/data/media`; backups must include this directory.
- Administrators can manage the instance's public-browsing and registration settings in the app. Enabling public browsing while resources are already marked public requires an explicit confirmation; private resources remain private.
- Anonymous visitors can search and page through public cookbooks and their public recipes. Cookbook-scoped reads omit private and unlisted recipes even when those recipes are referenced by a public cookbook.
- Recipe owners alone can change resource visibility; cookbook editors can edit content only when the recipe's edit policy permits it.
- Signed-in users now land on a compact kitchen dashboard header; the large editorial hero and signup banner remain anonymous-only.
- A signed-in reader may save an accessible recipe as a private owner-controlled copy. Original creator attribution and source recipe ID are preserved; the new account owns the copy and its new revision history.

### M4 — History and recovery (partial)
- Atomic revision snapshots, concurrency/version checks, diff/restore-as-new-version, distinct audit records, soft delete/trash, configurable retention and cleanup.
- Acceptance: rollback, conflict, access, retention, restore and migration tests.
- The security audit log stores the acting account, HTTP route/action, resource type/ID, response status, and timestamp for successful session-authenticated mutations and authenticated 403 denials. It deliberately omits request bodies and query strings; audit reads are administrator-only and paged.
- Administrators can configure revision and audit retention separately (7/30/90/365 days or unlimited); cleanup is bounded and preserves each recipe's latest snapshot.

### M5 — Self-hosting and portability
- Dockerfile/Compose, `.env.example`, health checks, persistent SQLite/media volumes, admin bootstrap, upgrade and backup/restore runbook, import/export format with attribution.
- Owner-scoped `cookbook-export-v1` JSON export/import is implemented with a strict size limit, validation, fresh private ownership on import, and attribution preservation. It excludes media and revision history.
- Owner-scoped `cookbook-export-v1` JSON export/import is implemented with a strict size limit, validation, fresh private ownership on import, and attribution preservation. It excludes media and revision history.
- Acceptance: reproducible local deployment and backup/restore rehearsal remain outstanding; Dockerfile and Compose configuration are prepared, and upgrade paths have migration tests.

### M6 — Visual and accessibility polish
- Refine the app using reference 02; ensure keyboard/focus, contrast, reduced motion, mobile layouts, loading/empty/error states, accessible dialogs and cooking mode.
- A distraction-reduced cooking view includes touch-sized ingredient checkboxes, one instruction at a time, keyboard step navigation, Escape to close, focus restoration, and live step announcements.
- Acceptance: responsive/accessibility review and frontend checks.

### M7 — Courses (optional)
- Only after core recipe milestone. Add disabled-by-default instance flag and separately modeled lessons/sections that may reference recipes.
- Acceptance: disabled routes/navigation/API rejection and enabled authoring/viewing tests.

### M8 — Federation research and staged delivery
- Write protocol comparison and threat model (at least two feasible approaches), then choose based on maintenance, security, event semantics, interoperability and deployment weight.
- Only after local privacy/identity work: signed/authenticated opt-in public exchange between two independent test instances; later consider controlled sharing/collaborative editing.
- Acceptance: disabled default; privacy leakage, malicious input, replay/duplicate, offline peer, attribution/revision, SSRF, revocation and deletion-limit tests/docs.
- Protocol comparison and threat model are documented. Implementation remains unstarted pending the explicit pre-implementation gates in `FEDERATION_RESEARCH.md`.

## Baseline verification

- At audit time, `go test ./...` could not use the host's read-only default Go build cache and frontend dependencies were not installed. Subsequent implementation checkpoints passed Go tests/vet with caches under `/tmp` and frontend lint/build using the lockfile-installed dependencies.
- Docker Compose configuration validates; the image build and deployment rehearsal remain unverified because the host has no running Docker daemon.
- Email-link sign-in offers an optional first-passkey setup prompt; passkeys can be named, listed, and revoked in account security rather than recipe editing. Existing credentials migrate with the display name “Passkey”.
