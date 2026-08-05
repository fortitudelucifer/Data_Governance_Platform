import { useEffect, useMemo, useRef } from 'react'

import { planeAspect, planeSliceCount, type MPRPlane, type Vec3 } from '@/lib/mprGeometry'
import { renderPlane, type MaskOverlay } from '@/lib/mprRender'
import { displayToPlaneUV, planeOrientation } from '@/lib/mprOrientation'

// MPR 单视图(执行方案-04 · C3.2b)。一个平面 = 一块画布:重切 + 窗位 → RGBA 上屏。
//
// 两条放射科肌肉记忆(别按图片画布的习惯做):
//   · 滚轮 = 上下切片(不是缩放);Ctrl+滚轮 = 缩放。
//   · 各向异性体素要按物理尺寸拉伸显示,否则圆病灶显示成椭圆(planeAspect)。
//
// 画布像素 1:1 对应体素(不预缩放),几何/拾取因此是精确整数;显示尺寸交给 CSS,
// 缩放不影响坐标计算。

const PLANE_LABEL: Record<MPRPlane, string> = {
  axial: '轴位 Axial',
  coronal: '冠状 Coronal',
  sagittal: '矢状 Sagittal',
}

export interface MPRViewProps {
  volume: Uint16Array
  dims: Vec3
  spacing: Vec3
  plane: MPRPlane
  slice: number
  lut: Uint8Array
  /** 面内十字位置(u,v),用于画定位线。 */
  crossU: number
  crossV: number
  zoom: number
  onSliceChange: (next: number) => void
  onZoomChange: (next: number) => void
  /** 在该平面点选 → 面内坐标(u,v),由父组件换算回体素十字。 */
  onPick: (u: number, v: number) => void
  /** 掩膜叠加层(与影像体同布局),三视共用同一份,天然对齐。 */
  /** 3×3 体素→世界方向矩阵(volume_meta.direction),决定显示朝向(C-H3)。 */
  direction: number[]
  /**
   * 掩膜叠加层走**稳定 ref + 版本号**,而不是"每次新建的数组 prop"。
   *
   * ⚠️ 这不是洁癖,是踩出来的:把一个持有 11 MB TypedArray 的**新数组**作为
   * 变化的 prop 传进来,React 渲染阶段能跑完(三个视图的函数体都执行了),
   * 但**提交阶段会把整个渲染进程卡死**——canvas 的 effect 再也不执行,页面
   * 连 `1+1` 都算不了,连 CDP 的 CPU profile 都取不回来。逐段二分才定位到:
   * 换成极小 buffer 就正常,换成稳定 ref 也正常。
   *
   * 所以:ref 的身份恒定,只有 overlayRev 这个数字在变,大缓冲区永远不参与
   * prop 的变更比较。新增任何大体量数据(标签体、点云…)都照此办理。
   */
  overlaysRef?: { current: MaskOverlay[] }
  overlayRev?: number
  /**
   * 涂抹模式:仅在 axial 开启(关键帧按 z 存,轴位是它的自然平面)。
   * 拖动时持续回调面内坐标;父组件据此往掩膜体里刷。
   */
  paintMode?: boolean
  onPaint?: (u: number, v: number, erase: boolean) => void
  onPaintEnd?: () => void
  /**
   * 已完成的测量（本平面本层的），画成线 + 端点。
   * 覆盖层用 SVG 而不是画进 canvas：测量不是影像内容，混进像素里既无法单独
   * 擦除，也会被 16-bit → RGBA 的窗位映射污染。
   */
  overlayShapes?: MeasureShape[]
  /** 测量进行中的临时点（点了第一下还没点第二下）。 */
  pendingPoints?: { u: number; v: number }[]
}

/** 一条待渲染的测量图形（面内体素坐标）。 */
export interface MeasureShape {
  id: string
  points: { u: number; v: number }[]
  label: string
  active: boolean
}

