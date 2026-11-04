# Cookbook

## Local deployment

The supported container layout serves the built Astro site and Go API as separate artifacts. From `cookbook-repo/`, run `podman compose up --build`; the static Caddy host is available at `http://127.0.0.1:8080` and proxies `/api/*` and `/healthz` to the private Go service. Stop only this project with `podman compose down`; persistent recipe data is in the named `cookbook-data` volume and is retained unless explicitly removed.

Copy `cookbook_back-end/.env.example` to `cookbook_back-end/.env` and configure sign-in delivery and the initial administrator before first startup. Never point local tests at the demo or production database. The Go image is backend-only; it does not embed Astro output. The independent static image serves the Astro build, resolves `/auth/confirm` to the generated `auth/confirm.html` while retaining query parameters, and uses per-page CSP generated from Astro's built CSP meta tags. The generator emits Cloudflare `_headers` and the matching Caddy policy include. Do not hand-maintain CSP hashes.

For deployment topology, origin placeholders, and the optional Cloudflare static-assets proxy template, see [`deploy/README.md`](deploy/README.md) and [`deploy/cloudflare/README.md`](deploy/cloudflare/README.md). No account configuration or deployment is performed by these files.

A self-hostable cookbook for collecting recipes and organizing them into cookbooks. The application uses an independent Go HTTP service, SQLite, and a static Astro frontend with React islands. Instances start private, and public browsing, account registration, courses, and federation are controlled independently.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the data model, access-control rules, API map, and implementation boundaries. [AUDIT_AND_PLAN.md](AUDIT_AND_PLAN.md) tracks milestone acceptance and verification.

## Local development

Requirements: Go 1.24+, Node.js 22.23.4+, and npm.

1. Copy `cookbook_back-end/.env.example` to `cookbook_back-end/.env` and configure the public URL. Email delivery is optional; to disable it, leave `COOKBOOK_EMAIL_COMMAND` empty and clear all four `COOKBOOK_SMTP_*` values. The Go service reads environment variables directly; load the file with your shell or preferred environment loader.
2. Run the API from `cookbook_back-end` with `go run .` (defaults to `127.0.0.1:8000`). It initializes the SQLite schema and serves API/health routes only.
3. In a second terminal, run `npm ci && npm run dev` from `coobook_front-end`. Astro serves the development UI at `http://localhost:4321` and proxies API requests to the Go service.

Run backend checks with `go test ./...` and `go vet ./...` from `cookbook_back-end`. Run frontend checks with `npm test`, `npm run lint`, and `npm run build` from `coobook_front-end`.

## Self-host with Docker Compose

1. Copy `cookbook_back-end/.env.example` to `cookbook_back-end/.env` and set `COOKBOOK_PUBLIC_URL`, `COOKBOOK_RP_ID`, and `COOKBOOK_RP_ORIGIN`. Email delivery is optional. To disable it, leave `COOKBOOK_EMAIL_COMMAND` empty and clear all four `COOKBOOK_SMTP_*` values, including the example host/from values. To enable email-link sign-in, configure either SMTP host/from values (STARTTLS; relay credentials are optional) or `COOKBOOK_EMAIL_COMMAND` with an executable in the container that accepts the documented JSON payload on stdin. SMTP connections and mail commands have a 30-second deadline. With both delivery options unset, the server starts normally and email-link requests return `503 email_unavailable`; passkey authentication remains available.
2. Set `COOKBOOK_ADMIN_EMAIL` and optionally `COOKBOOK_ADMIN_NAME` for the first startup. This creates the first administrator account even when no email delivery is configured.
3. Run `docker compose up --build -d`. Compose binds the service to `127.0.0.1:8000`; put a trusted HTTPS reverse proxy in front. The `cookbook-data` volume stores SQLite data at `/data/cookbook.db` and recipe images under `/data/media`.
4. After setup, remove `COOKBOOK_ADMIN_EMAIL` from `.env`. It provisions an account only when the instance has no users; it will not recreate or promote an account on an existing instance, including one with no administrators.

## Instance owner operations

