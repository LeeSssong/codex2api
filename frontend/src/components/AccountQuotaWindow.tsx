import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertCircle, Clock3 } from 'lucide-react'
import type { AccountRow } from '../types'
import { formatQuotaUSD, quotaWindowView } from '../lib/accountQuotaWindow'
import { formatBeijingTime } from '../utils/time'
import './account-quota-window.css'

/** One official quota window, with gateway fees kept separate from settlement. */
export default function AccountQuotaWindow({ account, slot }: { account: AccountRow; slot: '5h' | '7d' }) {
  const { t } = useTranslation()
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(timer)
  }, [])
  const snapshot = slot === '5h' ? account.quota_5h : account.quota_7d
  const view = quotaWindowView(account, slot, snapshot, account.quota_stats_status, now)
  const percent = view.used_percent
  // The live percentage remains useful while gateway statistics are pending.
  const livePercent = slot === '5h' ? account.usage_percent_5h : account.usage_percent_7d
  const progress = percent ?? (typeof livePercent === 'number' && Number.isFinite(livePercent) ? livePercent : undefined)
  const usable = view.state === 'ready' || view.state === 'low_sample' || view.state === 'no_usage' || view.used_usd !== undefined
  const resetAt = slot === '5h' ? account.reset_5h_at : account.reset_7d_at
  const resetMs = Date.parse(resetAt ?? '')
  const minutes = Math.max(0, Math.ceil((resetMs - now) / 60_000))
  const countdown = Number.isFinite(minutes) && minutes > 0 ? t('accounts.quotaResetIn', {
    days: Math.floor(minutes / 1440), hours: Math.floor(minutes % 1440 / 60), minutes: minutes % 60,
  }) : undefined
  const asOf = usable && view.as_of ? formatBeijingTime(view.as_of, '') : ''
  const observedAt = usable && view.observed_at ? formatBeijingTime(view.observed_at, '') : ''
  const warning = view.state === 'low_sample' || view.state === 'stale' || view.state === 'error'
  return (
    <section className="account-quota-window" aria-label={t('accounts.quotaWindowAria', { window: view.label })}>
      <div className="account-quota-window__heading">
        <span className="font-medium">{view.label}</span>
        <span className="font-semibold tabular-nums">{progress === undefined ? '—' : `${progress.toFixed(1)}%`}</span>
      </div>
      <div className="account-quota-window__track bg-muted" role="progressbar" aria-label={view.label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress === undefined ? undefined : Math.max(0, Math.min(100, progress))} aria-valuetext={progress === undefined ? t('accounts.quotaUnavailable') : `${progress.toFixed(1)}%`}>
        <div className={progress !== undefined && progress >= 90 ? 'bg-red-500' : progress !== undefined && progress >= 70 ? 'bg-amber-500' : 'bg-emerald-500'} style={{ width: `${Math.max(0, Math.min(100, progress ?? 0))}%` }} />
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
      {(view.state !== 'ready') && <div className={`account-quota-window__state ${warning ? 'text-amber-700 dark:text-amber-300' : 'text-muted-foreground'}`}>
        {warning && <AlertCircle className="size-3 shrink-0" aria-hidden="true" />}
        <span>{t(view.reason === 'observation_stale' ? 'accounts.quotaObservationStale' : `accounts.quotaState_${view.state}`)}</span>
      </div>}
      {countdown && <div className="account-quota-window__meta" title={formatBeijingTime(resetAt)}>
        <Clock3 className="size-3 shrink-0" aria-hidden="true" /><span>{countdown}</span>
      </div>}
      {usable && <div className="account-quota-window__source" title={t('accounts.quotaFreshnessDetail', { asOf: asOf || '—', observedAt: observedAt || '—' })}>
        <span>{t('accounts.quotaSource')}</span>
        {asOf && <time className="tabular-nums" dateTime={view.as_of}>{t('accounts.quotaUpdated', { time: asOf.slice(5, 16) })}</time>}
      </div>}
      {observedAt && <div className="account-quota-window__source">
        <time className="tabular-nums" dateTime={view.observed_at}>{t('accounts.quotaObserved', { time: observedAt.slice(5, 16) })}</time>
      </div>}
    </section>
  )
}
