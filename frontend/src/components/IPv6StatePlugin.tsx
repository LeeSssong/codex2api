import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, ChevronRight, Copy, RefreshCw, Settings2, Upload } from 'lucide-react'
import { api } from '../api'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { Input } from './ui/input'
import { Switch } from './ui/switch'
import { DraftNumberInput } from './ui/draft-number-input'
import { Select } from './ui/select'
import { Textarea } from './ui/textarea'
import { SegmentedPillGroup } from './ui/segmented-pill-group'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './ui/dialog'
import { STATE_MODEL_LABELS, stateRemaining, type StatePoolData } from '../lib/statePool'
import { ipv6StateDisplayStatus, isIPv6StateReady, parseIPv6States, parseStateLengths, serializeIPv6States, type IPv6StateConfig, type IPv6StateEntry, type IPv6StatePackage, type IPv6StateStatus } from '../lib/ipv6State'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../hooks/useToast'

export default function IPv6StatePlugin({ accounts, proxies }: { accounts: StatePoolData['accounts']; proxies: StatePoolData['proxies'] }) {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const [data, setData] = useState<IPv6StateStatus>()
  const [config, setConfig] = useState<IPv6StateConfig>()
  const [allAccounts, setAllAccounts] = useState(true)
  const [sources, setSources] = useState('')
  const [lengths, setLengths] = useState('')
  const [loadError, setLoadError] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importText, setImportText] = useState('')
  const [importError, setImportError] = useState('')
  const [importResult, setImportResult] = useState('')
  const [copyText, setCopyText] = useState('')
  const [detailKey, setDetailKey] = useState('')
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState('all')
  const [now, setNow] = useState(Date.now() / 1000)
  const offset = useRef(0)
  const revision = useRef(0)

  const accept = (next: IPv6StateStatus) => {
    offset.current = next.server_time - Date.now() / 1000
    setNow(next.server_time)
    setData(next)
  }

  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const refresh = async () => {
      const version = revision.current
      try {
        const next = await api.getIPv6State(controller.signal)
        if (!controller.signal.aborted && version === revision.current && version % 2 === 0) { accept(next); setLoadError('') }
      } catch (err) {
        if (!controller.signal.aborted) setLoadError(getErrorMessage(err))
      }
      if (!controller.signal.aborted) timer = setTimeout(refresh, 3000)
    }
    void refresh()
    const clock = setInterval(() => setNow(Date.now() / 1000 + offset.current), 1000)
    return () => { clearTimeout(timer); clearInterval(clock); controller.abort() }
  }, [])

  const describe = (code: string) => t(`ipv6State.codes.${code}`, { defaultValue: code })
  const name = (entry: IPv6StateEntry) => entry.account_name || `#${entry.account_id}`
  const key = (entry: IPv6StateEntry) => `${entry.account_id}/${entry.model}`
  const ready = data?.entries.filter(entry => isIPv6StateReady(entry, now)) ?? []
  const detail = data?.entries.find(entry => key(entry) === detailKey)
  const models = Object.keys(STATE_MODEL_LABELS).filter(model => data?.config.models.includes(model))
  const rows = Array.from(new Set(data?.entries.map(entry => entry.account_id) ?? [])).map(id => {
    const entries = data!.entries.filter(entry => entry.account_id === id)
    return { id, name: name(entries[0]), entries }
  })
  const visibleRows = rows.filter(row => `${row.name} ${row.id}`.toLowerCase().includes(search.toLowerCase()) &&
    (filter === 'all' || (filter === 'ready' ? row.entries.some(entry => isIPv6StateReady(entry, now)) : row.entries.some(entry => Boolean(entry.error) || entry.status === 'account_unavailable'))))

  const openSettings = () => {
    if (!data) return
    setConfig({ ...data.config })
    setAllAccounts(data.config.account_ids.length === 0)
    setSources(data.config.source_ips.join('\n'))
    setLengths(data.config.accepted_lengths.join(', '))
    setError('')
    setSettingsOpen(true)
  }

  const persist = async (next: IPv6StateConfig) => {
    setBusy(true); setError(''); revision.current++
    try {
      accept(await api.configureIPv6State(next))
      setSettingsOpen(false)
      showToast(t('ipv6State.saved'), 'success')
    } catch (err) { setError(describe(getErrorMessage(err))) }
    finally { revision.current++; setBusy(false) }
  }

  const toggle = (enabled: boolean) => {
    if (!data) return
    if (enabled && data.config.capture_mode === 'proxy' && !data.config.proxy_ids.length && !ready.length) {
      openSettings(); setError(t('ipv6State.setupFirst')); return
    }
    void persist({ ...data.config, enabled })
  }

  const saveSettings = () => {
    if (!config || !data) return
    if (!allAccounts && !config.account_ids.length) { setError(t('ipv6State.selectAccount')); return }
    if (!config.models.length) { setError(t('ipv6State.selectModel')); return }
    let acceptedLengths: number[]
    try { acceptedLengths = parseStateLengths(lengths) }
    catch { setError(t('ipv6State.invalidLengths')); return }
    void persist({ ...config, accepted_lengths: acceptedLengths, enabled: data.config.enabled, account_ids: allAccounts ? [] : config.account_ids, source_ips: sources.split(/[\s,]+/).filter(Boolean) })
  }

  const copy = async (entries: IPv6StateEntry[], raw = false) => {
    setBusy(true); setError('')
    try {
      const packs: IPv6StatePackage[] = []
      for (const entry of entries) packs.push(await api.exportIPv6State(entry.account_id, entry.model))
      const text = raw ? packs[0].value : packs.length === 1 ? JSON.stringify(packs[0]) : serializeIPv6States(packs)
      try { await navigator.clipboard.writeText(text); showToast(t('ipv6State.copiedCount', { count: packs.length }), 'success') }
      catch { setDetailKey(''); setCopyText(text) }
    } catch (err) { setError(getErrorMessage(err)) }
    finally { setBusy(false) }
  }

  const importState = async () => {
    let packs: IPv6StatePackage[]
    try { packs = parseIPv6States(importText) }
    catch { setImportError(t('ipv6State.invalidPackage')); return }
    setBusy(true); setImportError(''); setImportResult(''); revision.current++
    const failed: IPv6StatePackage[] = []
    const errors: string[] = []
    try {
      for (const pack of packs) {
        try { accept(await api.importIPv6State(pack)) }
        catch (err) { failed.push(pack); errors.push(`${STATE_MODEL_LABELS[pack.model] || pack.model}: ${describe(getErrorMessage(err))}`) }
      }
      const result = t('ipv6State.importSummary', { count: packs.length - failed.length, failed: failed.length })
      setImportResult(result)
      setImportText(failed.length ? serializeIPv6States(failed) : '')
      if (failed.length) setImportError(`${t('ipv6State.retryFailed')}\n${errors.join('\n')}`)
      else showToast(result, 'success')
    } finally { revision.current++; setBusy(false) }
  }

  return <section className="ipv6-state-plugin" aria-label={t('ipv6State.overview')}>
    <div className="ipv6-state-control">
      <div><h3>{t('ipv6State.automation')}</h3><p className="state-pool-meta">{t('ipv6State.automationHint')}</p></div>
      <label className="ipv6-state-enable"><span>{t(data?.config.enabled ? 'ipv6State.enabled' : 'ipv6State.disabled')}</span><Switch aria-label={t('ipv6State.automation')} checked={data?.config.enabled ?? false} disabled={busy || !data} onCheckedChange={toggle} /></label>
    </div>
    <div className="ipv6-state-toolbar">
      <div className="ipv6-state-summary" aria-live="polite"><strong>{t('ipv6State.readySummary', { ready: ready.length, total: data?.entries.length ?? 0 })}</strong><span className="state-pool-meta">{t(!data?.config.enabled ? 'ipv6State.offHint' : data?.running ? 'ipv6State.running' : 'ipv6State.idle', { active: data?.active_requests ?? 0, limit: data?.config.concurrency ?? 20 })}</span></div>
      <div className="ipv6-state-actions">
        <Button disabled={busy || !ready.length} onClick={() => void copy(ready)}><Copy />{t('ipv6State.copyAll')}</Button>
        <Button variant="outline" disabled={busy} onClick={() => { setImportOpen(true); setImportError(''); setImportResult('') }}><Upload />{t('ipv6State.paste')}</Button>
        <Button variant="outline" disabled={busy || !data} onClick={openSettings}><Settings2 />{t('ipv6State.settings')}</Button>
      </div>
    </div>
    {loadError || (!settingsOpen && error) || data?.error ? <p role="alert" className="state-pool-error">{loadError || error || describe(data?.error ?? '')}</p> : null}
    <div className="ipv6-state-filters"><Input aria-label={t('statePool.search')} placeholder={t('statePool.search')} value={search} onChange={event => setSearch(event.target.value)} /><Select aria-label={t('ipv6State.filter')} value={filter} onValueChange={setFilter} options={['all', 'ready', 'attention'].map(value => ({ value, label: t(`ipv6State.filter${value}`) }))} /></div>
    {!data ? <div className="state-pool-empty" role="status"><RefreshCw className="size-4 animate-spin inline-block" /> {t('statePool.loading')}</div> : !rows.length ? <div className="state-pool-empty"><p>{t('ipv6State.empty')}</p><Button variant="link" asChild><a href="/admin/accounts">{t('statePool.manageAccounts')}</a></Button></div> : <div className="ipv6-state-matrix">
      <table><caption className="sr-only">{t('ipv6State.overview')}</caption><thead><tr><th scope="col">{t('statePool.accounts')}</th>{models.map(model => <th scope="col" key={model}>{STATE_MODEL_LABELS[model]}</th>)}</tr></thead>
        <tbody>{visibleRows.map(row => <tr key={row.id}>
          <th scope="row"><div className="ipv6-state-account"><div><strong>{row.name}</strong><small>#{row.id}</small></div><Button variant="ghost" size="icon-sm" title={t('ipv6State.copyAccount')} aria-label={t('ipv6State.copyNamedAccount', { name: row.name })} disabled={busy || !row.entries.some(entry => isIPv6StateReady(entry, now))} onClick={() => void copy(row.entries.filter(entry => isIPv6StateReady(entry, now)))}><Copy /></Button></div></th>
          {models.map(model => {
            const entry = row.entries.find(item => item.model === model)
            const status = entry ? ipv6StateDisplayStatus(entry, now, data.config.enabled) : 'waiting'
            return <td key={model}><span className="ipv6-state-mobile-model">{STATE_MODEL_LABELS[model]}</span>{entry ? <Button variant="ghost" className={`ipv6-state-cell is-${status}`} aria-label={`${row.name} · ${STATE_MODEL_LABELS[model]} · ${describe(status)}`} onClick={() => { setDetailKey(key(entry)); setError('') }}>
              <span><span className="ipv6-state-cell-status">{status === 'ready' ? <Check className="size-3" /> : status === 'collecting' ? <RefreshCw className="size-3 animate-spin" /> : <span className="ipv6-state-dot" />}{describe(status)}</span>{status === 'ready' || entry.retry_at > now && data.config.enabled ? <small>{status === 'ready' ? stateRemaining(entry.expires_at, now) : t('ipv6State.retry', { time: stateRemaining(entry.retry_at, now) })}</small> : null}</span><ChevronRight className="size-3" />
            </Button> : <span className="state-pool-meta">—</span>}</td>
          })}
        </tr>)}</tbody></table>{!visibleRows.length ? <div className="state-pool-empty">{t('ipv6State.noMatches')}</div> : null}
    </div>}
    <p className="ipv6-state-footnote">{t('ipv6State.scopeHint')} {t('ipv6State.renewalHelp')}</p>

    <Dialog open={settingsOpen} onOpenChange={open => { if (!busy) { setSettingsOpen(open); setError('') } }}><DialogContent className="state-pool-dialog ipv6-state-dialog"><DialogHeader><DialogTitle>{t('ipv6State.settings')}</DialogTitle><DialogDescription>{t('ipv6State.settingsHint')}</DialogDescription></DialogHeader>
      {config ? <>
        <section className="ipv6-state-setting-section ipv6-state-rule-fields">
          <div><label htmlFor="ipv6-state-lengths">{t('ipv6State.acceptedLengths')}</label><Input disabled={busy} id="ipv6-state-lengths" aria-describedby="ipv6-state-lengths-help" value={lengths} onChange={event => setLengths(event.target.value)} placeholder="292, 332" autoComplete="off" spellCheck={false} /><p id="ipv6-state-lengths-help" className="state-pool-meta">{t('ipv6State.lengthsHelp')}</p></div>
          <div><label htmlFor="ipv6-state-concurrency">{t('ipv6State.concurrency')}</label><DraftNumberInput disabled={busy} id="ipv6-state-concurrency" aria-describedby="ipv6-state-concurrency-help" min={1} max={20} value={config.concurrency} onValueChange={value => setConfig({ ...config, concurrency: value })} /><p id="ipv6-state-concurrency-help" className="state-pool-meta">{t('ipv6State.concurrencyHelp')}{data && data.account_concurrency > 0 ? ` ${t('ipv6State.accountLimit', { count: data.account_concurrency })}` : ''}</p></div>
        </section>
        <section className="ipv6-state-setting-section"><h3>{t('statePool.models')}</h3><div className="ipv6-state-choices">{Object.entries(STATE_MODEL_LABELS).map(([model, label]) => <label key={model}><Checkbox disabled={busy} checked={config.models.includes(model)} onCheckedChange={checked => setConfig({ ...config, models: checked ? [...config.models, model] : config.models.filter(value => value !== model) })} />{label}</label>)}</div></section>
        <section className="ipv6-state-setting-section"><h3>{t('statePool.accounts')}</h3><label className="ipv6-state-enable"><Checkbox disabled={busy} checked={allAccounts} onCheckedChange={value => setAllAccounts(value === true)} />{t('ipv6State.allAccounts')}</label>{!allAccounts ? <div className="ipv6-state-choices ipv6-state-account-choices">{accounts.map(account => <label key={account.id}><Checkbox disabled={busy} checked={config.account_ids.includes(account.id)} onCheckedChange={checked => setConfig({ ...config, account_ids: checked ? [...config.account_ids, account.id] : config.account_ids.filter(id => id !== account.id) })} /><span>{account.name || `#${account.id}`}</span></label>)}</div> : null}</section>
        <section className="ipv6-state-setting-section"><h3>{t('ipv6State.captureMode')}</h3><SegmentedPillGroup disabled={busy} value={config.capture_mode} onChange={value => setConfig({ ...config, capture_mode: value })} label={t('ipv6State.captureMode')} options={[{ value: 'proxy', label: t('ipv6State.proxyShort') }, { value: 'local_ipv6', label: t('ipv6State.localMode') }]} />
          {config.capture_mode === 'proxy' ? <><div className="ipv6-state-choices">{proxies.map(proxy => <label key={proxy.id}><Checkbox checked={config.proxy_ids.includes(proxy.id)} disabled={busy || !proxy.enabled || proxy.id === config.forward_proxy_id} onCheckedChange={checked => setConfig({ ...config, proxy_ids: checked ? [...config.proxy_ids, proxy.id] : config.proxy_ids.filter(id => id !== proxy.id) })} /><span>{proxy.name || `#${proxy.id}`}</span></label>)}{!proxies.length ? <p className="state-pool-meta">{t('statePool.noProxies')}</p> : null}</div><Button asChild variant="link" className="ipv6-state-manage"><a href="/admin/proxies">{t('statePool.manageProxies')}</a></Button></> : <div className="ipv6-state-source-field"><label htmlFor="ipv6-state-sources">{t('ipv6State.sources')}</label><Textarea disabled={busy} id="ipv6-state-sources" autoComplete="off" spellCheck={false} value={sources} onChange={event => setSources(event.target.value)} placeholder={t('ipv6State.autoSources')} /><p className="state-pool-meta">{t('ipv6State.detected', { count: data?.local_ips.length ?? 0 })}</p><div className="ipv6-state-addresses">{data?.local_ips.map(ip => <code key={ip}>{ip}</code>)}</div></div>}
        </section>
        <details className="ipv6-state-advanced"><summary>{t('ipv6State.advanced')}</summary><div className="ipv6-state-advanced-fields"><label className="ipv6-state-interval" htmlFor="ipv6-state-interval">{t('ipv6State.interval')}<DraftNumberInput disabled={busy} id="ipv6-state-interval" min={1} max={300} value={config.interval_seconds} onValueChange={value => setConfig({ ...config, interval_seconds: value })} /></label>{config.capture_mode === 'proxy' ? <><label htmlFor="state292-forward">{t('statePool.forwardProxy')}</label><Select disabled={busy} id="state292-forward" value={String(config.forward_proxy_id)} onValueChange={value => setConfig({ ...config, forward_proxy_id: Number(value), proxy_ids: config.proxy_ids.filter(id => id !== Number(value)) })} options={[{ value: '0', label: t('ipv6State.noForward') }, ...proxies.filter(proxy => proxy.enabled).map(proxy => ({ value: String(proxy.id), label: proxy.name || `#${proxy.id}` }))]} /><label className="ipv6-state-enable"><Checkbox disabled={busy} checked={config.new_session} onCheckedChange={value => setConfig({ ...config, new_session: value === true })} />{t('ipv6State.newSession')}</label></> : null}<p className="state-pool-meta">{t('ipv6State.policy')}</p></div></details>
      </> : null}{error ? <p role="alert" className="state-pool-error">{error}</p> : null}<DialogFooter><Button variant="outline" disabled={busy} onClick={() => { setSettingsOpen(false); setError('') }}>{t('common.cancel')}</Button><Button disabled={busy} onClick={saveSettings}>{busy ? <RefreshCw className="animate-spin" /> : null}{t('ipv6State.save')}</Button></DialogFooter>
    </DialogContent></Dialog>

    <Dialog open={Boolean(detailKey)} onOpenChange={open => { if (!open) { setDetailKey(''); setError('') } }}><DialogContent className="ipv6-state-dialog"><DialogHeader><DialogTitle>{detail ? STATE_MODEL_LABELS[detail.model] || detail.model : t('statePool.details')}</DialogTitle><DialogDescription>{detail ? name(detail) : t('ipv6State.entryMissing')}</DialogDescription></DialogHeader>{detail ? <>
      <div className="ipv6-state-detail-status"><strong>{describe(ipv6StateDisplayStatus(detail, now, data?.config.enabled ?? false))}</strong><span>{isIPv6StateReady(detail, now) ? t('ipv6State.validFor', { time: stateRemaining(detail.expires_at, now) }) : detail.retry_at > now && data?.config.enabled ? t('ipv6State.retry', { time: stateRemaining(detail.retry_at, now) }) : ''}</span></div>
      {detail.error ? <p className="state-pool-error">{describe(detail.error)}</p> : null}
      {detail.refreshing ? <p role="status" className="state-pool-meta">{t('ipv6State.refreshing')}</p> : null}
      {detail.status === 'account_unavailable' ? <Button asChild variant="link"><a href="/admin/accounts">{t('statePool.manageAccounts')}</a></Button> : null}
      <dl className="ipv6-state-details">{[
        ['model', detail.model], ['attempts', String(detail.attempts)], ['length', String(detail.last_length || '—')],
        ['httpStatus', detail.http_status ? String(detail.http_status) : '—'], ['captureRoute', detail.proxy_name || detail.source_ip || '—'],
        ['cooldownReason', detail.cooldown_reason ? describe(`cooldown_${detail.cooldown_reason}`) : '—'],
        ['retrySource', detail.retry_source ? t(`ipv6State.${detail.retry_source}`) : '—'],
        ['issued', detail.issued_at ? new Date(detail.issued_at * 1000).toLocaleString() : '—'], ['expires', detail.expires_at ? new Date(detail.expires_at * 1000).toLocaleString() : '—'],
      ].map(([label, value]) => <div key={label}><dt>{t(`ipv6State.${label}`)}</dt><dd>{value}</dd></div>)}</dl>
      <p className="state-pool-meta">{t('ipv6State.scopeHint')}</p>{error ? <p role="alert" className="state-pool-error">{error}</p> : null}<DialogFooter><Button variant="outline" disabled={busy || !isIPv6StateReady(detail, now)} onClick={() => void copy([detail], true)}><Copy />{t('ipv6State.copyRaw')}</Button><Button disabled={busy || !isIPv6StateReady(detail, now)} onClick={() => void copy([detail])}><Copy />{t('ipv6State.copyPackage')}</Button></DialogFooter>
    </> : null}</DialogContent></Dialog>

    <Dialog open={importOpen} onOpenChange={open => { if (!busy) { setImportOpen(open); if (!open) setImportText('') } }}><DialogContent className="state-pool-dialog ipv6-state-dialog"><DialogHeader><DialogTitle>{t('ipv6State.paste')}</DialogTitle><DialogDescription>{t('ipv6State.pasteHint')}</DialogDescription></DialogHeader><Textarea className="state-pool-secret" aria-label={t('ipv6State.import')} autoComplete="off" spellCheck={false} disabled={busy} value={importText} onChange={event => { setImportText(event.target.value); setImportError(''); setImportResult('') }} placeholder={t('ipv6State.pastePlaceholder')} /><p className="state-pool-meta">{t('ipv6State.importHint')}</p>{importResult ? <p role="status">{importResult} {t(data?.config.enabled ? 'ipv6State.importActiveHint' : 'ipv6State.importOffHint')}</p> : null}{importError ? <p className="state-pool-error ipv6-state-import-error" role="alert">{importError}</p> : null}<DialogFooter><Button variant="outline" disabled={busy} onClick={() => { setImportOpen(false); setImportText('') }}>{t('common.close')}</Button><Button disabled={busy || !importText.trim()} onClick={() => void importState()}>{busy ? <RefreshCw className="animate-spin" /> : <Upload />}{t('ipv6State.import')}</Button></DialogFooter></DialogContent></Dialog>
    <Dialog open={Boolean(copyText)} onOpenChange={open => { if (!open) setCopyText('') }}><DialogContent><DialogHeader><DialogTitle>{t('statePool.copyFallback')}</DialogTitle><DialogDescription>{t('statePool.privateValue')}</DialogDescription></DialogHeader><Textarea className="state-pool-secret" aria-label={t('ipv6State.copyRaw')} readOnly value={copyText} onFocus={event => event.target.select()} /></DialogContent></Dialog>
  </section>
}
