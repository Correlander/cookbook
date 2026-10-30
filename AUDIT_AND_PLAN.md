# Cookbook repository audit and implementation plan

Audit date: 2026-10-30  
Repository: `Correlander/cookbook`, cloned at this workspace's `cookbook-repo` directory.  
Source brief and four visual references are in the sibling `cookbook_agent_handoff_federation/` directory.

## Current state

The repository is an early prototype, not a functioning cookbook service.

- **Backend:** Go 1.23.4, standard `net/http`, `golang-jwt/jwt/v4`, `golang.org/x/oauth2`, and `x/time`. `main.go` exposes OAuth initiation/callback and `/auth`; there is no persistence, recipe or cookbook model, CRUD API, migration system, or test suite.
- **Authentication:** Google OAuth is the only login path, contrary to the passwordless email/passkey requirement. OAuth client credentials are hard-coded in `utils/oauth.go`. State is held in a process-global map without expiry; concurrent map reads/writes are not consistently locked, and there is no CSRF-bound browser session. JWT signing secret is randomly regenerated on each process start, making all issued JWTs invalid after restart. JWT claims are parsed without issuer/audience requirements. Refresh tokens originate from Google and are kept in cookies, but both cookies set `Secure: false`, omit SameSite, and refresh handling emits token/provider errors. The limiter is global across users and `/auth` prints diagnostic messages. CORS and frontend callback origins are hard-coded to localhost.
- **Frontend:** React 19, Vite 7 and Tailwind 4. It renders a static welcome screen and placeholder search. Navigation controls are not wired, fixed minimum width harms mobile layout, starter Vite styles remain, and no API client or tests exist. Package lock is committed; dependencies are not installed in the clone.
- **Operations:** no database, configuration example, Docker setup, health endpoint, backup guidance, deployment docs, or CI found. README is a brief note describing the project as unfinished.
- **History:** recent commits are starter-level README and repository cleanup changes.

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
7. **Federation:** disabled by default and isolated behind asynchronous adapters/queues. Research at least two protocols before implementing. Evaluate a narrow signed Cookbook protocol against ActivityPub; avoid Matrix dependencies absent a demonstrated fit. Public browsing/export are not federation. Do not implement federation before local IDs, visibility, attribution, auth and authorization are sound.
8. **Marketing:** separate static site/deployment, later than core app. Public app pages initially remain client rendered; document SEO/social preview tradeoffs.

## Milestones

### M0 — Audit and decision record (current)
- Deliver this factual audit, architecture direction, risks, and staged plan.
- Record baseline verification limitations and repository state.

### M1 — Secure application foundation
- Add configuration validation, structured HTTP routing, health endpoint, predictable JSON errors, security headers, request size limits, and CI-friendly tests.
- Remove embedded OAuth secret and legacy OAuth routes/code. Require environment configuration; fail safely if required values are absent.
- Add SQLite schema/migrations and startup integration with foreign keys, WAL/busy timeout where appropriate.
- Acceptance: clean startup/config failures, health check, migration tests, `go test`, `go vet`.

### M2 — Passwordless authentication
- Implement email challenges and sessions with storage interfaces, expiry, one-time consumption, rate limiting, enumeration-resistant responses, secure cookies/CSRF strategy, and email delivery configuration.
- Add WebAuthn enrollment/assertion using maintained library, passkey list/revoke, and recovery via verified email.
- Acceptance: expiry/replay/rate/session tests, WebAuthn origin/challenge tests, no password/OAuth endpoints, frontend sign-in and optional passkey prompt.

### M3 — Cookbook and recipe local core
- Schema and APIs for users, cookbooks, memberships/invites, recipes, structured ingredients/steps, attribution and cookbook references.
- Centralized policy checks for private/public instance and resource visibility; owner-only edit default; pagination and indexed search.
- Responsive core UI for cookbook dashboard, cookbook details, recipe details/editor, sharing controls, based on references 01/02.
- Acceptance: anonymous/private/public, member/nonmember, cross-cookbook, ownership and visibility tests; frontend build/lint.

### M4 — History and recovery
- Atomic revision snapshots, concurrency/version checks, diff/restore-as-new-version, distinct audit records, soft delete/trash, configurable retention and cleanup.
- Acceptance: rollback, conflict, access, retention, restore and migration tests.

### M5 — Self-hosting and portability
- Dockerfile/Compose, `.env.example`, health checks, persistent SQLite/media volumes, admin bootstrap, upgrade and backup/restore runbook, import/export format with attribution.
- Acceptance: reproducible local deployment, backup/restore rehearsal, portable import/export tests.

### M6 — Visual and accessibility polish
- Refine the app using reference 02; ensure keyboard/focus, contrast, reduced motion, mobile layouts, loading/empty/error states, accessible dialogs and cooking mode.
- Acceptance: responsive/accessibility review and frontend checks.

### M7 — Courses (optional)
- Only after core recipe milestone. Add disabled-by-default instance flag and separately modeled lessons/sections that may reference recipes.
- Acceptance: disabled routes/navigation/API rejection and enabled authoring/viewing tests.

### M8 — Federation research and staged delivery
- Write protocol comparison and threat model (at least two feasible approaches), then choose based on maintenance, security, event semantics, interoperability and deployment weight.
- Only after local privacy/identity work: signed/authenticated opt-in public exchange between two independent test instances; later consider controlled sharing/collaborative editing.
- Acceptance: disabled default; privacy leakage, malicious input, replay/duplicate, offline peer, attribution/revision, SSRF, revocation and deletion-limit tests/docs.

## Baseline verification

- `go test ./...` was attempted but the host's default Go build cache is read-only; no repository tests currently exist. Rerun with `GOCACHE` under `/tmp`.
- Frontend `npm run build` was attempted but dependencies are not installed (`vite: command not found`). Install from the committed lockfile before verification.
- No tests or code have been changed as part of this audit yet.
