import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ArrowUpRight, Loader2 } from 'lucide-react'
import { api, type PluginManifest } from '../api'
import PageHeader from '../components/PageHeader'
import StateShell from '../components/StateShell'
import { Switch } from '../components/ui/switch'
import { Badge } from '../components/ui/badge'
import { useDataLoader } from '../hooks/useDataLoader'
import { useToast } from '../hooks/useToast'
import { getErrorMessage } from '../utils/error'

const routes: Record<string, string> = {
  'auto-config': '/smart-ops/auto-config',
  'priority-scheduling': '/smart-ops/priority',
  'quality-ops': '/smart-ops/quality',
  'account-ops': '/smart-ops/alerts',
  'token-guard': '/smart-ops/tokens',
  'credential-ops': '/smart-ops/credentials',
  'pelican-tests': '/smart-ops/pelican',
}

export default function Plugins() {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const [saving, setSaving] = useState<string | null>(null)
  const load = useCallback(() => api.getPlugins(), [])
  const { data, loading, error, reload, reloadSilently } = useDataLoader({ initialData: { plugins: [] as PluginManifest[] }, load })
  async function toggle(plugin: PluginManifest, enabled: boolean) {
    setSaving(plugin.id)
    try {
      await api.updatePlugin(plugin.id, enabled)
      await reloadSilently()
      showToast(t(enabled ? 'pluginManager.enabledSaved' : 'pluginManager.disabledSaved'))
    } catch (cause) {
      showToast(getErrorMessage(cause), 'error')
    } finally {
      setSaving(null)
    }
  }
  return <>
    <PageHeader title={t('pluginManager.title')} description={t('pluginManager.description')} onRefresh={() => void reload()} />
    <StateShell variant="page" loading={loading && data.plugins.length === 0} error={error} onRetry={() => void reload()}>
      <div className="divide-y rounded-xl border bg-card">
        {data.plugins.map(plugin => <section key={plugin.id} className="flex flex-col gap-4 p-5 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="font-medium">{t(`pluginManager.names.${plugin.id}`, { defaultValue: plugin.id })}</h3>
              <Badge variant={plugin.enabled ? 'default' : 'secondary'}>{t(plugin.enabled ? 'pluginManager.enabled' : 'pluginManager.disabled')}</Badge>
              <span className="font-mono text-xs text-muted-foreground">v{plugin.version}</span>
            </div>
            <p className="mt-1 text-sm text-muted-foreground">{t(`pluginManager.descriptions.${plugin.id}`, { defaultValue: '' })}</p>
            <p className="mt-2 text-xs text-muted-foreground">{t(plugin.update_mode === 'runtime' ? 'pluginManager.runtimeUpdate' : 'pluginManager.compiledUpdate')}</p>
          </div>
          <div className="flex shrink-0 items-center justify-between gap-6 sm:justify-end">
            {routes[plugin.id] && <Link className="inline-flex items-center gap-1 rounded text-sm text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring" to={routes[plugin.id]}>{t('pluginManager.configure')}<ArrowUpRight className="size-4" /></Link>}
            <div className="flex items-center gap-2">
              {saving === plugin.id && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
              <Switch checked={plugin.enabled} disabled={saving !== null} aria-label={t('pluginManager.toggle', { name: t(`pluginManager.names.${plugin.id}`) })} onCheckedChange={enabled => void toggle(plugin, enabled)} />
            </div>
          </div>
        </section>)}
      </div>
      <p className="mt-4 text-sm text-muted-foreground">{t('pluginManager.retention')}</p>
    </StateShell>
  </>
}
