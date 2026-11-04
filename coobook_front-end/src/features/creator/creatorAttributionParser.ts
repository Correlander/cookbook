import type { CreatorAttribution, CreatorAttributionRole } from "../../apiModels";

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function parseCreatorAttribution(
  value: unknown,
  message = "Could not load creator credits.",
): CreatorAttribution {
  if (!isRecord(value) ||
      (value.role !== "creator" && value.role !== "contributor" && value.role !== "adapter") ||
      typeof value.provenance !== "string") throw new Error(message);
  const role: CreatorAttributionRole = value.role;
  if (value.subject_kind === "external_claim") {
    if (value.name_claim !== undefined && typeof value.name_claim !== "string") throw new Error(message);
    return { subject_kind: "external_claim", role, provenance: value.provenance,
      ...(typeof value.name_claim === "string" ? { name_claim: value.name_claim } : {}) };
  }
  if (value.subject_kind !== "local" || !isRecord(value.profile) ||
      typeof value.profile.id !== "string" || !value.profile.id.trim() || typeof value.profile.public_name !== "string" ||
      (value.profile.visibility !== "private" && value.profile.visibility !== "instance")) throw new Error(message);
  return { subject_kind: "local", role, provenance: value.provenance,
    profile: { id: value.profile.id, public_name: value.profile.public_name, visibility: value.profile.visibility } };
}

export function parseOptionalCreatorAttributions(
  value: unknown,
  message = "Could not load creator credits.",
): { creator_attributions?: CreatorAttribution[] } {
  if (value === undefined) return {};
  if (!Array.isArray(value)) throw new Error(message);
  return { creator_attributions: value.map((item) => parseCreatorAttribution(item, message)) };
}
