import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, request } from "../api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import SIWCAccountPanel from "./SIWCAccountPanel";

export default function NewSIWCAccount({ onSaved }: { onSaved: () => void }) {
  const { t } = useTranslation();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [proxyId, setProxyId] = useState("0");
  const [proxies, setProxies] = useState<Array<{ id: number; label: string }>>([]);
  const [id, setId] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let active = true;
    void api.listProxies().then(value => { if (active) setProxies(value.proxies.filter(p => p.enabled).map(p => ({ id: p.id, label: p.label || `#${p.id}` }))); }).catch(() => { if (active) setError(t("siwc.loadFailed")); });
    return () => { active = false; };
  }, [t]);
  if (id) return <div className="space-y-3"><p className="text-sm text-muted-foreground">{t("siwc.enableAfterAuthorization")}</p><SIWCAccountPanel accountId={id} /></div>;
  return <div className="space-y-4">
    <p className="text-sm text-muted-foreground">{t("siwc.standalone")}</p>
    <label className="block space-y-1"><span>{t("siwc.email")}</span><Input type="email" autoComplete="username" value={email} onChange={e => setEmail(e.target.value)} /></label>
    <label className="block space-y-1"><span>{t("siwc.passwordOptional")}</span><Input type="password" autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} /></label>
    <label className="block space-y-1"><span>{t("siwc.totpOptional")}</span><Input type="password" autoComplete="off" value={totp} onChange={e => setTotp(e.target.value)} /></label>
    <label className="block space-y-1"><span>{t("siwc.egress")}</span><select className="w-full rounded-md border border-input bg-background p-2" value={proxyId} onChange={e => setProxyId(e.target.value)}><option value="0">{t("siwc.direct")}</option>{proxies.map(p => <option key={p.id} value={p.id}>{p.label}</option>)}</select></label>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    <Button type="button" disabled={busy || !email.trim()} onClick={() => void (async () => {
      setBusy(true); setError("");
      try { const result = await request<{ id: number }>("/accounts/siwc", { method: "POST", body: JSON.stringify({ email: email.trim(), password, totp_secret: totp, proxy_id: Number(proxyId) }) }); setPassword(""); setTotp(""); setId(result.id); onSaved(); }
      catch (failure) { setError(failure instanceof Error ? failure.message : t("siwc.failed")); }
      finally { setBusy(false); }
    })()}>{t("siwc.create")}</Button>
  </div>;
}
