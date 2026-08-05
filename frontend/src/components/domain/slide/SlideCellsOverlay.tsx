import { useCallback, useEffect, useRef } from 'react'
import OpenSeadragon from 'openseadragon'

import type { VideoTrack } from '@/api/videoTask'
import { imageToScreen, recoverAffine } from '@/lib/osdOverlayTransform'
import { decodeCOCORLE } from '@/lib/cocoRLE'

// 病理细胞叠加层(C2.3)。一个 ROI 的 cells 轨迹在其单关键帧的 instances[] 里装了
// 该 ROI 全部细胞(~几百;整片可达 6.7 万)。
//
// # 为什么用 canvas 而不是 per-cell SVG
//
// 把上万个实例当**变化的 prop** 交给 React 渲染成 SVG,会在**每帧视口更新**时
// reconcile 上万个节点 → 渲染进程卡死(C3.3 / 终端评审都踩过「大数组当 prop」)。
// 所以:cells 数据放**身份恒定的 ref**,视口每帧只做一次**命令式 canvas 重绘**
// (画上万个矩形是毫秒级),React 只负责挂载这一个 <canvas>。这正是那条纪律的落地。
//
// # 坐标同步
//
// 与区域叠加层同一套:屏幕位置唯一真源 = OSD 自己的 imageToViewerElement,每帧
// 重算仿射,细胞 bbox 在 level-0 像素空间 → imageToScreen 摆上屏,绝不另算视口数学。
// 视口外的细胞跳过绘制(整片 6.7 万时只画视野内那批)。

// 类别→色。stub 产 tumor/immune/stroma;真模型的类别也走这套映射,未知类别落到黄色。
const CLASS_COLORS: Record<string, string> = {
  tumor: '#ef4444',
  immune: '#3b82f6',
  stroma: '#22c55e',
  epithelial: '#a855f7',
}
function classColor(c?: string): string {
  return (c && CLASS_COLORS[c]) || '#eab308'
}
function hexRgb(hex: string): [number, number, number] {
  const h = hex.replace('#', '')
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]
}

// 掩膜低于这个屏幕像素尺寸就只画 bbox 框:fit 缩放下细胞是亚像素,画掩膜既看不见又要
// 白解上万个 RLE。够大(放大复核时)才解码画权威掩膜。
const MASK_MIN_PX = 6

// cellMaskCanvas 把一个细胞的 bbox-local RLE 解成一张 bbox 尺寸的彩色掩膜画布(带缓存)。
// decodeCOCORLE 不便宜,按 class+counts 缓存,别每帧重解。解不出 → 'error'(显式暴露)。
type CellLike = { class?: string; rle?: { size: number[]; counts: string } | null }
function cellMaskCanvas(cache: Map<string, HTMLCanvasElement | 'error'>, c: CellLike): HTMLCanvasElement | 'error' | null {
  if (!c.rle || !c.rle.counts || c.rle.size.length < 2) return null
  const key = `${c.class || ''}|${c.rle.counts}`
  const hit = cache.get(key)
  if (hit) return hit
  let out: HTMLCanvasElement | 'error'
  try {
    const { mask, h, w } = decodeCOCORLE({ size: [c.rle.size[0], c.rle.size[1]], counts: c.rle.counts })
    const cv = document.createElement('canvas')
    cv.width = w
    cv.height = h
    const cx = cv.getContext('2d')
    if (!cx) {
      out = 'error'
    } else {
      const img = cx.createImageData(w, h) // 与 decodeCOCORLE 一样 row-major,下标直接对应
      const [r, g, b] = hexRgb(classColor(c.class))
      for (let i = 0; i < w * h; i++) {
        if (mask[i]) {
          img.data[i * 4] = r
          img.data[i * 4 + 1] = g
          img.data[i * 4 + 2] = b
          img.data[i * 4 + 3] = 150
        }
      }
      cx.putImageData(img, 0, 0)
      out = cv
    }
  } catch {
    out = 'error' // 位串解不出(坏 RLE)——标出来,别静默当没有掩膜
  }
  cache.set(key, out)
  return out
}

interface Props {
  viewer: OpenSeadragon.Viewer
  cellTracks: VideoTrack[]
  /** 隐藏的 cells 轨迹 id(面板可逐条显隐)。 */
  hidden?: Set<string>
}

