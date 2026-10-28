import assert from "node:assert/strict";
import test from "node:test";
import {
  canManuallySelectIPv6,
  ipv6ManualBlockReason,
  ipv6ManualOptions,
} from "./accountIPv6Manual.ts";

const pool = (extra = {}) => ({
  config: {
    enabled: true,
    source_ips: [],
    cooldown_seconds: 600,
    max_attempts: 2,
    retry_5xx: true,
    revision: 1,
  },
  local_ips: ["2604::1", "2604::2", "2604::3", "2604::4"],
  bindings: [
    { account_id: 1, ip: "2604::1" },
    { account_id: 2, ip: "2604::2" },
  ],
  cooldowns: [
    { ip: "2604::3", until: 2000 },
    { ip: "2604::4", until: 999 },
  ],
  account_names: { 2: "must-not-be-present@example.test" },
  ...extra,
});
test("manual assignment requires a saved local route and no unsaved route change", () => {
  const local = { ipv6_egress_enabled: true, proxy_url: "" };
  assert.equal(canManuallySelectIPv6(local, true, ""), true);
  assert.equal(canManuallySelectIPv6(local, false, ""), false);
  assert.equal(canManuallySelectIPv6(local, true, "http://proxy:8080"), false);
  assert.equal(
    canManuallySelectIPv6(
      { ipv6_egress_enabled: false, proxy_url: "" },
      true,
      "",
    ),
    false,
  );
  assert.equal(canManuallySelectIPv6(null, true, ""), false);
});
test("pool choices distinguish current, occupied, cooling and available without exposing owners", () => {
  const rows = ipv6ManualOptions(pool(), 1, "2604::1", 1000);
  assert.deepEqual(rows, [
    { ip: "2604::1", status: "current" },
    { ip: "2604::2", status: "occupied" },
    { ip: "2604::3", status: "cooling" },
    { ip: "2604::4", status: "available" },
  ]);
  assert.equal(JSON.stringify(rows).includes("@"), false);
});
test("only configured local addresses are offered, current stays visible outside pool", () => {
  const data = pool({
    config: { enabled: true, source_ips: ["2604::4", "2604::5"] },
  });
  assert.deepEqual(ipv6ManualOptions(data, 1, "2604::1", 1000), [
    { ip: "2604::1", status: "current" },
    { ip: "2604::4", status: "available" },
  ]);
});
test("equivalent IPv6 spellings cannot bypass occupancy or cooldown exclusions", () => {
  const data = pool({
    local_ips: [
      "2604:0:0:0:0:0:0:2",
      "2604:0:0:0:0:0:0:3",
      "2604::4",
      "2604:0:0:0:0:0:0:4",
    ],
  });
  assert.deepEqual(
    ipv6ManualOptions(data, 8, "", 1000).map((row) => row.status),
    ["occupied", "cooling", "available"],
  );
});
test("pending accounts can select free addresses without pretending they have a binding", () => {
  assert.equal(
    ipv6ManualOptions(pool(), 8, "", 1000).find((row) => row.ip === "2604::4")
      .status,
    "available",
  );
  assert.equal(
    ipv6ManualOptions(pool(), 8, "", 1000).some(
      (row) => row.status === "current",
    ),
    false,
  );
});
test("disabled pool and failed discovery block manual assignment", () => {
  assert.equal(ipv6ManualBlockReason(pool()), "");
  assert.equal(
    ipv6ManualBlockReason(pool({ config: { enabled: false } })),
    "disabled",
  );
  assert.equal(
    ipv6ManualBlockReason(pool({ discovery_error: true })),
    "discovery",
  );
});
