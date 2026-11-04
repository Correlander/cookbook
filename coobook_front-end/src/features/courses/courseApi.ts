import type { Course } from "../../apiModels";
import { parseOptionalCreatorAttributions } from "../creator/creatorAttributionParser.ts";

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isNonnegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}

export function parseCourse(value: unknown): Course {
  if (!isRecord(value) || typeof value.id !== "string" || !value.id ||
      typeof value.title !== "string" || typeof value.owner_name !== "string" ||
      typeof value.description !== "string" ||
      (value.visibility !== "private" && value.visibility !== "public") ||
      !isNonnegativeInteger(value.lesson_count) || typeof value.created_at !== "string" ||
      typeof value.updated_at !== "string" ||
      (value.owner_id !== undefined && (typeof value.owner_id !== "string" || !value.owner_id)) ||
      (value.creator_name !== undefined && typeof value.creator_name !== "string")) {
    throw new Error("Could not load this course.");
  }
  return {
    id: value.id,
    title: value.title,
    owner_name: value.owner_name,
    description: value.description,
    visibility: value.visibility,
    lesson_count: value.lesson_count,
    created_at: value.created_at,
    updated_at: value.updated_at,
    ...(typeof value.owner_id === "string" ? { owner_id: value.owner_id } : {}),
    ...(typeof value.creator_name === "string" ? { creator_name: value.creator_name } : {}),
    ...parseOptionalCreatorAttributions(value.creator_attributions, "Could not load this course.")
  };
}

export function parseCourseList(value: unknown): Course[] {
  if (!Array.isArray(value)) throw new Error("Could not load courses.");
  try {
    return value.map(parseCourse);
  } catch {
    throw new Error("Could not load courses.");
  }
}
