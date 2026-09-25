import "./accountOps.browser.test.mjs";
import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright";

const base = process.env.ACCOUNT_OPS_TEST_URL || "http://127.0.0.1:5197";
const artifacts = process.env.SMART_OPS_ARTIFACT_DIR || "/tmp/smart-ops-ui";
const configFixture = () => ({
  enabled: true,
  group_ids: [2],
  interval_seconds: 600,
  probe_endpoint: "https://probe.example.test/check",
  probe_model: "mock-model",
  probe_headers: { Authorization: "********" },
  probe_timeout_seconds: 30,
  probe_concurrency: 3,
  max_probe_per_cycle: 20,
  auto_relogin: true,
  relogin_endpoint: "https://relogin.example.test/login",
  relogin_headers: { "X-Key": "********" },
  relogin_accounts: [
    {
      account_id: 7,
      email: "account@example.test",
      password: "********",
      mfa_secret: "********",
    },
  ],
  restore_schedulable: false,
  fail_streak_threshold: 2,
  bark_key: "********",
  notify_on_fix: true,
  notify_on_fail: true,
});
async function setup(page, { lang = "en", moduleEnabled = true } = {}) {
  await page.addInitScript(
    (language) => localStorage.setItem("lang", language),
    lang,
  );
  const fixture = {
    config: configFixture(),
    saved: [],
    jobs: [],
    cancelled: [],
    eventsRequests: [],
    conflict: false,
    failSave: false,
    moduleEnabled,
    runtime: {
      running: false,
      last_run: "2026-09-25T09:00:00Z",
      last_message: "",
      stats: {
        probed: 2,
        healthy: 1,
        auth_failed: 1,
        transient: 0,
        repaired: 1,
        state_fixed: 0,
        failed: 0,
        duration_ms: 250,
        started_at: 1,
      },
    },
  };
  const accounts = [
    {
      account_id: 7,
      account_name: "OAuth account",
      account_status: "disabled",
      schedulable: false,
      probe_state: "ok",
      probe_detail: "Credential valid",
      latency_ms: 90,
      fail_streak: 0,
      last_probe_at: "2026-09-25T09:00:00Z",
      last_fix_at: "2026-09-25T09:00:00Z",
      last_fix_action: "relogin",
      last_fix_result:
        "Credentials repaired; manual scheduling restriction retained",
      needs_relogin: false,
      updated_at: "2026-09-25T09:00:00Z",
    },
  ];
  const latest = [
    {
      id: 20,
      account_id: 7,
      account_name: "OAuth account",
      kind: "relogin_ok",
      detail: "Credentials repaired",
      latency_ms: 90,
      created_at: "2026-09-25T09:00:00Z",
    },
  ];
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      u = new URL(req.url()),
      path = u.pathname,
      post = req.method() !== "GET";
    let data = {},
      status = 200;
    if (path.endsWith("/bootstrap-status")) data = { needs_bootstrap: false };
    else if (path.endsWith("/health")) data = { status: "ok" };
    else if (path.endsWith("/api-keys")) data = { keys: [{ id: 1 }] };
    else if (path.endsWith("/branding")) data = { site_name: "Codex2API" };
    else if (path.endsWith("/settings/visible-channels"))
      data = { channels: ["codex"] };
    else if (path.endsWith("/system/update"))
      data = {
        current_version: "v3.0.0",
        latest_version: "v3.0.0",
        has_update: false,
        supported: false,
        mode: "source_image",
        source_repository: "hloolx/codex2api",
        source_revision: "abcdef0123456789",
        upstream_revision: "abcdef0123456789",
        check_status: "unknown",
        unsupported_reason: "Use the reviewed source release workflow",
        runtime_os: "linux",
        runtime_arch: "amd64",
      };
    else if (path.endsWith("/account-ops/module")) {
      if (post) fixture.moduleEnabled = req.postDataJSON().enabled;
      data = { enabled: fixture.moduleEnabled };
    } else if (path.endsWith("/token-guard/status"))
      data = {
        config: fixture.config,
        accounts,
        events: latest,
        runtime: fixture.runtime,
        module_enabled: fixture.moduleEnabled,
      };
    else if (path.endsWith("/token-guard/config")) {
      if (post) {
        fixture.saved.push(req.postDataJSON());
        if (fixture.failSave) {
          status = 400;
          data = { error: "Invalid mapping" };
          fixture.failSave = false;
        } else {
          fixture.config = {
            ...req.postDataJSON(),
            bark_key: "********",
            probe_headers: { Authorization: "********" },
            relogin_headers: { "X-Key": "********" },
            relogin_accounts: req
              .postDataJSON()
              .relogin_accounts.map((row) => ({
                ...row,
                password: "********",
                mfa_secret: "********",
              })),
          };
          data = fixture.config;
        }
      } else data = fixture.config;
    } else if (
      path.endsWith("/token-guard/run") ||
      /token-guard\/accounts\/\d+\/relogin$/.test(path)
    ) {
      if (fixture.conflict) {
        fixture.conflict = false;
        status = 409;
        data = { error: "active job" };
      } else {
        const id = "job-" + (fixture.jobs.length + 1);
        fixture.jobs.push(path);
        fixture.runtime = {
          ...fixture.runtime,
          running: true,
          job_id: id,
          job_state: "queued",
          cancellation: false,
        };
        status = 202;
        data = { job_id: id, state: "queued" };
      }
    } else if (/token-guard\/jobs\/[^/]+\/cancel$/.test(path)) {
      fixture.cancelled.push(path);
      fixture.runtime = {
        ...fixture.runtime,
        running: false,
        job_state: "cancelled",
        cancellation: true,
      };
      data = { cancelled: true };
    } else if (path.endsWith("/token-guard/events")) {
      fixture.eventsRequests.push(u.searchParams.get("before_id"));
      if (fixture.holdLatest && u.searchParams.get("before_id") === "0") {
        fixture.holdLatest = false;
        fixture.latestHeld?.();
        await new Promise((resolve) => (fixture.releaseLatest = resolve));
      }
      data =
        u.searchParams.get("before_id") === "20"
          ? {
              items: [
                { ...latest[0], id: 19, detail: "Older sanitized event" },
              ],
              next_cursor: 0,
            }
          : { items: latest, next_cursor: 20 };
    }
    await route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(data),
    });
  });
  return fixture;
}

