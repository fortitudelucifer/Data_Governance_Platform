import type { User } from '@/api/user'
import { TASK_STATE_LABELS } from '@/api/imageTask'

const STATE_FILTERS = ['HUMAN_PENDING', 'HUMAN_IN_PROGRESS', 'QA_PENDING', 'QA_REJECTED', 'FINALIZED', 'AI_PENDING', 'ROUTING']
const QC_FILTERS: { v: string; label: string }[] = [
  { v: 'passed', label: 'QC 通过' }, { v: 'failed', label: 'QC 失败' }, { v: 'pending', label: 'QC 待执行' },
]

function Sel({ value, onChange, placeholder, options }: {
  value: string; onChange: (v: string) => void; placeholder: string; options: { v: string; label: string }[]
}) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)}
      className="rounded-md border px-2 py-1 text-xs outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
      <option value="">{placeholder}</option>
      {options.map((o) => <option key={o.v} value={o.v}>{o.label}</option>)}
    </select>
  )
}

// 任务筛选条：状态 / QC / 指派人 / 审核人。状态、人员为客户端筛选；QC 走服务端。任务看板共用。
export function TaskFilterBar({
  stateFilter, onState, qcFilter, onQc, assigneeFilter, onAssignee, reviewerFilter, onReviewer, annotators, reviewers, canAssign, onClear, count,
}: {
  stateFilter: string; onState: (v: string) => void
  qcFilter: string; onQc: (v: string) => void
  assigneeFilter: string; onAssignee: (v: string) => void
  reviewerFilter: string; onReviewer: (v: string) => void
  annotators: User[]; reviewers: User[]; canAssign: boolean
  onClear: () => void; count: number
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 border-b px-6 py-2 text-xs" style={{ borderColor: 'var(--border)' }}>
      <Sel value={stateFilter} onChange={onState} placeholder="全部状态" options={STATE_FILTERS.map((s) => ({ v: s, label: TASK_STATE_LABELS[s] ?? s }))} />
      <Sel value={qcFilter} onChange={onQc} placeholder="全部 QC" options={QC_FILTERS} />
      {canAssign && (
        <>
          <Sel value={assigneeFilter} onChange={onAssignee} placeholder="全部指派人"
            options={[{ v: 'none', label: '未指派' }, ...annotators.map((u) => ({ v: String(u.id), label: u.display_name || u.username }))]} />
          <Sel value={reviewerFilter} onChange={onReviewer} placeholder="全部审核人"
            options={[{ v: 'none', label: '未指派审核' }, ...reviewers.map((u) => ({ v: String(u.id), label: u.display_name || u.username }))]} />
        </>
      )}
      {(stateFilter || qcFilter || assigneeFilter || reviewerFilter) && (
        <button onClick={onClear} className="underline hover:opacity-80" style={{ color: 'var(--muted-foreground)' }}>清除筛选</button>
      )}
      <span className="ml-auto" style={{ color: 'var(--muted-foreground)' }}>本页 {count} 项</span>
    </div>
  )
}
