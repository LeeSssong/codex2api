import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Copy, Download, Eye, Package, Play, RefreshCw, Square, Trash2, Upload } from 'lucide-react'
import { api } from '../api'
import PageHeader from '../components/PageHeader'
import { Button } from '../components/ui/button'
import { Checkbox } from '../components/ui/checkbox'
import { Input } from '../components/ui/input'
import { Textarea } from '../components/ui/textarea'
import { Switch } from '../components/ui/switch'
import { DraftNumberInput } from '../components/ui/draft-number-input'
import { Select } from '../components/ui/select'
import { SegmentedPillGroup } from '../components/ui/segmented-pill-group'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '../components/ui/dialog'
import { useToast } from '../hooks/useToast'
import { useConfirmDialog } from '../hooks/useConfirmDialog'
import { getErrorMessage } from '../utils/error'
import { groupStateJobs, isStateJobActive, readStateCapturePreferences, STATE_MODEL_LABELS, stateRemaining, type StateCheck, type StateEntry, type StateImportPreview, type StatePackage, type StatePoolData } from '../lib/statePool'
import './state-pool.css'

const preferencesKey = 'codex2api_state_capture_v1'

export default function StatePool() {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [preferences] = useState(() => {
    try { return readStateCapturePreferences(localStorage.getItem(preferencesKey)) }
    catch { return undefined }
  })
  const [data, setData] = useState<StatePoolData>()
  const [error, setError] = useState('')
  const [accounts, setAccounts] = useState<number[]>(preferences?.accounts ?? [])
  const [models, setModels] = useState<string[]>(preferences?.models ?? Object.keys(STATE_MODEL_LABELS))
  const [selected, setSelected] = useState<string[]>([])
  const [limits, setLimits] = useState({ concurrency: 4, per_account: 2, per_proxy: 2 })
  const [routeMode, setRouteMode] = useState<'business' | 'proxies'>(preferences?.routeMode ?? 'business')
  const [proxyIDs, setProxyIDs] = useState<number[]>(preferences?.proxyIDs ?? [])
  const [forwardProxyID, setForwardProxyID] = useState(preferences?.forwardProxyID ?? 0)
  const [candidates, setCandidates] = useState(preferences?.candidates ?? 3)
  const [strategy, setStrategy] = useState<'race' | 'sequential'>(preferences?.strategy ?? 'race')
  const [distinctIPs, setDistinctIPs] = useState(preferences?.distinctIPs ?? true)
  const [newSession, setNewSession] = useState(preferences?.newSession ?? true)
  const [enable, setEnable] = useState(preferences?.enable ?? true)
  const [strict, setStrict] = useState(preferences?.strict ?? true)
  const [busy, setBusy] = useState('')
  const [view, setView] = useState<'entries' | 'jobs'>('entries')
  const [now, setNow] = useState(Date.now() / 1000)
  const offset = useRef(0)
  const initialized = useRef(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importMode, setImportMode] = useState<'package' | 'raw'>('package')
  const [importText, setImportText] = useState('')
  const [preview, setPreview] = useState<{ text: string; items: StateImportPreview[] }>()
  const [previewError, setPreviewError] = useState('')
  const [importAccount, setImportAccount] = useState('')
  const [importModel, setImportModel] = useState('gpt-5.6-sol')
  const [capturedAt, setCapturedAt] = useState('')
  const [details, setDetails] = useState<{ name: string; checks: StateCheck[] }>()
  const [copyFallback, setCopyFallback] = useState('')
  const [search, setSearch] = useState('')

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      const result = await api.getStatePool(signal)
      setData(result)
      offset.current = result.server_time - Date.now() / 1000
      setNow(result.server_time)
      setError('')
      setSelected(old => old.filter(id => result.entries.some(entry => entry.id === id && entry.expires_at > result.server_time)))
      if (!initialized.current) {
        initialized.current = true
        setLimits(result.limits)
        const first = result.accounts.find(account => account.available)
        if (first) { if (!preferences) setAccounts([first.id]); setImportAccount(String(first.id)) }
        if (!preferences) {
          const enabledProxies = result.proxies.filter(proxy => proxy.enabled).slice(0, 3)
          if (enabledProxies.length) { setProxyIDs(enabledProxies.map(proxy => proxy.id)); setRouteMode('proxies') }
        }
        setAccounts(old => old.filter(id => result.accounts.some(account => account.id === id)))
        setProxyIDs(old => old.filter(id => result.proxies.some(proxy => proxy.id === id)))
        setForwardProxyID(old => result.proxies.some(proxy => proxy.id === old) ? old : 0)
      }
    } catch (err) {
      if (!signal?.aborted) setError(getErrorMessage(err, t('statePool.loadFailed')))
    }
  }, [t, preferences])

  useEffect(() => {
    if (!data) return
    try { localStorage.setItem(preferencesKey, JSON.stringify({ accounts, models, routeMode, proxyIDs, forwardProxyID, candidates, strategy, distinctIPs, newSession, enable, strict })) }
    catch { /* Storage availability must not block capture. */ }
  }, [Boolean(data), accounts, models, routeMode, proxyIDs, forwardProxyID, candidates, strategy, distinctIPs, newSession, enable, strict])

  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const poll = async () => {
      await refresh(controller.signal)
      if (!controller.signal.aborted) timer = setTimeout(poll, 2500)
    }
    void poll()
    return () => { controller.abort(); clearTimeout(timer) }
  }, [refresh])
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now() / 1000 + offset.current), 1000)
    return () => clearInterval(timer)
  }, [])
  useEffect(() => {
    setPreview(undefined); setPreviewError('')
    if (!importOpen || importMode !== 'package' || !importText.trim()) return
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      let pack: StatePackage
      try { pack = JSON.parse(importText) as StatePackage }
      catch { setPreviewError(t('statePool.invalidPackage')); return }
      try {
        const result = await api.previewStateImport(pack, controller.signal)
        if (!controller.signal.aborted) setPreview({ text: importText, items: result.items })
      } catch (err) {
        if (!controller.signal.aborted) setPreviewError(getErrorMessage(err, t('statePool.invalidPackage')))
      }
    }, 300)
    return () => { clearTimeout(timer); controller.abort() }
  }, [importText, importMode, importOpen, t])

  const act = async (key: string, action: () => Promise<void>) => {
    setBusy(key)
    try { await action(); await refresh() }
    catch (err) { showToast(getErrorMessage(err, t('statePool.actionFailed')), 'error') }
    finally { setBusy('') }
  }
  const copy = async (text: string) => {
    try { await navigator.clipboard.writeText(text); showToast(t('statePool.copied')) }
    catch { setCopyFallback(text) }
  }
  const exportStates = (ids: string[], raw = false, download = false) => act('export', async () => {
    const pack = await api.exportStates(ids)
    const text = raw ? pack.states[0].value : JSON.stringify(pack, null, download ? 2 : undefined)
    if (!download) { await copy(text); return }
    const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
    const link = document.createElement('a')
    link.href = url; link.download = `codex2api-state-${Date.now()}.json`; link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  })
  const capture = (accountIDs = accounts, requestedModels = models) => act('capture', async () => {
    await api.setStatePoolLimits(limits)
    const result = await api.captureStates({ account_ids: accountIDs, models: requestedModels, enable, strict, proxy_ids: routeMode === 'proxies' ? proxyIDs : [], candidates, strategy, distinct_ips: distinctIPs, new_session: newSession, forward_proxy_id: routeMode === 'proxies' ? forwardProxyID : 0 })
    showToast(t('statePool.submitted', { count: result.job_ids.length }))
    setView('jobs')
  })
  const changeEntry = (entry: StateEntry, change: Partial<Pick<StateEntry, 'enabled' | 'strict'>>) => act(entry.id, async () => {
    await api.configureState(entry.id, { enabled: entry.enabled, strict: entry.strict, ...change })
  })
  const removeEntry = async (entry: StateEntry) => {
    if (!await confirm({ title: t('statePool.delete'), description: t('statePool.deleteConfirm', { model: entry.model }), tone: 'destructive' })) return
    await act(entry.id, async () => { await api.deleteState(entry.id) })
  }
  const importStates = () => act('import', async () => {
    const text = importText.trim()
    let result
    if (importMode === 'package') {
      const pack = JSON.parse(text) as StatePackage
      result = await api.importStates({ package: pack, enable, strict, allow_partial: true })
    } else {
      const time = new Date(capturedAt).getTime() / 1000
      if (!Number.isFinite(time)) throw new Error(t('statePool.captureTimeRequired'))
      result = await api.importStates({ value: text, account_id: Number(importAccount), model: importModel, captured_at: Math.floor(time), enable, strict })
    }
    showToast(t('statePool.imported', { count: result.job_ids.length, reused: result.items?.filter(item => item.status === 'already_verified').length ?? 0, skipped: result.items?.filter(item => item.status === 'invalid').length ?? 0 }))
    setImportText(''); setImportOpen(false); setView(result.job_ids.length ? 'jobs' : 'entries')
  })
  const filteredAccounts = useMemo(() => data?.accounts.filter(account => `${account.name} ${account.id}`.toLowerCase().includes(search.toLowerCase())) ?? [], [data, search])
  const activeJobs = data?.jobs.filter(isStateJobActive).length ?? 0
  const readyEntries = data?.entries.filter(entry => entry.status === 'ready' && entry.expires_at > now).length ?? 0
  const exportableIDs = data?.entries.filter(entry => entry.expires_at > now).map(entry => entry.id) ?? []
  const transferIDs = selected.length ? selected : exportableIDs.slice(0, 128)
  const groups = useMemo(() => groupStateJobs(data?.jobs ?? []), [data?.jobs])
  const selectedProxies = data?.proxies.filter(proxy => proxyIDs.includes(proxy.id)) ?? []
  const knownIPs = new Set(selectedProxies.filter(proxy => !(newSession && proxy.supports_session_rotation) && proxy.test_status === 'success' && proxy.last_test_ip).map(proxy => proxy.last_test_ip)).size
  const dynamicProxies = selectedProxies.filter(proxy => proxy.supports_session_rotation).length
  const importable = preview?.text === importText ? preview.items.filter(item => item.status !== 'invalid' && item.expires_at > now).length : 0
  const captureDisabled = Boolean(busy) || Boolean(data?.resin_enabled) || !accounts.length || !models.length || !accounts.every(id => data?.accounts.some(account => account.id === id && account.available)) || accounts.length * models.length > 128 || routeMode === 'proxies' && (!proxyIDs.length || proxyIDs.length > candidates || !proxyIDs.every(id => data?.proxies.some(proxy => proxy.id === id && proxy.enabled)) || forwardProxyID !== 0 && !data?.proxies.some(proxy => proxy.id === forwardProxyID && proxy.enabled))
  const toggleAccount = (id: number, checked: boolean) => setAccounts(old => checked ? [...new Set([...old, id])] : old.filter(value => value !== id))
  const date = (value: number) => new Date(value * 1000).toLocaleString()
  const label = (value: string) => value === 'superseded' ? t('statePool.superseded') : value === 'blocked' ? t('statePool.blocked') : t(`statePool.status.${value}`, { defaultValue: value })
  const failure = (value?: string) => {
    if (!value) return ''
    const status = /^HTTP (401|403|429):/.exec(value)?.[1]
    return t(`statePool.errors.${status ? `upstream_${status}` : value}`, { defaultValue: value })
  }

  return <div className="state-pool">
    <PageHeader title={t('statePool.title')} actionMeta={t('statePool.summary', { ready: readyEntries, active: activeJobs })} onRefresh={() => void refresh()} actions={<Button variant="outline" size="sm" onClick={() => setImportOpen(true)}><Upload />{t('statePool.import')}</Button>} />
    {error ? <div role="alert" className="state-pool-error">{error}</div> : null}
    {data?.resin_enabled ? <div role="alert" className="state-pool-error">{t('statePool.resinConflict')}</div> : null}
    <div className="state-pool-setup">
      <section className="state-pool-accounts" aria-label={t('statePool.accounts')}>
        <div className="state-pool-section-head"><h3>{t('statePool.accounts')}</h3><Button size="sm" variant="ghost" onClick={() => setAccounts(data?.accounts.filter(account => account.available).map(account => account.id) ?? [])}>{t('statePool.selectAvailable')}</Button></div>
        <Input aria-label={t('statePool.search')} placeholder={t('statePool.search')} value={search} onChange={event => setSearch(event.target.value)} />
        {data && !data.accounts.some(account => account.available) ? <p className="state-pool-error">{t('statePool.noAvailableAccounts')} <a href="/admin/accounts">{t('statePool.manageAccounts')}</a></p> : null}
        <div className="state-pool-account-list">
          {!data ? <RefreshCw className="size-4 animate-spin" aria-label={t('statePool.loading')} /> : filteredAccounts.length === 0 ? <p>{t('statePool.noAccounts')}</p> : filteredAccounts.map(account => <label key={account.id} className="state-pool-account">
            <Checkbox checked={accounts.includes(account.id)} disabled={!account.available && !accounts.includes(account.id)} onCheckedChange={checked => toggleAccount(account.id, checked === true)} />
            <span><strong>{account.name || `#${account.id}`}</strong><small>#{account.id} · {account.plan}</small></span>
            <span className={`state-pool-dot ${account.available ? 'is-ready' : ''}`} title={t(account.available ? 'statePool.available' : 'statePool.unavailable')} />
          </label>)}
        </div>
        <div className="state-pool-proxy-heading"><h3>{t('statePool.captureEgress')}</h3><a href="/admin/proxies">{t('statePool.manageProxies')}</a></div>
        <SegmentedPillGroup value={routeMode} onChange={setRouteMode} label={t('statePool.captureEgress')} options={[{ value: 'business', label: t('statePool.businessEgress') }, { value: 'proxies', label: t('statePool.proxyPool') }]} />
        {routeMode === 'proxies' ? <div className="state-pool-proxy-list">{!data?.proxies.length ? <p className="state-pool-meta">{t('statePool.noProxies')}</p> : data.proxies.map(proxy => <label key={proxy.id} className="state-pool-account">
          <Checkbox checked={proxyIDs.includes(proxy.id)} disabled={!proxy.enabled || proxy.id === forwardProxyID || !proxyIDs.includes(proxy.id) && proxyIDs.length >= 12} onCheckedChange={checked => { setProxyIDs(old => checked ? [...old, proxy.id] : old.filter(id => id !== proxy.id)); if (checked) setCandidates(old => Math.max(old, proxyIDs.length + 1)) }} />
          <span><strong>{proxy.name || `Proxy #${proxy.id}`}</strong><small>{newSession && proxy.supports_session_rotation ? t('statePool.dynamicSession') : proxy.test_status === 'success' && proxy.last_test_ip ? proxy.last_test_ip : t('statePool.ipUnknown')}</small></span>
        </label>)}</div> : null}
        {routeMode === 'proxies' ? <div className="state-pool-meta">{t('statePool.proxySummary', { routes: proxyIDs.length, ips: knownIPs })}{newSession && dynamicProxies > 0 ? ` · ${t('statePool.dynamicCount', { count: dynamicProxies })}` : ''}</div> : null}
        {routeMode === 'proxies' ? <div className="state-pool-forward"><label htmlFor="state-forward-proxy" title={t('statePool.forwardProxyHint')}>{t('statePool.forwardProxy')}</label><Select id="state-forward-proxy" aria-label={t('statePool.forwardProxy')} value={String(forwardProxyID)} onValueChange={value => { const id = Number(value); setForwardProxyID(id); setProxyIDs(old => old.filter(proxyID => proxyID !== id)) }} options={[{ value: '0', label: t('statePool.noForwardProxy') }, ...(data?.proxies.filter(proxy => proxy.enabled || proxy.id === forwardProxyID).map(proxy => ({ value: String(proxy.id), label: proxy.name || `Proxy #${proxy.id}` })) ?? [])]} /></div> : null}
      </section>
      <section className="state-pool-options" aria-label={t('statePool.capture')}>
        <div className="state-pool-section-head"><h3>{t('statePool.models')}</h3><span className="state-pool-meta">{t('statePool.effortHigh')}</span></div>
        <div className="state-pool-models">{Object.entries(STATE_MODEL_LABELS).map(([model, name]) => <label key={model} title={model}>
          <Checkbox checked={models.includes(model)} onCheckedChange={checked => setModels(old => checked === true ? [...new Set([...old, model])] : old.filter(value => value !== model))} />
          <span>{name}<small>{model}</small></span>
        </label>)}</div>
        <div className="state-pool-strategy"><SegmentedPillGroup value={strategy} onChange={setStrategy} label={t('statePool.strategy')} options={[{ value: 'race', label: t('statePool.race') }, { value: 'sequential', label: t('statePool.sequential') }]} /><label>{t('statePool.candidates')}<DraftNumberInput aria-label={t('statePool.candidates')} min={Math.max(1, routeMode === 'proxies' ? proxyIDs.length : 1)} max={12} value={candidates} onValueChange={setCandidates} /></label></div>
        <div className="state-pool-limits">
          <label>{t('statePool.globalConcurrency')}<DraftNumberInput aria-label={t('statePool.globalConcurrency')} min={1} max={32} value={limits.concurrency} onValueChange={value => setLimits(old => ({ ...old, concurrency: value, per_account: Math.min(value, old.per_account) }))} /></label>
          <label>{t('statePool.accountConcurrency')}<DraftNumberInput aria-label={t('statePool.accountConcurrency')} min={1} max={Math.min(4, limits.concurrency)} value={limits.per_account} onValueChange={value => setLimits(old => ({ ...old, per_account: value }))} /></label>
          <label>{t('statePool.proxyConcurrency')}<DraftNumberInput aria-label={t('statePool.proxyConcurrency')} min={1} max={8} value={limits.per_proxy} onValueChange={value => setLimits(old => ({ ...old, per_proxy: value }))} /></label>
        </div>
        <div className="state-pool-switches">
          {routeMode === 'proxies' && dynamicProxies > 0 ? <label title={t('statePool.newSessionHint')}><Switch checked={newSession} onCheckedChange={setNewSession} />{t('statePool.newSession')}</label> : null}
          {routeMode === 'proxies' ? <label title={t('statePool.ipEvidence')}><Switch checked={distinctIPs} onCheckedChange={setDistinctIPs} />{t('statePool.dedupeIPs')}</label> : null}
          <label><Switch checked={enable} onCheckedChange={setEnable} />{t('statePool.enableAfterVerify')}</label>
          <label title={t('statePool.strictHint')}><Switch checked={strict} onCheckedChange={setStrict} />{t('statePool.strict')}</label>
        </div>
        <div className="state-pool-start"><span>{t('statePool.candidateBudget', { groups: accounts.length * models.length, candidates: accounts.length * models.length * candidates, requests: accounts.length * models.length * candidates * 2 })}<small>{t('statePool.verifyOnBusiness')}</small></span><Button disabled={captureDisabled} onClick={() => void capture()}>{busy === 'capture' ? <RefreshCw className="animate-spin" /> : <Play />}{t('statePool.capture')}</Button></div>
      </section>
    </div>
    <div className="state-pool-toolbar">
      <SegmentedPillGroup value={view} onChange={setView} label={t('statePool.view')} options={[{ value: 'entries', label: t('statePool.entries') }, { value: 'jobs', label: t('statePool.jobs') }]} />
      {view === 'entries' ? <div><Button variant="outline" size="sm" disabled={!transferIDs.length || Boolean(busy)} onClick={() => void exportStates(transferIDs)}><Package />{t(selected.length ? 'statePool.copySelected' : 'statePool.copyAvailable', { count: transferIDs.length })}</Button><Button variant="ghost" size="icon-sm" title={t('statePool.download')} aria-label={t('statePool.download')} disabled={!transferIDs.length || Boolean(busy)} onClick={() => void exportStates(transferIDs, false, true)}><Download /></Button></div> : null}
    </div>
    {view === 'entries' ? <div className="state-pool-table-wrap"><table className="state-pool-table"><thead><tr>
      <th><Checkbox aria-label={t('statePool.selectAll')} checked={exportableIDs.length > 0 && exportableIDs.every(id => selected.includes(id))} onCheckedChange={checked => setSelected(checked ? exportableIDs : [])} /></th>
      <th>{t('statePool.accountModel')}</th><th>{t('statePool.state')}</th><th>{t('statePool.remaining')}</th><th>{t('statePool.reuse')}</th><th>{t('statePool.strict')}</th><th>{t('statePool.actions')}</th>
    </tr></thead><tbody>{data?.entries.map(entry => {
      const expired = entry.expires_at <= now
      const status = expired ? 'expired' : entry.status
      return <tr key={entry.id}>
        <td><Checkbox aria-label={t('statePool.selectEntry', { model: entry.model })} checked={selected.includes(entry.id)} disabled={expired} onCheckedChange={checked => setSelected(old => checked ? [...old, entry.id] : old.filter(id => id !== entry.id))} /></td>
        <td><strong>{STATE_MODEL_LABELS[entry.model] || entry.model}</strong><small>{entry.account_name || `#${entry.account_id}`}</small><small>{entry.model} · {entry.effort}</small></td>
        <td><span className={`state-pool-status is-${status}`}>{status === 'ready' ? <Check className="size-3" /> : null}{label(status)}</span><code>{entry.fingerprint.slice(0, 12)}</code></td>
        <td><span className="state-pool-countdown">{stateRemaining(entry.expires_at, now)}</span><small title={date(entry.captured_at)}>{date(entry.expires_at)}</small></td>
        <td><Switch aria-label={t('statePool.reuseModel', { model: entry.model })} checked={entry.enabled} disabled={busy === entry.id || expired && !entry.enabled} onCheckedChange={enabled => void changeEntry(entry, { enabled })} /></td>
        <td><Switch aria-label={t('statePool.strictModel', { model: entry.model })} checked={entry.strict} disabled={busy === entry.id || expired} onCheckedChange={strict => void changeEntry(entry, { strict })} /></td>
        <td><div className="state-pool-row-actions">
          <Button variant="ghost" size="icon-sm" title={t('statePool.copyRaw')} aria-label={t('statePool.copyRaw')} disabled={expired || Boolean(busy)} onClick={() => void exportStates([entry.id], true)}><Copy /></Button>
          <Button variant="ghost" size="icon-sm" title={t('statePool.copyPackage')} aria-label={t('statePool.copyPackage')} disabled={expired || Boolean(busy)} onClick={() => void exportStates([entry.id])}><Package /></Button>
          <Button variant="ghost" size="icon-sm" title={t('statePool.details')} aria-label={t('statePool.details')} onClick={() => setDetails({ name: entry.model, checks: entry.checks })}><Eye /></Button>
          <Button variant="ghost" size="icon-sm" title={t('statePool.recapture')} aria-label={t('statePool.recapture')} disabled={Boolean(busy)} onClick={() => void capture([entry.account_id], [entry.model])}><RefreshCw /></Button>
          <Button variant="ghost" size="icon-sm" title={t('statePool.delete')} aria-label={t('statePool.delete')} disabled={Boolean(busy)} onClick={() => void removeEntry(entry)}><Trash2 /></Button>
        </div></td>
      </tr>
    })}</tbody></table>{!data?.entries.length ? <div className="state-pool-empty">{t(data ? 'statePool.noEntries' : 'statePool.loading')}</div> : null}</div> : <div className="state-pool-job-list">
      {!groups.length ? <div className="state-pool-empty">{t('statePool.noJobs')}</div> : groups.map(group => {
        const job = group.representative
        return <div className="state-pool-group" key={group.id}><div className="state-pool-job">
        <div><strong>{STATE_MODEL_LABELS[job.model] || job.model}</strong><small>{job.account_name}</small></div>
        <div><span className={`state-pool-status is-${job.status}`}>{isStateJobActive(job) ? <RefreshCw className="size-3 animate-spin" /> : null}{label(job.status === 'running' ? job.phase : job.status)}</span><small>{date(job.created_at)}</small></div>
        <div className="state-pool-job-result"><span>{t('statePool.groupProgress', { done: group.candidates.filter(candidate => !isStateJobActive(candidate)).length, total: group.candidates.length })}</span>{job.error ? <small className="state-pool-error-text">{failure(job.error)}</small> : null}{job.retry_at ? <small>{t('statePool.retryAt', { time: date(job.retry_at) })}</small> : null}</div>
        <div className="state-pool-row-actions">{group.active ? <Button variant="ghost" size="icon-sm" title={t('statePool.cancelGroup')} aria-label={t('statePool.cancelGroup')} disabled={Boolean(busy)} onClick={() => void act(group.id, async () => { await api.cancelStateGroup(group.id) })}><Square /></Button> : <Button variant="ghost" size="icon-sm" title={t('statePool.recapture')} aria-label={t('statePool.recapture')} disabled={Boolean(busy)} onClick={() => void capture([job.account_id], [job.model])}><RefreshCw /></Button>}</div>
      </div><details className="state-pool-candidate-details"><summary>{t('statePool.candidateDetails')}</summary>{group.candidates.map(candidate => <div className="state-pool-candidate" key={candidate.id}>
        <span>#{candidate.candidate || 1} · {candidate.capture_proxy?.name || t('statePool.businessEgress')}<small>{candidate.capture_proxy?.session_id ? `Session ${candidate.capture_proxy.session_id}` : candidate.capture_proxy?.last_test_ip ? t('statePool.lastTestIP', { ip: candidate.capture_proxy.last_test_ip }) : t('statePool.ipUnknown')}</small>{candidate.forward_proxy?.id ? <small>{t('statePool.forwardEvidence', { proxy: candidate.forward_proxy.name })}</small> : null}</span>
        <span className={`state-pool-status is-${candidate.status}`}>{label(candidate.status === 'running' ? candidate.phase : candidate.status)}<small>{failure(candidate.error)}</small></span>
        <Button variant="ghost" size="icon-sm" title={t('statePool.details')} aria-label={t('statePool.details')} onClick={() => setDetails({ name: `${candidate.model} #${candidate.candidate || 1}`, checks: candidate.checks })}><Eye /></Button>
      </div>)}</details></div>})}
    </div>}

    <Dialog open={importOpen} onOpenChange={open => { setImportOpen(open); if (!open) setImportText('') }}><DialogContent className="state-pool-dialog"><DialogHeader><DialogTitle>{t('statePool.import')}</DialogTitle><DialogDescription>{t('statePool.importBoundary')}</DialogDescription></DialogHeader>
      <SegmentedPillGroup value={importMode} onChange={setImportMode} label={t('statePool.importFormat')} options={[{ value: 'package', label: t('statePool.package') }, { value: 'raw', label: t('statePool.raw') }]} />
      {importMode === 'raw' ? <div className="state-pool-import-fields"><label>{t('statePool.accounts')}<Select value={importAccount} onValueChange={setImportAccount} options={data?.accounts.map(account => ({ value: String(account.id), label: account.name || `#${account.id}` })) ?? []} /></label><label>{t('statePool.models')}<Select value={importModel} onValueChange={setImportModel} options={Object.entries(STATE_MODEL_LABELS).map(([value, label]) => ({ value, label }))} /></label><label>{t('statePool.originalCaptureTime')}<Input type="datetime-local" step={1} value={capturedAt} onChange={event => setCapturedAt(event.target.value)} /></label></div> : null}
      <Textarea autoComplete="off" spellCheck={false} aria-label={t(importMode === 'package' ? 'statePool.package' : 'statePool.raw')} className="state-pool-secret" value={importText} onChange={event => setImportText(event.target.value)} placeholder={t(importMode === 'package' ? 'statePool.packagePlaceholder' : 'statePool.rawPlaceholder')} />
      {importMode === 'package' && importText.trim() ? <div className="state-pool-import-preview" aria-live="polite">{previewError ? <p className="state-pool-error" role="alert">{previewError}</p> : preview?.text !== importText ? <p className="state-pool-meta">{t('statePool.previewLoading')}</p> : preview.items.map(item => <div key={item.index}><span><strong>{STATE_MODEL_LABELS[item.model] || item.model}</strong><small>{item.account_name || t('statePool.noMatch')}</small></span><span title={item.reason} className={`state-pool-status is-${item.status === 'invalid' ? 'failed' : 'ready'}`}>{t(`statePool.importStatus.${item.status}`)}<small>{item.status !== 'invalid' ? stateRemaining(item.expires_at, now) : item.reason}</small></span></div>)}</div> : null}
      <div className="state-pool-switches"><label><Switch checked={enable} onCheckedChange={setEnable} />{t('statePool.enableAfterVerify')}</label><label><Switch checked={strict} onCheckedChange={setStrict} />{t('statePool.strict')}</label></div>
      <DialogFooter><Button variant="outline" onClick={() => { setImportOpen(false); setImportText('') }}>{t('common.cancel')}</Button><Button disabled={!importText.trim() || Boolean(busy) || Boolean(data?.resin_enabled) || importMode === 'raw' && (!importAccount || !capturedAt) || importMode === 'package' && !importable} onClick={() => void importStates()}>{busy === 'import' ? <RefreshCw className="animate-spin" /> : <Upload />}{t(importMode === 'package' ? 'statePool.importAvailable' : 'statePool.importVerify', { count: importable })}</Button></DialogFooter>
    </DialogContent></Dialog>
    <Dialog open={Boolean(details)} onOpenChange={open => { if (!open) setDetails(undefined) }}><DialogContent className="state-pool-dialog"><DialogHeader><DialogTitle>{t('statePool.details')}</DialogTitle><DialogDescription>{details?.name}</DialogDescription></DialogHeader><div className="state-pool-evidence">{details?.checks.map((check, index) => <section key={index}><div className="state-pool-section-head"><strong>{t(`statePool.phase.${check.phase}`)}</strong><span className={`state-pool-status is-${check.passed ? 'ready' : 'failed'}`}>{t(check.passed ? 'statePool.passed' : 'statePool.failed')}</span></div><p className="state-pool-meta">{check.proxy_name || t('statePool.businessEgress')}{check.last_test_ip ? ` · ${t('statePool.lastTestIP', { ip: check.last_test_ip })}` : ''}</p><p className="state-pool-meta">HTTP {check.http_status} · {check.terminal || check.error} · {(check.duration_ms / 1000).toFixed(1)} s · {check.input_tokens + check.output_tokens} tokens</p><pre>{check.answer || check.error}</pre></section>)}</div></DialogContent></Dialog>
    <Dialog open={Boolean(copyFallback)} onOpenChange={open => { if (!open) setCopyFallback('') }}><DialogContent className="state-pool-dialog"><DialogHeader><DialogTitle>{t('statePool.copyFallback')}</DialogTitle><DialogDescription>{t('statePool.privateValue')}</DialogDescription></DialogHeader><Textarea readOnly aria-label={t('statePool.raw')} className="state-pool-secret" value={copyFallback} onFocus={event => event.target.select()} /></DialogContent></Dialog>
    {confirmDialog}
  </div>
}