test("TokenGuard preserves full config, resumes jobs, cancels, paginates and reports conflicts", async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.PLAYWRIGHT_CHANNEL || "chrome",
  });
  const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
    }),
    errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await mkdir(artifacts, { recursive: true });
  try {
    const state = await setup(page);
    await page.goto(base + "/admin/smart-ops/tokens");
    await page
      .getByRole("heading", { name: "Credential checks", exact: true })
      .waitFor();
    await page.getByLabel("Interval (seconds)", { exact: true }).fill("900");
    await page.getByLabel("Probe concurrency", { exact: true }).fill("5");
    await page.getByLabel("Scope group IDs", { exact: true }).fill("4, 2, 4");
    await page.getByLabel("Mapping 1 password", { exact: true }).fill("pa,ss");
    await page.getByLabel("Mapping 1 MFA", { exact: true }).fill("");
    await page.getByLabel("Bark key", { exact: true }).fill("");
    await page
      .getByLabel("Probe request headers", { exact: true })
      .fill("Authorization: ********");
    await page
      .getByLabel("Relogin request headers", { exact: true })
      .fill("X-Key: ");
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    assert.equal(
      await page.getByLabel("Interval (seconds)", { exact: true }).inputValue(),
      "900",
      "refresh retains unsaved draft",
    );
    await page
      .getByRole("button", { name: "Save token guard settings", exact: true })
      .click();
    await page
      .locator(".ops-notice")
      .filter({ hasText: "Settings saved" })
      .waitFor();
    assert.equal(state.saved.length, 1);
    assert.equal(state.saved[0].probe_concurrency, 5);
    assert.equal(state.saved[0].interval_seconds, 900);
    assert.deepEqual(state.saved[0].group_ids, [2, 4]);
    assert.equal(state.saved[0].relogin_accounts[0].account_id, 7);
    assert.equal(state.saved[0].relogin_accounts[0].password, "pa,ss");
    assert.equal(state.saved[0].relogin_accounts[0].mfa_secret, "");
    assert.equal(state.saved[0].bark_key, "");
    assert.equal(state.saved[0].relogin_headers["X-Key"], "");
    assert.deepEqual(
      Object.keys(state.saved[0]).sort(),
      Object.keys(configFixture()).sort(),
    );
    assert.equal(
      await page.getByLabel("Mapping 1 password", { exact: true }).inputValue(),
      "********",
      "saved secrets replaced by masked server values",
    );
    await page.getByLabel("Scope group IDs", { exact: true }).fill("invalid");
    await page
      .getByRole("button", { name: "Save token guard settings", exact: true })
      .click();
    await page
      .getByRole("alert")
      .filter({ hasText: "Group IDs must be positive integers" })
      .waitFor();
    assert.equal(state.saved.length, 1);
    await page.getByLabel("Scope group IDs", { exact: true }).fill("2, 4");
    state.failSave = true;
    await page.getByLabel("Interval (seconds)", { exact: true }).fill("901");
    await page
      .getByRole("button", { name: "Save token guard settings", exact: true })
      .click();
    await page
      .getByRole("alert")
      .filter({ hasText: "Invalid mapping" })
      .waitFor();
    assert.equal(
      await page.getByLabel("Interval (seconds)", { exact: true }).inputValue(),
      "901",
      "server errors preserve editable draft",
    );
    await page.getByRole("button", { name: "Run check", exact: true }).click();
    await page
      .getByText("Job job-1 accepted. Progress updates automatically.", {
        exact: true,
      })
      .waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "Job running", exact: true })
        .isDisabled(),
      true,
    );
    await page.reload();
    await page
      .getByRole("button", { name: "Cancel job", exact: true })
      .waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "Job running", exact: true })
        .isDisabled(),
      true,
      "reload retains durable busy status",
    );
    await page.getByRole("button", { name: "Cancel job", exact: true }).click();
    await page
      .getByRole("button", { name: "Run check", exact: true })
      .waitFor();
    assert.equal(state.cancelled.length, 1);
    state.conflict = true;
    await page.getByRole("button", { name: "Run check", exact: true }).click();
    await page
      .getByRole("alert")
      .filter({ hasText: "Another incompatible job is active" })
      .waitFor();
    await page.getByRole("button", { name: "Relogin", exact: true }).click();
    await page
      .getByText("Job job-2 accepted. Progress updates automatically.", {
        exact: true,
      })
      .waitFor();
    assert.ok(state.jobs[1].endsWith("/accounts/7/relogin"));
    await page.getByRole("button", { name: "Cancel job", exact: true }).click();
    await page
      .getByRole("button", { name: "Run check", exact: true })
      .waitFor();
    state.holdLatest = true;
    await new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error("polling did not request latest events")),
        10000,
      );
      state.latestHeld = () => {
        clearTimeout(timer);
        resolve();
      };
    });
    await page
      .getByRole("button", { name: "Load more events", exact: true })
      .click();
    await page.getByRole("cell", { name: /Older sanitized event/ }).waitFor();
    assert.ok(state.eventsRequests.includes("20"));
    const latestFinished = page.waitForResponse((response) =>
      response.url().includes("/token-guard/events?before_id=0"),
    );
    state.releaseLatest();
    await latestFinished;
    await page.waitForTimeout(100);
    assert.equal(
      await page.getByRole("cell", { name: /Older sanitized event/ }).count(),
      1,
      "in-flight polling must not overwrite paginated history",
    );
    assert.equal(
      await page
        .getByRole("button", { name: "Load more events", exact: true })
        .count(),
      0,
    );
    assert.ok(
      await page
        .getByText(
          "Credentials repaired; manual scheduling restriction retained",
          { exact: true },
        )
        .isVisible(),
    );
    await page.screenshot({
      path: artifacts + "/token-guard-desktop.png",
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: artifacts + "/token-guard-mobile.png",
      fullPage: true,
    });
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth + 1,
      ),
      "mobile page does not overflow",
    );
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});

