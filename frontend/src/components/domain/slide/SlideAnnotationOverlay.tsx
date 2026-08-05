import { useEffect, useRef, useState } from 'react'
import OpenSeadragon from 'openseadragon'
import { Square, Hexagon, Move, MousePointer2, Check, Ruler, Eraser } from 'lucide-react'

import type { Shape } from '@/api/imageTask'
import { affineToMatrix, recoverAffine, type OverlayAffine } from '@/lib/osdOverlayTransform'
import { formatMicrons, lengthMicrons } from '@/lib/slideMeasure'

// WSI 区域标注叠加层(C1.3)。在 OSD 深缩放查看器之上画/改**区域**(polygon/bbox),
// 几何一律存**原生 level-0 像素坐标**——与瓦片端点、导出、bbox/polygon 轨迹同一空间。
//
// # 为什么不复用 image-annotation 的 InteractiveCanvas
//
// 那个画布用 CSS transform 把**整张图**缩放进一个框、SVG viewBox=整图,靠
// getBoundingClientRect 比例反算像素——前提是"整图能一屏放下"。WSI 46000×32914
// 放不下,视口必须归 OSD 管(瓦片按需加载)。所以这里几何在 image 空间,屏幕位置
// 由 OSD 视口每帧驱动(`osdOverlayTransform`),而不是自己缩放整图。
//
// # 坐标同步(会静默出错的地方)
//
// 叠加层与瓦片一旦不同步,框看着正常却落在错的细胞上,不报错。两条纪律:
//   ① 屏幕位置**唯一真源 = OSD 自己的坐标换算**(imageToViewerElement / windowToImage),
//      绝不另算一套视口数学。
//   ② `<g>` 的 transform 每次 `update-viewport`(含动画帧)都重算——静态叠加层会在
//      平移缩放时漂移。

type Tool = 'move' | 'bbox' | 'polygon' | 'edit' | 'ruler'

interface Measurement {
  a: number[]
  b: number[]
  um: number
}

interface Props {
  viewer: OpenSeadragon.Viewer
  /** level-0 像素空间的区域几何。 */
  shapes: Shape[]
  readOnly?: boolean
  /** µm/px。0 = 未知(探针拿不到 MPP);此时禁用测量,绝不编标尺(C1.1a 纪律)。 */
  mpp: number
  /** 新建区域的标签与颜色(由父面板给)。 */
  activeLabel: string
  activeColor: string
  labelColor?: (label?: string) => string
  onCommitShape?: (shape: Shape) => void
  onUpdateShapes?: (shapes: Shape[]) => void
  selectedId?: string | null
  onSelect?: (id: string | null) => void
}

const HANDLE_PX = 6 // 顶点把手半径(屏幕像素),随缩放反算成 image 半径

function bboxOf(points: number[][]) {
  const xs = points.map((p) => p[0])
  const ys = points.map((p) => p[1])
  return { xmin: Math.min(...xs), ymin: Math.min(...ys), xmax: Math.max(...xs), ymax: Math.max(...ys) }
}

