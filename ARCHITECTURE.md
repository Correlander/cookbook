# Cookbook architecture

This document describes the implementation in this checkout. It is a working architecture record, not a claim that every product milestone is finished. See [AUDIT_AND_PLAN.md](AUDIT_AND_PLAN.md) for acceptance status and [FEDERATION_RESEARCH.md](FEDERATION_RESEARCH.md) for the separate federation decision and security gates.

## Runtime and storage

- **Application:** one Go HTTP service serves the JSON API and the built React/Vite client. The frontend is client-rendered; there is no SSR requirement or separate application service.
- **HTTP lifecycle:** the server limits header reads to 5 seconds, full request reads and response writes to 3 minutes, idle connections to 60 seconds, and request headers to 1 MiB. The read deadline accommodates the 100 MiB compressed import limit; deployments with unusually slow clients may need a configurable timeout policy. SIGINT/SIGTERM drains active requests for up to 20 seconds before closing the listener; Compose allows 25 seconds before forced termination.
- **Database:** SQLite, opened with foreign-key enforcement, WAL mode, a busy timeout, and a single connection. Startup creates the schema and applies additive compatibility columns. SQLite and local media live together under `/data` in Compose; both must be backed up.
- **Media:** recipe images are local files with random generated names. Uploads accept JPEG, PNG, and WebP, enforce a 5 MiB file limit and a 40 million pixel limit, and are decoded to check type and dimensions. Recipe/revision rows refer to the image filename. Reads go through the API and check recipe visibility; only the recipe owner can upload, replace, or remove an image. Private images use `private, no-store`; images on public recipes in public instances may be cached for five minutes.
- **Operations:** Compose binds the service to loopback by default and persists `/data`. Put a trusted HTTPS reverse proxy in front for remote use. `scripts/backup-compose.sh` stops the service and archives the database, SQLite sidecars, and media; restore validates the archive, takes a pre-restore backup, and leaves the service stopped if restore fails. Archives contain plaintext account and recipe data, so the scripts require owner-only directory and archive permissions; store off-host copies encrypted.

## Data model

| Tables | Purpose and relationships |
| --- | --- |
| `instance_settings` | Singleton policy: public browsing, registration, revision/audit retention, and the courses flag. Courses and federation default off. |
| `users`, `sessions`, `email_challenges`, `webauthn_credentials`, `webauthn_ceremonies`, `auth_rate_limits` | Accounts and passwordless authentication state. Session tokens and one-time email tokens are stored as hashes; each session has a separate stable identifier and a bounded browser user-agent string for per-session identification and revocation. Passkey credentials use the maintained Go WebAuthn library. Expired transient rows are removed in bounded daily batches. |
| `cookbooks`, `cookbook_memberships`, `invitations` | Cookbook ownership, editor/viewer membership, and hashed, expiring invitations. Membership does not change recipe ownership. |
| `recipes`, `cookbook_recipes`, `recipe_revisions` | Canonical recipe data and structured ingredients, ordered steps, servings, times, tags, visibility, image reference, owner, creator attribution, optional source recipe, optimistic version, cookbook references, and snapshots. A recipe may be referenced by multiple cookbooks without copying its canonical record. |
| `audit_events` | Paged administrative security events for successful authenticated mutations and authenticated 403 denials. Request bodies and query strings are omitted. |
| `courses`, `course_lessons` | Optional, separately modeled course content. Lessons can link to recipes; deleting a linked recipe clears the lesson link. |

Recipe `owner_id` is the current account owner. `creator_name` preserves creator attribution, and `source_recipe_id` records a fork/source reference. Copying a recipe creates a private owner-controlled record and starts new revision history. Export/import preserves attribution and media, creates private owned copies, and does not transfer accounts, membership, revisions, or audit events.

## Access-control matrix

The API is the enforcement boundary. Hiding a control in the client does not grant or deny access. Public access requires both an enabled public instance and the resource's explicit `public` visibility.

| Actor | Read cookbooks and recipes | Edit content | Manage sharing or ownership |
| --- | --- | --- | --- |
| Anonymous | Publicly listed resources only when public browsing is enabled. Private and unlisted resources are not anonymously readable. | None. | None. |
| Signed-in nonmember | Own resources; otherwise the same public resources as anonymous visitors. | Own recipes and cookbooks. | May fork an accessible recipe into a private copy. |
| Cookbook viewer | Cookbooks they belong to; recipes are returned only when the user independently has access through ownership, membership in a cookbook containing the recipe, or public visibility. | Cannot edit cookbook or recipe content by virtue of viewer membership. | No revision-history access, membership, or invitation administration. |
| Cookbook editor | Same recipe read rules as a viewer. | May edit a linked recipe only when that recipe's `edit_policy` is `cookbook_editors`. The recipe owner can always edit. | Can read and compare recipe revisions when edit policy permits; cannot change cookbook membership, invitations, cookbook metadata, recipe visibility, or recipe ownership. |
| Cookbook owner | Their cookbook and any recipes they can independently read under the recipe policy. Owning a cookbook alone does not grant access to another user's private recipe. | May edit cookbook metadata; recipe editing still follows recipe ownership/edit policy. | May invite, change roles, remove members, revoke invitations, and control cookbook visibility. |
| Recipe owner | Their non-trashed recipes and associated readable cookbooks. | May edit, trash/restore, manage images, and change visibility. Optimistic version checks protect updates and revision restores. | May transfer recipe ownership to an existing account; creator attribution remains. |
| Instance administrator | Administrator pages and settings. Content access still follows the resource rules above. | May change instance settings and user administrator roles. | May manage account invitations and roles, and read the audit log. Cannot change their own admin role; the final administrator cannot be demoted. |
| Course owner | Their courses; public courses are readable only on a public instance. Linked recipe content follows recipe access independently. | May manage their course and lessons while courses are enabled. | No course sharing or membership role model. |

