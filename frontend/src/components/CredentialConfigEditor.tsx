import { useEffect, useRef, useState } from "react";
import { Play, RefreshCw, Save } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import {
  credentialAPI,
  type CredentialConfig,
  type CredentialConfigInput,
  type CredentialEngine,
  type CredentialMode,
} from "@/lib/credentialOps";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select } from "./ui/select";
import { Switch } from "./ui/switch";

export default function CredentialConfigEditor({
  accountId,
  email: initialEmail = "",
  studioConfigured,
  globalEngine = "local_worker",
  onSaved,
}: {
  accountId: number;
  email?: string;
  studioConfigured: boolean;
  globalEngine?: CredentialEngine | "";
  onSaved?: () => void;
}) {
  const { t } = useTranslation();
  const [config, setConfig] = useState<CredentialConfig | null>(null);
  const [email, setEmail] = useState(initialEmail);
  const [mode, setMode] = useState<CredentialMode>("password_totp");
  const [engine, setEngine] = useState<CredentialEngine>("inherit");
  const [proxy, setProxy] = useState("account");
  const [proxies, setProxies] = useState<Array<{ id: number; label: string }>>(
    [],
  );
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [otp, setOTP] = useState("");
  const [clearTotp, setClearTotp] = useState(false);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [revisionReady, setRevisionReady] = useState(true);
  const remote =
    mode === "password_totp" &&
    (engine === "session_studio" ||
      (engine === "inherit" && globalEngine === "session_studio"));
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    let current = true;
    void credentialAPI
      .config(accountId)
      .then((value) => {
        if (!current) return;
        setConfig(value);
        setEmail(value.email);
        setMode(value.mode);
        setEngine(value.engine || "inherit");
        setProxy(
          value.proxy_id
            ? String(value.proxy_id)
            : value.proxy_source || "account",
        );
      })
      .catch((cause) => {
        if (current && cause?.status !== 404)
          setError(t("credentialOps.configLoadFailed"));
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    void api
      .listProxies()
      .then((value) => {
        if (current)
          setProxies(
            value.proxies
              .filter((p) => p.enabled)
              .map((p) => ({ id: p.id, label: p.label || `#${p.id}` })),
          );
      })
      .catch(() => {});
    return () => {
      live.current = false;
      current = false;
    };
  }, [accountId, t]);
  async function submit(login: boolean) {
    let queued = false;
    setBusy(true);
    setError("");
    setNotice("");
    const input: CredentialConfigInput = {
      email,
      mode,
      engine: mode === "email_otp_url" ? "local_worker" : engine,
      proxy_source: remote ? "direct" : /^\d+$/.test(proxy) ? "proxy" : proxy,
      proxy_id: !remote && /^\d+$/.test(proxy) ? Number(proxy) : null,
      password,
      totp_secret: clearTotp ? "" : totp,
      otp_url: otp,
      clear_totp: clearTotp,
      revision: config?.revision,
    };
    try {
      if (login) {
        await credentialAPI.login(accountId, input);
        queued = true;
        if (!live.current) return;
        setPassword("");
        setTotp("");
        setOTP("");
        setRevisionReady(false);
        const value = await credentialAPI.config(accountId);
        if (live.current) {
          setConfig(value);
          setRevisionReady(true);
        }
      } else {
        const value = await credentialAPI.saveConfig(accountId, input);
        if (live.current) setConfig(value);
      }
      if (!live.current) return;
      setPassword("");
      setTotp("");
      setOTP("");
      setClearTotp(false);
      setNotice(t(`credentialOps.${login ? "loginQueued" : "configSaved"}`));
      onSaved?.();
    } catch (cause) {
      if (live.current)
        setError(
          queued
            ? t("credentialOps.configReloadFailed")
            : cause instanceof Error
              ? cause.message
              : t("credentialOps.configSaveFailed"),
        );
    } finally {
      if (live.current) setBusy(false);
    }
  }
  return (
    <section className="space-y-4">
      {loading ? (
        <p role="status" className="text-sm text-muted-foreground">
          {t("credentialOps.loading")}
        </p>
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-1.5 text-sm">
              <span>{t("credentialOps.email")}</span>
              <Input
                value={email}
                type="email"
                onChange={(event) => setEmail(event.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </label>
            <label className="space-y-1.5 text-sm">
              <span>{t("credentialOps.mode")}</span>
              <Select
                value={mode}
                onValueChange={(value) => setMode(value as CredentialMode)}
                options={[
                  {
                    value: "password_totp",
                    label: t("credentialOps.passwordMode"),
                  },
                  {
                    value: "email_otp_url",
                    label: t("credentialOps.emailMode"),
                  },
                ]}
              />
            </label>
            <label className="space-y-1.5 text-sm">
              <span>{t("credentialOps.engine")}</span>
              <Select
                aria-label={t("credentialOps.engine")}
                value={mode === "email_otp_url" ? "local_worker" : engine}
                onValueChange={(value) => {
                  setEngine(value as CredentialEngine);
                }}
                disabled={mode === "email_otp_url"}
                options={[
                  { value: "inherit", label: t("credentialOps.inheritEngine") },
                  {
                    value: "local_worker",
                    label: t("credentialOps.localWorker"),
                  },
                  ...(studioConfigured
                    ? [{ value: "session_studio", label: "Session Studio" }]
                    : []),
                ]}
              />
            </label>
            <label className="space-y-1.5 text-sm">
              <span>{t("credentialOps.proxy")}</span>
              <Select
                aria-label={t("credentialOps.proxy")}
                value={remote ? "remote" : proxy}
                onValueChange={setProxy}
                disabled={remote}
                options={[
                  ...(remote
                    ? [
                        {
                          value: "remote",
                          label: t("credentialOps.remoteEgress"),
                        },
                      ]
                    : []),
                  { value: "account", label: t("credentialOps.accountProxy") },
                  { value: "direct", label: t("credentialOps.direct") },
                  { value: "mihomo", label: t("credentialOps.nodePool") },
                  ...proxies.map((p) => ({
                    value: String(p.id),
                    label: p.label,
                  })),
                ]}
              />
            </label>
            {mode === "password_totp" ? (
              <>
                <label className="space-y-1.5 text-sm">
                  <span>{t("credentialOps.password")}</span>
                  <Input
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    autoComplete="new-password"
                    placeholder={
                      config?.password_configured
                        ? t("credentialOps.keepSecret")
                        : ""
                    }
                  />
                </label>
                <label className="space-y-1.5 text-sm">
                  <span>{t("credentialOps.totp")}</span>
                  <Input
                    type="password"
                    value={totp}
                    onChange={(event) => setTotp(event.target.value)}
                    autoComplete="off"
                    placeholder={
                      config?.totp_configured
                        ? t("credentialOps.keepSecret")
                        : ""
                    }
                    spellCheck={false}
                    disabled={clearTotp}
                  />
                </label>
              </>
            ) : (
              <label className="space-y-1.5 text-sm sm:col-span-2">
                <span>{t("credentialOps.otpURL")}</span>
                <Input
                  type="password"
                  value={otp}
                  onChange={(event) => setOTP(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder={
                    config?.otp_url_configured
                      ? t("credentialOps.keepSecret")
                      : "https://"
                  }
                />
              </label>
            )}
          </div>
          {mode === "password_totp" && config?.totp_configured && (
            <label className="flex items-center gap-2 text-sm">
              <Switch
                checked={clearTotp}
                onCheckedChange={(value) => {
                  setClearTotp(value);
                  if (value) setTotp("");
                }}
              />
              {t("credentialOps.clearTotp")}
            </label>
          )}
          <p className="text-xs leading-relaxed text-muted-foreground">
            {remote
              ? t("credentialOps.remoteHint")
              : t("credentialOps.fixedEgressHint")}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={busy || !email || !revisionReady}
              onClick={() => void submit(false)}
            >
              {busy ? (
                <RefreshCw className="size-4 animate-spin" />
              ) : (
                <Save className="size-4" />
              )}
              {t("credentialOps.saveConfig")}
            </Button>
            <Button
              disabled={busy || !email || !revisionReady}
              onClick={() => void submit(true)}
            >
              <Play className="size-4" />
              {t("credentialOps.saveAndLogin")}
            </Button>
          </div>
        </>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {!revisionReady && (
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => {
            setBusy(true);
            void credentialAPI
              .config(accountId)
              .then((value) => {
                if (live.current) {
                  setConfig(value);
                  setRevisionReady(true);
                  setError("");
                }
              })
              .catch(() => {
                if (live.current)
                  setError(t("credentialOps.configReloadFailed"));
              })
              .finally(() => {
                if (live.current) setBusy(false);
              });
          }}
        >
          <RefreshCw className="size-4" />
          {t("credentialOps.reloadConfig")}
        </Button>
      )}
      {notice && (
        <p role="status" className="text-sm text-muted-foreground">
          {notice}
        </p>
      )}
    </section>
  );
}
