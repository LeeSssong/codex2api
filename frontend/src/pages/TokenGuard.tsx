import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { useTranslation } from "react-i18next";
import {
  RefreshCw,
  Play,
  Square,
  Plus,
  Trash2,
  ShieldCheck,
} from "lucide-react";
import { api, AdminAPIError } from "../api";
import SmartOpsNav from "../components/SmartOpsNav";
import { Button } from "../components/ui/button";
import { AccountOpsModule } from "./AccountOps";
import { date } from "../lib/accountOps";
import {
  collectTokenGuardConfig,
  isTokenGuardBusy,
  tokenGuardHeadersText,
  TokenGuardFormError,
  type TokenGuardConfig,
  type TokenGuardEvent,
  type TokenGuardJob,
  type TokenGuardReloginAccount,
  type TokenGuardStatus,
} from "../lib/tokenGuard";
import "./account-ops.css";

type Draft = {
  config: TokenGuardConfig;
  groups: string;
  probeHeaders: string;
  reloginHeaders: string;
};
const toDraft = (config: TokenGuardConfig): Draft => ({
  config: structuredClone(config),
  groups: (config.group_ids ?? []).join(", "),
  probeHeaders: tokenGuardHeadersText(config.probe_headers),
  reloginHeaders: tokenGuardHeadersText(config.relogin_headers),
});
export default function TokenGuard() {
  const { t } = useTranslation();
  const [remote, setRemote] = useState<TokenGuardStatus | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [baseline, setBaseline] = useState<Draft | null>(null);
  const [events, setEvents] = useState<TokenGuardEvent[]>([]);
  const [cursor, setCursor] = useState(0);
  const [loading, setLoading] = useState(false),
    [saving, setSaving] = useState(false),
    [action, setAction] = useState(""),
    [paging, setPaging] = useState(false);
  const [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const [search, setSearch] = useState("");
  const alive = useRef(true),
    loadingRef = useRef(false),
    savingRef = useRef(false),
    paginated = useRef(false),
    revision = useRef(0),
    eventRevision = useRef(0);
  const dirty = Boolean(
    draft && JSON.stringify(draft) !== JSON.stringify(baseline),
  );
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  const busy = isTokenGuardBusy(remote?.runtime);
  const fail = useCallback(
    (e: unknown) => {
      setError(
        e instanceof TokenGuardFormError
          ? t("tokenGuard.validation." + e.field)
          : e instanceof AdminAPIError && e.status === 409
            ? t("tokenGuard.conflict")
            : e instanceof Error
              ? e.message
              : t("tokenGuard.error"),
      );
    },
    [t],
  );
  const applyConfig = useCallback((config: TokenGuardConfig) => {
    const next = toDraft(config);
    setDraft(next);
    setBaseline(next);
    dirtyRef.current = false;
  }, []);
  const load = useCallback(
    async (manual = false) => {
      if (loadingRef.current || savingRef.current) return;
      loadingRef.current = true;
      const requestRevision = revision.current;
      if (manual) eventRevision.current++;
      const requestedEvents = eventRevision.current;
      if (manual) {
        setLoading(true);
        setError("");
      }
      try {
        const status = await api.getTokenGuardStatus();
        if (!alive.current || requestRevision !== revision.current) return;
        setRemote(status);
        if (!dirtyRef.current) applyConfig(status.config);
        if (manual || !paginated.current) {
          const page = await api.getTokenGuardEvents();
          if (
            !alive.current ||
            requestRevision !== revision.current ||
            requestedEvents !== eventRevision.current
          )
            return;
          setEvents(page.items);
          setCursor(page.next_cursor);
          paginated.current = false;
        }
      } catch (e) {
        if (alive.current) fail(e);
      } finally {
        loadingRef.current = false;
        if (alive.current) setLoading(false);
      }
    },
    [applyConfig, fail],
  );
  useEffect(() => {
    alive.current = true;
    void load(true);
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 5000);
    return () => {
      alive.current = false;
      window.clearInterval(timer);
    };
  }, [load]);
  const patch = (value: Partial<TokenGuardConfig>) => {
    setNotice("");
    setDraft((d) => (d ? { ...d, config: { ...d.config, ...value } } : d));
  };
  const patchMapping = (
    index: number,
    value: Partial<TokenGuardReloginAccount>,
  ) => {
    if (draft)
      patch({
        relogin_accounts: draft.config.relogin_accounts.map((row, i) =>
          i === index ? { ...row, ...value } : row,
        ),
      });
  };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!draft || savingRef.current) return;
    setError("");
    setNotice("");
    let config: TokenGuardConfig;
    try {
      config = collectTokenGuardConfig(
        draft.config,
        draft.groups,
        draft.probeHeaders,
        draft.reloginHeaders,
      );
    } catch (e) {
      fail(e);
      return;
    }
    savingRef.current = true;
    setSaving(true);
    revision.current++;
    try {
      const saved = await api.saveTokenGuardConfig(config);
      if (!alive.current) return;
      applyConfig(saved);
      setRemote((r) => (r ? { ...r, config: saved } : r));
      setNotice(t("tokenGuard.saved"));
    } catch (e) {
      if (alive.current) fail(e);
    } finally {
      savingRef.current = false;
      if (alive.current) setSaving(false);
    }
  };
  const start = async (run: () => Promise<TokenGuardJob>, key: string) => {
    if (action || busy) return;
    setAction(key);
    setError("");
    setNotice("");
    try {
      const job = await run();
      if (!alive.current) return;
      setRemote((r) =>
        r
          ? {
              ...r,
              runtime: {
                ...r.runtime,
                job_id: job.job_id,
                job_state: job.state,
                running: true,
                cancellation: false,
              },
            }
          : r,
      );
      setNotice(t("tokenGuard.jobQueued", { id: job.job_id }));
      await load();
    } catch (e) {
      if (alive.current) {
        fail(e);
        void load();
      }
    } finally {
      if (alive.current) setAction("");
    }
  };
  const cancel = async () => {
    const id = remote?.runtime.job_id;
    if (!id || action) return;
    setAction("cancel");
    setError("");
    setNotice("");
    try {
      const result = await api.cancelTokenGuardJob(id);
      if (!alive.current) return;
      setNotice(
        t(
          result.cancelled
            ? "tokenGuard.cancelAccepted"
            : "tokenGuard.alreadyFinished",
        ),
      );
      await load();
    } catch (e) {
      if (alive.current) fail(e);
    } finally {
      if (alive.current) setAction("");
    }
  };
  const more = async () => {
    if (!cursor || paging) return;
    setPaging(true);
    setError("");
    paginated.current = true;
    const requestedEvents = ++eventRevision.current;
    try {
      const page = await api.getTokenGuardEvents(cursor);
      if (!alive.current || requestedEvents !== eventRevision.current) return;
      setEvents((previous) => [
        ...new Map(
          [...previous, ...page.items].map((item) => [item.id, item]),
        ).values(),
      ]);
      setCursor(page.next_cursor);
    } catch (e) {
      if (alive.current) fail(e);
    } finally {
      if (alive.current) setPaging(false);
    }
  };
  const config = draft?.config,
    stats = remote?.runtime.stats;
  const accounts = (remote?.accounts ?? []).filter((a) =>
    (a.account_name + " " + a.account_id)
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  const jobState = remote?.runtime.job_state || (busy ? "running" : "idle");
  const jobLabel = t("tokenGuard.jobs." + jobState, { defaultValue: jobState });
  const numberField = (
    key:
      | "interval_seconds"
      | "probe_concurrency"
      | "probe_timeout_seconds"
      | "max_probe_per_cycle"
      | "fail_streak_threshold",
    min: number,
    max: number,
  ) => (
    <label key={key}>
      {t("tokenGuard.fields." + key)}
      <input
        aria-label={t("tokenGuard.fields." + key)}
        type="number"
        required
        min={min}
        max={max}
        step={1}
        value={config?.[key] ?? ""}
        onChange={(e) => patch({ [key]: Number(e.target.value) })}
      />
    </label>
  );
  const toggle = (
    key:
      | "enabled"
      | "auto_relogin"
      | "restore_schedulable"
      | "notify_on_fix"
      | "notify_on_fail",
    hint?: string,
  ) => (
    <label className="ops-check">
      <input
        type="checkbox"
        role={key === "enabled" ? "switch" : undefined}
        checked={Boolean(config?.[key])}
        onChange={(e) => patch({ [key]: e.target.checked })}
      />
      <span>
        <strong>{t("tokenGuard.fields." + key)}</strong>
        {hint && <small>{t("tokenGuard." + hint)}</small>}
      </span>
    </label>
  );
  return (
    <div className="account-ops-workspace token-guard-workspace">
      <SmartOpsNav />
      <header className="ops-heading">
        <div>
          <h2>{t("tokenGuard.title")}</h2>
          <p>{t("tokenGuard.description")}</p>
        </div>
        <div className="ops-actions">
          <Button
            variant="outline"
            disabled={loading || saving}
            onClick={() => void load(true)}
          >
            <RefreshCw size={15} className={loading ? "animate-spin" : ""} />
            {t("tokenGuard.refresh")}
          </Button>
          {busy && remote?.runtime.job_id && (
            <Button
              variant="outline"
              disabled={
                Boolean(action) ||
                Boolean(remote.runtime.cancellation) ||
                jobState === "cancelling"
              }
              onClick={() => void cancel()}
            >
              <Square size={15} />
              {t("tokenGuard.cancel")}
            </Button>
          )}
          <Button
            disabled={
              !remote?.module_enabled ||
              busy ||
              Boolean(action) ||
              loading ||
              saving
            }
            onClick={() => void start(api.runTokenGuard, "run")}
          >
            <Play size={15} />
            {t(busy ? "tokenGuard.running" : "tokenGuard.runNow")}
          </Button>
        </div>
      </header>
      <AccountOpsModule
        enabled={remote?.module_enabled ?? false}
        disabled={!remote}
        onChange={(enabled) => {
          setRemote((r) => (r ? { ...r, module_enabled: enabled } : r));
          void load();
        }}
      />
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
      <p className="ops-hint">{t("tokenGuard.scopeNote")}</p>
      <section className="ops-summary" aria-label={t("tokenGuard.summary")}>
        <span>
          {t("tokenGuard.probed")}
          <strong>{stats?.probed ?? "—"}</strong>
          <small>
            {t("tokenGuard.healthy")}: {stats?.healthy ?? "—"}
          </small>
        </span>
        <span>
          {t("tokenGuard.authFailures")}
          <strong>{stats?.auth_failed ?? "—"}</strong>
          <small>
            {t("tokenGuard.transient")}: {stats?.transient ?? "—"}
          </small>
        </span>
        <span>
          {t("tokenGuard.repaired")}
          <strong>{stats?.repaired ?? "—"}</strong>
          <small>
            {t("tokenGuard.stateFixed")}: {stats?.state_fixed ?? "—"}
          </small>
        </span>
        <span>
          {t("tokenGuard.jobStatus")}
          <strong className="ops-summary-status">{jobLabel}</strong>
          <small>{date(remote?.runtime.last_run ?? undefined)}</small>
        </span>
      </section>
      {remote?.runtime.job_id && (
        <div className="ops-runtime" role="status">
          <span className={"ops-status " + (busy ? "warning" : "")}>
            {jobLabel}
          </span>
          <code>{remote.runtime.job_id}</code>
          <span>{remote.runtime.last_message || t("tokenGuard.waiting")}</span>
        </div>
      )}
      <div className="token-guard-columns">
        <section className="ops-panel">
          <header>
            <h2>
              <ShieldCheck size={18} />
              {t("tokenGuard.configuration")}
            </h2>
            <p>{t("tokenGuard.secretHint")}</p>
          </header>
          {draft && config ? (
            <form onSubmit={(e) => void save(e)} autoComplete="off">
              <fieldset
                className="ops-form"
                disabled={saving || !remote?.module_enabled}
              >
                {toggle("enabled", "enabledHint")}
                <label>
                  {t("tokenGuard.fields.group_ids")}
                  <input
                    aria-label={t("tokenGuard.fields.group_ids")}
                    value={draft.groups}
                    onChange={(e) =>
                      setDraft({ ...draft, groups: e.target.value })
                    }
                  />
                  <small>{t("tokenGuard.groupHint")}</small>
                </label>
                <div className="ops-grid">
                  {numberField("interval_seconds", 30, 86400)}
                  {numberField("probe_concurrency", 1, 16)}
                  {numberField("probe_timeout_seconds", 5, 900)}
                  {numberField("max_probe_per_cycle", 1, 100)}
                  {numberField("fail_streak_threshold", 1, 10)}
                </div>
                <fieldset>
                  <legend>{t("tokenGuard.probeService")}</legend>
                  <label>
                    {t("tokenGuard.fields.probe_endpoint")}
                    <input
                      type="url"
                      aria-label={t("tokenGuard.fields.probe_endpoint")}
                      value={config.probe_endpoint}
                      onChange={(e) =>
                        patch({ probe_endpoint: e.target.value })
                      }
                    />
                  </label>
                  <label>
                    {t("tokenGuard.fields.probe_model")}
                    <input
                      aria-label={t("tokenGuard.fields.probe_model")}
                      value={config.probe_model}
                      onChange={(e) => patch({ probe_model: e.target.value })}
                    />
                    <small>{t("tokenGuard.modelHint")}</small>
                  </label>
                  <label>
                    {t("tokenGuard.fields.probe_headers")}
                    <textarea
                      aria-label={t("tokenGuard.fields.probe_headers")}
                      spellCheck={false}
                      rows={3}
                      value={draft.probeHeaders}
                      onChange={(e) =>
                        setDraft({ ...draft, probeHeaders: e.target.value })
                      }
                    />
                    <small>{t("tokenGuard.headersHint")}</small>
                  </label>
                </fieldset>
                <fieldset>
                  <legend>{t("tokenGuard.reloginService")}</legend>
                  {toggle("auto_relogin", "autoReloginHint")}
                  <label>
                    {t("tokenGuard.fields.relogin_endpoint")}
                    <input
                      type="url"
                      aria-label={t("tokenGuard.fields.relogin_endpoint")}
                      value={config.relogin_endpoint}
                      onChange={(e) =>
                        patch({ relogin_endpoint: e.target.value })
                      }
                    />
                  </label>
                  <label>
                    {t("tokenGuard.fields.relogin_headers")}
                    <textarea
                      aria-label={t("tokenGuard.fields.relogin_headers")}
                      spellCheck={false}
                      rows={3}
                      value={draft.reloginHeaders}
                      onChange={(e) =>
                        setDraft({ ...draft, reloginHeaders: e.target.value })
                      }
                    />
                    <small>{t("tokenGuard.headersHint")}</small>
                  </label>
                  {toggle("restore_schedulable", "restoreHint")}
                </fieldset>
                <fieldset>
                  <legend>{t("tokenGuard.credentials")}</legend>
                  <small>{t("tokenGuard.mappingHint")}</small>
                  {config.relogin_accounts.map((row, index) => (
                    <div className="token-guard-credential" key={index}>
                      <div className="ops-grid">
                        <label>
                          {t("tokenGuard.accountID")}
                          <input
                            aria-label={t("tokenGuard.mappingAccount", {
                              index: index + 1,
                            })}
                            type="number"
                            required
                            min={1}
                            step={1}
                            value={row.account_id || ""}
                            onChange={(e) =>
                              patchMapping(index, {
                                account_id: Number(e.target.value),
                              })
                            }
                          />
                        </label>
                        <label>
                          {t("tokenGuard.email")}
                          <input
                            aria-label={t("tokenGuard.mappingEmail", {
                              index: index + 1,
                            })}
                            type="email"
                            required
                            value={row.email}
                            autoComplete="off"
                            onChange={(e) =>
                              patchMapping(index, { email: e.target.value })
                            }
                          />
                        </label>
                        <label>
                          {t("tokenGuard.password")}
                          <input
                            aria-label={t("tokenGuard.mappingPassword", {
                              index: index + 1,
                            })}
                            type="password"
                            value={row.password}
                            autoComplete="new-password"
                            onChange={(e) =>
                              patchMapping(index, { password: e.target.value })
                            }
                          />
                        </label>
                        <label>
                          {t("tokenGuard.mfa")}
                          <input
                            aria-label={t("tokenGuard.mappingMFA", {
                              index: index + 1,
                            })}
                            type="password"
                            value={row.mfa_secret}
                            autoComplete="new-password"
                            onChange={(e) =>
                              patchMapping(index, {
                                mfa_secret: e.target.value,
                              })
                            }
                          />
                        </label>
                      </div>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        aria-label={t("tokenGuard.removeMapping", {
                          index: index + 1,
                        })}
                        onClick={() =>
                          patch({
                            relogin_accounts: config.relogin_accounts.filter(
                              (_, i) => i !== index,
                            ),
                          })
                        }
                      >
                        <Trash2 size={14} />
                        {t("tokenGuard.remove")}
                      </Button>
                    </div>
                  ))}
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() =>
                      patch({
                        relogin_accounts: [
                          ...config.relogin_accounts,
                          {
                            account_id: 0,
                            email: "",
                            password: "",
                            mfa_secret: "",
                          },
                        ],
                      })
                    }
                  >
                    <Plus size={15} />
                    {t("tokenGuard.addMapping")}
                  </Button>
                </fieldset>
                <fieldset>
                  <legend>{t("tokenGuard.notifications")}</legend>
                  <label>
                    {t("tokenGuard.fields.bark_key")}
                    <input
                      aria-label={t("tokenGuard.fields.bark_key")}
                      type="password"
                      autoComplete="new-password"
                      value={config.bark_key}
                      onChange={(e) => patch({ bark_key: e.target.value })}
                    />
                  </label>
                  {toggle("notify_on_fix")}
                  {toggle("notify_on_fail")}
                </fieldset>
                <footer className="ops-save-row">
                  <small aria-live="polite">
                    {t(dirty ? "tokenGuard.unsaved" : "tokenGuard.saved")}
                  </small>
                  <Button type="submit" disabled={!dirty || saving}>
                    {t(saving ? "tokenGuard.saving" : "tokenGuard.save")}
                  </Button>
                </footer>
              </fieldset>
            </form>
          ) : (
            <p className="ops-empty">
              {t(error ? "tokenGuard.loadFailed" : "tokenGuard.loading")}
            </p>
          )}
        </section>
        <div className="ops-stack">
          <section className="ops-panel">
            <header>
              <h2>
                {t("tokenGuard.accounts")}{" "}
                <span className="ops-count">
                  {remote?.accounts.length ?? 0}
                </span>
              </h2>
              <p>{t("tokenGuard.repairHint")}</p>
            </header>
            <div className="ops-toolbar">
              <input
                aria-label={t("tokenGuard.search")}
                placeholder={t("tokenGuard.search")}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </div>
            <div className="ops-table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("tokenGuard.account")}</th>
                    <th>{t("tokenGuard.dbStatus")}</th>
                    <th>{t("tokenGuard.probe")}</th>
                    <th>{t("tokenGuard.lastFix")}</th>
                    <th>{t("tokenGuard.actions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {accounts.map((row) => (
                    <tr key={row.account_id}>
                      <td>
                        <strong>
                          {row.account_name || "#" + row.account_id}
                        </strong>
                        <small>#{row.account_id}</small>
                      </td>
                      <td>
                        <span
                          className={
                            "ops-status " +
                            (row.account_status === "error" ? "danger" : "")
                          }
                        >
                          {t("tokenGuard.accountStates." + row.account_status, {
                            defaultValue: row.account_status || "—",
                          })}
                        </span>
                        <small>
                          {t("tokenGuard.schedulable")}:{" "}
                          {t(
                            row.schedulable
                              ? "tokenGuard.on"
                              : "tokenGuard.off",
                          )}
                        </small>
                      </td>
                      <td>
                        <span
                          className={
                            "ops-status " +
                            (row.probe_state === "ok"
                              ? "ok"
                              : row.probe_state === "auth"
                                ? "danger"
                                : "warning")
                          }
                        >
                          {t(
                            "tokenGuard.probeStates." +
                              (row.probe_state || "pending"),
                            { defaultValue: row.probe_state },
                          )}
                        </span>
                        <small>{row.probe_detail}</small>
                        <small>
                          {t("tokenGuard.latency")}: {row.latency_ms} ms ·{" "}
                          {t("tokenGuard.streak")}: {row.fail_streak}
                        </small>
                        <small>{date(row.last_probe_at ?? undefined)}</small>
                        {row.needs_relogin && (
                          <small className="ops-danger-text">
                            {t("tokenGuard.needsRelogin")}
                          </small>
                        )}
                      </td>
                      <td>
                        {date(row.last_fix_at ?? undefined)}
                        <small>
                          {t("tokenGuard.actionsMap." + row.last_fix_action, {
                            defaultValue: row.last_fix_action || "—",
                          })}
                        </small>
                        <small>{row.last_fix_result}</small>
                      </td>
                      <td>
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={
                            !remote?.module_enabled ||
                            busy ||
                            Boolean(action) ||
                            saving
                          }
                          onClick={() =>
                            void start(
                              () =>
                                api.reloginTokenGuardAccount(row.account_id),
                              "relogin:" + row.account_id,
                            )
                          }
                        >
                          {t("tokenGuard.relogin")}
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!accounts.length && (
                <p className="ops-empty">
                  {t(search ? "tokenGuard.noMatches" : "tokenGuard.noAccounts")}
                </p>
              )}
            </div>
          </section>
          <section className="ops-panel">
            <header>
              <h2>
                {t("tokenGuard.events")}{" "}
                <span className="ops-count">{events.length}</span>
              </h2>
              <p>{t("tokenGuard.eventsHint")}</p>
            </header>
            <div className="ops-table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("tokenGuard.time")}</th>
                    <th>{t("tokenGuard.account")}</th>
                    <th>{t("tokenGuard.kind")}</th>
                    <th>{t("tokenGuard.detail")}</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((event) => (
                    <tr key={event.id}>
                      <td className="ops-nowrap">{date(event.created_at)}</td>
                      <td>
                        {event.account_name || "—"}
                        {event.account_id > 0 && (
                          <small>#{event.account_id}</small>
                        )}
                      </td>
                      <td>
                        <span
                          className={
                            "ops-status " +
                            (/failed|auth/.test(event.kind)
                              ? "danger"
                              : /ok|fixed/.test(event.kind)
                                ? "ok"
                                : "")
                          }
                        >
                          {t("tokenGuard.eventKinds." + event.kind, {
                            defaultValue: event.kind,
                          })}
                        </span>
                      </td>
                      <td>
                        {event.detail}
                        {event.latency_ms > 0 && (
                          <small>{event.latency_ms} ms</small>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!events.length && (
                <p className="ops-empty">{t("tokenGuard.noEvents")}</p>
              )}
            </div>
            {cursor > 0 && (
              <div className="ops-panel-actions">
                <Button
                  variant="outline"
                  disabled={paging}
                  onClick={() => void more()}
                >
                  {t(paging ? "tokenGuard.loading" : "tokenGuard.loadMore")}
                </Button>
              </div>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}
