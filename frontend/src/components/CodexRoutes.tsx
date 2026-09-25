import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FlaskConical, GitBranch, RefreshCw } from 'lucide-react'
import { api } from '../api'
import type { CodexPathSnapshot } from '../types'
import { Button } from './ui/button'
import { Select } from './ui/select'
import { Input } from './ui/input'
import { codexProbeOutcome, type CodexProbeLevel, type CodexProbeResult } from '../lib/codexProbe'

function CodexProbeResults({ results }: { results: CodexProbeResult[] }) {
  const { t } = useTranslation()
  return <ul className="divide-y divide-border" aria-label={t('codexRoutes.probe.results')}>
    {results.map(result => {
      const outcome = codexProbeOutcome(result)
      const successful = outcome === 'supported'
      return <li key={`${result.account_id}:${result.model}:${result.level}`} className="space-y-1.5 py-3 text-xs" data-probe-outcome={outcome}>
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
          <span className="font-medium">{t('codexRoutes.probe.account', { id: result.account_id })} · {t(`codexRoutes.probe.levels.${result.level}`)}</span>
          <span className={successful ? 'font-medium text-foreground' : 'font-medium text-destructive'}>{t(`codexRoutes.probe.outcomes.${outcome}`, { defaultValue: t('codexRoutes.probe.outcomes.error') })}</span>
        </div>
        <p className="break-all font-mono text-muted-foreground">{result.upstream} · {result.model}</p>
        <p className="leading-relaxed text-muted-foreground">{t('codexRoutes.probe.capability')}: {t(`codexRoutes.capabilities.${result.capability}`, { defaultValue: t('codexRoutes.capabilities.unknown') })}{result.basic_outcome && ` · ${t('codexRoutes.probe.levels.basic')}: ${t(`codexRoutes.probe.outcomes.${result.basic_outcome}`)}`}{result.tools_outcome && ` · ${t('codexRoutes.probe.levels.tools')}: ${t(`codexRoutes.probe.outcomes.${result.tools_outcome}`)}`}</p>
        {(result.message || result.error_code || (result.outcome === 'supported' && !successful)) && <p className="break-words leading-relaxed text-muted-foreground">{result.outcome === 'supported' && !successful ? t('codexRoutes.probe.incomplete') : result.message}{result.error_code && <span className="break-all font-mono">{result.message ? ' · ' : ''}{result.error_code}</span>}</p>}
        <div className="flex flex-wrap gap-x-3 gap-y-1 tabular-nums text-muted-foreground">
          {result.finished_at && <time dateTime={result.finished_at}>{new Date(result.finished_at).toLocaleString()}</time>}
          {!!result.http_status && <span>HTTP {result.http_status}</span>}
          {!!result.reported_status && result.reported_status !== result.http_status && <span>{t('codexRoutes.probe.reportedStatus')}: {result.reported_status}</span>}
          <span>{t('codexRoutes.probe.duration', { duration: result.duration_ms, count: result.attempts })}</span>
        </div>
      </li>
    })}
  </ul>
}