test("TokenGuard honours global disable and renders all supported languages", async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.PLAYWRIGHT_CHANNEL || "chrome",
  });
  try {
    for (const [lang, title, switchName, save] of [
      [
        "en",
        "Credential checks",
        "Enable smart operations",
        "Save token guard settings",
      ],
      ["zh", "凭证巡检", "启用智能运维模块", "保存凭证守护配置"],
      ["zh-TW", "憑證巡檢", "啟用智慧維運模組", "儲存憑證守護設定"],
    ]) {
      const page = await browser.newPage({
        viewport: { width: 390, height: 844 },
      });
      await setup(page, { lang, moduleEnabled: false });
      await page.goto(base + "/admin/token-guard");
      await page.getByRole("heading", { name: title, exact: true }).waitFor();
      assert.equal(
        await page
          .getByRole("button", { name: save, exact: true })
          .isDisabled(),
        true,
      );
      assert.equal(
        await page
          .getByRole("switch", { name: switchName, exact: true })
          .isDisabled(),
        false,
      );
      await page.getByRole("switch", { name: switchName, exact: true }).check();
      await page.waitForFunction(
        () => !document.querySelector("fieldset.ops-form").disabled,
      );
      assert.ok(
        !(await page.locator(".account-ops-workspace").innerText()).includes(
          "tokenGuard.",
        ),
        "no untranslated TokenGuard keys",
      );
      assert.ok(
        !(await page.locator(".account-ops-workspace").innerText()).includes(
          "smartOps.",
        ),
        "no untranslated navigation keys",
      );
      await page.close();
    }
  } finally {
    await browser.close();
  }
});

test('source-managed version popover identifies unknown checks and the upstream repository',async()=>{
  const browser=await chromium.launch({headless:true,channel:process.env.PLAYWRIGHT_CHANNEL||'chrome'})
  const page=await browser.newPage({viewport:{width:1440,height:1000}})
  try {
    await setup(page);await page.goto(base+'/admin/smart-ops/tokens')
    await page.getByRole('heading',{name:'Credential checks',exact:true}).waitFor()
    await page.getByRole('button',{name:/^v\d+\.\d+/}).click()
    await page.getByText('Upstream version unconfirmed',{exact:true}).waitFor()
    await page.getByText('Source: hloolx/codex2api',{exact:true}).waitFor()
    await page.getByText('Local revision: abcdef012345',{exact:true}).waitFor()
    await page.getByText('Use the reviewed source release workflow',{exact:true}).waitFor()
    assert.equal(await page.getByRole('link',{name:'View upstream commits',exact:true}).getAttribute('href'),'https://github.com/hloolx/codex2api/commits/main')
    assert.equal(await page.getByRole('button',{name:'Update now',exact:true}).count(),0)
    await page.screenshot({path:artifacts+'/managed-version-desktop.png',fullPage:false})
  } finally {await browser.close()}
})
