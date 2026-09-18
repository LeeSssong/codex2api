import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Package, RefreshCw, Save, Upload } from 'lucide-react'
import { api } from '../api'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { Switch } from './ui/switch'
import { DraftNumberInput } from './ui/draft-number-input'
import { Select } from './ui/select'
import { Textarea } from './ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from './ui/dialog'
import { STATE_MODEL_LABELS, stateRemaining, type StatePoolData } from '../lib/statePool'
import type { IPv6StateConfig, IPv6StateEntry, IPv6StateStatus } from '../lib/ipv6State'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../hooks/useToast'

export default function IPv6StatePlugin({ accounts, proxies }: { accounts: StatePoolData['accounts']; proxies: StatePoolData['proxies'] }) {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const [data, setData] = useState<IPv6StateStatus>()
  const [config, setConfig] = useState<IPv6StateConfig>()
  const [allAccounts, setAllAccounts] = useState(true)
  const [sources, setSources] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [importText, setImportText] = useState('')
  const [copyText, setCopyText] = useState('')
  const initialized = useRef(false)

  useEffect(() => {
    const controller = new AbortController()
    const refresh = async () => {
      try {
        const next = await api.getIPv6State(controller.signal)
        if (controller.signal.aborted) return
        setData(next)
        if (!initialized.current) {
          initialized.current = true
          setConfig(next.config)
          setAllAccounts(next.config.account_ids.length === 0)
          setSources(next.config.source_ips.join('\n'))
        }
      } catch (err) {
        if (!controller.signal.aborted) setError(getErrorMessage(err))
      }
    }
    void refresh()
    const timer = setInterval(() => void refresh(), 3000)
    return () => { clearInterval(timer); controller.abort() }
  }, [])

  const save = async (enabled?: boolean) => {
    if (!config || !data) return
    setBusy(true)
    setError('')
    try {
      const next = enabled === false ? { ...data.config, enabled: false } : {
        ...config, enabled: enabled ?? data.config.enabled,
        account_ids: allAccounts ? [] : config.account_ids,
        source_ips: sources.split(/[\s,]+/).filter(Boolean),
      }
      if (!allAccounts && next.account_ids.length === 0 && enabled !== false) throw new Error(t('ipv6State.selectAccount'))
      const result = await api.configureIPv6State(next)
      setData(result)
      setConfig(result.config)
      setAllAccounts(result.config.account_ids.length === 0)
      setSources(result.config.source_ips.join('\n'))
      showToast(t('ipv6State.saved'), 'success')
    } catch (err) { setError(getErrorMessage(err)) }
    finally { setBusy(false) }
  }

  const copy = async (entry: IPv6StateEntry, raw: boolean) => {
    setBusy(true)
    setError('')
    try {
      const pack = await api.exportIPv6State(entry.account_id, entry.model)
      const text = raw ? pack.value : JSON.stringify(pack)
      try { await navigator.clipboard.writeText(text); showToast(t('ipv6State.copied'), 'success') }
      catch { setCopyText(text) }
    } catch (err) { setError(getErrorMessage(err)) }
    finally { setBusy(false) }
  }

  const importState = async () => {
    setBusy(true)
    setError('')
    try {
      setData(await api.importIPv6State(JSON.parse(importText)))
      setImportOpen(false)
      setImportText('')
      showToast(t('ipv6State.imported'), 'success')
    } catch (err) { setError(getErrorMessage(err)) }
    finally { setBusy(false) }
  }

  const describe = (code: string) => t(`ipv6State.codes.${code}`, { defaultValue: code })
  return <section className="ipv6-state-plugin" aria-labelledby="ipv6-state-title">
    <div className="state-pool-section-head">
      <h3 id="ipv6-state-title">{t('ipv6State.title')}</h3>
      <label className="ipv6-state-enable"><span>{t(data?.config.enabled ? 'ipv6State.enabled' : 'ipv6State.disabled')}</span><Switch aria-label={t('ipv6State.title')} checked={data?.config.enabled ?? false} disabled={busy || !config} onCheckedChange={value => void save(value)} /></label>
    </div>
    <p className="state-pool-meta">{t('ipv6State.policy')}</p>
    {error || data?.error ? <p role="alert" className="state-pool-error">{error || describe(data?.error ?? '')}</p> : null}
    {!config ? <RefreshCw className="size-4 animate-spin" aria-label={t('statePool.loading')} /> : <>
      <div className="ipv6-state-fields">
        <div>
          <label className="ipv6-state-enable"><Checkbox checked={allAccounts} onCheckedChange={value => setAllAccounts(value === true)} />{t('ipv6State.allAccounts')}</label>
          {!allAccounts ? <div className="ipv6-state-choices">{accounts.map(account => <label key={account.id}><Checkbox checked={config.account_ids.includes(account.id)} onCheckedChange={checked => setConfig({ ...config, account_ids: checked ? [...config.account_ids, account.id] : config.account_ids.filter(id => id !== account.id) })} /><span>{account.name || `#${account.id}`}</span></label>)}</div> : null}
          <div className="ipv6-state-choices">{Object.entries(STATE_MODEL_LABELS).map(([model, label]) => <label key={model}><Checkbox checked={config.models.includes(model)} onCheckedChange={checked => setConfig({ ...config, models: checked ? [...config.models, model] : config.models.filter(value => value !== model) })} />{label}</label>)}</div>
          <label className="ipv6-state-interval" htmlFor="ipv6-state-interval">{t('ipv6State.interval')}<DraftNumberInput id="ipv6-state-interval" min={1} max={300} value={config.interval_seconds} onValueChange={value => setConfig({ ...config, interval_seconds: value })} /></label>
        </div>
        <div>
          <label htmlFor="state292-mode">{t('ipv6State.captureMode')}</label>
          <Select id="state292-mode" value={config.capture_mode} onValueChange={value => setConfig({ ...config, capture_mode: value as 'proxy' | 'local_ipv6' })} options={[{ value: 'proxy', label: t('ipv6State.proxyMode') }, { value: 'local_ipv6', label: t('ipv6State.localMode') }]} />
          {config.capture_mode === 'proxy' ? <>
            <div className="ipv6-state-choices">{proxies.map(proxy => <label key={proxy.id}><Checkbox checked={config.proxy_ids.includes(proxy.id)} disabled={!proxy.enabled || proxy.id === config.forward_proxy_id} onCheckedChange={checked => setConfig({ ...config, proxy_ids: checked ? [...config.proxy_ids, proxy.id] : config.proxy_ids.filter(id => id !== proxy.id) })} />{proxy.name || `#${proxy.id}`}</label>)}</div>
            <label htmlFor="state292-forward">{t('statePool.forwardProxy')}</label>
            <Select id="state292-forward" value={String(config.forward_proxy_id)} onValueChange={value => setConfig({ ...config, forward_proxy_id: Number(value), proxy_ids: config.proxy_ids.filter(id => id !== Number(value)) })} options={[{ value: '0', label: t('ipv6State.noForward') }, ...proxies.filter(proxy => proxy.enabled).map(proxy => ({ value: String(proxy.id), label: proxy.name || `#${proxy.id}` }))]} />
            <label className="ipv6-state-enable"><Checkbox checked={config.new_session} onCheckedChange={value => setConfig({ ...config, new_session: value === true })} />{t('ipv6State.newSession')}</label>
            <a href="/admin/proxies">{t('statePool.manageProxies')}</a>
          </> : <>
          <label htmlFor="ipv6-state-sources">{t('ipv6State.sources')}</label>
          <Textarea id="ipv6-state-sources" autoComplete="off" spellCheck={false} value={sources} onChange={event => setSources(event.target.value)} placeholder={t('ipv6State.autoSources')} />
          <p className="state-pool-meta">{t('ipv6State.detected', { count: data?.local_ips.length ?? 0 })}</p>
          <div className="ipv6-state-addresses">{data?.local_ips.map(ip => <code key={ip}>{ip}</code>)}</div>
          </>}
        </div>
      </div>
      <div className="ipv6-state-actions">
        <Button variant="outline" disabled={busy} onClick={() => void save()}>{busy ? <RefreshCw className="animate-spin" /> : <Save />}{t('ipv6State.save')}</Button>
        <Button variant="outline" disabled={busy} onClick={() => setImportOpen(true)}><Upload />{t('ipv6State.import')}</Button>
        <span className="state-pool-meta" aria-live="polite">{t(data?.running ? 'ipv6State.running' : 'ipv6State.idle')}</span>
      </div>
      <div className="ipv6-state-table" tabIndex={0} role="region" aria-label={t('ipv6State.entries')}>
        <table><thead><tr>{['account', 'model', 'result', 'attempts', 'length', 'remaining', 'actions'].map(key => <th key={key}>{t(`ipv6State.${key}`)}</th>)}</tr></thead>
          <tbody>{!data?.entries.length ? <tr><td colSpan={7}>{t('ipv6State.empty')}</td></tr> : data.entries.map(entry => <tr key={`${entry.account_id}/${entry.model}`}>
            <td><span>{entry.account_name || `#${entry.account_id}`}</span><small>{entry.proxy_name || entry.source_ip}</small>{entry.session_id ? <small>Session {entry.session_id}</small> : null}</td>
            <td>{STATE_MODEL_LABELS[entry.model] || entry.model}</td>
            <td><span className={`state-pool-status is-${entry.status === 'ready' ? 'ready' : 'queued'}`}>{describe(entry.status)}</span>{entry.error ? <small>{describe(entry.error)}</small> : null}{entry.http_status > 0 ? <small>HTTP {entry.http_status}</small> : null}</td>
            <td>{entry.attempts}</td><td>{entry.last_length || '-'}</td>
            <td title={entry.issued_at ? `${t('ipv6State.issued')}: ${new Date(entry.issued_at * 1000).toLocaleString()}` : undefined}>{entry.expires_at > (data?.server_time ?? 0) ? stateRemaining(entry.expires_at, data?.server_time ?? 0) : '-'}{entry.retry_at > (data?.server_time ?? 0) ? <small>{t('ipv6State.retry', { time: stateRemaining(entry.retry_at, data?.server_time ?? 0) })}</small> : null}</td>
            <td><div className="ipv6-state-row-actions"><Button variant="ghost" size="icon" disabled={busy || entry.status !== 'ready'} title={t('ipv6State.copyRaw')} aria-label={t('ipv6State.copyRaw')} onClick={() => void copy(entry, true)}><Copy /></Button><Button variant="ghost" size="icon" disabled={busy || entry.status !== 'ready'} title={t('ipv6State.copyPackage')} aria-label={t('ipv6State.copyPackage')} onClick={() => void copy(entry, false)}><Package /></Button></div></td>
          </tr>)}</tbody></table>
      </div>
    </>}
    <Dialog open={importOpen} onOpenChange={open => { setImportOpen(open); if (!open) setImportText('') }}><DialogContent><DialogHeader><DialogTitle>{t('ipv6State.import')}</DialogTitle><DialogDescription>{t('ipv6State.importHint')}</DialogDescription></DialogHeader><Textarea aria-label={t('ipv6State.import')} autoComplete="off" spellCheck={false} value={importText} onChange={event => setImportText(event.target.value)} />{error ? <p className="state-pool-error" role="alert">{error}</p> : null}<DialogFooter><Button disabled={busy || !importText.trim()} onClick={() => void importState()}><Upload />{t('ipv6State.import')}</Button></DialogFooter></DialogContent></Dialog>
    <Dialog open={Boolean(copyText)} onOpenChange={open => { if (!open) setCopyText('') }}><DialogContent><DialogHeader><DialogTitle>{t('statePool.copyFallback')}</DialogTitle><DialogDescription>{t('statePool.privateValue')}</DialogDescription></DialogHeader><Textarea aria-label={t('ipv6State.copyRaw')} readOnly value={copyText} onFocus={event => event.target.select()} /></DialogContent></Dialog>
  </section>
}
