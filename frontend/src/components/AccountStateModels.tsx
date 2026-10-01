import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Check, Minus, Pause, RefreshCw } from 'lucide-react'
import { STATE_MODEL_LABELS } from '../lib/statePool'
import { hasValidModelState, type AccountStateModel } from '../lib/accountStateModels'
import { stateTaskLabel } from '../lib/ipv6State'
import { useStateClock } from '../hooks/useStateClock'

export default function AccountStateModels({ states, accountID }: { states?: AccountStateModel[]; accountID?: number }) {
  const { t } = useTranslation()
  const now = useStateClock()
  if (!states?.length) return null
  return <div className="flex max-w-64 flex-wrap gap-1.5" aria-label={t('ipv6State.accountColumn')}>
    {states.map(entry => {
      const model = entry.model
      const label = STATE_MODEL_LABELS[model] || model
      const valid = hasValidModelState(states, model, now)
      const task = stateTaskLabel(valid, entry.capture_phase, entry.updated_at, now)
      const restriction = entry.restriction ? t(`ipv6State.restrictions.${entry.restriction}`, { defaultValue: t(`ipv6State.codes.cooldown_${entry.restriction}`) }) : ''
      const description = `${entry.in_scope === false ? t('ipv6State.outOfScope') : valid ? t('ipv6State.modelValid', { model: label, length: entry.length, minutes: Math.ceil((entry.expires_at - now) / 60) }) : t('ipv6State.modelMissing', { model: label })}${task ? ` · ${restriction || t(`ipv6State.codes.${task}`)}` : ''}`
      return <Link to={`/state-pool?state_model=${encodeURIComponent(model)}${accountID ? `&account=${accountID}` : ''}`} key={model} title={description} aria-label={description} data-state-valid={valid} className={`inline-flex min-h-7 flex-wrap items-center gap-1 rounded px-2 py-1 text-[11px] leading-relaxed focus-visible:outline-2 ${valid ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-200' : 'bg-muted text-muted-foreground'}`}>
        {entry.capture_phase === 'collecting' ? <RefreshCw aria-hidden="true" className="size-3 shrink-0 motion-safe:animate-spin" /> : entry.capture_phase === 'account_unavailable' ? <Pause aria-hidden="true" className="size-3 shrink-0" /> : valid ? <Check aria-hidden="true" className="size-3 shrink-0" /> : <Minus aria-hidden="true" className="size-3 shrink-0" />}{label}{task === 'renewing' || task === 'renewalRetry' ? <span>{t(`ipv6State.codes.${task}`)}</span> : null}
      </Link>
    })}
    <span className="basis-full text-[11px] text-muted-foreground">{states.every(item => item.in_scope === false) ? t('ipv6State.outOfScope') : t('ipv6State.accountCoverage', { total: states.length, count: states.filter(item => hasValidModelState(states, item.model, now)).length })}</span>
  </div>
}
