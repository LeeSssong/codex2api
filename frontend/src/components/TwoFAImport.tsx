import { useCallback, useEffect, useRef, useState } from "react";
import {
  CheckCircle2,
  KeyRound,
  Play,
  RefreshCw,
  RotateCcw,
  Square,
  XCircle,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Select } from "./ui/select";
import { SegmentedPillGroup } from "./ui/segmented-pill-group";
import CredentialEncryptionSetup from "./CredentialEncryptionSetup";
import AccountGroupMultiSelect from "./AccountGroupMultiSelect";
import { api } from "../api";
import type { AccountGroup } from "../types";
import {
  credentialAPI,
  credentialRequest,
  type CredentialJob,
} from "@/lib/credentialOps";
import {
  CredentialImportError,
  credentialImportKey,
  parseCredentialImport,
  type CredentialImportEntry,
} from "@/lib/credentialImport";

interface ImportRow {
  email: string;
  job?: CredentialJob;
  error?: string;
}
export default function TwoFAImport({ onSaved }: { onSaved?: () => void }) {
  const { t } = useTranslation();
  const [inputMode, setInputMode] = useState<"single" | "batch">("single");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [raw, setRaw] = useState("");
  const [ready, setReady] = useState(false);
  const [engineReady, setEngineReady] = useState(false);
  const [globalEngine, setGlobalEngine] = useState("local_worker");
  const remote = globalEngine === "session_studio";
  const [groups, setGroups] = useState<AccountGroup[]>([]);
  const [groupIds, setGroupIds] = useState<number[]>([]);
  const [proxies, setProxies] = useState<Array<{ id: number; label: string }>>(
    [],
  );
  const [proxy, setProxy] = useState("direct");
  const [rows, setRows] = useState<ImportRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const stop = useRef(false);
  const pending = useRef<CredentialImportEntry[]>([]);
  const importKeys = useRef<string[]>([]);
  const reported = useRef(new Set<number>());
  const saved = useRef(onSaved);
  saved.current = onSaved;
  const readyChanged = useCallback((value: boolean) => setReady(value), []);
  useEffect(() => {
    mounted.current = true;
    void credentialAPI
      .overview()
      .then((value) => {
        if (mounted.current) {
          setGlobalEngine(value.runtime_settings.engine || "local_worker");
          setEngineReady(true);
        }
      })
      .catch(() => {
        if (mounted.current) setError(t("credentialOps.engineLoadFailed"));
      });
    void api
      .listAccountGroups()
      .then((value) => {
        if (mounted.current)
          setGroups(value.groups.filter((group) => group.channel === "codex"));
      })
      .catch(() => {});
    void api
      .listProxies()
      .then((value) => {
        if (mounted.current)
          setProxies(
            value.proxies
              .filter((p) => p.enabled)
              .map((p) => ({ id: p.id, label: p.label || `#${p.id}` })),
          );
      })
      .catch(() => {});
    return () => {
      mounted.current = false;
      stop.current = true;
      pending.current = [];
    };
  }, []);
  useEffect(() => {
    if (
      !rows.some(
        (row) => row.job && ["queued", "running"].includes(row.job.Status),
      )
    )
      return;
    let live = true;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      const updates = await Promise.allSettled(
        rows.map(async (row) => {
          if (!row.job || !["queued", "running"].includes(row.job.Status))
            return row;
          const job = await credentialRequest<CredentialJob>(
            `/credential-ops/login/${row.job.ID}`,
            "GET",
            undefined,
            controller.signal,
          );
          return { ...row, job, error: undefined };
        }),
      );
      if (!live) return;
      const next = updates.map((result, i) =>
        result.status === "fulfilled"
          ? result.value
          : { ...rows[i], error: t("credentialOps.pollFailed") },
      );
      setRows(next);
      for (const row of next)
        if (
          row.job?.Status === "succeeded" &&
          !reported.current.has(row.job.ID)
        ) {
          reported.current.add(row.job.ID);
          saved.current?.();
        }
    }, 2000);
    return () => {
      live = false;
      clearTimeout(timer);
      controller.abort();
    };
  }, [rows, t]);
  const update = (index: number, patch: Partial<ImportRow>) => {
    if (mounted.current)
      setRows((current) =>
        current.map((row, i) => (i === index ? { ...row, ...patch } : row)),
      );
  };
  async function queue() {
    setError("");
    if (!pending.current.length) {
      try {
        pending.current = parseCredentialImport(
          inputMode === "batch"
            ? raw
            : JSON.stringify({ email, password, totp_secret: totp }),
        );
      } catch (cause) {
        setError(
          t(
            `credentialOps.importErrors.${cause instanceof CredentialImportError ? cause.code : "format"}`,
            { row: cause instanceof CredentialImportError ? cause.row : 0 },
          ),
        );
        return;
      }
      setRows(pending.current.map((entry) => ({ email: entry.email })));
      importKeys.current = pending.current.map(() => credentialImportKey());
      setRaw("");
      setPassword("");
      setTotp("");
    }
    setBusy(true);
    stop.current = false;
    for (let i = 0; i < pending.current.length; i++) {
      if (stop.current || !mounted.current) break;
      const entry = pending.current[i];
      if (!entry.password) continue;
      try {
        const job = await credentialAPI.import({
          ...entry,
          import_key: importKeys.current[i],
          mode: "password_totp",
          engine: "inherit",
          proxy_source: remote
            ? "direct"
            : proxy === "direct" || proxy === "mihomo"
              ? proxy
              : "proxy",
          proxy_id: !remote && /^\d+$/.test(proxy) ? Number(proxy) : null,
          group_ids: groupIds,
        });
        entry.password = "";
        entry.totp_secret = "";
        update(i, { job, error: undefined });
      } catch {
        update(i, { error: t("credentialOps.queueFailed") });
      }
    }
    if (pending.current.every((entry) => !entry.password)) pending.current = [];
    if (mounted.current) setBusy(false);
  }
  async function retry(index: number) {
    const row = rows[index];
    if (!row.job) {
      await queue();
      return;
    }
    try {
      const job = await credentialRequest<CredentialJob>(
        `/credential-ops/login/${row.job.ID}/retry`,
        "POST",
      );
      update(index, { job, error: undefined });
    } catch {
      update(index, { error: t("credentialOps.retryFailed") });
    }
  }
  async function cancel(index: number) {
    const row = rows[index];
    if (!row.job) return;
    try {
      await credentialAPI.cancel(row.job.ID);
      update(index, { job: { ...row.job, Status: "cancelled" } });
    } catch {
      update(index, { error: t("credentialOps.cancelFailed") });
    }
  }
  function reset() {
    pending.current = [];
    setRows([]);
    setRaw("");
    setEmail("");
    setPassword("");
    setTotp("");
    setError("");
  }
  const active = rows.some(
    (row) => row.job && ["queued", "running"].includes(row.job.Status),
  );
  return (
    <section className="space-y-4" aria-label={t("credentialOps.firstLogin")}>
      <CredentialEncryptionSetup onReady={readyChanged} />
      <p className="text-sm leading-relaxed text-muted-foreground">
        {t("credentialOps.firstLoginHint")}
      </p>
      {engineReady && (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {remote
            ? t("credentialOps.remoteHint")
            : t("credentialOps.fixedEgressHint")}
        </p>
      )}
      {rows.length === 0 ? (
        <>
          <SegmentedPillGroup
            label={t("credentialOps.inputMode")}
            value={inputMode}
            onChange={setInputMode}
            options={[
              { value: "single", label: t("credentialOps.single") },
              { value: "batch", label: t("credentialOps.batch") },
            ]}
            className="max-w-sm"
          />
          {inputMode === "single" ? (
            <div className="grid gap-4 sm:grid-cols-2">
              <label className="space-y-1.5 text-sm sm:col-span-2">
                <span>{t("credentialOps.email")}</span>
                <Input
                  type="email"
                  value={email}
                  onChange={(event) => setEmail(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                />
              </label>
              <label className="space-y-1.5 text-sm">
                <span>{t("credentialOps.password")}</span>
                <Input
                  type="password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  autoComplete="new-password"
                />
              </label>
              <label className="space-y-1.5 text-sm">
                <span>{t("credentialOps.totp")}</span>
                <Input
                  aria-label={t("credentialOps.totp")}
                  type="password"
                  value={totp}
                  onChange={(event) => setTotp(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                />
                <span className="block text-xs text-muted-foreground">
                  {t("credentialOps.totpHint")}
                </span>
              </label>
            </div>
          ) : (
            <label className="block space-y-1.5 text-sm">
              <span>{t("credentialOps.batchInput")}</span>
              <Textarea
                aria-label={t("credentialOps.batchInput")}
                rows={6}
                value={raw}
                onChange={(event) => setRaw(event.target.value)}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                placeholder={t("credentialOps.batchPlaceholder")}
              />
              <span className="block text-xs text-muted-foreground">
                {t("credentialOps.batchHint")}
              </span>
            </label>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="space-y-1.5 text-sm">
              <span>{t("credentialOps.proxy")}</span>
              <Select
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
                  { value: "direct", label: t("credentialOps.direct") },
                  { value: "mihomo", label: t("credentialOps.nodePool") },
                  ...proxies.map((p) => ({
                    value: String(p.id),
                    label: p.label,
                  })),
                ]}
              />
            </label>
            <div className="space-y-1.5 text-sm">
              <span>{t("credentialOps.groups")}</span>
              <AccountGroupMultiSelect
                groups={groups}
                value={groupIds}
                onChange={setGroupIds}
                placeholder={t("credentialOps.noGroups")}
                emptyLabel={t("credentialOps.noGroups")}
                selectedLabel={t("credentialOps.groupCount", {
                  count: groupIds.length,
                })}
              />
            </div>
          </div>
        </>
      ) : (
        <div className="space-y-1" aria-live="polite">
          {rows.map((row, index) => (
            <div
              key={`${index}-${row.email}`}
              className="flex flex-wrap items-center justify-between gap-3 border-b border-border py-3 last:border-b-0"
            >
              <div className="min-w-0 flex-1">
                <p className="break-all text-sm font-medium">{row.email}</p>
                <p className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
                  {row.job?.Status === "succeeded" ? (
                    <CheckCircle2 className="size-3.5 shrink-0 text-[hsl(var(--success))]" />
                  ) : row.error || row.job?.Status === "failed" ? (
                    <XCircle className="size-3.5 shrink-0 text-destructive" />
                  ) : (
                    <KeyRound className="size-3.5 shrink-0" />
                  )}
                  {row.job
                    ? t(`credentialOps.status.${row.job.Status}`, {
                        defaultValue: row.job.Status,
                      })
                    : t("credentialOps.pending")}
                  {row.job && ` · #${row.job.ID}`}
                </p>
                {row.error && (
                  <p role="alert" className="mt-1 text-xs text-destructive">
                    {row.error}
                  </p>
                )}
              </div>
              {row.job && ["queued", "running"].includes(row.job.Status) ? (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => void cancel(index)}
                >
                  {t("credentialOps.cancelTask")}
                </Button>
              ) : (
                (row.error ||
                  row.job?.Status === "failed" ||
                  row.job?.Status === "cancelled") && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy}
                    onClick={() => void retry(index)}
                  >
                    <RotateCcw className="size-3.5" />
                    {t("credentialOps.retry")}
                  </Button>
                )
              )}
            </div>
          ))}
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {rows.length === 0 ? (
          <Button
            disabled={busy || !ready || !engineReady}
            onClick={() => void queue()}
          >
            {busy ? (
              <RefreshCw className="size-4 animate-spin" />
            ) : (
              <Play className="size-4" />
            )}
            {t("credentialOps.loginImport")}
          </Button>
        ) : (
          <>
            {busy && (
              <Button
                variant="outline"
                onClick={() => {
                  stop.current = true;
                }}
              >
                <Square className="size-3.5" />
                {t("credentialOps.stopQueueing")}
              </Button>
            )}
            {!busy && pending.current.length > 0 && (
              <Button variant="outline" onClick={() => void queue()}>
                {t("credentialOps.resumeQueueing")}
              </Button>
            )}
            {!busy && (
              <Button variant="outline" onClick={reset}>
                {t("credentialOps.newImport")}
              </Button>
            )}
            <Link
              to="/credential-ops?view=tasks"
              className="text-sm text-primary hover:underline"
            >
              {t("credentialOps.viewTasks")}
            </Link>
          </>
        )}
      </div>
      {active && (
        <p className="text-xs text-muted-foreground">
          {t("credentialOps.durableHint")}
        </p>
      )}
    </section>
  );
}
