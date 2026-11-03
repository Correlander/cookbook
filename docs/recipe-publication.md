# Recipe visibility, publication, and federation

Cookbook stores local access visibility, public publication lifecycle, outbound federation consent, and index eligibility separately. These settings have different audiences and effects. Changing one does not implicitly grant the others.

## The four controls

| Control | Values | Effect |
| --- | --- | --- |
| Local visibility | `private`, `unlisted`, `public` | Controls who can read locally. `unlisted` is not a secret link or access capability. |
| Publication lifecycle | `unpublished`, `published`, `withdrawn` | Controls whether an eligible recipe appears in anonymous local public listings and projections. It never changes visibility. |
| Federation consent | `off`, `followers` | Controls owner consent for outbound recipe events. It defaults to `off`; current delivery remains the administrator-enrolled Exchange v1 peer profile. |
| Index eligibility | boolean | Reserved for a future index submission/removal adapter. It defaults to `false`; the API currently rejects `true`. |

Anonymous local reading requires all of these conditions: the instance is public, the recipe visibility is `public`, the recipe is not trashed, and publication lifecycle is `published`. Publishing never makes a private or unlisted recipe readable. Current public APIs provide filtered lists; a public recipe-by-ID detail endpoint has not shipped.

## Creating a recipe

`POST /api/recipes` accepts the local-only fields `confirm_public_access` and optional `initial_publication` in addition to the recipe fields.

New recipes default to private visibility and the following policy when `initial_publication` is omitted:

```json
{
  "publication_state": "unpublished",
  "federation_mode": "off",
  "index_eligible": false
}
```

Any create request that sets `visibility` to `public` must also send `"confirm_public_access": true`. This confirms only the local audience expansion. It does not publish the recipe or enable federation or indexing.

To create a locally public and published recipe without outbound federation consent, send the full initial choice in the same request:

```json
{
  "title": "Example recipe",
  "description": "A short description",
  "ingredients": [],
  "steps": [{ "text": "Prepare the dish." }],
  "visibility": "public",
  "confirm_public_access": true,
  "initial_publication": {
    "publication_state": "published",
    "federation_mode": "off",
    "index_eligible": false
  }
}
```

Initial publication requires public visibility and a public instance. Initial `followers` consent additionally requires the instance federation gate and a canonical federation origin. `index_eligible: true` is unavailable. Recipe, policy, first revision, cookbook references, audit event, and required federation work are committed together. These fields are local API fields and are not accepted in the federation wire format.

A recipe created with public visibility but left `unpublished` stays hidden from anonymous recipe listings. The publication panel and form flow are not yet integrated into the shipped frontend, so current clients may not expose these choices.

## Reading and changing publication policy

Only the current recipe owner can read or change its publication policy. Non-owners, including instance administrators, do not get a publication-policy read or write bypass. Missing/deleted recipes and non-owner requests are hidden as not found.

- `GET /api/recipes/{id}/publication` returns the owner’s policy view.
- `PUT /api/recipes/{id}/publication` replaces the complete writable policy tuple. It does not update recipe body, visibility, or recipe version.

Both successful responses return `200`, `Cache-Control: private, no-store`, and the same canonical JSON representation shape. The view includes read-only visibility and instance-gate context so a change to those gates changes the ETag. `updated_by` is included only in this owner-only representation.

A successful GET response has this shape:

```json
{
  "recipe_id": "recipe-id",
  "publication_state": "unpublished",
  "federation_mode": "off",
  "index_eligible": false,
  "updated_by": "owner-id",
  "updated_at": "2026-11-03T12:00:00.000000000Z",
  "published_at": null,
  "visibility": "private",
  "public_instance": true,
  "federation_enabled": false,
  "federation_origin": ""
}
```

The response includes a strong ETag exactly shaped as `"sha256:<64-lowercase-hex-digits>"`. The digest covers the exact canonical response bytes. Send that header value unchanged on PUT:

```http
If-Match: "sha256:<etag-from-the-current-GET>"
Content-Type: application/json
```

```json
{
  "publication_state": "published",
  "federation_mode": "off",
  "index_eligible": false
}
```

The request must include all three policy fields and exactly one valid strong `If-Match` value. The policy timestamp advances monotonically for every actual policy change, preventing a stale token from becoming valid again after a state revert. A no-op PUT returns the current representation and ETag without changing timestamps, audit events, or federation outbox state.

