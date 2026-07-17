import React from 'react'
import { ChevronsUpDown, UserCog, Trash2, Loader2 } from 'lucide-react'
import { TASK_STATE_LABELS, TASK_STATE_COLOR } from '@/api/imageTask'
import type { AnnotationTaskMeta } from '@/api/asset'
import { SortHeader } from '@/components/common/SortHeader'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'

// 任务看板使用的最小资产行结构。图片/语音/视频资产都可结构化适配（缩略图由 renderThumb 注入）。
export interface TaskAssetRow {
  id: number
  original_name: string
  created_at: string
  modality?: string
  task?: AnnotationTaskMeta
}

export type SortKey = 'created' | 'updated' | 'deadline'
export type SortState = { key: SortKey; dir: 'asc' | 'desc' } | null

const fmtTime = (s?: string | null) => (s ? new Date(s).toLocaleString('zh-CN') : '—')
const isOverdue = (s?: string | null) => !!s && new Date(s).getTime() < Date.now()
const SORT_LABEL: Record<SortKey, string> = { created: '创建时间', updated: '更新时间', deadline: '截止' }

// 通用任务看板表格：列出资产对应的标注任务（状态/指派/版本/时间/截止）+ 列头排序 + 指派入口。
// 与模态无关——缩略图由 renderThumb 注入（图片=缩略图，语音=波形，视频=帧）。未来语音/视频流水线复用。
export function TaskBoardTable({
  rows, canSelect, canAssign, selTasks, onToggleSel, userName, sort, onToggleSort, onOpen, onAssign, renderThumb,
  canDelete = false, onDelete, deletingId = null, allSelected = false, onToggleAll,
}: {
  rows: TaskAssetRow[]
  canSelect: boolean
  canAssign: boolean
  selTasks: Set<number>
  onToggleSel: (taskId: number) => void
  allSelected?: boolean
  onToggleAll?: () => void
  userName: (uid?: number | null) => string
  sort: SortState
  onToggleSort: (key: SortKey) => void
  onOpen: (taskId: number) => void
  onAssign: (taskIds: number[], cur?: { assignee_id?: number | null; reviewer_id?: number | null; deadline_at?: string | null }) => void
  renderThumb: (row: TaskAssetRow) => React.ReactNode
  canDelete?: boolean
  onDelete?: (row: TaskAssetRow) => void
  deletingId?: number | null
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
        <ChevronsUpDown className="h-3 w-3" />
        点「创建时间 / 更新时间 / 截止」<span style={{ color: 'var(--foreground)' }}>列头</span>可按时间排序，再点一次切换升 / 降序
        {sort && <span style={{ color: 'var(--primary)' }}>· 当前：{SORT_LABEL[sort.key]} {sort.dir === 'asc' ? '升序↑' : '降序↓'}</span>}
      </div>
      <div className="rounded-lg border overflow-hidden" style={{ borderColor: 'var(--border)' }}>
        <table className="w-full text-xs">
          <thead>
            <tr className="border-b text-left" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
              {canSelect && (
                <th className="w-9 px-2 py-2">
                  {onToggleAll && <input type="checkbox" checked={allSelected} onChange={onToggleAll} className="cursor-pointer" title="全选 / 取消全选" />}
                </th>
              )}
              <th className="px-2 py-2 font-medium">资产</th>
              <th className="px-2 py-2 font-medium">任务ID</th>
              <th className="px-2 py-2 font-medium">状态</th>
              <th className="px-2 py-2 font-medium">指派人</th>
              <th className="px-2 py-2 font-medium">审核人</th>
              <th className="px-2 py-2 font-medium text-center">版本</th>
              <SortHeader label="创建时间" active={sort?.key === 'created'} dir={sort?.dir ?? 'desc'} onClick={() => onToggleSort('created')} />
              <SortHeader label="更新时间" active={sort?.key === 'updated'} dir={sort?.dir ?? 'desc'} onClick={() => onToggleSort('updated')} />
              <SortHeader label="截止" active={sort?.key === 'deadline'} dir={sort?.dir ?? 'desc'} onClick={() => onToggleSort('deadline')} />
              <th className="px-2 py-2 font-medium">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y" style={{ borderColor: 'var(--border)' }}>
            {rows.map((a) => {
              const t = a.task
              const checked = !!t && selTasks.has(t.id)
              return (
                <tr key={a.id} className="group" style={{ borderColor: 'var(--border)', background: checked ? 'var(--accent)' : undefined }}>
                  {canSelect && (
                    <td className="px-2 py-2">{t && <input type="checkbox" checked={checked} onChange={() => onToggleSel(t.id)} />}</td>
                  )}
                  <td className="px-2 py-2">
                    <div className="flex items-center gap-2 min-w-0">
                      {renderThumb(a)}
                      <span className="truncate max-w-[140px]" title={a.original_name}>{a.original_name}</span>
                    </div>
                  </td>
                  <td className="px-2 py-2 font-mono">{t ? `#${t.id}` : '—'}</td>
                  <td className="px-2 py-2">
                    {t?.state ? <Badge variant="outline" className={`text-[10px] ${TASK_STATE_COLOR[t.state] ?? ''}`}>{TASK_STATE_LABELS[t.state] ?? t.state}</Badge> : '—'}
                  </td>
                  <td className="px-2 py-2">{userName(t?.assignee_id)}</td>
                  <td className="px-2 py-2">{userName(t?.reviewer_id)}</td>
                  <td className="px-2 py-2 text-center font-mono">{t?.version ?? '—'}</td>
                  <td className="px-2 py-2" style={{ color: 'var(--muted-foreground)' }}>{fmtTime(a.created_at)}</td>
                  <td className="px-2 py-2" style={{ color: 'var(--muted-foreground)' }}>{fmtTime(t?.updated_at)}</td>
                  <td className="px-2 py-2 font-mono" style={{ color: isOverdue(t?.deadline_at) ? '#ef4444' : 'var(--muted-foreground)' }}>
                    {t?.deadline_at ? (isOverdue(t.deadline_at) ? '逾期 · ' : '') + fmtTime(t.deadline_at) : '—'}
                  </td>
                  <td className="px-2 py-2">
                    <div className="flex items-center gap-1">
                      {t && <Button variant="ghost" size="sm" onClick={() => onOpen(t.id)}>打开</Button>}
                      {canAssign && t && (
                        <Button variant="ghost" size="sm" onClick={() => onAssign([t.id], t)} title="指派 / 改派">
                          <UserCog className="h-3.5 w-3.5" />
                        </Button>
                      )}
                      {canDelete && onDelete && (
                        <Button variant="ghost" size="sm" className="text-red-600 hover:text-red-700" title="删除样本（永久）"
                          disabled={deletingId === a.id} onClick={() => onDelete(a)}>
                          {deletingId === a.id ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                        </Button>
                      )}
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
