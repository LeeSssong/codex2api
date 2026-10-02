import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Save, RefreshCw } from 'lucide-react'
import PageHeader from '../components/PageHeader'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { SmartOpsField, SmartOpsSection, SmartOpsSwitch, SmartOpsNumber, SmartOpsGroups, SmartOpsModelList, SETTINGS_FIELD_GRID } from '../components/SmartOpsFields'
import { api, getSmartOpsConfig, putPriorityScheduling } from '../api'
import type { PriorityConfig } from '../lib/smartOps'
import type { AccountGroup } from '../types'

export default function PriorityScheduling() {
  const { t } = useTranslation()
  const [config, setConfig] = useState<PriorityConfig | null>(null)
  const [saved, setSaved] = useState('')
  const [enabled, setEnabled] = useState(false)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [groups, setGroups] = useState<AccountGroup[]>([])
  async function load() { try { const [d, g] = await Promise.all([getSmartOpsConfig(), api.listAccountGroups()]); setConfig(d.priority_scheduling); setSaved(JSON.stringify(d.priority_scheduling)); setEnabled(d.plugins['priority-scheduling']); setGroups(g.groups); setMessage('') } catch (e) { setMessage((e as Error).message) } }
  useEffect(() => { void load() }, [])
  async function save() { if (!config) return; setBusy(true); try { await putPriorityScheduling(config); setSaved(JSON.stringify(config)); setMessage(t('smartOps.saved')) } catch (e) { setMessage((e as Error).message) } finally { setBusy(false) } }
  const patch = (p: Partial<PriorityConfig>) => setConfig(v => v && { ...v, ...p })
  const disabled = !enabled || busy
  const thresholds: { key: keyof PriorityConfig; min: number; max: number }[] = [{ key: 'window_minutes', min: 5, max: 1440 }, { key: 'min_samples', min: 1, max: 100000 }, { key: 'target_ttft_ms', min: 100, max: 120000 }, { key: 'max_load_percent', min: 10, max: 100 }, { key: 'min_quality_percent', min: 0, max: 100 }]
  const weights: (keyof PriorityConfig)[] = ['quality_weight', 'latency_weight', 'load_weight', 'cost_weight']
  return <div className="space-y-6">
    <PageHeader title={t('smartOps.priorityTitle')} onRefresh={() => void load()} actions={<Button disabled={disabled || !config || saved === JSON.stringify(config)} onClick={() => void save()}>{busy ? <RefreshCw className="size-4 animate-spin" /> : <Save className="size-4" />}{t('common.save')}</Button>} />
    {message && <p role="status" className="text-sm text-muted-foreground">{message}</p>}
    {!config ? <p className="text-sm text-muted-foreground">{t('common.loading')}</p> : <>
      {!enabled && <p className="text-sm text-muted-foreground">{t('smartOps.disabled')}</p>}
      <SmartOpsSwitch label={t('smartOps.priorityEnabled')} value={config.enabled} onChange={v => patch({ enabled: v })} disabled={disabled} />
      <SmartOpsSwitch label={t('smartOps.balance_protocols')} value={config.balance_protocols} onChange={v => patch({ balance_protocols: v })} disabled={disabled} />
      <div className={SETTINGS_FIELD_GRID}>
        <SmartOpsField label={t('smartOps.groups')}><SmartOpsGroups groups={groups} value={config.group_ids || []} onChange={v => patch({ group_ids: v })} disabled={disabled} /></SmartOpsField>
        <SmartOpsField label={t('smartOps.models')}><SmartOpsModelList label={t('smartOps.models')} value={config.models || []} onChange={v => patch({ models: v })} disabled={disabled} /></SmartOpsField>
        <SmartOpsNumber label={t('smartOps.quality_max_age_hours')} value={config.quality_max_age_hours} min={1} max={168} onChange={v => patch({ quality_max_age_hours: v })} disabled={disabled} />
      </div>
      <SmartOpsField label={t('smartOps.mode')}><Select aria-label={t('smartOps.mode')} disabled={disabled} value={config.mode} onValueChange={v => patch({ mode: v })} options={['experience', 'balanced', 'profit', 'custom'].map(value => ({ value, label: t(`smartOps.modes.${value}`) }))} /></SmartOpsField>
      <SmartOpsSection title={t('smartOps.thresholds')}><div className={SETTINGS_FIELD_GRID}>{thresholds.map(({ key, min, max }) => <SmartOpsNumber key={key} label={t(`smartOps.${key}`)} value={Number(config[key])} min={min} max={max} onChange={v => patch({ [key]: v })} disabled={disabled} />)}</div></SmartOpsSection>
      {config.mode === 'custom' && <SmartOpsSection title={t('smartOps.weights')}><div className={SETTINGS_FIELD_GRID}>{weights.map(key => <SmartOpsNumber key={key} label={t(`smartOps.${key}`)} value={Number(config[key])} min={0} max={1000} integer={false} onChange={v => patch({ [key]: v })} disabled={disabled} />)}</div></SmartOpsSection>}
    </>}
  </div>
}
