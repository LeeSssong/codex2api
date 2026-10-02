import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Play, Square, Save, Pencil, Trash2, Plus, RefreshCw, Eye } from 'lucide-react'
import PageHeader from '../components/PageHeader'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Select } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { SmartOpsSection, SmartOpsField, SmartOpsNumber, SmartOpsGroups, SmartOpsSwitch, SETTINGS_FIELD_GRID } from '../components/SmartOpsFields'
import { api, getSmartOpsConfig, listPelicanTests, getPelicanTest, createPelicanTest, cancelPelicanTest, listPelicanPlans, savePelicanPlan, deletePelicanPlan, runPelicanPlan } from '../api'
import { extractQualityTestHTML } from '../lib/qualityTest'
import type { AccountGroup } from '../types'
import type { PelicanJob, PelicanRecord, PelicanPlan } from '../lib/smartOps'

const defaultJob: PelicanJob = { account_id: 0, group_ids: [], model: 'gpt-5.5', prompt: '', reasoning_effort: '', samples: 1, parallel: 1, retries: 1, max_history: 100 }

export default function PelicanTests() {
  const { t } = useTranslation()
  const [jobs, setJobs] = useState<PelicanRecord[]>([])
  const [plans, setPlans] = useState<PelicanPlan[]>([])
  const [groups, setGroups] = useState<AccountGroup[]>([])
  const [job, setJob] = useState<PelicanJob>(defaultJob)
  const [target, setTarget] = useState('groups')
  const [planID, setPlanID] = useState(0)
  const [name, setName] = useState('')
  const [interval, setPlanInterval] = useState(60)
  const [cron, setCron] = useState('')
  const [planEnabled, setPlanEnabled] = useState(true)
  const [enabled, setEnabled] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [selected, setSelected] = useState<PelicanRecord | null>(null)
  const selectedID = useRef(0)
  const [mode, setMode] = useState('manual')
  const load = useCallback(async () => {
    try { const [j, p, g, c] = await Promise.all([listPelicanTests(), listPelicanPlans(), api.listAccountGroups(), getSmartOpsConfig()]); setJobs(j.jobs); setPlans(p.plans); setGroups(g.groups); setEnabled(c.plugins['pelican-tests']); if (selectedID.current) setSelected(await getPelicanTest(selectedID.current)) }
    catch (e) { setMessage((e as Error).message) }
  }, [])
  useEffect(() => { void load(); const timer = setInterval(() => void load(), 3000); return () => clearInterval(timer) }, [load])
  const patch = (p: Partial<PelicanJob>) => setJob(v => ({ ...v, ...p }))
  const normalized = { ...job, account_id: target === 'account' ? job.account_id : 0, group_ids: target === 'groups' ? job.group_ids : [] }
  const valid = job.model.trim() && (target === 'account' ? job.account_id > 0 : job.group_ids.length > 0)
  async function act(operation: () => Promise<unknown>) { setBusy(true); setMessage(''); try { await operation(); await load() } catch (e) { setMessage((e as Error).message) } finally { setBusy(false) } }
  function edit(plan: PelicanPlan) { setPlanID(plan.id); setName(plan.name); setPlanInterval(plan.interval_minutes); setCron(plan.cron_expression || ''); setPlanEnabled(plan.enabled); setJob(plan.job); setTarget(plan.job.group_ids.length ? 'groups' : 'account'); setMode('scheduled') }
  const disabled = busy || !enabled
  return <div className="space-y-6">
    <PageHeader title={t('smartOps.pelicanTitle')} onRefresh={() => void load()} />
    {message && <p role="alert" className="text-sm text-destructive">{message}</p>}
    {!enabled && <p className="text-sm text-muted-foreground">{t('smartOps.disabled')}</p>}
    <div className={SETTINGS_FIELD_GRID}>
      <SmartOpsField label={t('smartOps.execution')}><Select aria-label={t('smartOps.execution')} value={mode} onValueChange={setMode} options={[{ value: 'manual', label: t('smartOps.manual') }, { value: 'scheduled', label: t('smartOps.scheduled') }]} /></SmartOpsField>
      <SmartOpsField label={t('smartOps.target')}><Select aria-label={t('smartOps.target')} value={target} onValueChange={setTarget} disabled={disabled} options={[{ value: 'groups', label: t('smartOps.groups') }, { value: 'account', label: t('smartOps.account') }]} /></SmartOpsField>
      {target === 'groups' ? <SmartOpsField label={t('smartOps.groups')}><SmartOpsGroups groups={groups} value={job.group_ids} onChange={v => patch({ group_ids: v })} disabled={disabled} /></SmartOpsField> : <SmartOpsNumber label={t('smartOps.accountID')} value={job.account_id} min={1} max={2147483647} disabled={disabled} onChange={v => patch({ account_id: v })} />}
      <SmartOpsField label={t('smartOps.model')}><Input aria-label={t('smartOps.model')} value={job.model} disabled={disabled} onChange={e => patch({ model: e.target.value })} /></SmartOpsField>
      <SmartOpsField label={t('smartOps.effort')}><Select aria-label={t('smartOps.effort')} disabled={disabled} value={job.reasoning_effort} onValueChange={v => patch({ reasoning_effort: v })} options={['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'].map(value => ({ value, label: value || t('smartOps.default') }))} /></SmartOpsField>
      <SmartOpsNumber label={t('smartOps.samples')} value={job.samples} min={1} max={100} disabled={disabled} onChange={v => patch({ samples: v, parallel: Math.min(v, job.parallel) })} />
      <SmartOpsNumber label={t('smartOps.parallel')} value={job.parallel} min={1} max={Math.min(10, job.samples)} disabled={disabled} onChange={v => patch({ parallel: v })} />
      <SmartOpsNumber label={t('smartOps.retries')} value={job.retries} min={0} max={5} disabled={disabled} onChange={v => patch({ retries: v })} />
    </div>
    <SmartOpsField label={t('smartOps.prompt')}><Textarea aria-label={t('smartOps.prompt')} maxLength={16000} disabled={disabled} value={job.prompt} onChange={e => patch({ prompt: e.target.value })} /></SmartOpsField>
    {mode === 'scheduled' && <SmartOpsSection title={t('smartOps.plan')}>
      <div className={SETTINGS_FIELD_GRID}>
        <SmartOpsField label={t('smartOps.name')}><Input aria-label={t('smartOps.name')} value={name} disabled={disabled} onChange={e => setName(e.target.value)} /></SmartOpsField>
        <SmartOpsNumber label={t('smartOps.interval')} value={interval} min={1} max={43200} disabled={disabled} onChange={setPlanInterval} />
        <SmartOpsField label={t('smartOps.cron')}><Input aria-label={t('smartOps.cron')} value={cron} disabled={disabled} onChange={e => setCron(e.target.value)} /></SmartOpsField>
      </div>
      <SmartOpsSwitch label={t('smartOps.planEnabled')} value={planEnabled} onChange={setPlanEnabled} disabled={disabled} />
    </SmartOpsSection>}
    <div className="flex flex-wrap gap-2">
      {mode === 'manual' ? <Button disabled={disabled || !valid} onClick={() => void act(() => createPelicanTest(normalized))}>{busy ? <RefreshCw className="size-4 animate-spin" /> : <Play className="size-4" />}{t('smartOps.run')}</Button> : <Button disabled={disabled || !valid || !name.trim()} onClick={() => void act(() => savePelicanPlan({ id: planID, name, enabled: planEnabled, interval_minutes: interval, cron_expression: cron, next_run_at: new Date(Date.now() + interval * 60000).toISOString(), job: normalized }))}><Save className="size-4" />{t('smartOps.savePlan')}</Button>}
      {planID > 0 && <Button variant="outline" onClick={() => { setPlanID(0); setName(''); setJob(defaultJob) }}><Plus className="size-4" />{t('smartOps.newPlan')}</Button>}
    </div>
    <SmartOpsSection title={t('smartOps.plans')}>
      <Table><TableHeader><TableRow>{['name', 'interval', 'nextRun', 'status', 'actions'].map(key => <TableHead key={key}>{t(`smartOps.${key}`)}</TableHead>)}</TableRow></TableHeader>
        <TableBody>{plans.length === 0 ? <TableRow><TableCell colSpan={5} className="py-8 text-center text-muted-foreground">{t('smartOps.noPlans')}</TableCell></TableRow> : plans.map(p => <TableRow key={p.id}>
          <TableCell className="font-medium">{p.name}</TableCell><TableCell>{p.interval_minutes}</TableCell><TableCell>{new Date(p.next_run_at).toLocaleString()}</TableCell><TableCell>{t(p.enabled ? 'smartOps.active' : 'smartOps.paused')}</TableCell>
          <TableCell><div className="flex gap-1"><Button variant="ghost" size="icon" title={t('smartOps.run')} aria-label={t('smartOps.run')} disabled={disabled} onClick={() => void act(() => runPelicanPlan(p.id))}><Play className="size-4" /></Button><Button variant="ghost" size="icon" title={t('smartOps.edit')} aria-label={t('smartOps.edit')} disabled={disabled} onClick={() => edit(p)}><Pencil className="size-4" /></Button><Button variant="ghost" size="icon" title={t('common.delete')} aria-label={t('common.delete')} disabled={disabled} onClick={() => void act(() => deletePelicanPlan(p.id))}><Trash2 className="size-4" /></Button><SmartOpsSwitch label={t('smartOps.planEnabled')} value={p.enabled} disabled={disabled} onChange={v => void act(() => savePelicanPlan({ ...p, enabled: v }))} /></div></TableCell>
        </TableRow>)}</TableBody></Table>
    </SmartOpsSection>
    <SmartOpsSection title={t('smartOps.history')}>
      <Table><TableHeader><TableRow>{['job', 'created', 'model', 'samples', 'status', 'actions'].map(key => <TableHead key={key}>{t(`smartOps.${key}`)}</TableHead>)}</TableRow></TableHeader>
        <TableBody>{jobs.length === 0 ? <TableRow><TableCell colSpan={6} className="py-8 text-center text-muted-foreground">{t('smartOps.noJobs')}</TableCell></TableRow> : jobs.map(j => <TableRow key={j.id}>
          <TableCell className="font-mono">{j.id}</TableCell><TableCell>{new Date(j.created_at).toLocaleString()}</TableCell><TableCell className="font-mono text-xs">{j.model}</TableCell><TableCell>{j.results.length} / {j.samples}</TableCell><TableCell>{t(`smartOps.statuses.${j.status}`)}</TableCell>
          <TableCell><div className="flex gap-1"><Button variant="ghost" size="icon" title={t('smartOps.results')} aria-label={t('smartOps.results')} onClick={() => { selectedID.current = j.id; void getPelicanTest(j.id).then(setSelected).catch(e => setMessage(e.message)) }}><Eye className="size-4" /></Button>{['queued', 'running'].includes(j.status) && <Button variant="ghost" size="icon" title={t('smartOps.cancel')} aria-label={t('smartOps.cancel')} disabled={disabled} onClick={() => void act(() => cancelPelicanTest(j.id))}><Square className="size-4" /></Button>}</div></TableCell>
        </TableRow>)}</TableBody></Table>
    </SmartOpsSection>
    {selected && <SmartOpsSection title={`${t('smartOps.results')} #${selected.id}`}>
      {selected.error && <p className="text-sm text-destructive">{selected.error}</p>}
      {selected.results.map(r => <div key={r.sample} className="space-y-3 border-b border-border pb-4">
        <div className="flex flex-wrap gap-4 text-sm"><span>{t('smartOps.account')}: {r.account_id}</span><span>{t('smartOps.latency')}: {Math.round(r.latency / 1000000)}</span><span>{t('smartOps.tokenUsage')}: {r.input_tokens ?? 0} / {r.output_tokens ?? 0}</span><span>{t(`smartOps.statuses.${r.status}`)}</span></div>
        <p className="text-sm">{t('smartOps.cost')}: {r.cost_usd === undefined ? t('smartOps.incomplete') : `$${r.cost_usd.toFixed(6)}`}{r.cost_incomplete && ` (${t('smartOps.incomplete')})`}</p>
        {r.error && <p className="text-sm text-destructive">{r.error}</p>}
        {r.output && <details><summary className="cursor-pointer text-sm">{t('smartOps.output')}</summary><pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted p-4 text-xs">{r.output}</pre></details>}
        {r.output && <iframe title={t('smartOps.results')} className="h-96 w-full border border-border bg-white" src="/api/quality-test/preview" sandbox="allow-scripts" referrerPolicy="no-referrer" onLoad={e => e.currentTarget.contentWindow?.postMessage({ type: 'quality-test-preview', html: extractQualityTestHTML(r.output) }, '*')} />}
      </div>)}
    </SmartOpsSection>}
  </div>
}
