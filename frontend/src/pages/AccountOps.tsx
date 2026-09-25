import { useTranslation } from "react-i18next";
import {
  useCallback,
  useEffect,
  useState,
  useRef,
  type FormEvent,
} from "react";
import SmartOpsNav from "../components/SmartOpsNav";
import { api } from "../api";
import { Button } from "../components/ui/button";
import { Select } from "../components/ui/select";
import {
  date,
  type AccountOpsSettings,
  type AccountOpsEvent,
} from "../lib/accountOps";
import "./account-ops.css";
export function AccountOpsModule({
  enabled,
  onChange,
  disabled = false,
}: {
  enabled: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false),
    [pendingValue, setPendingValue] = useState<boolean | null>(null),
    [error, setError] = useState("");
  return (
    <section className="ops-module">
      <label className="ops-check">
        <input
          type="checkbox"
          role="switch"
          aria-label={t("smartOps.copy.moduleSwitch")}
          checked={pendingValue ?? enabled}
          disabled={busy || disabled}
          onChange={(e) => {
            const value = e.target.checked;
            setPendingValue(value);
            setBusy(true);
            setError("");
            void api
              .saveAccountOpsModule(value)
              .then((r) => onChange(r.enabled))
              .catch((e) => setError(e.message))
              .finally(() => {
                setPendingValue(null);
                setBusy(false);
              });
          }}
        />
        <span>
          <strong>{t("smartOps.copy.moduleName")}</strong>
          <small>{t("smartOps.copy.moduleHint")}</small>
        </span>
      </label>
      {error && <p role="alert">{error}</p>}
    </section>
  );
}
export default function AccountOps() {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(false),
    [remote, setRemote] = useState<AccountOpsSettings | null>(null),
    [draft, setDraft] = useState<AccountOpsSettings | null>(null),
    [events, setEvents] = useState<AccountOpsEvent[]>([]),
    [more, setMore] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false),
    [search, setSearch] = useState(""),
    [kind, setKind] = useState("all");
  const fail = (e: unknown) =>
    setError(e instanceof Error ? e.message : String(e));
  const paginated = useRef(false);
  const load = useCallback(async () => {
    try {
      const [m, c, e] = await Promise.all([
        api.getAccountOpsModule(),
        api.getAccountOpsSettings(),
        api.getAccountOpsAlerts(),
      ]);
      setEnabled(m.enabled);
      setRemote(c);
      setDraft((d) => d || c);
      setEvents(e.items);
      setMore(e.items.length === 100);
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
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!draft) return;
    setBusy(true);
    setError("");
    try {
      const result = await api.saveAccountOpsSettings(draft);
      setRemote(result);
      setDraft(result);
      setNotice(t("smartOps.copy.alertsSaved"));
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };
  const patch = (v: Partial<AccountOpsSettings["config"]>) =>
    setDraft((d) => (d ? { ...d, config: { ...d.config, ...v } } : d));
  const smtp = (v: Partial<AccountOpsSettings["smtp"]>) =>
    setDraft((d) => (d ? { ...d, smtp: { ...d.smtp, ...v } } : d));
  const states: Record<string, string> = {
    pending: t("smartOps.copy.pending"),
    sending: t("smartOps.copy.sending"),
    sent: t("smartOps.copy.sent"),
    failed: t("smartOps.copy.sendFailed"),
    suppressed: t("smartOps.copy.suppressed"),
  };
  const kinds: Record<string, string> = {
    balance_low: t("smartOps.copy.balanceLow"),
    weekly_quota: t("smartOps.copy.weeklyQuota"),
    quality_degraded: t("smartOps.copy.qualityAction"),
    quality_restored: t("smartOps.copy.qualityRestored"),
  };
  return (
    <div className="account-ops-workspace">
      <SmartOpsNav />
      <header className="ops-heading">
        <div>
          <h2>{t("smartOps.copy.alerts")}</h2>
          <p>{t("smartOps.copy.alertsDescription")}</p>
        </div>
        <Button variant="outline" onClick={() => void load()}>
          {t("smartOps.copy.refresh")}
        </Button>
      </header>
      <AccountOpsModule
        enabled={enabled}
        onChange={(v) => {
          setEnabled(v);
          if (!v) patch({ enabled: false });
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
      <div className="ops-columns">
        <section className="ops-panel">
          <header>
            <h2>{t("smartOps.copy.emailAlerts")}</h2>
            <p>{t("smartOps.copy.cooldownHint")}</p>
          </header>
          {draft ? (
            <form onSubmit={(e) => void save(e)}>
              <fieldset className="ops-form" disabled={!enabled || busy}>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    role="switch"
                    aria-label={t("smartOps.copy.enableAlerts")}
                    checked={draft.config.enabled}
                    onChange={(e) => patch({ enabled: e.target.checked })}
                  />
                  {t("smartOps.copy.enableAlerts")}
                </label>
                <label>
                  {t("smartOps.copy.recipient")}
                  <input
                    type="email"
                    aria-label={t("smartOps.copy.recipient")}
                    required={draft.config.enabled}
                    maxLength={254}
                    value={draft.config.recipient}
                    onChange={(e) => patch({ recipient: e.target.value })}
                  />
                </label>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={draft.config.balance_low}
                    onChange={(e) => patch({ balance_low: e.target.checked })}
                  />
                  {t("smartOps.copy.balanceLow")}
                </label>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={draft.config.weekly_quota}
                    onChange={(e) => patch({ weekly_quota: e.target.checked })}
                  />
                  {t("smartOps.copy.weeklyQuota")}
                </label>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={draft.config.quality_degraded}
                    onChange={(e) =>
                      patch({ quality_degraded: e.target.checked })
                    }
                  />
                  {t("smartOps.copy.qualityAction")}
                </label>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={draft.config.quality_restored}
                    onChange={(e) =>
                      patch({ quality_restored: e.target.checked })
                    }
                  />
                  {t("smartOps.copy.qualityRestored")}
                </label>
                <label>
                  {t("smartOps.copy.cooldown")}
                  <input
                    aria-label={t("smartOps.copy.cooldown")}
                    required
                    type="number"
                    min={5}
                    max={1440}
                    value={draft.config.cooldown_minutes}
                    onChange={(e) =>
                      patch({ cooldown_minutes: Number(e.target.value) })
                    }
                  />
                </label>
                <small>{t("smartOps.copy.recipientHint")}</small>
                <fieldset>
                  <legend>{t("smartOps.copy.smtpSettings")}</legend>
                  <label>
                    {t("smartOps.copy.smtpHost")}
                    <input
                      aria-label={t("smartOps.copy.smtpHost")}
                      required={draft.config.enabled}
                      value={draft.smtp.host}
                      onChange={(e) => smtp({ host: e.target.value })}
                    />
                  </label>
                  <div className="ops-grid">
                    <label>
                      {t("smartOps.copy.port")}
                      <input
                        aria-label={t("smartOps.copy.smtpPort")}
                        type="number"
                        min={1}
                        max={65535}
                        value={draft.smtp.port}
                        onChange={(e) => smtp({ port: Number(e.target.value) })}
                      />
                    </label>
                    <label>
                      {t("smartOps.copy.encryption")}
                      <Select
                        aria-label={t("smartOps.copy.smtpEncryption")}
                        value={draft.smtp.tls_mode}
                        options={[
                          { value: "starttls", label: "STARTTLS" },
                          { value: "tls", label: "TLS" },
                        ]}
                        onValueChange={(value) =>
                          smtp({ tls_mode: value as "tls" | "starttls" })
                        }
                      />
                    </label>
                  </div>
                  <label>
                    {t("smartOps.copy.from")}
                    <input
                      aria-label={t("smartOps.copy.from")}
                      type="email"
                      required={draft.config.enabled}
                      value={draft.smtp.from}
                      onChange={(e) => smtp({ from: e.target.value })}
                    />
                  </label>
                  <label>
                    {t("smartOps.copy.username")}
                    <input
                      autoComplete="off"
                      aria-label={t("smartOps.copy.smtpUsername")}
                      value={draft.smtp.username}
                      onChange={(e) => smtp({ username: e.target.value })}
                    />
                  </label>
                  <label>
                    {t("smartOps.copy.password")}
                    <input
                      aria-label={t("smartOps.copy.smtpPassword")}
                      type="password"
                      autoComplete="new-password"
                      placeholder={
                        draft.smtp.password_configured
                          ? t("smartOps.copy.passwordStored")
                          : t("smartOps.copy.passwordPlaceholder")
                      }
                      value={draft.smtp.password || ""}
                      onChange={(e) => smtp({ password: e.target.value })}
                    />
                  </label>
                </fieldset>
                <Button type="submit">
                  {busy
                    ? t("smartOps.copy.saving")
                    : t("smartOps.copy.saveAlerts")}
                </Button>
              </fieldset>
            </form>
          ) : (
            <p className="ops-empty">{t("smartOps.copy.loading")}</p>
          )}
        </section>
        <section className="ops-panel">
          <header>
            <h2>{t("smartOps.copy.alertEvents")}</h2>
            <p>
              {enabled && remote?.config.enabled
                ? t("smartOps.copy.observing")
                : t("smartOps.copy.disabled")}{" "}
              · {t("smartOps.noRawErrors")}
            </p>
          </header>
          <div className="ops-toolbar">
            <input
              aria-label={t("smartOps.copy.searchAlertAccounts")}
              placeholder={t("smartOps.copy.searchAccounts")}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <Select
              aria-label={t("smartOps.copy.alertType")}
              value={kind}
              options={[
                { value: "all", label: t("smartOps.copy.allTypes") },
                { value: "balance_low", label: t("smartOps.copy.balanceLow") },
                {
                  value: "weekly_quota",
                  label: t("smartOps.copy.weeklyQuota"),
                },
                {
                  value: "quality_degraded",
                  label: t("smartOps.copy.qualityAction"),
                },
                {
                  value: "quality_restored",
                  label: t("smartOps.copy.qualityRestored"),
                },
              ]}
              onValueChange={setKind}
            />
          </div>
          {!!(remote?.dropped || remote?.failures) && (
            <p role="alert" className="ops-error">
              {t("smartOps.droppedSignals", {
                dropped: remote?.dropped,
                failures: remote?.failures,
              })}
            </p>
          )}
          <div className="ops-table-scroll">
            <table>
              <thead>
                <tr>
                  <th>{t("smartOps.copy.account")}</th>
                  <th>{t("smartOps.copy.alertType")}</th>
                  <th>{t("smartOps.copy.latestSignal")}</th>
                  <th>{t("smartOps.copy.mailStatus")}</th>
                </tr>
              </thead>
              <tbody>
                {events
                  .filter(
                    (e) =>
                      (kind === "all" || kind === e.kind) &&
                      `${e.account_name} ${e.account_id}`
                        .toLowerCase()
                        .includes(search.toLowerCase()),
                  )
                  .map((e) => (
                    <tr key={`${e.account_id}:${e.kind}`}>
                      <td>
                        {e.account_name || `#${e.account_id}`}
                        <small>
                          #{e.account_id} · {e.occurrences}{" "}
                          {t("smartOps.copy.occurrences")}
                        </small>
                      </td>
                      <td>
                        {kinds[e.kind] || e.kind}
                        <small>
                          {e.kind.startsWith("quality_")
                            ? e.signal === "groups_removed"
                              ? t("smartOps.copy.groupsRemoved")
                              : e.signal === "scheduling_disabled"
                                ? t("smartOps.copy.schedulingDisabled")
                                : t("smartOps.copy.restored")
                            : `HTTP ${e.http_status}`}
                        </small>
                      </td>
                      <td>
                        {date(e.last_seen)}
                        <small>
                          {t("smartOps.copy.firstSeen")}
                          {date(e.first_seen)}
                        </small>
                      </td>
                      <td>
                        {states[e.state] || e.state}
                        <small>
                          {e.last_sent_at
                            ? date(e.last_sent_at)
                            : t("smartOps.copy.notSent")}
                        </small>
                        {e.state === "failed" && (
                          <small>
                            {e.attempts < 3
                              ? t("smartOps.copy.nextRetry")
                              : t("smartOps.copy.retryStopped")}{" "}
                            {date(e.next_send_at)}
                          </small>
                        )}
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
            {!events.length && (
              <p className="ops-empty">{t("smartOps.copy.noAlerts")}</p>
            )}
          </div>
          {more && (
            <Button
              variant="ghost"
              onClick={() =>
                void api
                  .getAccountOpsAlerts(events.length)
                  .then((p) => {
                    paginated.current = true;
                    setEvents((es) => [
                      ...new Map(
                        [...es, ...p.items].map((e) => [
                          `${e.account_id}:${e.kind}`,
                          e,
                        ]),
                      ).values(),
                    ]);
                    setMore(p.items.length === 100);
                  })
                  .catch(fail)
              }
            >
              {t("smartOps.copy.loadMore")}
            </Button>
          )}
        </section>
      </div>
      <p className="ops-scope">{t("smartOps.copy.alertScope")}</p>
    </div>
  );
}
