import React from 'react'

export function PlaceholderPage({ title }: { title: string }) {
  return (
    <div className="flex flex-1 items-center justify-center">
      <div className="text-center">
        <p className="text-lg font-medium">{title}</p>
        <p className="text-sm mt-1" style={{ color: 'var(--muted-foreground)' }}>
          即将推出
        </p>
      </div>
    </div>
  )
}
