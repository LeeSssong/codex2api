import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Minus, RefreshCw } from 'lucide-react'
import { STATE_MODEL_LABELS } from '../lib/statePool'
import { hasValidModelState, type AccountStateModel } from '../lib/accountStateModels'

export default function AccountStateModels({ states }: { states?: AccountStateModel[] }) {
  const { t } = useTranslation()
  const [now, setNow] = useState(Date.now() / 1000)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now() / 1000), 1000)
    return () => clearInterval(timer)
  }, [])
  return <div className="flex max-w-64 flex-wrap gap-1.5" aria-label={t('ipv6State.accountColumn')}>
    {Object.entries(STATE_MODEL_LABELS).map(([model, label]) => {
      const entry = states?.find(item => item.model === model)
      const valid = hasValidModelState(states, model, now)
      const description = valid ? t('ipv6State.modelValid', { model: label, length: entry!.length, minutes: Math.ceil((entry!.expires_at - now) / 60) }) : t('ipv6State.modelMissing', { model: label })
      return <span key={model} title={description} aria-label={description} data-state-valid={valid} className={`inline-flex items-center gap-1 rounded px-1.5 py-1 text-[11px] ${valid ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-200' : 'bg-muted text-muted-foreground'}`}>
        {entry?.refreshing ? <RefreshCw aria-hidden="true" className="size-3 animate-spin" /> : valid ? <Check aria-hidden="true" className="size-3" /> : <Minus aria-hidden="true" className="size-3" />}{label}
      </span>
    })}
  </div>
}
