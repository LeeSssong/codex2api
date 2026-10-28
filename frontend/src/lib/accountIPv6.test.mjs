import assert from "node:assert/strict";
import test from "node:test";
import {
  accountEgressPatch,
  importEgressFields,
  importTimezoneFields,
  selectAccountEgress,
} from "./accountIPv6.ts";
import {
  buildProxyBindingContext,
  resolveAccountProxyBinding,
} from "./accountProxyBinding.ts";

test("local selection stores membership and a real empty proxy URL", () => {
  assert.deepEqual(selectAccountEgress(true, "http://old:8080"), {
    ipv6_egress_enabled: true,
    proxy_url: "",
  });
});
test("real proxies and explicit blank disable membership without dropping blank", () => {
  assert.deepEqual(selectAccountEgress(false, " http://real:8080 "), {
    ipv6_egress_enabled: false,
    proxy_url: "http://real:8080",
  });
  assert.deepEqual(selectAccountEgress(false, ""), {
    ipv6_egress_enabled: false,
    proxy_url: "",
  });
});
test("unrelated account edits omit egress while intentional transitions preserve explicit blank", () => {
  assert.deepEqual(
    accountEgressPatch({ proxy_url: "", ipv6_egress_enabled: true }, true, ""),
    {},
  );
  assert.deepEqual(
    accountEgressPatch({ proxy_url: "", ipv6_egress_enabled: true }, false, ""),
    { ipv6_egress_enabled: false, proxy_url: "" },
  );
  assert.deepEqual(
    accountEgressPatch(
      { proxy_url: "http://real:8080" },
      false,
      "http://real:8080",
    ),
    {},
  );
  assert.deepEqual(accountEgressPatch({ proxy_url: "" }, true, ""), {
    ipv6_egress_enabled: true,
    proxy_url: "",
  });
});
test("default import leaves source preferences to backend; explicit selection overrides including blank", () => {
  assert.deepEqual(importEgressFields(false, true, ""), {});
  assert.deepEqual(importEgressFields(true, true, ""), {
    ipv6_egress_enabled: "true",
    proxy_url: "",
  });
  assert.deepEqual(importEgressFields(true, false, ""), {
    ipv6_egress_enabled: "false",
    proxy_url: "",
  });
  assert.deepEqual(importEgressFields(true, false, "http://real:8080"), {
    ipv6_egress_enabled: "false",
    proxy_url: "http://real:8080",
  });
});
test("New York checkbox sends an explicit default or an explicit empty default", () => {
  assert.deepEqual(importTimezoneFields(true), {
    default_timezone: "America/New_York",
  });
  assert.deepEqual(importTimezoneFields(false), { default_timezone: "" });
});
test("IPv6 badges do not invent a proxy or fall back to global/pool when pending", () => {
  const ctx = buildProxyBindingContext({
    poolEnabled: true,
    globalProxy: "http://global:8080",
  });
  assert.equal(
    resolveAccountProxyBinding(
      { ipv6_egress_enabled: true, ipv6_egress_ip: "" },
      ctx,
    ).kind,
    "local_ipv6",
  );
  assert.equal(
    resolveAccountProxyBinding(
      {
        ipv6_egress_enabled: true,
        ipv6_egress_ip: "2a0a:4cc0:101:799:0:0:0:1",
      },
      ctx,
    ).url,
    "",
  );
});

test("native explicit IPv4 opt-out badge bypasses pool and global without changing other channels", () => {
  const ctx = buildProxyBindingContext({
    nativeCodex: true,
    poolEnabled: true,
    globalProxy: "http://global:8080",
  });
  assert.equal(
    resolveAccountProxyBinding(
      { ipv6_egress_enabled: false, proxy_url: "" },
      ctx,
    ).kind,
    "direct",
  );
  assert.equal(
    resolveAccountProxyBinding(
      { openai_responses_api: true, ipv6_egress_enabled: false, proxy_url: "" },
      ctx,
    ).kind,
    "global",
  );
  const otherChannel = buildProxyBindingContext({
    poolEnabled: true,
    globalProxy: "http://global:8080",
  });
  assert.equal(
    resolveAccountProxyBinding(
      { ipv6_egress_enabled: false, proxy_url: "" },
      otherChannel,
    ).kind,
    "global",
  );
});
