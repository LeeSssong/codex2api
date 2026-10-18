import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { Download, RefreshCw, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import Modal from "./Modal";
import { Button } from "./ui/button";
import { Select } from "./ui/select";
import { Switch } from "./ui/switch";
import {
  credentialAPI,
  type CredentialExport,
  type CredentialRotation,
} from "@/lib/credentialOps";
import { credentialCombo } from "@/lib/credentialImport";

function download(text: string, filename: string, type: string) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
export default function CredentialTOTPDialog({
  accountId,
  name,
  onClose,
}: {
  accountId: number;
  name: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [rotation, setRotation] = useState<CredentialRotation | null>(null);
  const [source, setSource] = useState("current");
  const [includePassword, setIncludePassword] = useState(false);
  const [exported, setExported] = useState<CredentialExport | null>(null);
  const [qr, setQR] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState(false);
  const sequence = useRef(0);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      try {
        const value = await credentialAPI.rotation(accountId);
        if (live.current) setRotation(value);
      } catch {
        if (live.current) setError(t("credentialOps.rotationLoadFailed"));
      }
      if (live.current) timer = setTimeout(poll, 3000);
    }
    void poll();
    return () => {
      live.current = false;
      sequence.current++;
      clearTimeout(timer);
    };
  }, [accountId, t]);
  function clearExport() {
    sequence.current++;
    setExported(null);
    setQR("");
    setError("");
  }
  async function action(kind: "rotate" | "verify_new" | "verify_old") {
    setBusy(true);
    setError("");
    setConfirm(false);
    clearExport();
    try {
      const value =
        kind === "rotate"
          ? await credentialAPI.rotate(accountId)
          : await credentialAPI.verify(accountId, kind);
      if (live.current) setRotation(value);
    } catch {
      if (live.current) setError(t("credentialOps.rotationFailed"));
    } finally {
      if (live.current) setBusy(false);
    }
  }
  async function exportSecrets() {
    const epoch = ++sequence.current;
    setBusy(true);
    setError("");
    setExported(null);
    setQR("");
    try {
      const value = await credentialAPI.export(
        accountId,
        source,
        includePassword,
      );
      if (!live.current || sequence.current !== epoch) return;
      setExported(value);
      const uri =
        value.otpauth_uri ||
        `otpauth://totp/${encodeURIComponent(`OpenAI:${value.email}`)}?secret=${encodeURIComponent(value.totp_secret)}&issuer=OpenAI&algorithm=SHA1&digits=6&period=30`;
      const image = await QRCode.toDataURL(uri, {
        width: 192,
        margin: 2,
        errorCorrectionLevel: "M",
      });
      if (live.current && sequence.current === epoch) setQR(image);
    } catch {
      if (live.current && sequence.current === epoch)
        setError(t("credentialOps.exportFailed"));
    } finally {
      if (live.current && sequence.current === epoch) setBusy(false);
    }
  }
  const uncertain = rotation?.status === "uncertain";
  const active =
    rotation &&
    [
      "queued",
      "running",
      "enrolling",
      "prepared",
      "activating",
      "verifying",
    ].includes(rotation.status);
  let comboAllowed = false;
  if (exported) {
    try {
      credentialCombo(exported);
      comboAllowed = true;
    } catch {
      /* JSON preserves ambiguous passwords. */
    }
  }
  return (
    <Modal
      show
      title={`${t("credentialOps.totpManage")} · ${name}`}
      onClose={() => {
        clearExport();
        onClose();
      }}
      contentClassName="sm:max-w-[660px]"
    >
      <div className="space-y-5">
        <p className="text-sm leading-relaxed text-muted-foreground">
          {t("credentialOps.rotationHint")}
        </p>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className="flex items-center gap-2 text-sm">
            <ShieldCheck className="size-4" />
            {t("credentialOps.rotationStatus")}:{" "}
            {t(`credentialOps.rotationStates.${rotation?.status || "idle"}`, {
              defaultValue: rotation?.status || "—",
            })}
          </span>
          <Button
            size="sm"
            variant="outline"
            disabled={busy || !!active || uncertain}
            onClick={() => setConfirm(true)}
          >
            {t("credentialOps.rotateTotp")}
          </Button>
        </div>
        {confirm && (
          <div className="space-y-3 rounded-lg border border-border p-4">
            <p className="text-sm">
              {t("credentialOps.rotationConfirm", { name })}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() => void action("rotate")}
              >
                {t("credentialOps.confirmRotation")}
              </Button>
              <Button variant="outline" onClick={() => setConfirm(false)}>
                {t("common.cancel")}
              </Button>
            </div>
          </div>
        )}
        {uncertain && (
          <div className="space-y-3">
            <p role="status" className="text-sm text-[hsl(var(--warning))]">
              {t("credentialOps.uncertainHint")}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                variant="outline"
                disabled={busy || !rotation?.candidate_available}
                onClick={() => void action("verify_new")}
              >
                {t("credentialOps.verifyNew")}
              </Button>
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => void action("verify_old")}
              >
                {t("credentialOps.verifyOld")}
              </Button>
            </div>
          </div>
        )}
        <div className="space-y-4 border-t border-border pt-5">
          <h3 className="text-sm font-semibold">
            {t("credentialOps.exportCredentials")}
          </h3>
          <label className="block space-y-1.5 text-sm">
            <span>{t("credentialOps.exportSource")}</span>
            <Select
              disabled={busy}
              value={source}
              onValueChange={(value) => {
                clearExport();
                setSource(value);
              }}
              options={[
                {
                  value: "current",
                  label: t("credentialOps.currentCredentials"),
                },
                ...(rotation?.candidate_available
                  ? [
                      {
                        value: "candidate",
                        label: t("credentialOps.candidateCredentials"),
                      },
                    ]
                  : []),
                ...(rotation?.previous_available
                  ? [
                      {
                        value: "previous",
                        label: t("credentialOps.previousCredentials"),
                      },
                    ]
                  : []),
              ]}
            />
          </label>
          <label className="flex items-center gap-2 text-sm">
            <Switch
              disabled={busy}
              checked={includePassword}
              onCheckedChange={(value) => {
                clearExport();
                setIncludePassword(value);
              }}
            />
            {t("credentialOps.includePassword")}
          </label>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => void exportSecrets()}
          >
            {busy ? (
              <RefreshCw className="size-4 animate-spin" />
            ) : (
              <Download className="size-4" />
            )}
            {t("credentialOps.showExport")}
          </Button>
          {exported && (
            <div className="space-y-3 rounded-lg border border-border p-4">
              {qr && (
                <img
                  src={qr}
                  alt={t("credentialOps.totpQR")}
                  width={192}
                  height={192}
                  className="rounded bg-white"
                />
              )}
              <p className="break-all font-mono text-sm">
                {exported.totp_secret}
              </p>
              <div className="flex flex-wrap gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    download(
                      JSON.stringify(exported, null, 2),
                      `openai-${accountId}-credentials.json`,
                      "application/json",
                    )
                  }
                >
                  {t("credentialOps.downloadJSON")}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    download(
                      exported.totp_secret,
                      `openai-${accountId}-2fa.txt`,
                      "text/plain",
                    )
                  }
                >
                  {t("credentialOps.downloadSecret")}
                </Button>
                {comboAllowed && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      download(
                        credentialCombo(exported),
                        `openai-${accountId}-credentials.txt`,
                        "text/plain",
                      )
                    }
                  >
                    {t("credentialOps.downloadCombo")}
                  </Button>
                )}
              </div>
              {!comboAllowed && (
                <p className="text-xs text-muted-foreground">
                  {t("credentialOps.useJSON")}
                </p>
              )}
            </div>
          )}
        </div>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}
