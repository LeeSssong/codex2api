import { useContext, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertCircle, Clock3 } from 'lucide-react'
import type { AccountRow } from '../types'
import { formatQuotaUSD, quotaResetText, quotaWindowView } from '../lib/accountQuotaWindow'
import { formatBeijingTime } from '../utils/time'
import './account-quota-window.css'
import { AccountQuotaRefreshContext } from './AccountQuotaRefreshContext'

/** One official quota window, with gateway fees kept separate from settlement. */
export default function AccountQuotaWindow({ account, slot }: { account: AccountRow; slot: '5h' | '7d' }) {
  const { t } = useTranslation()
  const requestUsage = useContext(AccountQuotaRefreshContext)
  const root = useRef<HTMLElement>(null)
  const accountRef = useRef(account)
  accountRef.current = account
  useEffect(() => {
    if (!requestUsage || window.matchMedia('(min-width: 768px)').matches) return
    const request = () => requestUsage(accountRef.current)
    if (typeof IntersectionObserver === 'undefined') { request(); return }
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) { request(); observer.disconnect() }
    }, { rootMargin: '200px 0px', threshold: 0.01 })
    if (root.current) observer.observe(root.current)
    return () => observer.disconnect()
  }, [requestUsage, account.id])
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000)
    return () => window.clearInterval(timer)
  }, [])
  const snapshot = slot === '5h' ? account.quota_5h : account.quota_7d
  const view = quotaWindowView(account, slot, snapshot, account.quota_stats_status, now)
  const percent = view.used_percent
  const progress = percent
  const usable = view.state === 'ready' || view.state === 'low_sample' || view.state === 'no_usage' || view.used_usd !== undefined
  const resetAt = view.reset_at
  const resetText = quotaResetText(resetAt, progress, now)
  const countdown = resetText === 'pending' ? t('accounts.quotaResetPending') : resetText === 'now' ? t('accounts.quotaResetNow') : resetText
  const asOf = usable && view.as_of ? formatBeijingTime(view.as_of, '') : ''
  const warning = view.state === 'error'
  return (
    <section ref={root} className="account-quota-window" aria-label={t('accounts.quotaWindowAria', { window: view.label })}>
      <div className="account-quota-window__heading">
        <span className="font-medium">{view.label}</span>
        <span className="font-semibold tabular-nums">{progress === undefined ? '—' : Math.round(progress) > 999 ? '>999%' : `${Math.round(progress)}%`}</span>
      </div>
      <div className="account-quota-window__track bg-muted" role="progressbar" aria-label={view.label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress === undefined ? undefined : Math.max(0, Math.min(100, progress))} aria-valuetext={progress === undefined ? t('accounts.quotaUnavailable') : Math.round(progress) > 999 ? '>999%' : `${Math.round(progress)}%`}>
        <div className={progress !== undefined && progress >= 90 ? 'bg-red-500' : progress !== undefined && progress >= 75 ? 'bg-amber-500' : 'bg-emerald-500'} style={{ width: `${Math.max(0, Math.min(100, progress ?? 0))}%` }} />
      </div>
      <div className="account-quota-window__amounts tabular-nums">
        <span>{t('accounts.quotaUsed')} <strong>{formatQuotaUSD(view.used_usd)}</strong></span>
        <span className="text-muted-foreground" aria-hidden="true">/</span>
        <span className="text-muted-foreground">{t('accounts.quotaEstimatedTotal')} <strong className="text-foreground">{formatQuotaUSD(view.estimated_total_usd)}</strong></span>
      </div>
      {usable && <div className="account-quota-window__meta tabular-nums">
        <span>{view.requests === undefined ? '—' : view.requests.toLocaleString()} {t('accounts.usageReqUnit')}</span>
        <span>{view.tokens === undefined ? '—' : view.tokens.toLocaleString()} {t('accounts.usageTokUnit')}</span>
        <span>{t('accounts.userBilledLabel')} {formatQuotaUSD(view.user_usd)}</span>
      </div>}
      {(view.cost_basis === 'legacy' || view.cost_basis === 'mixed') && <div className="account-quota-window__source">{t('accounts.quotaLegacyCost')}</div>}
      {(view.state !== 'ready') && <div className={`account-quota-window__state ${warning ? 'text-amber-700 dark:text-amber-300' : 'text-muted-foreground'}`}>
        {warning && <AlertCircle className="size-3 shrink-0" aria-hidden="true" />}
        <span>{t(`accounts.quotaState_${view.state}`)}</span>
      </div>}
      {countdown && <div className="account-quota-window__meta" title={formatBeijingTime(resetAt)}>
        <Clock3 className="size-3 shrink-0" aria-hidden="true" /><span>{countdown}</span>
      </div>}
      {usable && <div className="account-quota-window__source" title={asOf}>
        <span>{t('accounts.quotaSource')}</span>
        {asOf && <time className="tabular-nums" dateTime={view.as_of}>{t('accounts.quotaUpdated', { time: asOf.slice(5, 16) })}</time>}
      </div>}

    </section>
  )
}
