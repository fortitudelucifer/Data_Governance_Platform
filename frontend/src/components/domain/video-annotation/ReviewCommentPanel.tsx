import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, MapPin, MessageSquarePlus, RotateCcw, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { reviewCommentApi, type ReviewComment } from '@/api/reviewComment'

interface Props {
  taskId: number
  currentFrame: number
  /** track_id of the currently selected track, if any — becomes the anchor. */
  selectedTrackId?: number
  canReview: boolean
  canAnnotate: boolean
  /** Jump the workbench to a comment's anchor. */
  onJump: (frame: number | undefined, trackId: number | undefined) => void
}

function anchorLabel(c: ReviewComment): string {
  if (c.frame == null) return '整体'
  return c.track_id != null ? `第 ${c.frame} 帧 · #${c.track_id}` : `第 ${c.frame} 帧`
}

// ReviewCommentPanel：审核员把问题钉在 帧+track 上，标注员点一行就跳到问题点。
// 面向「技能有限」的标注员：整行即跳转热区，未处理批注高对比标红，
// 「已修复」是显式按钮且有即时反馈。
export function ReviewCommentPanel({ taskId, currentFrame, selectedTrackId, canReview, canAnnotate, onJump }: Props) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState('')
  const [anchorHere, setAnchorHere] = useState(true)

  const { data: comments = [] } = useQuery({
    queryKey: ['review-comments', taskId],
    queryFn: () => reviewCommentApi.list(taskId),
  })
  const invalidate = () => qc.invalidateQueries({ queryKey: ['review-comments', taskId] })

  const addMut = useMutation({
    mutationFn: () =>
      reviewCommentApi.create(
        taskId,
        anchorHere ? { frame: currentFrame, track_id: selectedTrackId } : {},
        draft,
      ),
    onSuccess: () => { setDraft(''); invalidate() },
  })
  const resolveMut = useMutation({
    mutationFn: ({ id, resolved }: { id: string; resolved: boolean }) => reviewCommentApi.setResolved(taskId, id, resolved),
    onSuccess: invalidate,
  })
  const removeMut = useMutation({
    mutationFn: (id: string) => reviewCommentApi.remove(taskId, id),
    onSuccess: invalidate,
  })

  const open = comments.filter((c) => c.status === 'open')
  if (!comments.length && !canReview) return null

  return (
    <div className="space-y-2 rounded-md border p-2.5"
      style={{ borderColor: open.length ? '#ef4444' : 'var(--border)', background: 'var(--muted)' }}>
      <div className="flex items-center justify-between text-[11px] font-medium" style={{ color: 'var(--muted-foreground)' }}>
        <span className="flex items-center gap-1.5">
          <MapPin className="h-3.5 w-3.5" style={{ color: open.length ? '#ef4444' : 'var(--muted-foreground)' }} />
          审核批注
        </span>
        {open.length > 0 && (
          <span className="rounded-full px-1.5 py-0.5 text-[10px] font-semibold text-white" style={{ background: '#ef4444' }}>
            {open.length} 条待修复
          </span>
        )}
      </div>

      {open.length > 0 && canAnnotate && (
        <p className="text-[11px]" style={{ color: '#ef4444' }}>
          点击任一条即可跳到问题所在的帧与 track；全部标记「已修复」后才能重新提交。
        </p>
      )}

      {comments.map((c) => {
        const resolved = c.status === 'resolved'
        return (
          <div key={c.id}
            onClick={() => onJump(c.frame, c.track_id)}
            className="cursor-pointer rounded-md border p-2 text-xs transition-colors hover:bg-[var(--accent)]"
            style={{ borderColor: resolved ? 'var(--border)' : '#ef4444', background: 'var(--background)', opacity: resolved ? 0.6 : 1 }}>
            <div className="flex items-center justify-between gap-2">
              <span className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium"
                style={{ background: 'var(--muted)', color: 'var(--muted-foreground)' }}>
                <MapPin className="h-3 w-3" />{anchorLabel(c)}
              </span>
              <span className="truncate text-[10px]" style={{ color: 'var(--muted-foreground)' }}>{c.author_name || `用户 ${c.author_id}`}</span>
            </div>
            <p className="mt-1.5 break-words" style={{ textDecoration: resolved ? 'line-through' : 'none' }}>{c.body}</p>
            <div className="mt-1.5 flex justify-end gap-1.5" onClick={(e) => e.stopPropagation()}>
              {canAnnotate && !resolved && (
                <Button size="sm" className="h-6 text-[11px]" style={{ background: 'var(--chart-2)' }}
                  disabled={resolveMut.isPending}
                  onClick={() => resolveMut.mutate({ id: c.id, resolved: true })}>
                  <Check className="mr-1 h-3 w-3" />已修复
                </Button>
              )}
              {canReview && resolved && (
                <Button size="sm" variant="outline" className="h-6 text-[11px]" disabled={resolveMut.isPending}
                  onClick={() => resolveMut.mutate({ id: c.id, resolved: false })}>
                  <RotateCcw className="mr-1 h-3 w-3" />重开
                </Button>
              )}
              {canReview && (
                <Button size="sm" variant="outline" className="h-6 border-red-200 text-[11px] text-red-600"
                  disabled={removeMut.isPending}
                  onClick={() => { if (confirm('删除这条批注？')) removeMut.mutate(c.id) }}>
                  <Trash2 className="h-3 w-3" />
                </Button>
              )}
            </div>
          </div>
        )
      })}

      {canReview && (
        <div className="space-y-1.5 border-t pt-2" style={{ borderColor: 'var(--border)' }}>
          <textarea value={draft} onChange={(e) => setDraft(e.target.value)} rows={2}
            placeholder="写一条批注，钉在当前帧…"
            className="w-full resize-none rounded-md border p-2 text-xs outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
          <label className="flex items-center gap-1.5 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
            <input type="checkbox" checked={anchorHere} onChange={(e) => setAnchorHere(e.target.checked)} />
            钉在第 {currentFrame} 帧{selectedTrackId != null ? ` · #${selectedTrackId}` : '（未选中 track）'}
          </label>
          <Button size="sm" className="h-7 w-full text-xs" disabled={!draft.trim() || addMut.isPending}
            onClick={() => addMut.mutate()}>
            <MessageSquarePlus className="mr-1 h-3.5 w-3.5" />
            {addMut.isPending ? '提交中…' : '添加批注'}
          </Button>
        </div>
      )}
    </div>
  )
}
