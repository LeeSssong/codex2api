import {
  useCallback,
  useEffect,
  useState,
  useRef,
  type FormEvent,
} from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { Button } from "../components/ui/button";
import {
  date,
  type AccountOpsSettings,
  type AccountOpsEvent,
} from "../lib/accountOps";
import "./account-ops.css";
export function AccountOpsModule({
  enabled,
  onChange,
}: {
  enabled: boolean;
  onChange: (v: boolean) => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <section className="ops-module">
      <label className="ops-check">
        <input
          type="checkbox"
          role="switch"
          aria-label="启用账号运维内置模块"
          checked={enabled}
          disabled={busy}
          onChange={(e) => {
            const value = e.target.checked;
            setBusy(true);
            setError("");
            void api
              .saveAccountOpsModule(value)
              .then((r) => onChange(r.enabled))
              .catch((e) => setError(e.message))
              .finally(() => setBusy(false));
          }}
        />
        <span>
          <strong>账号运维内置模块</strong>
          <small>启用后可配置降智运维规则和账号邮件告警。</small>
        </span>
      </label>
      {error && <p role="alert">{error}</p>}
    </section>
  );
}
export default function AccountOps() {
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
      setNotice("告警与 SMTP 配置已保存");
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
    pending: "待发送",
    sending: "发送中",
    sent: "已发送",
    failed: "发送失败",
    suppressed: "已抑制",
  };
  return (
    <div className="account-ops-workspace">
      <nav className="ops-tabs">
        <Link to="/quality-ops">降智运维</Link>
        <Link className="active" to="/account-ops">
          账号告警
        </Link>
      </nav>
      <header className="ops-heading">
        <div>
          <h1>账号告警</h1>
          <p>观察上游失败响应，提醒余额不足或周额度已用尽。</p>
        </div>
        <Button variant="outline" onClick={() => void load()}>
          刷新
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
            <h2>邮件告警</h2>
            <p>每个账号、每类信号分别合并与冷却</p>
          </header>
          {draft ? (
            <form onSubmit={(e) => void save(e)}>
              <fieldset className="ops-form" disabled={!enabled || busy}>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    role="switch"
                    aria-label="启用邮件告警"
                    checked={draft.config.enabled}
                    onChange={(e) => patch({ enabled: e.target.checked })}
                  />
                  启用邮件告警
                </label>
                <label>
                  收件邮箱
                  <input
                    type="email"
                    aria-label="收件邮箱"
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
                  余额不足
                </label>
                <label className="ops-check">
                  <input
                    type="checkbox"
                    checked={draft.config.weekly_quota}
                    onChange={(e) => patch({ weekly_quota: e.target.checked })}
                  />
                  周额度已用尽
                </label>
                <label>
                  冷却时间（分钟）
                  <input
                    aria-label="冷却时间（分钟）"
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
                <small>
                  仅配置一个收件人；默认 60 分钟，支持 5–1440 分钟。
                </small>
                <fieldset>
                  <legend>SMTP 邮件设置</legend>
                  <label>
                    SMTP 主机
                    <input
                      aria-label="SMTP 主机"
                      required={draft.config.enabled}
                      value={draft.smtp.host}
                      onChange={(e) => smtp({ host: e.target.value })}
                    />
                  </label>
                  <div className="ops-grid">
                    <label>
                      端口
                      <input
                        aria-label="SMTP 端口"
                        type="number"
                        min={1}
                        max={65535}
                        value={draft.smtp.port}
                        onChange={(e) => smtp({ port: Number(e.target.value) })}
                      />
                    </label>
                    <label>
                      传输加密
                      <select
                        aria-label="SMTP 传输加密"
                        value={draft.smtp.tls_mode}
                        onChange={(e) =>
                          smtp({
                            tls_mode: e.target.value as "tls" | "starttls",
                          })
                        }
                      >
                        <option value="starttls">STARTTLS</option>
                        <option value="tls">TLS</option>
                      </select>
                    </label>
                  </div>
                  <label>
                    发件邮箱
                    <input
                      aria-label="发件邮箱"
                      type="email"
                      required={draft.config.enabled}
                      value={draft.smtp.from}
                      onChange={(e) => smtp({ from: e.target.value })}
                    />
                  </label>
                  <label>
                    用户名
                    <input
                      autoComplete="off"
                      aria-label="SMTP 用户名"
                      value={draft.smtp.username}
                      onChange={(e) => smtp({ username: e.target.value })}
                    />
                  </label>
                  <label>
                    密码
                    <input
                      aria-label="SMTP 密码"
                      type="password"
                      autoComplete="new-password"
                      placeholder={
                        draft.smtp.password_configured
                          ? "已配置；留空保留"
                          : "输入 SMTP 密码"
                      }
                      value={draft.smtp.password || ""}
                      onChange={(e) => smtp({ password: e.target.value })}
                    />
                  </label>
                </fieldset>
                <Button type="submit">
                  {busy ? "保存中…" : "保存告警配置"}
                </Button>
              </fieldset>
            </form>
          ) : (
            <p className="ops-empty">加载中…</p>
          )}
        </section>
        <section className="ops-panel">
          <header>
            <h2>告警事件</h2>
            <p>
              {enabled && remote?.config.enabled ? "正在观察" : "已停用"} ·
              不保存原始错误或凭据
            </p>
          </header>
          <div className="ops-toolbar">
            <input
              aria-label="搜索告警账号"
              placeholder="搜索账号"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <select
              aria-label="告警类型"
              value={kind}
              onChange={(e) => setKind(e.target.value)}
            >
              <option value="all">全部类型</option>
              <option value="balance_low">余额不足</option>
              <option value="weekly_quota">周额度已用尽</option>
            </select>
          </div>
          {!!(remote?.dropped || remote?.failures) && (
            <p role="alert" className="ops-error">
              信号队列丢弃 {remote?.dropped} 次，处理失败 {remote?.failures}{" "}
              次。
            </p>
          )}
          <div className="ops-table-scroll">
            <table>
              <thead>
                <tr>
                  <th>账号</th>
                  <th>失败类型</th>
                  <th>最近触发</th>
                  <th>邮件状态</th>
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
                          #{e.account_id} · {e.occurrences} 次
                        </small>
                      </td>
                      <td>
                        {e.kind === "balance_low" ? "余额不足" : "周额度已用尽"}
                        <small>HTTP {e.http_status}</small>
                      </td>
                      <td>
                        {date(e.last_seen)}
                        <small>首次 {date(e.first_seen)}</small>
                      </td>
                      <td>
                        {states[e.state] || e.state}
                        <small>
                          {e.last_sent_at ? date(e.last_sent_at) : "尚未发送"}
                        </small>
                        {e.state === "failed" && (
                          <small>
                            {e.attempts < 3
                              ? "下次重试"
                              : "重试已停止，后续信号可重新提醒"}{" "}
                            {date(e.next_send_at)}
                          </small>
                        )}
                      </td>
                    </tr>
                  ))}
              </tbody>
            </table>
            {!events.length && <p className="ops-empty">尚无告警事件</p>}
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
              加载更多
            </Button>
          )}
        </section>
      </div>
      <p className="ops-scope">
        此提醒基于明确的失败信号，不代表已查询到准确余额，不会自动修改账号。普通限流、无可用账号及泛化额度错误不会触发此告警。
      </p>
    </div>
  );
}