Unlisted cookbooks and recipes are not public-by-link: they remain available to owners and authorized cookbook members. Public cookbook detail also filters out recipes the requester cannot independently read. Returned recipe cookbook IDs are filtered to associations the requester can read. Reading a public recipe does not expose its prior revisions; revision listing and diff require recipe edit permission, matching the history controls in the app.

## HTTP surface

All API JSON responses are `no-store`. Mutating API requests use same-origin checks and authenticated routes are wrapped with session validation and mutation auditing where applicable. Token-bearing magic-link and invitation pages are also `no-store`.

| Route family | Methods and access |
| --- | --- |
| `/healthz` | `GET`; process health. |
| `/api/auth/email`, `/api/auth/consume`, `/api/auth/me`, `/api/auth/logout` | Email sign-in request and explicit one-time token consumption (`POST`), session read (`GET`), and logout (`POST`). |
| `/api/auth/passkey/register/{begin,finish}`, `/api/auth/passkey/login/{begin,finish}` | `POST`; passkey ceremonies. Registration requires a session issued within ten minutes; ceremony origin, challenge, and replay checks are server-side. |
| `/api/auth/passkeys`, `/api/auth/passkeys/{credential}` | `GET` list; `DELETE` revoke requires recent authentication. |
| `/api/auth/sessions`, `/api/auth/sessions/{id}` | `GET` lists the signed-in account's active sessions; `DELETE` revokes one of that account's sessions. |
| `/api/public/recipes`, `/api/public/cookbooks`, `/api/public/cookbooks/{id}/recipes` | `GET`; public browsing, search, and pagination. Instance and resource visibility are enforced. |
| `/api/cookbooks`, `/api/cookbooks/{id}` | `GET`/`POST` collection; `GET`/`PATCH` item. Authenticated; edits are owner-only. |
| `/api/cookbooks/{id}/members`, `/api/cookbooks/{id}/members/invitations` | Membership and invitation list/update/create/revoke; cookbook owner controls changes. |
| `/api/recipes`, `/api/recipes/{id}` | `GET`/`POST` collection; `GET`/`PUT`/`DELETE` item. Authenticated writes; delete moves the recipe to trash. |
| `/api/recipes/{id}/image` | `GET`/`HEAD` visibility-checked reads; `POST`/`PUT`/`DELETE` owner-only writes. |
| `/api/recipes/{id}/revisions`, `/api/recipes/{id}/revisions/diff`, `/api/recipes/{id}/revisions/{version}/restore` | Revision reads, comparison, and restore-as-new-version require recipe edit rights. |
| `/api/recipes/trash`, `/api/recipes/{id}/restore`, `/api/recipes/{id}/fork`, `/api/recipes/{id}/transfer` | Owner trash/restore, accessible recipe copy, and owner-initiated ownership transfer; transfer requires a session issued within ten minutes. |
| `/api/export`, `/api/import` | Authenticated owner-scoped portability export and import. ZIP imports have bounded compressed/expanded sizes and path/type validation. |
| `/api/admin/settings`, `/api/admin/users`, `/api/admin/users/{id}`, `/api/admin/invitations`, `/api/admin/audit` | Administrator settings, paged user/audit reads, role updates (recent authentication required), and account invitations. |
| `/api/courses`, `/api/courses/{id}`, `/api/courses/{id}/lessons[/<lesson>]` | Authenticated course authoring when enabled. The API returns `404` for course routes while disabled. |
| `/api/public/courses[/<id>[/lessons]]` | `GET`; public course browsing when both courses and public browsing are enabled. |

Unmatched `GET` and `HEAD` paths serve the frontend shell for client-side routing. The separate `marketing-site/` is static and is not part of the signed-in app build.

## Boundaries and tradeoffs

- SQLite and local media keep a self-hosted install small and easy to back up. The service is designed around a single SQLite database and filesystem volume, not multi-node shared storage.
- Revision snapshots and the security audit log are separate records with independent retention. Retention settings allow 7, 30, 90, 365 days, or unlimited; cleanup is bounded and retains each recipe's latest snapshot.
- Courses are a secondary feature, disabled by default, and are separate from recipe instructions. The current UI/API implement local course authoring and reading; they do not establish browser end-to-end acceptance.
- Federation is researched but not implemented. `federation_enabled` is only a default-off database setting today; there are no peer routes, outbound queues, or remote delivery. Follow the prerequisites and threat controls in `FEDERATION_RESEARCH.md` before implementation.
- The marketing site is a separate static artifact with an application screenshot. Its content is reviewed against current features; a deployment target has not been selected, and publication remains outstanding.
