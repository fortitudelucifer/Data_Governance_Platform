import React, { useState, useEffect } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { workbenchBackTo } from '@/lib/navBack'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ArrowLeft, ChevronLeft, ChevronRight,
  Trash2, Check, X, Loader2, Sparkles, RefreshCw, Type,
} from 'lucide-react'
import {
  taskApi, capabilityApi, TASK_STATE_LABELS, TASK_STATE_COLOR,
  VLM_MODELS, ADHOC_ALLOWED_STATES, REROUTE_ALLOWED_STATES,
  type Shape,
} from '@/api/imageTask'
import { assetApi } from '@/api/asset'
import { datasetApi } from '@/api/dataset'
import { InteractiveCanvas } from '@/components/domain/image-annotation/InteractiveCanvas'
import { useResizablePanel, SplitHandle } from '@/components/common/ResizablePanel'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useAuthStore } from '@/stores/auth'
import * as perms from '@/lib/roles'

const CHART_COLORS = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)']

type Tab = 'annotations' | 'ai' | 'routing' | 'trace' | 'review'

// 工作台 AI 面板按后端 /capabilities 动态渲染。已知能力给中文名 + 是否需要 VLM 模型，
// 未知能力（如以后新增的检测适配器）回退显示原始 capability_type。
const CAP_META: Record<string, { label: string; vlm?: boolean; engine?: string }> = {
  'ocr.structure': { label: 'OCR 结构化', engine: 'PaddleOCR' },
  'seg.instance': { label: '实例分割 (YOLO)', engine: 'YOLOv8-seg' },
  'vlm.caption': { label: 'VLM 描述', vlm: true },
  'vlm.structured_extract': { label: 'VLM 抽取', vlm: true },
  'vlm.grounding': { label: 'VLM 定位', vlm: true },
}
// 不作为工作台「补跑按钮」出现：seg.interactive=画布 SAM 工具，text.chat=纯文本
const NON_INVOKE_CAPS = new Set(['seg.interactive', 'text.chat'])
const capLabel = (c: string) => CAP_META[c]?.label ?? c
const STATUS_ZH: Record<string, string> = { success: '成功', failed: '失败', timeout: '超时', pending: '进行中' }

// 仅这些任务状态接受人工编辑（与后端 canEditHuman 一致）；其它状态画布只读
const EDITABLE_STATES = new Set(['HUMAN_PENDING', 'HUMAN_IN_PROGRESS', 'QA_REJECTED'])
// AI 建议层颜色（与画布一致）
const AI_COLOR = '#22d3ee'

