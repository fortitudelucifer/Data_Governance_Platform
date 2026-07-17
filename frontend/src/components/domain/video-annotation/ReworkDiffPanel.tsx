import { useQuery } from '@tanstack/react-query'
import { GitCompare, Minus, Pencil, Plus } from 'lucide-react'

import { trackApi, type TrackChange } from '@/api/videoTask'

interface Props {
  taskId: number
  /** Jump to the frame the annotator touched, and select that track. */
  onJump: (frame: number | undefined, trackId: number) => void
}

// 关键帧改动摘要：+新增 / -删除 / ~移动（几何或 outside/occluded 变了）
function kfSummary(c: TrackChange): string {
  const { added, removed, moved } = c.keyframes
  const parts: string[] = []
  if (added.length) parts.push(`+${added.length} 关键帧`)
  if (removed.length) parts.push(`−${removed.length} 关键帧`)
  if (moved.length) parts.push(`~${moved.length} 帧改动`)
  if (c.fields.length) parts.push(c.fields.join('/') + ' 变更')
  return parts.join(' · ')
}

// ReworkDiffPanel：标注员返工重新提交后，审核员只需复核真正动过的地方，
// 而不是把 50 条 track 重看一遍（B3.1）。
export function ReworkDiffPanel({ taskId, onJump }: Props) {
  const { data } = useQuery({
    queryKey: ['track-diff', taskId],
    queryFn: () => trackApi.diff(taskId),
  })
  const diff = data?.diff
  if (!diff) return null // 只提交过一轮 → 无返工可对比
  const total = diff.added.length + diff.removed.length + diff.changed.length

  return (
    <div className="space-y-2 rounded-md border p-2.5" style={{ borderColor: 'var(--chart-4)', background: 'var(--muted)' }}>
      <div className="flex items-center justify-between text-[11px] font-medium" style={{ color: 'var(--muted-foreground)' }}>
        <span className="flex items-center gap-1.5">
          <GitCompare className="h-3.5 w-3.5" style={{ color: 'var(--chart-4)' }} />
          返工对比 · 第 {diff.from_round} 轮 → 第 {diff.to_round} 轮
        </span>
        <span>{total ? `${total} 处改动` : '无改动'}</span>
      </div>

      {total === 0 && (
        <p className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
          标注员重新提交了，但两轮内容完全一致。
        </p>
      )}

      {diff.added.map((id) => (
        <button key={`a${id}`} onClick={() => onJump(undefined, id)}
          className="flex w-full items-center gap-1.5 rounded-md border px-2 py-1.5 text-left text-[11px] hover:bg-[var(--accent)]"
          style={{ borderColor: 'var(--border)', background: 'var(--background)' }}>
          <Plus className="h-3 w-3" style={{ color: 'var(--chart-2)' }} />
          <span>新增 track <b>#{id}</b></span>
        </button>
      ))}

      {diff.removed.map((id) => (
        <div key={`r${id}`} className="flex items-center gap-1.5 rounded-md border px-2 py-1.5 text-[11px]"
          style={{ borderColor: 'var(--border)', background: 'var(--background)', opacity: 0.7 }}>
          <Minus className="h-3 w-3 text-red-500" />
          {/* 已被删除，跳过去也没东西可看 */}
          <span>删除 track <b>#{id}</b></span>
        </div>
      ))}

      {diff.changed.map((c) => (
        <button key={`c${c.track_id}`} onClick={() => onJump(c.first_frame, c.track_id)}
          className="flex w-full items-start gap-1.5 rounded-md border px-2 py-1.5 text-left text-[11px] hover:bg-[var(--accent)]"
          style={{ borderColor: 'var(--border)', background: 'var(--background)' }}>
          <Pencil className="mt-0.5 h-3 w-3 shrink-0" style={{ color: 'var(--chart-4)' }} />
          <span className="min-w-0">
            <b>#{c.track_id}</b> {c.label}
            <span className="ml-1" style={{ color: 'var(--muted-foreground)' }}>{kfSummary(c)}</span>
            {c.first_frame != null && (
              <span className="ml-1" style={{ color: 'var(--primary)' }}>→ 第 {c.first_frame} 帧</span>
            )}
          </span>
        </button>
      ))}
    </div>
  )
}
