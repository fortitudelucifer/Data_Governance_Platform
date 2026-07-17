import React from 'react'
import { Badge } from '@/components/ui/badge'

const STAGE_MAP: Record<string, { label: string; className: string }> = {
  not_annotated:   { label: '未标注',   className: 'border-[var(--border)] text-[var(--muted-foreground)]' },
  auto_annotating: { label: '标注中',   className: 'bg-blue-50 text-blue-700 border-blue-200' },
  auto_annotated:  { label: '已自动标注', className: 'bg-amber-50 text-amber-700 border-amber-200' },
  auto_failed:     { label: '标注失败', className: 'bg-red-50 text-red-700 border-red-200' },
  refining:        { label: '精标中',   className: 'bg-purple-50 text-purple-700 border-purple-200' },
  refined:         { label: '已精标',   className: 'bg-emerald-50 text-emerald-700 border-emerald-200' },
  reviewed:        { label: '已审核',   className: 'bg-emerald-50 text-emerald-700 border-emerald-200' },
}

export function StageTag({ stage }: { stage: string }) {
  const cfg = STAGE_MAP[stage] ?? { label: stage, className: '' }
  return (
    <Badge variant="outline" className={`shrink-0 whitespace-nowrap ${cfg.className}`}>
      {cfg.label}
    </Badge>
  )
}
