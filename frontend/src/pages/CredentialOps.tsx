import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  CheckCircle2,
  KeyRound,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Save,
  Search,
  Settings2,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import PageHeader from "../components/PageHeader";
import Modal from "../components/Modal";
import TwoFAImport from "../components/TwoFAImport";
import CredentialConfigEditor from "../components/CredentialConfigEditor";
import CredentialEncryptionSetup from "../components/CredentialEncryptionSetup";
import CredentialTOTPDialog from "../components/CredentialTOTPDialog";
import CredentialMihomoSettings from "../components/CredentialMihomoSettings";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { DraftNumberInput } from "@/components/ui/draft-number-input";
import { SegmentedPillGroup } from "@/components/ui/segmented-pill-group";
import {
  credentialAPI,
  type CredentialAccount,
  type CredentialOverview,
  type CredentialRules,
  type CredentialRuntimeSettings,
} from "@/lib/credentialOps";
import { api } from "../api";
import type { AccountRow } from "../types";

type View = "accounts" | "tasks" | "rules" | "runtime";
const views: View[] = ["accounts", "tasks", "rules", "runtime"];
export default function CredentialOps() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const view = views.includes(params.get("view") as View)
    ? (params.get("view") as View)
    : "accounts";
  const [data, setData] = useState<CredentialOverview | null>(null);
  const [error, setError] = useState("");
  const [mutationError, setMutationError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [selected, setSelected] = useState<number[]>([]);
  const [showImport, setShowImport] = useState(false);
  const [editor, setEditor] = useState<{
    id: number;
    name: string;
    email: string;
  } | null>(null);
  const [rotation, setRotation] = useState<CredentialAccount | null>(null);
  const [remove, setRemove] = useState<CredentialAccount | null>(null);
  const [showAdd, setShowAdd] = useState(false);
  const [available, setAvailable] = useState<AccountRow[]>([]);
  const [addId, setAddId] = useState("");
  const alive = useRef(true);
  const requestSeq = useRef(0);
  const load = useCallback(async () => {
    const seq = ++requestSeq.current;
    try {
      const next = await credentialAPI.overview();
      if (alive.current && seq === requestSeq.current) {
        setData(next);
        setError("");
      }
    } catch (cause) {
      if (alive.current && seq === requestSeq.current)
        setError(
          cause instanceof Error
            ? cause.message
            : t("credentialOps.loadFailed"),
        );
    }
  }, [t]);
  useEffect(() => {
    alive.current = true;
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      await load();
      if (alive.current) timer = setTimeout(poll, 5000);
    }
    void poll();
    return () => {
      alive.current = false;
      requestSeq.current++;
      clearTimeout(timer);
    };
  }, [load]);
  useEffect(() => {
    const id = Number(params.get("account"));
    if (id > 0 && data) {
      const row = data.accounts.find((account) => account.account_id === id);
      if (params.get("action") === "totp" && row) setRotation(row);
      else
        setEditor({
          id,
          name: row?.name || `#${id}`,
          email: row?.email || row?.config?.email || "",
        });
    }
  }, [params, !!data]);
  async function mutate(action: () => Promise<unknown>, message = "saved") {
    setBusy(true);
    setMutationError("");
    setNotice("");
    try {
      await action();
      if (!alive.current) return;
      setNotice(t(`credentialOps.${message}`));
      await load();
    } catch (cause) {
      if (alive.current)
        setMutationError(
          cause instanceof Error ? cause.message : t("credentialOps.failed"),
        );
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  async function addExisting() {
    setShowAdd(true);
    setMutationError("");
    try {
      const value = await api.getAccounts({ channel: "codex", view: "lite" });
      if (alive.current)
        setAvailable(
          value.accounts.filter(
            (account) =>
              account.account_type === "oauth" &&
              !account.agent_identity &&
              !account.at_only &&
              account.status !== "deleted" &&
              !data?.accounts.some((row) => row.account_id === account.id),
          ),
        );
    } catch {
      setMutationError(t("credentialOps.loadAccountsFailed"));
    }
  }
  const rows = useMemo(
    () =>
      (data?.accounts || []).filter((row) => {
        const q = search.trim().toLowerCase();
        return (
          (!q ||
            [
              row.name,
              row.email,
              row.account_id,
              row.probe_state,
              row.blocked_reason,
            ]
              .join(" ")
              .toLowerCase()
              .includes(q)) &&
          (filter === "all" ||
            (filter === "paused" && !row.enabled) ||
            (filter === "healthy" && row.enabled && row.probe_state === "ok") ||
            (filter === "attention" &&
              row.enabled &&
              (!!row.blocked_reason ||
                !["ok", "pending"].includes(row.probe_state))))
        );
      }),
    [data, search, filter],
  );
  const chosen = selected.filter((id) =>
    rows.some((row) => row.account_id === id),
  );
  const time = (value?: string | null) =>
    value ? new Date(value).toLocaleString() : "—";
  return (
    <main className="mx-auto w-full max-w-[1440px] space-y-5">
      <PageHeader
        title={t("credentialOps.title")}
        description={t("credentialOps.description")}
        onRefresh={() => {
          setRefreshing(true);
          void load().finally(() => setRefreshing(false));
        }}
        actionMeta={
          refreshing ? (
            <span role="status">{t("credentialOps.refreshing")}</span>
          ) : undefined
        }
        actions={
          <>
            <Button variant="outline" onClick={() => void addExisting()}>
              <Plus className="size-4" />
              {t("credentialOps.addExisting")}
            </Button>
            <Button onClick={() => setShowImport(true)}>
              <KeyRound className="size-4" />
              {t("credentialOps.firstLogin")}
            </Button>
          </>
        }
      />
      <SegmentedPillGroup
        label={t("credentialOps.sections")}
        value={view}
        onChange={(value) => setParams({ view: value })}
        options={views.map((value) => ({
          value,
          label: t(`credentialOps.tabs.${value}`),
        }))}
        className="max-w-xl"
      />
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {mutationError && (
        <p role="alert" className="text-sm text-destructive">
          {mutationError}
        </p>
      )}
      {notice && (
        <p
          role="status"
          className="flex items-center gap-2 text-sm text-[hsl(var(--success))]"
        >
          <CheckCircle2 className="size-4" />
          {notice}
        </p>
      )}
      {!data && !error && (
        <p role="status" className="text-sm text-muted-foreground">
          {t("credentialOps.loading")}
        </p>
      )}
      {data && (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
            <span className="flex items-center gap-2">
              <ShieldCheck className="size-4" />
              {t(
                `credentialOps.${data.encryption?.configured ? "encryptionReady" : "encryptionRequired"}`,
              )}
            </span>
            <span className="text-muted-foreground">
              {t(
                `credentialOps.workerStates.${data.worker?.state || "unavailable"}`,
                { defaultValue: data.worker?.state || "—" },
              )}{" "}
              ·{" "}
              {t("credentialOps.concurrency", {
                count: data.runtime_settings?.worker_concurrency || 3,
              })}
            </span>
          </div>
          {view === "accounts" && (
            <>
              <div className="flex flex-wrap items-center gap-3">
                <div className="relative min-w-0 flex-1 sm:max-w-sm">
                  <Search className="pointer-events-none absolute left-3 top-2.5 size-4 text-muted-foreground" />
                  <Input
                    aria-label={t("credentialOps.search")}
                    placeholder={t("credentialOps.search")}
                    value={search}
                    onChange={(event) => setSearch(event.target.value)}
                    className="pl-9"
                  />
                </div>
                <Select
                  value={filter}
                  onValueChange={setFilter}
                  aria-label={t("credentialOps.filter")}
                  options={["all", "healthy", "attention", "paused"].map(
                    (value) => ({
                      value,
                      label: t(`credentialOps.filters.${value}`),
                    }),
                  )}
                  className="w-auto min-w-[130px]"
                />
                {chosen.length > 0 && (
                  <Button
                    variant="outline"
                    disabled={busy}
                    onClick={() =>
                      void mutate(async () => {
                        const results = await Promise.allSettled(
                          chosen.map((id) => credentialAPI.login(id)),
                        );
                        const failed = results.filter(
                          (result) => result.status === "rejected",
                        ).length;
                        if (failed)
                          throw new Error(
                            t("credentialOps.batchPartial", { count: failed }),
                          );
                        setSelected([]);
                      }, "loginQueued")
                    }
                  >
                    <Play className="size-4" />
                    {t("credentialOps.batchRelogin", { count: chosen.length })}
                  </Button>
                )}
              </div>
              <div className="space-y-3 md:hidden">
                {rows.map((row) => (
                  <article
                    key={row.account_id}
                    className="space-y-4 rounded-xl border border-border bg-card p-4"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0 flex-1">
                        <Link
                          to={`/accounts?account=${row.account_id}`}
                          className="block break-words font-medium text-primary hover:underline"
                        >
                          {row.name || `#${row.account_id}`}
                        </Link>
                        <p className="mt-1 break-all text-xs text-muted-foreground">
                          {row.email || row.config?.email}
                        </p>
                      </div>
                      <Switch
                        checked={selected.includes(row.account_id)}
                        aria-label={t("credentialOps.selectAccount", {
                          name: row.name,
                        })}
                        onCheckedChange={(value) =>
                          setSelected((current) =>
                            value
                              ? [...current, row.account_id]
                              : current.filter((id) => id !== row.account_id),
                          )
                        }
                      />
                    </div>
                    <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
                      <span
                        className={
                          row.probe_state === "auth" ? "text-destructive" : ""
                        }
                      >
                        {row.enabled
                          ? t(`credentialOps.probeStates.${row.probe_state}`, {
                              defaultValue: row.probe_state,
                            })
                          : t("credentialOps.paused")}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {t("credentialOps.failureCount", {
                          count: row.fail_streak,
                        })}
                      </span>
                    </div>
                    <div className="space-y-1 text-xs text-muted-foreground">
                      <p>
                        {t(
                          row.config?.mode === "email_otp_url"
                            ? "credentialOps.emailMode"
                            : "credentialOps.passwordMode",
                        )}{" "}
                        ·{" "}
                        {row.effective_engine === "session_studio"
                          ? "Session Studio"
                          : t("credentialOps.localWorker")}
                      </p>
                      <p>
                        {row.effective_proxy || t("credentialOps.accountProxy")}
                      </p>
                      <p>
                        {t("credentialOps.nextProbe")}:{" "}
                        {row.enabled ? time(row.next_probe_at) : "—"}
                      </p>
                      <p>
                        {t("credentialOps.passwordConfigured")}:{" "}
                        {t(
                          row.config?.password_configured
                            ? "credentialOps.yes"
                            : "credentialOps.no",
                        )}{" "}
                        · {t("credentialOps.totpConfigured")}:{" "}
                        {t(
                          row.config?.totp_configured
                            ? "credentialOps.yes"
                            : "credentialOps.no",
                        )}
                      </p>
                      <p>
                        {t("credentialOps.cooldownUntil")}:{" "}
                        {time(row.cooldown_until)}
                      </p>
                      {row.blocked_reason && (
                        <p className="break-words text-destructive">
                          {row.blocked_reason}
                        </p>
                      )}
                    </div>
                    <label className="flex items-center gap-2 text-sm">
                      <Switch
                        checked={row.auto_relogin}
                        disabled={busy}
                        onCheckedChange={(value) =>
                          void mutate(() =>
                            credentialAPI.monitor(row.account_id, {
                              enabled: row.enabled,
                              auto_relogin: value,
                            }),
                          )
                        }
                      />
                      {t("credentialOps.autoRelogin")}
                    </label>
                    <div className="flex flex-wrap gap-2 border-t border-border pt-3">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={busy}
                        onClick={() =>
                          void mutate(
                            () => credentialAPI.login(row.account_id),
                            "loginQueued",
                          )
                        }
                      >
                        <Play className="size-3.5" />
                        {t("credentialOps.relogin")}
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() =>
                          setEditor({
                            id: row.account_id,
                            name: row.name,
                            email: row.email || row.config?.email || "",
                          })
                        }
                      >
                        {t("credentialOps.edit")}
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => setRotation(row)}
                      >
                        {t("credentialOps.totpManage")}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy}
                        onClick={() =>
                          void mutate(
                            () => credentialAPI.probe(row.account_id),
                            "probeQueued",
                          )
                        }
                      >
                        {t("credentialOps.inspect")}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy}
                        onClick={() =>
                          void mutate(() =>
                            credentialAPI.monitor(row.account_id, {
                              enabled: !row.enabled,
                              auto_relogin: row.auto_relogin,
                            }),
                          )
                        }
                      >
                        {t(`credentialOps.${row.enabled ? "pause" : "resume"}`)}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => setRemove(row)}
                        aria-label={t("credentialOps.removeAccount", {
                          name: row.name,
                        })}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </article>
                ))}
                {rows.length === 0 && (
                  <p className="py-8 text-center text-sm text-muted-foreground">
                    {t(
                      data.accounts.length
                        ? "credentialOps.noResults"
                        : "credentialOps.empty",
                    )}
                  </p>
                )}
              </div>
              <div className="hidden overflow-x-auto rounded-xl border border-border md:block">
                <table className="w-full text-sm">
                  <thead className="bg-muted/40 text-xs text-muted-foreground">
                    <tr>
                      {[
                        "selection",
                        "account",
                        "login",
                        "inspection",
                        "lastActivity",
                        "actions",
                      ].map((key) => (
                        <th
                          key={key}
                          className="px-4 py-3 text-left font-medium"
                        >
                          {t(`credentialOps.columns.${key}`)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row) => (
                      <tr
                        key={row.account_id}
                        className="border-t border-border align-top"
                      >
                        <td className="px-4 py-4">
                          <Switch
                            checked={selected.includes(row.account_id)}
                            onCheckedChange={(value) =>
                              setSelected((current) =>
                                value
                                  ? [...current, row.account_id]
                                  : current.filter(
                                      (id) => id !== row.account_id,
                                    ),
                              )
                            }
                            aria-label={t("credentialOps.selectAccount", {
                              name: row.name,
                            })}
                          />
                        </td>
                        <td className="max-w-64 px-4 py-4">
                          <Link
                            to={`/accounts?account=${row.account_id}`}
                            className="break-all font-medium text-primary hover:underline"
                          >
                            {row.name || `#${row.account_id}`}
                          </Link>
                          <p className="mt-1 break-all text-xs text-muted-foreground">
                            {row.email ||
                              row.config?.email ||
                              `#${row.account_id}`}
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {row.effective_proxy ||
                              t("credentialOps.accountProxy")}
                          </p>
                        </td>
                        <td className="px-4 py-4">
                          <p>
                            {t(
                              `credentialOps.${row.config?.mode === "email_otp_url" ? "emailMode" : "passwordMode"}`,
                            )}
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {row.config?.password_configured ||
                            row.config?.otp_url_configured ||
                            data.login_configured[String(row.account_id)]
                              ? t("credentialOps.configured")
                              : t("credentialOps.incomplete")}
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {t("credentialOps.passwordConfigured")}:{" "}
                            {t(
                              row.config?.password_configured
                                ? "credentialOps.yes"
                                : "credentialOps.no",
                            )}{" "}
                            · {t("credentialOps.totpConfigured")}:{" "}
                            {t(
                              row.config?.totp_configured
                                ? "credentialOps.yes"
                                : "credentialOps.no",
                            )}
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {row.effective_engine === "session_studio"
                              ? "Session Studio"
                              : t("credentialOps.localWorker")}
                          </p>
                        </td>
                        <td className="max-w-60 px-4 py-4">
                          <p
                            className={
                              row.probe_state === "auth"
                                ? "text-destructive"
                                : row.probe_state === "ok"
                                  ? "text-[hsl(var(--success))]"
                                  : ""
                            }
                          >
                            {row.enabled
                              ? t(
                                  `credentialOps.probeStates.${row.probe_state}`,
                                  { defaultValue: row.probe_state },
                                )
                              : t("credentialOps.paused")}
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {t("credentialOps.failureCount", {
                              count: row.fail_streak,
                            })}
                          </p>
                          {row.blocked_reason && (
                            <p className="mt-1 break-words text-xs text-destructive">
                              {row.blocked_reason}
                            </p>
                          )}
                          <label className="mt-3 flex items-center gap-2 text-xs">
                            <Switch
                              disabled={busy}
                              checked={row.auto_relogin}
                              onCheckedChange={(value) =>
                                void mutate(() =>
                                  credentialAPI.monitor(row.account_id, {
                                    enabled: row.enabled,
                                    auto_relogin: value,
                                  }),
                                )
                              }
                            />
                            {t("credentialOps.autoRelogin")}
                          </label>
                        </td>
                        <td className="px-4 py-4 text-xs text-muted-foreground">
                          <p>
                            {t("credentialOps.lastProbe")}:{" "}
                            {time(row.last_probe_at)}
                          </p>
                          <p className="mt-1">
                            {t("credentialOps.lastLogin")}:{" "}
                            {time(row.last_reauth_at)}
                          </p>
                          <p className="mt-1">
                            {t("credentialOps.nextProbe")}:{" "}
                            {row.enabled ? time(row.next_probe_at) : "—"}
                          </p>
                          <p className="mt-1">
                            {t("credentialOps.cooldownUntil")}:{" "}
                            {time(row.cooldown_until)}
                          </p>
                        </td>
                        <td className="min-w-56 px-4 py-3">
                          <div className="flex max-w-72 flex-wrap gap-1">
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={busy}
                              onClick={() =>
                                void mutate(
                                  () => credentialAPI.login(row.account_id),
                                  "loginQueued",
                                )
                              }
                            >
                              <Play className="size-3.5" />
                              {t("credentialOps.relogin")}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() =>
                                setEditor({
                                  id: row.account_id,
                                  name: row.name,
                                  email: row.email || row.config?.email || "",
                                })
                              }
                            >
                              <Settings2 className="size-3.5" />
                              {t("credentialOps.edit")}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={busy}
                              onClick={() =>
                                void mutate(
                                  () => credentialAPI.probe(row.account_id),
                                  "probeQueued",
                                )
                              }
                            >
                              {t("credentialOps.inspect")}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => setRotation(row)}
                            >
                              {t("credentialOps.totpManage")}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              disabled={busy}
                              onClick={() =>
                                void mutate(() =>
                                  credentialAPI.monitor(row.account_id, {
                                    enabled: !row.enabled,
                                    auto_relogin: row.auto_relogin,
                                  }),
                                )
                              }
                            >
                              {row.enabled ? (
                                <Pause className="size-3.5" />
                              ) : (
                                <Play className="size-3.5" />
                              )}
                              {t(
                                `credentialOps.${row.enabled ? "pause" : "resume"}`,
                              )}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => setRemove(row)}
                              aria-label={t("credentialOps.removeAccount", {
                                name: row.name,
                              })}
                            >
                              <Trash2 className="size-3.5" />
                            </Button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {rows.length === 0 && (
                  <div className="space-y-2 px-5 py-12 text-center">
                    <p className="font-medium">
                      {t(
                        data.accounts.length
                          ? "credentialOps.noResults"
                          : "credentialOps.empty",
                      )}
                    </p>
                    <p className="text-sm text-muted-foreground">
                      {t("credentialOps.emptyHint")}
                    </p>
                  </div>
                )}
              </div>
            </>
          )}
          {view === "tasks" && (
            <div className="overflow-x-auto rounded-xl border border-border">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-xs text-muted-foreground">
                  <tr>
                    {["task", "account", "state", "created", "actions"].map(
                      (key) => (
                        <th
                          key={key}
                          className="px-4 py-3 text-left font-medium"
                        >
                          {t(`credentialOps.columns.${key}`)}
                        </th>
                      ),
                    )}
                  </tr>
                </thead>
                <tbody>
                  {data.tasks.map((task) => (
                    <tr
                      key={task.ID}
                      className="border-t border-border align-top"
                    >
                      <td className="px-4 py-4 font-mono">
                        #{task.ID}
                        <p className="mt-1 text-xs">
                          {task.OAuthProfile || "codex"} {task.Kind || ""}
                        </p>
                      </td>
                      <td className="px-4 py-4">
                        {task.AccountID
                          ? `#${task.AccountID}`
                          : t("credentialOps.firstLogin")}
                      </td>
                      <td className="px-4 py-4">
                        <p>
                          {t(`credentialOps.status.${task.Status}`, {
                            defaultValue: task.Status,
                          })}
                        </p>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {t(`credentialOps.stages.${task.Stage}`, {
                            defaultValue: task.Stage,
                          })}
                        </p>
                        {task.Error && (
                          <p className="mt-1 max-w-sm break-words text-xs text-destructive">
                            {task.Error}
                          </p>
                        )}
                      </td>
                      <td className="px-4 py-4 text-xs tabular-nums">
                        {time(task.CreatedAt)}
                      </td>
                      <td className="px-4 py-3">
                        {["queued", "running"].includes(task.Status) && (
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={busy}
                            onClick={() =>
                              void mutate(() => credentialAPI.cancel(task.ID))
                            }
                          >
                            <X className="size-3.5" />
                            {t("credentialOps.cancelTask")}
                          </Button>
                        )}
                        {["failed", "cancelled"].includes(task.Status) &&
                          (!task.Kind || task.Kind === "login") && (
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={busy}
                              onClick={() =>
                                void mutate(
                                  () => credentialAPI.retry(task.ID),
                                  "loginQueued",
                                )
                              }
                            >
                              {t("credentialOps.retry")}
                            </Button>
                          )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {data.tasks.length === 0 && (
                <p className="px-5 py-12 text-center text-sm text-muted-foreground">
                  {t("credentialOps.noTasks")}
                </p>
              )}
            </div>
          )}
          {view === "rules" && (
            <RulesEditor
              value={data.rules}
              busy={busy}
              onSave={(value) => mutate(() => credentialAPI.rules(value))}
            />
          )}
          {view === "runtime" && (
            <RuntimeEditor
              value={data.runtime_settings}
              worker={data.worker}
              busy={busy}
              onSave={(value) => mutate(() => credentialAPI.runtime(value))}
            />
          )}
        </>
      )}
      {showImport && (
        <Modal
          show
          title={t("credentialOps.firstLogin")}
          onClose={() => setShowImport(false)}
          contentClassName="sm:max-w-[760px]"
        >
          <TwoFAImport onSaved={() => void load()} />
        </Modal>
      )}
      {editor && (
        <Modal
          show
          title={`${t("credentialOps.loginConfig")} · ${editor.name}`}
          onClose={() => {
            setEditor(null);
            if (params.has("account")) setParams({ view });
          }}
          contentClassName="sm:max-w-[700px]"
        >
          <CredentialEncryptionSetup />
          <div className="mt-5">
            <CredentialConfigEditor
              key={editor.id}
              accountId={editor.id}
              email={editor.email}
              studioConfigured={!!data?.session_studio_configured}
              globalEngine={data?.runtime_settings.engine}
              onSaved={() => void load()}
            />
          </div>
        </Modal>
      )}
      {rotation && (
        <CredentialTOTPDialog
          accountId={rotation.account_id}
          name={rotation.name}
          onClose={() => setRotation(null)}
        />
      )}
      {remove && (
        <Modal
          show
          title={t("credentialOps.removeTitle")}
          onClose={() => setRemove(null)}
          footer={
            <>
              <Button variant="outline" onClick={() => setRemove(null)}>
                {t("common.cancel")}
              </Button>
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() =>
                  void mutate(async () => {
                    await credentialAPI.remove(remove.account_id);
                    setRemove(null);
                  })
                }
              >
                {t("credentialOps.remove")}
              </Button>
            </>
          }
        >
          <p className="text-sm">
            {t("credentialOps.removeConfirm", { name: remove.name })}
          </p>
        </Modal>
      )}
      {showAdd && (
        <Modal
          show
          title={t("credentialOps.addExisting")}
          onClose={() => setShowAdd(false)}
          contentClassName="sm:max-w-[700px]"
        >
          <div className="space-y-5">
            <Select
              value={addId}
              onValueChange={setAddId}
              aria-label={t("credentialOps.chooseAccount")}
              placeholder={t("credentialOps.chooseAccount")}
              options={available.map((account) => ({
                value: String(account.id),
                label: `${account.name || account.email} · #${account.id}`,
              }))}
            />
            <CredentialEncryptionSetup />
            {addId && (
              <CredentialConfigEditor
                key={addId}
                accountId={Number(addId)}
                email={
                  available.find((row) => row.id === Number(addId))?.email || ""
                }
                studioConfigured={!!data?.session_studio_configured}
                globalEngine={data?.runtime_settings.engine}
                onSaved={() =>
                  void mutate(async () => {
                    await credentialAPI.monitor(Number(addId), {
                      enabled: true,
                      auto_relogin: true,
                    });
                    setShowAdd(false);
                    setAddId("");
                  })
                }
              />
            )}
          </div>
        </Modal>
      )}
    </main>
  );
}

