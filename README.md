# Cookbook

## Local deployment

The supported container layout serves the built Astro site and Go API as separate artifacts. From `cookbook-repo/`, run `podman compose up --build`; the static Caddy host is available at `http://127.0.0.1:8080` and proxies `/api/*` and `/healthz` to the private Go service. Stop only this project with `podman compose down`; persistent recipe data is in the named `cookbook-data` volume and is retained unless explicitly removed.

Copy `cookbook_back-end/.env.example` to `cookbook_back-end/.env` and configure sign-in delivery and the initial administrator before first startup. Never point local tests at the demo or production database. The Go image is backend-only; it does not embed Astro output. The independent static image serves the Astro build, resolves `/auth/confirm` to the generated `auth/confirm.html` while retaining query parameters, and uses per-page CSP generated from Astro's built CSP meta tags. The generator emits Cloudflare `_headers` and the matching Caddy policy include. Do not hand-maintain CSP hashes.

For deployment topology, origin placeholders, and the optional Cloudflare static-assets proxy template, see [`deploy/README.md`](deploy/README.md) and [`deploy/cloudflare/README.md`](deploy/cloudflare/README.md). No account configuration or deployment is performed by these files.

A self-hostable cookbook for collecting recipes and organizing them into cookbooks. The application uses an independent Go HTTP service, SQLite, and a static Astro frontend with React islands. Instances start private, and public browsing, account registration, courses, and federation are controlled independently.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the data model, access-control rules, API map, and implementation boundaries. [AUDIT_AND_PLAN.md](AUDIT_AND_PLAN.md) tracks milestone acceptance and verification.

## Local development

Requirements: Go 1.24+, Node.js 22.23.4+, and npm.

1. Copy `cookbook_back-end/.env.example` to `cookbook_back-end/.env` and configure the public URL and mail delivery. The Go service reads environment variables directly; load the file with your shell or preferred environment loader.
2. Run the API from `cookbook_back-end` with `go run .` (defaults to `127.0.0.1:8000`). It initializes the SQLite schema and serves API/health routes only.
3. In a second terminal, run `npm ci && npm run dev` from `coobook_front-end`. Astro serves the development UI at `http://localhost:4321` and proxies API requests to the Go service.

Run backend checks with `go test ./...` and `go vet ./...` from `cookbook_back-end`. Run frontend checks with `npm test`, `npm run lint`, and `npm run build` from `coobook_front-end`.

## Self-host with Docker Compose

1. Copy `.env.example` to `.env`, then set `COOKBOOK_PUBLIC_URL`, `COOKBOOK_RP_ID`, `COOKBOOK_RP_ORIGIN`, and the SMTP host/from values. SMTP delivery requires STARTTLS; credentials are optional for relays that do not require authentication. Alternatively, configure `COOKBOOK_EMAIL_COMMAND` with an executable in the container that accepts the documented JSON payload on stdin. SMTP connections and mail commands have a 30-second deadline.
2. Set `COOKBOOK_ADMIN_EMAIL` and optionally `COOKBOOK_ADMIN_NAME` for the first startup. This creates the first administrator account. Leave the email challenge/sign-in flow configured before making the instance reachable.
3. Run `docker compose up --build -d`. Compose binds the service to `127.0.0.1:8000`; put a trusted HTTPS reverse proxy in front. The `cookbook-data` volume stores SQLite data at `/data/cookbook.db` and recipe images under `/data/media`.
4. After setup, remove `COOKBOOK_ADMIN_EMAIL` from `.env`. It provisions an account only when the instance has no users; it will not recreate or promote an account on an existing instance, including one with no administrators.

## Instance owner operations

The first instance owner is selected explicitly from an existing account. While the API is running, an administrator can obtain account IDs from the admin user list. Stop the backend before an offline owner command; the service and CLI coordinate through a persistent Linux advisory lock, and the CLI fails without opening the database if the service still holds its lock.

For Compose, stop the backend and run the one-shot command against the same data volume:

```sh
podman compose stop cookbook
podman compose run --rm --no-deps cookbook instance owner claim --account-id <account-id>
podman compose up -d cookbook
```

The command prompts for an exact confirmation. Claiming changes no administrator role, account credentials, content ownership or private-content access. After claim, only the instance owner can grant or revoke administrator roles or stage/activate the local federation signing-key rotation. Existing administrators retain ordinary admin features and peer management, but lose those instance-identity controls. Before the first claim, existing administrators retain their current role and federation identity controls. The canonical federation origin remains a host-operator startup setting (`COOKBOOK_FEDERATION_ORIGIN`); it is not changed through admin settings.

Role changes and signing-key rotations serialize with owner-transfer acceptance. A mutation that already holds the database writer lock finishes before the transfer; a request that acquires the lock after acceptance rechecks the new authority and is denied if the caller no longer controls instance identity. This does not cancel requests or sessions; the gate applies to these protected writes.

