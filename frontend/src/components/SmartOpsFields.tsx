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
  return <section className="space-y-4 border-t border-border pt-6"><h2 className="text-base font-semibold">{title}</h2>{children}</section>
}
export function SmartOpsField({ label, children }: { label: string; children: ReactNode }) {
  return <div className="min-w-0 space-y-2"><div className="text-sm font-medium">{label}</div>{children}</div>
}
export function SmartOpsSwitch({ label, value, onChange, disabled }: { label: string; value: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  const id = useId()
  return <div className="flex min-w-0 items-center justify-between gap-4 border-b border-border/50 py-3"><label htmlFor={id} className="text-sm">{label}</label><Switch id={id} checked={value} onCheckedChange={onChange} disabled={disabled} /></div>
}
export function SmartOpsNumber({ label, value, onChange, min = 1, max = 10000, disabled, integer = true }: { label: string; value: number; onChange: (v: number) => void; min?: number; max?: number; disabled?: boolean; integer?: boolean }) {
  const id = useId()
  return <div className="space-y-2"><label htmlFor={id} className="text-sm font-medium">{label}</label><DraftNumberInput id={id} aria-label={label} min={min} max={max} value={value} onValueChange={onChange} integer={integer} disabled={disabled} /></div>
}
export function SmartOpsGroups({ groups, value, onChange, disabled }: { groups: AccountGroup[]; value: number[]; onChange: (v: number[]) => void; disabled?: boolean }) {
  const { t } = useTranslation()
  return <AccountGroupMultiSelect groups={groups} value={value} onChange={onChange} disabled={disabled} placeholder={t('accounts.groupsPlaceholder')} emptyLabel={t('accounts.groupsNone')} selectedLabel={t('accounts.groupsSelected', { count: value.length })} />
}
