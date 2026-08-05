import React, { useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Upload, ArrowLeft, ImageIcon, Loader2, Download, X, LayoutGrid, Table2, UserCog, Sparkles, Trash2 } from 'lucide-react'
import { assetApi } from '@/api/asset'
import { datasetApi } from '@/api/dataset'
import { userApi } from '@/api/user'
import { taskApi, capabilityApi, TASK_STATE_LABELS, TASK_STATE_COLOR, type TaskAssignPayload } from '@/api/imageTask'
import { batchAnnotateApi, MODALITY_CAPS, CAP_LABELS } from '@/api/batchAnnotate'
import {
  exportImageDataset, IMAGE_EXPORT_FORMATS, type ImageExportFormat,
  exportAudioDataset, AUDIO_EXPORT_FORMATS, type AudioExportFormat,
  exportVideoDataset, VIDEO_EXPORT_FORMATS, type VideoExportFormat,
  exportGenericDataset, GENERIC_EXPORT_FORMATS, type GenericExportFormat,
} from '@/api/export'
import { AssetImage } from '@/components/domain/image-annotation/AssetImage'
import { PageHeader } from '@/components/common/PageHeader'
import { Pagination } from '@/components/common/Pagination'
import { TaskBoardTable, type SortState, type SortKey } from '@/components/domain/task-board/TaskBoardTable'
import { TaskFilterBar } from '@/components/domain/task-board/TaskFilterBar'
import { AssignModal } from '@/components/domain/task-board/AssignModal'
import { GridSkeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useAuthStore } from '@/stores/auth'
import { taskRouteFor } from '@/lib/taskRoute'
import { backToOr, fromState, urlInt, useUrlParams } from '@/lib/navBack'
import { uploadFileSmart } from '@/api/upload'
import { canAnnotate as roleCanAnnotate, canAssign as roleCanAssign, canExport as roleCanExport } from '@/lib/roles'

