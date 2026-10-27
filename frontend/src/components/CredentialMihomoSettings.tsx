import { useEffect, useState } from "react";
import { Download, Save } from "lucide-react";
import { useTranslation } from "react-i18next";
import { credentialRequest } from "@/lib/credentialOps";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { DraftNumberInput } from "./ui/draft-number-input";

interface PoolStatus {
  configured: boolean;
  installed: boolean;
  node_count: number;
  nodes: string[];
  active: number;
  concurrency: number;
  binary: string;
  version: string;
}
export default function CredentialMihomoSettings() {
  const { t } = useTranslation();
  const [status, setStatus] = useState<PoolStatus | null>(null);
  const [nodes, setNodes] = useState("");
  const [binary, setBinary] = useState("");
  const [concurrency, setConcurrency] = useState(3);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  useEffect(() => {
    let live = true;
    void credentialRequest<PoolStatus>("/credential-ops/mihomo")
      .then((value) => {
        if (live) {
          setStatus(value);
          setBinary(value.binary);
          setConcurrency(value.concurrency);
        }
      })
      .catch(() => {
        if (live) setError(t("credentialOps.poolLoadFailed"));
      });
    return () => {
      live = false;
    };
  }, [t]);
  async function action(install: boolean) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const value = await credentialRequest<PoolStatus>(
        install ? "/credential-ops/mihomo/install" : "/credential-ops/mihomo",
        install ? "POST" : "PUT",
        install ? {} : { nodes, binary, concurrency },
      );
      setStatus(value);
      setNodes("");
      setNotice(t("credentialOps.saved"));
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : t("credentialOps.failed"),
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="space-y-4 border-t border-border pt-5">
      <h3 className="text-sm font-semibold">{t("credentialOps.nodePool")}</h3>
      <p className="text-sm leading-relaxed text-muted-foreground">
        {t("credentialOps.poolHint")}
      </p>
      <div className="flex flex-wrap gap-x-5 gap-y-2 text-xs text-muted-foreground">
        <span>
          {t("credentialOps.poolNodes", { count: status?.node_count || 0 })}
        </span>
        <span>
          {t("credentialOps.poolActive", { count: status?.active || 0 })}
        </span>
        <span>
          {t(
            `credentialOps.${status?.installed ? "poolInstalled" : "poolMissing"}`,
          )}
        </span>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <label className="space-y-1.5 text-sm sm:col-span-2">
          <span>{t("credentialOps.poolInput")}</span>
          <Textarea
            value={nodes}
            onChange={(event) => setNodes(event.target.value)}
            rows={5}
            autoComplete="off"
            spellCheck={false}
            placeholder={
              status?.configured
                ? t("credentialOps.keepSecret")
                : "proxies:\n  - name: login-node\n    type: socks5\n    server: proxy.example.com\n    port: 1080"
            }
          />
        </label>
        <label className="space-y-1.5 text-sm">
          <span>{t("credentialOps.poolBinary")}</span>
          <Input
            value={binary}
            onChange={(event) => setBinary(event.target.value)}
            autoComplete="off"
          />
        </label>
        <label className="space-y-1.5 text-sm">
          <span>{t("credentialOps.poolConcurrency")}</span>
          <DraftNumberInput
            value={concurrency}
            min={1}
            max={16}
            onValueChange={setConcurrency}
          />
        </label>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => void action(false)}
        >
          <Save className="size-4" />
          {t("credentialOps.poolSave")}
        </Button>
        <Button
          variant="outline"
          disabled={busy || !!status?.active}
          onClick={() => void action(true)}
        >
          <Download className="size-4" />
          {t("credentialOps.poolInstall")}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm text-muted-foreground">
          {notice}
        </p>
      )}
    </section>
  );
}
