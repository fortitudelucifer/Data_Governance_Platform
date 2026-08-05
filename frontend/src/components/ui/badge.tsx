import React from 'react'
import { cn } from '@/lib/utils'

interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement> {
  variant?: 'default' | 'secondary' | 'outline' | 'destructive'
}

export function Badge({ className, variant = 'default', ...props }: BadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium border',
        variant === 'default' && 'bg-[var(--primary)] text-[var(--primary-foreground)] border-transparent',
        variant === 'secondary' && 'bg-[var(--secondary)] text-[var(--secondary-foreground)] border-transparent',
        variant === 'outline' && 'border-[var(--border)] text-[var(--foreground)] bg-transparent',
        variant === 'destructive' && 'bg-red-100 text-red-700 border-red-200',
        className,
      )}
      {...props}
    />
  )
}
