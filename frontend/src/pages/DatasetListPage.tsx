import React, { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Plus, Search, Trash2, FolderOpen, FileText, Download, X, Loader2, Tags, FileCog, Sparkles } from 'lucide-react'
import { datasetApi, type Dataset, type CreateDatasetParams } from '@/api/dataset'
import { OntologyEditor } from '@/components/domain/dataset/OntologyEditor'
import { VideoAIConfigEditor } from '@/components/domain/dataset/VideoAIConfigEditor'
import { fromState, urlInt, useUrlParams } from '@/lib/navBack'
import { ExportMetaEditor } from '@/components/domain/dataset/ExportMetaEditor'
import { dashboardApi } from '@/api/dashboard'
import {
  exportImageDataset, IMAGE_EXPORT_FORMATS, type ImageExportFormat,
  exportAudioDataset, AUDIO_EXPORT_FORMATS, type AudioExportFormat,
  exportVideoDataset, VIDEO_EXPORT_FORMATS, type VideoExportFormat,
  exportGenericDataset, GENERIC_EXPORT_FORMATS, type GenericExportFormat,
} from '@/api/export'
import { PageHeader } from '@/components/common/PageHeader'
import { Pagination } from '@/components/common/Pagination'
import { SortHeader } from '@/components/common/SortHeader'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { TableSkeleton } from '@/components/ui/skeleton'
import { useAuthStore } from '@/stores/auth'
import { canExport as roleCanExport } from '@/lib/roles'

const MODALITY_LABELS: Record<string, string> = {
  text: '文本', image: '图片', audio: '音频', video: '视频',
}
const ANNOTATION_TYPE_LABELS: Record<string, string> = {
  text_annotation: '文本标注', image_annotation: '图片标注', ner: '命名实体',
}

type DatasetExportFormat = ImageExportFormat | AudioExportFormat | VideoExportFormat | GenericExportFormat
type DatasetExportOption = { fmt: DatasetExportFormat; label: string }

const COMMON_DATASET_EXPORT_FORMATS: DatasetExportOption[] = [
  { fmt: 'jsonl', label: '通用 JSONL（按各自模态导出）' },
]

function normalizeModality(modality?: string | null) {
  return (modality || 'text').toLowerCase()
}

function exportFormatsForModality(modality: string): DatasetExportOption[] {
  switch (normalizeModality(modality)) {
    case 'image':
      return IMAGE_EXPORT_FORMATS
    case 'audio':
      return AUDIO_EXPORT_FORMATS
    case 'video':
      return VIDEO_EXPORT_FORMATS
    default:
      return GENERIC_EXPORT_FORMATS
  }
}

// 文本导出的 annotation_stage 过滤项（仅文本模态支持；空 = 全部）。
const TEXT_EXPORT_STAGES: { value: string; label: string }[] = [
  { value: '', label: '全部阶段' },
  { value: 'refined', label: '仅已精修完成 (refined)' },
  { value: 'refining', label: '仅精修中 (refining)' },
  { value: 'auto_annotated', label: '仅已自动标注 (auto_annotated)' },
  { value: 'not_annotated', label: '仅未标注 (not_annotated)' },
]

// stage 仅对文本/通用导出生效（图片/音视频导出的是 FINALIZED 快照，无此概念）。
async function exportDatasetByModality(ds: Dataset, fmt: DatasetExportFormat, stage?: string) {
  switch (normalizeModality(ds.modality)) {
    case 'image':
      return exportImageDataset(ds.id, fmt as ImageExportFormat)
    case 'audio':
      return exportAudioDataset(ds.id, fmt as AudioExportFormat)
    case 'video':
      return exportVideoDataset(ds.id, fmt as VideoExportFormat)
    default:
      return exportGenericDataset(ds.id, fmt as GenericExportFormat, undefined, stage)
  }
}