export function AssetListPage() {
  const { id } = useParams<{ id: string }>()
  const datasetId = Number(id)
  const navigate = useNavigate()
  const location = useLocation() // 带给工作台，供顶栏「返回」回到这里
  const qc = useQueryClient()
  const user = useAuthStore((s) => s.user)
  const canUpload = roleCanAnnotate(user?.role)
  const canExport = roleCanExport(user?.role)
  const canAssign = roleCanAssign(user?.role)
  const fileRef = useRef<HTMLInputElement>(null)
  // 页码放进 URL：从工作台点「返回」时才能回到原来那一页（工作台带回的
  // 「来处」含 query）。视图/筛选是会话内偏好，仍留在组件里。
  const [params, patchParams] = useUrlParams()
  const page = urlInt(params, 'page', 1)
  const setPage = (n: number) => patchParams({ page: n === 1 ? null : n })
  const [view, setView] = useState<'grid' | 'table'>('grid')
  const [exportOpen, setExportOpen] = useState(false)
  const [exporting, setExporting] = useState<string | null>(null)
  const [pageErr, setPageErr] = useState('')
  const [selTasks, setSelTasks] = useState<Set<number>>(new Set())
  const [qcFilter, setQcFilter] = useState('')
  const [stateFilter, setStateFilter] = useState('')
  const [assigneeFilter, setAssigneeFilter] = useState<string>('') // '' all, 'none' 未指派, or user id
  const [reviewerFilter, setReviewerFilter] = useState<string>('') // '' all, 'none' 未指派, or user id
  const [assignModal, setAssignModal] = useState<{ taskIds: number[]; curAssignee?: number | null; curReviewer?: number | null; curDeadline?: string | null } | null>(null)
  const [sort, setSort] = useState<SortState>(null)
  const pageSize = 24

  const toggleSort = (key: SortKey) =>
    setSort((s) => (s?.key === key ? { key, dir: s.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: 'desc' }))
  const toggleTask = (taskId: number) =>
    setSelTasks((prev) => { const n = new Set(prev); n.has(taskId) ? n.delete(taskId) : n.add(taskId); return n })

  // 用户 id→名 映射（显示/筛选指派人、审核人）。仅在能管理时拉取，失败回退为 #id。
  const { data: usersData } = useQuery({
    queryKey: ['users-all'],
    queryFn: () => userApi.list({ page: 1, page_size: 200 }),
    enabled: canAssign,
    staleTime: 5 * 60 * 1000,
  })
  const users = usersData?.items ?? []
  const userName = (uid?: number | null) => (uid == null ? '—' : (users.find((u) => u.id === uid)?.display_name || users.find((u) => u.id === uid)?.username || `#${uid}`))
  const annotators = users.filter((u) => /annotator|admin/i.test(u.role))
  const reviewers = users.filter((u) => /reviewer|admin/i.test(u.role))

  const { data, isLoading } = useQuery({
    queryKey: ['assets', datasetId, page, qcFilter],
    queryFn: () => assetApi.list(datasetId, page, pageSize, qcFilter || undefined),
  })
  const { data: dataset } = useQuery({
    queryKey: ['dataset', datasetId],
    queryFn: () => datasetApi.get(datasetId),
    enabled: Number.isFinite(datasetId) && datasetId > 0,
    staleTime: 5 * 60 * 1000,
  })

  const [uploadPct, setUploadPct] = useState<number | null>(null)
  const [uploadNote, setUploadNote] = useState<string | null>(null)
  const uploadMut = useMutation({
    // T0.2: chunk large files direct-to-store; small files / local driver use
    // the simple upload. Sequential so progress is meaningful.
    mutationFn: async (files: File[]) => {
      let created = 0, deduped = 0
      for (let i = 0; i < files.length; i++) {
        const r = await uploadFileSmart(datasetId, files[i], (frac) =>
          setUploadPct(Math.round(((i + frac) / files.length) * 100)))
        if (r.deduplicated) deduped++; else created++
      }
      return { created, deduped }
    },
    onSuccess: ({ created, deduped }) => {
      qc.invalidateQueries({ queryKey: ['assets', datasetId] })
      // 「允许重复」选项已随后端唯一约束移除：同数据集同内容只有一行。
      setUploadNote(deduped === 0 ? (created > 0 ? `已导入 ${created} 个` : null)
        : created > 0 ? `新增 ${created} 个；${deduped} 个为相同文件（已去重，未重复导入）`
        : `该文件已存在于本数据集（内容相同，已去重，未新建）。`)
    },
    onError: (e: any) => setPageErr(e?.response?.data?.error || e?.message || '上传失败'),
    onSettled: () => setUploadPct(null),
  })

  const assignMut = useMutation({
    mutationFn: (t: { taskIds: number[]; payload: TaskAssignPayload }) =>
      taskApi.batchAssign(t.taskIds, t.payload),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['assets', datasetId] }); setAssignModal(null); setSelTasks(new Set()) },
    onError: (e: any) => setPageErr(e?.response?.data?.error || e?.message || '任务管理更新失败'),
  })

  // 硬删除样本（连带 blob/任务/标注）。需 admin/审核员（后端 rolesReview 门）。
  const [deletingId, setDeletingId] = useState<number | null>(null)
  const deleteMut = useMutation({
    mutationFn: (assetId: number) => assetApi.remove(assetId),
    onMutate: (assetId) => setDeletingId(assetId),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['assets', datasetId] }); setSelTasks(new Set()) },
    onError: (e: any) => setPageErr(e?.response?.status === 403 ? '删除失败：需要 admin / 审核员权限' : `删除失败：${e?.response?.data?.error || e?.message || ''}`),
    onSettled: () => setDeletingId(null),
  })
  const canDelete = canExport // rolesReview（admin / reviewer）

  const doDeleteAsset = (a: { id: number; original_name: string }) => {
    if (confirm(`删除样本「${a.original_name}」？\n将永久删除文件、任务与全部标注，无法恢复。`)) deleteMut.mutate(a.id)
  }
  const doBatchDelete = async () => {
    const ids = (data?.items ?? []).filter((a) => a.task && selTasks.has(a.task.id)).map((a) => a.id)
    if (ids.length === 0) return
    if (!confirm(`删除选中的 ${ids.length} 个样本？\n将永久删除各自的文件、任务与全部标注，无法恢复。`)) return
    setPageErr('')
    for (const id of ids) {
      try { setDeletingId(id); await assetApi.remove(id) }
      catch (e: any) { setPageErr(e?.response?.status === 403 ? '删除失败：需要 admin / 审核员权限' : `部分删除失败：${e?.response?.data?.error || e?.message || ''}`) }
    }
    setDeletingId(null); setSelTasks(new Set())
    qc.invalidateQueries({ queryKey: ['assets', datasetId] })
  }

  // --- 批量自动标注（item 4）---
  const modality = (dataset?.modality || data?.items?.[0]?.modality || '').toLowerCase()
  const [batchCap, setBatchCap] = useState('')
  const [batchModel, setBatchModel] = useState('')
  const [batchConc, setBatchConc] = useState(2)
  const { data: capModels = [] } = useQuery({
    queryKey: ['cap-models'],
    queryFn: () => capabilityApi.models(),
    staleTime: 5 * 60 * 1000,
    enabled: canUpload,
  })
  // 该模态可批量的能力（有已注册模型的）
  const batchCaps = (MODALITY_CAPS[modality] ?? []).filter((c) => capModels.some((m) => m.capability_type === c))
  const canRunBatchAnnotate = canUpload && batchCaps.length > 0
  const effCap = batchCap || batchCaps[0] || ''
  const capModelOpts = capModels.filter((m) => m.capability_type === effCap)
  const effModel = batchModel || capModelOpts[0]?.model || ''
  const { data: batchJob } = useQuery({
    queryKey: ['batch-status', datasetId],
    queryFn: () => batchAnnotateApi.status(datasetId),
    enabled: canUpload,
    refetchInterval: (q) => (q.state.data?.status === 'running' ? 1500 : false),
  })
  const batchRunning = batchJob?.status === 'running'
  const startBatchMut = useMutation({
    mutationFn: () => batchAnnotateApi.start(datasetId, { task_ids: [...selTasks], capability: effCap, model: effModel || undefined, concurrency: batchConc }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['batch-status', datasetId] }); setSelTasks(new Set()) },
    onError: (e: any) => setPageErr(e?.response?.data?.error || e?.message || '批量自动标注启动失败'),
  })
  const cancelBatchMut = useMutation({
    mutationFn: () => batchAnnotateApi.cancel(datasetId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['batch-status', datasetId] }),
  })
  // 批量跑完后刷新资产（任务状态/结果变化）
  React.useEffect(() => {
    if (batchJob && (batchJob.status === 'completed' || batchJob.status === 'cancelled')) {
      qc.invalidateQueries({ queryKey: ['assets', datasetId] })
    }
  }, [batchJob?.status]) // eslint-disable-line react-hooks/exhaustive-deps

  const total = data?.total ?? 0
  const totalPages = Math.ceil(total / pageSize)
  // 客户端筛选（状态 / 指派人）——作用于当前页；QC 走服务端
  const assets = (data?.items ?? []).filter((a) => {
    if (stateFilter && a.task?.state !== stateFilter) return false
    if (assigneeFilter === 'none' && a.task?.assignee_id != null) return false
    if (assigneeFilter && assigneeFilter !== 'none' && String(a.task?.assignee_id) !== assigneeFilter) return false
    if (reviewerFilter === 'none' && a.task?.reviewer_id != null) return false
    if (reviewerFilter && reviewerFilter !== 'none' && String(a.task?.reviewer_id) !== reviewerFilter) return false
    return true
  })
  // 点列头排序（创建 / 更新 / 截止）——空值永远排末尾
  const sortVal = (a: any, key: string) => {
    const v = key === 'created' ? a.created_at : key === 'updated' ? a.task?.updated_at : a.task?.deadline_at
    return v ? new Date(v).getTime() : null
  }
  const rows = sort
    ? [...assets].sort((x, y) => {
        const a = sortVal(x, sort.key), b = sortVal(y, sort.key)
        if (a == null && b == null) return 0
        if (a == null) return 1
        if (b == null) return -1
        return sort.dir === 'asc' ? a - b : b - a
      })
    : assets

  // 一键全选（对齐文本模态）：选中当前页所有有任务的样本。
  const selectableTaskIds = rows.filter((a) => a.task).map((a) => a.task!.id)
  const allSelected = selectableTaskIds.length > 0 && selectableTaskIds.every((id) => selTasks.has(id))
  const toggleAll = () => setSelTasks(allSelected ? new Set() : new Set(selectableTaskIds))
  const canSelect = canExport || canAssign || canDelete || canRunBatchAnnotate

  const handleFiles = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? [])
    if (files.length) uploadMut.mutate(files)
    e.target.value = ''
  }

  // 导出格式随数据集模态切换：音频→字幕/RTTM/CSV/JSONL，视频→CVAT/MOT/JSONL，图片→COCO/YOLO/JSONL/JSON-LD，文本/其它→通用导出。
  const exportFormats = modality === 'audio' ? AUDIO_EXPORT_FORMATS : modality === 'video' ? VIDEO_EXPORT_FORMATS : modality === 'image' ? IMAGE_EXPORT_FORMATS : GENERIC_EXPORT_FORMATS

  const doExport = async (fmt: string) => {
    setExportOpen(false); setPageErr(''); setExporting(fmt)
    const taskIds = selTasks.size ? Array.from(selTasks) : undefined
    try {
      if (modality === 'audio') await exportAudioDataset(datasetId, fmt as AudioExportFormat, taskIds)
      else if (modality === 'video') await exportVideoDataset(datasetId, fmt as VideoExportFormat, taskIds)
      else if (modality === 'image') await exportImageDataset(datasetId, fmt as ImageExportFormat, taskIds)
      else await exportGenericDataset(datasetId, fmt as GenericExportFormat)
    } catch (e: any) {
      setPageErr(e?.response?.status === 403 ? '导出失败：需要 admin / 审核员权限' : `导出失败：${e?.message || '可能暂无已定稿的最终标注'}`)
    } finally {
      setExporting(null)
    }
  }

  const openAssign = (taskIds: number[], cur?: { assignee_id?: number | null; reviewer_id?: number | null; deadline_at?: string | null }) =>
    setAssignModal({
      taskIds,
      curAssignee: cur?.assignee_id ?? null,
      curReviewer: cur?.reviewer_id ?? null,
      curDeadline: cur?.deadline_at ?? null,
    })

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      <PageHeader
        title={dataset?.name ?? '数据集'}
        description={`共 ${total.toLocaleString()} ${modality === 'audio' ? '段音频' : modality === 'video' ? '个视频' : modality === 'image' ? '张图片' : '项'}`}
        leading={
          <Button variant="ghost" size="sm" onClick={() => navigate(backToOr(location.state, '/datasets'))}>
            <ArrowLeft className="h-3.5 w-3.5" />返回
          </Button>
        }
        actions={
          <div className="flex items-center gap-2">
            {/* 视图切换 */}
            <div className="flex items-center rounded-md border" style={{ borderColor: 'var(--border)' }}>
              <button onClick={() => setView('grid')} title="网格视图"
                className="flex h-8 w-8 items-center justify-center rounded-l-md"
                style={{ background: view === 'grid' ? 'var(--accent)' : 'transparent' }}><LayoutGrid className="h-3.5 w-3.5" /></button>
              <button onClick={() => setView('table')} title="表格 / 任务看板"
                className="flex h-8 w-8 items-center justify-center rounded-r-md"
                style={{ background: view === 'table' ? 'var(--accent)' : 'transparent' }}><Table2 className="h-3.5 w-3.5" /></button>
            </div>
            {canSelect && selectableTaskIds.length > 0 && (
              <label className="flex cursor-pointer items-center gap-1.5 text-xs" style={{ color: 'var(--muted-foreground)' }} title="全选 / 取消全选当前页">
                <input type="checkbox" className="h-3.5 w-3.5 cursor-pointer" checked={allSelected} onChange={toggleAll} />
                全选{selTasks.size > 0 ? ` (${selTasks.size})` : ''}
              </label>
            )}
            {canAssign && selTasks.size > 0 && (
              <Button variant="outline" size="sm" onClick={() => openAssign(Array.from(selTasks))}>
                <UserCog className="h-3.5 w-3.5" />任务管理 ({selTasks.size})
              </Button>
            )}
            {canDelete && selTasks.size > 0 && (
              <Button variant="outline" size="sm" className="border-red-200 text-red-600 hover:text-red-700" disabled={deletingId != null} onClick={doBatchDelete}>
                {deletingId != null ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}删除选中 ({selTasks.size})
              </Button>
            )}
            {canExport && (
              <>
                {selTasks.size > 0 && (
                  <button onClick={() => setSelTasks(new Set())} className="text-xs underline hover:opacity-80" style={{ color: 'var(--muted-foreground)' }}>清空选择</button>
                )}
                <div className="relative">
                  <Button variant="outline" size="sm" disabled={!!exporting} onClick={() => setExportOpen((o) => !o)}>
                    {exporting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Download className="h-3.5 w-3.5" />}
                    {exporting ? '导出中...' : (selTasks.size ? `导出选中 (${selTasks.size})` : '导出全部')}
                  </Button>
                  {exportOpen && (
                    <div className="absolute right-0 z-30 mt-1 w-52 rounded-md border py-1 shadow-md"
                      style={{ background: 'var(--card)', borderColor: 'var(--border)' }} onMouseLeave={() => setExportOpen(false)}>
                      {exportFormats.map(({ fmt, label }) => (
                        <button key={fmt} disabled={!!exporting}
                          className="block w-full px-3 py-1.5 text-left text-xs transition-colors hover:bg-[var(--accent)]"
                          onClick={() => doExport(fmt)}>{label}</button>
                      ))}
                      <div className="mt-1 border-t px-3 pb-0.5 pt-1.5 text-[10px]" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
                        {selTasks.size ? `仅导出选中的 ${selTasks.size} 项（已定稿部分）` : '勾选可只导出其中几项；不选则导出整个数据集'}
                      </div>
                    </div>
                  )}
                </div>
              </>
            )}
            {canUpload && (
              <>
                <input ref={fileRef} type="file" accept="image/*,audio/*,video/*" multiple className="hidden" onChange={handleFiles} />
                <Button variant="outline" size="sm" disabled={uploadMut.isPending} onClick={() => fileRef.current?.click()}>
                  {uploadMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Upload className="h-3.5 w-3.5" />}
                  {uploadMut.isPending ? `导入中 ${uploadPct ?? 0}%` : '导入数据'}
                </Button>
              </>
            )}
          </div>
        }
      />

      <TaskFilterBar
        stateFilter={stateFilter} onState={setStateFilter}
        qcFilter={qcFilter} onQc={(v) => { setQcFilter(v); setPage(1) }}
        assigneeFilter={assigneeFilter} onAssignee={setAssigneeFilter}
        reviewerFilter={reviewerFilter} onReviewer={setReviewerFilter}
        annotators={annotators} reviewers={reviewers} canAssign={canAssign}
        onClear={() => { setStateFilter(''); setQcFilter(''); setAssigneeFilter(''); setReviewerFilter(''); setPage(1) }}
        count={assets.length}
      />

      {/* 批量自动标注条（item 4）：勾选任务后出现，或有任务运行中 */}
      {canUpload && batchCaps.length > 0 && (selTasks.size > 0 || batchRunning) && (
        <div className="flex flex-wrap items-center gap-2 border-b px-6 py-2 text-xs" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
          <span className="flex items-center gap-1 font-medium" style={{ color: 'var(--primary)' }}><Sparkles className="h-3.5 w-3.5" />批量自动标注</span>
          {batchRunning ? (
            <>
              <span style={{ color: 'var(--muted-foreground)' }}>
                进行中 {(batchJob?.done ?? 0) + (batchJob?.failed ?? 0)}/{batchJob?.total ?? 0}（成功 {batchJob?.done ?? 0} · 失败 {batchJob?.failed ?? 0}）· {CAP_LABELS[batchJob?.capability ?? ''] ?? batchJob?.capability}
              </span>
              <Button size="sm" variant="outline" className="ml-auto border-red-200 text-red-600" onClick={() => cancelBatchMut.mutate()}>取消</Button>
            </>
          ) : (
            <>
              <span style={{ color: 'var(--muted-foreground)' }}>已选 {selTasks.size} 项</span>
              <select value={effCap} onChange={(e) => { setBatchCap(e.target.value); setBatchModel('') }}
                className="rounded border px-1.5 py-1" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                {batchCaps.map((c) => <option key={c} value={c}>{CAP_LABELS[c] ?? c}</option>)}
              </select>
              <select value={effModel} onChange={(e) => setBatchModel(e.target.value)}
                className="rounded border px-1.5 py-1" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                {capModelOpts.length === 0 && <option value="">（无模型 · 去能力配置接入）</option>}
                {capModelOpts.map((m) => <option key={`${m.provider_name}|${m.model}`} value={m.model}>{m.provider_name}{m.model ? ` · ${m.model}` : ''}</option>)}
              </select>
              <label className="flex items-center gap-1" style={{ color: 'var(--muted-foreground)' }}>并发
                <input type="number" min={1} max={8} value={batchConc}
                  onChange={(e) => setBatchConc(Math.max(1, Math.min(8, Number(e.target.value) || 2)))}
                  className="w-12 rounded border px-1 py-1" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
              </label>
              <Button size="sm" disabled={startBatchMut.isPending || !effCap || selTasks.size === 0} onClick={() => startBatchMut.mutate()}>
                {startBatchMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : '开始自动标注'}
              </Button>
            </>
          )}
        </div>
      )}

      {pageErr && (
        <div className="flex items-start gap-2 border-b px-6 py-2 text-xs" style={{ borderColor: 'var(--border)', background: 'color-mix(in srgb, #ef4444 12%, transparent)', color: '#ef4444' }}>
          <span className="flex-1">{pageErr}</span>
          <button onClick={() => setPageErr('')} className="shrink-0 opacity-70 hover:opacity-100"><X className="h-3.5 w-3.5" /></button>
        </div>
      )}
      {uploadNote && (
        <div className="flex items-start gap-2 border-b px-6 py-2 text-xs" style={{ borderColor: 'var(--border)', background: 'var(--muted)', color: 'var(--foreground)' }}>
          <span className="flex-1">{uploadNote}</span>
          <button onClick={() => setUploadNote(null)} className="shrink-0 opacity-70 hover:opacity-100"><X className="h-3.5 w-3.5" /></button>
        </div>
      )}

      <div className="flex-1 overflow-auto px-6 py-4">
        {isLoading ? (
          <GridSkeleton count={12} />
        ) : assets.length === 0 ? (
          <div className="flex h-40 flex-col items-center justify-center gap-2">
            <ImageIcon className="h-8 w-8" style={{ color: 'var(--muted-foreground)' }} />
            <p className="text-sm" style={{ color: 'var(--muted-foreground)' }}>暂无数据</p>
          </div>
        ) : view === 'grid' ? (
          <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6 gap-3">
            {rows.map((a) => {
              const checked = !!a.task && selTasks.has(a.task.id)
              return (
                <div key={a.id}
                  className={`group relative rounded-lg border overflow-hidden transition-colors ${a.task ? 'cursor-pointer hover:border-[var(--primary)]/50' : ''}`}
                  style={{ borderColor: checked ? 'var(--primary)' : 'var(--border)', borderWidth: checked ? 2 : 1, background: 'var(--card)' }}
                  onClick={() => a.task && navigate(taskRouteFor(a.modality, a.task.id), fromState(location))}>
                  {canSelect && a.task && (
                    <label className="absolute left-1.5 top-1.5 z-10 flex items-center justify-center rounded bg-black/45 p-1 backdrop-blur"
                      onClick={(e) => e.stopPropagation()}>
                      <input type="checkbox" className="h-4 w-4 cursor-pointer" checked={checked} onChange={() => toggleTask(a.task!.id)} />
                    </label>
                  )}
                  {canDelete && (
                    <button title="删除样本（永久）"
                      onClick={(e) => { e.stopPropagation(); doDeleteAsset(a) }}
                      className="absolute right-1.5 top-1.5 z-10 hidden rounded bg-black/45 p-1 text-white backdrop-blur transition-colors hover:bg-red-600 group-hover:block">
                      {deletingId === a.id ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
                    </button>
                  )}
                  <AssetImage assetId={a.id} alt={a.original_name} modality={a.modality} className="aspect-square w-full object-cover" />
                  <div className="p-2">
                    <p className="truncate text-xs font-medium" title={a.original_name}>{a.original_name}</p>
                    <div className="mt-1.5 flex items-center justify-between gap-1">
                      {a.task?.state ? (
                        <Badge variant="outline" className={`text-[10px] ${TASK_STATE_COLOR[a.task.state] ?? ''}`}>{TASK_STATE_LABELS[a.task.state] ?? a.task.state}</Badge>
                      ) : <span className="text-[10px]" style={{ color: 'var(--muted-foreground)' }}>无任务</span>}
                      <span className="text-[10px] font-mono" style={{ color: 'var(--muted-foreground)' }}>
                        {a.modality === 'image'
                          ? `${a.width}×${a.height}`
                          : a.duration_ms != null
                            ? `${Math.floor(a.duration_ms / 60000)}:${String(Math.floor((a.duration_ms % 60000) / 1000)).padStart(2, '0')}`
                            : a.modality}
                      </span>
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
        ) : (
          <TaskBoardTable
            rows={rows}
            canSelect={canSelect} canAssign={canAssign}
            selTasks={selTasks} onToggleSel={toggleTask}
            userName={userName}
            sort={sort} onToggleSort={toggleSort}
            onOpen={(tid) => {
              const row = rows.find((a) => a.task?.id === tid)
              navigate(taskRouteFor(row?.modality ?? modality, tid), fromState(location))
            }}
            onAssign={openAssign}
            renderThumb={(a) => <AssetImage assetId={a.id} modality={a.modality} className="h-8 w-8 shrink-0 rounded object-cover" />}
            canDelete={canDelete} onDelete={(a) => doDeleteAsset(a)} deletingId={deletingId}
            allSelected={allSelected} onToggleAll={toggleAll}
          />
        )}
      </div>

      <Pagination page={page} totalPages={totalPages} total={total} onPage={setPage} />

      {assignModal && (
        <AssignModal
          taskIds={assignModal.taskIds} curAssignee={assignModal.curAssignee} curReviewer={assignModal.curReviewer} curDeadline={assignModal.curDeadline}
          annotators={annotators} reviewers={reviewers} userName={userName}
          pending={assignMut.isPending}
          onClose={() => setAssignModal(null)}
          onSubmit={(payload) => assignMut.mutate({ taskIds: assignModal.taskIds, payload })}
        />
      )}
    </div>
  )
}
