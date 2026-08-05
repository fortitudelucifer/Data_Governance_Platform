import React from 'react'
import { cn } from '@/lib/utils'

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'default' | 'outline' | 'ghost' | 'destructive'
  size?: 'sm' | 'md' | 'icon'
}

export function Button({ className, variant = 'default', size = 'md', ...props }: ButtonProps) {
  return (
    <button
      className={cn(
        'inline-flex items-center justify-center gap-1.5 rounded-md font-medium transition-colors disabled:opacity-50 disabled:pointer-events-none cursor-pointer',
        variant === 'default' && 'bg-[var(--primary)] text-[var(--primary-foreground)] hover:opacity-90',
        variant === 'outline' && 'border border-[var(--border)] bg-transparent hover:bg-[var(--accent)] text-[var(--foreground)]',
        variant === 'ghost' && 'bg-transparent hover:bg-[var(--accent)] text-[var(--foreground)]',
        variant === 'destructive' && 'bg-red-600 text-white hover:bg-red-700',
        size === 'sm' && 'h-7 px-2.5 text-xs',
        size === 'md' && 'h-9 px-4 text-sm',
        size === 'icon' && 'h-8 w-8',
        className,
      )}
      {...props}
    />
  )
}
