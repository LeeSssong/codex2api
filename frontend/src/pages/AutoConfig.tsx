import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Save, Trash2, RefreshCw } from 'lucide-react'
import PageHeader from '../components/PageHeader'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { SmartOpsSection, SmartOpsField, SmartOpsSwitch, SmartOpsNumber, SmartOpsGroups, SmartOpsModelList, SETTINGS_FIELD_GRID } from '../components/SmartOpsFields'
import { api, getSmartOpsConfig, putOAuthAutoConfig } from '../api'
import type { AccountGroup } from '../types'
import type { OAuthAutoConfig, BPSDefaults, Quality5xxConfig } from '../lib/smartOps'

export default function AutoConfig() {
  const { t } = useTranslation()
  const [config, setConfig] = useState<OAuthAutoConfig | null>(null)
  const [saved, setSaved] = useState('')
  const [groups, setGroups] = useState<AccountGroup[]>([])
  const [enabled, setEnabled] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  async function load() {
    try { const [data, g] = await Promise.all([getSmartOpsConfig(), api.listAccountGroups()]); setConfig(data.oauth_auto_config); setSaved(JSON.stringify(data.oauth_auto_config)); setGroups(g.groups); setEnabled(data.plugins['auto-config']); setMessage('') }
    catch (e) { setMessage((e as Error).message) }
  }
  useEffect(() => { void load() }, [])
  async function save() {
    if (!config) return
    setBusy(true)
    try { await putOAuthAutoConfig(config); const data = await getSmartOpsConfig(); setConfig(data.oauth_auto_config); setSaved(JSON.stringify(data.oauth_auto_config)); setMessage(t('smartOps.saved')) }
    catch (e) { setMessage((e as Error).message) }
    finally { setBusy(false) }
  }
  const dirty = config && saved !== JSON.stringify(config)
  const disabled = !enabled || busy
  const patch = (p: Partial<OAuthAutoConfig>) => setConfig(v => v && { ...v, ...p })
  const bps = (p: Partial<BPSDefaults>) => config && patch({ bps: { ...config.bps, ...p } })
  const quality = config?.quality_5xx ?? { enabled: false, floor: 5, cooldown_seconds: 300, models: [] }
  const patchQuality = (p: Partial<Quality5xxConfig>) => patch({ quality_5xx: { ...quality, ...p } })
  const channel = config?.platform === 'openai' ? 'codex' : config?.platform
  const initialGroups = groups.filter(g => (g.channel || 'codex') === channel)
  const bpsSwitches: (keyof BPSDefaults)[] = ['ws_sse_acceleration', 'auto_enable_on_degradation', 'all_models', 'omit_unsupported_tools', 'ignore_encrypted_content', 'auto_disable_on_403', 'auto_recover_on_403', 'auto_move_on_403', 'session_proxy', 'cache_creation_as_input']
  return <div className="mx-auto w-full max-w-[1180px] space-y-6">
    <PageHeader title={t('smartOps.autoTitle')} onRefresh={() => void load()} actions={<Button onClick={() => void save()} disabled={disabled || !dirty}>{busy ? <RefreshCw className="size-4 animate-spin" /> : <Save className="size-4" />}{t('common.save')}</Button>} />
    {message && <p role="status" className="text-sm text-muted-foreground">{message}</p>}
    {config && !enabled && <p className="text-sm text-muted-foreground">{t('smartOps.disabled')}</p>}
    {!config ? <p className="text-sm text-muted-foreground">{t('common.loading')}</p> : <>
      <SmartOpsSection title={t('smartOps.initialDefaults')}>
        <SmartOpsSwitch label={t('smartOps.autoEnabled')} value={config.enabled} onChange={v => patch({ enabled: v })} disabled={disabled} />
        <div className={SETTINGS_FIELD_GRID}>
          <SmartOpsField label={t('smartOps.platform')}><Select aria-label={t('smartOps.platform')} value={config.platform} onValueChange={v => patch({ platform: v, group_ids: [] })} disabled={disabled} options={['openai', 'claude', 'grok', 'antigravity'].map(value => ({ value, label: t(`smartOps.platforms.${value}`) }))} /></SmartOpsField>
          <SmartOpsField label={t('smartOps.groups')}><SmartOpsGroups groups={initialGroups} value={config.group_ids} onChange={v => patch({ group_ids: v })} disabled={disabled} /></SmartOpsField>
          <SmartOpsNumber label={t('smartOps.priority')} value={config.priority} min={0} onChange={v => patch({ priority: v })} disabled={disabled} />
          <SmartOpsNumber label={t('smartOps.concurrency')} value={config.concurrency} onChange={v => patch({ concurrency: v })} disabled={disabled} />
          <SmartOpsNumber label={t('smartOps.load_factor')} value={config.load_factor} onChange={v => patch({ load_factor: v })} disabled={disabled} />
        </div>
      </SmartOpsSection>
      <SmartOpsSection title={t('smartOps.modelMappings')}>
        {config.model_mappings.map((rule, index) => <div key={index} className="flex flex-wrap items-center gap-2">
          <Input className="min-w-36 flex-1" aria-label={t('smartOps.sourceModel')} value={rule.from} disabled={disabled} onChange={e => patch({ model_mappings: config.model_mappings.map((r, i) => i === index ? { ...r, from: e.target.value } : r) })} />
          <Input className="min-w-36 flex-1" aria-label={t('smartOps.targetModel')} value={rule.to} disabled={disabled} onChange={e => patch({ model_mappings: config.model_mappings.map((r, i) => i === index ? { ...r, to: e.target.value } : r) })} />
          <Button variant="ghost" size="icon" title={t('common.delete')} aria-label={t('common.delete')} disabled={disabled} onClick={() => patch({ model_mappings: config.model_mappings.filter((_, i) => i !== index) })}><Trash2 className="size-4" /></Button>
        </div>)}
        <Button variant="outline" disabled={disabled} onClick={() => patch({ model_mappings: [...config.model_mappings, { from: '', to: '' }] })}><Plus className="size-4" />{t('smartOps.addRule')}</Button>
      </SmartOpsSection>
      <SmartOpsSection title={t('smartOps.concurrencyUpgrade')}>
        <SmartOpsSwitch label={t('smartOps.upgrade_enabled')} value={config.upgrade_enabled} onChange={v => patch({ upgrade_enabled: v })} disabled={disabled} />
        <SmartOpsField label={t('smartOps.upgradeGroups')}><SmartOpsGroups groups={groups} value={config.upgrade_group_ids || []} onChange={v => patch({ upgrade_group_ids: v })} disabled={disabled} /></SmartOpsField>
        <div className={SETTINGS_FIELD_GRID}>
          <SmartOpsNumber label={t('smartOps.successes_per_step')} value={config.successes_per_step} max={100000} onChange={v => patch({ successes_per_step: v })} disabled={disabled} />
          <SmartOpsNumber label={t('smartOps.upgrade_step')} value={config.upgrade_step} max={1000} onChange={v => patch({ upgrade_step: v })} disabled={disabled} />
          <SmartOpsNumber label={t('smartOps.max_concurrency')} value={config.max_concurrency} onChange={v => patch({ max_concurrency: v })} disabled={disabled} />
          <SmartOpsNumber label={t('smartOps.cooldown_seconds')} value={config.cooldown_seconds} max={86400} onChange={v => patch({ cooldown_seconds: v })} disabled={disabled} />
        </div>
      </SmartOpsSection>
      <SmartOpsSection title={t('smartOps.quality5xx.title')}>
        <SmartOpsSwitch label={t('smartOps.quality5xx.enabled')} value={quality.enabled} onChange={v => patchQuality({ enabled: v })} disabled={disabled} />
        <p className="text-sm text-muted-foreground">{t('smartOps.quality5xx.description')}</p>
        <div className={SETTINGS_FIELD_GRID}>
          <SmartOpsNumber label={t('smartOps.quality5xx.floor')} value={quality.floor} onChange={v => patchQuality({ floor: v })} disabled={disabled} />
          <SmartOpsNumber label={t('smartOps.quality5xx.interval')} value={quality.cooldown_seconds} max={86400} onChange={v => patchQuality({ cooldown_seconds: v })} disabled={disabled} />
          <SmartOpsField label={t('smartOps.quality5xx.models')}><SmartOpsModelList label={t('smartOps.quality5xx.models')} value={quality.models || []} onChange={v => patchQuality({ models: v })} disabled={disabled} /></SmartOpsField>
        </div>
      </SmartOpsSection>
      <SmartOpsSection title={t('smartOps.bpsDefaults')}>
        <div className={SETTINGS_FIELD_GRID}>{bpsSwitches.map(key => <SmartOpsSwitch key={key} label={key === 'ws_sse_acceleration' ? t('smartOps.wsUnavailable') : t(`smartOps.bps.${key}`)} value={Boolean(config.bps[key])} onChange={v => bps(key === 'session_proxy' && v ? { session_proxy: true, proxy_source: 'ip_pool' } : { [key]: v })} disabled={disabled || key === 'ws_sse_acceleration'} />)}</div>
        <div className={SETTINGS_FIELD_GRID}>
          <SmartOpsField label={t('smartOps.models')}><SmartOpsModelList label={t('smartOps.models')} disabled={disabled || config.bps.all_models} value={config.bps.models} onChange={v => bps({ models: v })} /></SmartOpsField>
          <SmartOpsNumber label={t('smartOps.recovery_interval_minutes')} value={config.bps.recovery_interval_minutes} max={10080} onChange={v => bps({ recovery_interval_minutes: v })} disabled={disabled} />
          <SmartOpsField label={t('smartOps.bps.target_group_id')}><Select aria-label={t('smartOps.bps.target_group_id')} value={String(config.bps.target_group_id)} onValueChange={v => bps({ target_group_id: Number(v) })} disabled={disabled} options={[{ value: '-1', label: t('smartOps.none') }, ...groups.filter(g => (g.channel || 'codex') === 'codex').map(g => ({ value: String(g.id), label: g.name }))]} /></SmartOpsField>
          <SmartOpsField label={t('smartOps.bps.proxy_source')}><Select aria-label={t('smartOps.bps.proxy_source')} value={config.bps.session_proxy ? 'ip_pool' : config.bps.proxy_source} onValueChange={v => bps({ proxy_source: v })} disabled={disabled || config.bps.session_proxy} options={[{ value: 'mihomo', label: t('smartOps.mihomoUnavailable') }, { value: 'ip_pool', label: t('smartOps.proxyPool') }]} /></SmartOpsField>
        </div>
      </SmartOpsSection>
      <SmartOpsSection title={t('smartOps.modelBilling')}>
        <SmartOpsSwitch label={t('smartOps.billingEnabled')} value={config.model_billing?.enabled || false} onChange={v => patch({ model_billing: { ...config.model_billing, enabled: v } })} disabled={disabled} />
        {(config.model_billing?.rules || []).map((rule, index) => <div key={index} className="flex flex-wrap items-end gap-2">
          <div className="min-w-36 flex-1"><SmartOpsField label={t('smartOps.modelPattern')}><Input value={rule.model} aria-label={t('smartOps.modelPattern')} disabled={disabled} onChange={e => patch({ model_billing: { ...config.model_billing, rules: config.model_billing.rules.map((r, i) => i === index ? { ...r, model: e.target.value } : r) } })} /></SmartOpsField></div>
          <SmartOpsNumber label={t('smartOps.multiplier')} value={rule.multiplier} min={1} max={1000} integer={false} disabled={disabled} onChange={v => patch({ model_billing: { ...config.model_billing, rules: config.model_billing.rules.map((r, i) => i === index ? { ...r, multiplier: v } : r) } })} />
          <Button variant="ghost" size="icon" title={t('common.delete')} aria-label={t('common.delete')} disabled={disabled} onClick={() => patch({ model_billing: { ...config.model_billing, rules: config.model_billing.rules.filter((_, i) => i !== index) } })}><Trash2 className="size-4" /></Button>
        </div>)}
        <Button variant="outline" disabled={disabled} onClick={() => patch({ model_billing: { enabled: config.model_billing?.enabled || false, rules: [...(config.model_billing?.rules || []), { model: '', multiplier: 1 }] } })}><Plus className="size-4" />{t('smartOps.addRule')}</Button>
      </SmartOpsSection>
    </>}
  </div>
}
