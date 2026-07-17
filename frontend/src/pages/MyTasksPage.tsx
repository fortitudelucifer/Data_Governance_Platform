import React, { useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Briefcase, ChevronRight } from 'lucide-react'
import { taskApi, TASK_STATE_LABELS, TASK_STATE_COLOR } from '@/api/imageTask'
import { datasetApi } from '@/api/dataset'
import { AssetImage } from '@/components/domain/image-annotation/AssetImage'
import { PageHeader } from '@/components/common/PageHeader'
import { Pagination } from '@/components/common/Pagination'
import { ListSkeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { useAuthStore } from '@/stores/auth'
import { taskRouteFor } from '@/lib/taskRoute'
import { fromState } from '@/lib/navBack'

const FILTERS: { id: string; label: string; state?: string }[] = [
  { id: 'all', label: '全部' },
  { id: 'pending', label: '待标注', state: 'HUMAN_PENDING' },
  { id: 'progress', label: '标注中', state: 'HUMAN_IN_PROGRESS' },
  { id: 'qa', label: '待审核', state: 'QA_PENDING' },
  { id: 'done', label: '已完成', state: 'FINALIZED' },
]
const fmtTime = (s?: string | null) => (s ? new Date(s).toLocaleString('zh-CN') : '—')
const isOverdue = (s?: string | null) => !!s && new Date(s).getTime() < Date.now()

export function MyTasksPage() {
  const navigate = useNavigate()
  const location = useLocation() // 带给工作台，供顶栏「返回」回到这里
  const user = useAuthStore((s) => s.user)
  const [filter, setFilter] = useState('all')
  const [page, setPage] = useState(1)
  const pageSize = 20

  const activeFilter = FILTERS.find((f) => f.id === filter)
  const { data, isLoading } = useQuery({
    queryKey: ['my-tasks', filter, page],
    queryFn: () => taskApi.list({ mine: true, page, page_size: pageSize, ...(activeFilter?.state ? { state: activeFilter.state } : {}) }),
  })

  const { data: dsData } = useQuery({ queryKey: ['datasets-name'], queryFn: () => datasetApi.list(), staleTime: 5 * 60 * 1000 })
  const dsName = (id: number) => dsData?.items.find((d) => d.id === id)?.name ?? `数据集 #${id}`
  const dsModality = (id: number) => dsData?.items.find((d) => d.id === id)?.modality

  const tasks = data?.items ?? []
  const total = data?.total ?? 0
  const totalPages = Math.ceil(total / pageSize)
  // 默认按截止排序：逾期/最近优先，空截止排末尾
  const sorted = [...tasks].sort((a, b) => {
    const da = a.deadline_at ? new Date(a.deadline_at).getTime() : null
    const db = b.deadline_at ? new Date(b.deadline_at).getTime() : null
    if (da == null && db == null) return 0
    if (da == null) return 1
    if (db == null) return -1
    return da - db
  })
  const myRole = (t: { assignee_id?: number | null; reviewer_id?: number | null }) =>
    t.reviewer_id === user?.id ? '审核员' : t.assignee_id === user?.id ? '标注员' : ''

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      <PageHeader title="我的任务" description={`共 ${total.toLocaleString()} 个任务`} />

      {/* 状态筛选 */}
      <div className="flex items-center gap-1.5 px-6 py-3 border-b" style={{ borderColor: 'var(--border)' }}>
        {FILTERS.map((f) => (
          <button key={f.id} onClick={() => { setFilter(f.id); setPage(1) }}
            className="rounded-full border px-3 py-1 text-xs transition-colors"
            style={{
              borderColor: filter === f.id ? 'var(--primary)' : 'var(--border)',
              background: filter === f.id ? 'var(--primary)' : 'transparent',
              color: filter === f.id ? 'var(--primary-foreground)' : 'var(--muted-foreground)',
            }}>
            {f.label}
          </button>
        ))}
      </div>

      <div className="flex-1 overflow-auto px-6 py-4">
        {isLoading ? (
          <ListSkeleton rows={6} avatar />
        ) : tasks.length === 0 ? (
          <div className="flex h-40 flex-col items-center justify-center gap-2">
            <Briefcase className="h-8 w-8" style={{ color: 'var(--muted-foreground)' }} />
            <p className="text-sm" style={{ color: 'var(--muted-foreground)' }}>暂无任务</p>
          </div>
        ) : (
          <div className="grid gap-2.5">
            {sorted.map((t) => {
              const role = myRole(t)
              const overdue = isOverdue(t.deadline_at)
              return (
                <div key={t.id}
                  className="group flex items-center gap-4 rounded-lg border p-3 cursor-pointer hover:border-[var(--primary)]/50 transition-colors"
                  style={{ borderColor: 'var(--border)', background: 'var(--card)' }}
                  onClick={() => navigate(taskRouteFor(dsModality(t.dataset_id), t.id), fromState(location))}>
                  <AssetImage assetId={t.asset_id} modality={dsModality(t.dataset_id)} className="h-14 w-14 shrink-0 rounded-md object-cover" />
                  <div className="flex-1 min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>任务 #{t.id}</span>
                      <Badge variant="outline" className={`text-[10px] ${TASK_STATE_COLOR[t.state] ?? ''}`}>
                        {TASK_STATE_LABELS[t.state] ?? t.state}
                      </Badge>
                      {role && <Badge variant="secondary" className="text-[10px]">{role}</Badge>}
                    </div>
                    <p className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                      <span className="truncate max-w-[160px]">{dsName(t.dataset_id)}</span>
                      <span>v{t.version}</span>
                      <span>更新 {fmtTime(t.updated_at)}</span>
                      {t.deadline_at && (
                        <span style={{ color: overdue ? '#ef4444' : undefined }}>{overdue ? '逾期 · ' : ''}截止 {fmtTime(t.deadline_at)}</span>
                      )}
                    </p>
                  </div>
                  <ChevronRight className="h-4 w-4 opacity-0 group-hover:opacity-100 transition-opacity" style={{ color: 'var(--muted-foreground)' }} />
                </div>
              )
            })}
          </div>
        )}
      </div>

      <Pagination page={page} totalPages={totalPages} total={total} onPage={setPage} />
    </div>
  )
}