| Response | Meaning |
| --- | --- |
| `401 unauthorized` | No authenticated account. |
| `404 not_found` | Recipe is missing, deleted, or not owned by the caller. |
| `428 precondition_required` | `If-Match` is missing. |
| `400 invalid_precondition` | ETag is weak, wildcard, malformed, or multiple values were sent. |
| `412 precondition_failed` | ETag is stale; response includes the current ETag. Fetch and review the new state before retrying. |
| `400 invalid_publication_policy` | Enum or state transition is invalid. |
| `409 publication_unavailable` / `federation_unavailable` | Current visibility, instance, or federation gate does not allow the requested opt-in. |
| `409 index_submission_unavailable` | Index integration is not available. |
| `503 federation_queue_unavailable` | Required federation state/tombstone could not be safely recorded; the policy change is rolled back. |

### Allowed lifecycle changes

- `unpublished` can remain unpublished or become published.
- `published` can remain published or become withdrawn. It cannot be reset to unpublished; withdraw to preserve publication history.
- `withdrawn` can remain withdrawn or be explicitly republished.
- Consent may be changed while policy remains published, subject to the current gates. Withdrawing requires federation mode off and index eligibility false.
- Publishing requires public visibility and a public instance. Enabling `followers` requires published/public state, a public instance, federation enabled, and a canonical origin.

Entering `published` sets `published_at`. Withdrawal preserves the previous timestamp; explicit republishing records a new one. Policy changes update `updated_by` and `updated_at`, but do not create a recipe body revision or increment its version.

## Visibility, revisions, and lifecycle actions

Recipe body updates remain separate from policy PUTs. Moving a recipe from private or unlisted to public requires an owner’s `confirm_public_access: true` in the recipe update. The check is repeated inside the recipe mutation transaction. Non-owner cookbook editors cannot change visibility or publication/federation/index policy.

Changing visibility to private or unlisted immediately narrows local reads and queues required federation reconciliation in the same recipe/revision transaction. It does not silently turn off stored owner federation consent. If the owner later restores public visibility with a fresh confirmation, outbound eligibility can resume when consent and current instance/peer gates permit it.

Revision restore restores recipe content and the revision’s visibility only; it does not restore publication or federation policy. A restored visibility expansion still requires confirmation.

Trash, restore, and ownership transfer update policy, audit, and required federation outbox state transactionally:

- **Trash:** marks the recipe deleted; published becomes withdrawn; federation and index consent become off; the ETag advances; the last `published_at` is retained. A required tombstone is queued in the same transaction.
- **Restore from trash:** advances the policy ETag and leaves publication withdrawn/unpublished with federation and index consent off. Restore never resumes public or federated delivery.
- **Transfer ownership:** a published recipe becomes withdrawn; federation and index consent become off in every lifecycle state before the new owner takes control. The new owner must make a fresh explicit choice.

If required audit or federation work cannot be recorded, these lifecycle actions roll back. Previously downloaded or exported copies cannot be recalled by withdrawal, trash, or tombstones.

## Federation and legacy data

Current Exchange v1 egress requires current owner consent (`followers`) plus a published, public, active recipe; public-instance and federation gates must allow delivery, and the configured origin must be canonical. The enabled peer is still administrator-enrolled. Consent revocation, withdrawal, making content locally ineligible, trash, or transfer reconciles previously published peer objects with ordered tombstones. Delivery also checks current eligibility so a stale queued recipe body is not sent after consent is withdrawn. A tombstone records removal at the peer; it cannot erase a copy the peer already retained.

Migration 3 conservatively backfills a legacy recipe with public visibility as locally `published`, while setting federation consent to `off`, index eligibility to `false`, and leaving `published_at` and `updated_by` unset. Non-public legacy recipes become `unpublished`. A legacy soft-deleted public row may retain `published` in its stored policy, but the deleted-row gate still makes it ineligible for local public reads or federation egress; restore advances that policy to a non-consenting state. Legacy public visibility never becomes federation consent by implication.

Index submission/removal is not implemented. ActivityPub actors, open follows, public actor documents, and public actor routes are not implemented. Optional Person-actor identity persistence exists, but public actor publication must wait for actor-aware backup/recovery verification. The static frontend remains the default deployment; an optional public renderer and public recipe detail endpoint remain future work.