export function MPRView({
  volume,
  dims,
  spacing,
  plane,
  slice,
  lut,
  crossU,
  crossV,
  zoom,
  onSliceChange,
  onZoomChange,
  onPick,
  direction,
  overlaysRef,
  overlayRev = 0,
  paintMode = false,
  onPaint,
  onPaintEnd,
  overlayShapes,
  pendingPoints,
}: MPRViewProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const wrapRef = useRef<HTMLDivElement>(null)
  const painting = useRef(false)
  const total = planeSliceCount(dims, plane)
  const aspect = planeAspect(dims, spacing, plane)
  // 朝向:把体素顺序摆成解剖惯例(前上为上、病人左在画面右),并给出四边标签。
  const orient = useMemo(() => planeOrientation(direction, plane), [direction, plane])

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const { rgba, width, height } = renderPlane(volume, dims, plane, slice, lut, overlaysRef?.current, orient)
    canvas.width = width
    canvas.height = height
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    // 先建再拷:直接 new ImageData(rgba,…) 会撞上 TS 对 TypedArray buffer 类型的
    // 泛型约束(ArrayBufferLike ≠ ArrayBuffer),拷贝一次既过类型也不影响性能。
    const img = ctx.createImageData(width, height)
    img.data.set(rgba)
    ctx.putImageData(img, 0, 0)
  }, [volume, dims, plane, slice, lut, overlayRev, orient])

  // 滚轮必须挂**原生非被动**监听:React 的 onWheel 是 passive 的,里面调
  // preventDefault() 完全无效——表现就是 Ctrl+滚轮缩放了整个浏览器页面而不是
  // 画布(用户实测踩到)。passive:false 才能真正拦下浏览器的默认缩放/滚动。
  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      if (e.ctrlKey) {
        onZoomChange(clamp(zoom * (e.deltaY < 0 ? 1.1 : 1 / 1.1), 0.25, 8))
        return
      }
      // 滚轮 = 上下切片(放射科肌肉记忆)
      onSliceChange(clamp(slice + (e.deltaY > 0 ? 1 : -1), 0, total - 1))
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [zoom, slice, total, onZoomChange, onSliceChange])

  /** 显示坐标 → 画布(体素)坐标:除以显示尺寸再乘画布分辨率。 */
  const toVoxelUV = (e: React.MouseEvent<HTMLCanvasElement>): [number, number] | null => {
    const canvas = canvasRef.current
    if (!canvas) return null
    const r = canvas.getBoundingClientRect()
    const du = Math.floor(((e.clientX - r.left) / r.width) * canvas.width)
    const dv = Math.floor(((e.clientY - r.top) / r.height) * canvas.height)
    // **必须过一次反翻转**:画布上看到的位置 ≠ 体素面内坐标。漏了这一步,
    // 点选与涂抹会落在镜像位置——画面上笔刷跟手，实际改的是对侧体素。
    return displayToPlaneUV(orient, du, dv, canvas.width, canvas.height)
  }

  const handleMouseDown = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const uv = toVoxelUV(e)
    if (!uv) return
    if (paintMode && onPaint) {
      painting.current = true
      // 右键 / Alt = 擦除(标注员的通用手感)。
      onPaint(uv[0], uv[1], e.button === 2 || e.altKey)
      return
    }
    onPick(uv[0], uv[1])
  }

  const handleMouseMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
    if (!painting.current || !paintMode || !onPaint) return
    const uv = toVoxelUV(e)
    if (uv) onPaint(uv[0], uv[1], e.altKey || (e.buttons & 2) !== 0)
  }

  const endPaint = () => {
    if (!painting.current) return
    painting.current = false
    onPaintEnd?.()
  }

  // 显示尺寸:宽固定,高按物理宽高比(各向异性不压扁)。
  const displayW = 320 * zoom
  const displayH = displayW * aspect

  return (
    // min-w-0 不能省:作为 grid/flex 子项时默认 min-width:auto,画布放大后
    // (displayW = 320*zoom)会把列**撑破**而不是触发内层的 overflow-auto,
    // 表现就是影像横向溢出屏幕边框。归零后超出部分才由下面那层自己滚。
    <div className="flex min-w-0 flex-col gap-1" ref={wrapRef}>
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{PLANE_LABEL[plane]}</span>
        <span>
          {slice + 1} / {total}
        </span>
      </div>
      <div className="relative overflow-auto bg-black" style={{ maxHeight: 420 }}>
        <canvas
          ref={canvasRef}
          onMouseDown={handleMouseDown}
          onMouseMove={handleMouseMove}
          onMouseUp={endPaint}
          onMouseLeave={endPaint}
          onContextMenu={(e) => e.preventDefault()} // 右键用于擦除，不弹菜单
          style={{
            width: displayW,
            height: displayH,
            imageRendering: 'pixelated',
            display: 'block',
            cursor: paintMode ? 'cell' : 'crosshair',
          }}
        />
        {/* 十字定位线:体素坐标 →(同一翻转)→ 显示坐标,与画面保持一致。 */}
        {(() => {
          const cw = canvasRef.current?.width || 1
          const ch = canvasRef.current?.height || 1
          const [du, dv] = displayToPlaneUV(orient, crossU, crossV, cw, ch)
          return (
            <>
              <div
                className="pointer-events-none absolute bg-cyan-400/70"
                style={{ left: 0, width: displayW, height: 1, top: ((dv + 0.5) / ch) * displayH }}
              />
              <div
                className="pointer-events-none absolute bg-cyan-400/70"
                style={{ top: 0, height: displayH, width: 1, left: ((du + 0.5) / cw) * displayW }}
              />
            </>
          )
        })()}
        {/* 测量覆盖层。坐标要过与影像**同一次**翻转（displayToPlaneUV），
            否则尺子画在一个地方、量的是另一个地方——线看着正常，数字是别处的。 */}
        {(overlayShapes?.length || pendingPoints?.length) ? (
          <svg
            className="pointer-events-none absolute left-0 top-0"
            width={displayW}
            height={displayH}
            data-testid="measure-overlay"
          >
            {[...(overlayShapes ?? []),
              ...(pendingPoints?.length
                ? [{ id: '__pending', points: pendingPoints, label: '', active: true }]
                : [])].map((sh) => {
              const cw = canvasRef.current?.width || 1
              const ch = canvasRef.current?.height || 1
              const pts = sh.points.map((p) => {
                const [du, dv] = displayToPlaneUV(orient, p.u, p.v, cw, ch)
                return [((du + 0.5) / cw) * displayW, ((dv + 0.5) / ch) * displayH] as const
              })
              const color = sh.active ? '#fbbf24' : '#22d3ee'
              return (
                <g key={sh.id}>
                  {pts.length > 1 && (
                    <polyline
                      points={pts.map(([x, y]) => `${x},${y}`).join(' ')}
                      fill="none"
                      stroke={color}
                      strokeWidth={1.5}
                    />
                  )}
                  {pts.map(([x, y], i) => (
                    <circle key={i} cx={x} cy={y} r={3} fill={color} />
                  ))}
                  {sh.label && pts.length > 0 && (
                    <text
                      x={pts[pts.length - 1][0] + 6}
                      y={pts[pts.length - 1][1] - 6}
                      fill={color}
                      fontSize={11}
                      stroke="#000"
                      strokeWidth={0.5}
                      paintOrder="stroke"
                    >
                      {sh.label}
                    </text>
                  )}
                </g>
              )
            })}
          </svg>
        ) : null}

        {/* 方位标签(C-H3 的安全面):即使有人对翻转约定有异议,标签也让他一眼
            看出哪边是哪边——左右翻转看不出来,但标签能。 */}
        <span className="pointer-events-none absolute left-1/2 top-0.5 -translate-x-1/2 text-[10px] font-bold text-cyan-300">{orient.labels.top}</span>
        <span className="pointer-events-none absolute bottom-0.5 left-1/2 -translate-x-1/2 text-[10px] font-bold text-cyan-300">{orient.labels.bottom}</span>
        <span className="pointer-events-none absolute left-0.5 top-1/2 -translate-y-1/2 text-[10px] font-bold text-cyan-300">{orient.labels.left}</span>
        <span className="pointer-events-none absolute right-0.5 top-1/2 -translate-y-1/2 text-[10px] font-bold text-cyan-300">{orient.labels.right}</span>
      </div>
    </div>
  )
}

function clamp(x: number, lo: number, hi: number) {
  return x < lo ? lo : x > hi ? hi : x
}
