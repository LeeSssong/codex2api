import assert from "node:assert/strict";
import test from "node:test";
import {
  parseBpsModels,
  bpsPolicyPatch,
  bpsEffectiveModels,
} from "./basispointsPolicy.ts";
test("empty selected models remains explicit empty and never inherits", () => {
  assert.deepEqual(bpsPolicyPatch("selected", "", false, true), {
    model_scope: "selected",
    models: [],
    auto_disable_on_403: false,
    cache_creation_as_input: true,
  });
  assert.deepEqual(
    bpsEffectiveModels(
      { model_scope: "selected", models: [] },
      { model_scope: "selected", models: ["gpt-5.6-sol"] },
    ),
    [],
  );
});
test("inherit and all remain bounded by global selected catalog", () => {
  const global = {
    model_scope: "selected",
    models: ["gpt-5.6-sol", "gpt-6-astra"],
  };
  assert.deepEqual(
    bpsEffectiveModels({ model_scope: "all", models: [] }, global),
    global.models,
  );
  assert.deepEqual(
    bpsEffectiveModels({ model_scope: "inherit", models: [] }, global),
    global.models,
  );
  assert.deepEqual(
    bpsEffectiveModels(
      { model_scope: "selected", models: ["gpt-6-astra", "unknown"] },
      global,
    ),
    ["gpt-6-astra"],
  );
});
test("models normalize separators and duplicates without invented defaults", () => {
  assert.deepEqual(parseBpsModels(" gpt-6-astra, gpt-5.6-sol\ngpt-6-astra "), [
    "gpt-6-astra",
    "gpt-5.6-sol",
  ]);
  assert.deepEqual(parseBpsModels(""), []);
});
test("a relay-only edit preserves the latest complete persisted settings", async () => {
  const { bpsSettingsUpdate } = await import("./basispointsPolicy.ts");
  const latest = {
    enabled: true,
    model_scope: "selected",
    models: ["gpt-6-astra"],
    image_relay_enabled: false,
    image_relay_public_origin: "https://relay.example.com",
    image_relay_epoch: 9,
  };
  assert.deepEqual(bpsSettingsUpdate(latest, { image_relay_enabled: true }), {
    enabled: true,
    model_scope: "selected",
    models: ["gpt-6-astra"],
    image_relay_enabled: true,
    image_relay_public_origin: "https://relay.example.com",
  });
});
test('model family intersections match valid case-insensitive snapshots in either direction', () => {
 const effective=(account,global)=>bpsEffectiveModels({model_scope:'selected',models:account},{model_scope:'selected',models:global});
 assert.deepEqual(effective(['GPT-6-ASTRA-2026-09-26'],['gpt-6-astra']),['gpt-6-astra-2026-09-26']);
 assert.deepEqual(effective(['gpt-6-astra'],['GPT-6-ASTRA-2026-09-26']),['gpt-6-astra-2026-09-26']);
 assert.deepEqual(effective(['gpt-6-astra-2024-02-29'],['gpt-6-astra']),['gpt-6-astra-2024-02-29']);
 for(const suffix of ['2026-02-29','2026-13-01','2026-09-31','2026-9-26','2026-09-26-extra']) assert.deepEqual(effective(['gpt-6-astra-'+suffix],['gpt-6-astra']),[]);
 assert.deepEqual(effective(['gpt-6-astra-2026-09-26'],['gpt-6-astra-2026-09-25']),[]);
 assert.deepEqual(effective(['GPT-6-ASTRA','gpt-6-astra'],['gpt-6-astra']),['gpt-6-astra']);
});