The first instance owner is selected explicitly from an existing account. While the API is running, an administrator can obtain account IDs from the admin user list. If email sign-in is unavailable, stop the backend and use the read-only local account lookup described below to confirm an account ID. Offline commands coordinate through a persistent Linux advisory lock and fail while the service holds its lock.

For Compose, stop the backend and run the one-shot command against the same data volume:

```sh
podman compose stop cookbook
podman compose run --rm --no-deps cookbook instance owner claim --account-id <account-id>
podman compose up -d cookbook
```

When the first administrator cannot receive an email link, the operator can discover that bootstrapped account's ID locally without querying SQLite manually. Stop the backend, then run the exact-email lookup against the same Compose data volume:

```sh
podman compose stop cookbook
podman compose run --rm --no-deps -T cookbook instance account lookup --email admin@example.test
```

The command requires an exact email match and prints the account ID, email, and display name as one JSON line so terminal control characters in stored names are escaped. It opens the database read-only, makes no account changes, and does not report or infer email verification or roles. Use the returned ID with the initial passkey enrollment command below; that grant is separately confirmed and does not change account roles.

To add another household member when email delivery is unavailable, the host operator can create an ordinary account on an already initialized instance. Stop the backend and run:

```sh
podman compose stop cookbook
podman compose run --rm --no-deps cookbook instance account create --email member@example.test --display-name "Household Member"
podman compose run --rm --no-deps cookbook instance passkey-enrollment issue --account-id <account-id>
podman compose up -d cookbook
```

Account creation requires typing the exact `CREATE ACCOUNT` confirmation shown and acquiring the exclusive maintenance lock. Create and lookup commands print the identity as one JSON line, escaping terminal control characters in stored or supplied names. The create command uses the host operator's authority to provision one local account from the supplied email and display name; it does not verify mailbox ownership or send email. It rejects a missing or uninitialized database before creating its maintenance-lock file, then rechecks initialized account state after acquiring the lock. The account has no credential, session, administrator or owner role, Person actor, or passkey grant. The separately confirmed passkey-enrollment command creates the one-time grant; give its token to the account holder through a secure channel. If the instance has no accounts yet, create the initial administrator through the first-start `COOKBOOK_ADMIN_EMAIL` setup above; `account create` refuses to replace that bootstrap path.

The owner-claim command prompts for an exact confirmation. Claiming changes no administrator role, account credentials, content ownership or private-content access. After claim, only the instance owner can grant or revoke administrator roles or stage/activate the local federation signing-key rotation. Existing administrators retain ordinary admin features and peer management, but lose those instance-identity controls. Before the first claim, existing administrators retain their current role and federation identity controls. The canonical federation origin remains a host-operator startup setting (`COOKBOOK_FEDERATION_ORIGIN`); it is not changed through admin settings.

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

The offline Person actor recovery verifier reads an explicit database file without applying migrations or changing account state. Use `describe` to emit the sorted version-1 actor manifest, then `verify` to compare that manifest against a database copy. The manifest contains only actor IDs, canonical URIs, lifecycle state, and active public-key fingerprints; retired actors have a null fingerprint. Verification accepts a manifest file or `-` for stdin and never uses `COOKBOOK_DATABASE` as a fallback:

```sh
cookbook-backend instance recovery describe --database /path/to/extracted/cookbook.db > actor-manifest.json
cookbook-backend instance recovery verify --database /path/to/extracted/cookbook.db --manifest actor-manifest.json
```

For a database with uncheckpointed WAL commits, keep its matching `-wal` and `-shm` sidecars alongside the database during verification. The verifier reads through SQLite's read-only snapshot and fails closed if required WAL state is unavailable; it never treats the main database file alone as the complete snapshot.

Restore with `./scripts/restore-compose.sh /path/to/cookbook-....tar.gz` using Podman Compose or Docker Compose and Python 3.9+. The archive and its directory must be accessible only by the current user. The script validates archive paths, file types, permissions, sizes, checksums and federation key files, then replaces the database, media and `/data/secrets`. It does not create an automatic pre-restore backup. If you need a separate backup, choose an explicitly agreed private destination and create it separately before restoring. Legacy database/media archives require `--accept-federation-key-loss`; see [Backup and recovery](docs/backup-and-recovery.md). If applying a restore fails after shutdown, the service remains stopped for inspection. Database schema upgrades run when the service starts.

