import { useState } from 'react'
import { CalendarClock, Loader2 } from 'lucide-react'
import type { User } from '@/api/user'
import type { TaskAssignPayload } from '@/api/imageTask'
import { Button } from '@/components/ui/button'

type FieldChoice = '' | '0' | string
type DeadlineMode = 'keep' | 'clear' | 'set'

const fmtTime = (s?: string | null) => (s ? new Date(s).toLocaleString('zh-CN') : '—')

// 任务管理弹窗。与模态无关，图片/音频/视频任务看板共用。
// 后端约定：uint 字段 0 = 清空；deadline_at "" = 清空；字段缺省 = 不修改。
export function AssignModal({
  taskIds, curAssignee, curReviewer, curDeadline, annotators, reviewers, userName, pending, onClose, onSubmit, itemLabel = '任务',
}: {
  taskIds: number[]
  curAssignee?: number | null
  curReviewer?: number | null
  curDeadline?: string | null
  annotators: User[]
  reviewers: User[]
  userName: (uid?: number | null) => string
  pending: boolean
  onClose: () => void
  onSubmit: (payload: TaskAssignPayload) => void
  itemLabel?: string
}) {
  const [assignee, setAssignee] = useState<FieldChoice>('')
  const [reviewer, setReviewer] = useState<FieldChoice>('')
  const [deadlineMode, setDeadlineMode] = useState<DeadlineMode>('keep')
  const [deadlineInput, setDeadlineInput] = useState('')

  const buildPayload = (): TaskAssignPayload => {
    const payload: TaskAssignPayload = {}
    if (assignee !== '') payload.assignee_id = Number(assignee)
    if (reviewer !== '') payload.reviewer_id = Number(reviewer)
    if (deadlineMode === 'clear') payload.deadline_at = ''
    if (deadlineMode === 'set' && deadlineInput) payload.deadline_at = new Date(deadlineInput).toISOString()
    return payload
  }
  const hasRoleChange = assignee !== '' || reviewer !== ''
  const hasDeadlineChange = deadlineMode === 'clear' || (deadlineMode === 'set' && !!deadlineInput)
  const canSubmit = hasRoleChange || hasDeadlineChange

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" onClick={onClose}>
      <div className="w-[380px] rounded-lg border p-4 shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }} onClick={(e) => e.stopPropagation()}>
        <p className="text-sm font-medium mb-1">任务管理（{taskIds.length} 个{itemLabel}）</p>
        {taskIds.length === 1 && (
          <div className="mb-3 space-y-0.5 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
            <p>当前：标注 {userName(curAssignee)} · 审核 {userName(curReviewer)}</p>
            <p className="flex items-center gap-1"><CalendarClock className="h-3 w-3" />截止 {fmtTime(curDeadline)}</p>
          </div>
        )}
        <label className="block text-xs mb-1" style={{ color: 'var(--muted-foreground)' }}>标注员</label>
        <select value={assignee} onChange={(e) => setAssignee(e.target.value)}
          className="mb-3 w-full rounded-md border px-2 py-1.5 text-sm outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
          <option value="">— 不修改 —</option>
          <option value="0">清空标注员</option>
          {annotators.map((u) => <option key={u.id} value={u.id}>{u.display_name || u.username}</option>)}
        </select>
        <label className="block text-xs mb-1" style={{ color: 'var(--muted-foreground)' }}>审核员</label>
        <select value={reviewer} onChange={(e) => setReviewer(e.target.value)}
          className="mb-3 w-full rounded-md border px-2 py-1.5 text-sm outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
          <option value="">— 不修改 —</option>
          <option value="0">清空审核员</option>
          {reviewers.map((u) => <option key={u.id} value={u.id}>{u.display_name || u.username}</option>)}
        </select>
        <label className="block text-xs mb-1" style={{ color: 'var(--muted-foreground)' }}>截止时间</label>
        <div className="mb-3 grid grid-cols-[112px_1fr] gap-2">
          <select value={deadlineMode} onChange={(e) => setDeadlineMode(e.target.value as DeadlineMode)}
            className="rounded-md border px-2 py-1.5 text-sm outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
            <option value="keep">不修改</option>
            <option value="set">设置</option>
            <option value="clear">清除</option>
          </select>
          <input type="datetime-local" value={deadlineInput} disabled={deadlineMode !== 'set'}
            onChange={(e) => setDeadlineInput(e.target.value)}
            className="rounded-md border px-2 py-1.5 text-sm outline-none disabled:opacity-50" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
        </div>
        <p className="mb-4 text-[10px]" style={{ color: 'var(--muted-foreground)' }}>留「不修改」保持原值；选择“清空/清除”会移除对应字段。</p>
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>取消</Button>
          <Button size="sm" disabled={pending || !canSubmit} onClick={() => onSubmit(buildPayload())}>
            {pending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}确定
          </Button>
        </div>
      </div>
    </div>
  )
}
