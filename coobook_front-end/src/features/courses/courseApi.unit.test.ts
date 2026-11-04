import { expect, it } from "vitest";
import { parseCourse, parseCourseList } from "./courseApi.ts";

const legacyCourse = {
  id: "course-1", owner_id: "owner-1", owner_name: "Custodian", title: "Bread",
  description: "", visibility: "private", lesson_count: 0, created_at: "then", updated_at: "then",
};

it("accepts legacy course responses without new attribution fields", () => {
  expect(parseCourse(legacyCourse)).toEqual(legacyCourse);
  expect(parseCourseList([legacyCourse])).toEqual([legacyCourse]);
});

it("retains separate creator name and typed credits while preserving owner custody", () => {
  const course = parseCourse({
    ...legacyCourse,
    creator_name: "Claimed teacher",
    creator_attributions: [{ subject_kind: "external_claim", name_claim: "Imported teacher", role: "contributor", provenance: "portable_claim" }],
  });
  expect(course.owner_name).toBe("Custodian");
  expect(course.creator_name).toBe("Claimed teacher");
  expect(course.creator_attributions?.[0].subject_kind).toBe("external_claim");
});

it.each([
  { creator_attributions: null },
  { creator_attributions: [{ subject_kind: "local", profile: { id: "p1", public_name: "Hidden", visibility: "private" }, role: "creator", provenance: 12 }] },
  { creator_attributions: [{ subject_kind: "local", profile: { id: "p1", public_name: "Hidden", visibility: "public" }, role: "creator", provenance: "self_asserted_at_creation" }] },
  { creator_name: 22 },
])("rejects malformed attribution fields instead of casting them", (extra) => {
  expect(() => parseCourse({ ...legacyCourse, ...extra })).toThrow("Could not load this course.");
});