export function SlideCellsOverlay({ viewer, cellTracks, hidden }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  // 大数组放 ref(恒定身份),命令式重绘从这里读;绝不把它当变化的 prop 进 React 渲染。
  const dataRef = useRef<{ tracks: VideoTrack[]; hidden?: Set<string> }>({ tracks: [] })
  dataRef.current = { tracks: cellTracks, hidden }
  // 解码后的 bbox-local 掩膜画布缓存(#16);cellTracks 变时清,防涨。
  const maskCache = useRef<Map<string, HTMLCanvasElement | 'error'>>(new Map())

  const draw = useCallback(() => {
    const canvas = canvasRef.current
    const host = canvas?.parentElement
    if (!canvas || !host) return
    const dpr = window.devicePixelRatio || 1
    const w = host.clientWidth
    const h = host.clientHeight
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
      canvas.width = Math.round(w * dpr)
      canvas.height = Math.round(h * dpr)
      canvas.style.width = `${w}px`
      canvas.style.height = `${h}px`
    }
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, w, h)

    let affine
    try {
      const p00 = viewer.viewport.imageToViewerElementCoordinates(new OpenSeadragon.Point(0, 0))
      const p10 = viewer.viewport.imageToViewerElementCoordinates(new OpenSeadragon.Point(1, 0))
      affine = recoverAffine(p00, p10)
    } catch {
      return // 视口未就绪
    }
    const { tracks, hidden } = dataRef.current
    for (const t of tracks) {
      if (hidden?.has(t.id)) continue
      const insts = t.keyframes?.[0]?.instances
      if (!insts) continue
      for (const c of insts) {
        if (!c.bbox || c.bbox.length < 4) continue
        const [sx, sy] = imageToScreen(affine, c.bbox[0], c.bbox[1])
        const sw = c.bbox[2] * affine.scale
        const sh = c.bbox[3] * affine.scale
        if (sx + sw < 0 || sy + sh < 0 || sx > w || sy > h) continue // 视口裁剪
        // 够大且有 RLE → 画**权威掩膜**(#16),复核员才看得出错位/坏分割;太小(fit 下亚
        // 像素)或无 RLE → 只画 bbox 框。解码失败用洋红框显式暴露,不静默当没有掩膜。
        if (sw >= MASK_MIN_PX && sh >= MASK_MIN_PX && c.rle && c.rle.counts) {
          const mc = cellMaskCanvas(maskCache.current, c)
          if (mc === 'error') {
            ctx.strokeStyle = '#e11d9a'
            ctx.lineWidth = 2
            ctx.strokeRect(sx, sy, sw, sh)
            continue
          }
          if (mc) {
            ctx.imageSmoothingEnabled = false // 最近邻:高倍下每个掩膜像素是清晰方块
            ctx.drawImage(mc, sx, sy, sw, sh)
          }
        }
        ctx.strokeStyle = classColor(c.class)
        ctx.lineWidth = 1
        ctx.strokeRect(sx, sy, sw, sh)
      }
    }
  }, [viewer])

  // OSD 视口每帧变化 → rAF 合并后重绘一次。
  useEffect(() => {
    let raf = 0
    const schedule = () => {
      if (raf) return
      raf = requestAnimationFrame(() => {
        raf = 0
        draw()
      })
    }
    viewer.addHandler('update-viewport', schedule)
    viewer.addHandler('resize', schedule)
    viewer.addHandler('open', schedule)
    schedule()
    return () => {
      if (raf) cancelAnimationFrame(raf)
      viewer.removeHandler('update-viewport', schedule)
      viewer.removeHandler('resize', schedule)
      viewer.removeHandler('open', schedule)
    }
  }, [viewer, draw])

  // cellTracks 变 → 丢掩膜缓存(counts 变 = 键变,顺手清防涨);仅数据变,不随显隐清。
  useEffect(() => {
    maskCache.current.clear()
  }, [cellTracks])

  // cells / 显隐变化 → 立即重绘一次(数据变了,不等下次视口事件)。
  useEffect(() => {
    draw()
  }, [cellTracks, hidden, draw])

  // 纯展示,不吃指针(OSD 照常平移缩放;区域 SVG 叠加层在其上)。
  return <canvas ref={canvasRef} data-testid="slide-cells-canvas" className="pointer-events-none absolute inset-0" />
}