## Library

The signed-in Library shows recipes you can currently access separately from recipes and cookbooks you have saved. Saving a cookbook keeps a personal reference; it does not add you as a member, copy the cookbook or its recipes, or grant access. The Library checks current access when it displays a saved cookbook. If access is revoked or the cookbook is deleted, the saved entry remains removable but shows only as unavailable. Removing a saved reference does not change the source cookbook or its membership.

An authenticated cookbook detail page can check one saved reference with `GET /api/library/cookbook-bookmarks/status?local_cookbook_id=...`. The private, no-store response contains only `{"saved":false}` or `{"saved":true,"bookmark_id":"…"}`; missing and currently unreadable cookbooks both return `not_found`. This read does not save the cookbook or grant access.

An authenticated recipe detail page can check one saved reference with `GET /api/library/bookmarks/status?local_recipe_id=...` for a local recipe or `?remote_object_uri=...` for a currently cached remote recipe. The private, no-store response contains only `{"saved":false}` or `{"saved":true,"bookmark_id":"…"}`. Missing or currently unreadable local recipes, and missing or invalidated remote cache entries, return `not_found`. Remote status never fetches from the network, and saving does not grant recipe access.

`GET /api/library/cookbooks?limit=24&offset=0` returns a signed-in account's unique owned, member, or saved cookbook references with current availability, relationship flags, current cookbook data, and visible attribution. Publicly readable cookbooks are included only when owned, joined, or saved. Saved unavailable references retain only their bookmark identity and save time; saving never grants cookbook access.

`GET /api/library/recipes?limit=24&offset=0` returns the signed-in account's unique owned, shared-through-current-cookbook-membership, linked-from-an-owned-cookbook, or saved recipe references. Each local recipe candidate still passes the canonical recipe read policy; public-only unrelated recipes are excluded. Saved remote recipes resolve only from the current validated local cache and are never fetched. The available recipe projection includes only currently readable cookbook associations and each current actor membership role; unavailable saved references expose only bookmark ID, fixed target kind, and save time. The response merges canonical identities before counting and paging, orders by `updated_at DESC` (or save time for unavailable references), then lexical `bookmark:<id>`, `local:<recipe-id>`, or `remote:<canonical-object-uri>` key, defaults to 24 items, caps pages at 100, and is private/no-store.

The endpoint accepts optional `q`, a trimmed literal substring query over title, description, and tags, limited to 200 UTF-8 bytes. Matching follows SQLite `lower()`: ASCII letters are case-insensitive, while non-ASCII letters are compared literally. It searches that personal-inclusion set rather than every recipe the account can read. Blank queries retain unavailable saved references; nonblank queries omit them. Saved remote snapshots are searchable from the local cache only; search never contacts a peer.

On existing instances, saved remote recipes remain unavailable until their cached search fields pass local validation and backfill. Blank queries keep the redacted saved reference during backfill; nonblank queries omit it. Backfill reads the local cache and never contacts peers.

`GET /api/library/recipes/remote?remote_object_uri=<encoded-object-uri>` returns one exact validated cached public remote recipe snapshot to a signed-in account, including on a private instance. The reader verifies the canonical object URI and its cached source identity/body in a read-only transaction. The endpoint does not scan bookmarks, require a saved bookmark as access, or fetch from a peer; missing, deleted, or source-mismatched snapshots return `not_found`, malformed selectors/URIs return `400`, and cache/storage failures return `500`. The response is private/no-store and contains only remote snapshot metadata and public recipe content.

## Personal data export and import

