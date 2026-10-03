import { useCallback, useEffect, useState } from 'react'
import { RefreshCw, Save, Settings2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DraftNumberInput } from '@/components/ui/draft-number-input'
import { SegmentedPillGroup } from '@/components/ui/segmented-pill-group'
import PageHeader from '../components/PageHeader'
import { Card, CardContent } from '@/components/ui/card'
import { useTranslation } from 'react-i18next'
import TwoFAImport from '../components/TwoFAImport'
import { credentialRequest, type CredentialAccount, type CredentialOverview, type CredentialRules } from '@/lib/credentialOps'

export default function CredentialOps() {
  const { t } = useTranslation()
  const [data, setData] = useState<CredentialOverview | null>(null)
  const [selected, setSelected] = useState<CredentialAccount | null>(null)
  const [view, setView] = useState<'accounts' | 'import' | 'jobs'>('accounts')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [notice, setNotice] = useState('')
  const [rules, setRules] = useState<CredentialRules | null>(null)
  const load = useCallback(async () => { try { const next = await credentialRequest<CredentialOverview>('/credential-ops'); setData(next); setRules(current => current ?? next.rules); setError('') } catch (err) { setError(String(err)) } }, [])
  useEffect(() => { void load(); const timer = setInterval(() => void load(), 5000); return () => clearInterval(timer) }, [load])
  async function action(path: string, method = 'POST', body?: unknown) { setBusy(true); try { await credentialRequest(path, method, body); await load() } catch (err) { setError(err instanceof Error ? err.message : t('credentialOps.failed')) } finally { setBusy(false) } }
  return <main className="mx-auto w-full max-w-[1180px] space-y-6">
    <PageHeader title={t('credentialOps.title')} onRefresh={() => { setRefreshing(true); void load().finally(() => setRefreshing(false)) }} actionMeta={refreshing ? <RefreshCw className="size-4 animate-spin" /> : undefined} />
    <Card><CardContent className="p-4 sm:p-5"><SegmentedPillGroup value={view} onChange={setView} label={t('credentialOps.views')} className="max-w-xl" options={[{ value: 'accounts', label: t('credentialOps.accountsView') }, { value: 'import', label: t('credentialOps.firstLogin') }, { value: 'jobs', label: t('credentialOps.jobsView') }]} /></CardContent></Card>
    {notice && <p role="status" className="text-sm text-muted-foreground">{notice}</p>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {data && !data.enabled && <p className="text-sm text-muted-foreground">{t('credentialOps.disabled')}</p>}
    {!data && !error && <p className="text-sm text-muted-foreground">{t('credentialOps.loading')}</p>}
    {view === 'import' && data?.enabled && <div className="max-w-3xl"><TwoFAImport onSaved={() => void load()} sessionStudioConfigured={data.session_studio_configured} /></div>}
    {view === 'accounts' && <>
      <Card><CardContent className="space-y-4"><div><h2 className="font-semibold">{t('credentialOps.globalRules')}</h2><p className="mt-1 text-sm text-muted-foreground">{t('credentialOps.globalHint')}</p></div>{rules && <><div className="grid gap-4 sm:grid-cols-3">{([['interval_seconds', 'probeInterval', 60, 86400], ['retry_seconds', 'retryInterval', 30, 86400], ['failure_threshold', 'failureThreshold', 1, 10], ['cooldown_seconds', 'cooldown', 60, 604800]] as const).map(([key, label, min, max]) => <label key={key} className="space-y-1 text-sm">{t(`credentialOps.${label}`)}<DraftNumberInput min={min} max={max} value={rules[key]} onValueChange={value => setRules({ ...rules, [key]: value })} /></label>)}</div><Button variant="outline" disabled={busy || !data?.enabled || JSON.stringify(rules)===JSON.stringify(data.rules)} onClick={async () => { setBusy(true); setError(''); try { const saved = await credentialRequest<CredentialRules>('/credential-ops/rules', 'PUT', rules); setRules(saved); await load(); setNotice(t('credentialOps.saved')) } catch (err) { setError(String(err)) } finally { setBusy(false) } }}><Save className="size-4" />{t('credentialOps.saveRules')}</Button></>}</CardContent></Card>
      <Card><CardContent className="p-0"><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead className="border-b bg-muted/20 text-muted-foreground"><tr>{['account', 'probeResult', 'failStreak', 'nextProbe', 'autoLogin', 'actions'].map(key => t(`credentialOps.${key}`)).map(label => <th key={label} className="whitespace-nowrap px-4 py-3 font-medium">{label}</th>)}</tr></thead><tbody>{data?.accounts.map(account => <tr key={account.account_id} className="border-b last:border-0 hover:bg-muted/20"><td className="max-w-64 px-4 py-3"><div className="truncate">{account.name}</div><span className="text-xs text-muted-foreground">#{account.account_id}</span></td><td className="px-4 py-3"><span title={account.probe_detail}>{t(`credentialOps.probe.${account.probe_state}`, { defaultValue: account.probe_state })}</span></td><td className="px-4 py-3">{account.fail_streak}</td><td className="whitespace-nowrap px-4 py-3">{account.enabled ? new Date(account.next_probe_at).toLocaleString() : t('credentialOps.off')}</td><td className="px-4 py-3">{data?.login_configured[String(account.account_id)] ? t('credentialOps.on') : t('credentialOps.needsLoginConfig')}</td><td className="px-4 py-3"><div className="flex gap-1"><Button size="icon" variant="ghost" title={t('credentialOps.configure')} onClick={() => setSelected({ ...account })}><Settings2 className="size-4" /></Button></div></td></tr>)}</tbody></table>{data?.accounts.length === 0 && <p className="py-8 text-center text-sm text-muted-foreground">{t('credentialOps.noAccounts')}</p>}</div></CardContent></Card>
      {selected && <section className="space-y-5 rounded-xl border border-border/70 bg-card p-5 shadow-sm"><div className="flex items-center justify-between"><h2 className="text-base font-semibold">{selected.name}</h2><Button size="icon" variant="ghost" title={t('credentialOps.closeConfig')} onClick={() => setSelected(null)}><X className="size-4" /></Button></div>{data?.enabled && <div className="max-w-3xl border-t pt-5"><TwoFAImport accountId={selected.account_id} onSaved={() => void load()} sessionStudioConfigured={data.session_studio_configured} /></div>}</section>}
    </>}
    {view === 'jobs' && <Card><CardContent className="p-0"><div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead className="border-b bg-muted/20 text-muted-foreground"><tr>{['job', 'account', 'statusStage', 'attempts', 'created', 'actions'].map(key => t(`credentialOps.${key}`)).map(label => <th key={label} className="whitespace-nowrap px-4 py-3 font-medium">{label}</th>)}</tr></thead><tbody>{data?.tasks.map(task => <tr key={task.ID} className="border-b last:border-0 hover:bg-muted/20"><td className="px-4 py-3">#{task.ID}</td><td className="px-4 py-3">{task.AccountID || t('credentialOps.firstImport')}</td><td className="px-4 py-3"><p>{t(`credentialOps.status.${task.Status}`, { defaultValue: task.Status })} / {t(`credentialOps.stage.${task.Stage}`, { defaultValue: task.Stage })}</p>{task.Error && <p className="text-xs text-destructive">{task.Error}</p>}</td><td className="px-4 py-3">{task.Attempt}</td><td className="whitespace-nowrap px-4 py-3">{new Date(task.CreatedAt).toLocaleString()}</td><td className="px-4 py-3">{['queued', 'running'].includes(task.Status) && <Button size="icon" variant="ghost" title={t('credentialOps.cancelJob')} disabled={busy} onClick={() => void action(`/credential-ops/login/${task.ID}`, 'DELETE')}><X className="size-4" /></Button>}</td></tr>)}</tbody></table>{data?.tasks.length === 0 && <p className="py-8 text-center text-sm text-muted-foreground">{t('credentialOps.noJobs')}</p>}</div></CardContent></Card>}
  </main>
}
