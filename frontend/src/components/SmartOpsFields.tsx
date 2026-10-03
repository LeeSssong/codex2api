import type { ReactNode } from 'react'
import { useId, useState } from 'react'
import { Input } from '@/components/ui/input'
import { useTranslation } from 'react-i18next'
import { DraftNumberInput } from '@/components/ui/draft-number-input'
import { Switch } from '@/components/ui/switch'
import AccountGroupMultiSelect from './AccountGroupMultiSelect'
import type { AccountGroup } from '../types'

export const SETTINGS_FIELD_GRID = 'grid grid-cols-1 gap-x-4 gap-y-4 sm:grid-cols-2'
export function SmartOpsModelList({ label, value, onChange, disabled }: { label: string; value: string[]; onChange: (v: string[]) => void; disabled?: boolean }) {
  const [draft, setDraft] = useState<string | null>(null)
  return <Input aria-label={label} value={draft ?? value.join(', ')} onChange={e => setDraft(e.target.value)} onBlur={() => { if (draft !== null) { onChange(draft.split(',').map(v => v.trim()).filter(Boolean)); setDraft(null) } }} disabled={disabled} />
}
export function SmartOpsSection({ title, children }: { title: string; children: ReactNode }) {
  return <section className="overflow-hidden rounded-xl border border-border/70 bg-card shadow-sm"><div className="flex items-center gap-3 border-b border-border/60 bg-muted/20 px-5 py-4"><h2 className="text-sm font-semibold tracking-tight text-foreground">{title}</h2><div className="h-px flex-1 bg-border/50" /></div><div className="space-y-5 p-5">{children}</div></section>
}
export function SmartOpsField({ label, children }: { label: string; children: ReactNode }) {
  return <div className="min-w-0 space-y-2"><div className="text-xs font-medium tracking-wide text-muted-foreground">{label}</div>{children}</div>
}
export function SmartOpsSwitch({ label, value, onChange, disabled }: { label: string; value: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  const id = useId()
  return <div className="flex min-w-0 items-center justify-between gap-4 rounded-lg border border-border/60 bg-muted/15 px-3.5 py-3"><label htmlFor={id} className="text-sm font-medium">{label}</label><Switch id={id} checked={value} onCheckedChange={onChange} disabled={disabled} /></div>
}
export function SmartOpsNumber({ label, value, onChange, min = 1, max = 10000, disabled, integer = true }: { label: string; value: number; onChange: (v: number) => void; min?: number; max?: number; disabled?: boolean; integer?: boolean }) {
  const id = useId()
  return <div className="space-y-2"><label htmlFor={id} className="text-sm font-medium">{label}</label><DraftNumberInput id={id} aria-label={label} min={min} max={max} value={value} onValueChange={onChange} integer={integer} disabled={disabled} /></div>
}
export function SmartOpsGroups({ groups, value, onChange, disabled }: { groups: AccountGroup[]; value: number[]; onChange: (v: number[]) => void; disabled?: boolean }) {
  const { t } = useTranslation()
  return <AccountGroupMultiSelect groups={groups} value={value} onChange={onChange} disabled={disabled} placeholder={t('accounts.groupsPlaceholder')} emptyLabel={t('accounts.groupsNone')} selectedLabel={t('accounts.groupsSelected', { count: value.length })} />
}
