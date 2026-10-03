import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { KeyRound, Play, RefreshCw, Save, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { credentialRequest, type CredentialConfig, type CredentialJob, type CredentialOverview } from '@/lib/credentialOps'

export default function TwoFAImport({ accountId = 0, onSaved, sessionStudioConfigured }: { accountId?: number; onSaved?: () => void; sessionStudioConfigured?: boolean }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [totp, setTotp] = useState('')
  const [otpURL, setOTPURL] = useState('')
  const [mode, setMode] = useState('password_totp')
  const [engine, setEngine] = useState('local_worker')
  const [proxySource, setProxySource] = useState('account')
  const [studioAvailable, setStudioAvailable] = useState(sessionStudioConfigured ?? false)
  const [stored, setStored] = useState<CredentialConfig | null>(null)
  const [clearPassword, setClearPassword] = useState(false)
  const [clearTOTP, setClearTOTP] = useState(false)
  const [clearOTPURL, setClearOTPURL] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [job, setJob] = useState<CredentialJob | null>(null)
  useEffect(() => {
    let live = true
    setName(''); setEmail(''); setPassword(''); setTotp(''); setOTPURL(''); setStored(null); setJob(null); setMessage(''); setClearPassword(false); setClearTOTP(false); setClearOTPURL(false)
    setMode('password_totp'); setEngine('local_worker'); setProxySource('account')
    if (sessionStudioConfigured === undefined) void credentialRequest<CredentialOverview>('/credential-ops').then(data => { if (live) setStudioAvailable(data.session_studio_configured) }).catch(() => {})
    if (accountId > 0) void credentialRequest<CredentialConfig>(`/accounts/${accountId}/credential-ops/config`).then(config => {
      if (!live) return
      setStored(config); setEmail(config.email); setMode(config.mode); setEngine(config.engine); setProxySource(config.proxy_source)
    }).catch(() => {})
    return () => { live = false }
  }, [accountId])
  useEffect(() => {
    if (sessionStudioConfigured !== undefined) setStudioAvailable(sessionStudioConfigured)
  }, [sessionStudioConfigured])
  useEffect(() => {
    if (!job || !['queued', 'running'].includes(job.Status)) return
    const timer = setInterval(() => { void credentialRequest<CredentialJob>(`/credential-ops/login/${job.ID}`).then(next => {
      setJob(next)
      if (next.Status === 'succeeded') { setMessage(t('credentialOps.loginSucceeded')); onSaved?.() }
      if (next.Status === 'failed') setMessage(next.Error || t('credentialOps.failed'))
    }).catch(error => setMessage(String(error))) }, 2000)
    return () => clearInterval(timer)
  }, [job, onSaved])
  async function submit(login: boolean) {
    setBusy(true); setMessage('')
    const body = { name, email, password, totp_secret: totp, otp_url: otpURL, mode, engine, proxy_source: proxySource, clear_password: clearPassword, clear_totp: clearTOTP, clear_otp_url: clearOTPURL }
    try {
      const path = accountId === 0 ? '/credential-ops/import' : `/accounts/${accountId}/credential-ops/${login ? 'login' : 'config'}`
      if (login || accountId === 0) { const created = await credentialRequest<CredentialJob>(path, 'POST', body); setJob(created); setMessage(t('credentialOps.queuedMessage', { id: created.ID })) }
      else { setStored(await credentialRequest<CredentialConfig>(path, 'PUT', body)); setMessage(t('credentialOps.saved')); onSaved?.() }
      setPassword(''); setTotp(''); setOTPURL(''); setClearPassword(false); setClearTOTP(false); setClearOTPURL(false)
    } catch (error) { setMessage(error instanceof Error ? error.message : t('credentialOps.failed')) } finally { setBusy(false) }
  }
  async function cancel() { if (!job) return; try { await credentialRequest(`/credential-ops/login/${job.ID}`, 'DELETE'); setJob({ ...job, Status: 'cancelled', Stage: 'cancelled' }) } catch (error) { setMessage(String(error)) } }
  return <section className="space-y-4">
    <h2 className="flex items-center gap-2 text-base font-semibold"><KeyRound className="size-4" />{accountId ? t('credentialOps.accountConfig', { id: accountId }) : t('credentialOps.firstLogin')}</h2>
    <div className="grid gap-4 sm:grid-cols-2">
      {accountId === 0 && <label className="space-y-1 text-sm sm:col-span-2">{t('credentialOps.name')}<Input value={name} onChange={event => setName(event.target.value)} maxLength={200} autoComplete="off" /></label>}
      <label className="space-y-1 text-sm">{t('credentialOps.email')}<Input value={email} onChange={event => setEmail(event.target.value)} type="email" autoComplete="off" /></label>
      <div className="space-y-1 text-sm"><label htmlFor={`credential-mode-${accountId}`}>{t('credentialOps.mode')}</label><Select id={`credential-mode-${accountId}`} value={mode} onValueChange={value => { setMode(value); if (value === 'email_otp_url') setEngine('local_worker') }} options={[{ value: 'password_totp', label: t('credentialOps.passwordMode') }, { value: 'email_otp_url', label: t('credentialOps.emailMode') }]} /></div>
      <div className="space-y-1 text-sm"><label htmlFor={`credential-engine-${accountId}`}>{t('credentialOps.engine')}</label><Select id={`credential-engine-${accountId}`} value={engine} onValueChange={value => { setEngine(value); if (value === 'session_studio') setProxySource('direct') }} options={[{ value: 'local_worker', label: t('credentialOps.localWorker') }, ...(studioAvailable && mode === 'password_totp' ? [{ value: 'session_studio', label: t('credentialOps.sessionStudio') }] : [])]} /></div>
      <div className="space-y-1 text-sm"><label htmlFor={`credential-proxy-${accountId}`}>{t('credentialOps.proxy')}</label><Select id={`credential-proxy-${accountId}`} value={proxySource} onValueChange={setProxySource} disabled={engine === 'session_studio'} options={[{ value: 'account', label: t('credentialOps.accountProxy') }, { value: 'global', label: t('credentialOps.globalProxy') }, { value: 'direct', label: t('credentialOps.direct') }]} /></div>
      {mode === 'password_totp' ? <>
        <label className="space-y-1 text-sm">{t('credentialOps.password')}<Input value={password} onChange={event => setPassword(event.target.value)} placeholder={stored?.password_configured ? t('credentialOps.configured') : ''} type="password" autoComplete="new-password" /></label>
        <label className="space-y-1 text-sm">{t('credentialOps.totp')}<Input value={totp} onChange={event => setTotp(event.target.value)} placeholder={stored?.totp_configured ? t('credentialOps.configured') : ''} type="password" autoComplete="off" /></label>
      </> : <label className="space-y-1 text-sm sm:col-span-2">{t('credentialOps.otpURL')}<Input value={otpURL} onChange={event => setOTPURL(event.target.value)} placeholder={stored?.otp_url_configured ? t('credentialOps.configured') : 'https://'} type="password" autoComplete="off" /></label>}
    </div>
    {accountId > 0 && mode === 'password_totp' && <div className="flex flex-wrap gap-4 text-sm"><label className="flex items-center gap-2"><Switch checked={clearPassword} onCheckedChange={setClearPassword} />{t('credentialOps.clearPassword')}</label><label className="flex items-center gap-2"><Switch checked={clearTOTP} onCheckedChange={setClearTOTP} />{t('credentialOps.clearTOTP')}</label></div>}
    {accountId > 0 && mode === 'email_otp_url' && <label className="flex items-center gap-2 text-sm"><Switch checked={clearOTPURL} onCheckedChange={setClearOTPURL} />{t('credentialOps.clearOTPURL')}</label>}
    {job && <p role="status" className="rounded-lg border border-border bg-muted/30 p-3 text-sm">{t('credentialOps.jobLabel', { id: job.ID })}: {t(`credentialOps.status.${job.Status}`, { defaultValue: job.Status })} / {t(`credentialOps.stage.${job.Stage}`, { defaultValue: job.Stage })}{job.AccountID > 0 && ` · ${t('credentialOps.accountLabel', { id: job.AccountID })}`}</p>}
    {message && <p role="status" className="text-sm text-muted-foreground">{message}</p>}
    <div className="flex flex-wrap gap-2">{accountId > 0 && <Button variant="outline" onClick={() => void submit(false)} disabled={busy}><Save className="size-4" />{t('credentialOps.saveConfig')}</Button>}<Button onClick={() => void submit(true)} disabled={busy || !email || !!job && ['queued', 'running'].includes(job.Status)}>{busy ? <RefreshCw className="size-4 animate-spin" /> : <Play className="size-4" />}{t(busy ? 'credentialOps.submitting' : accountId ? 'credentialOps.relogin' : 'credentialOps.loginImport')}</Button>{job && ['queued', 'running'].includes(job.Status) && <Button variant="outline" onClick={() => void cancel()}><X className="size-4" />{t('credentialOps.cancelJob')}</Button>}</div>
  </section>
}
