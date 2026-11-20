import { useEffect, useId, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { request } from "../api";
import { credentialAPI, type CredentialJob } from "../lib/credentialOps";
import { Button } from "./ui/button";
import { Textarea } from "./ui/textarea";

interface SIWCState {
  bound: boolean;
  enabled: boolean;
  status: "ready" | "not_bound" | "reauthorization_required" | "refresh_pending";
}
interface Authorization { session_id: string; auth_url: string }

export default function SIWCAccountPanel({ accountId }: { accountId: number }) {
  const { t } = useTranslation();
  const callbackId = useId();
  const [state, setState] = useState<SIWCState | null>(null);
  const [session, setSession] = useState<Authorization | null>(null);
  const [callback, setCallback] = useState("");
  const [job, setJob] = useState<CredentialJob | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirm, setConfirm] = useState(false);
  const endpoint = `/accounts/${accountId}/siwc`;
  const load = async () => setState(await request<SIWCState>(endpoint));

  useEffect(() => {
    const abort = new AbortController();
    void request<SIWCState>(endpoint, { signal: abort.signal }).then(setState).catch(() => {
      if (!abort.signal.aborted) setError(t("siwc.loadFailed"));
    });
    return () => abort.abort();
  }, [endpoint, t]);

  useEffect(() => {
    if (!job || !["queued", "running"].includes(job.Status)) return;
    let active = true;
    const timer = setTimeout(() => {
      void credentialAPI.job(job.ID).then(async current => {
        if (!active) return;
        setJob(current);
        if (current.Status === "succeeded") {
          const updated = await request<SIWCState>(endpoint);
          if (!active) return;
          setState(updated);
          setNotice(t("siwc.saved"));
        } else if (["failed", "cancelled"].includes(current.Status)) setError(t("siwc.failed"));
      }).catch(() => { if (active) { setError(t("siwc.loadFailed")); setJob(previous => previous ? { ...previous } : null); } });
    }, 2000);
    return () => { active = false; clearTimeout(timer); };
  }, [job, endpoint, t]);

  const run = async (action: () => Promise<void>) => {
    setBusy(true); setError(""); setNotice("");
    try { await action(); } catch (failure) { setError(failure instanceof Error ? failure.message : t("siwc.failed")); }
    finally { setBusy(false); }
  };
  const pending = busy || !!job && ["queued", "running"].includes(job.Status);

  return <section className="space-y-3 border-t border-border pt-5" aria-label={t("siwc.title")}>
    <h3 className="font-semibold text-foreground">{t("siwc.title")}</h3>
    <p className="text-sm text-muted-foreground">{t("siwc.description")}</p>
    <div role="status" className="text-sm">
      {state ? <><p>{t(`siwc.${state.status}`)}</p><p>{t("siwc.active", { protocol: state.enabled ? "SIWC" : "Codex" })}</p></> : t("siwc.loading")}
      {notice && <p>{notice}</p>}
      {job && <p>{t("siwc.task", { status: job.Status })}</p>}
    </div>
    {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
    <div className="flex flex-wrap gap-2">
      <Button type="button" variant="outline" disabled={pending} onClick={() => void run(async () => {
        setSession(await request<Authorization>(`${endpoint}/authorize`, { method: "POST" })); setCallback("");
      })}>{t("siwc.browser")}</Button>
      <Button type="button" variant="outline" disabled={pending} onClick={() => void run(async () => {
        setSession(null); setCallback("");
        setJob(await credentialAPI.login(accountId, { oauth_profile: "siwc", mode: "password_totp", engine: "local_worker", proxy_source: "account" }));
      })}>{t("siwc.password")}</Button>
      <Button type="button" variant="ghost" disabled={busy} onClick={() => void run(load)}>{t("siwc.retry")}</Button>
    </div>
    <Link className="text-sm underline underline-offset-4" to="/credential-ops">{t("siwc.configure")}</Link>
    {session && <div className="space-y-3">
      <p className="text-sm text-muted-foreground">{t("siwc.hint")}</p>
      <a className="inline-block text-sm underline underline-offset-4" href={session.auth_url} target="_blank" rel="noopener noreferrer">{t("siwc.open")}</a>
      <label htmlFor={callbackId} className="block text-sm">{t("siwc.callback")}</label>
      <Textarea id={callbackId} value={callback} onChange={event => setCallback(event.target.value)} autoComplete="off" spellCheck={false} />
      <Button type="button" disabled={pending || !callback.trim()} onClick={() => void run(async () => {
        setState(await request<SIWCState>(`${endpoint}/callback`, { method: "POST", body: JSON.stringify({ session_id: session.session_id, callback: callback.trim() }) }));
        setSession(null); setCallback(""); setNotice(t("siwc.saved"));
      })}>{t("siwc.complete")}</Button>
    </div>}
    {state && <div className="flex flex-wrap gap-2">
      <Button type="button" disabled={pending || (!state.enabled && state.status !== "ready")} onClick={() => void run(async () => {
        setState(await request<SIWCState>(`${endpoint}/protocol`, { method: "PUT", body: JSON.stringify({ enabled: !state.enabled }) }));
      })}>{t(state.enabled ? "siwc.native" : "siwc.use")}</Button>
      {state.bound && !confirm && <Button type="button" variant="outline" disabled={pending} onClick={() => setConfirm(true)}>{t("siwc.revoke")}</Button>}
      {confirm && <><Button type="button" variant="destructive" disabled={pending} onClick={() => void run(async () => {
        setState(await request<SIWCState>(endpoint, { method: "DELETE" })); setConfirm(false);
      })}>{t("siwc.confirm")}</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => setConfirm(false)}>{t("siwc.cancel")}</Button></>}
    </div>}
    <p className="text-xs text-muted-foreground">{t("siwc.unverified")}</p>
  </section>;
}