export function SlideAnnotationOverlay({
  viewer, shapes, readOnly, mpp, activeLabel, activeColor, labelColor,
  onCommitShape, onUpdateShapes, selectedId, onSelect,
}: Props) {
  const [tool, setTool] = useState<Tool>('move')
  const [affine, setAffine] = useState<OverlayAffine | null>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const [draftBbox, setDraftBbox] = useState<{ start: number[]; cur: number[] } | null>(null)
  const [draftPoly, setDraftPoly] = useState<number[][]>([])
  const [rulerA, setRulerA] = useState<number[] | null>(null) // 尺子第一个点(第二点落定即成一条测量)
  const [measurements, setMeasurements] = useState<Measurement[]>([]) // 临时测量(不入库)
  const canMeasure = mpp > 0
  const [editDraft, setEditDraft] = useState<Shape[] | null>(null)
  const vertexDrag = useRef<{ shapeId: string; type: 'poly'; vi: number } | { shapeId: string; type: 'bbox'; opp: number[] } | null>(null)
  const bodyDrag = useRef<{ shapeId: string; startImg: number[]; orig: number[][] } | null>(null)
  const downRef = useRef<{ x: number; y: number } | null>(null)

  // ① 每次视口变化重算仿射。用 rAF 合并同一帧内的多次 update-viewport。OSD 的
  //    imageToViewerElement 是唯一真源;两点(image (0,0)/(1,0))反解 scale+offset。
  useEffect(() => {
    let raf = 0
    const sync = () => {
      if (raf) return
      raf = requestAnimationFrame(() => {
        raf = 0
        try {
          const p00 = viewer.viewport.imageToViewerElementCoordinates(new OpenSeadragon.Point(0, 0))
          const p10 = viewer.viewport.imageToViewerElementCoordinates(new OpenSeadragon.Point(1, 0))
          setAffine(recoverAffine(p00, p10))
        } catch {
          /* 视口未就绪 */
        }
      })
    }
    viewer.addHandler('update-viewport', sync)
    viewer.addHandler('resize', sync)
    viewer.addHandler('open', sync)
    sync()
    return () => {
      if (raf) cancelAnimationFrame(raf)
      viewer.removeHandler('update-viewport', sync)
      viewer.removeHandler('resize', sync)
      viewer.removeHandler('open', sync)
    }
  }, [viewer])

  // 绘制/编辑工具下,叠加层截获指针(OSD 不再平移);move 下放行给 OSD。
  const interactive = !readOnly && tool !== 'move'

  // 绘制/编辑时叠加层吃掉指针 → 滚轮也被吃 → OSD 缩不了。把滚轮转发给 OSD 缩放,
  // 这样"边画边缩"顺手。⚠️ React onWheel 是 passive,preventDefault 无效(C3.2 踩过),
  // 必须挂原生 {passive:false} 监听。
  useEffect(() => {
    const el = svgRef.current
    if (!el || !interactive) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const factor = e.deltaY < 0 ? 1.2 : 1 / 1.2
      const vp = viewer.viewport.windowToViewportCoordinates(new OpenSeadragon.Point(e.clientX, e.clientY))
      viewer.viewport.zoomBy(factor, vp)
      viewer.viewport.applyConstraints()
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [viewer, interactive])

  // 屏幕(window)→ image(level-0)像素。用 OSD 的 windowToImage(唯一真源),
  // 不用 rect 比例——避免自算视口数学漂移。
  const toImg = (clientX: number, clientY: number): number[] => {
    const p = viewer.viewport.windowToImageCoordinates(new OpenSeadragon.Point(clientX, clientY))
    const size = viewer.world.getItemAt(0)?.getContentSize()
    const w = size?.x ?? Infinity
    const h = size?.y ?? Infinity
    return [Math.max(0, Math.min(w, p.x)), Math.max(0, Math.min(h, p.y))]
  }

  // 键盘:V 移动 / R 矩形 / P 多边形 / E 编辑 / Esc 取消 / Del 删除选中。
  useEffect(() => {
    if (readOnly) return
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      if (e.ctrlKey || e.metaKey || e.altKey) return
      const k = e.key.toLowerCase()
      if (k === 'r') setTool('bbox')
      else if (k === 'p') setTool('polygon')
      else if (k === 'v') setTool('move')
      else if (k === 'e') setTool('edit')
      else if (k === 'm' && mpp > 0) setTool('ruler')
      else if (e.key === 'Escape') { setDraftBbox(null); setDraftPoly([]); setRulerA(null); onSelect?.(null) }
      else if ((e.key === 'Delete' || e.key === 'Backspace') && selectedId && onUpdateShapes) {
        e.preventDefault()
        onUpdateShapes(shapes.filter((s) => s.id !== selectedId))
        onSelect?.(null)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [readOnly, selectedId, shapes]) // eslint-disable-line react-hooks/exhaustive-deps

  const onPointerDown = (e: React.PointerEvent) => {
    if (!interactive) return
    downRef.current = { x: e.clientX, y: e.clientY }
    if (tool === 'bbox') {
      const p = toImg(e.clientX, e.clientY)
      setDraftBbox({ start: p, cur: p })
      ;(e.currentTarget as SVGElement).setPointerCapture(e.pointerId)
    }
  }

  const onPointerMove = (e: React.PointerEvent) => {
    if (vertexDrag.current) {
      const [nx, ny] = toImg(e.clientX, e.clientY)
      const d = vertexDrag.current
      setEditDraft(shapes.map((s) => {
        if (s.id !== d.shapeId) return s
        if (d.type === 'poly') return { ...s, points: s.points.map((p, i) => (i === d.vi ? [nx, ny] : p)) }
        return { ...s, points: [[nx, ny], d.opp] }
      }))
    } else if (bodyDrag.current) {
      const [cx, cy] = toImg(e.clientX, e.clientY)
      const d = bodyDrag.current
      const dx = cx - d.startImg[0]
      const dy = cy - d.startImg[1]
      setEditDraft(shapes.map((s) => (s.id === d.shapeId ? { ...s, points: d.orig.map(([px, py]) => [px + dx, py + dy]) } : s)))
    } else if (tool === 'bbox' && draftBbox) {
      setDraftBbox({ start: draftBbox.start, cur: toImg(e.clientX, e.clientY) })
    }
  }

  const onPointerUp = (e: React.PointerEvent) => {
    if (readOnly) return
    const moved = downRef.current ? Math.hypot(e.clientX - downRef.current.x, e.clientY - downRef.current.y) > 4 : false
    if (vertexDrag.current) {
      if (editDraft && onUpdateShapes) onUpdateShapes(editDraft)
      setEditDraft(null); vertexDrag.current = null
    } else if (bodyDrag.current) {
      if (moved && editDraft && onUpdateShapes) onUpdateShapes(editDraft)
      setEditDraft(null); bodyDrag.current = null
    } else if (tool === 'bbox' && draftBbox) {
      const [x1, y1] = draftBbox.start
      const [x2, y2] = draftBbox.cur
      if (Math.abs(x2 - x1) > 3 && Math.abs(y2 - y1) > 3) {
        onCommitShape?.({ id: `bbox-${Date.now()}`, kind: 'bbox', label: activeLabel, points: [[x1, y1], [x2, y2]], source: 'manual', color: activeColor })
      }
      setDraftBbox(null)
    } else if (tool === 'polygon' && !moved) {
      setDraftPoly((pts) => [...pts, toImg(e.clientX, e.clientY)])
    } else if (tool === 'ruler' && !moved) {
      const p = toImg(e.clientX, e.clientY)
      if (!rulerA) {
        setRulerA(p)
      } else {
        const um = lengthMicrons(rulerA[0], rulerA[1], p[0], p[1], mpp)
        if (um != null) setMeasurements((m) => [...m, { a: rulerA, b: p, um }])
        setRulerA(null)
      }
    }
    downRef.current = null
  }

  const finishPolygon = () => {
    if (draftPoly.length >= 3) {
      onCommitShape?.({ id: `poly-${Date.now()}`, kind: 'polygon', label: activeLabel, points: draftPoly, source: 'manual', color: activeColor })
    }
    setDraftPoly([])
  }

  const renderShapes = editDraft ?? shapes
  const invScale = affine ? 1 / affine.scale : 1 // image 半径 = 屏幕像素 / scale
  const handleR = HANDLE_PX * invScale
  const handleShape = tool === 'edit' && selectedId ? renderShapes.find((s) => s.id === selectedId) : null

  const tools: { id: Tool; icon: React.ReactNode; title: string }[] = [
    { id: 'move', icon: <Move className="h-4 w-4" />, title: '移动/平移 (V)' },
    { id: 'bbox', icon: <Square className="h-4 w-4" />, title: '矩形框 (R)' },
    { id: 'polygon', icon: <Hexagon className="h-4 w-4" />, title: '多边形 (P)' },
    { id: 'edit', icon: <MousePointer2 className="h-4 w-4" />, title: '选择/编辑顶点 (E)' },
    // 尺子:mpp 未知时不出现——绝不给一个编出来的标尺(C1.1a/C3.25 纪律)。
    ...(canMeasure ? [{ id: 'ruler' as Tool, icon: <Ruler className="h-4 w-4" />, title: '测量 (M)' }] : []),
  ]

  return (
    <>
      {/* 工具栏(左上,浮在查看器上) */}
      {!readOnly && (
        <div className="absolute left-4 top-4 z-20 flex flex-col gap-2">
          <div className="flex flex-col gap-1 rounded-lg border p-1 backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', opacity: 0.95 }}>
            {tools.map((t) => (
              <button key={t.id} title={t.title} data-testid={`slide-tool-${t.id}`} onClick={() => setTool(t.id)}
                className="flex h-9 w-9 items-center justify-center rounded-md transition-colors"
                style={{ background: tool === t.id ? 'var(--primary)' : 'transparent', color: tool === t.id ? 'var(--primary-foreground)' : 'var(--foreground)' }}>
                {t.icon}
              </button>
            ))}
          </div>
          {tool === 'polygon' && draftPoly.length > 0 && (
            <button onClick={finishPolygon} data-testid="slide-poly-finish"
              className="flex items-center gap-1 rounded-lg border px-2.5 py-1.5 text-xs backdrop-blur"
              style={{ background: 'var(--card)', borderColor: 'var(--border)' }}>
              <Check className="h-3.5 w-3.5" style={{ color: 'var(--chart-2)' }} />完成 ({draftPoly.length} 点)
            </button>
          )}
          {tool === 'edit' && (
            <div className="rounded-lg border px-2 py-1.5 text-[11px] backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', color: 'var(--muted-foreground)', maxWidth: 150 }}>
              点击选中 · 拖顶点改形状 · 拖框身平移 · Del 删除
            </div>
          )}
          {tool === 'ruler' && (
            <div className="flex flex-col gap-1 rounded-lg border px-2 py-1.5 text-[11px] backdrop-blur" style={{ background: 'var(--card)', borderColor: 'var(--border)', color: 'var(--muted-foreground)', maxWidth: 160 }}>
              <span>点两下量一段长度（{mpp.toFixed(3)} µm/px）</span>
              {measurements.length > 0 && (
                <button data-testid="slide-ruler-clear" onClick={() => { setMeasurements([]); setRulerA(null) }} className="flex items-center gap-1 text-left">
                  <Eraser className="h-3 w-3" />清除测量（{measurements.length}）
                </button>
              )}
            </div>
          )}
        </div>
      )}

      {/* SVG 叠加层:move 下 pointer-events:none(放行 OSD);绘制/编辑下截获。 */}
      <svg
        ref={svgRef}
        data-testid="slide-overlay"
        className="absolute inset-0 h-full w-full"
        style={{ pointerEvents: interactive ? 'auto' : 'none', cursor: tool === 'polygon' || tool === 'bbox' ? 'crosshair' : 'default' }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
      >
        {affine && (
          <g data-testid="slide-overlay-g" data-affine={`${affine.scale},${affine.ox},${affine.oy}`} transform={affineToMatrix(affine)}>
            {renderShapes.map((s, i) => {
              const c = s.color || labelColor?.(s.label) || 'var(--chart-1)'
              const sel = s.id === selectedId
              const canHit = tool === 'edit' && !readOnly
              const common = {
                fill: c, fillOpacity: sel ? 0.35 : 0.18, stroke: c, strokeWidth: sel ? 3 : 2,
                vectorEffect: 'non-scaling-stroke' as const,
                style: { pointerEvents: (canHit ? 'auto' : 'none') as React.CSSProperties['pointerEvents'], cursor: canHit ? 'move' : 'default' },
                onPointerDown: canHit ? (e: React.PointerEvent) => {
                  e.stopPropagation()
                  downRef.current = { x: e.clientX, y: e.clientY }
                  onSelect?.(s.id)
                  bodyDrag.current = { shapeId: s.id, startImg: toImg(e.clientX, e.clientY), orig: s.points }
                  ;(e.currentTarget as SVGElement).setPointerCapture(e.pointerId)
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

            {/* 顶点把手(单选 + 编辑工具);r 随缩放反算成 image 半径,屏幕恒定大小 */}
            {handleShape && handleShape.kind === 'bbox' && handleShape.points.length >= 2 && (() => {
              const b = bboxOf(handleShape.points)
              const corners = [
                { x: b.xmin, y: b.ymin, opp: [b.xmax, b.ymax] },
                { x: b.xmax, y: b.ymin, opp: [b.xmin, b.ymax] },
                { x: b.xmin, y: b.ymax, opp: [b.xmax, b.ymin] },
                { x: b.xmax, y: b.ymax, opp: [b.xmin, b.ymin] },
              ]
              return corners.map((cn, i) => (
                <circle key={`h${i}`} cx={cn.x} cy={cn.y} r={handleR} fill="#fff" stroke="var(--primary)" strokeWidth={2} vectorEffect="non-scaling-stroke"
                  style={{ pointerEvents: 'auto', cursor: 'nwse-resize' }}
                  onPointerDown={(e) => { e.stopPropagation(); vertexDrag.current = { shapeId: handleShape.id, type: 'bbox', opp: cn.opp }; (e.currentTarget as SVGElement).setPointerCapture(e.pointerId) }} />
              ))
            })()}
            {handleShape && handleShape.kind === 'polygon' && handleShape.points.map((p, i) => (
              <circle key={`h${i}`} cx={p[0]} cy={p[1]} r={handleR} fill="#fff" stroke="var(--primary)" strokeWidth={2} vectorEffect="non-scaling-stroke"
                style={{ pointerEvents: 'auto', cursor: 'grab' }}
                onPointerDown={(e) => { e.stopPropagation(); vertexDrag.current = { shapeId: handleShape.id, type: 'poly', vi: i }; (e.currentTarget as SVGElement).setPointerCapture(e.pointerId) }} />
            ))}

            {/* bbox 绘制预览 */}
            {draftBbox && (() => {
              const [[x1, y1], [x2, y2]] = [draftBbox.start, draftBbox.cur]
              const x = Math.min(x1, x2), y = Math.min(y1, y2), w = Math.abs(x2 - x1), h = Math.abs(y2 - y1)
              return <rect x={x} y={y} width={w} height={h} fill="#000" fillOpacity={0.06} stroke="#fff" strokeWidth={2} vectorEffect="non-scaling-stroke" strokeDasharray="6 4" />
            })()}
            {/* polygon 绘制预览 */}
            {draftPoly.length > 0 && (
              <>
                <polyline points={draftPoly.map((p) => p.join(',')).join(' ')} fill="none" stroke="#fff" strokeWidth={2} vectorEffect="non-scaling-stroke" />
                {draftPoly.map((p, i) => <circle key={i} cx={p[0]} cy={p[1]} r={handleR} fill="#fff" stroke="#111" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />)}
              </>
            )}

            {/* 测量:线 + 端点 + 读数(µm/mm)。线用 non-scaling-stroke、字号与端点半径
                随缩放反算,屏幕上恒定大小。读数带描边(paint-order)保证在任何组织上都清晰。 */}
            {measurements.map((m, i) => {
              const mx = (m.a[0] + m.b[0]) / 2
              const my = (m.a[1] + m.b[1]) / 2
              return (
                <g key={`m${i}`} data-testid="slide-measurement">
                  <line x1={m.a[0]} y1={m.a[1]} x2={m.b[0]} y2={m.b[1]} stroke="#f8fafc" strokeWidth={2.5} vectorEffect="non-scaling-stroke" />
                  <line x1={m.a[0]} y1={m.a[1]} x2={m.b[0]} y2={m.b[1]} stroke="#111" strokeWidth={1} vectorEffect="non-scaling-stroke" strokeDasharray="6 4" />
                  {[m.a, m.b].map((p, j) => <circle key={j} cx={p[0]} cy={p[1]} r={handleR * 0.7} fill="#f8fafc" stroke="#111" strokeWidth={1} vectorEffect="non-scaling-stroke" />)}
                  <text x={mx} y={my} dy={-6 * invScale} textAnchor="middle" fontSize={13 * invScale} fill="#f8fafc" stroke="#111" strokeWidth={3} vectorEffect="non-scaling-stroke" style={{ paintOrder: 'stroke', userSelect: 'none' }}>
                    {formatMicrons(m.um)}
                  </text>
                </g>
              )
            })}
            {rulerA && <circle cx={rulerA[0]} cy={rulerA[1]} r={handleR * 0.7} fill="#38bdf8" stroke="#fff" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />}
          </g>
        )}
      </svg>
    </>
  )
}
