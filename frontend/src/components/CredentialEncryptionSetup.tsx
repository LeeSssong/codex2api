import { useEffect, useState } from "react";
import { CheckCircle2, RefreshCw, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "./ui/button";
import { credentialAPI, type CredentialEncryption } from "@/lib/credentialOps";

export default function CredentialEncryptionSetup({
  onReady,
}: {
  onReady?: (ready: boolean) => void;
}) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<CredentialEncryption | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [checking, setChecking] = useState(true);
  useEffect(() => {
    let live = true;
    credentialAPI
      .encryption()
      .then((value) => {
        if (live) {
          setStatus(value);
          onReady?.(value.configured);
        }
      })
      .catch(() => {
        if (live) setError(t("credentialOps.encryptionFailed"));
      })
      .finally(() => {
        if (live) setChecking(false);
      });
    return () => {
      live = false;
    };
  }, [t, onReady]);
  async function initialize() {
    setBusy(true);
    setError("");
    try {
      const value = await credentialAPI.initialize();
      setStatus(value);
      onReady?.(value.configured);
    } catch {
      setError(t("credentialOps.encryptionFailed"));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="space-y-2 rounded-lg border border-border bg-muted/20 p-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="flex items-center gap-2 text-sm font-medium">
          {status?.configured ? (
            <CheckCircle2 className="size-4 shrink-0 text-[hsl(var(--success))]" />
          ) : (
            <ShieldCheck className="size-4 shrink-0" />
          )}
          {t(
            `credentialOps.${checking ? "encryptionChecking" : status?.configured ? "encryptionReady" : "encryptionRequired"}`,
          )}
        </span>
        {!status?.configured && !checking && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => void initialize()}
          >
            {busy && <RefreshCw className="size-3.5 animate-spin" />}
            {t("credentialOps.encryptionInitialize")}
          </Button>
        )}
      </div>
      {!status?.configured && (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {t("credentialOps.encryptionHint")}
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
