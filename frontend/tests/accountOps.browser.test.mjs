import assert from "node:assert/strict";
import { test } from "node:test";
import { chromium } from "playwright";
import { newQualityPlan } from "../src/lib/accountOps.ts";

// Run against a local Vite server. Every API request is intercepted; no live
// accounts, mailboxes or upstream models participate in this smoke test.
test("source account quality and alert pages save, trigger, inspect and paginate", async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.PLAYWRIGHT_CHANNEL || "chrome",
  });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const plans = [];
  let moduleEnabled = false;
  let config = {
    config: {
      enabled: false,
      recipient: "",
      balance_low: true,
      weekly_quota: true,
      cooldown_minutes: 60,
    },
    smtp: {
      host: "",
      port: 587,
      username: "",
      password_configured: false,
      from: "",
      tls_mode: "starttls",
    },
    dropped: 0,
    failures: 0,
  };
  let triggered = 0;
  const accounts = [
    {
      id: 1,
      name: "Test account",
      email: "account@example.test",
      group_ids: [1],
    },
    {
      id: 2,
      name: "Judge account",
      email: "judge@example.test",
      group_ids: [1],
    },
  ];
  const groups = [{ id: 1, name: "Judges", channel: "codex", member_count: 2 }];
  const now = new Date().toISOString();
  let history = [];
  await page.route("**/api/**", async (route) => {
    const u = new URL(route.request().url()),
      path = u.pathname;
    const post = route.request().method() !== "GET";
    let data = {};
    if (path.endsWith("/bootstrap-status")) data = { needs_bootstrap: false };
    else if (path.endsWith("/health")) data = { status: "ok" };
    else if (path.endsWith("/branding")) data = { site_name: "Codex2API" };
    else if (path.endsWith("/settings/visible-channels"))
      data = { channels: ["codex"] };
    else if (path.endsWith("/account-ops/module")) {
      if (post) moduleEnabled = route.request().postDataJSON().enabled;
      data = { enabled: moduleEnabled };
    } else if (path.endsWith("/account-ops/config")) {
      if (post) {
        config = { ...config, ...route.request().postDataJSON() };
        config.smtp.password = "";
        config.smtp.password_configured = true;
      }
      data = config;
    } else if (path.endsWith("/account-ops/alerts")) data = { items: [] };
    else if (path.endsWith("/quality-ops/plans")) {
      if (post) {
        const p = route.request().postDataJSON();
        const index = plans.findIndex((old) => old.id === p.id);
        const saved = {
          ...p,
          id: p.id || plans.length + 1,
          version: p.version + 1,
          next_run: now,
        };
        if (index >= 0) plans[index] = saved;
        else plans.push(saved);
        data = saved;
      } else data = { items: plans };
    } else if (/quality-ops\/plans\/\d+\/trigger$/.test(path)) {
      triggered++;
      history = [
        {
          id: 10,
          plan_id: 1,
          account_id: 1,
          account_name: "Test account",
          started_at: now,
          completed_at: now,
          outcome: "failed",
          action: "groups_removed",
          passed_count: 0,
          total_count: 2,
          plan: plans[0],
          results: [
            {
              output: "20",
              verdict: "incorrect",
              reason: "wrong total",
              account_id: 2,
              model_id: "judge-model",
              duration_ms: 100,
            },
          ],
        },
      ];
      data = { queued: true };
    } else if (path.endsWith("/quality-ops/history"))
      data =
        u.searchParams.has("before") && u.searchParams.get("before") !== "0"
          ? {
              items: [{ ...history[0], id: 9, account_name: "Older account" }],
              next_cursor: 0,
            }
          : { items: history, next_cursor: history.length ? 10 : 0 };
    else if (path.endsWith("/quality-ops/history/10")) data = history[0];
    else if (path.endsWith("/quality-test-prompts")) data = { prompts: [] };
    else if (path.endsWith("/quality-test/options"))
      data = {
        models: ["test-model", "judge-model"],
        reasoning_efforts: ["", "medium", "high"],
      };
    else if (path.endsWith("/accounts")) data = { accounts };
    else if (path.endsWith("/account-groups")) data = { groups };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(data),
    });
  });
  try {
    await page.goto(
      (process.env.ACCOUNT_OPS_TEST_URL || "http://127.0.0.1:5197") +
        "/admin/quality-ops",
    );
    await page
      .getByRole("heading", { name: "降智运维", exact: true })
      .waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "新建规则", exact: true })
        .isDisabled(),
      true,
    );
    await page.getByRole("switch", { name: "启用账号运维内置模块" }).click();
    await page.waitForFunction(
      () =>
        document.querySelector('[aria-label="启用账号运维内置模块"]').checked,
    );
    await page.getByRole("button", { name: "新建规则", exact: true }).click();
    await page
      .getByRole("checkbox", { name: "Test account #1", exact: true })
      .check();
    await page.getByLabel("测试模型", { exact: true }).fill("test-model");
    await page.getByLabel("判题分组", { exact: true }).selectOption("1");
    await page.getByLabel("判题模型", { exact: true }).fill("judge-model");
    await page.getByLabel("每轮并行次数").fill("2");
    await page.getByRole("checkbox", { name: "Judges", exact: true }).check();
    await page.getByRole("button", { name: "保存规则", exact: true }).click();
    await page.getByText("已保存 1 条规则").waitFor();
    assert.equal(plans[0].action, "remove_groups");
    assert.equal(plans[0].auto_restore, false);
    assert.equal(plans[0].prompt, newQualityPlan().prompt);
    assert.equal(plans[0].samples, 2);
    await page.getByRole("button", { name: "立即检测" }).click();
    await page.getByText("已加入下一次扫描").waitFor();
    assert.equal(triggered, 1);
    await page.getByRole("button", { name: "详情", exact: true }).click();
    await page.getByText("wrong total", { exact: true }).waitFor();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "加载更多", exact: true }).click();
    await page.getByRole("cell", { name: /Older account/ }).waitFor();
    await page.screenshot({
      path: "/tmp/account-quality-port-desktop.png",
      fullPage: true,
    });
    await page
      .locator(".ops-tabs")
      .getByRole("link", { name: "账号告警" })
      .click();
    await page
      .getByLabel("SMTP 主机", { exact: true })
      .fill("smtp.example.test");
    await page
      .getByLabel("发件邮箱", { exact: true })
      .fill("from@example.test");
    await page.getByLabel("收件邮箱", { exact: true }).fill("ops@example.test");
    await page.getByRole("switch", { name: "启用邮件告警" }).check();
    await page
      .getByRole("button", { name: "保存告警配置", exact: true })
      .click();
    await page.getByText("告警与 SMTP 配置已保存").waitFor();
    assert.equal(config.config.enabled, true);
    assert.equal(config.config.cooldown_minutes, 60);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: "/tmp/account-alerts-port-mobile.png",
      fullPage: true,
    });
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth + 1,
      ),
      "mobile overflow",
    );
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});
