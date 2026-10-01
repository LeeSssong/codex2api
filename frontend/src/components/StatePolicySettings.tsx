import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api'
import { Switch } from './ui/switch'
import { Button } from './ui/button'
import { getErrorMessage } from '../utils/error'
import { subscribeStateChange } from '../lib/stateSync'

export default function StatePolicySettings() {
  const { t } = useTranslation()
  const [value, setValue] = useState<boolean>()
  const [enabled, setEnabled] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [refreshMinutes, setRefreshMinutes] = useState(10)
  useEffect(() => {
    const controller = new AbortController()
    const refresh = () => { void api.getIPv6State(controller.signal).then(data => { if (!controller.signal.aborted) { setValue(data.config.require_valid_state); setEnabled(data.config.enabled); setRefreshMinutes(data.config.refresh_before_minutes) } }).catch(err => { if (!controller.signal.aborted) setError(getErrorMessage(err)) }) }
    refresh()
    const unsubscribe = subscribeStateChange(refresh)
    return () => { controller.abort(); unsubscribe() }
  }, [])
  const save = async (checked: boolean) => {
    setBusy(true); setError('')
    try { const data = await api.configureStatePolicy(checked); setValue(data.config.require_valid_state); setEnabled(data.config.enabled) }
    catch (err) { setError(getErrorMessage(err)) }
    finally { setBusy(false) }
  }
  return <div className="space-y-4">
    <div className="flex items-start justify-between gap-6"><div className="space-y-1"><label className="text-sm font-medium" htmlFor="require-valid-state">{t('ipv6State.requireValid')}</label><p id="require-valid-state-help" className="text-xs leading-relaxed text-muted-foreground">{t('ipv6State.requireValidHelp')}</p></div><Switch id="require-valid-state" aria-describedby="require-valid-state-help" disabled={value === undefined || busy} checked={value ?? false} onCheckedChange={checked => void save(checked)} /></div>
    <p className="text-xs leading-relaxed text-muted-foreground">{t('ipv6State.renewalHelp', { minutes: refreshMinutes })}</p>
    {value && !enabled ? <p role="status" className="text-xs text-amber-700 dark:text-amber-300">{t('ipv6State.strictDisabled')}</p> : null}
    {error ? <p role="alert" className="text-sm text-destructive">{error}</p> : null}
    <Button asChild variant="outline" size="sm"><a href="/admin/state-pool?settings=1">{t('ipv6State.openManagement')}</a></Button>
  </div>
}
