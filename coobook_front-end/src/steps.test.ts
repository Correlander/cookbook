import test from "node:test";
import assert from "node:assert/strict";
import { moveStep } from "./steps.ts";

test("moves a step without mutating the original list", () => {
  const steps = [{ id: "a" }, { id: "b" }, { id: "c" }];
  const reordered = moveStep(steps, 0, 1);

  assert.deepEqual(
    reordered.map((step) => step.id),
    ["b", "a", "c"],
  );
  assert.deepEqual(
    steps.map((step) => step.id),
    ["a", "b", "c"],
  );
});

test("leaves the list unchanged when a move would cross either boundary", () => {
  const steps = [{ id: "a" }, { id: "b" }];
  assert.equal(moveStep(steps, 0, -1), steps);
  assert.equal(moveStep(steps, 1, 1), steps);
});
