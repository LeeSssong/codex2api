import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select } from "./ui/select";
import { Switch } from "./ui/switch";
import {
  bpsEffectiveModels,
  bpsPolicyPatch,
  type BasispointsPolicy as Policy,
  type BasispointsSettings,
  type BpsScope,
} from "../lib/basispointsPolicy";

export function BasispointsPolicyEditor({
  ids,
  onChanged,
}: {
  ids: number[];
  onChanged?: () => void;
}) {
  const { t } = useTranslation();
  const [policy, setPolicy] = useState<Policy>();
  const [global, setGlobal] = useState<BasispointsSettings>();
  const [models, setModels] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [batchConfirmed, setBatchConfirmed] = useState(false);
  const [version, setVersion] = useState(0);
  const idsKey = ids.join(",");
  useEffect(() => {
    let current = true;
    setPolicy(undefined);
    setGlobal(undefined);
    setBatchConfirmed(false);
    setError("");
    if (!ids.length || ids.length > 100) return;
    void Promise.all([
      api.getBasispointsSettings(),
      ...ids.map((id) => api.getCodexRoutes(id)),
    ])
      .then(([settings, ...accounts]) => {
        if (!current) return;
        setGlobal(settings);
        const first = accounts[0]?.basispoints_policy;
        if (!first || accounts.some((account) => !account.basispoints_policy)) {
          setError(t("bps.unavailable"));
          return;
        }
        // A mixed batch requires a deliberate choice before overwriting any account.
        const mixed = accounts.some(
          (account) =>
            JSON.stringify(
              account.basispoints_policy &&
                bpsPolicyPatch(
                  account.basispoints_policy.model_scope,
                  account.basispoints_policy.models.join(","),
                  account.basispoints_policy.auto_disable_on_403,
                  account.basispoints_policy.cache_creation_as_input,
                ),
            ) !==
            JSON.stringify(
              bpsPolicyPatch(
                first.model_scope,
                first.models.join(","),
                first.auto_disable_on_403,
                first.cache_creation_as_input,
              ),
            ),
        );
        setPolicy(first);
        setModels(first.models.join(", "));
        if (mixed) setNotice(t("bps.mixed"));
      })
      .catch(() => {
        if (current) setError(t("bps.loadFailed"));
      });
    return () => {
      current = false;
    };
  }, [idsKey, version, t]);
  async function save(restore = false) {
    if (!policy || busy || (ids.length > 1 && !batchConfirmed)) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await api.updateCodexRoutes({
        ids,
        upstream: "basispoints",
        ...(restore
          ? { allowed: true }
          : {
              basispoints_policy: bpsPolicyPatch(
                policy.model_scope,
                models,
                policy.auto_disable_on_403,
                policy.cache_creation_as_input,
              ),
            }),
      });
      onChanged?.();
      setVersion((x) => x + 1);
      setNotice(t(restore ? "bps.restored" : "bps.saved"));
    } catch {
      setError(t("bps.saveFailed"));
    } finally {
      setBusy(false);
    }
  }
  const effective =
    policy && global
      ? bpsEffectiveModels(
          { ...policy, models: models.split(/[\s,]+/).filter(Boolean) },
          global,
        )
      : undefined;
  return (
    <section
      className="space-y-3 rounded-lg border border-border p-3"
      aria-label={t("bps.policy")}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-sm font-semibold">{t("bps.policy")}</h4>
        <a
          className="text-xs text-primary underline-offset-4 hover:underline"
          href="/admin/smart-ops/alerts"
        >
          {t("bps.events")}
        </a>
      </div>
      {policy && (
        <>
          <p className="text-xs text-muted-foreground">
            {t("bps.source")}:{" "}
            {t(
              policy.model_scope === "inherit"
                ? "bps.inherit"
                : "bps.accountOverride",
            )}
          </p>
          <Select
            aria-label={t("bps.scope")}
            value={policy.model_scope}
            disabled={busy}
            onValueChange={(value) =>
              setPolicy({ ...policy, model_scope: value as BpsScope })
            }
            options={["inherit", "all", "selected"].map((value) => ({
              value,
              label: t("bps." + value),
            }))}
          />
          {policy.model_scope === "selected" && (
            <Input
              aria-label={t("bps.models")}
              value={models}
              disabled={busy}
              onChange={(e) => setModels(e.target.value)}
              placeholder={t("bps.modelsHint")}
            />
          )}
          <p className="text-xs text-muted-foreground">
            {t("bps.effective")}:{" "}
            {effective === null
              ? t("bps.allBounded")
              : effective?.length
                ? effective.join(", ")
                : t("bps.none")}
          </p>
          <p className="text-xs text-muted-foreground">
            {t("bps.modelBoundary")}
          </p>
          <div className="flex items-center justify-between gap-3">
            <div>
              <p className="text-sm">{t("bps.auto403")}</p>
              <p className="text-xs text-muted-foreground">
                {t("bps.auto403Hint")}
              </p>
            </div>
            <Switch
              aria-label={t("bps.auto403")}
              disabled={busy}
              checked={policy.auto_disable_on_403}
              onCheckedChange={(checked) =>
                setPolicy({ ...policy, auto_disable_on_403: checked })
              }
            />
          </div>
          <div className="flex items-center justify-between gap-3">
            <div>
              <p className="text-sm">{t("bps.cache")}</p>
              <p className="text-xs text-muted-foreground">
                {t("bps.cacheHint")}
              </p>
            </div>
            <Switch
              aria-label={t("bps.cache")}
              disabled={busy}
              checked={policy.cache_creation_as_input}
              onCheckedChange={(checked) =>
                setPolicy({ ...policy, cache_creation_as_input: checked })
              }
            />
          </div>
          {ids.length > 1 && (
            <label className="flex items-center gap-2 text-xs">
              <input
                type="checkbox"
                checked={batchConfirmed}
                disabled={busy}
                onChange={(e) => setBatchConfirmed(e.target.checked)}
              />
              {t("bps.confirmBatch")}
            </label>
          )}
          <Button
            type="button"
            size="sm"
            disabled={busy || (ids.length > 1 && !batchConfirmed)}
            onClick={() => void save()}
          >
            {t("bps.savePolicy")}
          </Button>
          {ids.length === 1 && policy.disabled_by && (
            <div className="space-y-2 rounded-md border border-border bg-muted/40 p-3 text-xs">
              <p>
                {t("bps.disabledBy")}: {policy.disabled_by}
              </p>
              <p>
                {t("bps.reason")}: {policy.disabled_reason || t("bps.unknown")}
              </p>
              {policy.disabled_at && (
                <time>
                  {new Date(policy.disabled_at * 1000).toLocaleString()}
                </time>
              )}
              <p>{t("bps.restoreHint")}</p>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => void save(true)}
              >
                {t("bps.restore")}
              </Button>
            </div>
          )}
        </>
      )}
      {!policy && !error && (
        <p className="text-xs text-muted-foreground">{t("bps.loading")}</p>
      )}
      {error && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
      {notice && (
        <p className="text-xs text-muted-foreground" role="status">
          {notice}
        </p>
      )}
    </section>
  );
}
