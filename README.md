# Cookbook

A self-hostable cookbook for collecting recipes and organizing them into cookbooks. The application uses a Go HTTP service, SQLite, and a React/Vite interface. Instances start private, and public browsing, account registration, courses, and federation are controlled independently.

## Local development

Requirements: Go 1.24+, Node.js 20+, and npm.

1. Copy `cookbook_back-end/.env.example` to `cookbook_back-end/.env` and configure the public URL and mail delivery. The Go service reads environment variables directly; load the file with your shell or preferred environment loader.
2. Run the API from `cookbook_back-end` with `go run .` (defaults to `127.0.0.1:8000`). It initializes the SQLite schema. Its embedded frontend is a placeholder during local development; the Vite server is the development UI.
3. In a second terminal, run `npm ci && npm run dev` from `coobook_front-end`. Vite serves the development UI at `http://localhost:5173` and proxies API requests to the Go service.

Run backend checks with `go test ./...` and `go vet ./...` from `cookbook_back-end`. Run frontend checks with `npm test`, `npm run lint`, and `npm run build` from `coobook_front-end`.

## Self-host with Docker Compose

1. Copy `.env.example` to `.env`, then set `COOKBOOK_PUBLIC_URL`, `COOKBOOK_RP_ID`, `COOKBOOK_RP_ORIGIN`, and the SMTP host/from values. SMTP delivery requires STARTTLS; credentials are optional for relays that do not require authentication. Alternatively, configure `COOKBOOK_EMAIL_COMMAND` with an executable in the container that accepts the documented JSON payload on stdin.
2. Set `COOKBOOK_ADMIN_EMAIL` and optionally `COOKBOOK_ADMIN_NAME` for the first startup. This creates the first administrator account. Leave the email challenge/sign-in flow configured before making the instance reachable.
3. Run `docker compose up --build -d`. Compose binds the service to `127.0.0.1:8000`; put a trusted HTTPS reverse proxy in front. The `cookbook-data` volume stores SQLite data at `/data/cookbook.db` and recipe images under `/data/media`.
4. After setup, remove `COOKBOOK_ADMIN_EMAIL` from `.env`. It has no effect once an administrator already exists.

Run `./scripts/backup-compose.sh` to create a private archive in `./backups`. The helper stops the Compose service, archives the SQLite database together with any WAL or rollback-journal sidecar and `/data/media`, and restarts the service if it was running. Set `COOKBOOK_BACKUP_DIR` to change the default directory or pass a directory argument. Keep backups in encrypted storage outside the host as well; archives are not encrypted and contain accounts and private cookbook data.

Restore with `./scripts/restore-compose.sh /path/to/cookbook-....tar.gz` from a host with Docker Compose and Python 3.9+. The archive and its directory must not be group- or world-writable. The script validates archive paths and file types, creates a fresh pre-restore backup in `COOKBOOK_BACKUP_DIR` (default `./backups`), replaces the database and media, and restarts the service if it was running. If applying the restore fails after shutdown, the service remains stopped for inspection. After a successful restore, verify the service health check and sign in before deleting the pre-restore backup. Database schema upgrades run when the service starts.

## Personal data export and import

Signed-in users can download their owned, non-deleted recipes and owned cookbooks from `/api/export` as a `cookbook-export-v2` ZIP bundle, then import it through `POST /api/import` (100 MiB maximum compressed size). Import creates new private, owner-controlled copies and starts fresh revision history. It preserves recipe photos, creator attribution, source IDs, and cookbook membership links, but does not transfer account identities, collaborators, revisions, or audit events. Older `cookbook-export-v1` JSON bundles remain importable. An export is a portability aid, not a full instance backup; use the stopped-service database and media backup procedure above for complete recovery.

## Authentication and registration

Sign-in uses one-time email links and WebAuthn passkeys. Email sign-in requests are limited to five per email address per hour and 30 per client IP per hour; passkey registration and sign-in ceremonies are limited to 30 starts per client IP per hour. `COOKBOOK_TRUSTED_PROXIES` accepts comma-separated proxy CIDRs; only when the connecting peer matches one of those ranges does the service use the trusted proxy chain in `X-Forwarded-For`. Leave it unset unless the network boundary is controlled.

`COOKBOOK_PUBLIC_URL` must be the browser-facing app origin; passkeys require HTTPS outside localhost. `COOKBOOK_RP_ID` defaults to its hostname and `COOKBOOK_RP_ORIGIN` must match the public origin.

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

Email-link and passkey authentication, first-admin bootstrap, administrator-controlled registration and account roles, public browsing, and history retention settings, account invitations, private-first SQLite storage, authorization checks, cookbook and recipe APIs, paged public cookbook and recipe discovery, private recipe/cookbook creation and editing, cookbook member sharing, owner-controlled visibility, server-side recipe and cookbook pagination, recipe image uploads, private recipe copying with creator attribution, touch-friendly cooking mode with an ingredient checklist, structured recipe metadata, revision history/diff/restore with bounded retention cleanup, recipe trash with configurable retention, owner-scoped JSON/ZIP export and import (including recipe photos), and a separately retained administrator security audit log are implemented. Public cookbook views include only explicitly public recipes. A protocol comparison and threat model are documented in [FEDERATION_RESEARCH.md](FEDERATION_RESEARCH.md); federation implementation remains gated and disabled by default. The audit log records successful authenticated API changes and authenticated access denials; it excludes request bodies and query strings. The Go service can embed and serve a production frontend build; the Dockerfile creates that build. Courses remain disabled by default.

Courses are an optional, separate feature. An administrator can enable them in instance settings; authors can create private or public courses with ordered lessons and optional links to recipes they can read. Public courses require public browsing to be enabled, and linked recipes are shown only to readers authorized to access them. When courses are disabled, course pages and API routes are unavailable. Federation remains disabled by default.
