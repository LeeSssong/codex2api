import * as React from 'react'
import { Checkbox as CheckboxPrimitive } from 'radix-ui'
import { Check, Minus } from 'lucide-react'
import { cn } from '@/lib/utils'

export function Checkbox({ className, ...props }: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return <CheckboxPrimitive.Root {...props} className={cn('inline-flex size-4 shrink-0 items-center justify-center rounded border border-input bg-background text-primary-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-40 data-[state=checked]:border-primary data-[state=checked]:bg-primary data-[state=indeterminate]:bg-primary', className)}>
    <CheckboxPrimitive.Indicator>{props.checked === 'indeterminate' ? <Minus className="size-3" /> : <Check className="size-3" />}</CheckboxPrimitive.Indicator>
  </CheckboxPrimitive.Root>
}