export function ImageAnnotationPage() {
  const { id } = useParams<{ id: string }>()
  const taskId = Number(id)
  const navigate = useNavigate()
  const location = useLocation()
  const qc = useQueryClient()
  const user = useAuthStore((s) => s.user)
  // 角色能力走 R-01 能力层（此前这里内联了角色数组，且漏了 audio/video_* ——
  // 与后端 rolesAnnotate/rolesReview 真值不符，正是能力层要消灭的那类漂移）。
  const role = user?.role ?? ''
  const canAnnotate = perms.canAnnotate(role)
  const canReview = perms.canReview(role)
  // 可拖拽分隔条：标注员自选「画布 ↔ Inspector」左右比例。
  const { width: panelWidth, startDrag, containerRef } = useResizablePanel('image.panelWidth', { initial: 340 })

  const [tab, setTab] = useState<Tab>('annotations')
  const [reviewNote, setReviewNote] = useState('')
  const [vlmModel, setVlmModel] = useState(VLM_MODELS[0].value)
  const [invokeMsg, setInvokeMsg] = useState<{ kind: 'ok' | 'warn' | 'err'; text: string } | null>(null)
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const [textDraft, setTextDraft] = useState('')
  const [labelDraft, setLabelDraft] = useState('')
  const [colorDraft, setColorDraft] = useState('')
  const [savedFlash, setSavedFlash] = useState(false)
  const [saveErr, setSaveErr] = useState('')
  const [batchLabel, setBatchLabel] = useState('')
  const [selectedAiId, setSelectedAiId] = useState<string | null>(null)
  const [descDraft, setDescDraft] = useState('')
  const [descSavedFlash, setDescSavedFlash] = useState(false)

  const { data: task, isLoading: taskLoading } = useQuery({
    queryKey: ['task', taskId],
    queryFn: () => taskApi.get(taskId),
  })
  const { data: asset } = useQuery({
    queryKey: ['asset-detail', task?.asset_id],
    queryFn: () => assetApi.detail(task!.asset_id),
    enabled: !!task?.asset_id,
  })
  const { data: humanAnn } = useQuery({
    queryKey: ['human-annotation', taskId],
    queryFn: () => taskApi.getHumanAnnotation(taskId),
  })
  const { data: aiResults } = useQuery({
    queryKey: ['ai-results', taskId],
    queryFn: () => taskApi.getAIResults(taskId),
  })
  const { data: adjacent } = useQuery({
    queryKey: ['adjacent', taskId],
    queryFn: () => taskApi.getAdjacent(taskId, true),
  })
  // 顶栏「返回」：回到来处，否则回上一级（本任务所属数据集的资产列表）。
  const backTo = workbenchBackTo(location.state, task?.dataset_id)
  const { data: caps = [] } = useQuery({
    queryKey: ['capabilities'],
    queryFn: () => capabilityApi.list(),
    staleTime: 5 * 60 * 1000,
  })
  // 统一「能力→模型」：VLM 模型下拉改由 /capabilities/models 驱动（env + 能力配置），
  // 不再硬编码 VLM_MODELS；空时回退到 VLM_MODELS。
  const { data: capModels = [] } = useQuery({
    queryKey: ['cap-models'],
    queryFn: () => capabilityApi.models(),
    staleTime: 5 * 60 * 1000,
  })
  const { data: labelDefs = [] } = useQuery({
    queryKey: ['label-config', task?.dataset_id],
    queryFn: () => datasetApi.getLabelConfig(task!.dataset_id),
    enabled: !!task?.dataset_id,
    staleTime: 5 * 60 * 1000,
  })
  const { data: routing } = useQuery({
    queryKey: ['routing', taskId],
    queryFn: () => taskApi.getRouting(taskId),
    enabled: tab === 'routing',
  })
  const { data: trace } = useQuery({
    queryKey: ['trace', taskId],
    queryFn: () => taskApi.getTrace(taskId),
    enabled: tab === 'trace',
  })

  const shapes = humanAnn?.shapes ?? []
  const selected = selectedIds.length === 1 ? shapes.find((s) => s.id === selectedIds[0]) ?? null : null
  const selectedIdx = selected ? shapes.findIndex((s) => s.id === selected.id) : -1
  const allSelected = shapes.length > 0 && selectedIds.length === shapes.length

  // 选中变化时同步编辑草稿（文本 / 标签 / 颜色）
  useEffect(() => {
    setTextDraft((selected?.attrs?.text as string) ?? '')
    setLabelDraft(selected?.label ?? '')
    setColorDraft(selected?.color ?? '')
    setSavedFlash(false)
  }, [selected?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  // 同步图片级描述草稿（来自人工标注的 fields.caption）
  useEffect(() => {
    setDescDraft((humanAnn?.fields?.caption as string) ?? '')
  }, [humanAnn?.fields?.caption])

  const colorForLabel = (label?: string) => labelDefs.find((l) => l.name === label)?.color ?? ''
  const toggleSelect = (id: string) =>
    setSelectedIds((ids) => (ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id]))
  const toggleAll = () => setSelectedIds(allSelected ? [] : shapes.map((s) => s.id))

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['human-annotation', taskId] })
    qc.invalidateQueries({ queryKey: ['task', taskId] })
  }

  // 保存 shapes（采纳 AI / 删除标注 共用）。务必带上当前 fields/texts，否则后端整体替换会清空图片描述等。
  const saveShapesMut = useMutation({
    mutationFn: (newShapes: Shape[]) => taskApi.putHumanAnnotation(taskId, { shapes: newShapes, fields: humanAnn?.fields, texts: humanAnn?.texts }),
    onSuccess: () => { setSaveErr(''); refresh() },
    onError: (e: any) => setSaveErr(e?.response?.data?.error || e?.message || '保存失败'),
  })

  // 保存图片级描述（VLM 采纳 / 手动编辑）：连同当前 shapes/texts 一起发，避免被整体替换清空。
  const saveDescMut = useMutation({
    mutationFn: (v: { caption: string; tags?: string[] }) =>
      taskApi.putHumanAnnotation(taskId, {
        shapes,
        texts: humanAnn?.texts,
        fields: { ...(humanAnn?.fields ?? {}), caption: v.caption, ...(v.tags ? { tags: v.tags } : {}) },
      }),
    onSuccess: () => { setSaveErr(''); refresh() },
    onError: (e: any) => setSaveErr(e?.response?.data?.error || e?.message || '保存失败'),
  })

  // 主动补跑 AI 能力
  const invokeMut = useMutation({
    mutationFn: (v: { capability: string; model?: string }) => taskApi.invoke(taskId, v.capability, v.model),
    onSuccess: (res, v) => {
      const label = capLabel(v.capability)
      if (res.status === 'success') {
        setInvokeMsg({ kind: 'ok', text: `补跑「${label}」成功 · ${res.latency_ms ?? '?'}ms` })
      } else {
        setInvokeMsg({ kind: 'warn', text: `补跑「${label}」返回 ${res.status}：${res.error || '(无详情)'}` })
      }
      qc.invalidateQueries({ queryKey: ['ai-results', taskId] })
      qc.invalidateQueries({ queryKey: ['task', taskId] })
      qc.invalidateQueries({ queryKey: ['trace', taskId] })
      qc.invalidateQueries({ queryKey: ['routing', taskId] })
    },
    onError: (e: any, v) => {
      const label = capLabel(v.capability)
      setInvokeMsg({ kind: 'err', text: `补跑「${label}」失败：${e?.response?.data?.error || e?.message || String(e)}` })
    },
  })

  // 重新路由（仅终态可用）
  const reprocessMut = useMutation({
    mutationFn: () => taskApi.reprocess(taskId),
    onSuccess: () => {
      setInvokeMsg({ kind: 'ok', text: '已重新路由：任务回到 ROUTING，AIWorker 将按当前探针 + 阈值重跑' })
      qc.invalidateQueries({ queryKey: ['task', taskId] })
      qc.invalidateQueries({ queryKey: ['routing', taskId] })
      qc.invalidateQueries({ queryKey: ['trace', taskId] })
      qc.invalidateQueries({ queryKey: ['ai-results', taskId] })
    },
    onError: (e: any) => setInvokeMsg({ kind: 'err', text: `重新路由失败：${e?.response?.data?.error || e?.message || String(e)}` }),
  })

  const submitMut = useMutation({
    mutationFn: () => taskApi.submit(taskId),
    onSuccess: () => {
      refresh()
      if (adjacent?.next_task_id) navigate(`/image-tasks/${adjacent.next_task_id}`, { state: location.state })
      else navigate(backTo)
    },
  })

  const qaMut = useMutation({
    mutationFn: (pass: boolean) => pass ? taskApi.qaPass(taskId, reviewNote) : taskApi.qaReject(taskId, reviewNote),
    onSuccess: () => {
      refresh()
      if (adjacent?.next_task_id) navigate(`/image-tasks/${adjacent.next_task_id}`, { state: location.state })
      else navigate(backTo)
    },
  })

  const deleteShape = (idx: number) => {
    const id = shapes[idx]?.id
    setSelectedIds((ids) => ids.filter((x) => x !== id))
    saveShapesMut.mutate(shapes.filter((_, i) => i !== idx))
  }

  // 删除当前多选的所有 shapes
  const deleteSelected = () => {
    const del = new Set(selectedIds)
    setSelectedIds([])
    saveShapesMut.mutate(shapes.filter((s) => !del.has(s.id)))
  }

  // 批量给多选 shapes 应用标签
  const applyBatchLabel = (lbl: string) => {
    const set = new Set(selectedIds)
    saveShapesMut.mutate(shapes.map((s) => (set.has(s.id) ? { ...s, label: lbl } : s)))
  }

  // 编辑选中 shape 的字段（label / attrs.text 等）并持久化
  const commitShapeEdit = (idx: number, patch: Partial<Shape> & { attrs?: Record<string, any> }) => {
    const next = shapes.map((s, i) =>
      i === idx ? { ...s, ...patch, attrs: { ...s.attrs, ...(patch.attrs ?? {}) } } : s
    )
    saveShapesMut.mutate(next)
  }

  // 显式保存当前选中标注（标签 / 文本 / 颜色）+ 「已保存」反馈（傻瓜化）
  const saveSelected = () => {
    if (selectedIdx < 0) return
    commitShapeEdit(selectedIdx, { label: labelDraft.trim(), color: colorDraft || undefined, attrs: { text: textDraft } })
    setSavedFlash(true)
    setTimeout(() => setSavedFlash(false), 1500)
  }

  // 保存图片级描述（caption + tags）+ 「已保存」反馈
  const saveDesc = (caption: string, tags?: string[]) => {
    saveDescMut.mutate({ caption: caption.trim(), tags })
    setDescSavedFlash(true)
    setTimeout(() => setDescSavedFlash(false), 1500)
  }

  if (taskLoading) {
    return <div className="flex flex-1 items-center justify-center"><Loader2 className="h-5 w-5 animate-spin" style={{ color: 'var(--muted-foreground)' }} /></div>
  }

  const imgW = asset?.width || 1000
  const imgH = asset?.height || 750
  const ocrBoxes = aiResults?.ocr?.boxes ?? []
  const segPolys = aiResults?.seg?.polygons ?? []
  const aiCount = ocrBoxes.length + segPolys.length
  // AI 建议形状（叠加到画布的青色虚线层）：YOLO 分割多边形 + OCR 框
  const aiShapes: Shape[] = [
    ...segPolys.map((p, i) => ({ id: `ai-seg-${i}`, kind: 'polygon', label: p.class_name, points: p.points, source: 'ai_seg', confidence: p.score })),
    ...ocrBoxes.map((b, i) => ({ id: `ai-ocr-${i}`, kind: 'bbox', label: b.text || 'OCR', points: [[b.x, b.y], [b.x + b.w, b.y + b.h]], source: 'ai_ocr', confidence: b.confidence })),
  ]
  // 画布上点 AI 虚线框 → 采纳为一条标注
  const adoptAiShape = (s: Shape) => saveShapesMut.mutate([...shapes, { ...s, id: `${s.source || 'ai'}-${Date.now()}` }])
  // 一键把所有 AI 建议采纳到画布
  const adoptAllAi = () => {
    if (!aiShapes.length) return
    const stamp = Date.now()
    saveShapesMut.mutate([...shapes, ...aiShapes.map((s, i) => ({ ...s, id: `${s.source || 'ai'}-${stamp}-${i}` }))])
  }
  // 手动添加一个文本标注：在图片中心放一个小框并选中，便于立即输入文字 / 调整位置
  const addTextShape = () => {
    const id = `text-${Date.now()}`
    const w = Math.max(48, Math.round(imgW * 0.12)), h = Math.max(28, Math.round(imgH * 0.05))
    const cx = Math.round(imgW / 2), cy = Math.round(imgH / 2)
    const s: Shape = { id, kind: 'bbox', label: '文本', source: 'manual', attrs: { text: '' }, points: [[cx - w / 2, cy - h / 2], [cx + w / 2, cy + h / 2]] }
    saveShapesMut.mutate([...shapes, s])
    setSelectedIds([id]); setTab('annotations')
  }

  // 能否人工编辑标注：有标注权限 + 任务处于可编辑状态
  const editable = canAnnotate && !!task && EDITABLE_STATES.has(task.state)

  // 主动调 AI 的可用性
  const canInvoke = !!task && ADHOC_ALLOWED_STATES.has(task.state)
  const canReroute = !!task && REROUTE_ALLOWED_STATES.has(task.state)
  const invokingCap = invokeMut.isPending ? (invokeMut.variables?.capability ?? '') : ''
  // 工作台可补跑能力（按后端注册能力动态生成，剔除非补跑类）
  const invokeCaps = caps.filter((c) => !NON_INVOKE_CAPS.has(c))
  const hasVlmCap = invokeCaps.some((c) => CAP_META[c]?.vlm)
  // VLM 模型选项：来自 /capabilities/models（vlm.* 的去重模型），空则回退 VLM_MODELS
  const vlmModelOpts = (() => {
    const out: { value: string; label: string }[] = []
    capModels.filter((m) => m.capability_type.startsWith('vlm.') && m.model).forEach((m) => {
      if (!out.some((o) => o.value === m.model)) out.push({ value: m.model, label: `${m.provider_name} · ${m.model}` })
    })
    return out.length ? out : VLM_MODELS
  })()
  const effVlmModel = vlmModelOpts.some((o) => o.value === vlmModel) ? vlmModel : (vlmModelOpts[0]?.value ?? '')
  // 路由"选用"的路径（用于路由 tab 的路径对比）
  const routedCaps = new Set<string>([
    ...((routing?.recommended_models ?? []).map((m) => m.capability_type)),
    ...(routing?.need_ocr ? ['ocr.structure'] : []),
    ...(routing?.need_caption ? ['vlm.caption'] : []),
  ])
  // 某能力是否已有结果
  const capHasResult = (cap: string) => {
    if (cap === 'ocr.structure') return ocrBoxes.length > 0 || aiResults?.ocr?.status === 'success'
    if (cap === 'seg.instance') return segPolys.length > 0 || aiResults?.seg?.status === 'success'
    if (cap.startsWith('vlm.')) return !!aiResults?.vlm?.caption || aiResults?.vlm?.status === 'success'
    return false
  }
  // 已保存的图片级描述标签
  const descTags: string[] = Array.isArray(humanAnn?.fields?.tags) ? (humanAnn!.fields!.tags as string[]) : []
  const savedCaption = (humanAnn?.fields?.caption as string) ?? ''

  const TABS: { id: Tab; label: string }[] = [
    { id: 'annotations', label: `标注 ${shapes.length}` },
    { id: 'ai', label: `AI ${aiCount}` },
    { id: 'routing', label: '路由' },
    { id: 'trace', label: '追踪' },
    ...(canReview ? [{ id: 'review' as Tab, label: '审核' }] : []),
  ]

  return (
    <div className="flex flex-1 flex-col min-h-0 overflow-hidden">
      {/* 顶栏 */}
      <div className="flex h-13 shrink-0 items-center gap-3 border-b px-4 py-2.5" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
        <Button variant="ghost" size="sm" onClick={() => navigate(backTo)}>
          <ArrowLeft className="h-3.5 w-3.5" />返回
        </Button>
        <span className="font-mono text-sm font-medium">任务 #{taskId}</span>
        {asset && <span className="font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>{asset.original_name}</span>}
        {task && (
          <Badge variant="outline" className={`text-[10px] ${TASK_STATE_COLOR[task.state] ?? ''}`}>
            {TASK_STATE_LABELS[task.state] ?? task.state}
          </Badge>
        )}
        <div className="ml-auto flex items-center gap-2">
          <Button variant="outline" size="sm" disabled={!adjacent?.prev_task_id}
            onClick={() => adjacent?.prev_task_id && navigate(`/image-tasks/${adjacent.prev_task_id}`, { state: location.state })}>
            <ChevronLeft className="h-3.5 w-3.5" />上一个
          </Button>
          <Button variant="outline" size="sm" disabled={!adjacent?.next_task_id}
            onClick={() => adjacent?.next_task_id && navigate(`/image-tasks/${adjacent.next_task_id}`, { state: location.state })}>
            下一个<ChevronRight className="h-3.5 w-3.5" />
          </Button>
          {editable && (
            <Button size="sm" disabled={submitMut.isPending} onClick={() => submitMut.mutate()}>
              {submitMut.isPending ? '提交中...' : '提交'}
            </Button>
          )}
        </div>
      </div>

      <div ref={containerRef} className="flex min-h-0 flex-1">
        {/* 中心：交互式标注画布 */}
        {asset ? (
          <InteractiveCanvas
            assetId={asset.id}
            imgW={imgW}
            imgH={imgH}
            shapes={shapes}
            taskId={taskId}
            readOnly={!editable}
            onCommitShape={(shape) => saveShapesMut.mutate([...shapes, shape])}
            selectedIds={selectedIds}
            onSelectionChange={(ids) => { setSelectedIds(ids); if (ids.length) setTab('annotations') }}
            onUpdateShapes={(next) => saveShapesMut.mutate(next)}
            labelColor={colorForLabel}
            aiShapes={aiShapes}
            selectedAiId={selectedAiId}
            onSelectAi={(id) => setSelectedAiId((cur) => (cur === id ? null : id))}
          />
        ) : (
          <div className="flex flex-1 items-center justify-center" style={{ background: '#0f172a' }}>
            <Loader2 className="h-6 w-6 animate-spin text-white/60" />
          </div>
        )}

        {/* 可拖拽分隔条 */}
        <SplitHandle onPointerDown={startDrag} title="拖动调节画布 / Inspector 宽度" />

        {/* 右侧 Inspector */}
        <div className="flex shrink-0 flex-col border-l" style={{ width: panelWidth, borderColor: 'var(--border)', background: 'var(--card)' }}>
          {/* tab 切换（横向可滚动，容纳多个标签） */}
          <div className="flex overflow-x-auto border-b" style={{ borderColor: 'var(--border)' }}>
            {TABS.map((t) => (
              <button key={t.id} onClick={() => setTab(t.id)}
                className="shrink-0 whitespace-nowrap px-3.5 py-2.5 text-xs transition-colors border-b-2"
                style={{
                  borderColor: tab === t.id ? 'var(--primary)' : 'transparent',
                  color: tab === t.id ? 'var(--foreground)' : 'var(--muted-foreground)',
                  fontWeight: tab === t.id ? 600 : 400,
                }}>
                {t.label}
              </button>
            ))}
          </div>

          {/* 状态提示：当前任务不可编辑（已提交/审核/终态等） */}
          {canAnnotate && !editable && task && (
            <div className="border-b px-3 py-2 text-[11px]" style={{ borderColor: 'var(--border)', background: 'color-mix(in srgb, var(--chart-4) 15%, transparent)', color: 'var(--chart-4)' }}>
              当前任务状态「{TASK_STATE_LABELS[task.state] ?? task.state}」不可编辑标注（仅 待标注 / 标注中 / 已驳回 可编辑）。
            </div>
          )}
          {/* 保存失败提示（不再静默） */}
          {saveErr && (
            <div className="flex items-start gap-2 border-b px-3 py-2 text-[11px]" style={{ borderColor: 'var(--border)', background: 'color-mix(in srgb, #ef4444 12%, transparent)', color: '#ef4444' }}>
              <span className="flex-1">保存失败：{saveErr}</span>
              <button onClick={() => setSaveErr('')} className="shrink-0 opacity-70 hover:opacity-100"><X className="h-3 w-3" /></button>
            </div>
          )}

          {/* 标注列表 + 选中编辑表单 */}
          {tab === 'annotations' && (
            <div className="flex-1 overflow-auto p-3 space-y-3">
              {/* 手动添加文本标注 */}
              {editable && (
                <Button size="sm" variant="outline" className="w-full" disabled={saveShapesMut.isPending} onClick={addTextShape}>
                  <Type className="h-3.5 w-3.5" />+ 文本标注
                </Button>
              )}

              {/* 图片描述（整图级，VLM 描述可「采纳」填入） */}
              {(editable || savedCaption) && (
                <div className="rounded-md border p-2.5 space-y-2" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-medium">图片描述</span>
                    {savedCaption && descDraft.trim() === savedCaption
                      ? <span className="text-[10px]" style={{ color: 'var(--chart-2)' }}>✓ 已保存</span>
                      : descDraft.trim()
                        ? <span className="text-[10px]" style={{ color: 'var(--chart-4)' }}>● 未保存</span>
                        : null}
                  </div>
                  {editable ? (
                    <>
                      <textarea value={descDraft} onChange={(e) => setDescDraft(e.target.value)}
                        onBlur={() => { if (descDraft.trim() !== savedCaption) saveDesc(descDraft) }}
                        rows={2} placeholder="整张图片的描述（可在 AI tab 点 VLM 描述的「采纳」自动填入）"
                        className="w-full resize-none rounded-md border px-2 py-1 text-xs outline-none"
                        style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                      <Button size="sm" className="w-full" disabled={saveDescMut.isPending} onClick={() => saveDesc(descDraft)}
                        style={descSavedFlash ? { background: 'var(--chart-2)' } : undefined}>
                        {descSavedFlash ? <><Check className="h-3.5 w-3.5" />已保存</> : '保存描述'}
                      </Button>
                    </>
                  ) : (
                    <p className="text-xs whitespace-pre-wrap">{savedCaption || '—'}</p>
                  )}
                  {descTags.length > 0 && (
                    <div className="flex flex-wrap gap-1">
                      {descTags.map((t, i) => <Badge key={i} variant="secondary" className="text-[10px]">{t}</Badge>)}
                    </div>
                  )}
                </div>
              )}

              {/* 多选工具条：全选 / 删除选中 */}
              {shapes.length > 0 && editable && (
                <div className="flex items-center gap-2 text-xs">
                  <label className="flex cursor-pointer select-none items-center gap-1.5">
                    <input type="checkbox" checked={allSelected}
                      ref={(el) => { if (el) el.indeterminate = selectedIds.length > 0 && !allSelected }}
                      onChange={toggleAll} />
                    全选（{shapes.length}）
                  </label>
                  {selectedIds.length > 0 && (
                    <Button variant="outline" size="sm" className="ml-auto text-red-600 border-red-200"
                      disabled={saveShapesMut.isPending} onClick={deleteSelected}>
                      <Trash2 className="h-3.5 w-3.5" />删除选中 ({selectedIds.length})
                    </Button>
                  )}
                </div>
              )}

              {/* 批量标签：多选（>1）时统一应用标签 */}
              {selectedIds.length > 1 && editable && (
                <div className="rounded-md border p-2.5 space-y-2" style={{ borderColor: 'var(--primary)', background: 'var(--muted)' }}>
                  <span className="text-xs font-medium">批量标签（{selectedIds.length} 项）</span>
                  <div className="flex items-center gap-1.5">
                    <input list="label-defs" value={batchLabel} onChange={(e) => setBatchLabel(e.target.value)}
                      placeholder={labelDefs.length ? '选择或输入标签' : '输入标签'}
                      className="flex-1 rounded-md border px-2 py-1 text-xs outline-none"
                      style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                    <Button size="sm" disabled={!batchLabel.trim() || saveShapesMut.isPending}
                      onClick={() => applyBatchLabel(batchLabel.trim())}>应用</Button>
                  </div>
                  <datalist id="label-defs">
                    {labelDefs.map((ld) => <option key={ld.name} value={ld.name} />)}
                  </datalist>
                </div>
              )}

              {/* 选中 shape 的编辑表单 */}
              {selected && (
                <div className="rounded-md border p-2.5 space-y-2.5" style={{ borderColor: 'var(--primary)', background: 'var(--muted)' }}>
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-medium">已选标注</span>
                    <button onClick={() => setSelectedIds([])} className="opacity-60 hover:opacity-100"><X className="h-3.5 w-3.5" /></button>
                  </div>
                  {/* 类型 */}
                  <FormRow label="类型">
                    <Badge variant="secondary" className="text-[10px]">{KIND_ZH[selected.kind] ?? selected.kind}</Badge>
                  </FormRow>
                  {/* 标签：数据集 label_config 下拉（datalist 支持自定义） */}
                  <FormRow label="标签">
                    {!editable ? (
                      <span className="text-xs">{selected.label || '—'}</span>
                    ) : (
                      <div className="flex flex-1 items-center gap-1.5">
                        {(colorDraft || colorForLabel(selected.label)) && (
                          <span className="h-3 w-3 shrink-0 rounded-sm" style={{ background: colorDraft || colorForLabel(selected.label) }} />
                        )}
                        <input list="label-defs" value={labelDraft} onChange={(e) => setLabelDraft(e.target.value)}
                          placeholder={labelDefs.length ? '选择或输入标签' : 'bbox / OCR 字段 / 物体类'}
                          onBlur={() => { if (labelDraft.trim() !== (selected.label ?? '')) commitShapeEdit(selectedIdx, { label: labelDraft.trim() }) }}
                          className="flex-1 rounded-md border px-2 py-1 text-xs outline-none"
                          style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                        <datalist id="label-defs">
                          {labelDefs.map((ld) => <option key={ld.name} value={ld.name}>{ld.hotkey ? `[${ld.hotkey}]` : ''}</option>)}
                        </datalist>
                      </div>
                    )}
                  </FormRow>
                  {/* 文本 */}
                  <FormRow label="文本" align="start">
                    {!editable ? (
                      <span className="text-xs whitespace-pre-wrap">{(selected.attrs?.text as string) || '—'}</span>
                    ) : (
                      <textarea value={textDraft} onChange={(e) => setTextDraft(e.target.value)}
                        onBlur={() => { if (textDraft !== ((selected.attrs?.text as string) ?? '')) commitShapeEdit(selectedIdx, { attrs: { text: textDraft } }) }}
                        rows={2} placeholder="文本内容（OCR / 描述）"
                        className="flex-1 resize-none rounded-md border px-2 py-1 text-xs outline-none"
                        style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                    )}
                  </FormRow>
                  {/* 颜色：自选选框颜色（高对比，便于区分；点选即生效） */}
                  {editable && (
                    <FormRow label="颜色" align="start">
                      <div className="flex flex-1 flex-wrap items-center justify-end gap-1">
                        {SWATCH_COLORS.map((c) => (
                          <button key={c} title={c}
                            onClick={() => { setColorDraft(c); commitShapeEdit(selectedIdx, { color: c }) }}
                            className="h-5 w-5 rounded-sm transition-transform hover:scale-110"
                            style={{ background: c, outline: (colorDraft || '').toLowerCase() === c ? '2px solid var(--foreground)' : 'none', outlineOffset: 1 }} />
                        ))}
                        <input type="color" value={hexOrDefault(colorDraft || colorForLabel(selected.label))}
                          onChange={(e) => { setColorDraft(e.target.value); commitShapeEdit(selectedIdx, { color: e.target.value }) }}
                          title="自定义颜色" className="h-5 w-6 cursor-pointer rounded border p-0.5"
                          style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                      </div>
                    </FormRow>
                  )}
                  {/* 外接框 */}
                  <FormRow label="外接框">
                    {(() => { const b = bboxOf(selected.points); return (
                      <span className="font-mono text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                        x={b.x} y={b.y} w={b.w} h={b.h}
                      </span>
                    )})()}
                  </FormRow>
                  {/* 来源 */}
                  <FormRow label="来源">
                    <Badge variant="outline" className="text-[10px]">{selected.source || 'human'}</Badge>
                  </FormRow>
                  {editable && (
                    <div className="flex gap-2 pt-0.5">
                      <Button size="sm" className="flex-1" disabled={saveShapesMut.isPending} onClick={saveSelected}
                        style={savedFlash ? { background: 'var(--chart-2)' } : undefined}>
                        {savedFlash ? <><Check className="h-3.5 w-3.5" />已保存</> : '保存'}
                      </Button>
                      <Button variant="outline" size="sm" className="text-red-600 border-red-200"
                        disabled={saveShapesMut.isPending} onClick={() => deleteShape(selectedIdx)}>
                        <Trash2 className="h-3.5 w-3.5" />删除
                      </Button>
                    </div>
                  )}
                </div>
              )}

              {/* shape 列表 */}
              {shapes.length === 0 ? (
                <p className="text-center text-sm py-8" style={{ color: 'var(--muted-foreground)' }}>暂无标注</p>
              ) : (
                <div className="space-y-1.5">
                  {shapes.map((s, i) => {
                    const active = selectedIds.includes(s.id)
                    const labelColor = s.color || colorForLabel(s.label)
                    return (
                      <div key={s.id || i}
                        onClick={(e) => (e.shiftKey || e.ctrlKey || e.metaKey) ? toggleSelect(s.id) : setSelectedIds([s.id])}
                        className="flex items-center justify-between rounded-md border p-2 group cursor-pointer transition-colors"
                        style={{ borderColor: active ? 'var(--primary)' : 'var(--border)', background: active ? 'var(--accent)' : 'transparent' }}>
                        <div className="flex min-w-0 items-center gap-2.5">
                          {editable && (
                            <input type="checkbox" checked={active} onClick={(e) => e.stopPropagation()}
                              onChange={() => toggleSelect(s.id)} />
                          )}
                          <span className="h-3 w-3 shrink-0 rounded-sm" style={{ background: labelColor || CHART_COLORS[i % 5] }} />
                          <div className="min-w-0">
                            <p className="truncate text-sm font-medium" style={labelColor ? { color: labelColor } : undefined}>
                              {s.label || (s.attrs?.text as string) || KIND_ZH[s.kind] || s.kind}
                            </p>
                            <p className="text-[10px] font-mono" style={{ color: 'var(--muted-foreground)' }}>
                              {KIND_ZH[s.kind] ?? s.kind} {s.source ? `· ${s.source}` : ''}
                              {s.confidence ? ` · ${Math.round(s.confidence * 100)}%` : ''}
                            </p>
                          </div>
                        </div>
                        {editable && (
                          <Button variant="ghost" size="icon" disabled={saveShapesMut.isPending}
                            onClick={(e) => { e.stopPropagation(); deleteShape(i) }}>
                            <Trash2 className="h-3.5 w-3.5 text-red-500" />
                          </Button>
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
              <p className="pt-2 text-center text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                用左上工具栏在图片上绘制：矩形框 / 多边形 / 智能点选
              </p>
            </div>
          )}

          {/* AI 预标注 + 主动调 AI */}
          {tab === 'ai' && (
            <div className="flex-1 overflow-auto p-3 space-y-3">
              {/* 主动调 AI 面板 */}
              {canAnnotate && (
                <div className="rounded-md border p-2.5 space-y-2" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
                  <div className="flex items-center gap-1.5">
                    <Sparkles className="h-3.5 w-3.5" style={{ color: 'var(--primary)' }} />
                    <span className="text-xs font-medium">主动调用 AI</span>
                    {!canInvoke && (
                      <span className="ml-auto text-[10px]" style={{ color: 'var(--muted-foreground)' }}>当前状态不可补跑</span>
                    )}
                  </div>
                  {/* VLM 模型选择框（存在 VLM 能力时显示） */}
                  {hasVlmCap && (
                    <div className="flex items-center gap-2">
                      <span className="text-[11px] shrink-0" style={{ color: 'var(--muted-foreground)' }}>VLM 模型</span>
                      <select value={effVlmModel} onChange={(e) => setVlmModel(e.target.value)}
                        className="flex-1 rounded-md border px-2 py-1 text-xs outline-none"
                        style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                        {vlmModelOpts.map((m) => <option key={m.value} value={m.value}>{m.label}</option>)}
                      </select>
                    </div>
                  )}
                  {/* 按后端注册能力动态生成补跑按钮（YOLO/实例分割 等都会自动出现） */}
                  <div className="grid grid-cols-2 gap-1.5">
                    {invokeCaps.length === 0 && (
                      <span className="col-span-2 text-center text-[11px]" style={{ color: 'var(--muted-foreground)' }}>暂无可用 AI 能力</span>
                    )}
                    {invokeCaps.map((cap) => {
                      const meta = CAP_META[cap]
                      return (
                        <CapButton key={cap} label={meta?.label ?? cap} cap={cap}
                          sub={meta?.vlm ? effVlmModel : meta?.engine}
                          disabled={!canInvoke || invokeMut.isPending}
                          loading={invokingCap === cap}
                          onClick={() => invokeMut.mutate({ capability: cap, model: meta?.vlm ? effVlmModel : undefined })} />
                      )
                    })}
                  </div>
                  {invokeMsg && <InvokeBanner msg={invokeMsg} onClose={() => setInvokeMsg(null)} />}
                </div>
              )}

              {aiResults?.vlm?.caption && (
                <div className="rounded-md border p-2.5 text-sm" style={{ borderColor: 'var(--border)' }}>
                  <div className="mb-1 flex items-center justify-between">
                    <p className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>VLM 描述</p>
                    {editable && (
                      <Button variant="ghost" size="sm" disabled={saveDescMut.isPending}
                        onClick={() => { saveDesc(aiResults?.vlm?.caption ?? '', aiResults?.vlm?.tags); setTab('annotations') }}>
                        <Check className="h-4 w-4" style={{ color: 'var(--chart-2)' }} />采纳
                      </Button>
                    )}
                  </div>
                  {aiResults.vlm.caption}
                  {aiResults.vlm.tags && aiResults.vlm.tags.length > 0 && (
                    <div className="mt-1.5 flex flex-wrap gap-1">
                      {aiResults.vlm.tags.map((t, i) => <Badge key={i} variant="secondary" className="text-[10px]">{t}</Badge>)}
                    </div>
                  )}
                </div>
              )}
              {aiCount > 0 && editable && (
                <Button size="sm" variant="outline" className="w-full" disabled={saveShapesMut.isPending} onClick={adoptAllAi}>
                  <Check className="h-3.5 w-3.5" />全部采纳到画布 ({aiCount})
                </Button>
              )}
              {aiCount === 0 && !aiResults?.vlm?.caption ? (
                <p className="text-center text-sm py-6" style={{ color: 'var(--muted-foreground)' }}>暂无 AI 预标注结果</p>
              ) : (
                <>
                  {aiCount > 0 && (
                    <p className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>点条目=在图中高亮选中；点「采纳」才真正进入标注</p>
                  )}
                  {aiShapes.map((s) => {
                    const active = s.id === selectedAiId
                    return (
                      <div key={s.id} onClick={() => setSelectedAiId(active ? null : s.id)}
                        className="flex items-center justify-between rounded-md border p-2 cursor-pointer transition-colors"
                        style={{ borderColor: active ? AI_COLOR : 'var(--border)', borderWidth: active ? 2 : 1, background: 'var(--accent)' }}>
                        <div className="min-w-0">
                          <p className="text-sm font-medium truncate">{s.label || (s.source === 'ai_ocr' ? '文本框' : '对象')}</p>
                          <p className="text-[10px] font-mono" style={{ color: 'var(--muted-foreground)' }}>
                            {s.source === 'ai_ocr' ? 'OCR' : '分割 (YOLO)'}{s.confidence ? ` · ${Math.round(s.confidence * 100)}%` : ''}{active ? ' · 已选中' : ''}
                          </p>
                        </div>
                        {editable && (
                          <Button variant="ghost" size="sm" disabled={saveShapesMut.isPending}
                            onClick={(e) => { e.stopPropagation(); adoptAiShape(s); setSelectedAiId(null) }}>
                            <Check className="h-4 w-4" style={{ color: 'var(--chart-2)' }} />采纳
                          </Button>
                        )}
                      </div>
                    )
                  })}
                </>
              )}
            </div>
          )}

          {/* 路由信息 + 重新路由 */}
          {tab === 'routing' && (
            <div className="flex-1 overflow-auto p-3 space-y-3">
              <div className="flex items-center justify-between">
                <span className="text-xs font-medium">路由信息</span>
                {canAnnotate && (
                  <Button variant="outline" size="sm" disabled={!canReroute || reprocessMut.isPending}
                    onClick={() => reprocessMut.mutate()} title={canReroute ? '' : '仅终态任务（FINALIZED/EXPORTED/QC_FAILED）可重新路由'}>
                    {reprocessMut.isPending
                      ? <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      : <RefreshCw className="h-3.5 w-3.5" />}
                    重新路由
                  </Button>
                )}
              </div>
              {!canReroute && canAnnotate && (
                <p className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                  仅终态任务（FINALIZED / EXPORTED / QC_FAILED）可重新路由。
                </p>
              )}
              {invokeMsg && <InvokeBanner msg={invokeMsg} onClose={() => setInvokeMsg(null)} />}

              {!routing ? (
                <p className="text-center text-sm py-6" style={{ color: 'var(--muted-foreground)' }}>暂无路由信息</p>
              ) : (
                <div className="space-y-3">
                  <KV label="路由策略" value={routing.strategy || '—'} mono />
                  <div className="flex gap-1.5">
                    <Badge variant={routing.need_ocr ? 'default' : 'secondary'} className="text-[10px]">
                      OCR {routing.need_ocr ? '✓' : '✗'}
                    </Badge>
                    <Badge variant={routing.need_caption ? 'default' : 'secondary'} className="text-[10px]">
                      Caption {routing.need_caption ? '✓' : '✗'}
                    </Badge>
                  </div>
                  {routing.reasons?.length > 0 && (
                    <div>
                      <p className="text-[11px] mb-1" style={{ color: 'var(--muted-foreground)' }}>路由依据</p>
                      <ul className="space-y-1">
                        {routing.reasons.map((r, i) => (
                          <li key={i} className="text-xs flex gap-1.5">
                            <span style={{ color: 'var(--primary)' }}>·</span>{r}
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                  {routing.features && Object.keys(routing.features).length > 0 && (
                    <div>
                      <p className="text-[11px] mb-1" style={{ color: 'var(--muted-foreground)' }}>图像特征</p>
                      <div className="rounded-md border divide-y" style={{ borderColor: 'var(--border)' }}>
                        {Object.entries(routing.features).map(([k, v]) => (
                          <div key={k} className="flex items-center justify-between px-2 py-1 text-xs" style={{ borderColor: 'var(--border)' }}>
                            <span className="font-mono" style={{ color: 'var(--muted-foreground)' }}>{k}</span>
                            <span className="font-mono">{formatFeature(v)}</span>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                  {routing.recommended_models && routing.recommended_models.length > 0 && (
                    <div>
                      <p className="text-[11px] mb-1" style={{ color: 'var(--muted-foreground)' }}>推荐模型</p>
                      <div className="flex flex-wrap gap-1">
                        {routing.recommended_models.map((m, i) => (
                          <Badge key={i} variant="outline" className="text-[10px] font-mono">
                            {m.capability_type}:{m.model_id}
                          </Badge>
                        ))}
                      </div>
                    </div>
                  )}
                  {routing.created_at && (
                    <p className="text-[10px]" style={{ color: 'var(--muted-foreground)' }}>
                      路由时间：{new Date(routing.created_at).toLocaleString('zh-CN')}
                    </p>
                  )}
                </div>
              )}

              {/* 路径对比 / 手动补充：对比"路由选用"与"实际结果"，缺的路径可手动补跑 */}
              <div className="space-y-1.5 border-t pt-3" style={{ borderColor: 'var(--border)' }}>
                <p className="text-xs font-medium">路径对比 / 手动补充</p>
                <p className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                  对比"路由选用 vs 实际结果"。漏走的路径可点「补跑」手动补上，再回 AI tab 比对、选用。
                </p>
                {invokeCaps.map((cap) => {
                  const meta = CAP_META[cap]
                  const routed = routedCaps.has(cap)
                  const has = capHasResult(cap)
                  const running = invokingCap === cap
                  return (
                    <div key={cap} className="flex items-center gap-2 rounded-md border p-2 text-xs" style={{ borderColor: 'var(--border)' }}>
                      <div className="min-w-0 flex-1">
                        <p className="font-medium truncate">{meta?.label ?? cap}</p>
                        <p className="mt-0.5 flex flex-wrap gap-x-2 text-[10px] font-mono" style={{ color: 'var(--muted-foreground)' }}>
                          <span style={{ color: routed ? 'var(--chart-2)' : undefined }}>路由 {routed ? '✓选用' : '— 未选'}</span>
                          <span style={{ color: has ? 'var(--chart-2)' : undefined }}>结果 {has ? '✓已有' : '无'}</span>
                        </p>
                      </div>
                      <Button variant="outline" size="sm" disabled={!canInvoke || invokeMut.isPending}
                        onClick={() => invokeMut.mutate({ capability: cap, model: meta?.vlm ? effVlmModel : undefined })}>
                        {running ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
                        {has ? '重跑' : '补跑'}
                      </Button>
                    </div>
                  )
                })}
                {!canInvoke && (
                  <p className="text-[10px]" style={{ color: 'var(--muted-foreground)' }}>当前任务状态不可补跑。</p>
                )}
              </div>
            </div>
          )}

          {/* 调用追踪 */}
          {tab === 'trace' && (
            <div className="flex-1 overflow-auto p-3 space-y-4">
              <div>
                <p className="text-xs font-medium mb-1.5">AI 运行记录（ai_runs）</p>
                {!trace?.ai_runs?.length ? (
                  <p className="text-center text-sm py-4" style={{ color: 'var(--muted-foreground)' }}>暂无运行记录</p>
                ) : (
                  <div className="space-y-1.5">
                    {trace.ai_runs.map((run) => (
                      <div key={run.id || run.run_id} className="rounded-md border p-2 text-xs" style={{ borderColor: 'var(--border)' }}>
                        <div className="flex items-center justify-between">
                          <span className="font-mono font-medium">{run.capability_type}</span>
                          <StatusPill status={run.status} />
                        </div>
                        <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 font-mono" style={{ color: 'var(--muted-foreground)' }}>
                          {run.provider?.model_id && <span>模型 {run.provider.model_id}</span>}
                          <span>延迟 {run.latency_ms}ms</span>
                          {run.attempt > 1 && <span>第 {run.attempt} 次</span>}
                          {run.cost > 0 && <span>成本 {run.cost}</span>}
                        </div>
                        {run.error && <p className="mt-1 text-red-500">{run.error}</p>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
              <div>
                <p className="text-xs font-medium mb-1.5">调用日志（trace_logs）</p>
                {!trace?.trace_logs?.length ? (
                  <p className="text-center text-sm py-4" style={{ color: 'var(--muted-foreground)' }}>暂无调用日志</p>
                ) : (
                  <div className="space-y-1.5">
                    {trace.trace_logs.map((log) => (
                      <div key={log.id} className="rounded-md border p-2 text-xs" style={{ borderColor: 'var(--border)' }}>
                        <div className="flex items-center justify-between">
                          <span className="font-mono font-medium">{log.capability_type}</span>
                          <StatusPill status={log.status} />
                        </div>
                        <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 font-mono" style={{ color: 'var(--muted-foreground)' }}>
                          {log.provider && <span>{log.provider}</span>}
                          {log.model && <span>{log.model}</span>}
                          {log.latency_ms > 0 && <span>{log.latency_ms}ms</span>}
                          {log.created_at && <span>{new Date(log.created_at).toLocaleTimeString('zh-CN')}</span>}
                        </div>
                        {log.error && <p className="mt-1 text-red-500">{log.error}</p>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          {/* 审核 */}
          {tab === 'review' && canReview && (
            <div className="flex flex-1 flex-col">
              <div className="flex-1 p-4 space-y-3">
                <div>
                  <label className="text-sm font-medium">审核意见</label>
                  <textarea value={reviewNote} onChange={(e) => setReviewNote(e.target.value)}
                    placeholder="请输入审核意见（驳回时必填）..." rows={5}
                    className="mt-1.5 w-full resize-none rounded-md border p-2.5 text-sm outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                </div>
                <div>
                  <label className="text-xs font-medium" style={{ color: 'var(--muted-foreground)' }}>快捷回复</label>
                  <div className="mt-1.5 flex flex-wrap gap-1.5">
                    {['标注不完整', '分类错误', '边界不贴合', '多余标注'].map((q) => (
                      <button key={q} onClick={() => setReviewNote((n) => n ? `${n}；${q}` : q)}
                        className="rounded-full border px-2.5 py-1 text-xs transition-colors hover:bg-[var(--accent)]"
                        style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
                        {q}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
              <div className="flex gap-3 border-t p-4" style={{ borderColor: 'var(--border)' }}>
                <Button variant="outline" className="flex-1 text-red-600 border-red-200"
                  disabled={qaMut.isPending} onClick={() => qaMut.mutate(false)}>
                  <X className="h-4 w-4" />驳回重做
                </Button>
                <Button className="flex-1" style={{ background: 'var(--chart-2)' }}
                  disabled={qaMut.isPending} onClick={() => qaMut.mutate(true)}>
                  <Check className="h-4 w-4" />审核通过
                </Button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

// 主动调 AI 按钮（label + 背后引擎/模型名）
function CapButton({ label, sub, disabled, loading, onClick }: {
  label: string; sub?: string; cap: string; disabled: boolean; loading: boolean; onClick: () => void
}) {
  return (
    <Button variant="outline" size="sm" className="h-auto flex-col items-center gap-0.5 py-1.5" disabled={disabled} onClick={onClick}>
      <span className="flex items-center gap-1">
        {loading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
        {label}
      </span>
      {sub && <span className="text-[9px] font-mono opacity-60">{sub}</span>}
    </Button>
  )
}

// 补跑结果提示横幅
function InvokeBanner({ msg, onClose }: { msg: { kind: 'ok' | 'warn' | 'err'; text: string }; onClose: () => void }) {
  const color = msg.kind === 'ok' ? 'var(--chart-2)' : msg.kind === 'warn' ? 'var(--chart-4)' : '#ef4444'
  return (
    <div className="flex items-start gap-2 rounded-md border px-2 py-1.5 text-[11px]"
      style={{ borderColor: color, color }}>
      <span className="flex-1">{msg.text}</span>
      <button onClick={onClose} className="shrink-0 opacity-60 hover:opacity-100"><X className="h-3 w-3" /></button>
    </div>
  )
}

function KV({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between text-xs">
      <span style={{ color: 'var(--muted-foreground)' }}>{label}</span>
      <span className={mono ? 'font-mono' : ''}>{value}</span>
    </div>
  )
}

function StatusPill({ status }: { status: string }) {
  const ok = status === 'success'
  const color = ok ? 'var(--chart-2)' : status === 'timeout' ? 'var(--chart-4)' : '#ef4444'
  return (
    <span className="rounded-full px-1.5 py-0.5 text-[10px] font-medium" style={{ color, background: 'transparent', border: `1px solid ${color}` }}>
      {STATUS_ZH[status] ?? status}
    </span>
  )
}

function formatFeature(v: any): string {
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : v.toFixed(3)
  if (typeof v === 'boolean') return v ? '是' : '否'
  if (v == null) return '—'
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

// 标注 shape 类型中文名
const KIND_ZH: Record<string, string> = {
  bbox: '矩形框', polygon: '多边形', point: '点', polyline: '折线', ellipse: '椭圆', mask: '掩膜',
}

// 高对比快捷色板（与画布工具栏一致）
const SWATCH_COLORS = ['#ef4444', '#f97316', '#eab308', '#22c55e', '#06b6d4', '#3b82f6', '#a855f7', '#ec4899']
// type=color 需要合法 hex，否则回退
const hexOrDefault = (c?: string) => (/^#[0-9a-fA-F]{6}$/.test(c ?? '') ? (c as string) : '#3b82f6')

// 标注表单一行（左标签 + 右内容）
function FormRow({ label, children, align = 'center' }: { label: string; children: React.ReactNode; align?: 'center' | 'start' }) {
  return (
    <div className={`flex gap-2 ${align === 'start' ? 'items-start' : 'items-center'}`}>
      <span className="w-10 shrink-0 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>{label}</span>
      <div className="flex flex-1 items-center justify-end">{children}</div>
    </div>
  )
}

// 任意 points 的外接框（bbox / polygon 通用）
function bboxOf(points: number[][]): { x: number; y: number; w: number; h: number } {
  if (!points?.length) return { x: 0, y: 0, w: 0, h: 0 }
  const xs = points.map((p) => p[0]), ys = points.map((p) => p[1])
  const x = Math.min(...xs), y = Math.min(...ys)
  return { x: Math.round(x), y: Math.round(y), w: Math.round(Math.max(...xs) - x), h: Math.round(Math.max(...ys) - y) }
}
