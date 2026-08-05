import React from 'react'

interface PageHeaderProps {
  title: string
  description?: string
  leading?: React.ReactNode
  reserveLeading?: boolean
  actions?: React.ReactNode
}

export function PageHeader({ title, description, leading, reserveLeading = true, actions }: PageHeaderProps) {
  const showLeadingSlot = reserveLeading || leading

  return (
    <div className="flex items-center justify-between border-b px-6 py-4" style={{ borderColor: 'var(--border)' }}>
      <div className="flex min-w-0 items-center gap-3">
        {showLeadingSlot && <div className="flex w-[88px] shrink-0 items-center">{leading}</div>}
        <div className="min-w-0">
          <h1 className="truncate text-lg font-semibold">{title}</h1>
          {description && (
            <p className="mt-0.5 truncate text-sm" style={{ color: 'var(--muted-foreground)' }}>{description}</p>
          )}
        </div>
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
