import React, { useEffect, useRef, useState } from 'react'
import {
  Square, Hexagon, MousePointerClick, Move, MousePointer2,
  ZoomIn, ZoomOut, Maximize, Loader2, Check, Eraser, Sparkles,
} from 'lucide-react'
import { taskApi, type Shape } from '@/api/imageTask'
import { AssetImage } from './AssetImage'

const CHART_COLORS = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)']
// 高对比快捷色板（绘制/编辑选框时可一键取色，减少视觉疲劳）
const SWATCHES = ['#ef4444', '#f97316', '#eab308', '#22c55e', '#06b6d4', '#3b82f6', '#a855f7', '#ec4899']

type Tool = 'move' | 'edit' | 'bbox' | 'polygon' | 'sam'
type CanvasLabelOption = { name: string; display?: string; color?: string }

interface Props {
  // Background: either an image asset (default AssetImage) or a custom node
  // (e.g. a <video> for the video workspace, or a tiled viewer for WSI). When
  // `background` is provided it replaces AssetImage; the SVG shape layer
  // overlays it in the same imgW×imgH coordinate space.
  assetId?: number
  background?: React.ReactNode
  imgW: number
  imgH: number
  shapes: Shape[]
  taskId: number
  readOnly?: boolean
  // SAM interactive point-select (needs seg.interactive). Off by default.
  enableSam?: boolean
  // Custom SAM call — video injects one that segments the CURRENT frame (sends
  // image_b64). When absent, defaults to taskApi.segment (still image asset).
  samSegment?: (points: number[][], box?: number[]) => Promise<{ polygons: number[][] }>
  // Video SAM2 propagation: given the current point prompt, propagate the object
  // across the whole clip (creates a track). When present, a 「传播全片」button
  // shows alongside SAM 采纳.
  onPropagate?: (points: number[][], label: string) => Promise<void> | void
  onCommitShape: (shape: Shape) => void
  // 精细编辑：选择同步 + 整体替换（顶点拖拽 / 框选 / 删除）
  selectedIds?: string[]
  onSelectionChange?: (ids: string[]) => void
  onUpdateShapes?: (shapes: Shape[]) => void
  // 按标签取色（与右侧列表一致）；返回空串则回退到 CHART_COLORS
  labelColor?: (label?: string) => string
  // 新建 shape 的本体标签选项；不传时保留自由文本标签输入。
  labelOptions?: CanvasLabelOption[]
  // freeLabel: 即使有 labelOptions 也用「可输入+建议(datalist)」而非受限下拉，
  // 并暴露颜色选择器（视频端用——标签更开放、可换框色）。
  freeLabel?: boolean
  // AI 建议层（YOLO 分割 / OCR 框 等）：青色虚线叠加；点击=选中高亮（不直接采纳，采纳在右侧列表）
  aiShapes?: Shape[]
  selectedAiId?: string | null
  onSelectAi?: (id: string) => void
}

const AI_COLOR = '#22d3ee'

// 任意 points 的外接框
function bboxOf(points: number[][]) {
  const xs = points.map((p) => p[0]), ys = points.map((p) => p[1])
  return { xmin: Math.min(...xs), ymin: Math.min(...ys), xmax: Math.max(...xs), ymax: Math.max(...ys) }
}

