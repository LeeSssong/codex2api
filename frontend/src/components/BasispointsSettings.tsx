import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select } from "./ui/select";
import { Switch } from "./ui/switch";
import {
  bpsSettingsUpdate,
  parseBpsModels,
  type BasispointsSettings as Settings,
} from "../lib/basispointsPolicy";

export function BasispointsSettingsEditor() {
  const { t } = useTranslation();
  const [settings, setSettings] = useState<Settings>();
  const [models, setModels] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  useEffect(() => {
    let active = true;
    void api
      .getBasispointsSettings()
      .then((value) => {
        if (active) {
          setSettings(value);
          setModels(value.models.join(", "));
        }
      })
      .catch(() => {
        if (active) setError(t("bps.loadFailed"));
      });
    return () => {
      active = false;
    };
  }, [t]);
  async function save(patch: Partial<Settings>) {
    if (busy) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const latest = await api.getBasispointsSettings();
      const saved = await api.updateBasispointsSettings(
        bpsSettingsUpdate(latest, patch),
      );
      setSettings((current) => ({
        ...saved,
        model_scope:
          patch.model_scope ?? current?.model_scope ?? saved.model_scope,
        image_relay_public_origin:
          patch.image_relay_public_origin ??
          current?.image_relay_public_origin ??
          saved.image_relay_public_origin,
      }));
      if (patch.models !== undefined) setModels(saved.models.join(", "));
      setNotice(t("bps.saved"));
    } catch {
      setError(t("bps.saveFailed"));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="mt-4 space-y-4 border-t border-border pt-4">
      {settings && (
        <>
          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="bps-global-scope">
              {t("bps.globalModels")}
            </label>
            <Select
              id="bps-global-scope"
              aria-label={t("bps.globalModels")}
              value={settings.model_scope}
              disabled={busy}
              onValueChange={(value) =>
                setSettings({
                  ...settings,
                  model_scope: value as Settings["model_scope"],
                })
              }
              options={["all", "selected"].map((value) => ({
                value,
                label: t("bps." + value),
              }))}
            />
            {settings.model_scope === "selected" && (
              <Input
                aria-label={t("bps.models")}
                value={models}
                disabled={busy}
                onChange={(e) => setModels(e.target.value)}
                placeholder={t("bps.modelsHint")}
              />
            )}
            <p className="text-xs text-muted-foreground">
              {t("bps.globalHint")}
            </p>
            {settings.model_scope === "selected" &&
              !parseBpsModels(models).length && (
                <p className="text-xs text-muted-foreground">{t("bps.none")}</p>
              )}
            <Button
              type="button"
              size="sm"
              disabled={busy}
              onClick={() =>
                void save({
                  model_scope: settings.model_scope,
                  models: parseBpsModels(models),
                })
              }
            >
              {t("bps.saveModels")}
            </Button>
          </div>
          <div className="space-y-3 border-t border-border pt-4">
            <h4 className="text-sm font-semibold">{t("bps.relay")}</h4>
            <div className="flex items-center justify-between gap-3">
              <div>
                <p className="text-sm">{t("bps.relayEnabled")}</p>
                <p className="text-xs text-muted-foreground">
                  {t("bps.relayHint")}
                </p>
              </div>
              <Switch
                aria-label={t("bps.relayEnabled")}
                checked={settings.image_relay_enabled}
                disabled={busy}
                onCheckedChange={(checked) =>
                  void save({ image_relay_enabled: checked })
                }
              />
            </div>
            <div className="space-y-2">
              <label className="text-xs font-medium" htmlFor="bps-origin">
                {t("bps.origin")}
              </label>
              <Input
                id="bps-origin"
                aria-label={t("bps.origin")}
                value={settings.image_relay_public_origin}
                disabled={busy}
                onChange={(e) =>
                  setSettings({
                    ...settings,
                    image_relay_public_origin: e.target.value,
                  })
                }
              />
              <p className="text-xs text-muted-foreground">
                {t("bps.originHint")}
              </p>
              <Button
                type="button"
                size="sm"
                disabled={busy}
                onClick={() =>
                  void save({
                    image_relay_public_origin:
                      settings.image_relay_public_origin.trim(),
                  })
                }
              >
                {t("bps.saveOrigin")}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {t("bps.relayState")}:{" "}
              {settings.image_relay_runtime
                ? t(
                    "bps.runtimeStates." +
                      settings.image_relay_runtime.validation_status,
                  )
                : t("bps.runtimeUnavailable")}
              . {t("bps.epoch")}: {settings.image_relay_epoch}
            </p>
            <div className="space-y-2 rounded-lg border border-border bg-muted/30 p-3">
              <h5 className="text-xs font-medium">{t("bps.limits")}</h5>
              {settings.image_relay_runtime ? (
                <>
                  <p className="text-xs text-muted-foreground">
                    {t("bps.configSource")}:{" "}
                    {t("bps.sources." + settings.image_relay_runtime.source)} ·{" "}
                    {t("bps.backend")}: {settings.image_relay_runtime.backend}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {t("bps.signingKey")}:{" "}
                    {t(
                      settings.image_relay_runtime.signing_key_configured
                        ? "bps.configured"
                        : "bps.missing",
                    )}
                  </p>
                  <dl className="grid grid-cols-1 gap-2 text-xs sm:grid-cols-2">
                    {Object.entries({
                      max_image_bytes:
                        settings.image_relay_runtime.max_image_bytes,
                      max_request_bytes:
                        settings.image_relay_runtime.max_request_bytes,
                      max_images: settings.image_relay_runtime.max_images,
                      max_pixels: settings.image_relay_runtime.max_pixels,
                      ttl_seconds: settings.image_relay_runtime.ttl_seconds,
                      max_slots: settings.image_relay_runtime.max_slots,
                      active_slots: settings.image_relay_runtime.active_slots,
                      request_memory_limit_bytes:
                        settings.image_relay_runtime.request_memory_limit_bytes,
                      request_body_limit_bytes:
                        settings.image_relay_runtime.request_body_limit_bytes,
                    }).map(([key, value]) => (
                      <div key={key}>
                        <dt className="text-muted-foreground">
                          {t("bps.runtimeFields." + key)}
                        </dt>
                        <dd className="font-mono">
                          {typeof value === "number"
                            ? value.toLocaleString()
                            : t("bps.unknown")}
                        </dd>
                      </div>
                    ))}
                  </dl>
                </>
              ) : (
                <p className="text-xs text-muted-foreground">
                  {t("bps.runtimeUnavailable")}
                </p>
              )}
              <h5 className="text-xs font-medium">{t("bps.usage")}</h5>
              {settings.image_relay_usage ? (
                <dl className="grid grid-cols-1 gap-2 text-xs sm:grid-cols-2">
                  {Object.entries(settings.image_relay_usage)
                    .filter(([key]) =>
                      [
                        "bytes",
                        "assets",
                        "reserved_bytes",
                        "reserved_assets",
                        "cleanup_pending",
                        "rejections",
                        "cleanup_errors",
                        "max_bytes",
                        "max_assets",
                      ].includes(key),
                    )
                    .map(([key, value]) => (
                      <div key={key}>
                        <dt className="text-muted-foreground">
                          {t("bps.usageFields." + key)}
                        </dt>
                        <dd className="font-mono">
                          {typeof value === "number"
                            ? value.toLocaleString()
                            : t("bps.unknown")}
                        </dd>
                      </div>
                    ))}
                </dl>
              ) : (
                <p className="text-xs text-muted-foreground">
                  {t("bps.usageUnavailable")}
                </p>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {t("bps.imageBoundary")}
            </p>
          </div>
        </>
      )}
      {!settings && !error && (
        <p className="text-xs text-muted-foreground">{t("bps.loading")}</p>
      )}
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="text-xs text-muted-foreground">
          {notice}
        </p>
      )}
    </div>
  );
}
