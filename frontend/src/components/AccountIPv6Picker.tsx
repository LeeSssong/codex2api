import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { RefreshCw } from "lucide-react";
import { api } from "../api";
import type { IPv6EgressData } from "../lib/ipv6Egress";
import {
  ipv6ManualBlockReason,
  ipv6ManualOptions,
} from "../lib/accountIPv6Manual";
import { getErrorMessage } from "../utils/error";
import { useToast } from "../hooks/useToast";
import { Button } from "./ui/button";

export interface IPv6ManualActions {
  accountID: number;
  onAssign: (ip: string, expectedIP: string) => Promise<string>;
  onRefresh: (ip: string) => void;
}

export default function AccountIPv6Picker({
  accountID,
  onAssign,
  onRefresh,
  disabled = false,
}: IPv6ManualActions & { disabled?: boolean }) {
  const { t } = useTranslation();
  const { showToast } = useToast();
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<IPv6EgressData | null>(null);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [actionError, setActionError] = useState("");
  const onRefreshRef = useRef(onRefresh);
  onRefreshRef.current = onRefresh;

  async function load(signal?: AbortSignal) {
    setLoading(true);
    setLoadError("");
    try {
      const next = await api.getIPv6Egress(signal);
      if (signal?.aborted) return;
      setData(next);
      // The server snapshot is authoritative after a stale-binding conflict, including pending "".
      onRefreshRef.current(
        next.bindings.find((row) => row.account_id === accountID && row.reason !== "retired")?.ip ?? "",
      );
    } catch (error) {
      if (!signal?.aborted) setLoadError(getErrorMessage(error));
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [open, accountID]);

  const currentIP =
    data?.bindings.find((row) => row.account_id === accountID && row.reason !== "retired")?.ip ?? "";
  const options = data ? ipv6ManualOptions(data, accountID, currentIP) : [];
  const blockReason = data ? ipv6ManualBlockReason(data) : "";
  const busy = disabled || loading || saving;
  const blocked = busy || !data || Boolean(loadError) || Boolean(blockReason);
  const canAssign =
    !blocked &&
    options.some((row) => row.ip === selected && row.status === "available");

  async function assign() {
    if (!canAssign) return;
    setSaving(true);
    setActionError("");
    try {
      const actual = await onAssign(selected, currentIP);
      setSelected(actual);
      setData((previous) =>
        previous
          ? {
              ...previous,
              bindings: [
                ...previous.bindings.filter(
                  (row) => row.account_id !== accountID,
                ),
                {
                  account_id: accountID,
                  ip: actual,
                  changed_at: Date.now() / 1000,
                  reason: "manual",
                  rotations: 0,
                },
              ],
            }
          : previous,
      );
      showToast(t("accounts.ipv6ManualSuccess"));
      await load();
    } catch (error) {
      const conflict = Boolean(
        error &&
        typeof error === "object" &&
        "status" in error &&
        error.status === 409,
      );
      const message = conflict
        ? t("accounts.ipv6ManualConflict")
        : t("accounts.ipv6ManualFailed", { error: getErrorMessage(error) });
      setActionError(message);
      showToast(message, "error");
      // Retain the user's selection while refreshing both pool availability and expected_ip.
      await load();
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="min-w-0 space-y-2">
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={disabled || saving}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        {t("accounts.ipv6ManualChoose")}
      </Button>
      {open ? (
        <div className="min-w-0 space-y-2 rounded-md border border-border bg-muted/20 p-3">
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs font-medium">
              {t("accounts.ipv6ManualPool")}
            </span>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => void load()}
              aria-label={t("accounts.ipv6ManualRefresh")}
            >
              <RefreshCw
                className={`size-3.5 ${loading ? "animate-spin" : ""}`}
              />
              {t("accounts.ipv6ManualRefresh")}
            </Button>
          </div>
          {blockReason ? (
            <p role="status" className="text-xs text-destructive">
              {t(`accounts.ipv6ManualBlocked_${blockReason}`)}
            </p>
          ) : null}
          {loadError ? (
            <p role="alert" className="text-xs text-destructive">
              {t("accounts.ipv6ManualLoadFailed", { error: loadError })}
            </p>
          ) : null}
          {actionError ? (
            <p role="alert" className="text-xs text-destructive">
              {actionError}
            </p>
          ) : null}
          {!loading &&
          data &&
          !options.some((row) => row.status === "available") ? (
            <p className="text-xs text-muted-foreground">
              {t("accounts.ipv6ManualEmpty")}
            </p>
          ) : null}
          <p className="text-[11px] text-muted-foreground">{t("accounts.ipv6ManualScrollHint")}</p>
          <div
            className="max-h-48 space-y-1 overflow-y-auto"
            role="radiogroup"
            aria-label={t("accounts.ipv6ManualPool")}
          >
            {options.map((row) => (
              <label
                key={row.ip}
                className={`flex min-w-0 items-center gap-2 rounded border border-border/60 px-2 py-1.5 ${row.status === "available" && !blocked ? "cursor-pointer hover:bg-muted/60" : "text-muted-foreground"}`}
              >
                <input
                  type="radio"
                  name={`ipv6-address-${accountID}`}
                  aria-label={row.ip}
                  checked={selected === row.ip}
                  disabled={blocked || row.status !== "available"}
                  onChange={() => {
                    setSelected(row.ip);
                    setActionError("");
                  }}
                />
                <span
                  className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs"
                  tabIndex={0}
                >
                  {row.ip}
                </span>
                <span className="shrink-0 text-[11px]">
                  {t(`accounts.ipv6ManualStatus_${row.status}`)}
                </span>
              </label>
            ))}
          </div>
          {selected && !options.some((row) => row.ip === selected) ? (
            <p className="overflow-x-auto whitespace-nowrap font-mono text-xs text-muted-foreground">
              {t("accounts.ipv6ManualUnavailable", { ip: selected })}
            </p>
          ) : null}
          <Button
            type="button"
            size="sm"
            disabled={!canAssign}
            onClick={() => void assign()}
          >
            {t("accounts.ipv6ManualApply")}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
