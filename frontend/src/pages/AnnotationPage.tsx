import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ArrowLeft, Check, X, Plus, ChevronRight, ChevronLeft,
  Sparkles, CheckCircle2, CheckCheck, Loader2, Redo2, Search, Trash2, Undo2,
} from 'lucide-react'
import { documentApi } from '@/api/document'
import { refinementApi, type RefinementState } from '@/api/refinement'
import type { QAPair } from '@/api/document'
import { StageTag } from '@/components/common/StageTag'
import { useResizablePanel, SplitHandle } from '@/components/common/ResizablePanel'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { AutoAnnotateButton } from '@/components/domain/annotation/AutoAnnotateButton'
import { HighlightedDoc } from '@/components/domain/annotation/HighlightedDoc'
import { MultiModelAnnotatePanel } from '@/components/domain/annotation/MultiModelAnnotatePanel'
import { QACard } from '@/components/domain/annotation/QACard'
import { LlmRefineButton } from '@/components/domain/annotation/LlmRefineButton'
import {
  cloneQAPairs,
  findSearchMatches,
  pushHistory,
  readDocumentSelection,
  resolveTextFields,
  type TextSelectionMenu,
} from '@/lib/textAnnotation'

export function AnnotationPage() {
  const { id, key } = useParams<{ id: string; key: string }>()
  const datasetId = Number(id)
  const docKey = key!
  const navigate = useNavigate()
  const qc = useQueryClient()

  const [inspectorOpen, setInspectorOpen] = useState(true)
  const [editingIndex, setEditingIndex] = useState<number | null>(null)
  const [activeQa, setActiveQa] = useState<number | null>(null)
  const [selectionMenu, setSelectionMenu] = useState<TextSelectionMenu | null>(null)
  const [qaSearchMenu, setQaSearchMenu] = useState<TextSelectionMenu | null>(null)
  const [docSearch, setDocSearch] = useState('')
  const [activeSearch, setActiveSearch] = useState(-1)
  const [selectedTextField, setSelectedTextField] = useState<string | null>(null)

  // 可拖拽分隔条：标注员自调「正文 / 问答对」宽度。统一复用 ResizablePanel
  // （与图片/音频工作台同一组件），记忆到 localStorage 'anno.qaWidth'。
  const docArticleRef = useRef<HTMLElement>(null)
  const { width: qaWidth, startDrag, containerRef: splitRef } = useResizablePanel('anno.qaWidth', { initial: 420, min: 300, leftMin: 320 })

  // 正文高亮 ↔ QA 卡片 双向联动：点击一侧滚动并高亮另一侧
  const selectQa = (i: number, from: 'doc' | 'card') => {
    setActiveQa(i)
    const targetId = from === 'doc' ? `qacard-${i}` : `docspan-${i}`
    setTimeout(() => document.getElementById(targetId)?.scrollIntoView({ block: 'center', behavior: 'smooth' }), 0)
  }

  // 文档正文
  const { data: doc, isLoading: docLoading } = useQuery({
    queryKey: ['document', docKey, datasetId],
    queryFn: () => documentApi.get(docKey, datasetId),
  })

  // 精标会话（QA 对 + etag + cursor）
  const { data: refinement, isLoading: refLoading } = useQuery({
    queryKey: ['refinement', docKey, datasetId],
    queryFn: () => refinementApi.start(docKey, datasetId),
  })

  const [qaPairs, setQaPairs] = useState<QAPair[]>([])
  const [etag, setEtag] = useState('')
  const [cursor, setCursor] = useState(0)
  const [jumpInput, setJumpInput] = useState('')
  const [undoStack, setUndoStack] = useState<QAPair[][]>([])
  const [redoStack, setRedoStack] = useState<QAPair[][]>([])

  useEffect(() => {
    if (refinement) {
      setQaPairs(refinement.qa_pairs ?? [])
      setEtag(refinement.etag)
      setCursor(refinement.cursor ?? 0)
      setActiveQa(refinement.cursor ?? 0)
    }
  }, [refinement])

  useEffect(() => {
    setUndoStack([])
    setRedoStack([])
  }, [datasetId, docKey])

  const textFields = useMemo(() => resolveTextFields(doc?.data), [doc?.data])
  const docTextField = selectedTextField && textFields.some((f) => f.field === selectedTextField)
    ? selectedTextField
    : textFields[0]?.field ?? 'text'
  const docText = textFields.find((f) => f.field === docTextField)?.text ?? ''
  const docTitle = (doc?.data?.title as string) || docKey
  const searchMatches = useMemo(() => findSearchMatches(docText, docSearch), [docText, docSearch])
  const unconfirmedCount = useMemo(() => qaPairs.filter((p) => !p.confirmed).length, [qaPairs])

  useEffect(() => {
    if (selectedTextField && textFields.some((f) => f.field === selectedTextField)) return
    setSelectedTextField(textFields[0]?.field ?? null)
  }, [selectedTextField, textFields])

  useEffect(() => {
    if (!docSearch.trim() || searchMatches.length === 0) {
      setActiveSearch(-1)
      return
    }
    setActiveSearch((prev) => (prev >= 0 && prev < searchMatches.length ? prev : 0))
  }, [docSearch, searchMatches.length])

  const applyState = (s: RefinementState) => {
    setQaPairs(s.qa_pairs ?? [])
    setEtag(s.etag)
    setCursor(s.cursor ?? 0)
  }

  const rememberQASnapshot = useCallback(() => {
    const snapshot = cloneQAPairs(qaPairs)
    setUndoStack((prev) => pushHistory(prev, snapshot))
    setRedoStack([])
  }, [qaPairs])

  useEffect(() => {
    setJumpInput(qaPairs.length > 0 ? String(Math.min(cursor + 1, qaPairs.length)) : '')
  }, [cursor, qaPairs.length])

  const clearSelectionMenu = useCallback(() => {
    setSelectionMenu(null)
    setQaSearchMenu(null)
    window.getSelection()?.removeAllRanges()
  }, [])

  const handleDocumentSelection = useCallback(() => {
    window.setTimeout(() => {
      const container = docArticleRef.current
      if (!container || !docText) return
      const next = readDocumentSelection(container)
      setSelectionMenu(next)
    }, 0)
  }, [docText])

  const editMut = useMutation({
    mutationFn: ({ index, pair }: { index: number; pair: Partial<QAPair> }) =>
      refinementApi.editQAPair(docKey, index, pair, etag, datasetId),
    onSuccess: (s) => { applyState(s); setEditingIndex(null) },
  })

  const deleteMut = useMutation({
    mutationFn: (index: number) => refinementApi.deleteQAPair(docKey, index, etag, datasetId),
    onSuccess: applyState,
  })

  const cursorMut = useMutation({
    mutationFn: ({ action, index }: { action: 'prev' | 'next' | 'jump'; index?: number }) =>
      refinementApi.navigateCursor(docKey, action, etag, index, datasetId),
    onSuccess: (s) => {
      applyState(s)
      setActiveQa(s.cursor)
      setEditingIndex(null)
      setTimeout(() => {
        document.getElementById(`qacard-${s.cursor}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
        document.getElementById(`docspan-${s.cursor}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
      }, 0)
    },
  })

  const addMut = useMutation({
    mutationFn: (pair: Partial<QAPair>) => refinementApi.addQAPair(docKey, pair, etag, datasetId),
    onSuccess: (s) => {
      applyState(s)
      const nextIndex = (s.qa_pairs ?? []).length - 1
      if (nextIndex >= 0) {
        setActiveQa(nextIndex)
        setEditingIndex(nextIndex)
      }
    },
  })

  const addSelectionQa = () => {
    if (!selectionMenu) return
    rememberQASnapshot()
    addMut.mutate({
      question: '',
      answer: '',
      source: 'manual',
      confirmed: false,
      span_text: selectionMenu.text,
      span_start: selectionMenu.start,
      span_end: selectionMenu.end,
      text_field: docTextField,
    })
    clearSelectionMenu()
  }

  const jumpSearch = (direction: 1 | -1) => {
    if (searchMatches.length === 0) return
    const next = activeSearch < 0
      ? 0
      : (activeSearch + direction + searchMatches.length) % searchMatches.length
    setActiveSearch(next)
    setTimeout(() => document.getElementById(`searchmatch-${next}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }), 0)
  }

  const searchFromQaText = (text: string) => {
    const query = text.trim()
    if (!query) return
    setDocSearch(query)
    setActiveSearch(0)
    setQaSearchMenu(null)
    window.getSelection()?.removeAllRanges()
    setTimeout(() => document.getElementById('searchmatch-0')?.scrollIntoView({ block: 'center', behavior: 'smooth' }), 0)
  }

  const jumpCursor = () => {
    const next = Number(jumpInput)
    if (!Number.isFinite(next) || next < 1 || next > qaPairs.length) return
    cursorMut.mutate({ action: 'jump', index: next - 1 })
  }

  useEffect(() => {
    if (activeSearch >= 0) {
      setTimeout(() => document.getElementById(`searchmatch-${activeSearch}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' }), 0)
    }
  }, [activeSearch])

  // 确认状态切换：editQAPair 不处理 confirmed（后端由游标导航控制），故用 bulkUpdate 整体提交
  const confirmMut = useMutation({
    mutationFn: (index: number) => {
      const next = qaPairs.map((p, i) => (i === index ? { ...p, confirmed: !p.confirmed } : p))
      return refinementApi.bulkUpdate(docKey, next, etag, datasetId)
    },
    onSuccess: applyState,
  })

  const batchQaMut = useMutation({
    mutationFn: (action: 'confirm_all' | 'clear_unconfirmed') => {
      const next = action === 'confirm_all'
        ? qaPairs.map((p) => ({ ...p, confirmed: true }))
        : qaPairs.filter((p) => p.confirmed)
      return refinementApi.bulkUpdate(docKey, next, etag, datasetId)
    },
    onSuccess: (s) => {
      applyState(s)
      setEditingIndex(null)
      const nextPairs = s.qa_pairs ?? []
      setActiveQa(nextPairs.length > 0 ? Math.min(s.cursor ?? 0, nextPairs.length - 1) : null)
    },
  })

  const restoreHistoryMut = useMutation({
    mutationFn: ({ pairs }: { pairs: QAPair[]; direction: 'undo' | 'redo'; currentPairs: QAPair[] }) =>
      refinementApi.bulkUpdate(docKey, pairs, etag, datasetId),
    onSuccess: (s) => {
      applyState(s)
      setEditingIndex(null)
      const nextPairs = s.qa_pairs ?? []
      setActiveQa(nextPairs.length > 0 ? Math.min(s.cursor ?? 0, nextPairs.length - 1) : null)
    },
    onError: (_e, vars) => {
      if (vars.direction === 'undo') {
        setUndoStack((prev) => pushHistory(prev, vars.pairs))
        setRedoStack((prev) => prev.slice(0, -1))
      } else {
        setRedoStack((prev) => pushHistory(prev, vars.pairs))
        setUndoStack((prev) => prev.slice(0, -1))
      }
    },
  })

  const qaWriteBusy = editMut.isPending
    || deleteMut.isPending
    || confirmMut.isPending
    || cursorMut.isPending
    || addMut.isPending
    || batchQaMut.isPending
    || restoreHistoryMut.isPending

  const confirmAllQAPairs = () => {
    if (unconfirmedCount === 0) return
    if (!window.confirm(`将 ${unconfirmedCount} 条未确认 QA 标记为已确认？`)) return
    rememberQASnapshot()
    batchQaMut.mutate('confirm_all')
  }

  const clearUnconfirmedQAPairs = () => {
    if (unconfirmedCount === 0) return
    const kept = qaPairs.length - unconfirmedCount
    if (!window.confirm(`将删除 ${unconfirmedCount} 条未确认 QA，仅保留 ${kept} 条已确认 QA。确定继续？`)) return
    rememberQASnapshot()
    batchQaMut.mutate('clear_unconfirmed')
  }

  const undoQAPairs = useCallback(() => {
    if (qaWriteBusy || undoStack.length === 0) return
    const pairs = undoStack[undoStack.length - 1]
    const currentPairs = cloneQAPairs(qaPairs)
    setUndoStack((prev) => prev.slice(0, -1))
    setRedoStack((prev) => pushHistory(prev, currentPairs))
    restoreHistoryMut.mutate({ pairs, direction: 'undo', currentPairs })
  }, [qaPairs, qaWriteBusy, restoreHistoryMut, undoStack])

  const redoQAPairs = useCallback(() => {
    if (qaWriteBusy || redoStack.length === 0) return
    const pairs = redoStack[redoStack.length - 1]
    const currentPairs = cloneQAPairs(qaPairs)
    setRedoStack((prev) => prev.slice(0, -1))
    setUndoStack((prev) => pushHistory(prev, currentPairs))
    restoreHistoryMut.mutate({ pairs, direction: 'redo', currentPairs })
  }, [qaPairs, qaWriteBusy, redoStack, restoreHistoryMut])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      const tagName = target?.tagName
      const isEditable = target?.isContentEditable || tagName === 'INPUT' || tagName === 'TEXTAREA' || tagName === 'SELECT'
      if (isEditable || !(event.ctrlKey || event.metaKey)) return
      const keyName = event.key.toLowerCase()
      if (keyName === 'z' && !event.shiftKey) {
        event.preventDefault()
        undoQAPairs()
      } else if (keyName === 'y' || (keyName === 'z' && event.shiftKey)) {
        event.preventDefault()
        redoQAPairs()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [redoQAPairs, undoQAPairs])

  const completeMut = useMutation({
    mutationFn: () => refinementApi.complete(docKey, etag, datasetId),
    onSuccess: (s) => {
      applyState(s)
      qc.invalidateQueries({ queryKey: ['documents', datasetId] })
      qc.invalidateQueries({ queryKey: ['document', docKey, datasetId] })
      navigate(`/datasets/${datasetId}/documents`)
    },
  })

  const directCompleteMut = useMutation({
    mutationFn: () => documentApi.directComplete(docKey, datasetId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['documents', datasetId] })
      qc.invalidateQueries({ queryKey: ['document', docKey, datasetId] })
      qc.invalidateQueries({ queryKey: ['refinement', docKey, datasetId] })
      navigate(`/datasets/${datasetId}/documents`)
    },
  })

  const completeDisabled = qaWriteBusy || editingIndex !== null || completeMut.isPending || directCompleteMut.isPending

  const directComplete = () => {
    if (completeDisabled) return
    if (!window.confirm('直接完成会将当前样本置为已精标并返回列表。确定继续？')) return
    directCompleteMut.mutate()
  }

  if (docLoading || refLoading) {
    return (
      <div className="flex flex-1 items-center justify-center">
        <Loader2 className="h-5 w-5 animate-spin" style={{ color: 'var(--muted-foreground)' }} />
      </div>
    )
  }

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      {/* 顶栏 */}
      <div className="flex h-12 shrink-0 items-center gap-3 border-b px-4 text-sm" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
        <Button variant="ghost" size="sm" onClick={() => navigate(`/datasets/${datasetId}/documents`)}>
          <ArrowLeft className="h-3.5 w-3.5" />返回
        </Button>
        <span className="font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>{docKey}</span>
        <span className="font-medium truncate max-w-md">{docTitle}</span>
        <StageTag stage={doc?.annotation_stage ?? 'not_annotated'} />
        <div className="ml-auto flex items-center gap-2">
          <AutoAnnotateButton
            datasetId={datasetId}
            docKey={docKey}
            onCompleted={() => {
              qc.invalidateQueries({ queryKey: ['refinement', docKey, datasetId] })
              qc.invalidateQueries({ queryKey: ['document', docKey, datasetId] })
            }}
          />
          <MultiModelAnnotatePanel
            datasetId={datasetId}
            docKey={docKey}
            etag={etag}
            textField={docTextField}
            onAdopted={(s) => {
              rememberQASnapshot()
              applyState(s)
              qc.invalidateQueries({ queryKey: ['refinement', docKey, datasetId] })
              qc.invalidateQueries({ queryKey: ['document', docKey, datasetId] })
            }}
          />
          <LlmRefineButton
            datasetId={datasetId}
            docKey={docKey}
            enabled={doc?.llm_refinement_enabled}
            score={doc?.llm_refinement_score}
            reasoning={doc?.llm_refinement_reasoning}
            version={doc?.llm_refinement_version}
          />
          <Button variant="outline" size="sm" onClick={() => setInspectorOpen((v) => !v)}>
            {inspectorOpen ? '隐藏' : '显示'} QA 面板
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={completeDisabled}
            title={editingIndex !== null ? '请先保存或取消当前正在编辑的 QA' : '无需逐条精标时直接置为已精标'}
            onClick={directComplete}
          >
            <CheckCheck className="h-3.5 w-3.5" />
            {directCompleteMut.isPending ? '提交中...' : '直接完成'}
          </Button>
          <Button
            size="sm"
            disabled={completeDisabled}
            title={editingIndex !== null ? '请先保存或取消当前正在编辑的 QA' : '完成当前精标会话'}
            onClick={() => completeMut.mutate()}
          >
            <CheckCircle2 className="h-3.5 w-3.5" />
            {completeMut.isPending ? '提交中...' : '完成精标'}
          </Button>
        </div>
      </div>

      {selectionMenu && (
        <div
          className="fixed z-40 flex items-center gap-1 rounded-md border px-1.5 py-1 shadow-lg"
          style={{
            left: selectionMenu.x,
            top: selectionMenu.y,
            transform: 'translate(-50%, -100%)',
            background: 'var(--popover)',
            borderColor: 'var(--border)',
          }}
        >
          <Button
            size="sm"
            variant="ghost"
            disabled={addMut.isPending}
            onMouseDown={(e) => e.preventDefault()}
            onClick={addSelectionQa}
          >
            <Plus className="h-3.5 w-3.5" />
            新增 QA
          </Button>
          <Button
            size="icon"
            variant="ghost"
            onMouseDown={(e) => e.preventDefault()}
            onClick={clearSelectionMenu}
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      )}

      {qaSearchMenu && (
        <div
          className="fixed z-40 flex items-center gap-1 rounded-md border px-1.5 py-1 shadow-lg"
          style={{
            left: qaSearchMenu.x,
            top: qaSearchMenu.y,
            transform: 'translate(-50%, -100%)',
            background: 'var(--popover)',
            borderColor: 'var(--border)',
          }}
        >
          <Button
            variant="ghost"
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => searchFromQaText(qaSearchMenu.text)}
          >
            <Search className="h-3.5 w-3.5" />
            搜索原文
          </Button>
          <Button
            size="icon"
            variant="ghost"
            onMouseDown={(e) => e.preventDefault()}
            onClick={clearSelectionMenu}
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      )}

      <div ref={splitRef} className="flex min-h-0 flex-1">
        {/* 中心：文档正文 */}
        <div className="flex flex-1 flex-col min-w-0 overflow-auto" style={{ background: 'var(--background)' }}>
          <div className="sticky top-0 z-20 flex shrink-0 items-center gap-2 border-b px-4 py-2" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
            <div className="relative w-full max-w-sm">
              <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2" style={{ color: 'var(--muted-foreground)' }} />
              <input
                value={docSearch}
                onChange={(e) => setDocSearch(e.target.value)}
                placeholder="搜索正文..."
                className="h-8 w-full rounded-md border pl-9 pr-8 text-sm outline-none"
                style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
              />
              {docSearch && (
                <button
                  className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 opacity-70 hover:opacity-100"
                  onClick={() => setDocSearch('')}
                  title="清除"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
            <span className="min-w-[72px] text-xs tabular-nums" style={{ color: 'var(--muted-foreground)' }}>
              {searchMatches.length > 0 && activeSearch >= 0 ? `${activeSearch + 1}/${searchMatches.length}` : '0/0'}
            </span>
            <Button variant="outline" size="icon" disabled={searchMatches.length === 0} onClick={() => jumpSearch(-1)}>
              <ChevronLeft className="h-3.5 w-3.5" />
            </Button>
            <Button variant="outline" size="icon" disabled={searchMatches.length === 0} onClick={() => jumpSearch(1)}>
              <ChevronRight className="h-3.5 w-3.5" />
            </Button>
            {textFields.length > 1 && (
              <div
                className="ml-auto flex max-w-[46%] shrink-0 items-center gap-0.5 overflow-x-auto rounded-md border p-0.5"
                style={{ borderColor: 'var(--border)', background: 'var(--background)' }}
              >
                {textFields.map((field) => (
                  <Button
                    key={field.field}
                    variant={docTextField === field.field ? 'default' : 'ghost'}
                    size="sm"
                    className="h-7 shrink-0 px-2 text-xs"
                    title={`${field.field} · ${field.text.length} 字`}
                    onClick={() => setSelectedTextField(field.field)}
                  >
                    {field.label}
                  </Button>
                ))}
              </div>
            )}
          </div>
          <article
            ref={docArticleRef}
            onPointerDown={() => setSelectionMenu(null)}
            onPointerUp={handleDocumentSelection}
            onKeyUp={handleDocumentSelection}
            className="mx-auto max-w-3xl px-8 py-8 text-[15px] leading-relaxed whitespace-pre-wrap"
          >
            <HighlightedDoc
              text={docText}
              textField={docTextField}
              qaPairs={qaPairs}
              activeQa={activeQa}
              searchMatches={searchMatches}
              activeSearch={activeSearch}
              onSelect={(i) => selectQa(i, 'doc')}
            />
          </article>
        </div>

        {/* 可拖拽分隔条：调节正文 / 问答对宽度（共用 ResizablePanel） */}
        {inspectorOpen && <SplitHandle onPointerDown={startDrag} title="拖动调节正文 / 问答对宽度" />}

        {/* 右侧：QA Inspector */}
        {inspectorOpen && (
          <div className="flex shrink-0 flex-col" style={{ width: qaWidth, borderColor: 'var(--border)', background: 'var(--card)' }}>
            <div className="flex items-center justify-between border-b px-4 py-2.5" style={{ borderColor: 'var(--border)' }}>
              <div className="flex items-center gap-2">
                <span className="text-sm font-semibold">问答对</span>
                <Badge variant="secondary">{qaPairs.length}</Badge>
              </div>
              <Button variant="ghost" size="sm"
                onClick={() => {
                  rememberQASnapshot()
                  addMut.mutate({ question: '', answer: '', source: 'manual', confirmed: false })
                }}
                disabled={qaWriteBusy}>
                <Plus className="h-3.5 w-3.5" />新增
              </Button>
            </div>
            {qaPairs.length > 0 && (
              <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2" style={{ borderColor: 'var(--border)' }}>
                <Button
                  variant="outline"
                  size="icon"
                  title="撤销 Ctrl+Z"
                  disabled={undoStack.length === 0 || qaWriteBusy}
                  onClick={undoQAPairs}
                >
                  <Undo2 className="h-3.5 w-3.5" />
                </Button>
                <Button
                  variant="outline"
                  size="icon"
                  title="重做 Ctrl+Y"
                  disabled={redoStack.length === 0 || qaWriteBusy}
                  onClick={redoQAPairs}
                >
                  <Redo2 className="h-3.5 w-3.5" />
                </Button>
                <Button
                  variant="outline"
                  size="icon"
                  title="上一条"
                  disabled={cursor <= 0 || qaWriteBusy}
                  onClick={() => cursorMut.mutate({ action: 'prev' })}
                >
                  <ChevronLeft className="h-3.5 w-3.5" />
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={qaWriteBusy}
                  onClick={() => {
                    rememberQASnapshot()
                    cursorMut.mutate({ action: 'next' })
                  }}
                >
                  <Check className="h-3.5 w-3.5" />
                  确认/下一条
                </Button>
                <span className="min-w-[52px] text-center text-xs tabular-nums" style={{ color: 'var(--muted-foreground)' }}>
                  {Math.min(cursor + 1, qaPairs.length)}/{qaPairs.length}
                </span>
                <input
                  type="number"
                  min={1}
                  max={qaPairs.length}
                  value={jumpInput}
                  onChange={(e) => setJumpInput(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') jumpCursor() }}
                  className="h-8 w-16 rounded-md border px-2 text-sm outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)', color: 'var(--foreground)' }}
                />
                <Button
                  variant="outline"
                  size="sm"
                  disabled={qaWriteBusy}
                  onClick={jumpCursor}
                >
                  跳转
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={unconfirmedCount === 0 || qaWriteBusy}
                  title={`确认 ${unconfirmedCount} 条未确认 QA`}
                  onClick={confirmAllQAPairs}
                >
                  <CheckCheck className="h-3.5 w-3.5" />
                  全部确认
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={unconfirmedCount === 0 || qaWriteBusy}
                  title={`删除 ${unconfirmedCount} 条未确认 QA`}
                  onClick={clearUnconfirmedQAPairs}
                >
                  <Trash2 className="h-3.5 w-3.5 text-red-500" />
                  清空未确认
                </Button>
              </div>
            )}

            <div className="flex-1 overflow-auto p-3 space-y-2">
              {qaPairs.length === 0 ? (
                <p className="text-center text-sm py-8" style={{ color: 'var(--muted-foreground)' }}>
                  暂无问答对，点击「新增」添加
                </p>
              ) : qaPairs.map((qa, i) => (
                <QACard
                  key={i}
                  index={i}
                  qa={qa}
                  editing={editingIndex === i}
                  active={activeQa === i}
                  current={cursor === i}
                  busy={qaWriteBusy}
                  onSelect={() => selectQa(i, 'card')}
                  onEdit={() => setEditingIndex(i)}
                  onCancelEdit={() => setEditingIndex(null)}
                  onSave={(pair) => {
                    rememberQASnapshot()
                    editMut.mutate({ index: i, pair })
                  }}
                  onConfirm={() => {
                    rememberQASnapshot()
                    confirmMut.mutate(i)
                  }}
                  onDelete={() => {
                    rememberQASnapshot()
                    deleteMut.mutate(i)
                  }}
                  onSearchSelection={(menu) => setQaSearchMenu(menu)}
                />
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
