import type { ReactNode } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'

interface StatCardProps {
  icon: ReactNode
  iconClass: string
  label: string
  value: number | string
  sub?: string
  className?: string
  wrapLabel?: boolean
}

const iconColors: Record<string, string> = {
  blue: 'bg-blue-500/10 text-blue-600 dark:text-blue-400 ring-blue-500/20',
  green: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 ring-emerald-500/20',
  amber: 'bg-amber-500/10 text-amber-600 dark:text-amber-400 ring-amber-500/20',
  red: 'bg-destructive/10 text-destructive ring-destructive/20',
  purple: 'bg-primary/10 text-primary ring-primary/20',
}

export default function StatCard({ icon, iconClass, label, value, sub, className, wrapLabel }: StatCardProps) {
  return (
    <Card
      className={cn(
        'group relative min-h-24 overflow-hidden rounded-lg py-0 border-border/70 bg-card shadow-2xs transition-colors duration-200 hover:border-border motion-reduce:transition-none',
        className,
      )}
    >
      <CardContent className="relative flex flex-col justify-between gap-1.5 p-3.5 sm:p-5">
        <div className="flex items-center justify-between gap-2">
          <div className="min-w-0">
            <span className={cn('block text-[11px] font-medium leading-relaxed text-muted-foreground', wrapLabel ? 'whitespace-normal break-words' : 'truncate')}>
              {label}
            </span>
            <div className="mt-1 text-[22px] font-semibold leading-none tabular-nums text-foreground sm:mt-2 sm:text-[28px]">
              {value}
            </div>
          </div>
          <div
            className={cn(
              'flex size-9 shrink-0 items-center justify-center rounded-lg ring-1 ring-inset sm:size-10',
              iconColors[iconClass] || iconColors.purple,
            )}
            aria-hidden="true"
          >
            <span className="[&_svg]:size-[18px] sm:[&_svg]:size-[20px]">{icon}</span>
          </div>
        </div>
        {sub ? (
          <div className="border-t border-border/60 pt-2 text-xs leading-relaxed tabular-nums text-muted-foreground">
            {sub}
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}