function CodexStrongProbe({ ids, onChanged }: { ids: number[]; onChanged?: () => void }) {
  const { t } = useTranslation()
  const formID = useId()
  const idsKey = ids.join(',')
  const [model, setModel] = useState('')
  const [level, setLevel] = useState<CodexProbeLevel>('basic')
  const [running, setRunning] = useState(false)
  const [completed, setCompleted] = useState(0)
  const [results, setResults] = useState<CodexProbeResult[]>([])
  const [history, setHistory] = useState<CodexProbeResult[]>([])
  const [loadingHistory, setLoadingHistory] = useState(false)
  const [historyFailed, setHistoryFailed] = useState(false)
  const [historyVersion, setHistoryVersion] = useState(0)
  const [notice, setNotice] = useState<'done' | 'canceled' | 'failed' | ''>('')
  const controllerRef = useRef<AbortController | null>(null)
  const modelName = model.trim()
  const canRun = ids.length > 0 && ids.length <= 100 && modelName.length > 0 && modelName.length <= 128

  useEffect(() => {
    setResults([]); setCompleted(0); setNotice(''); setRunning(false)
    return () => { controllerRef.current?.abort(); controllerRef.current = null }
  }, [idsKey])

  useEffect(() => {
    const controller = new AbortController()
    setHistory([]); setHistoryFailed(false); setLoadingHistory(false)
    if (!ids.length || ids.length > 100) return
    setLoadingHistory(true)
    const timer = window.setTimeout(() => {
      const pending = idsKey.split(',').map(Number)
      const loaded: CodexProbeResult[] = []
      let failed = false
      const worker = async () => {
        while (pending.length && !controller.signal.aborted) {
          const id = pending.shift()!
          try {
            const response = await api.getCodexRoutes(id, modelName || undefined, controller.signal)
            loaded.push(...(response.probes ?? []))
          } catch { if (!controller.signal.aborted) failed = true }
        }
      }
      void Promise.all(Array.from({ length: Math.min(3, pending.length) }, worker)).then(() => {
        if (controller.signal.aborted) return
        setHistory(loaded.sort((a, b) => a.account_id - b.account_id || a.level.localeCompare(b.level)))
        setHistoryFailed(failed); setLoadingHistory(false)
      })
    }, 300)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [idsKey, modelName, historyVersion])

  async function run() {
    if (!canRun || controllerRef.current) return
    const controller = new AbortController()
    controllerRef.current = controller
    setRunning(true); setCompleted(0); setResults([]); setNotice('')
    try {
      const batch = await api.probeCodexAccounts({ ids, model: modelName, level }, event => {
        if (controllerRef.current !== controller) return
        setCompleted(event.completed)
        if (event.type === 'result' && event.result) setResults(previous => [...previous.filter(result => result.account_id !== event.result!.account_id), event.result!])
      }, controller.signal)
      if (controllerRef.current !== controller) return
      setResults(batch.results); setCompleted(batch.completed)
      setNotice(batch.completed === batch.total && batch.results.length === batch.total ? 'done' : 'failed')
    } catch {
      if (controllerRef.current === controller) setNotice(controller.signal.aborted ? 'canceled' : 'failed')
    } finally {
      if (controllerRef.current === controller) {
        controllerRef.current = null
        setRunning(false); setHistoryVersion(value => value + 1)
        onChanged?.()
      }
    }
  }

  return <section className="space-y-3 border-t border-border pt-4" aria-labelledby={`${formID}-title`}>
    <div className="space-y-1">
      <h3 id={`${formID}-title`} className="text-sm font-medium">{t('codexRoutes.probe.title')}</h3>
      <p id={`${formID}-hint`} className="max-w-prose text-xs leading-relaxed text-muted-foreground">{t('codexRoutes.probe.explanation')}</p>
    </div>
    <div className="flex flex-wrap items-end gap-2">
      <div className="min-w-0 basis-48 flex-1 space-y-1.5">
        <label htmlFor={`${formID}-model`} className="text-xs font-medium">{t('codexRoutes.probe.model')}</label>
        <Input id={`${formID}-model`} value={model} onChange={event => setModel(event.target.value)} disabled={running} maxLength={128} placeholder={t('codexRoutes.probe.modelPlaceholder')} aria-describedby={`${formID}-hint`} />
      </div>
      <div className="min-w-0 basis-48 flex-1 space-y-1.5">
        <label htmlFor={`${formID}-level`} className="text-xs font-medium">{t('codexRoutes.probe.level')}</label>
        <Select id={`${formID}-level`} value={level} onValueChange={value => setLevel(value as CodexProbeLevel)} disabled={running} options={(['basic', 'tools'] as const).map(value => ({ value, label: t(`codexRoutes.probe.levels.${value}`) }))} />
      </div>
      {running
        ? <Button type="button" variant="outline" size="sm" onClick={() => controllerRef.current?.abort()}>{t('codexRoutes.probe.cancel')}</Button>
        : <Button type="button" size="sm" disabled={!canRun} onClick={() => void run()}><FlaskConical className="size-4" aria-hidden="true" />{t('codexRoutes.probe.run', { count: ids.length })}</Button>}
    </div>
    <p className="text-xs leading-relaxed text-muted-foreground">{t(`codexRoutes.probe.levelHints.${level}`)}</p>
    <p className="text-xs leading-relaxed text-muted-foreground">{t('codexRoutes.probe.limits')}</p>
    {!modelName && <p className="text-xs text-muted-foreground">{t('codexRoutes.probe.modelRequired')}</p>}
    {(running || notice) && <p role={notice === 'failed' ? 'alert' : 'status'} aria-live="polite" className={`flex items-center gap-2 text-xs ${notice === 'failed' ? 'text-destructive' : 'text-muted-foreground'}`}>
      {running && <RefreshCw className="size-3.5 shrink-0 motion-safe:animate-spin" aria-hidden="true" />}
      {t(`codexRoutes.probe.${running ? 'progress' : notice}`, { completed, total: ids.length })}
    </p>}
    {!!results.length && <CodexProbeResults results={results} />}
    <details className="border-t border-border pt-3">
      <summary className="cursor-pointer text-xs font-medium outline-offset-4">{t('codexRoutes.probe.history')}</summary>
      <p className="mt-2 text-xs leading-relaxed text-muted-foreground">{t('codexRoutes.probe.historyHint')}</p>
      {loadingHistory ? <p role="status" className="mt-2 text-xs text-muted-foreground">{t('codexRoutes.probe.loading')}</p>
        : history.length ? <CodexProbeResults results={history} /> : <p className="mt-2 text-xs text-muted-foreground">{t('codexRoutes.probe.empty')}</p>}
      {historyFailed && <p role="alert" className="mt-2 text-xs text-destructive">{t('codexRoutes.probe.historyFailed')} <Button type="button" variant="link" size="sm" onClick={() => setHistoryVersion(value => value + 1)}>{t('codexRoutes.probe.retry')}</Button></p>}
    </details>
  </section>
}

export function CodexRouteBadges({ paths, expanded = false }: { paths?: CodexPathSnapshot[]; expanded?: boolean }) {
  const { t } = useTranslation()
  if (!paths?.length) return null
  const evidence = paths.filter(p => p.model || !paths.some(q => q.upstream === p.upstream && q.model))
  const visible = expanded ? evidence : ['codex', 'basispoints'].flatMap(upstream => evidence.filter(p => p.upstream === upstream).slice(0, 2))
  return <div className="space-y-1.5" aria-label={t('codexRoutes.title')}>
    {visible.map(p => {
      const observed = p.observed_at ? new Date(p.observed_at / 1e6).toLocaleString() : t('codexRoutes.never')
      const facts = [p.model || t('codexRoutes.accountWide'), t(`codexRoutes.capabilities.${p.capability}`), t(`codexRoutes.health.${p.health}`), p.allowed ? '' : t('codexRoutes.disabled'), observed, p.source, p.reason, p.health_reason].filter(Boolean).join(' · ')
      return <div key={`${p.upstream}:${p.model}`} title={facts} className="text-xs leading-relaxed">
        <span className={p.allowed ? 'font-medium text-foreground' : 'font-medium text-destructive'}>{t(`codexRoutes.paths.${p.upstream}`)}</span>
        <span className="text-muted-foreground"> · {t(`codexRoutes.capabilities.${p.capability}`)} · {p.allowed ? t(`codexRoutes.health.${p.health}`) : t('codexRoutes.disabled')}</span>
        {p.model && <div className="break-all text-muted-foreground">{p.model}</div>}
        {expanded && <div className="break-words text-muted-foreground">{t('codexRoutes.observed')}: {observed}{p.source && ` · ${p.source}`}{p.reason && ` · ${p.reason}`}{p.health_reason && ` · ${p.health_reason}`}{p.cooldown_until && Date.parse(p.cooldown_until) > 0 && ` · ${t('codexRoutes.until')}: ${new Date(p.cooldown_until).toLocaleString()}`}</div>}
      </div>
    })}
    {!expanded && evidence.length > visible.length && <span className="text-xs text-muted-foreground">{t('codexRoutes.more', { count: evidence.length - visible.length })}</span>}
  </div>
}

export function CodexRouteManager({ ids, onChanged }: { ids: number[]; onChanged?: () => void }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [path, setPath] = useState('basispoints')
  const [action, setAction] = useState('reset')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [failed, setFailed] = useState(false)
  const [paths, setPaths] = useState<CodexPathSnapshot[]>()
  const [routesVersion, setRoutesVersion] = useState(0)
  const accountID = ids.length === 1 ? ids[0] : undefined
  useEffect(() => { setMessage(''); setFailed(false) }, [accountID, open])
  useEffect(() => {
    setPaths(undefined)
    if (!open || !accountID) return
    let current = true
    void api.getCodexRoutes(accountID).then(result => { if (current) setPaths(result.paths) }).catch(() => { if (current) { setFailed(true); setMessage(t('codexRoutes.loadFailed')) } })
    return () => { current = false }
  }, [accountID, open, t, routesVersion])
  async function save() {
    setBusy(true); setMessage(''); setFailed(false)
    try {
      const result = await api.updateCodexRoutes({ ids, upstream: path, ...(action === 'reset' ? { reset_observations: true } : { allowed: action === 'allow' }) })
      onChanged?.()
      setMessage(t('codexRoutes.saved', { count: result.updated }))
      setRoutesVersion(value => value + 1)
    } catch { setFailed(true); setMessage(t('codexRoutes.saveFailed')) }
    finally { setBusy(false) }
  }
  return <div className="min-w-0 space-y-3">
    <Button type="button" variant="outline" size="sm" aria-expanded={open} onClick={() => setOpen(!open)}><GitBranch className="size-4" aria-hidden="true" />{t('codexRoutes.manage', { count: ids.length })}</Button>
    {open && <div className="space-y-3 rounded-lg border border-border bg-card p-3">
      <p className="text-xs leading-relaxed text-muted-foreground">{t('codexRoutes.explanation')}</p>
      {accountID && <CodexRouteBadges paths={paths} expanded />}
      <div className="flex flex-wrap items-end gap-2">
        <Select className="min-w-40 flex-1" aria-label={t('codexRoutes.path')} value={path} onValueChange={setPath} disabled={busy} options={['basispoints', 'codex'].map(value => ({ value, label: t(`codexRoutes.paths.${value}`) }))} />
        <Select className="min-w-48 flex-1" aria-label={t('codexRoutes.action')} value={action} onValueChange={setAction} disabled={busy} options={['reset', 'allow', 'disable'].map(value => ({ value, label: t(`codexRoutes.actions.${value}`) }))} />
        <Button type="button" size="sm" disabled={busy || !ids.length || ids.length > 100} onClick={() => void save()}>{busy && <RefreshCw className="size-4 motion-safe:animate-spin" aria-hidden="true" />}{t('codexRoutes.apply')}</Button>
      </div>
      <p className="text-xs text-muted-foreground">{t('codexRoutes.resetHint')}</p>
      {ids.length > 100 && <p className="text-xs text-destructive">{t('codexRoutes.batchLimit')}</p>}
      {message && <p role={failed ? 'alert' : 'status'} className={failed ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>{message}</p>}
      <CodexStrongProbe ids={ids} onChanged={() => {
        onChanged?.()
        setRoutesVersion(value => value + 1)
      }} />
    </div>}
  </div>
}
