# Local creator profiles

Creator profiles are local identities linked to one account. Migration v7 adds the profile and typed attribution records without backfilling legacy names, owners, revision editors, cookbook members, or imported content.

## Visibility and profile API

Profiles have `private` or `instance` visibility. Private profiles are readable only by their owner. Instance profiles are readable by signed-in local accounts, even when anonymous instance access is disabled. Profile visibility never grants access to a recipe or cookbook; every attributed resource is still filtered by its ordinary access rules. Administrators and instance owners receive no privacy bypass. There is no public profile endpoint or cross-account credit workflow.

`GET /api/account/creator-profile` returns `{ "profile": null }` when no profile exists, otherwise `{ "profile": CreatorProfile }`. `PUT /api/account/creator-profile` accepts `{ "public_name", "bio", "visibility" }` and returns the same envelope with the saved profile. `CreatorProfile` is `{ "id", "public_name", "bio", "visibility", "created_at", "updated_at" }`; it contains no account ID, email, actor URI, or key. Writes require the normal authenticated session, same-origin CSRF protection, and audit middleware.

Private profile names may be empty. Instance visibility requires a 1–80 Unicode scalar name (up to 320 UTF-8 bytes). Bio is plain text capped at 1,000 UTF-8 bytes. An empty private profile is created lazily for typed recipe creation, cookbook creation, or a fork; its name is blank and it does not copy the account display name. `GET /api/creator-profiles/{id}` uses the same `{ "profile": CreatorProfile }` envelope and returns generic `404 not_found` for absent or unreadable profiles.

`GET /api/creator-profiles/{id}/recipes?limit=&offset=` returns `{ "profile": CreatorProfile, "items": [{ "recipe": Recipe, "role": string, "provenance": string }], "limit": number, "offset": number }`. The `Recipe` is the existing authenticated recipe DTO after ACL checks. The cookbook route returns the same envelope with items `{ "cookbook": { "id", "title", "description", "visibility", "created_at", "updated_at" }, "role", "provenance" }`; it omits account custody IDs. Both lists include only local typed attribution rows. A visible profile with no readable attributed resources returns an empty `items` array. An absent or inaccessible profile returns `404 not_found`; invalid pagination returns `400 invalid_pagination`. Private API responses are `private, no-store`.

## Typed credits in canonical reads

Authenticated canonical `GET /api/recipes`, `GET /api/recipes/{id}`, `GET /api/cookbooks`, and `GET /api/cookbooks/{id}` responses may include the additive `creator_attributions` array. It is omitted when no typed rows are readable. A local row has `subject_kind: "local"`, a `profile` object containing only `id`, current `public_name` and `visibility`, plus its stored `role` and `provenance`. A private profile row is visible only to its owner; an instance profile row is visible to signed-in local accounts. Both the resource ACL and profile ACL must allow the row. Admin and instance-owner status do not bypass either rule.

An `external_claim` row contains only its discriminant, optional `name_claim`, role, and provenance. It is unverified and never resolves or links to a local profile, even if its foreign identifiers happen to match a local ID. The separate legacy `creator_name` remains unchanged and is not merged or deduplicated with typed credits. Recipe cards and detail views should treat role and provenance as labels, not proof; a cookbook creator denotes collection creation, while a recipe adapter denotes a fork relationship distinct from `source_recipe_id`. Anonymous and `/api/public/*` responses do not gain this field. The authenticated list projection batches credits for the page within its read snapshot.

## Creation and attribution

Recipe create accepts an optional create-only `creator_attribution` object:

- `{ "kind": "local_profile" }` records the signed-in account's local profile as creator, with `self_asserted_at_creation` provenance. It never accepts a caller-supplied profile ID. The profile is lazily created as private with an empty name. Its name and ID are not copied into the recipe's legacy `creator_name` field or public projection.
- `{ "kind": "name_claim", "name": "…" }` records only bounded unverified text in the existing `creator_name` field; it creates no profile relation.
- `{ "kind": "unknown" }` records no byline or typed creator relation.
- Omitting `creator_attribution` preserves legacy client behavior and does not create typed attribution.

The field is accepted only on create. Update and revision-restore requests reject it as an unknown field, so editing a recipe cannot silently relabel its creator. New cookbook creation records the authenticated account's collection-creator relation in the same transaction. Forking retains `source_recipe_id` and adds the current account as an adapter in the existing transaction; it does not copy source creator identity onto the copy as a local creator.

## Portable attribution

Export v4 remains compatible with v1–v3. It carries attribution claims but no profile settings. Local attribution is exported only if the exporter can read the profile: the owner may self-export a private profile claim, while another account may export an instance-visible profile claim only when the resource is also exportable. An unreadable local relation is omitted wholly, including its canonical creator/source IDs; legacy `creator_name` is handled independently under its existing export policy.

V4 import always stores attribution as an external, unverified claim, never binds it to the importer, and never fetches a source. Re-export preserves the original source instance ID, creator ID, optional name claim, role, and provenance without recursive wrapping. Claims use a 32-character lowercase hex source-instance/creator-ID pair, known role, UTF-8 name up to 80 Unicode scalars/320 bytes, and provenance up to 128 bytes. Duplicate target/source/creator/role claims fail rather than merge. Unknown fields, malformed claims, duplicate claims, and missing archive targets are rejected; import conflicts roll back the resource transaction.
