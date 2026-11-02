import test from "node:test";
import assert from "node:assert/strict";
import { scaleQuantity } from "./quantity.ts";

test("scales integer, decimal, fraction, and mixed-fraction amounts", () => {
  assert.equal(scaleQuantity("2 cups", 1.5), "3 cups");
  assert.equal(scaleQuantity("0.5 tsp", 2), "1 tsp");
  assert.equal(scaleQuantity("1/2 cup", 2), "1 cup");
  assert.equal(scaleQuantity("1 1/2 cups", 2), "3 cups");
});

test("leaves amounts without a simple numeric prefix and ranges unchanged", () => {
  assert.equal(scaleQuantity("to taste", 2), "to taste");
  assert.equal(scaleQuantity("1–2 cloves", 2), "1–2 cloves");
  assert.equal(scaleQuantity("1 or 2 pinches", 2), "1 or 2 pinches");
});

test("keeps the original quantity for an unchanged or invalid scale", () => {
  assert.equal(scaleQuantity("1 cup", 1), "1 cup");
  assert.equal(scaleQuantity("1 cup", 0), "1 cup");
  assert.equal(scaleQuantity("1 cup", Number.NaN), "1 cup");
});