Signing-key rotation uses atomic files for staging and activation. If a rotation request returns `federation_key_rotation_outcome_unknown`, do not retry immediately: read the authorized `GET /api/admin/federation/identity` status and compare its `key_fingerprint` and optional `pending_key_fingerprint` with fingerprints recorded before activation. If the active fingerprint now matches the previously staged fingerprint and no pending fingerprint remains, activation took effect; if the old active fingerprint and pending fingerprint remain, activation did not take effect and may be retried after review. For an unknown result from staging, inspect the returned state and pending-key fingerprint before deciding whether to continue; do not blindly stage again. If the observed state does not match the expected pre- or post-activation state, stop and inspect the protected key files before acting. The status response exposes SHA-256 fingerprints of public keys only, never private-key material. The unknown-outcome response records an attempted/unknown audit event on a best-effort basis; a database commit failure can prevent that event and cannot be reported as a successful rotation audit. The API performs no automatic retry.

To recover instance ownership, choose an existing account ID, stop the backend, and run:

```sh
podman compose stop cookbook
podman compose run --rm --no-deps cookbook instance owner recover --account-id <account-id> --reason "document the operator recovery context"
podman compose up -d cookbook
```

Recovery requires an exact interactive confirmation and records the prior/target account IDs and reason in the audit log. It cancels any pending owner transfer and advances the authority generation. It does not sign in as the target, reset credentials, grant administrator role or bypass content permissions. These offline commands are supported on Linux; other operating systems fail closed because the shared/exclusive advisory lock is unavailable.

Run `./scripts/backup-compose.sh [private-directory]` to create a versioned full-instance archive. The helper stops the Compose service, archives the SQLite database and sidecars, `/data/media`, the active federation signing key and any pending rotation key, then restarts the service if it was running. Without an argument it uses `./backups`; `COOKBOOK_BACKUP_DIR` also selects the default. The destination and archive are restricted to the current user, but the archive is plaintext and contains private user data and signing keys. Store backups only in an explicitly agreed private, encrypted destination. See [Backup and recovery](docs/backup-and-recovery.md) for the format, limits and legacy archive behavior.

Restore with `./scripts/restore-compose.sh /path/to/cookbook-....tar.gz` using Podman Compose or Docker Compose and Python 3.9+. The archive and its directory must be accessible only by the current user. The script validates archive paths, file types, permissions, sizes, checksums and federation key files, then replaces the database, media and `/data/secrets`. It does not create an automatic pre-restore backup. If you need a separate backup, choose an explicitly agreed private destination and create it separately before restoring. Legacy database/media archives require `--accept-federation-key-loss`; see [Backup and recovery](docs/backup-and-recovery.md). If applying a restore fails after shutdown, the service remains stopped for inspection. Database schema upgrades run when the service starts.

## Personal data export and import

Signed-in users can download their owned, non-deleted recipes and owned cookbooks from `/api/export` as a `cookbook-export-v2` ZIP bundle, then import it through `POST /api/import` (100 MiB maximum compressed size). Import creates new private, owner-controlled copies and starts fresh revision history. It preserves recipe photos, creator attribution, source IDs, and cookbook membership links, but does not transfer account identities, collaborators, revisions, or audit events. Bundles are limited to 100,000 cookbook-recipe links, and recipe content is checked against current creation limits. Older `cookbook-export-v1` JSON bundles remain importable. An export is a portability aid, not a full instance backup; use the stopped-service database and media backup procedure above for complete recovery.

## Authentication and registration

Sign-in uses one-time email links and WebAuthn passkeys. Opening an email link displays a confirmation page; the token is consumed only after the user chooses “Continue to Cookbook,” so link preloading alone does not redeem it. Email sign-in requests are limited to five per email address per hour and 30 per client IP per hour; passkey ceremony starts are limited to 30 per client IP per hour, and passkey login completions are limited to 60 per client IP per hour. `COOKBOOK_TRUSTED_PROXIES` accepts comma-separated proxy CIDRs; only when the connecting peer matches one of those ranges does the service use the trusted proxy chain in `X-Forwarded-For`. Leave it unset unless the network boundary is controlled.

`COOKBOOK_PUBLIC_URL` must be the browser-facing app origin; passkeys require HTTPS outside localhost. `COOKBOOK_RP_ID` defaults to its hostname and `COOKBOOK_RP_ORIGIN` must match the public origin.

For a new account that cannot receive an email sign-in link, an operator can issue a short-lived, one-time initial passkey grant while the backend is stopped. Use the exact existing account ID from the administrator user list:

```sh
podman compose run --rm --no-deps cookbook instance passkey-enrollment issue --account-id <account-id>
```

