import { expect, it } from "vitest";
import { parseCreatorAttribution, parseOptionalCreatorAttributions } from "./creatorAttributionParser.ts";

it("accepts the published local and external attribution projections", () => {
  expect(parseCreatorAttribution({ subject_kind: "local", profile: { id: "p1", public_name: "Baker", visibility: "instance" }, role: "creator", provenance: "self_asserted_at_creation" }).subject_kind).toBe("local");
  expect(parseCreatorAttribution({ subject_kind: "local", profile: { id: "p1", public_name: "", visibility: "private" }, role: "contributor", provenance: "self_asserted_at_creation" })).toMatchObject({ subject_kind: "local", profile: { id: "p1", public_name: "", visibility: "private" }, role: "contributor", provenance: "self_asserted_at_creation" });
  expect(parseCreatorAttribution({ subject_kind: "external_claim", name_claim: "Claimed", role: "adapter", provenance: "portable_claim" }).subject_kind).toBe("external_claim");
  expect(parseOptionalCreatorAttributions(undefined)).toEqual({});
  expect(parseOptionalCreatorAttributions([])).toEqual({ creator_attributions: [] });
});

it.each([
  null,
  { subject_kind: "external_claim", role: "creator", provenance: "x", name_claim: 7 },
  { subject_kind: "local", role: "creator", provenance: "x", profile: { id: "", public_name: "X", visibility: "private" } },
  { subject_kind: "local", role: "creator", provenance: "x", profile: { id: " \t", public_name: "X", visibility: "private" } },
  { subject_kind: "local", role: "creator", provenance: "x", profile: { id: "p1", public_name: "X", visibility: "public" } },
])("rejects malformed present credit data", (credit) => {
  expect(() => parseCreatorAttribution(credit)).toThrow("Could not load creator credits.");
});