export function InteractiveCanvas({
  assetId, background, imgW, imgH, shapes, taskId, readOnly, enableSam = true, samSegment, onPropagate, onCommitShape,
  selectedIds = [], onSelectionChange, onUpdateShapes, labelColor,
  labelOptions = [], freeLabel = false, aiShapes = [], selectedAiId, onSelectAi,
}: Props) {
  const wrapperRef = useRef<HTMLDivElement>(null)
  const [tool, setTool] = useState<Tool>('move')
  const [showAi, setShowAi] = useState(true)
  const [label, setLabel] = useState('对象')
  const [drawColor, setDrawColor] = useState('#ef4444') // 新建选框的默认颜色
  const [zoom, setZoom] = useState(100)
  const [pan, setPan] = useState({ x: 0, y: 0 })

  // 绘制中状态
  const [draftBbox, setDraftBbox] = useState<{ start: number[]; cur: number[] } | null>(null)
  const [draftPoly, setDraftPoly] = useState<number[][]>([])
  const [samPoints, setSamPoints] = useState<number[][]>([])
  const [draftSeg, setDraftSeg] = useState<number[][] | null>(null)
  const [samLoading, setSamLoading] = useState(false)
  const [samErr, setSamErr] = useState('')
  const [propagating, setPropagating] = useState(false)

  // 精细编辑中状态
  const [editDraft, setEditDraft] = useState<Shape[] | null>(null) // 顶点拖拽时的临时副本
  const [marquee, setMarquee] = useState<{ start: number[]; cur: number[] } | null>(null)
  const vertexDrag = useRef<
    | { type: 'poly'; shapeId: string; vi: number }
    | { type: 'bbox'; shapeId: string; opp: number[] }
    | null
  >(null)
  // 整体拖动一个 shape（编辑工具下拖框身平移）
  const bodyDrag = useRef<{ shapeId: string; startImg: number[]; orig: number[][] } | null>(null)

  const downRef = useRef<{ x: number; y: number } | null>(null)
  const panStartRef = useRef<{ pan: { x: number; y: number }; mouse: { x: number; y: number } } | null>(null)

  const selSet = new Set(selectedIds)
  const renderShapes = editDraft ?? shapes
  const colorOf = (s: Shape, i: number) => s.color || labelColor?.(s.label) || CHART_COLORS[i % 5]
  const activeLabelOption = labelOptions.find((o) => o.name === label) ?? labelOptions[0]
  // freeLabel: label is free text, color is user-controlled (drawColor). Otherwise
  // the selected option drives both name and color (image behaviour, unchanged).
  const activeLabel = freeLabel ? label : (activeLabelOption?.name ?? label)
  const activeDrawColor = freeLabel ? drawColor : (activeLabelOption?.color || drawColor)

  const select = (id: string, additive: boolean) => {
    if (!onSelectionChange) return
    if (additive) {
      onSelectionChange(selSet.has(id) ? selectedIds.filter((x) => x !== id) : [...selectedIds, id])
    } else {
      onSelectionChange([id])
    }
  }

  // 键盘快捷键：V 移动 / E 编辑 / R 矩形 / P 多边形 / S 点选 / Esc 取消 / Del 删除选中
  useEffect(() => {
    if (readOnly) return
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      if (e.ctrlKey || e.metaKey || e.altKey) return
      const k = e.key.toLowerCase()
      if (k === 'r') setTool('bbox')
      else if (k === 'p') setTool('polygon')
      else if (k === 's' && enableSam) setTool('sam')
      else if (k === 'v' && !e.shiftKey) setTool('move')
      else if (k === 'e') setTool('edit')
      else if (e.key === 'Escape') { setDraftBbox(null); setDraftPoly([]); setSamPoints([]); setDraftSeg(null); setMarquee(null); onSelectionChange?.([]) }
      else if ((e.key === 'Delete' || e.key === 'Backspace') && selectedIds.length && onUpdateShapes) {
        e.preventDefault()
        onUpdateShapes(shapes.filter((s) => !selSet.has(s.id)))
        onSelectionChange?.([])
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [readOnly, selectedIds, shapes]) // eslint-disable-line react-hooks/exhaustive-deps

  // 屏幕坐标 → 原图像素坐标（rect 比例法，自动消除 zoom 影响）
  const toImg = (clientX: number, clientY: number): number[] => {
    const rect = wrapperRef.current!.getBoundingClientRect()
    const x = ((clientX - rect.left) / rect.width) * imgW
    const y = ((clientY - rect.top) / rect.height) * imgH
    return [Math.max(0, Math.min(imgW, x)), Math.max(0, Math.min(imgH, y))]
  }

  // 顶点拖拽起手（由 SVG 顶点把手触发）
  const startVertexDrag = (e: React.PointerEvent, drag: NonNullable<typeof vertexDrag.current>) => {
    if (readOnly) return
    e.stopPropagation()
    vertexDrag.current = drag
    wrapperRef.current?.setPointerCapture(e.pointerId)
  }

  const onPointerDown = (e: React.PointerEvent) => {
    if (readOnly) return
    downRef.current = { x: e.clientX, y: e.clientY }
    if (tool === 'bbox') {
      const p = toImg(e.clientX, e.clientY)
      setDraftBbox({ start: p, cur: p })
    } else if (tool === 'move') {
      panStartRef.current = { pan: { ...pan }, mouse: { x: e.clientX, y: e.clientY } }
      ;(e.target as HTMLElement).setPointerCapture?.(e.pointerId)
    } else if (tool === 'edit') {
      // 命中空白处 → 开始框选（命中 shape/把手已 stopPropagation）
      const p = toImg(e.clientX, e.clientY)
      setMarquee({ start: p, cur: p })
      wrapperRef.current?.setPointerCapture(e.pointerId)
    }
  }

  const onPointerMove = (e: React.PointerEvent) => {
    if (vertexDrag.current) {
      const [nx, ny] = toImg(e.clientX, e.clientY)
      const d = vertexDrag.current
      setEditDraft(
        shapes.map((s) => {
          if (s.id !== d.shapeId) return s
          if (d.type === 'poly') {
            const pts = s.points.map((p, i) => (i === d.vi ? [nx, ny] : p))
            return { ...s, points: pts }
          }
          return { ...s, points: [[nx, ny], d.opp] }
        })
      )
    } else if (bodyDrag.current) {
      const [cx, cy] = toImg(e.clientX, e.clientY)
      const d = bodyDrag.current
      const dx = cx - d.startImg[0], dy = cy - d.startImg[1]
      setEditDraft(shapes.map((s) => (s.id === d.shapeId ? { ...s, points: d.orig.map(([px, py]) => [px + dx, py + dy]) } : s)))
    } else if (marquee) {
      setMarquee({ start: marquee.start, cur: toImg(e.clientX, e.clientY) })
    } else if (tool === 'bbox' && draftBbox) {
      setDraftBbox({ start: draftBbox.start, cur: toImg(e.clientX, e.clientY) })
    } else if (tool === 'move' && panStartRef.current) {
      const { pan: p0, mouse } = panStartRef.current
      setPan({ x: p0.x + (e.clientX - mouse.x), y: p0.y + (e.clientY - mouse.y) })
    }
  }

  const onPointerUp = (e: React.PointerEvent) => {
    if (readOnly) return
    const moved = downRef.current
      ? Math.hypot(e.clientX - downRef.current.x, e.clientY - downRef.current.y) > 4
      : false

    if (vertexDrag.current) {
      if (editDraft && onUpdateShapes) onUpdateShapes(editDraft)
      setEditDraft(null)
      vertexDrag.current = null
    } else if (bodyDrag.current) {
      // 拖动了才提交；只是点击（未移动）则仅选中
      if (moved && editDraft && onUpdateShapes) onUpdateShapes(editDraft)
      setEditDraft(null)
      bodyDrag.current = null
    } else if (marquee) {
      const m = bboxOf([marquee.start, marquee.cur])
      // 拖动距离过小视作点击空白 → 清空选择；否则框选相交的 shapes
      if (!moved) {
        onSelectionChange?.([])
      } else {
        const hit = shapes.filter((s) => {
          const b = bboxOf(s.points)
          return !(b.xmax < m.xmin || b.xmin > m.xmax || b.ymax < m.ymin || b.ymin > m.ymax)
        }).map((s) => s.id)
        onSelectionChange?.(e.shiftKey ? Array.from(new Set([...selectedIds, ...hit])) : hit)
      }
      setMarquee(null)
    } else if (tool === 'bbox' && draftBbox) {
      const [x1, y1] = draftBbox.start
      const [x2, y2] = draftBbox.cur
      if (Math.abs(x2 - x1) > 3 && Math.abs(y2 - y1) > 3) {
        onCommitShape({ id: `bbox-${Date.now()}`, kind: 'bbox', label: activeLabel, points: [[x1, y1], [x2, y2]], source: 'manual', color: activeDrawColor })
      }
      setDraftBbox(null)
    } else if (tool === 'move') {
      panStartRef.current = null
    } else if (tool === 'polygon' && !moved) {
      setDraftPoly((pts) => [...pts, toImg(e.clientX, e.clientY)])
    } else if (tool === 'sam' && !moved) {
      const next = [...samPoints, toImg(e.clientX, e.clientY)]
      setSamPoints(next)
      runSam(next)
    }
    downRef.current = null
  }

  const runSam = async (points: number[][]) => {
    setSamLoading(true); setSamErr('')
    try {
      const res = samSegment ? await samSegment(points) : await taskApi.segment(taskId, points)
      const flat = res.polygons?.[0]
      if (flat && flat.length >= 6) {
        const pts: number[][] = []
        for (let i = 0; i + 1 < flat.length; i += 2) pts.push([flat[i], flat[i + 1]])
        setDraftSeg(pts)
      } else {
        setSamErr('未返回有效分割，换个点再试')
      }
    } catch (e: any) {
      setSamErr('SAM 调用失败：' + (e?.response?.data?.error || e?.message || 'SAM 服务未启动？'))
    } finally {
      setSamLoading(false)
    }
  }

  const finishPolygon = () => {
    if (draftPoly.length >= 3) {
      onCommitShape({ id: `poly-${Date.now()}`, kind: 'polygon', label: activeLabel, points: draftPoly, source: 'manual', color: activeDrawColor })
    }
    setDraftPoly([])
  }

  const adoptSam = () => {
    if (draftSeg && draftSeg.length >= 3) {
      onCommitShape({ id: `sam-${Date.now()}`, kind: 'polygon', label: activeLabel, points: draftSeg, source: 'sam', color: activeDrawColor })
    }
    setSamPoints([]); setDraftSeg(null)
  }

  const clearSam = () => { setSamPoints([]); setDraftSeg(null); setSamErr('') }

  const tools: { id: Tool; icon: React.ReactNode; title: string }[] = [
    { id: 'edit', icon: <MousePointer2 className="h-4 w-4" />, title: '选择/编辑顶点 (E)' },
    { id: 'bbox', icon: <Square className="h-4 w-4" />, title: '矩形框 (R)' },
    { id: 'polygon', icon: <Hexagon className="h-4 w-4" />, title: '多边形 (P)' },
    ...(enableSam ? [{ id: 'sam' as Tool, icon: <MousePointerClick className="h-4 w-4" />, title: '智能点选 SAM (S)' }] : []),
    { id: 'move', icon: <Move className="h-4 w-4" />, title: '移动/平移 (V)' },
  ]

  const cursor = tool === 'move' ? 'grab' : tool === 'sam' ? 'pointer' : tool === 'edit' ? 'default' : 'crosshair'
  // 单选且为编辑工具时，渲染顶点把手
  const handleShape = tool === 'edit' && selectedIds.length === 1
    ? renderShapes.find((s) => s.id === selectedIds[0] && !s.attrs?.locked)
    : null

  return (
    <div className="relative flex flex-1 items-center justify-center overflow-hidden" style={{ background: '#0f172a' }}>
      {/* 工具栏（左上） */}
      {!readOnly && (
        <div className="absolute top-4 left-4 z-20 flex flex-col gap-2">
          <div className="flex flex-col gap-1 rounded-lg border p-1 backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', opacity: 0.95 }}>
            {tools.map((t) => (
              <button key={t.id} title={t.title} onClick={() => setTool(t.id)}
                className="flex h-9 w-9 items-center justify-center rounded-md transition-colors"
                style={{
                  background: tool === t.id ? 'var(--primary)' : 'transparent',
                  color: tool === t.id ? 'var(--primary-foreground)' : 'var(--foreground)',
                }}>
                {t.icon}
              </button>
            ))}
          </div>
          {/* 新建图形：默认标签 + 颜色（设一次，后续框沿用） */}
          <div className="flex flex-col gap-1.5 rounded-lg border p-1.5 backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', opacity: 0.95 }}>
            <div className="flex items-center gap-1.5">
              {freeLabel ? (
                <>
                  <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="标签（输入/选择）" list="canvas-label-options"
                    className="h-7 w-28 rounded border px-2 text-xs outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  <datalist id="canvas-label-options">
                    {labelOptions.map((opt) => <option key={opt.name} value={opt.name}>{opt.display || opt.name}</option>)}
                  </datalist>
                  <input type="color" value={drawColor} onChange={(e) => setDrawColor(e.target.value)} title="新建选框颜色"
                    className="h-7 w-7 cursor-pointer rounded border p-0.5" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                </>
              ) : labelOptions.length > 0 ? (
                <>
                  <span className="h-4 w-4 shrink-0 rounded-sm border" style={{ background: activeDrawColor, borderColor: 'var(--border)' }} />
                  <select value={activeLabel} onChange={(e) => setLabel(e.target.value)}
                    className="h-7 w-28 rounded border px-2 text-xs outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                    {labelOptions.map((opt) => <option key={opt.name} value={opt.name}>{opt.display || opt.name}</option>)}
                  </select>
                </>
              ) : (
                <>
                  <input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="标签"
                    className="h-7 w-24 rounded border px-2 text-xs outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  <input type="color" value={drawColor} onChange={(e) => setDrawColor(e.target.value)} title="新建选框颜色"
                    className="h-7 w-7 cursor-pointer rounded border p-0.5" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                </>
              )}
            </div>
            {labelOptions.length === 0 && (
              <div className="flex items-center gap-1">
                {SWATCHES.map((c) => (
                  <button key={c} title={c} onClick={() => setDrawColor(c)}
                    className="h-4 w-4 rounded-sm border transition-transform hover:scale-110"
                    style={{ background: c, borderColor: drawColor.toLowerCase() === c ? 'var(--foreground)' : 'transparent', borderWidth: drawColor.toLowerCase() === c ? 2 : 1 }} />
                ))}
              </div>
            )}
          </div>
          {/* 编辑工具提示 */}
          {tool === 'edit' && (
            <div className="rounded-lg border px-2 py-1.5 text-[11px] backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', color: 'var(--muted-foreground)', maxWidth: 150 }}>
              点击选中 · 拖空白框选 · Shift 多选 · 拖顶点改形状 · Del 删除
            </div>
          )}
          {/* 多边形完成 */}
          {tool === 'polygon' && draftPoly.length > 0 && (
            <button onClick={finishPolygon}
              className="flex items-center gap-1 rounded-lg border px-2.5 py-1.5 text-xs backdrop-blur"
              style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
              <Check className="h-3.5 w-3.5" style={{ color: 'var(--chart-2)' }} />完成 ({draftPoly.length} 点)
            </button>
          )}
          {/* SAM 操作 */}
          {tool === 'sam' && (samPoints.length > 0 || samLoading || samErr) && (
            <div className="flex flex-col gap-1 rounded-lg border p-1.5 text-xs backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', maxWidth: 170 }}>
              {samLoading && <span className="flex items-center gap-1" style={{ color: 'var(--muted-foreground)' }}><Loader2 className="h-3 w-3 animate-spin" />分割中...</span>}
              {samErr && <span style={{ color: '#ef4444' }}>{samErr}</span>}
              {draftSeg && (
                <button onClick={adoptSam} className="flex items-center gap-1"><Check className="h-3.5 w-3.5" style={{ color: 'var(--chart-2)' }} />采纳（本帧）</button>
              )}
              {onPropagate && samPoints.length > 0 && (
                <button disabled={propagating} className="flex items-center gap-1" style={{ color: 'var(--primary)' }}
                  title="用这个点让 SAM2 跨帧传播，一键生成整段 track"
                  onClick={async () => {
                    setPropagating(true); setSamErr('')
                    try { await onPropagate(samPoints, activeLabel); clearSam() }
                    catch (e: any) { setSamErr('传播失败：' + (e?.response?.data?.message || e?.message || '')) }
                    finally { setPropagating(false) }
                  }}>
                  {propagating ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
                  {propagating ? '传播中…' : 'SAM2 传播全片'}
                </button>
              )}
              <button onClick={clearSam} className="flex items-center gap-1" style={{ color: 'var(--muted-foreground)' }}><Eraser className="h-3.5 w-3.5" />清除点</button>
            </div>
          )}
        </div>
      )}

      {/* AI 建议层开关（左下） */}
      {aiShapes.length > 0 && (
        <div className="absolute bottom-4 left-4 z-20 flex items-center gap-2 rounded-lg border px-2.5 py-1.5 text-xs backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', opacity: 0.95 }}>
          <label className="flex cursor-pointer select-none items-center gap-1.5">
            <input type="checkbox" checked={showAi} onChange={(e) => setShowAi(e.target.checked)} />
            <span className="inline-block h-2.5 w-2.5 rounded-sm" style={{ background: AI_COLOR }} />
            AI 建议 ({aiShapes.length})
          </label>
          {onSelectAi && <span style={{ color: 'var(--muted-foreground)' }}>· 点框选中</span>}
        </div>
      )}

      {/* 缩放浮层（右上） */}
      <div className="absolute top-4 right-4 z-20 flex items-center gap-1 rounded-lg border p-1 backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', opacity: 0.95 }}>
        <button className="flex h-8 w-8 items-center justify-center rounded-md hover:bg-[var(--accent)]" onClick={() => setZoom((z) => Math.max(20, z - 10))}><ZoomOut className="h-4 w-4" /></button>
        <span className="w-12 text-center text-xs font-mono select-none">{zoom}%</span>
        <button className="flex h-8 w-8 items-center justify-center rounded-md hover:bg-[var(--accent)]" onClick={() => setZoom((z) => Math.min(400, z + 10))}><ZoomIn className="h-4 w-4" /></button>
        <button className="flex h-8 w-8 items-center justify-center rounded-md hover:bg-[var(--accent)]" onClick={() => { setZoom(100); setPan({ x: 0, y: 0 }) }}><Maximize className="h-4 w-4" /></button>
      </div>

      {/* 变换层：translate(pan) scale(zoom) */}
      <div
        ref={wrapperRef}
        className="relative select-none"
        style={{
          transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom / 100})`,
          maxWidth: '85%', maxHeight: '82vh', touchAction: 'none', cursor,
          aspectRatio: `${imgW} / ${imgH}`,
        }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
      >
        {background ?? (assetId != null && <AssetImage assetId={assetId} className="pointer-events-none block h-full w-full object-contain" />)}
        <svg className="absolute inset-0 h-full w-full" viewBox={`0 0 ${imgW} ${imgH}`} preserveAspectRatio="none" style={{ pointerEvents: 'none' }}>
          {/* 已有 shapes */}
          {renderShapes.map((s, i) => {
            const c = colorOf(s, i)
            const sel = selSet.has(s.id)
            const locked = !!s.attrs?.locked
            const interactive = tool === 'edit' && !readOnly && !locked
            const common = {
              fill: c, fillOpacity: sel ? 0.42 : 0.24, stroke: c, strokeWidth: sel ? 4 : 3,
              strokeDasharray: locked ? '8 5' : undefined,
              style: { pointerEvents: (interactive ? 'auto' : 'none') as React.CSSProperties['pointerEvents'], cursor: interactive ? 'move' : 'default' },
              onPointerDown: interactive ? (e: React.PointerEvent) => {
                e.stopPropagation()
                downRef.current = { x: e.clientX, y: e.clientY }
                const additive = e.shiftKey || e.ctrlKey || e.metaKey
                select(s.id, additive)
                // 非多选时，允许拖动整个 shape 平移
                if (!additive) {
                  bodyDrag.current = { shapeId: s.id, startImg: toImg(e.clientX, e.clientY), orig: s.points }
                  wrapperRef.current?.setPointerCapture(e.pointerId)
                }
              } : undefined,
            }
            if (s.kind === 'bbox' && s.points.length >= 2) {
              const b = bboxOf(s.points)
              return <rect key={s.id || i} x={b.xmin} y={b.ymin} width={b.xmax - b.xmin} height={b.ymax - b.ymin} {...common} />
            }
            if (s.kind === 'polygon' && s.points.length >= 3) {
              return <polygon key={s.id || i} points={s.points.map((p) => p.join(',')).join(' ')} {...common} />
            }
            return null
          })}

          {/* AI 建议层（YOLO 分割 / OCR 框）：青色虚线；点击=选中高亮，选中项实线加粗 */}
          {(showAi ? aiShapes : aiShapes.filter((s) => s.id === selectedAiId)).map((s, i) => {
            const hl = !!selectedAiId && s.id === selectedAiId
            const clickable = !!onSelectAi
            const common = {
              fill: AI_COLOR, fillOpacity: hl ? 0.3 : 0.06, stroke: AI_COLOR, strokeWidth: hl ? 4 : 2.5,
              strokeDasharray: hl ? undefined : '8 5',
              style: { pointerEvents: (clickable ? 'auto' : 'none') as React.CSSProperties['pointerEvents'], cursor: clickable ? 'pointer' : 'default' },
              onPointerDown: clickable ? (e: React.PointerEvent) => { e.stopPropagation(); onSelectAi!(s.id) } : undefined,
            }
            if (s.kind === 'bbox' && s.points.length >= 2) {
              const b = bboxOf(s.points)
              return <rect key={`ai-${i}`} x={b.xmin} y={b.ymin} width={b.xmax - b.xmin} height={b.ymax - b.ymin} {...common} />
            }
            if (s.kind === 'polygon' && s.points.length >= 3) {
              return <polygon key={`ai-${i}`} points={s.points.map((p) => p.join(',')).join(' ')} {...common} />
            }
            return null
          })}

          {/* 顶点把手（单选 + 编辑工具） */}
          {handleShape && (() => {
            const c = labelColor?.(handleShape.label) || 'var(--primary)'
            if (handleShape.kind === 'bbox' && handleShape.points.length >= 2) {
              const b = bboxOf(handleShape.points)
              const corners: { x: number; y: number; opp: number[] }[] = [
                { x: b.xmin, y: b.ymin, opp: [b.xmax, b.ymax] },
                { x: b.xmax, y: b.ymin, opp: [b.xmin, b.ymax] },
                { x: b.xmin, y: b.ymax, opp: [b.xmax, b.ymin] },
                { x: b.xmax, y: b.ymax, opp: [b.xmin, b.ymin] },
              ]
              return corners.map((cn, i) => (
                <circle key={`h${i}`} cx={cn.x} cy={cn.y} r={6} fill="#fff" stroke={c} strokeWidth={2}
                  style={{ pointerEvents: 'auto', cursor: 'nwse-resize' }}
                  onPointerDown={(e) => startVertexDrag(e, { type: 'bbox', shapeId: handleShape.id, opp: cn.opp })} />
              ))
            }
            if (handleShape.kind === 'polygon') {
              return handleShape.points.map((p, i) => (
                <circle key={`h${i}`} cx={p[0]} cy={p[1]} r={6} fill="#fff" stroke={c} strokeWidth={2}
                  style={{ pointerEvents: 'auto', cursor: 'grab' }}
                  onPointerDown={(e) => startVertexDrag(e, { type: 'poly', shapeId: handleShape.id, vi: i })} />
              ))
            }
            return null
          })()}

          {/* 框选预览：白底 + 深色虚线 */}
          {marquee && (() => {
            const b = bboxOf([marquee.start, marquee.cur])
            const w = b.xmax - b.xmin, h = b.ymax - b.ymin
            return (
              <g>
                <rect x={b.xmin} y={b.ymin} width={w} height={h} fill="var(--primary)" fillOpacity={0.08} stroke="#fff" strokeWidth={3} />
                <rect x={b.xmin} y={b.ymin} width={w} height={h} fill="none" stroke="#111" strokeWidth={1.5} strokeDasharray="6 4" />
              </g>
            )
          })()}

          {/* bbox 绘制预览：白底实线 + 深色虚线（任意背景都清晰可辨） */}
          {draftBbox && (() => {
            const [[x1, y1], [x2, y2]] = [draftBbox.start, draftBbox.cur]
            const x = Math.min(x1, x2), y = Math.min(y1, y2), w = Math.abs(x2 - x1), h = Math.abs(y2 - y1)
            return (
              <g>
                <rect x={x} y={y} width={w} height={h} fill="#000" fillOpacity={0.06} stroke="#fff" strokeWidth={4} />
                <rect x={x} y={y} width={w} height={h} fill="none" stroke="#111" strokeWidth={2} strokeDasharray="9 6" />
              </g>
            )
          })()}
          {/* polygon 绘制预览：白底实线 + 深色虚线 + 高对比顶点 */}
          {draftPoly.length > 0 && (() => {
            const pts = draftPoly.map((p) => p.join(',')).join(' ')
            return (
              <>
                <polyline points={pts} fill="none" stroke="#fff" strokeWidth={4} />
                <polyline points={pts} fill="none" stroke="#111" strokeWidth={2} strokeDasharray="9 6" />
                {draftPoly.map((p, i) => <circle key={i} cx={p[0]} cy={p[1]} r={5} fill="#fff" stroke="#111" strokeWidth={2} />)}
              </>
            )
          })()}
          {/* SAM 预览 polygon + 点 */}
          {draftSeg && <polygon points={draftSeg.map((p) => p.join(',')).join(' ')} fill="var(--chart-2)" fillOpacity={0.2} stroke="var(--chart-2)" strokeWidth={2} />}
          {samPoints.map((p, i) => <circle key={i} cx={p[0]} cy={p[1]} r={5} fill="var(--chart-2)" stroke="#fff" strokeWidth={1.5} />)}
        </svg>
      </div>
    </div>
  )
}