function RulesEditor({
  value,
  busy,
  onSave,
}: {
  value: CredentialRules;
  busy: boolean;
  onSave: (value: CredentialRules) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(value);
  const dirty = JSON.stringify(draft) !== JSON.stringify(value);
  return (
    <section className="max-w-3xl space-y-5 rounded-xl border border-border bg-card p-5">
      <h3 className="font-semibold">{t("credentialOps.rulesTitle")}</h3>
      <p className="text-sm leading-relaxed text-muted-foreground">
        {t("credentialOps.rulesHint")}
      </p>
      <div className="grid gap-5 sm:grid-cols-2">
        {(
          [
            { key: "interval_seconds", min: 60, max: 86400 },
            { key: "retry_seconds", min: 30, max: 86400 },
            { key: "failure_threshold", min: 1, max: 10 },
            { key: "cooldown_seconds", min: 60, max: 604800 },
          ] as const
        ).map((field) => (
          <label key={field.key} className="space-y-1.5 text-sm">
            <span>{t(`credentialOps.ruleFields.${field.key}`)}</span>
            <DraftNumberInput
              value={draft[field.key]}
              min={field.min}
              max={field.max}
              onValueChange={(number) =>
                setDraft((current) => ({ ...current, [field.key]: number }))
              }
            />
          </label>
        ))}
      </div>
      <Button disabled={busy || !dirty} onClick={() => void onSave(draft)}>
        <Save className="size-4" />
        {t("credentialOps.saveRules")}
      </Button>
    </section>
  );
}

function RuntimeEditor({
  value,
  worker,
  busy,
  onSave,
}: {
  value: CredentialRuntimeSettings;
  worker: CredentialOverview["worker"];
  busy: boolean;
  onSave: (value: CredentialRuntimeSettings) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(
    value ||
      ({
        engine: "local_worker",
        worker_concurrency: 3,
      } as CredentialRuntimeSettings),
  );
  const [headers, setHeaders] = useState("");
  const [error, setError] = useState("");
  async function save() {
    let parsed: Record<string, string> | undefined;
    if (headers.trim()) {
      try {
        const object = JSON.parse(headers);
        if (
          !object ||
          typeof object !== "object" ||
          Array.isArray(object) ||
          Object.values(object).some((value) => typeof value !== "string")
        )
          throw new Error();
        parsed = object;
      } catch {
        setError(t("credentialOps.headersInvalid"));
        return;
      }
    }
    setError("");
    await onSave({
      ...draft,
      ...(parsed ? { session_studio_headers: parsed } : {}),
    });
    setHeaders("");
  }
  return (
    <section className="max-w-3xl space-y-5 rounded-xl border border-border bg-card p-5">
      <h3 className="font-semibold">{t("credentialOps.runtimeTitle")}</h3>
      <label className="flex items-center gap-2 text-sm">
        <Switch
          checked={draft.enabled !== false}
          onCheckedChange={(enabled) =>
            setDraft((current) => ({ ...current, enabled }))
          }
        />
        {t("credentialOps.automationEnabled")}
      </label>
      <CredentialEncryptionSetup />
      <div className="grid gap-5 sm:grid-cols-2">
        <label className="space-y-1.5 text-sm">
          <span>{t("credentialOps.engine")}</span>
          <Select
            value={draft.engine || "local_worker"}
            onValueChange={(engine) =>
              setDraft((current) => ({
                ...current,
                engine: engine as CredentialRuntimeSettings["engine"],
              }))
            }
            options={[
              { value: "local_worker", label: t("credentialOps.localWorker") },
              { value: "session_studio", label: "Session Studio" },
            ]}
          />
        </label>
        <label className="space-y-1.5 text-sm">
          <span>{t("credentialOps.workerConcurrency")}</span>
          <DraftNumberInput
            value={draft.worker_concurrency}
            min={1}
            max={16}
            onValueChange={(worker_concurrency) =>
              setDraft((current) => ({ ...current, worker_concurrency }))
            }
          />
        </label>
        <label className="space-y-1.5 text-sm sm:col-span-2">
          <span>{t("credentialOps.studioEndpoint")}</span>
          <Input
            type="url"
            autoComplete="off"
            spellCheck={false}
            value={draft.session_studio_endpoint || ""}
            onChange={(event) =>
              setDraft((current) => ({
                ...current,
                session_studio_endpoint: event.target.value,
              }))
            }
            placeholder="https://"
          />
        </label>
        <label className="space-y-1.5 text-sm sm:col-span-2">
          <span>{t("credentialOps.studioHeaders")}</span>
          <Textarea
            value={headers}
            onChange={(event) => setHeaders(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder={t("credentialOps.keepSecret")}
          />
        </label>
      </div>
      <p className="text-sm leading-relaxed text-muted-foreground">
        {draft.engine === "session_studio"
          ? t("credentialOps.remoteHint")
          : t("credentialOps.fixedEgressHint")}
      </p>
      <div className="space-y-1 text-sm text-muted-foreground">
        <p>
          {t("credentialOps.worker")}:{" "}
          {t(`credentialOps.workerStates.${worker?.state || "unavailable"}`, {
            defaultValue: worker?.state || "—",
          })}
        </p>
        {worker?.reason && <p>{worker.reason}</p>}
        {worker?.version && (
          <p>
            {t("credentialOps.version")}: {worker.version}
          </p>
        )}
        <p>
          {t("credentialOps.workerCapabilities")}:{" "}
          {worker?.capabilities?.length
            ? worker.capabilities
                .map((value) =>
                  t(`credentialOps.capabilities.${value}`, {
                    defaultValue: value,
                  }),
                )
                .join(" · ")
            : t("credentialOps.capabilitiesUnknown")}
        </p>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <Button disabled={busy} onClick={() => void save()}>
        <Save className="size-4" />
        {t("credentialOps.saveRuntime")}
      </Button>
      <CredentialMihomoSettings />
    </section>
  );
}
