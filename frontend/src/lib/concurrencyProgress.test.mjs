import assert from "node:assert/strict";
import test from "node:test";
import { chunkConcurrencyProgressIDs, formatConcurrencyProgress, projectConcurrencyProgress } from "./concurrencyProgress.ts";

test("large account pages split progress requests at the API bound", () => {
  const chunks = chunkConcurrencyProgressIDs(Array.from({ length: 405 }, (_, index) => index + 1));
  assert.deepEqual(chunks.map(chunk => chunk.length), [200, 200, 5]);
  assert.equal(chunks.flat().length, 405);
});

test("concurrency progress only renders an enabled eligible account", () => {
  const state = {
    revision: "r1",
    concurrency: 3,
    successes: 12,
    required: 20,
    maximum: 100,
    step: 1,
    paused_until: "",
  };
  assert.deepEqual(projectConcurrencyProgress(true, false, state), state);
  assert.equal(projectConcurrencyProgress(false, false, state), null);
  assert.equal(projectConcurrencyProgress(true, false, undefined), null);
});

test("a paused runtime hides account progression even when the last batch had state", () => {
  assert.equal(projectConcurrencyProgress(true, true, {
    revision: "r1", concurrency: 3, successes: 12, required: 20,
    maximum: 100, step: 1, paused_until: "",
  }), null);
});

test("concurrency progress formats the current step and next target", () => {
  const display = formatConcurrencyProgress({
    revision: "r1",
    concurrency: 3,
    successes: 12,
    required: 20,
    maximum: 100,
    step: 1,
    paused_until: "",
  });
  assert.deepEqual(display, { successes: 12, required: 20, next: 4, capped: false });
});

test("concurrency progress caps a malformed counter and recognizes the maximum", () => {
  const display = formatConcurrencyProgress({
    revision: "r1",
    concurrency: 100,
    successes: 200,
    required: 20,
    maximum: 100,
    step: 1,
    paused_until: "",
  });
  assert.deepEqual(display, { successes: 20, required: 20, next: 100, capped: true });
});
