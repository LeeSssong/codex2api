import { useTranslation } from "react-i18next";
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  useRef,
  type FormEvent,
} from "react";
import SmartOpsNav from "../components/SmartOpsNav";
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
  const { t } = useTranslation();
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
      setNotice(t("smartOps.savedRules", { count: saved.length }));
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
      <SmartOpsNav />
      <header className="ops-heading">
        <div>
          <h2>{t("smartOps.copy.qualityTitle")}</h2>
          <p>{t("smartOps.copy.qualityDescription")}</p>
        </div>
        <div>
          <Button variant="outline" onClick={() => void load()}>
            {t("smartOps.copy.refresh")}
          </Button>{" "}
          <Button disabled={!enabled || busy} onClick={() => edit()}>
            {t("smartOps.copy.newRule")}
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
          {t("smartOps.copy.totalRules")}
          <strong>{loaded ? plans.length : "—"}</strong>
        </span>
        <span>
          {t("smartOps.copy.activeRules")}
          <strong>{plans.filter((p) => p.enabled).length}</strong>
        </span>
        <span>
          {t("smartOps.copy.attention")}{" "}
          <strong>
            {
              rounds.filter((r) =>
                ["restore_conflict", "action_failed"].includes(r.action),
              ).length
            }
          </strong>
        </span>
        <span>
          {t("smartOps.copy.loadedRounds")}
          <strong>{rounds.length}</strong>
        </span>
      </div>
      <div className="ops-columns">
        <section className="ops-panel">
          <header>
            <h2>{t("smartOps.copy.ruleLibrary")}</h2>
            <p>{t("smartOps.copy.oneRule")}</p>
          </header>
          <input
            aria-label={t("smartOps.copy.searchRules")}
            placeholder={t("smartOps.copy.searchRulePlaceholder")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <Button variant="ghost" onClick={() => setSelected(null)}>
            {t("smartOps.copy.allAccounts")}
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
                      {t("smartOps.accountRule", {
                        account: p.account_id,
                        rule: p.id,
                      })}
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
                        p.enabled
                          ? t("smartOps.copy.rulePaused")
                          : t("smartOps.copy.ruleEnabled"),
                      )
                    }
                  >
                    {p.enabled
                      ? t("smartOps.copy.running")
                      : t("smartOps.copy.paused")}
                  </Button>
                </div>
                <code>{p.model}</code>
                <p>
                  {p.action === "remove_groups"
                    ? groupNames(p.remove_group_ids)
                    : t("smartOps.copy.disableScheduling")}
                </p>
                <small>
                  {t("smartOps.copy.nextCheck")}
                  {p.enabled ? date(p.next_run) : "—"}
                </small>
                {!p.judge && <p>{t("smartOps.copy.configureJudge")}</p>}
                <footer>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setSelected(p.id)}
                  >
                    {t("smartOps.copy.history")}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={!enabled || busy}
                    onClick={() => edit(p)}
                  >
                    {t("smartOps.copy.edit")}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={!enabled || busy || !p.enabled}
                    onClick={() =>
                      void mutate(
                        () => api.triggerAccountQualityPlan(p.id),
                        t("smartOps.copy.queued"),
                      )
                    }
                  >
                    {t("smartOps.copy.runCheck")}
                  </Button>
                </footer>
              </article>
            ))}
            {!visible.length && (
              <p className="ops-empty">
                {loaded
                  ? t("smartOps.copy.noRules")
                  : t("smartOps.copy.loading")}
              </p>
            )}
          </div>
        </section>
        <section className="ops-panel">
          <header>
            <h2>{t("smartOps.copy.operationsHistory")}</h2>
            <p>{t("smartOps.copy.historyDescription")}</p>
          </header>
          <div className="ops-toolbar">
            <span>
              {selected === null
                ? t("smartOps.copy.allAccounts")
                : t("smartOps.ruleNumber", { id: selected })}
            </span>
            <Select
              aria-label={t("smartOps.copy.filterHistory")}
              value={filter}
              options={[
                { value: "all", label: t("smartOps.copy.allResults") },
                { value: "passed", label: t("smartOps.copy.allPassed") },
                { value: "failed", label: t("smartOps.copy.failed") },
                {
                  value: "inconclusive",
                  label: t("smartOps.copy.inconclusive"),
                },
                { value: "attention", label: t("smartOps.copy.attention") },
              ]}
              onValueChange={setFilter}
            />
          </div>
          <div className="ops-table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t("smartOps.copy.checkTime")}</th>
                  <th>{t("smartOps.copy.account")}</th>
                  <th>{t("smartOps.copy.result")}</th>
                  <th>{t("smartOps.copy.accountAction")}</th>
                  <th>{t("smartOps.copy.details")}</th>
                </tr>
              </thead>
              <tbody>
                {history.map((r) => (
                  <tr key={r.id}>
                    <td>
                      {date(r.started_at)}
                      <small>
                        {t("smartOps.copy.completed")}
                        {date(r.completed_at)}
                      </small>
                    </td>
                    <td>
                      {r.account_name || `#${r.account_id}`}
                      <small>
                        {t("smartOps.copy.rule")}
                        {r.plan_id}
                      </small>
                    </td>
                    <td>
                      {r.passed_count} / {r.total_count}
                      <small>
                        {r.outcome === "passed"
                          ? t("smartOps.copy.allPassed")
                          : r.outcome === "failed"
                            ? t("smartOps.copy.failed")
                            : t("smartOps.copy.inconclusive")}
                      </small>
                    </td>
                    <td>
                      {formatQualityAction(r.action, r.plan.action, (key) =>
                        t("smartOps.actions." + key, { defaultValue: key }),
                      )}
                      <small>
                        {r.plan.action === "remove_groups"
                          ? groupNames(r.plan.remove_group_ids)
                          : t("smartOps.copy.disableScheduling")}
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
                        {t("smartOps.copy.details")}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!history.length && (
              <p className="ops-empty">{t("smartOps.copy.noHistory")}</p>
            )}
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
              {t("smartOps.copy.loadMore")}
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
            <DialogTitle>
              {form?.id
                ? t("smartOps.copy.editRule")
                : t("smartOps.copy.newRule")}
            </DialogTitle>
          </DialogHeader>
          {form && (
            <form onSubmit={(e) => void save(e)}>
              <fieldset disabled={busy} className="ops-form">
                {!form.id && (
                  <fieldset>
                    <legend>{t("smartOps.copy.selectAccounts")}</legend>
                    <input
                      aria-label={t("smartOps.copy.searchAccounts")}
                      placeholder={t("smartOps.copy.searchAccounts")}
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
                    <small>
                      {t("smartOps.selectedAccounts", {
                        count: selectedAccounts.length,
                      })}
                    </small>
                  </fieldset>
                )}
                <div className="ops-grid">
                  <label>
                    {t("smartOps.copy.testModel")}
                    <input
                      aria-label={t("smartOps.copy.testModel")}
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
                    {t("smartOps.copy.cron")}
                    <input
                      required
                      aria-label={t("smartOps.copy.cron")}
                      value={form.cron}
                      onChange={(e) => patch({ cron: e.target.value })}
                    />
                  </label>
                  <label>
                    {t("smartOps.copy.reasoning")}
                    <Select
                      aria-label={t("smartOps.copy.reasoning")}
                      value={form.reasoning_effort}
                      options={[
                        ...new Set(["", form.reasoning_effort, ...efforts]),
                      ].map((m) => ({
                        value: m,
                        label: m || t("smartOps.copy.modelDefault"),
                      }))}
                      onValueChange={(value) =>
                        patch({ reasoning_effort: value })
                      }
                    />
                  </label>
                  <label>
                    {t("smartOps.copy.samples")}
                    <input
                      aria-label={t("smartOps.copy.samples")}
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
                    {t("smartOps.copy.retention")}
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
                  {t("smartOps.copy.prompt")}
                  <textarea
                    aria-label={t("smartOps.copy.prompt")}
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
                    {t("smartOps.copy.candyPreset")}
                  </Button>
                  <Select
                    aria-label={t("smartOps.copy.savedPrompt")}
                    value=""
                    options={presets.map((p) => ({
                      value: String(p.id),
                      label: p.name,
                    }))}
                    placeholder={t("smartOps.copy.savedPrompt")}
                    onValueChange={(value) => {
                      const preset = presets.find(
                        (p) => p.id === Number(value),
                      );
                      if (preset) patch({ prompt: preset.prompt });
                    }}
                  />
                </div>
                <label>
                  {t("smartOps.copy.expectedAnswer")}
                  <textarea
                    aria-label={t("smartOps.copy.expectedAnswer")}
                    rows={2}
                    required
                    maxLength={4000}
                    value={form.expected_answer}
                    onChange={(e) => patch({ expected_answer: e.target.value })}
                  />
                </label>
                <fieldset>
                  <legend>{t("smartOps.copy.judge")}</legend>
                  <div className="ops-grid">
                    <label>
                      {t("smartOps.copy.judgeGroup")}
                      <Select
                        aria-label={t("smartOps.copy.judgeGroup")}
                        value={
                          form.judge?.group_id
                            ? String(form.judge.group_id)
                            : ""
                        }
                        options={groups.map((g) => ({
                          value: String(g.id),
                          label: `${g.name} #${g.id}`,
                        }))}
                        placeholder={t("smartOps.copy.selectGroup")}
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
                      {t("smartOps.copy.judgeModel")}
                      <input
                        required
                        aria-label={t("smartOps.copy.judgeModel")}
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
                    {t("smartOps.copy.judgePrompt")}
                    <textarea
                      required
                      aria-label={t("smartOps.copy.judgePrompt")}
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
                  <small>{t("smartOps.copy.judgeHint")}</small>
                </fieldset>
                <fieldset>
                  <legend>{t("smartOps.copy.failureAction")}</legend>
                  <label className="ops-check">
                    <input
                      type="radio"
                      name="action"
                      checked={form.action === "remove_groups"}
                      onChange={() => patch({ action: "remove_groups" })}
                    />
                    {t("smartOps.copy.removeGroups")}
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
                    {t("smartOps.copy.disableAccount")}
                  </label>
                </fieldset>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={form.auto_restore}
                    onChange={(e) => patch({ auto_restore: e.target.checked })}
                  />
                  {t("smartOps.copy.autoRestore")}
                </label>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={form.enabled}
                    onChange={(e) => patch({ enabled: e.target.checked })}
                  />
                  {t("smartOps.copy.enableRule")}
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
                        if (window.confirm(t("smartOps.copy.deleteConfirm")))
                          void mutate(async () => {
                            await api.deleteAccountQualityPlan(form.id);
                            setForm(null);
                          }, t("smartOps.copy.ruleDeleted"));
                      }}
                    >
                      {t("smartOps.copy.deleteRule")}
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => setForm(null)}
                  >
                    {t("smartOps.copy.cancel")}
                  </Button>
                  <Button
                    type="submit"
                    disabled={!form.id && !selectedAccounts.length}
                  >
                    {busy
                      ? t("smartOps.copy.saving")
                      : t("smartOps.copy.saveRule")}
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
            <DialogTitle>
              {t("smartOps.copy.roundDetail")}
              {detail?.account_name}
            </DialogTitle>
          </DialogHeader>
          {detail && (
            <>
              <p>
                {date(detail.started_at)} ·{" "}
                {formatQualityAction(detail.action, detail.plan.action, (key) =>
                  t("smartOps.actions." + key, { defaultValue: key }),
                )}
              </p>
              <p>
                {t("smartOps.copy.referencePrefix")}
                {detail.plan.expected_answer}
              </p>
              {detail.results?.map((r, i) => (
                <article className="ops-sample" key={i}>
                  <h3>
                    {t("smartOps.sampleNumber", { index: i + 1 })} ·{" "}
                    {r.verdict === "correct"
                      ? t("smartOps.copy.passed")
                      : r.verdict === "incorrect"
                        ? t("smartOps.copy.incorrect")
                        : t("smartOps.copy.inconclusive")}
                  </h3>
                  <pre>
                    {r.output || r.error || t("smartOps.copy.noAnswer")}
                  </pre>
                  <p>{r.reason || r.error}</p>
                  <small>
                    {t("smartOps.judgeIdentity", {
                      model: r.model_id || "—",
                      account: r.account_id || "—",
                    })}{" "}
                    · {(r.duration_ms / 1000).toFixed(1)}s
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
