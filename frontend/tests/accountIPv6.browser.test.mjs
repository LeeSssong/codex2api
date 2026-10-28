import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { build } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { chromium } from "playwright";
const root = fileURLToPath(new URL("../", import.meta.url));
const fullIP = "2a0a:4cc0:0101:0799:abcd:1234:5678:9001";
let browser, script, css, artifacts, realAPIScript;
before(async () => {
  artifacts = await mkdtemp(path.join(tmpdir(), "codex2api-account-ipv6-"));
  const result = await build({
    configFile: false,
    root,
    logLevel: "silent",
    plugins: [
      react(),
      tailwindcss(),
      {
        name: "account-ipv6-fixture",
        enforce: "pre",
        resolveId(id) {
          if (id === "virtual:account-ipv6") return "\0account-ipv6.tsx";
          if (/^(?:\.\.\/|\.\/)+api$/.test(id)) return "\0mock-api";
        },
        load(id) {
          if (id === "\0mock-api")
            return 'export const api=new Proxy({},{get:(_,name)=>(...args)=>window.__api(name,args)}); export const getAdminKey=()=>"fixture"; export const resetAdminAuthState=()=>{}; export const request=()=>Promise.resolve({}); export class AdminAPIError extends Error {}';
          if (id !== "\0account-ipv6.tsx") return;
          return `import React from 'react';import {createRoot} from 'react-dom/client';import {MemoryRouter} from 'react-router-dom';
    import Accounts from ${JSON.stringify(path.join(root, "src/pages/Accounts.tsx"))};
    import Quick from ${JSON.stringify(path.join(root, "src/components/AccountProxyQuickEditor.tsx"))};
    import Panel from ${JSON.stringify(path.join(root, "src/components/IPv6EgressPanel.tsx"))};
    import TestConnectionModal from ${JSON.stringify(path.join(root, "src/components/TestConnectionModal.tsx"))};
    import {VisibleChannelsProvider} from ${JSON.stringify(path.join(root, "src/visibleChannels.tsx"))};
    import {ToastProvider} from ${JSON.stringify(path.join(root, "src/components/ToastProvider.tsx"))};
    import {buildProxyBindingContext} from ${JSON.stringify(path.join(root, "src/lib/accountProxyBinding.ts"))};
    import ${JSON.stringify(path.join(root, "src/i18n.ts"))};import ${JSON.stringify(path.join(root, "src/index.css"))};
    function Fixture(){const [testing,setTesting]=React.useState(false);const [account,setAccount]=React.useState(window.__account);const binding=(ip)=>setAccount(current=>({...current,ipv6_egress_ip:ip}));return window.__mode==='accounts'?<VisibleChannelsProvider><Accounts/></VisibleChannelsProvider>:window.__mode==='panel'?<Panel/>:<><Quick account={account} accountLabel="fixture@example.test" proxies={window.__proxies} ctx={buildProxyBindingContext({proxies:window.__proxies})} allowLocalIPv6={window.__native} onClose={()=>{window.__closed=true}} onSaved={()=>{window.__saved=true}} onTestLocalIPv6={()=>setTesting(true)} onAssignIPv6={async(ip,expectedIP)=>{const result=await window.__api("setAccountIPv6Egress",[account.id,{ip,expected_ip:expectedIP}]);binding(result.ip);return result.ip}} onRefreshIPv6={binding}/>{testing&&<TestConnectionModal account={window.__account} onClose={()=>setTesting(false)} onSettled={()=>{}}/>}</>}
    createRoot(document.getElementById('root'),{onUncaughtError:(error)=>{window.__renderError=error.stack;console.error(error.stack)}}).render(<MemoryRouter><ToastProvider><Fixture/></ToastProvider></MemoryRouter>);`;
        },
      },
    ],
    resolve: { alias: { "@": path.join(root, "src") } },
    define: { "process.env.NODE_ENV": '"production"' },
    build: {
      write: false,
      minify: false,
      rollupOptions: { input: "virtual:account-ipv6" },
      lib: {
        entry: "virtual:account-ipv6",
        formats: ["iife"],
        name: "AccountIPv6Fixture",
      },
    },
  });
  const bundle = Array.isArray(result) ? result[0] : result;
  script = bundle.output.find((item) => item.type === "chunk").code;
  css = bundle.output
    .filter((item) => item.type === "asset" && item.fileName.endsWith(".css"))
    .map((item) => item.source)
    .join("\n");
  const apiBundle = await build({
    configFile: false, root, logLevel: 'silent',
    resolve: { alias: { '@': path.join(root, 'src') } },
    build: { write: false, minify: false, lib: { entry: path.join(root, 'src/api.ts'), formats: ['iife'], name: 'ManualContractAPI' } },
  });
  realAPIScript = (Array.isArray(apiBundle) ? apiBundle[0] : apiBundle).output.find(item => item.type === 'chunk').code;
  browser = await chromium.launch({ headless: true });
});
after(async () => {
  await browser?.close();
  console.log(`Browser artifacts: ${artifacts}`);
});
async function fixture(t, options = {}) {
  const page = await browser.newPage({
    viewport: options.mobile
      ? { width: 390, height: 844 }
      : { width: 1360, height: 1000 },
  });
  page.setDefaultTimeout(6000);
  t.after(async () => {
    if (t.passed === false)
      console.log(
        "FAILURE DOM",
        (await page.locator("body").innerHTML()).slice(0, 5000),
        await page.evaluate(() => ({
          calls: window.__calls,
          error: window.__renderError,
        })),
      );
    await page.close();
  });
  page.on("console", (msg) => {
    if (msg.type() === "error") errors.push(msg.text());
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  t.after(() => assert.deepEqual(errors, []));
  await page.route("http://ipv6.test/**", async (route) => {
    const url = route.request().url();
    if (url.includes("/accounts/") && url.includes("/test?"))
      return route.fulfill({
        contentType: "text/event-stream",
        body: 'data: {"type":"error","error":"fixture test completed"}\n\n',
      });
    if (url.includes("/accounts/import"))
      return route.fulfill({
        contentType: "application/json",
        body: '{"success":1,"failed":0,"total":1}',
      });
    return route.fulfill({
      contentType: "text/html",
      body: '<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="root"></div></body></html>',
    });
  });
  await page.goto("http://ipv6.test/");
  await page.evaluate(
    ({ options, fullIP }) => {
      localStorage.setItem("lang", options.lang || "en");
      localStorage.setItem("codex2api:accounts:page-mode", "pool");
      localStorage.setItem("codex2api:accounts:analysis-visible", "false");
      window.__mode = options.mode || "quick";
      window.__native = options.native !== false;
      window.__calls = [];
      window.__manualConflicts = options.manualConflict ? 1 : 0;
      window.__egress = options.egress || null;
      window.__account = {
        id: 1,
        name: "fixture@example.test",
        email: "fixture@example.test",
        account_type: "oauth",
        status: "active",
        enabled: true,
        plan_type: "plus",
        health_tier: "healthy",
        created_at: "2026-10-29T00:00:00Z",
        group_ids: [],
        models: ["gpt-5.5"],
        detail_loaded: true,
        proxy_url: "",
        ipv6_egress_enabled: true,
        ipv6_egress_ip: fullIP,
        ...options.account,
      };
      window.__proxies = [
        {
          id: 9,
          url: "http://proxy.example:8080",
          enabled: true,
          label: "Real proxy",
          bound_count: 0,
        },
      ];
      window.__api = async (name, args) => {
        window.__calls.push({ name, args });
        if (name === "getVisibleChannels")
          return { channels: ["codex", "claude", "grok"] };
        if (name === "getAccountsPage")
          return {
            accounts: [{...window.__account}],
            total: 1,
            page: 1,
            page_size: 20,
            facets: { tags: [], email_domains: [] },
            summary: { total: 1 },
          };
        if (name === "getAccount") return {...window.__account};
        if (name === "generateOAuthURL") return {session_id:"fixture-session",auth_url:"https://auth.example.test/login"};
        if (name === "getAccountHealthBars") return { buckets: {} };
        if (name === "getIntelligenceBars") return { bars: {} };
        if (name === "getAccountPageStats") return { stats: {} };
        if (name === "getAccountLiveState") return { accounts: {} };
        if (name === "getAPIKeys") return { keys: [] };
        if (name === "listProxies" || name === "getProxies")
          return { proxies: window.__proxies };
        if (name === "getModels") return { models: ["gpt-5.5"] };
        if (name === "listAccountGroups") return { groups: [] };
        if (name === "getChannelMonitorBillingRates") return { rates: [] };
        if (name === "setAccountIPv6Egress") {
          if (window.__manualConflicts > 0) {
            window.__manualConflicts--;
            if (options.manualConflict === 'cooling') {
              window.__egress.cooldowns.push({ip:args[1].ip,until:Math.floor(Date.now()/1000)+600});
            } else {
              window.__account.ipv6_egress_ip = "2a0a:4cc0:0101:0799:abcd:1234:5678:9005";
              window.__egress.bindings = window.__egress.bindings.filter(row=>row.account_id!==1);
              window.__egress.bindings.push({account_id:1,ip:window.__account.ipv6_egress_ip});
            }
            const error = new Error("fixture stale binding"); error.status = 409; throw error;
          }
          window.__account.ipv6_egress_ip = options.actualIP || args[1].ip;
          window.__egress.bindings = window.__egress.bindings.filter(row=>row.account_id!==1);
          window.__egress.bindings.push({account_id:1,ip:window.__account.ipv6_egress_ip});
          return {ip: window.__account.ipv6_egress_ip};
        }
        if (name === "getIPv6Egress")
          return window.__egress || {
            config: {
              enabled: false,
              source_ips: [],
              cooldown_seconds: 600,
              max_attempts: 2,
              retry_5xx: true,
            },
            local_ips: [],
            bindings: [],
            cooldowns: [],
            account_names: {},
          };
        if (name === "testProxy")
          return { success: true, ip: "203.0.113.9", latency_ms: 8 };
        if (name === "updateAccountScheduler" && options.saveError)
          throw new Error("fixture save rejected");
        return {};
      };
    },
    { options, fullIP },
  );
  await page.addStyleTag({ content: css });
  await page.addScriptTag({ content: script });
  return page;
}
const calls = (page, name) =>
  page.evaluate(
    (name) => window.__calls.filter((call) => call.name === name),
    name,
  );
async function choose(page, name) {
  await page
    .getByRole("button", {
      name: /Local IPV6|select.*pool|Real proxy|从代理池选择|本机 IPV6/i,
    })
    .last()
    .click();
  await page.getByRole("option", { name }).click();
}
test("full actual address is read-only and clear sends explicit blank plus membership false", async (t) => {
  const page = await fixture(t, { mobile: true });
  const address = page.getByRole("textbox", {
    name: "Actual bound IPv6 address",
  });
  assert.equal(await address.inputValue(), fullIP);
  assert.equal(await address.getAttribute("readonly"), "");
  await page.screenshot({
    path: path.join(artifacts, "quick-ipv6-mobile.png"),
  });
  await page.getByRole("button", { name: "Clear proxy", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  assert.deepEqual((await calls(page, "updateAccountScheduler"))[0].args, [
    1,
    { ipv6_egress_enabled: false, proxy_url: "" },
  ]);
});
test("real proxy disables membership and only tests a real URL", async (t) => {
  const page = await fixture(t);
  await choose(page, /Real proxy/);
  await page.getByRole("button", { name: "Test proxy", exact: true }).click();
  assert.equal(
    (await calls(page, "testProxy"))[0].args[0],
    "http://proxy.example:8080",
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  assert.deepEqual((await calls(page, "updateAccountScheduler"))[0].args[1], {
    ipv6_egress_enabled: false,
    proxy_url: "http://proxy.example:8080",
  });
});
test("new local selection stays pending and cannot test unsaved settings", async (t) => {
  const page = await fixture(t, {
    account: {
      ipv6_egress_enabled: false,
      ipv6_egress_ip: "",
      proxy_url: "http://proxy.example:8080",
    },
  });
  await choose(page, "Local IPV6");
  assert.equal(
    await page
      .getByRole("textbox", { name: "Actual bound IPv6 address" })
      .inputValue(),
    "Pending assignment",
  );
  assert.equal(
    await page
      .getByRole("button", { name: "Test current egress" })
      .isDisabled(),
    true,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  assert.deepEqual((await calls(page, "updateAccountScheduler"))[0].args[1], {
    ipv6_egress_enabled: true,
    proxy_url: "",
  });
  assert.equal((await calls(page, "testProxy")).length, 0);
});
test("failed save retains draft without reporting success", async (t) => {
  const page = await fixture(t, { saveError: true });
  await page.getByRole("button", { name: "Clear proxy", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByText(/fixture save rejected/).waitFor();
  assert.equal(
    await page.evaluate(() => Boolean(window.__saved || window.__closed)),
    false,
  );
});
test("other channels retain legacy proxy-only payload", async (t) => {
  const page = await fixture(t, {
    native: false,
    account: {
      ipv6_egress_enabled: false,
      proxy_url: "http://proxy.example:8080",
    },
  });
  assert.equal(
    await page.getByRole("button", { name: "Test current egress" }).count(),
    0,
  );
  await page.getByRole("textbox").fill("http://other.example:8080");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  assert.deepEqual((await calls(page, "updateAccountScheduler"))[0].args[1], {
    proxy_url: "http://other.example:8080",
  });
});
test("saved local selection opens real account test endpoint without testing IP as proxy", async (t) => {
  const page = await fixture(t);
  await page.getByRole("button", { name: "Test current egress" }).click();
  const button = page.getByRole("button", { name: /Start Test|开始测试/i });
  await button.waitFor();
  const request = page.waitForRequest((req) =>
    req.url().includes("/api/admin/accounts/1/test?"),
  );
  await button.click();
  await request;
  assert.equal((await calls(page, "testProxy")).length, 0);
});
test("pool explanation scopes membership and states fail-closed behavior", async (t) => {
  const page = await fixture(t, { mode: "panel" });
  await page
    .getByText(/Enabling the pool does not enroll existing accounts/)
    .waitFor();
  await page.getByText(/fail closed when the pool is disabled/).waitFor();
});
test("checked import timezone sends New York, omits untouched enrollment, preserves source", async (t) => {
  const page = await fixture(t, { mode: "accounts", lang: "zh" });
  await page.getByRole("button", { name: "导入", exact: true }).click();
  assert.equal(
    await page.getByRole("checkbox", { name: /缺省时区使用纽约/ }).isChecked(),
    true,
  );
  await page.screenshot({ path: path.join(artifacts, "import-defaults.png") });
  const content =
    '{"access_token":"fixture","timezone":"Asia/Tokyo","proxy_url":""}';
  const request = page.waitForRequest((req) =>
    req.url().includes("/api/admin/accounts/import"),
  );
  await page
    .locator('input[type=file][accept=".json"]')
    .first()
    .setInputFiles({
      name: "fixture.json",
      mimeType: "application/json",
      buffer: Buffer.from(content),
    });
  const body = (await request).postData();
  assert.match(body, /name="default_timezone"\r\n\r\nAmerica\/New_York/);
  assert.equal(body.includes('name="ipv6_egress_enabled"'), false);
  assert.equal(body.includes('name="proxy_url"\r\n'), false);
  assert.ok(body.includes(content));
});
test("unchecked timezone and explicit blank import are not dropped", async (t) => {
  const page = await fixture(t, { mode: "accounts", lang: "zh" });
  await page.getByRole("button", { name: "导入", exact: true }).click();
  await page.getByRole("checkbox", { name: /缺省时区使用纽约/ }).uncheck();
  await page.getByRole("button", { name: "清空代理", exact: true }).click();
  const request = page.waitForRequest((req) =>
    req.url().includes("/api/admin/accounts/import"),
  );
  await page
    .locator('input[type=file][accept=".json"]')
    .first()
    .setInputFiles({
      name: "fixture.json",
      mimeType: "application/json",
      buffer: Buffer.from('{"access_token":"fixture"}'),
    });
  const body = (await request).postData();
  assert.match(body, /name="default_timezone"\r\n\r\n\r\n/);
  assert.match(body, /name="ipv6_egress_enabled"\r\n\r\nfalse/);
  assert.match(body, /name="proxy_url"\r\n\r\n\r\n/);
});

test("main edit preserves existing egress on unrelated save", async (t) => {
  const page = await fixture(t, { mode: "accounts" });
  await page.getByTitle("Edit Settings", { exact: true }).first().click();
  assert.equal(
    await page
      .getByRole("textbox", { name: "Actual bound IPv6 address" })
      .inputValue(),
    fullIP,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const payload = (await calls(page, "updateAccountScheduler"))[0].args[1];
  assert.equal(Object.hasOwn(payload, "proxy_url"), false);
  assert.equal(Object.hasOwn(payload, "ipv6_egress_enabled"), false);
});
test("main edit clearing and saving leaves the IPv6 pool with explicit blank", async (t) => {
  const page = await fixture(t, { mode: "accounts" });
  await page.getByTitle("Edit Settings", { exact: true }).first().click();
  await page.getByRole("button", { name: "Clear proxy", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  const payload = (await calls(page, "updateAccountScheduler"))[0].args[1];
  assert.equal(payload.proxy_url, "");
  assert.equal(payload.ipv6_egress_enabled, false);
});
test("native add defaults to IPv6 and sends membership independently of proxy URL", async (t) => {
  const page = await fixture(t, { mode: "accounts" });
  await page.getByRole("button", { name: "Add Account", exact: true }).click();
  await page
    .getByRole("button", { name: "Refresh Token", exact: true })
    .click();
  assert.equal(
    await page
      .getByRole("textbox", { name: "Actual bound IPv6 address" })
      .inputValue(),
    "Pending assignment",
  );
  await page
    .getByPlaceholder("One refresh token per line. Bulk paste is supported.")
    .fill("fixture-refresh-token");
  await page.getByRole("button", { name: "Add", exact: true }).click();
  const payload = (await calls(page, "addAccount"))[0].args[0];
  assert.equal(payload.proxy_url, "");
  assert.equal(payload.ipv6_egress_enabled, true);
});
test("Responses add has no native IPv6 enrollment control", async (t) => {
  const page = await fixture(t, { mode: "accounts" });
  await page.getByRole("button", { name: "Add Account", exact: true }).click();
  await page.getByRole("button", { name: "API Key", exact: true }).click();
  assert.equal(
    await page
      .getByRole("textbox", { name: "Actual bound IPv6 address" })
      .count(),
    0,
  );
  assert.equal(
    await page.getByRole("button", { name: "Local IPV6", exact: true }).count(),
    0,
  );
});

test("reauthorization explicit clearing sends opt-out while untouched preserves route", async (t) => {
  const page = await fixture(t, { mode: "accounts" });
  await page.getByTitle("Edit Settings", {exact:true}).first().click();
  await page.getByRole("button", {name:"Account",exact:true}).click();
  assert.equal(await page.getByRole("textbox",{name:"Actual bound IPv6 address"}).inputValue(), fullIP);
  await page.getByRole("button",{name:"Clear proxy",exact:true}).click();
  await page.getByRole("button",{name:"Generate Auth Link",exact:true}).click();
  const generated = (await calls(page,"generateOAuthURL"))[0].args[0];
  assert.equal(generated.ipv6_egress_enabled,false);
  assert.equal(generated.proxy_url,"");
  await page.getByPlaceholder("http://localhost:1455/auth/callback?code=...&state=...").fill("http://localhost:1455/auth/callback?code=fixture&state=fixture");
  await page.getByRole("button",{name:"Update Authorization Parameters",exact:true}).click();
  await page.getByTitle("Edit Settings",{exact:true}).first().waitFor();
  const updated = (await calls(page,"updateOAuthAccount"))[0].args[1];
  assert.equal(updated.ipv6_egress_enabled,false);
  assert.equal(updated.proxy_url,"");
});
test("reauthorization without route edits omits durable selection override", async (t) => {
  const page = await fixture(t, { mode: "accounts" });
  await page.getByTitle("Edit Settings", {exact:true}).first().click();
  await page.getByRole("button", {name:"Account",exact:true}).click();
  await page.getByRole("button",{name:"Generate Auth Link",exact:true}).click();
  await page.getByPlaceholder("http://localhost:1455/auth/callback?code=...&state=...").fill("http://localhost:1455/auth/callback?code=fixture&state=fixture");
  await page.getByRole("button",{name:"Update Authorization Parameters",exact:true}).click();
  const updated = (await calls(page,"updateOAuthAccount"))[0].args[1];
  assert.equal(Object.hasOwn(updated,"ipv6_egress_enabled"),false);
  assert.equal(Object.hasOwn(updated,"proxy_url"),false);
});
test("successful Agent Identity import resets touched egress for the next import", async (t) => {
  const page = await fixture(t, { mode: "accounts" });
  async function open(){
    await page.getByRole("button",{name:"Add Account",exact:true}).click();
    await page.getByRole("button",{name:"Agent Identity",exact:true}).click();
    await page.getByPlaceholder(/Paste the full auth.json/).fill('{"auth_mode":"agentIdentity"}');
  }
  await open();
  await page.getByRole("button",{name:"Clear proxy",exact:true}).click();
  await page.getByRole("button",{name:"Import & create account",exact:true}).click();
  await open();
  await page.getByRole("button",{name:"Import & create account",exact:true}).click();
  const imported=(await calls(page,"importCodexAgentIdentity"))[1].args[0];
  assert.equal(Object.hasOwn(imported,"ipv6_egress_enabled"),false);
  assert.equal(Object.hasOwn(imported,"proxy_url"),false);
});

const freeIP = '2a0a:4cc0:0101:0799:abcd:1234:5678:9002';
const busyIP = '2a0a:4cc0:0101:0799:abcd:1234:5678:9003';
const coolingIP = '2a0a:4cc0:0101:0799:abcd:1234:5678:9004';
const staleIP = '2a0a:4cc0:0101:0799:abcd:1234:5678:9005';
const manualPool = (extra = {}) => ({
  config: {enabled:true,source_ips:[],cooldown_seconds:600,max_attempts:2,retry_5xx:true,revision:1},
  local_ips: [fullIP,freeIP,busyIP,coolingIP,staleIP],
  bindings: [{account_id:1,ip:fullIP},{account_id:2,ip:busyIP}],
  cooldowns: [{ip:coolingIP,until: Math.floor(Date.now()/1000)+600}],
  account_names:{2:'private-owner@example.test'},
  ...extra,
});
async function openManual(page) {
  await page.getByRole('button',{name:'Choose IP manually',exact:true}).click();
  await page.getByRole('radio',{name:freeIP,exact:true}).waitFor();
}

test('manual main edit excludes occupied/cooling addresses and updates only the binding', async t => {
  const page = await fixture(t,{mode:'accounts',egress:manualPool()});
  await page.getByTitle('Edit Settings',{exact:true}).first().click();
  await openManual(page);
  assert.equal(await page.getByRole('radio',{name:busyIP,exact:true}).isDisabled(),true);
  assert.equal(await page.getByRole('radio',{name:coolingIP,exact:true}).isDisabled(),true);
  assert.equal(await page.getByText('private-owner@example.test').count(),0);
  await page.getByRole('radio',{name:freeIP,exact:true}).check();
  await page.getByRole('button',{name:'Switch IP',exact:true}).click();
  await page.getByText('IPv6 address updated.',{exact:true}).waitFor();
  assert.equal(await page.getByRole('textbox',{name:'Actual bound IPv6 address'}).inputValue(),freeIP);
  assert.deepEqual((await calls(page,'setAccountIPv6Egress'))[0].args,[1,{ip:freeIP,expected_ip:fullIP}]);
  assert.equal((await calls(page,'updateAccountScheduler')).length,0);
  await page.getByRole('button',{name:'Cancel',exact:true}).click();
  // The Accounts row retains the actual binding when opening the quick editor next.
  await page.getByRole('button',{name:'Local IPV6',exact:true}).first().click();
  assert.equal(await page.getByRole('textbox',{name:'Actual bound IPv6 address'}).inputValue(),freeIP);
});

test('manual pending quick edit sends explicit empty expected IP and uses returned actual address', async t => {
  const page=await fixture(t,{account:{ipv6_egress_ip:''},egress:manualPool({bindings:[{account_id:2,ip:busyIP}]}),actualIP:staleIP});
  await openManual(page);
  await page.getByRole('radio',{name:freeIP,exact:true}).check();
  await page.getByRole('button',{name:'Switch IP',exact:true}).click();
  await page.getByText('IPv6 address updated.',{exact:true}).waitFor();
  assert.deepEqual((await calls(page,'setAccountIPv6Egress'))[0].args,[1,{ip:freeIP,expected_ip:''}]);
  assert.equal(await page.getByRole('textbox',{name:'Actual bound IPv6 address'}).inputValue(),staleIP);
  assert.equal((await calls(page,'testProxy')).length,0);
});

test('manual entry is hidden for unsaved enrollment or dirty routes in both editors', async t => {
  const pending=await fixture(t,{account:{ipv6_egress_enabled:false},egress:manualPool()});
  await choose(pending, 'Local IPV6');
  assert.equal(await pending.getByRole('button',{name:'Choose IP manually',exact:true}).count(),0);
  for (const mode of ['quick','accounts']) {
    const page=await fixture(t,{mode,egress:manualPool()});
    if(mode==='accounts') await page.getByTitle('Edit Settings',{exact:true}).first().click();
    assert.equal(await page.getByRole('button',{name:'Choose IP manually',exact:true}).count(),1);
    await page.getByRole('button',{name:'Clear proxy',exact:true}).click();
    assert.equal(await page.getByRole('button',{name:'Choose IP manually',exact:true}).count(),0);
  }
});

test('manual disabled pool and discovery failure disable assignment with explanation', async t => {
  for(const [extra,reason] of [
    [{config:{...manualPool().config,enabled:false}},'The IPv6 pool is disabled. Manual selection is unavailable.'],
    [{discovery_error:true},'Local IPv6 discovery failed. Refresh before selecting an address.'],
  ]) {
    const page=await fixture(t,{egress:manualPool(extra)});
    await openManual(page);
    await page.getByText(reason,{exact:true}).waitFor();
    assert.equal(await page.getByRole('radio',{name:freeIP,exact:true}).isDisabled(),true);
    assert.equal(await page.getByRole('button',{name:'Switch IP',exact:true}).isDisabled(),true);
    assert.equal((await calls(page,'setAccountIPv6Egress')).length,0);
  }
});

test('manual stale 409 refreshes expected binding, retains selection and permits retry', async t => {
  const page=await fixture(t,{mode:'accounts',egress:manualPool(),manualConflict:true});
  await page.getByTitle('Edit Settings',{exact:true}).first().click();
  await page.getByRole('button',{name:'Not bound (pass client timezone through)',exact:true}).click();
  await page.getByRole('option',{name:/America\/New_York/}).click();
  await openManual(page);
  await page.getByRole('radio',{name:freeIP,exact:true}).check();
  await page.getByRole('button',{name:'Switch IP',exact:true}).click();
  await page.getByText('The address is occupied, cooling down, or the binding changed. Pool status refreshed; choose an available address and retry.',{exact:true}).first().waitFor();
  await page.waitForFunction(ip=>document.querySelector('textarea[aria-label="Actual bound IPv6 address"]')?.value===ip,staleIP);
  assert.equal(await page.getByRole('radio',{name:freeIP,exact:true}).isChecked(),true);
  await page.getByRole('button',{name:'Refresh IP pool',exact:true}).click();
  await page.getByRole('button',{name:'Switch IP',exact:true}).click();
  await page.getByText('IPv6 address updated.',{exact:true}).waitFor();
  assert.deepEqual((await calls(page,'setAccountIPv6Egress'))[1].args,[1,{ip:freeIP,expected_ip:staleIP}]);
  await page.getByRole('button',{name:'Save',exact:true}).click();
  const saved = (await calls(page,'updateAccountScheduler'))[0].args[1];
  assert.equal(saved.timezone,'America/New_York');
  assert.equal(Object.hasOwn(saved,'proxy_url'),false);
  assert.equal(Object.hasOwn(saved,'ipv6_egress_enabled'),false);
});

test('manual mobile Chinese picker keeps full addresses scrollable without owner names', async t => {
  const page=await fixture(t,{mobile:true,lang:'zh',egress:manualPool()});
  await page.getByRole('button',{name:'手工选择IP',exact:true}).click();
  await page.getByRole('radio',{name:freeIP,exact:true}).waitFor();
  assert.equal(await page.getByRole('textbox',{name:'实际绑定的 IPv6 地址'}).inputValue(),fullIP);
  assert.equal(await page.getByText(freeIP,{exact:true}).count(),1);
  assert.equal(await page.getByText(freeIP,{exact:true}).evaluate(node=>getComputedStyle(node).overflowX), 'auto');
  await page.getByRole('radio',{name:freeIP,exact:true}).check();
  assert.equal(await page.getByText('private-owner@example.test').count(),0);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  await page.screenshot({path:path.join(artifacts,'manual-ipv6-mobile-zh.png'),fullPage:true});
});

test('manual real API serializes POST ip and explicit expected_ip and exposes 409 status', async t => {
  const page=await browser.newPage();
  t.after(()=>page.close());
  const requests=[];
  await page.route('http://manual.test/**',route=> {
    const request=route.request();
    if(new URL(request.url()).pathname==='/api/admin/accounts/1/ipv6-egress') {
      requests.push({method:request.method(),body:request.postDataJSON(),contentType:request.headers()['content-type']});
      return route.fulfill({status:requests.length===1?200:409,contentType:'application/json',body:requests.length===1?JSON.stringify({ip:freeIP}):JSON.stringify({error:'stale binding'})});
    }
    return route.fulfill({contentType:'text/html',body:'<!doctype html><html><body></body></html>'});
  });
  await page.goto('http://manual.test/');
  await page.addScriptTag({content:realAPIScript});
  const success=await page.evaluate(ip=>window.ManualContractAPI.api.setAccountIPv6Egress(1,{ip,expected_ip:''}),freeIP);
  assert.deepEqual(success,{ip:freeIP});
  const failure=await page.evaluate(async ip=>{
    try { await window.ManualContractAPI.api.setAccountIPv6Egress(1,{ip,expected_ip:ip}); return null }
    catch(error) { return {status:error.status,message:error.message} }
  },freeIP);
  assert.deepEqual(failure,{status:409,message:'stale binding'});
  assert.deepEqual(requests,[
    {method:'POST',body:{ip:freeIP,expected_ip:''},contentType:'application/json'},
    {method:'POST',body:{ip:freeIP,expected_ip:freeIP},contentType:'application/json'},
  ]);
});

test('manual cooling 409 retains selection disabled until refresh makes it available', async t => {
  const page=await fixture(t,{egress:manualPool(),manualConflict:'cooling'});
  await openManual(page);
  const choice=page.getByRole('radio',{name:freeIP,exact:true});
  await choice.check();
  await page.getByRole('button',{name:'Switch IP',exact:true}).click();
  await page.waitForFunction(ip=>document.querySelector(`input[aria-label="${ip}"]`)?.disabled,freeIP);
  assert.equal(await choice.isChecked(),true);
  assert.equal(await page.getByRole('button',{name:'Switch IP',exact:true}).isDisabled(),true);
  await page.evaluate(ip=>{window.__egress.cooldowns=window.__egress.cooldowns.filter(row=>row.ip!==ip)},freeIP);
  await page.getByRole('button',{name:'Refresh IP pool',exact:true}).click();
  await page.getByRole('button',{name:'Switch IP',exact:true}).click();
  await page.getByText('IPv6 address updated.',{exact:true}).waitFor();
  assert.deepEqual((await calls(page,'setAccountIPv6Egress'))[1].args,[1,{ip:freeIP,expected_ip:fullIP}]);
});

test('retired bindings stay pending and lease-only addresses remain unavailable', async t => {
 const page=await fixture(t,{account:{ipv6_egress_ip:''},egress:manualPool({bindings:[{account_id:1,ip:fullIP,reason:'retired'}],cooldowns:[],leased_ips:[fullIP,busyIP]})});
 await openManual(page);
 assert.equal(await page.getByRole('textbox',{name:'Actual bound IPv6 address'}).inputValue(),'Pending assignment');
 assert.equal(await page.getByRole('radio',{name:fullIP,exact:true}).isDisabled(),true);
 assert.equal(await page.getByRole('radio',{name:busyIP,exact:true}).isDisabled(),true);
 await page.getByRole('radio',{name:freeIP,exact:true}).check();
 await page.getByRole('button',{name:'Switch IP',exact:true}).click();
 await page.getByText('IPv6 address updated.',{exact:true}).waitFor();
 assert.deepEqual((await calls(page,'setAccountIPv6Egress'))[0].args,[1,{ip:freeIP,expected_ip:''}]);
});
