import React, { useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { backToOr } from '@/lib/navBack'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Upload, Trash2, Pencil, FileText, ArrowLeft, ChevronsUpDown, Search, X, Sparkles, RotateCcw, Download, Loader2, UserCog } from 'lucide-react'
import { datasetApi } from '@/api/dataset'
import { documentApi, type Document } from '@/api/document'
import { exportGenericDataset, GENERIC_EXPORT_FORMATS, type GenericExportFormat } from '@/api/export'
import { userApi } from '@/api/user'
import type { TaskAssignPayload } from '@/api/imageTask'
import { PageHeader } from '@/components/common/PageHeader'
import { Pagination } from '@/components/common/Pagination'
import { SortHeader } from '@/components/common/SortHeader'
import { StageTag } from '@/components/common/StageTag'
import { TableSkeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { DocumentCandidateBatchBar } from '@/components/domain/annotation/DocumentCandidateBatchBar'
import { AssignModal } from '@/components/domain/task-board/AssignModal'
import { useAuthStore } from '@/stores/auth'

const nullableNumber = (v: any): number | null => {
  if (v == null || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? n : null
}
const docAssigneeId = (doc: Document) => nullableNumber(doc.assignee_id ?? doc.data?.assignee_id)
const docReviewerId = (doc: Document) => nullableNumber(doc.reviewer_id ?? doc.data?.reviewer_id)
const docDeadline = (doc: Document) => (doc.deadline_at || doc.data?.deadline_at || doc.data?.deadline) as string | undefined

export function DocumentListPage() {
  const { id } = useParams<{ id: string }>()
  const datasetId = Number(id)
  const navigate = useNavigate()
  const location = useLocation()
  const qc = useQueryClient()
  const isAdmin = useAuthStore((s) => s.isAdmin)
  const user = useAuthStore((s) => s.user)
  const canExport = ['admin', 'reviewer', 'image_reviewer'].includes(user?.role ?? '')
  const canAssign = ['admin', 'reviewer', 'image_reviewer'].includes(user?.role ?? '')
  const canDelete = canExport
  const canSelect = canExport || canAssign || canDelete
  const fileRef = useRef<HTMLInputElement>(null)
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [sort, setSort] = useState<{ key: 'created' | 'updated' | 'deadline'; dir: 'asc' | 'desc' } | null>(null)
  const [assignModal, setAssignModal] = useState<{ docKeys: string[]; curAssignee?: number | null; curReviewer?: number | null; curDeadline?: string | null } | null>(null)
  const [assigneeFilter, setAssigneeFilter] = useState('')
  const [reviewerFilter, setReviewerFilter] = useState('')
  const [searchInput, setSearchInput] = useState('')
  const [query, setQuery] = useState('')
  const [batchMsg, setBatchMsg] = useState('')
  const [exportOpen, setExportOpen] = useState(false)
  const [exporting, setExporting] = useState<string | null>(null)
  const [exportErr, setExportErr] = useState('')
  const [deletingKey, setDeletingKey] = useState<string | null>(null)
  const pageSize = 20

  const toggleSort = (key: 'created' | 'updated' | 'deadline') =>
    setSort((s) => (s?.key === key ? { key, dir: s.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: 'desc' }))

  const { data, isLoading } = useQuery({
    queryKey: ['documents', datasetId, page, query],
    queryFn: () => documentApi.list(datasetId, page, pageSize, query),
  })
  const { data: dataset } = useQuery({
    queryKey: ['dataset', datasetId],
    queryFn: () => datasetApi.get(datasetId),
    enabled: Number.isFinite(datasetId) && datasetId > 0,
    staleTime: 5 * 60 * 1000,
  })
  const { data: importFormats = [] } = useQuery({
    queryKey: ['document-import-formats'],
    queryFn: () => documentApi.formats().catch(() => []),
    enabled: isAdmin(),
    staleTime: 10 * 60 * 1000,
  })
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
  const importAccept = useMemo(() => {
    const exts = importFormats.flatMap((f) => f.extensions ?? [])
    return exts.length > 0 ? Array.from(new Set(exts)).join(',') : '.json,.jsonl,.csv'
  }, [importFormats])

  const applySearch = () => {
    setSelected(new Set())
    setPage(1)
    setQuery(searchInput.trim())
  }

  const clearSearch = () => {
    setSearchInput('')
    setQuery('')
    setSelected(new Set())
    setPage(1)
  }

  const importMut = useMutation({
    mutationFn: (file: File) => documentApi.import(datasetId, file),
    onSuccess: (report) => {
      qc.invalidateQueries({ queryKey: ['documents', datasetId] })
      qc.invalidateQueries({ queryKey: ['dataset', datasetId] })
      setSelected(new Set())
      setPage(1)
      const skipped = report.skipped_count ? `，跳过 ${report.skipped_count}` : ''
      const failed = report.failed_count ? `，失败 ${report.failed_count}` : ''
      setBatchMsg(`导入完成：成功 ${report.imported_count}${skipped}${failed}`)
    },
    onError: (e: any) => setBatchMsg(e?.response?.data?.message || e?.message || '导入失败'),
  })

  const batchDeleteMut = useMutation({
    mutationFn: (keys: string[]) => documentApi.batchDelete(datasetId, keys),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['documents', datasetId] })
      setSelected(new Set())
    },
    onError: (e: any) => setBatchMsg(e?.response?.status === 403 ? '删除失败：需要 admin / 审核员权限' : `删除失败：${e?.response?.data?.error || e?.response?.data?.message || e?.message || ''}`),
  })

  const deleteMut = useMutation({
    mutationFn: (key: string) => documentApi.delete(datasetId, key),
    onMutate: (key) => setDeletingKey(key),
    onSuccess: (_res, key) => {
      qc.invalidateQueries({ queryKey: ['documents', datasetId] })
      setSelected((prev) => {
        const next = new Set(prev)
        next.delete(key)
        return next
      })
    },
    onError: (e: any) => setBatchMsg(e?.response?.status === 403 ? '删除失败：需要 admin / 审核员权限' : `删除失败：${e?.response?.data?.error || e?.response?.data?.message || e?.message || ''}`),
    onSettled: () => setDeletingKey(null),
  })

  const assignMut = useMutation({
    mutationFn: (v: { docKeys: string[]; payload: TaskAssignPayload }) => documentApi.assign(datasetId, v.docKeys, v.payload),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['documents', datasetId] })
      setSelected(new Set()); setAssignModal(null)
    },
    onError: (e: any) => setBatchMsg(e?.response?.data?.error || e?.response?.data?.message || e?.message || '任务管理更新失败'),
  })

  const reAnnotateMut = useMutation({
    mutationFn: (doc: Document) => documentApi.reAnnotate(doc.doc_key, datasetId),
    onSuccess: (_res, doc) => {
      qc.invalidateQueries({ queryKey: ['documents', datasetId] })
      qc.invalidateQueries({ queryKey: ['document', doc.doc_key, datasetId] })
      qc.invalidateQueries({ queryKey: ['refinement', doc.doc_key, datasetId] })
      navigate(`/datasets/${datasetId}/documents/${doc.doc_key}/annotate`)
    },
    onError: (e: any) => setBatchMsg(e?.response?.data?.message || e?.message || '重新标注失败'),
  })

  const reAnnotateDocument = (doc: Document) => {
    const title = (doc.data?.title as string) || doc.doc_key
    if (!window.confirm(`将「${title}」退回精标中并进入编辑页面。确定继续？`)) return
    reAnnotateMut.mutate(doc)
  }

  const deleteDocument = (doc: Document) => {
    const title = (doc.data?.title as string) || doc.doc_key
    if (!window.confirm(`删除文档「${title}」？\n将永久删除该文档的版本与全部标注，无法恢复。`)) return
    deleteMut.mutate(doc.doc_key)
  }

  const rawDocs = data?.items ?? []
  const selectedDocKeys = useMemo(() => Array.from(selected), [selected])
  const filteredDocs = rawDocs.filter((doc) => {
    const assigneeId = docAssigneeId(doc)
    const reviewerId = docReviewerId(doc)
    if (assigneeFilter === 'none' && assigneeId != null) return false
    if (assigneeFilter && assigneeFilter !== 'none' && String(assigneeId) !== assigneeFilter) return false
    if (reviewerFilter === 'none' && reviewerId != null) return false
    if (reviewerFilter && reviewerFilter !== 'none' && String(reviewerId) !== reviewerFilter) return false
    return true
  })
  const docs = sort
    ? [...filteredDocs].sort((x, y) => {
        const get = (d: Document) => {
          const v = sort.key === 'created' ? d.created_at : sort.key === 'updated' ? d.updated_at : docDeadline(d)
          return v ? new Date(v).getTime() : null
        }
        const a = get(x), b = get(y)
        if (a == null && b == null) return 0
        if (a == null) return 1
        if (b == null) return -1
        return sort.dir === 'asc' ? a - b : b - a
      })
    : filteredDocs
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  const toggleSelect = (key: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      next.has(key) ? next.delete(key) : next.add(key)
      return next
    })
  }

  const allPageSelected = docs.length > 0 && docs.every((d) => selected.has(d.doc_key))
  const toggleAll = () => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (allPageSelected) docs.forEach((d) => next.delete(d.doc_key))
      else docs.forEach((d) => next.add(d.doc_key))
      return next
    })
  }

  const deleteSelectedDocuments = () => {
    if (selectedDocKeys.length === 0) return
    if (!window.confirm(`删除选中的 ${selectedDocKeys.length} 个文档？\n将永久删除各自的版本与全部标注，无法恢复。`)) return
    batchDeleteMut.mutate(selectedDocKeys)
  }

  const handleFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (file) importMut.mutate(file)
    e.target.value = ''
  }

  const doExport = async (fmt: GenericExportFormat) => {
    setExportOpen(false); setExportErr(''); setExporting(fmt)
    try {
      await exportGenericDataset(datasetId, fmt, selectedDocKeys.length ? selectedDocKeys : undefined)
    } catch (e: any) {
      setExportErr(e?.response?.status === 403 ? '导出失败：需要 admin / 审核员权限' : `导出失败：${e?.response?.data?.error || e?.message || '可能暂无可导出文档'}`)
    } finally {
      setExporting(null)
    }
  }

  const openAssign = (docKeys: string[], cur?: { assignee_id?: number | null; reviewer_id?: number | null; deadline_at?: string | null }) =>
    setAssignModal({
      docKeys,
      curAssignee: cur?.assignee_id ?? null,
      curReviewer: cur?.reviewer_id ?? null,
      curDeadline: cur?.deadline_at ?? null,
    })

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      <PageHeader
        title={dataset?.name ?? '数据集'}
        description={`共 ${total.toLocaleString()} 项数据`}
        leading={
          <Button variant="ghost" size="sm" onClick={() => navigate(backToOr(location.state, '/datasets'))}>
            <ArrowLeft className="h-3.5 w-3.5" />返回
          </Button>
        }
        actions={
          <div className="flex items-center gap-2">
            {canExport && (
              <div className="relative">
                <Button variant="outline" size="sm" disabled={!!exporting} onClick={() => setExportOpen((o) => !o)}>
                  {exporting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Download className="h-3.5 w-3.5" />}
                  {exporting ? '导出中...' : (selected.size ? `导出选中 (${selected.size})` : '导出全部')}
                </Button>
                {exportOpen && (
                  <div className="absolute right-0 z-30 mt-1 w-48 rounded-md border py-1 shadow-md"
                    style={{ background: 'var(--card)', borderColor: 'var(--border)' }}
                    onMouseLeave={() => setExportOpen(false)}>
                    {GENERIC_EXPORT_FORMATS.map(({ fmt, label }) => (
                      <button key={fmt} disabled={!!exporting}
                        className="block w-full px-3 py-1.5 text-left text-xs transition-colors hover:bg-[var(--accent)]"
                        onClick={() => doExport(fmt)}>
                        {label}
                      </button>
                    ))}
                    <div className="mt-1 border-t px-3 pb-0.5 pt-1.5 text-[10px]" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
                      {selected.size ? `仅导出选中的 ${selected.size} 项` : '未勾选时导出全部文档'}
                    </div>
                  </div>
                )}
              </div>
            )}
            {isAdmin() && (
              <>
                <input ref={fileRef} type="file" accept={importAccept} className="hidden" onChange={handleFile} />
                <Button variant="outline" size="sm" disabled={importMut.isPending} onClick={() => fileRef.current?.click()}>
                  <Upload className="h-3.5 w-3.5" />
                  {importMut.isPending ? '导入中...' : '导入数据'}
                </Button>
              </>
            )}
          </div>
        }
      />

      {/* 批量操作栏 */}
      {selected.size > 0 && canSelect && (
        <div className="flex flex-wrap items-center gap-2 px-6 py-2.5 border-b" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
          <span className="shrink-0 text-sm whitespace-nowrap">已选中 {selected.size} 项</span>
          {canAssign && (
            <Button variant="outline" size="sm" className="whitespace-nowrap" onClick={() => openAssign(selectedDocKeys)}>
              <UserCog className="h-3.5 w-3.5" />任务管理
            </Button>
          )}
          {canDelete && (
            <Button variant="outline" size="sm" className="border-red-200 text-red-600 hover:text-red-700 whitespace-nowrap" disabled={batchDeleteMut.isPending}
              onClick={deleteSelectedDocuments}>
              {batchDeleteMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}删除选中
            </Button>
          )}
          <Button variant="ghost" size="sm" className="ml-auto whitespace-nowrap" onClick={() => setSelected(new Set())}>取消选择</Button>
        </div>
      )}
      {exportErr && (
        <div className="flex items-start gap-2 border-b px-6 py-2 text-xs" style={{ borderColor: 'var(--border)', background: 'color-mix(in srgb, #ef4444 12%, transparent)', color: '#ef4444' }}>
          <span className="flex-1">{exportErr}</span>
          <button onClick={() => setExportErr('')} className="shrink-0 opacity-70 hover:opacity-100"><X className="h-3.5 w-3.5" /></button>
        </div>
      )}
      {batchMsg && (
        <div className="flex items-start gap-2 border-b px-6 py-2 text-xs" style={{ borderColor: 'var(--border)', background: 'color-mix(in srgb, var(--chart-2) 12%, transparent)', color: 'var(--foreground)' }}>
          <Sparkles className="mt-0.5 h-3.5 w-3.5 shrink-0" style={{ color: 'var(--chart-2)' }} />
          <span className="flex-1">{batchMsg}</span>
          <button onClick={() => setBatchMsg('')} className="shrink-0 opacity-70 hover:opacity-100"><X className="h-3.5 w-3.5" /></button>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2 border-b px-6 py-3" style={{ borderColor: 'var(--border)' }}>
        <div className="relative w-full max-w-xl">
          <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2" style={{ color: 'var(--muted-foreground)' }} />
          <input
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') applySearch() }}
            placeholder="搜索 doc_key / 标题 / 正文片段..."
            className="h-8 w-full rounded-md border pl-9 pr-9 text-sm outline-none"
            style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
          />
          {searchInput && (
            <button
              className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 opacity-70 hover:opacity-100"
              onClick={clearSearch}
              title="清除搜索"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
        <Button variant="outline" size="sm" disabled={isLoading} onClick={applySearch}>
          搜索
        </Button>
        {canAssign && (
          <>
            <select value={assigneeFilter} onChange={(e) => setAssigneeFilter(e.target.value)}
              className="h-8 rounded-md border px-2 text-xs outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
              <option value="">全部标注员</option>
              <option value="none">未指派</option>
              {annotators.map((u) => <option key={u.id} value={u.id}>{u.display_name || u.username}</option>)}
            </select>
            <select value={reviewerFilter} onChange={(e) => setReviewerFilter(e.target.value)}
              className="h-8 rounded-md border px-2 text-xs outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
              <option value="">全部审核员</option>
              <option value="none">未指派审核</option>
              {reviewers.map((u) => <option key={u.id} value={u.id}>{u.display_name || u.username}</option>)}
            </select>
          </>
        )}
        {(assigneeFilter || reviewerFilter) && (
          <button onClick={() => { setAssigneeFilter(''); setReviewerFilter('') }} className="text-xs underline hover:opacity-80" style={{ color: 'var(--muted-foreground)' }}>
            清除人员筛选
          </button>
        )}
        {query && (
          <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>
            当前筛选：<span className="font-mono" style={{ color: 'var(--foreground)' }}>{query}</span> · 命中 {total.toLocaleString()} 项
          </span>
        )}
      </div>

      {isAdmin() && (
        <DocumentCandidateBatchBar
          datasetId={datasetId}
          docKeys={selectedDocKeys}
          onMessage={setBatchMsg}
          onClearSelection={() => setSelected(new Set())}
        />
      )}

      {/* 表格 */}
      <div className="flex-1 overflow-auto px-6 py-4">
        {isLoading ? (
          <TableSkeleton rows={8} cols={5} />
        ) : docs.length === 0 ? (
          <div className="flex h-40 flex-col items-center justify-center gap-2">
            <FileText className="h-8 w-8" style={{ color: 'var(--muted-foreground)' }} />
            <p className="text-sm" style={{ color: 'var(--muted-foreground)' }}>
              {query ? '未找到匹配的数据资产' : '暂无数据资产，请先导入'}
            </p>
          </div>
        ) : (
          <div className="space-y-2">
            <div className="flex items-center gap-1.5 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
              <ChevronsUpDown className="h-3 w-3" />
              点「创建时间 / 更新时间 / 截止」<span style={{ color: 'var(--foreground)' }}>列头</span>可排序，再点一次切换升 / 降序
              {sort && <span style={{ color: 'var(--primary)' }}>· 当前：{({ created: '创建时间', updated: '更新时间', deadline: '截止' })[sort.key]} {sort.dir === 'asc' ? '升序↑' : '降序↓'}</span>}
            </div>
            <div className="rounded-lg border overflow-x-auto" style={{ borderColor: 'var(--border)' }}>
            <table className="w-full min-w-[1320px] table-fixed text-sm">
              <colgroup>
                {canSelect && <col style={{ width: 48 }} />}
                <col style={{ width: '24%' }} />
                <col style={{ width: 116 }} />
                <col style={{ width: 108 }} />
                <col style={{ width: 108 }} />
                <col style={{ width: 64 }} />
                <col style={{ width: 148 }} />
                <col style={{ width: 148 }} />
                <col style={{ width: 156 }} />
                <col style={{ width: 220 }} />
              </colgroup>
              <thead>
                <tr className="border-b text-left" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
                  {canSelect && (
                    <th className="px-4 py-2.5 w-10">
                      <input type="checkbox" checked={allPageSelected} onChange={toggleAll}
                        ref={(el) => { if (el) el.indeterminate = selected.size > 0 && !allPageSelected }} />
                    </th>
                  )}
                  <th className="px-3 py-2.5 font-medium text-xs uppercase tracking-wider whitespace-nowrap" style={{ color: 'var(--muted-foreground)' }}>资产 ID</th>
                  <th className="px-3 py-2.5 font-medium text-xs uppercase tracking-wider whitespace-nowrap" style={{ color: 'var(--muted-foreground)' }}>阶段</th>
                  <th className="px-3 py-2.5 font-medium text-xs uppercase tracking-wider whitespace-nowrap" style={{ color: 'var(--muted-foreground)' }}>标注员</th>
                  <th className="px-3 py-2.5 font-medium text-xs uppercase tracking-wider whitespace-nowrap" style={{ color: 'var(--muted-foreground)' }}>审核员</th>
                  <th className="px-3 py-2.5 font-medium text-xs uppercase tracking-wider whitespace-nowrap text-center" style={{ color: 'var(--muted-foreground)' }}>版本</th>
                  <SortHeader label="创建时间" active={sort?.key === 'created'} dir={sort?.dir ?? 'desc'} onClick={() => toggleSort('created')} thClassName="px-4 py-2.5" />
                  <SortHeader label="更新时间" active={sort?.key === 'updated'} dir={sort?.dir ?? 'desc'} onClick={() => toggleSort('updated')} thClassName="px-4 py-2.5" />
                  <SortHeader label="截止" active={sort?.key === 'deadline'} dir={sort?.dir ?? 'desc'} onClick={() => toggleSort('deadline')} thClassName="px-4 py-2.5" />
                  <th
                    className="sticky right-0 z-20 px-3 py-2.5 text-right font-medium text-xs uppercase tracking-wider whitespace-nowrap"
                    style={{ color: 'var(--muted-foreground)', background: 'var(--muted)', boxShadow: '-10px 0 16px -16px rgba(0,0,0,.55)' }}
                  >
                    操作
                  </th>
                </tr>
              </thead>
              <tbody>
                {docs.map((doc) => (
                  <DocRow key={doc.doc_key} doc={doc}
                    canSelect={canSelect}
                    checked={selected.has(doc.doc_key)} onToggle={() => toggleSelect(doc.doc_key)}
                    onAnnotate={() => navigate(`/datasets/${datasetId}/documents/${doc.doc_key}/annotate`)}
                    canAssign={canAssign}
                    canDelete={canDelete}
                    userName={userName}
                    onAssign={() => openAssign([doc.doc_key], { assignee_id: docAssigneeId(doc), reviewer_id: docReviewerId(doc), deadline_at: docDeadline(doc) ?? null })}
                    onReAnnotate={() => reAnnotateDocument(doc)}
                    reAnnotatePending={reAnnotateMut.isPending}
                    onDelete={() => deleteDocument(doc)}
                    deleting={deletingKey === doc.doc_key}
                  />
                ))}
              </tbody>
            </table>
            </div>
          </div>
        )}
      </div>

      <Pagination page={page} totalPages={totalPages} total={total} onPage={setPage} />

      {assignModal && (
        <AssignModal
          taskIds={assignModal.docKeys.map((_, i) => i + 1)}
          itemLabel="文档"
          curAssignee={assignModal.curAssignee}
          curReviewer={assignModal.curReviewer}
          curDeadline={assignModal.curDeadline}
          annotators={annotators}
          reviewers={reviewers}
          userName={userName}
          pending={assignMut.isPending}
          onClose={() => setAssignModal(null)}
          onSubmit={(payload) => assignMut.mutate({ docKeys: assignModal.docKeys, payload })}
        />
      )}
    </div>
  )
}

function DocRow({ doc, canSelect, checked, onToggle, onAnnotate, canAssign, canDelete, userName, onAssign, onReAnnotate, reAnnotatePending, onDelete, deleting }: {
  doc: Document; canSelect: boolean
  checked: boolean; onToggle: () => void; onAnnotate: () => void
  canAssign: boolean; canDelete: boolean; userName: (uid?: number | null) => string; onAssign: () => void
  onReAnnotate: () => void; reAnnotatePending: boolean; onDelete: () => void; deleting: boolean
}) {
  const title = (doc.data?.title as string) || (doc.data?.text as string)?.slice(0, 50) || doc.doc_key
  const assigneeId = docAssigneeId(doc)
  const reviewerId = docReviewerId(doc)
  const deadline = docDeadline(doc)
  const overdue = !!deadline && new Date(deadline).getTime() < Date.now()
  const canReAnnotate = isCompletedStage(doc.annotation_stage)
  return (
    <tr className="border-b group cursor-pointer" style={{ borderColor: 'var(--border)' }} onClick={onAnnotate}>
      {canSelect && (
        <td className="px-4 py-3" onClick={(e) => e.stopPropagation()}>
          <input type="checkbox" checked={checked} onChange={onToggle} />
        </td>
      )}
      <td className="px-3 py-3">
        <div className="flex items-center gap-2 min-w-0">
          <FileText className="h-4 w-4 shrink-0" style={{ color: 'var(--muted-foreground)' }} />
          <span className="font-mono text-[10px] truncate max-w-[72px] shrink-0" title={doc.doc_key} style={{ color: 'var(--muted-foreground)' }}>{doc.doc_key}</span>
          <span className="min-w-0 truncate">{title}</span>
        </div>
      </td>
      <td className="px-3 py-3 whitespace-nowrap"><StageTag stage={doc.annotation_stage ?? 'not_annotated'} /></td>
      <td className="px-3 py-3 text-xs truncate" style={{ color: 'var(--muted-foreground)' }}>{userName(assigneeId)}</td>
      <td className="px-3 py-3 text-xs truncate" style={{ color: 'var(--muted-foreground)' }}>{userName(reviewerId)}</td>
      <td className="px-3 py-3 text-center font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>{doc.version ?? '—'}</td>
      <td className="px-4 py-3 text-xs" style={{ color: 'var(--muted-foreground)' }}>
        {doc.created_at ? new Date(doc.created_at).toLocaleString('zh-CN') : '—'}
      </td>
      <td className="px-4 py-3 text-xs" style={{ color: 'var(--muted-foreground)' }}>
        {doc.updated_at ? new Date(doc.updated_at).toLocaleString('zh-CN') : '—'}
      </td>
      <td className="px-4 py-3 text-xs font-mono" style={{ color: overdue ? '#ef4444' : 'var(--muted-foreground)' }}>
        {deadline ? (overdue ? '逾期 · ' : '') + new Date(deadline).toLocaleString('zh-CN') : '—'}
      </td>
      <td
        className="sticky right-0 z-10 px-2 py-3 whitespace-nowrap"
        style={{ background: checked ? 'var(--accent)' : 'var(--background)', boxShadow: '-10px 0 16px -16px rgba(0,0,0,.55)' }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex flex-nowrap items-center justify-end gap-1">
          <Button variant="ghost" size="sm" className="h-8 shrink-0 whitespace-nowrap px-2" onClick={onAnnotate}>
            <Pencil className="h-3.5 w-3.5" />标注
          </Button>
          {canAssign && (
            <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0" onClick={onAssign} title="任务管理">
              <UserCog className="h-3.5 w-3.5" />
            </Button>
          )}
          {canReAnnotate && (
            <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0" disabled={reAnnotatePending} onClick={onReAnnotate} title="重新标注">
              <RotateCcw className="h-3.5 w-3.5" />
            </Button>
          )}
          {canDelete && (
            <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0 text-red-600 hover:text-red-700" disabled={deleting} onClick={onDelete} title="删除文档">
              {deleting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
            </Button>
          )}
        </div>
      </td>
    </tr>
  )
}

function isCompletedStage(stage?: string) {
  return stage === 'refined' || stage === 'reviewed'
}
