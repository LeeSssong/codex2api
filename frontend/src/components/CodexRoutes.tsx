import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { GitBranch, LoaderCircle } from 'lucide-react'
import { api } from '../api'
import type { CodexPathSnapshot } from '../types'
import { Button } from './ui/button'
import { Select } from './ui/select'

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
  const accountID = ids.length === 1 ? ids[0] : undefined
  useEffect(() => {
    setPaths(undefined); setMessage('')
    if (!open || !accountID) return
    let current = true
    void api.getCodexRoutes(accountID).then(result => { if (current) setPaths(result.paths) }).catch(() => { if (current) { setFailed(true); setMessage(t('codexRoutes.loadFailed')) } })
    return () => { current = false }
  }, [accountID, open, t])
  async function save() {
    setBusy(true); setMessage(''); setFailed(false)
    try {
      const result = await api.updateCodexRoutes({ ids, upstream: path, ...(action === 'reset' ? { reset_observations: true } : { allowed: action === 'allow' }) })
      onChanged?.()
      setMessage(t('codexRoutes.saved', { count: result.updated }))
      if (accountID) setPaths((await api.getCodexRoutes(accountID)).paths)
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
        <Button type="button" size="sm" disabled={busy || !ids.length || ids.length > 100} onClick={() => void save()}>{busy && <LoaderCircle className="size-4 motion-safe:animate-spin" aria-hidden="true" />}{t('codexRoutes.apply')}</Button>
      </div>
      <p className="text-xs text-muted-foreground">{t('codexRoutes.resetHint')}</p>
      {ids.length > 100 && <p className="text-xs text-destructive">{t('codexRoutes.batchLimit')}</p>}
      {message && <p role={failed ? 'alert' : 'status'} className={failed ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>{message}</p>}
    </div>}
  </div>
}
