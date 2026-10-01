import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowRight } from 'lucide-react'
import { Button } from './ui/button'
import { STATE_MODEL_LABELS } from '../lib/statePool'
import type { StateSummary } from '../lib/accountStateModels'

export default function StateCoverage({ summary, target = 'accounts', includeSaved = false }: { summary?: StateSummary; target?: 'accounts' | 'state-pool'; includeSaved?: boolean }) {
  const { t } = useTranslation()
  if (!summary) return null
  return <div className="min-w-0 space-y-2.5 text-xs leading-relaxed text-muted-foreground">
    <p className="font-medium">{t('ipv6State.coverage', { total: summary.models.length, count: summary.covered_models })}</p>
    <ul className="flex flex-wrap gap-x-6 gap-y-2" aria-label={t('ipv6State.modelCoverage')}>
      {summary.models.map(item => <li className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1" key={item.model}>
        <span className="font-semibold text-foreground">{STATE_MODEL_LABELS[item.model] || item.model}</span>
        {includeSaved ? <Link className="inline-flex min-h-8 items-center rounded tabular-nums underline decoration-border underline-offset-4 hover:text-foreground focus-visible:outline-2" aria-label={t('ipv6State.modelSavedCount', { model: STATE_MODEL_LABELS[item.model] || item.model, count: item.reuse_accounts })} to={`/${target}?state=valid&state_model=${encodeURIComponent(item.model)}`}>
          {t('ipv6State.savedCount', { count: item.reuse_accounts })}
        </Link> : null}
        <Link className="inline-flex min-h-8 items-center rounded tabular-nums underline decoration-border underline-offset-4 hover:text-foreground focus-visible:outline-2" aria-label={t('ipv6State.modelCount', { model: STATE_MODEL_LABELS[item.model] || item.model, count: item.available_accounts })} to={`/${target}?state=available&state_model=${encodeURIComponent(item.model)}`}>
          {t('ipv6State.availableCount', { count: item.available_accounts })}
        </Link>
      </li>)}
    </ul>
    <p>{t(summary.enabled ? summary.require_valid_state ? 'ipv6State.strictMode' : 'ipv6State.optionalMode' : 'ipv6State.reuseOff')}</p>
    {summary.require_valid_state && !summary.enabled ? <div role="status" className="flex flex-wrap items-center gap-2 text-amber-700 dark:text-amber-300">
      <span>{t('ipv6State.strictDisabled')}</span><Button asChild size="sm" variant="link"><Link to="/state-pool?settings=1">{t('ipv6State.settings')}<ArrowRight /></Link></Button><Button asChild size="sm" variant="link"><Link to="/settings?tab=codex#settings-codex-state">{t('ipv6State.callPolicy')}</Link></Button>
    </div> : null}
  </div>
}