The command requires an interactive terminal, an exact confirmation, and the exclusive maintenance lock. It prints the enrollment token once; give it to the account holder through a secure channel. The token expires after 15 minutes and is stored only as a SHA-256 digest. To revoke an unused grant, run `podman compose run --rm --no-deps cookbook instance passkey-enrollment revoke --account-id <account-id>`. The account holder submits the token by POST to `/api/auth/passkey/enrollment/begin`, then submits it again with the returned `ceremony_id` and the browser's WebAuthn registration response to `/api/auth/passkey/enrollment/finish`. These endpoints require the exact configured Origin and do not create a login session; the new passkey is used for a separate sign-in. No self-service enrollment form is currently provided.

Account security lists active browser sessions, showing a bounded browser identifier and expiry, and lets you revoke another session without signing out the current browser. Signing out from the current browser remains available from the account menu.

Passkey enrollment/removal, administrator-role changes, and recipe ownership transfers require a sign-in from the last ten minutes. If a session is older, sign in again and retry.

`COOKBOOK_REGISTRATION` sets the initial mode on first startup: `invite_only` (default), `open`, or `disabled`. Administrators can change registration and public browsing settings in the dashboard after setup. Public browsing is off by default; an administrator must explicitly enable it, and existing public cookbooks or recipes require an additional confirmation before they become visible to anonymous visitors. Resources marked private remain private. Administrators can also create and revoke account invitations. Cookbook owners can invite collaborators as viewers or editors, change roles, and remove members. Invitation links are bound to the specified email address and expire after seven days. A cookbook invitation adds the recipient as a member only after they verify their email; the cookbook owner controls the member role. The recipient must verify that mailbox by consuming the one-time email link before an account is created.

Recipe owners can transfer ownership to an existing account by email; the transfer is confirmed in the interface and recorded in the audit log. Creator attribution and revision history remain attached to the recipe.

Recipe details can adjust ingredient quantities to a different serving count for simple numeric, decimal, and fractional amounts. Text such as “to taste” and quantity ranges are left as entered; scaling is a view-only adjustment and does not edit the saved recipe.

Cookbook recipe lists support search, exact tag filtering, and sorting by recent update, oldest update, or title.

In the recipe editor, instruction steps can be added, removed, and moved up or down before saving.

A signed-in reader with access to a recipe can save a copy into their own account. Copies keep the source creator attribution and source recipe ID, belong to the copying account, start private and owner-only, and begin a separate revision history.

Recipe owners can move recipes to their private trash and restore them. `COOKBOOK_TRASH_RETENTION_DAYS` defaults to `30`; expired trash is permanently deleted during startup and daily cleanup. Set it to `0` to retain trashed recipes indefinitely. Permanent cleanup also removes recipe revision history and cookbook references.

Administrators can separately set recipe revision and security audit retention to 7, 30, 90, or 365 days, or unlimited. Both default to 365 days. Older revisions are deleted in bounded batches while each recipe's current snapshot is retained; audit events are also removed in bounded batches. The current settings are visible in the administrator dashboard.

`COOKBOOK_ADMIN_EMAIL` bootstraps the first administrator on startup if no administrator exists. The account can sign in through its verified email address. Keep control of the environment file and email account. Administrators can grant or revoke administrator access in the Accounts section; the interface requires another administrator to change your own role, and the API protects the last administrator.

For command-based email delivery, Cookbook writes one JSON object to the executable's stdin:

```json
{"to":"person@example.test","subject":"Your Cookbook sign-in link","text":"Use this one-time link to sign in. It expires in 15 minutes.","sign_in_url":"https://cookbook.example/auth/confirm?token=..."}
```

The command must deliver the message and exit with status 0. Do not log the one-time URL. SMTP configuration requires STARTTLS and supports optional SMTP authentication.

## Current scope and remaining work

Email-link and passkey authentication, first-admin bootstrap, administrator-controlled registration and account roles, public browsing, and history retention settings, account invitations, private-first SQLite storage, authorization checks, cookbook and recipe APIs, paged public cookbook and recipe discovery, private recipe/cookbook creation and editing, cookbook member sharing, owner-controlled visibility, server-side recipe and cookbook pagination, recipe image uploads, private recipe copying with creator attribution, touch-friendly cooking mode with an ingredient checklist, structured recipe metadata, revision history/diff/restore with bounded retention cleanup, recipe trash with configurable retention, owner-scoped JSON/ZIP export and import (including recipe photos), and a separately retained administrator security audit log are implemented. Public cookbook views include only explicitly public recipes. Opt-in, signed recipe federation is implemented and disabled by default; see [FEDERATION_PROTOCOL.md](FEDERATION_PROTOCOL.md) and its [threat model](FEDERATION_RESEARCH.md). The audit log records successful authenticated API changes and authenticated access denials; it excludes request bodies and query strings. The Go service is deployed independently of the static frontend. Courses remain disabled by default.

Courses are an optional, separate feature. An administrator can enable them in instance settings; authors can create private or public courses with ordered lessons and optional links to recipes they can read. Public courses require public browsing to be enabled, and linked recipes are shown only to readers authorized to access them. When courses are disabled, course pages and API routes are unavailable. Federation remains disabled by default and requires trusted peers to be configured by an administrator.
