import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  useRef,
  type FormEvent,
} from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import type { AccountRow, AccountGroup } from "../types";
import { Button } from "../components/ui/button";
import { Select } from "../components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { AccountOpsModule } from "./AccountOps";
import {
  CANDY_PROMPT,
  date,
  formatQualityAction,
  newQualityPlan,
  type AccountQualityPlan,
  type AccountQualityRound,
} from "../lib/accountOps";
import "./account-ops.css";
export default function AccountQuality() {
  const [enabled, setEnabled] = useState(false),
    [plans, setPlans] = useState<AccountQualityPlan[]>([]),
    [rounds, setRounds] = useState<AccountQualityRound[]>([]),
    [cursor, setCursor] = useState(0),
    [accounts, setAccounts] = useState<AccountRow[]>([]),
    [groups, setGroups] = useState<AccountGroup[]>([]),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false),
    [loaded, setLoaded] = useState(false);
  const [search, setSearch] = useState(""),
    [selected, setSelected] = useState<number | null>(null),
    [filter, setFilter] = useState("all"),
    [form, setForm] = useState<AccountQualityPlan | null>(null),
    [selectedAccounts, setSelectedAccounts] = useState<number[]>([]),
    [accountSearch, setAccountSearch] = useState(""),
    [detail, setDetail] = useState<AccountQualityRound | null>(null),
    [models, setModels] = useState<string[]>([]),
    [efforts, setEfforts] = useState<string[]>([]),
    [judgeModels, setJudgeModels] = useState<string[]>([]),
    [presets, setPresets] = useState<
      { id: number; name: string; prompt: string }[]
    >([]);
  const paginated = useRef(false);
  const fail = (e: unknown) =>
    setError(e instanceof Error ? e.message : String(e));
  const load = useCallback(async () => {
    try {
      const [module, p, h, a, g] = await Promise.all([
        api.getAccountOpsModule(),
        api.getAccountQualityPlans(),
        api.getAccountQualityHistory(),
        api.getAccounts({ view: "lite" }),
        api.listAccountGroups(),
      ]);
      setEnabled(module.enabled);
      setPlans(p.items);
      setRounds(h.items);
      setCursor(h.next_cursor);
      setAccounts(a.accounts);
      setGroups(g.groups);
      setLoaded(true);
    } catch (e) {
      fail(e);
    }
  }, []);
  useEffect(() => {
    void load();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible" && !paginated.current)
        void load();
    }, 30000);
    return () => clearInterval(timer);
  }, [load]);
  useEffect(() => {
    if (!form) return;
    const ids = form.id ? [form.account_id] : selectedAccounts;
    if (!ids.length) {
      setModels([]);
      setEfforts([]);
      return;
    }
    let alive = true;
    void Promise.all(ids.map((id) => api.getQualityTestOptions(id)))
      .then((options) => {
        if (!alive) return;
        setModels(
          options[0].models.filter((m) =>
            options.every((o) => o.models.includes(m)),
          ),
        );
        setEfforts(
          options[0].reasoning_efforts.filter((m) =>
            options.every((o) => o.reasoning_efforts.includes(m)),
          ),
        );
      })
      .catch(fail);
    return () => {
      alive = false;
    };
  }, [form?.id, form?.account_id, selectedAccounts]);
  useEffect(() => {
    if (!form?.judge?.group_id) {
      setJudgeModels([]);
      return;
    }
    let alive = true;
    const candidates = accounts.filter((a) =>
      a.group_ids?.includes(form.judge!.group_id),
    );
    void Promise.allSettled(
      candidates.map((a) => api.getQualityTestOptions(a.id)),
    ).then((results) => {
      if (alive)
        setJudgeModels([
          ...new Set(
            results.flatMap((r) =>
              r.status === "fulfilled" ? r.value.models : [],
            ),
          ),
        ]);
    });
    return () => {
      alive = false;
    };
  }, [form?.judge?.group_id, accounts]);
  const names = useMemo(
    () => new Map(accounts.map((a) => [a.id, a.name || a.email || `#${a.id}`])),
    [accounts],
  );
  const groupNames = (ids: number[]) =>
    ids
      .map((id) => groups.find((g) => g.id === id)?.name || `#${id}`)
      .join("、") || "—";
  const visible = plans.filter((p) =>
    `${names.get(p.account_id)} ${p.account_id} ${p.model}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  const history = rounds.filter(
    (r) =>
      (selected === null || r.plan_id === selected) &&
      (filter === "all" ||
        (filter === "attention"
          ? ["restore_conflict", "action_failed"].includes(r.action)
          : r.outcome === filter)),
  );
  const mutate = async (fn: () => Promise<unknown>, message: string) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      setNotice(message);
      await load();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };
  const edit = (p?: AccountQualityPlan) => {
    setForm(p ? structuredClone(p) : newQualityPlan());
    setSelectedAccounts([]);
    setAccountSearch("");
    setError("");
    void api
      .getQualityTestPrompts()
      .then((r) => setPresets(r.prompts))
      .catch(fail);
  };
  const patch = (value: Partial<AccountQualityPlan>) =>
    setForm((f) => (f ? { ...f, ...value } : f));
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!form) return;
    const ids = form.id ? [form.account_id] : selectedAccounts;
    const saved: number[] = [];
    setBusy(true);
    setError("");
    try {
      for (const id of ids) {
        await api.saveAccountQualityPlan({ ...form, account_id: id });
        saved.push(id);
      }
      setForm(null);
      setNotice(`已保存 ${saved.length} 条规则`);
    } catch (e) {
      setSelectedAccounts(ids.filter((id) => !saved.includes(id)));
      fail(e);
    } finally {
      setBusy(false);
      await load();
    }
  };
  return (
    <div className="account-ops-workspace">
      <nav className="ops-tabs">
        <Link className="active" to="/quality-ops">
          降智运维
        </Link>
        <Link to="/account-ops">账号告警</Link>
      </nav>
      <header className="ops-heading">
        <div>
          <h1>降智运维</h1>
          <p>按账号定时检测回答质量，按规则执行失败动作与自动恢复。</p>
        </div>
        <div>
          <Button variant="outline" onClick={() => void load()}>
            刷新
          </Button>{" "}
          <Button disabled={!enabled || busy} onClick={() => edit()}>
            新建规则
          </Button>
        </div>
      </header>
      <AccountOpsModule enabled={enabled} onChange={setEnabled} />
      {error && (
        <p role="alert" className="ops-error">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="ops-notice">
          {notice}
        </p>
      )}
      <div className="ops-summary">
        <span>
          规则总数 <strong>{loaded ? plans.length : "—"}</strong>
        </span>
        <span>
          运行中规则 <strong>{plans.filter((p) => p.enabled).length}</strong>
        </span>
        <span>
          需要关注{" "}
          <strong>
            {
              rounds.filter((r) =>
                ["restore_conflict", "action_failed"].includes(r.action),
              ).length
            }
          </strong>
        </span>
        <span>
          已加载轮次 <strong>{rounds.length}</strong>
        </span>
      </div>
      <div className="ops-columns">
        <section className="ops-panel">
          <header>
            <h2>规则库</h2>
            <p>每个账号最多一条规则</p>
          </header>
          <input
            aria-label="搜索规则"
            placeholder="搜索账号、模型"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <Button variant="ghost" onClick={() => setSelected(null)}>
            全部账号
          </Button>
          <div className="ops-rule-list">
            {visible.map((p) => (
              <article
                className={`ops-rule ${selected === p.id ? "selected" : ""}`}
                key={p.id}
              >
                <div className="ops-rule-title">
                  <button onClick={() => setSelected(p.id)}>
                    <strong>
                      {names.get(p.account_id) || `#${p.account_id}`}
                    </strong>
                    <small>
                      #{p.account_id} · 规则 {p.id}
                    </small>
                  </button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={!enabled || busy}
                    onClick={() =>
                      void mutate(
                        () =>
                          api.saveAccountQualityPlan({
                            ...p,
                            enabled: !p.enabled,
                          }),
                        p.enabled ? "规则已暂停" : "规则已启用",
                      )
                    }
                  >
                    {p.enabled ? "运行中" : "已暂停"}
                  </Button>
                </div>
                <code>{p.model}</code>
                <p>
                  {p.action === "remove_groups"
                    ? groupNames(p.remove_group_ids)
                    : "关闭调度"}
                </p>
                <small>下次检测 {p.enabled ? date(p.next_run) : "—"}</small>
                {!p.judge && <p>请配置判题器</p>}
                <footer>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setSelected(p.id)}
                  >
                    记录
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={!enabled || busy}
                    onClick={() => edit(p)}
                  >
                    编辑
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={!enabled || busy || !p.enabled}
                    onClick={() =>
                      void mutate(
                        () => api.triggerAccountQualityPlan(p.id),
                        "已加入下一次扫描",
                      )
                    }
                  >
                    立即检测
                  </Button>
                </footer>
              </article>
            ))}
            {!visible.length && (
              <p className="ops-empty">{loaded ? "暂无规则" : "加载中…"}</p>
            )}
          </div>
        </section>
        <section className="ops-panel">
          <header>
            <h2>运营记录</h2>
            <p>按轮次查看测试回答与账号处理结果</p>
          </header>
          <div className="ops-toolbar">
            <span>{selected === null ? "全部账号" : `规则 ${selected}`}</span>
            <Select
              aria-label="筛选记录"
              value={filter}
              options={[
                { value: "all", label: "全部结果" },
                { value: "passed", label: "全部通过" },
                { value: "failed", label: "未通过" },
                { value: "inconclusive", label: "不确定" },
                { value: "attention", label: "需要关注" },
              ]}
              onValueChange={setFilter}
            />
          </div>
          <div className="ops-table-scroll">
            <table>
              <thead>
                <tr>
                  <th>检测时间</th>
                  <th>账号</th>
                  <th>测试结果</th>
                  <th>账号处理</th>
                  <th>详情</th>
                </tr>
              </thead>
              <tbody>
                {history.map((r) => (
                  <tr key={r.id}>
                    <td>
                      {date(r.started_at)}
                      <small>完成 {date(r.completed_at)}</small>
                    </td>
                    <td>
                      {r.account_name || `#${r.account_id}`}
                      <small>规则 {r.plan_id}</small>
                    </td>
                    <td>
                      {r.passed_count} / {r.total_count}
                      <small>
                        {r.outcome === "passed"
                          ? "全部通过"
                          : r.outcome === "failed"
                            ? "未通过"
                            : "不确定"}
                      </small>
                    </td>
                    <td>
                      {formatQualityAction(r.action, r.plan.action)}
                      <small>
                        {r.plan.action === "remove_groups"
                          ? groupNames(r.plan.remove_group_ids)
                          : "关闭调度"}
                      </small>
                    </td>
                    <td>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() =>
                          void api
                            .getAccountQualityRound(r.id)
                            .then(setDetail)
                            .catch(fail)
                        }
                      >
                        详情
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!history.length && <p className="ops-empty">暂无检测记录</p>}
          </div>
          {cursor > 0 && (
            <Button
              variant="ghost"
              onClick={() =>
                void api
                  .getAccountQualityHistory(cursor)
                  .then((p) => {
                    paginated.current = true;
                    setRounds((r) => [
                      ...new Map(
                        [...r, ...p.items].map((item) => [item.id, item]),
                      ).values(),
                    ]);
                    setCursor(p.next_cursor);
                  })
                  .catch(fail)
              }
            >
              加载更多
            </Button>
          )}
        </section>
      </div>
      <Dialog
        open={form !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setForm(null);
        }}
      >
        <DialogContent className="ops-dialog">
          <DialogHeader>
            <DialogTitle>{form?.id ? "编辑规则" : "新建规则"}</DialogTitle>
          </DialogHeader>
          {form && (
            <form onSubmit={(e) => void save(e)}>
              <fieldset disabled={busy} className="ops-form">
                {!form.id && (
                  <fieldset>
                    <legend>选择账号（支持批量）</legend>
                    <input
                      aria-label="搜索账号"
                      placeholder="搜索账号"
                      value={accountSearch}
                      onChange={(e) => setAccountSearch(e.target.value)}
                    />
                    <div className="ops-check-list">
                      {accounts
                        .filter((a) =>
                          `${a.name} ${a.email} ${a.id}`
                            .toLowerCase()
                            .includes(accountSearch.toLowerCase()),
                        )
                        .map((a) => (
                          <label key={a.id}>
                            <input
                              type="checkbox"
                              checked={selectedAccounts.includes(a.id)}
                              disabled={plans.some(
                                (p) => p.account_id === a.id,
                              )}
                              onChange={(e) =>
                                setSelectedAccounts((ids) =>
                                  e.target.checked
                                    ? [...ids, a.id]
                                    : ids.filter((id) => id !== a.id),
                                )
                              }
                            />
                            {a.name || a.email || `#${a.id}`} #{a.id}
                          </label>
                        ))}
                    </div>
                    <small>已选 {selectedAccounts.length} 个账号</small>
                  </fieldset>
                )}
                <div className="ops-grid">
                  <label>
                    测试模型
                    <input
                      aria-label="测试模型"
                      required
                      maxLength={100}
                      list="ops-test-models"
                      value={form.model}
                      onChange={(e) => patch({ model: e.target.value })}
                    />
                    <datalist id="ops-test-models">
                      {models.map((m) => (
                        <option key={m} value={m} />
                      ))}
                    </datalist>
                  </label>
                  <label>
                    五段 Cron
                    <input
                      required
                      aria-label="五段 Cron"
                      value={form.cron}
                      onChange={(e) => patch({ cron: e.target.value })}
                    />
                  </label>
                  <label>
                    推理强度
                    <Select
                      aria-label="推理强度"
                      value={form.reasoning_effort}
                      options={[
                        ...new Set(["", form.reasoning_effort, ...efforts]),
                      ].map((m) => ({ value: m, label: m || "模型默认" }))}
                      onValueChange={(value) => patch({ reasoning_effort: value })}
                    />
                  </label>
                  <label>
                    每轮并行次数
                    <input
                      aria-label="每轮并行次数"
                      type="number"
                      min={1}
                      max={8}
                      required
                      value={form.samples}
                      onChange={(e) =>
                        patch({ samples: Number(e.target.value) })
                      }
                    />
                  </label>
                  <label>
                    保留结果数
                    <input
                      type="number"
                      min={1}
                      max={200}
                      required
                      value={form.max_results}
                      onChange={(e) =>
                        patch({ max_results: Number(e.target.value) })
                      }
                    />
                  </label>
                </div>
                <label>
                  题目
                  <textarea
                    aria-label="题目"
                    required
                    maxLength={32000}
                    rows={5}
                    value={form.prompt}
                    onChange={(e) => patch({ prompt: e.target.value })}
                  />
                </label>
                <div className="ops-toolbar">
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      patch({ prompt: CANDY_PROMPT, expected_answer: "21" })
                    }
                  >
                    使用糖果题
                  </Button>
                  <Select
                    aria-label="使用已保存题目"
                    value=""
                    options={presets.map((p) => ({ value: String(p.id), label: p.name }))}
                    placeholder="使用已保存题目"
                    onValueChange={(value) => {
                      const preset = presets.find((p) => p.id === Number(value));
                      if (preset) patch({ prompt: preset.prompt });
                    }}
                  />
                </div>
                <label>
                  参考答案
                  <textarea
                    aria-label="参考答案"
                    rows={2}
                    required
                    maxLength={4000}
                    value={form.expected_answer}
                    onChange={(e) => patch({ expected_answer: e.target.value })}
                  />
                </label>
                <fieldset>
                  <legend>判题器</legend>
                  <div className="ops-grid">
                    <label>
                      判题分组
                      <Select
                        aria-label="判题分组"
                        value={form.judge?.group_id ? String(form.judge.group_id) : ""}
                        options={groups.map((g) => ({ value: String(g.id), label: `${g.name} #${g.id}` }))}
                        placeholder="请选择分组"
                        onValueChange={(value) =>
                          patch({
                            judge: {
                              ...(form.judge || newQualityPlan().judge!),
                              group_id: Number(value),
                            },
                          })
                        }
                      />
                    </label>
                    <label>
                      判题模型
                      <input
                        required
                        aria-label="判题模型"
                        maxLength={100}
                        list="ops-judge-models"
                        value={form.judge?.model_id || ""}
                        onChange={(e) =>
                          patch({
                            judge: {
                              ...(form.judge || newQualityPlan().judge!),
                              model_id: e.target.value,
                            },
                          })
                        }
                      />
                      <datalist id="ops-judge-models">
                        {judgeModels.map((m) => (
                          <option key={m} value={m} />
                        ))}
                      </datalist>
                    </label>
                  </div>
                  <label>
                    判题提示词
                    <textarea
                      required
                      aria-label="判题提示词"
                      rows={3}
                      maxLength={16000}
                      value={form.judge?.prompt || ""}
                      onChange={(e) =>
                        patch({
                          judge: {
                            ...(form.judge || newQualityPlan().judge!),
                            prompt: e.target.value,
                          },
                        })
                      }
                    />
                  </label>
                  <small>
                    语义比较回答与参考答案；不确定及请求失败不触发账号变更。
                  </small>
                </fieldset>
                <fieldset>
                  <legend>失败动作</legend>
                  <label className="ops-check">
                    <input
                      type="radio"
                      name="action"
                      checked={form.action === "remove_groups"}
                      onChange={() => patch({ action: "remove_groups" })}
                    />
                    取消指定分组归属
                  </label>
                  {form.action === "remove_groups" && (
                    <div className="ops-check-list">
                      {groups.map((g) => (
                        <label key={g.id}>
                          <input
                            type="checkbox"
                            checked={form.remove_group_ids.includes(g.id)}
                            onChange={(e) =>
                              patch({
                                remove_group_ids: e.target.checked
                                  ? [...form.remove_group_ids, g.id]
                                  : form.remove_group_ids.filter(
                                      (id) => id !== g.id,
                                    ),
                              })
                            }
                          />
                          {g.name}
                        </label>
                      ))}
                    </div>
                  )}
                  <label className="ops-check">
                    <input
                      type="radio"
                      name="action"
                      checked={form.action === "disable_scheduling"}
                      onChange={() => patch({ action: "disable_scheduling" })}
                    />
                    关闭账号调度
                  </label>
                </fieldset>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={form.auto_restore}
                    onChange={(e) => patch({ auto_restore: e.target.checked })}
                  />
                  后续全轮通过后自动恢复本规则执行的变更
                </label>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={form.enabled}
                    onChange={(e) => patch({ enabled: e.target.checked })}
                  />
                  启用规则
                </label>
                {error && (
                  <p role="alert" className="ops-error">
                    {error}
                  </p>
                )}
                <footer>
                  {form.id > 0 && (
                    <Button
                      type="button"
                      variant="destructive"
                      onClick={() => {
                        if (
                          window.confirm(
                            "删除规则会清除历史和恢复记录，不会自动恢复账号，确定删除？",
                          )
                        )
                          void mutate(async () => {
                            await api.deleteAccountQualityPlan(form.id);
                            setForm(null);
                          }, "规则已删除");
                      }}
                    >
                      删除规则
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => setForm(null)}
                  >
                    取消
                  </Button>
                  <Button
                    type="submit"
                    disabled={!form.id && !selectedAccounts.length}
                  >
                    {busy ? "保存中…" : "保存规则"}
                  </Button>
                </footer>
              </fieldset>
            </form>
          )}
        </DialogContent>
      </Dialog>
      <Dialog
        open={detail !== null}
        onOpenChange={(open) => {
          if (!open) setDetail(null);
        }}
      >
        <DialogContent className="ops-dialog">
          <DialogHeader>
            <DialogTitle>轮次详情 · {detail?.account_name}</DialogTitle>
          </DialogHeader>
          {detail && (
            <>
              <p>
                {date(detail.started_at)} ·{" "}
                {formatQualityAction(detail.action, detail.plan.action)}
              </p>
              <p>参考答案：{detail.plan.expected_answer}</p>
              {detail.results?.map((r, i) => (
                <article className="ops-sample" key={i}>
                  <h3>
                    第 {i + 1} 次 ·{" "}
                    {r.verdict === "correct"
                      ? "通过"
                      : r.verdict === "incorrect"
                        ? "判错"
                        : "不确定"}
                  </h3>
                  <pre>{r.output || r.error || "无回答"}</pre>
                  <p>{r.reason || r.error}</p>
                  <small>
                    判题模型 {r.model_id || "—"} · 账号 #{r.account_id || "—"} ·{" "}
                    {(r.duration_ms / 1000).toFixed(1)}s
                  </small>
                </article>
              ))}
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
