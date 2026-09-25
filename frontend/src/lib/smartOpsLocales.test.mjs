import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
const langs = ["zh", "en", "zh-TW"];
const resources = langs.map((lang) =>
  JSON.parse(
    readFileSync(
      new URL("../locales/" + lang + ".json", import.meta.url),
      "utf8",
    ),
  ),
);
function flatten(value, prefix = "") {
  return Object.fromEntries(
    Object.entries(value).flatMap(([key, text]) =>
      typeof text === "string"
        ? [[prefix + key, text]]
        : Object.entries(flatten(text, prefix + key + ".")),
    ),
  );
}
test("smart operations translations keep identical keys and interpolation fields in every locale", () => {
  for (const namespace of ["smartOps", "tokenGuard", "managedVersion"]) {
    const sets = resources.map((resource) => flatten(resource[namespace]));
    for (const translated of sets.slice(1)) {
      assert.deepEqual(
        Object.keys(translated).sort(),
        Object.keys(sets[0]).sort(),
      );
      for (const [key, text] of Object.entries(translated)) {
        assert.ok(text.trim(), namespace + "." + key + " must not be empty");
        assert.deepEqual(
          (text.match(/\{\{[^}]+\}\}/g) || []).sort(),
          (sets[0][key].match(/\{\{[^}]+\}\}/g) || []).sort(),
          namespace + "." + key,
        );
      }
    }
  }
});
