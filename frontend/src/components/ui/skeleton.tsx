import React from 'react'
import { cn } from '@/lib/utils'

export function Skeleton({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('animate-pulse rounded-md', className)}
      style={{ background: 'var(--muted)' }}
      {...props}
    />
  )
}

/** 表格行骨架：用于表格类列表页 */
export function TableSkeleton({ rows = 6, cols = 4 }: { rows?: number; cols?: number }) {
  return (
    <div className="rounded-lg border overflow-hidden" style={{ borderColor: 'var(--border)' }}>
      {Array.from({ length: rows }).map((_, r) => (
        <div key={r} className="flex items-center gap-4 border-b px-4 py-3" style={{ borderColor: 'var(--border)' }}>
          {Array.from({ length: cols }).map((_, c) => (
            <Skeleton key={c} className="h-4" style={{ width: c === 0 ? '30%' : `${15 + (c % 3) * 8}%` }} />
          ))}
        </div>
      ))}
    </div>
  )
}

/** 网格骨架：用于图片资产等卡片网格 */
export function GridSkeleton({ count = 12 }: { count?: number }) {
  return (
    <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6 gap-3">
      {Array.from({ length: count }).map((_, i) => (
        <div key={i} className="rounded-lg border overflow-hidden" style={{ borderColor: 'var(--border)' }}>
          <Skeleton className="aspect-square w-full rounded-none" />
          <div className="p-2 space-y-1.5">
            <Skeleton className="h-3 w-3/4" />
            <Skeleton className="h-3 w-1/2" />
          </div>
        </div>
      ))}
    </div>
  )
}

/** 列表骨架：用于任务/卡片纵向列表（avatar 时左侧带缩略图占位） */
export function ListSkeleton({ rows = 6, avatar = false }: { rows?: number; avatar?: boolean }) {
  return (
    <div className="grid gap-2.5">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex items-center gap-4 rounded-lg border p-3" style={{ borderColor: 'var(--border)' }}>
          {avatar && <Skeleton className="h-14 w-14 shrink-0 rounded-md" />}
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-1/3" />
            <Skeleton className="h-3 w-1/2" />
          </div>
        </div>
      ))}
    </div>
  )
}