Signed-in users can download their owned, non-deleted recipes and owned cookbooks from `/api/export` as a `cookbook-export-v5` ZIP bundle, then import it through `POST /api/import` (100 MiB maximum compressed size). Import creates new private, owner-controlled copies and starts fresh revision history. It preserves recipe photos, creator attribution, source IDs, and cookbook membership links, but does not transfer account identities, collaborators, revisions, or audit events. V5 also carries the requesting account's saved recipe and cookbook reference identities and save times; it does not include snapshots of referenced content or grant access. A saved reference is not rebound to an imported copy. References whose instance namespace differs from this instance remain unavailable here. Bundles are limited to 100,000 cookbook-recipe links and 10,000 references of each saved type; recipe content is checked against current creation limits. Older v1–v4 archives remain importable. An export is a portability aid, not a full instance backup; use the stopped-service database and media backup procedure above for complete recovery.

## Authentication and registration

Sign-in uses one-time email links when email delivery is configured, and WebAuthn passkeys. With email delivery disabled, email-link requests return `503 email_unavailable`; passkey login and enrollment remain available. Opening an email link displays a confirmation page; the token is consumed only after the user chooses “Continue to Cookbook,” so link preloading alone does not redeem it. Email sign-in requests are limited to five per email address per hour and 30 per client IP per hour when enabled; passkey ceremony starts are limited to 30 per client IP per hour, and passkey login completions are limited to 60 per client IP per hour. `COOKBOOK_TRUSTED_PROXIES` accepts comma-separated proxy CIDRs; only when the connecting peer matches one of those ranges does the service use the trusted proxy chain in `X-Forwarded-For`. Leave it unset unless the network boundary is controlled.

`COOKBOOK_PUBLIC_URL` must be the browser-facing app origin; passkeys require HTTPS outside localhost. `COOKBOOK_RP_ID` defaults to its hostname and `COOKBOOK_RP_ORIGIN` must match the public origin.

For an account that cannot receive an email sign-in link, an operator can issue a short-lived, one-time initial passkey grant while the backend is stopped. Use the exact existing account ID from the administrator user list or the read-only local lookup above:

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

`COOKBOOK_ADMIN_EMAIL` bootstraps the first administrator on startup only when the instance has no user accounts. It neither creates an account nor promotes an existing account on a populated instance. When email delivery is configured, the account can sign in through its verified email address. Keep control of the environment file and email account. Administrators can grant or revoke administrator access in the Accounts section; the interface requires another administrator to change your own role, and the API protects the last administrator.

For command-based email delivery, Cookbook writes one JSON object to the executable's stdin:

```json
{"to":"person@example.test","subject":"Your Cookbook sign-in link","text":"Use this one-time link to sign in. It expires in 15 minutes.","sign_in_url":"https://cookbook.example/auth/confirm?token=..."}
```

The command must deliver the message and exit with status 0. Do not log the one-time URL. SMTP configuration requires STARTTLS and supports optional SMTP authentication.

## Current scope and remaining work

Email-link and passkey authentication, first-admin bootstrap, owner- or administrator-managed instance settings, owner-governed (or pre-claim administrator-managed) account roles, account invitations, private-first SQLite storage, authorization checks, cookbook and recipe APIs, paged public cookbook and recipe discovery, private recipe/cookbook creation and editing, cookbook member sharing, owner-controlled visibility, server-side recipe and cookbook pagination, recipe image uploads, private recipe copying with creator attribution, touch-friendly cooking mode with an ingredient checklist, structured recipe metadata, revision history/diff/restore with bounded retention cleanup, recipe trash with configurable retention, owner-scoped JSON/ZIP export and import (including recipe photos), and a separately retained administrator security audit log are implemented. Public cookbook views include only explicitly public recipes. Opt-in, signed recipe federation is implemented and disabled by default; see [FEDERATION_PROTOCOL.md](FEDERATION_PROTOCOL.md) and its [threat model](FEDERATION_RESEARCH.md). The audit log records successful authenticated API changes and authenticated access denials; it excludes request bodies and query strings. The Go service is deployed independently of the static frontend. Courses remain disabled by default.

Courses are an optional, separate feature. The instance owner or an administrator can enable them in instance settings; authors can create private or public courses with ordered lessons and optional links to recipes they can read. Public courses require public browsing to be enabled, and linked recipes are shown only to readers authorized to access them. When courses are disabled, course pages and API routes are unavailable. Federation remains disabled by default and requires trusted peers to be configured by an administrator.