export function DatasetListPage() {
  const navigate = useNavigate()
  const location = useLocation() // 带给详情页，供其「返回」原样复原本页（含 ?page/?q/?sort）
  const qc = useQueryClient()
  const isAdmin = useAuthStore((s) => s.isAdmin)
  const user = useAuthStore((s) => s.user)
  const canExport = roleCanExport(user?.role)
  // 列表状态放进 URL：用户点进数据集再返回时，页码/搜索/排序才不会丢
  // （此前是 useState，返回必回第 1 页——正是用户报的那个问题）。
  const [params, patchParams] = useUrlParams()
  const page = urlInt(params, 'page', 1)
  const search = params.get('q') ?? ''
  const sortParam = params.get('sort')
  const sortDir: 'asc' | 'desc' | null = sortParam === 'asc' || sortParam === 'desc' ? sortParam : null
  // 输入框逐字改 URL 会把历史刷爆 → replace；换搜索词回到第 1 页
  const setSearch = (v: string) => patchParams({ q: v, page: null }, true)
  const setPage = (n: number) => patchParams({ page: n === 1 ? null : n })
  const toggleSortDir = () => patchParams({ sort: sortDir === 'desc' ? 'asc' : 'desc' })
  const [showCreate, setShowCreate] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<number | null>(null)
  const [ontologyEdit, setOntologyEdit] = useState<Dataset | null>(null)
  const [videoAIEdit, setVideoAIEdit] = useState<Dataset | null>(null)
  const [exportMetaEdit, setExportMetaEdit] = useState<Dataset | null>(null)
  const [selDs, setSelDs] = useState<Set<number>>(new Set()) // 勾选的数据集（按集导出，各自一个文件）
  const [exportOpen, setExportOpen] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [exportErr, setExportErr] = useState('')
  const [exportStage, setExportStage] = useState('') // 文本导出的 stage 过滤（空 = 全部）
  const pageSize = 20

  const toggleDs = (id: number) =>
    setSelDs((p) => { const n = new Set(p); n.has(id) ? n.delete(id) : n.add(id); return n })

  // 批量导出：每个选中的数据集按自身模态导出为一个文件（不合并）。
  const batchExport = async (fmt: DatasetExportFormat) => {
    setExportOpen(false); setExportErr(''); setExporting(true)
    try {
      // stage 只在纯文本选择时生效，避免混合选择残留旧过滤悄悄作用到文本数据集。
      const effectiveStage = isTextOnly ? exportStage : ''
      for (const id of Array.from(selDs)) {
        const ds = (data?.items ?? []).find((item) => item.id === id) ?? await datasetApi.get(id)
        await exportDatasetByModality(ds, fmt, effectiveStage)
        await new Promise((r) => setTimeout(r, 350)) // 间隔，避免浏览器拦截连续下载
      }
    } catch (e: any) {
      setExportErr(e?.response?.status === 403 ? '导出失败：需要 admin / 审核员权限' : `导出失败：${e?.message || '部分数据集可能暂无已定稿标注'}`)
    } finally {
      setExporting(false)
    }
  }

  const { data, isLoading } = useQuery({
    queryKey: ['datasets', page, pageSize, search],
    queryFn: () => datasetApi.list({ page, page_size: pageSize, q: search || undefined }),
    placeholderData: (prev) => prev, // 逐字搜索时别闪空列表
  })

  const { data: categories } = useQuery({
    queryKey: ['categories'],
    queryFn: () => datasetApi.categories.list(),
    enabled: isAdmin(),
  })

  const deleteMut = useMutation({
    mutationFn: (id: number) => datasetApi.delete(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['datasets'] }); setDeleteConfirm(null) },
  })

  // 搜索在服务端做（?q=）：前端再过滤一遍只会把「当前页」当成全集，
  // 第 5 页上的数据集永远搜不到。
  const filtered = data?.items ?? []
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  // 管理员可按「更新时间」排序定位活跃/陈旧数据集
  const rows = sortDir
    ? [...filtered].sort((a, b) => {
        const av = a.updated_at ? new Date(a.updated_at).getTime() : 0
        const bv = b.updated_at ? new Date(b.updated_at).getTime() : 0
        return sortDir === 'asc' ? av - bv : bv - av
      })
    : filtered
  const selectableDatasets = rows
  const allSelected = selectableDatasets.length > 0 && selectableDatasets.every((d) => selDs.has(d.id))
  const toggleAllDatasets = () => setSelDs((p) => {
    const n = new Set(p)
    if (selectableDatasets.every((d) => n.has(d.id))) selectableDatasets.forEach((d) => n.delete(d.id))
    else selectableDatasets.forEach((d) => n.add(d.id))
    return n
  })
  const selectedOnPage = rows.filter((d) => selDs.has(d.id))
  const selectedModalities = new Set(selectedOnPage.map((d) => normalizeModality(d.modality)))
  const allSelectedVisible = selDs.size > 0 && selectedOnPage.length === selDs.size
  const exportFormats =
    allSelectedVisible && selectedModalities.size === 1
      ? exportFormatsForModality(Array.from(selectedModalities)[0])
      : COMMON_DATASET_EXPORT_FORMATS
  // stage 过滤仅对纯文本选择有意义（其它模态导出的是 FINALIZED 快照）。
  const isTextOnly = allSelectedVisible && selectedModalities.size === 1 && Array.from(selectedModalities)[0] === 'text'

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      <PageHeader
        title="数据集"
        description={`共 ${data?.total ?? 0} 个数据集`}
        actions={
          <div className="flex items-center gap-2">
            {canExport && selDs.size > 0 && (
              <>
                <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>
                  已选 {selDs.size}
                  <button onClick={() => setSelDs(new Set())} className="ml-1.5 underline hover:opacity-80">清空</button>
                </span>
                <div className="relative">
                  <Button variant="outline" size="sm" disabled={exporting} onClick={() => setExportOpen((o) => !o)}>
                    {exporting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Download className="h-3.5 w-3.5" />}
                    {exporting ? '导出中...' : `导出选中 (${selDs.size})`}
                  </Button>
                  {exportOpen && (
                    <div className="absolute right-0 z-30 mt-1 w-56 rounded-md border py-1 shadow-md"
                      style={{ background: 'var(--card)', borderColor: 'var(--border)' }}
                      onMouseLeave={() => setExportOpen(false)}>
                      {isTextOnly && (
                        <div className="border-b px-3 pb-2 pt-1" style={{ borderColor: 'var(--border)' }}>
                          <label className="mb-1 block text-[10px]" style={{ color: 'var(--muted-foreground)' }}>标注阶段过滤</label>
                          <select
                            value={exportStage}
                            onChange={(e) => setExportStage(e.target.value)}
                            className="h-7 w-full rounded border px-1.5 text-xs outline-none"
                            style={{ borderColor: 'var(--input)', background: 'var(--background)' }}
                          >
                            {TEXT_EXPORT_STAGES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
                          </select>
                        </div>
                      )}
                      {exportFormats.map(({ fmt, label }) => (
                        <button key={fmt} disabled={exporting}
                          className="block w-full px-3 py-1.5 text-left text-xs transition-colors hover:bg-[var(--accent)]"
                          onClick={() => batchExport(fmt)}>
                          {label}
                        </button>
                      ))}
                      <div className="mt-1 border-t px-3 pb-0.5 pt-1.5 text-[10px]" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
                        {selectedModalities.size > 1
                          ? '混合模态仅提供通用 JSONL；每个数据集各导出为一个文件'
                          : isTextOnly
                            ? `每个数据集各导出为一个文件；${exportStage ? `仅 ${TEXT_EXPORT_STAGES.find((s) => s.value === exportStage)?.label ?? exportStage}` : '含全部阶段'}（《通用元数据字段》信封）`
                            : '每个数据集各导出为一个文件（仅含已定稿标注）'}
                      </div>
                    </div>
                  )}
                </div>
              </>
            )}
            {isAdmin() && (
              <Button size="sm" onClick={() => setShowCreate(true)}>
                <Plus className="h-3.5 w-3.5" />
                新建数据集
              </Button>
            )}
          </div>
        }
      />

      {exportErr && (
        <div className="flex items-start gap-2 border-b px-6 py-2 text-xs" style={{ borderColor: 'var(--border)', background: 'color-mix(in srgb, #ef4444 12%, transparent)', color: '#ef4444' }}>
          <span className="flex-1">{exportErr}</span>
          <button onClick={() => setExportErr('')} className="shrink-0 opacity-70 hover:opacity-100"><X className="h-3.5 w-3.5" /></button>
        </div>
      )}

      {/* 搜索栏 */}
      <div className="flex items-center gap-3 px-6 py-3 border-b" style={{ borderColor: 'var(--border)' }}>
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-4 w-4" style={{ color: 'var(--muted-foreground)' }} />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索数据集..."
            className="h-8 w-full rounded-md border pl-9 pr-3 text-sm outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
          />
        </div>
      </div>

      {/* 表格 */}
      <div className="flex-1 overflow-auto px-6 py-4">
        {isLoading ? (
          <TableSkeleton rows={6} cols={5} />
        ) : filtered.length === 0 ? (
          <div className="flex h-40 flex-col items-center justify-center gap-2">
            <FolderOpen className="h-8 w-8" style={{ color: 'var(--muted-foreground)' }} />
            <p className="text-sm" style={{ color: 'var(--muted-foreground)' }}>
              {search ? '未找到匹配的数据集' : '暂无数据集'}
            </p>
          </div>
        ) : (
          <div className="rounded-lg border overflow-hidden" style={{ borderColor: 'var(--border)' }}>
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
                  {canExport && (
                    <th className="w-10 px-3 py-2.5">
                      <input type="checkbox" checked={allSelected} onChange={toggleAllDatasets}
                        ref={(el) => { if (el) el.indeterminate = selDs.size > 0 && !allSelected }}
                        title="全选当前页数据集" disabled={selectableDatasets.length === 0} />
                    </th>
                  )}
                  <th className="px-4 py-2.5 font-medium text-xs uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>名称</th>
                  <th className="px-4 py-2.5 font-medium text-xs uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>类型</th>
                  <th className="px-4 py-2.5 font-medium text-xs uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>文档数</th>
                  <th className="px-4 py-2.5 font-medium text-xs uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>进度</th>
                  <th className="px-4 py-2.5 font-medium text-xs uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>分类</th>
                  {isAdmin() && (
                    <SortHeader label="更新时间" active={sortDir !== null} dir={sortDir ?? 'desc'}
                      onClick={toggleSortDir} thClassName="px-4 py-2.5" />
                  )}
                  <th className="px-4 py-2.5 font-medium text-xs uppercase tracking-wider" style={{ color: 'var(--muted-foreground)' }}>操作</th>
                </tr>
              </thead>
              <tbody className="divide-y" style={{ '--tw-divide-opacity': 1 } as React.CSSProperties}>
                {rows.map((ds) => (
                  <DatasetRow
                    key={ds.id}
                    dataset={ds}
                    isAdmin={isAdmin()}
                    canExport={canExport}
                    selected={selDs.has(ds.id)}
                    onToggle={() => toggleDs(ds.id)}
                    onOpen={() => navigate(
                      `/datasets/${ds.id}/${ds.modality && ds.modality !== 'text' ? 'assets' : 'documents'}`,
                      fromState(location),
                    )}
                    onDelete={() => setDeleteConfirm(ds.id)}
                    onEditOntology={() => setOntologyEdit(ds)}
                    onEditExportMeta={() => setExportMetaEdit(ds)}
                    onEditVideoAI={() => setVideoAIEdit(ds)}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <Pagination page={page} totalPages={totalPages} total={total} onPage={setPage} />

      {/* 新建弹窗 */}
      {showCreate && (
        <CreateDatasetModal
          categories={categories ?? []}
          onClose={() => setShowCreate(false)}
          onCreated={() => { qc.invalidateQueries({ queryKey: ['datasets'] }); setShowCreate(false) }}
        />
      )}

      {/* 删除确认 */}
      {deleteConfirm !== null && (
        <ConfirmModal
          title="删除数据集"
          description="此操作将删除该数据集及其所有文档，无法恢复。确认删除？"
          loading={deleteMut.isPending}
          onConfirm={() => deleteMut.mutate(deleteConfirm)}
          onCancel={() => setDeleteConfirm(null)}
        />
      )}

      {/* 标签本体编辑（音/视频，管理员） */}
      {ontologyEdit && (
        <OntologyEditor
          datasetId={ontologyEdit.id}
          modality={normalizeModality(ontologyEdit.modality)}
          datasetName={ontologyEdit.name}
          onClose={() => setOntologyEdit(null)}
        />
      )}

      {/* AI 预标注设置（视频，管理员）——B2.8 成本闸门 */}
      {videoAIEdit && (
        <VideoAIConfigEditor
          datasetId={videoAIEdit.id}
          datasetName={videoAIEdit.name}
          onClose={() => setVideoAIEdit(null)}
        />
      )}

      {/* 导出信封元数据编辑（文本，管理员）——《通用元数据字段》规范 */}
      {exportMetaEdit && (
        <ExportMetaEditor
          datasetId={exportMetaEdit.id}
          datasetName={exportMetaEdit.name}
          onClose={() => setExportMetaEdit(null)}
        />
      )}
    </div>
  )
}

function DatasetRow({
  dataset: ds, isAdmin, canExport, selected, onToggle, onOpen, onDelete, onEditOntology, onEditExportMeta, onEditVideoAI,
}: {
  dataset: Dataset; isAdmin: boolean; canExport: boolean; selected: boolean
  onToggle: () => void; onOpen: () => void; onDelete: () => void; onEditOntology: () => void; onEditExportMeta: () => void
  onEditVideoAI: () => void
}) {
  const mod = normalizeModality(ds.modality)
  const hasOntology = mod === 'video' || mod === 'audio'
  const hasExportMeta = mod === 'text'
  const hasVideoAI = mod === 'video' // detect_track 成本闸门（B2.8）只对视频有意义
  return (
    <tr
      className="group transition-colors cursor-pointer"
      style={{ borderColor: 'var(--border)', background: selected ? 'var(--accent)' : undefined }}
      onClick={onOpen}
    >
      {canExport && (
        <td className="px-3 py-3" onClick={(e) => e.stopPropagation()}>
          <input type="checkbox" checked={selected} onChange={onToggle} title="勾选以导出此数据集" />
        </td>
      )}
      <td className="px-4 py-3">
        <div className="flex items-center gap-2">
          <FileText className="h-4 w-4 shrink-0" style={{ color: 'var(--muted-foreground)' }} />
          <span className="font-medium">{ds.name}</span>
        </div>
      </td>
      <td className="px-4 py-3">
        <Badge variant="secondary">
          {MODALITY_LABELS[ds.modality] ?? ds.modality}
          {ds.annotation_type && ` · ${ANNOTATION_TYPE_LABELS[ds.annotation_type] ?? ds.annotation_type}`}
        </Badge>
      </td>
      <td className="px-4 py-3">
        <span className="font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>
          {ds.doc_count.toLocaleString()}
        </span>
      </td>
      <td className="px-4 py-3"><DatasetProgress ds={ds} /></td>
      <td className="px-4 py-3">
        {ds.category ? (
          <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>{ds.category.name}</span>
        ) : (
          <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>—</span>
        )}
      </td>
      {isAdmin && (
        <td className="px-4 py-3 text-xs" style={{ color: 'var(--muted-foreground)' }}>
          {ds.updated_at ? new Date(ds.updated_at).toLocaleString('zh-CN') : '—'}
        </td>
      )}
      <td className="px-4 py-3">
        <div className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
          <Button variant="ghost" size="sm" onClick={onOpen}>打开</Button>
          {isAdmin && hasOntology && (
            <Button variant="ghost" size="icon" onClick={onEditOntology} title="编辑标签本体">
              <Tags className="h-3.5 w-3.5" style={{ color: 'var(--muted-foreground)' }} />
            </Button>
          )}
          {isAdmin && hasVideoAI && (
            <Button variant="ghost" size="icon" onClick={onEditVideoAI} title="AI 预标注设置（触发模式 / 成本闸门）">
              <Sparkles className="h-3.5 w-3.5" style={{ color: 'var(--muted-foreground)' }} />
            </Button>
          )}
          {isAdmin && hasExportMeta && (
            <Button variant="ghost" size="icon" onClick={onEditExportMeta} title="导出信封元数据">
              <FileCog className="h-3.5 w-3.5" style={{ color: 'var(--muted-foreground)' }} />
            </Button>
          )}
          {isAdmin && (
            <Button variant="ghost" size="icon" onClick={onDelete}>
              <Trash2 className="h-3.5 w-3.5 text-red-500" />
            </Button>
          )}
        </div>
      </td>
    </tr>
  )
}

// 数据集进度条：傻瓜化展示 已标 / 总数 + 剩余待标，让标注员一眼知道哪还有活、推进到哪。
// 复用 /dashboard/stats（按 dataset 聚合，已缓存）；按模态取"已完成"口径。
function DatasetProgress({ ds }: { ds: Dataset }) {
  const { data, isLoading } = useQuery({
    queryKey: ['ds-progress', ds.id],
    queryFn: () => dashboardApi.stats(ds.id),
    staleTime: 60_000,
  })
  if (isLoading || !data) {
    return <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>…</span>
  }
  let total = 0
  let done = 0
  if (ds.modality === 'image' && data.image_tasks) {
    total = data.image_tasks.total
    const sd = data.image_tasks.state_distribution ?? {}
    done = (sd.FINALIZED ?? 0) + (sd.EXPORTED ?? 0)
  } else {
    total = data.doc_count || ds.doc_count
    const sd = data.stage_distribution ?? {}
    done = (sd.refined ?? 0) + (sd.completed ?? 0)
  }
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  const remaining = Math.max(total - done, 0)
  return (
    <div className="w-32">
      <div className="flex items-center justify-between text-[10px]" style={{ color: 'var(--muted-foreground)' }}>
        <span className="font-mono">{done}/{total}</span>
        {total === 0
          ? <span>—</span>
          : remaining > 0
            ? <span style={{ color: 'var(--chart-4)' }}>剩 {remaining}</span>
            : <span style={{ color: 'var(--chart-2)' }}>✓ 完成</span>}
      </div>
      <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full" style={{ background: 'var(--muted)' }}>
        <div className="h-full rounded-full transition-all" style={{ width: `${pct}%`, background: pct === 100 ? 'var(--chart-2)' : 'var(--primary)' }} />
      </div>
    </div>
  )
}

function CreateDatasetModal({
  categories, onClose, onCreated,
}: {
  categories: { id: number; name: string }[]
  onClose: () => void
  onCreated: () => void
}) {
  const qc = useQueryClient()
  const [form, setForm] = useState<CreateDatasetParams>({ name: '', modality: 'text', annotation_type: 'text_annotation' })
  const [error, setError] = useState('')

  // 数据类型 → modality + 对应 annotation_type 的联动
  const MODALITY_OPTIONS: { value: string; label: string; annType: string }[] = [
    { value: 'text', label: '文本', annType: 'text_annotation' },
    { value: 'image', label: '图片', annType: 'image_annotation' },
    { value: 'audio', label: '音频', annType: 'audio_annotation' },
    { value: 'video', label: '视频', annType: 'video_annotation' },
  ]

  const mut = useMutation({
    mutationFn: (data: CreateDatasetParams) =>
      datasetApi.create({ ...data, modality: data.modality || 'text' }),
    onSuccess: () => onCreated(),
    onError: (e: any) => setError(e?.response?.data?.message ?? '创建失败'),
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-md rounded-xl border p-6 shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
        <h2 className="mb-4 text-base font-semibold">新建数据集</h2>
        <div className="space-y-3">
          <Field label="名称 *">
            <input
              value={form.name}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              placeholder="数据集名称"
              className="h-8 w-full rounded-md border px-3 text-sm outline-none"
              style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
            />
          </Field>
          <Field label="数据类型">
            <select
              value={form.modality}
              onChange={(e) => {
                const opt = MODALITY_OPTIONS.find((o) => o.value === e.target.value)!
                setForm((f) => ({ ...f, modality: opt.value, annotation_type: opt.annType }))
              }}
              className="h-8 w-full rounded-md border px-3 text-sm outline-none"
              style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
            >
              {MODALITY_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
          </Field>
          {categories.length > 0 && (
            <Field label="分类">
              <select
                value={form.category_id ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, category_id: e.target.value ? Number(e.target.value) : undefined }))}
                className="h-8 w-full rounded-md border px-3 text-sm outline-none"
                style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
              >
                <option value="">不选择</option>
                {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </select>
            </Field>
          )}
          {error && <p className="text-xs text-red-600">{error}</p>}
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>取消</Button>
          <Button size="sm" disabled={!form.name || mut.isPending} onClick={() => mut.mutate(form)}>
            {mut.isPending ? '创建中...' : '创建'}
          </Button>
        </div>
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>{label}</label>
      {children}
    </div>
  )
}

function ConfirmModal({ title, description, loading, onConfirm, onCancel }: {
  title: string; description: string; loading: boolean
  onConfirm: () => void; onCancel: () => void
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-sm rounded-xl border p-6 shadow-xl" style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
        <h2 className="mb-2 text-base font-semibold">{title}</h2>
        <p className="text-sm mb-5" style={{ color: 'var(--muted-foreground)' }}>{description}</p>
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onCancel}>取消</Button>
          <Button variant="destructive" size="sm" disabled={loading} onClick={onConfirm}>
            {loading ? '删除中...' : '确认删除'}
          </Button>
        </div>
      </div>
    </div>
  )
}
