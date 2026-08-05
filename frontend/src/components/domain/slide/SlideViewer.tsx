import { useEffect, useRef } from 'react'
import OpenSeadragon from 'openseadragon'

import { fetchSlideMeta, tileUrl, type SlideMeta } from '@/api/slide'
import { buildSlideTileSource, tileExistsAt } from '@/lib/slideTileSource'

// WSI 深缩放查看器(C1.2c)。用 OpenSeadragon 而不是手写:平移/缩放/瓦片裁剪/
// 惯性/内存回收都是它成熟的部分,手写全是静默错的高发区。
//
// 两个非标配置,都由 C1.1c 的定案逼出来:
//   ① **瓦片端点要 JWT** → 不能用裸 <img>,必须 loadTilesWithAjax + Bearer 头。
//   ② **原生层是 ×4 不是 ×2** → 自定义 TileSource:标准 DZI 层呈现,但只有原生层
//      对应的 DZI 层有真瓦片(tileExists 门),其余层 OSD 从最近粗层放大。层映射
//      数学在 slideTileSource.ts,已单测 + 变异锁死。

interface Props {
  assetId: number
  /** slide_meta 可由父组件预取传入;不传则自取。 */
  meta?: SlideMeta
  /** 供 C1.3 标注层挂载:OSD 实例就绪时回调。 */
  onReady?: (viewer: OpenSeadragon.Viewer) => void
}

export function SlideViewer({ assetId, meta: metaProp, onReady }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)
  const viewerRef = useRef<OpenSeadragon.Viewer | null>(null)

  useEffect(() => {
    let cancelled = false
    const host = hostRef.current
    if (!host) return

    const start = (meta: SlideMeta) => {
      if (cancelled) return
      const spec = buildSlideTileSource(meta)
      const token = localStorage.getItem('token') ?? ''

      // 自定义 TileSource:标准 DZI 骨架 + 原生层门。
      const tileSource = {
        width: spec.width,
        height: spec.height,
        tileSize: spec.tileSize,
        tileOverlap: 0,
        minLevel: spec.minLevel, // 最粗原生层,不是 0(否则 fit-zoom 什么都不加载)
        maxLevel: spec.maxLevel,
        // 只有原生层对应的 DZI 层有真瓦片;其余 false → OSD 从最近粗层放大填充,
        // 零服务端计算。这是"不预生成 DZI"能成立的关键。
        tileExists: (level: number, x: number, y: number) => tileExistsAt(spec, meta, level, x, y),
        getTileUrl: (level: number, x: number, y: number) => {
          const nativeIdx = spec.dziToNative.get(level)
          // tileExists 已保证只有存在的层会走到这里;防御性回退到 level0。
          return tileUrl(assetId, nativeIdx ?? 0, x, y)
        },
      }

      const viewer = OpenSeadragon({
        element: host,
        tileSources: tileSource as unknown as OpenSeadragon.TileSourceOptions,
        // 瓦片端点要鉴权 → 走 ajax 带 Bearer,而不是 <img src>(带不了头)。
        loadTilesWithAjax: true,
        ajaxHeaders: token ? { Authorization: `Bearer ${token}` } : {},
        crossOriginPolicy: false,
        showNavigator: true,
        navigatorPosition: 'BOTTOM_RIGHT',
        showRotationControl: false,
        gestureSettingsMouse: { clickToZoom: false, dblClickToZoom: true },
        // 放射/病理惯例:滚轮缩放要顺手,别太灵敏。
        zoomPerScroll: 1.3,
        minZoomImageRatio: 0.8,
        maxZoomPixelRatio: 2, // 允许放大到 2× 像素(40× 切片下看细胞边界)
        animationTime: 0.4,
        // 大切片的瓦片缓存:别把内存吃光,也别频繁丢造成来回加载。
        maxImageCacheCount: 400,
      })
      viewerRef.current = viewer

      // #9:R/F 会旋转/翻转底图,但标注与细胞叠加层的仿射只表达平移+缩放(recoverAffine
      // 取 x 轴两点、不含旋转)——底图一转,整层标注就错位落到别的组织上,且不报错。OSD
      // 默认键盘绑了这些键(showRotationControl:false 只藏了按钮、没禁键),这里拦掉它们的
      // 默认动作。标注页自己的 R=矩形工具等快捷键走另一套 window keydown,不受影响。
      viewer.addHandler('canvas-key', (e) => {
        const k = (e as unknown as { originalEvent?: KeyboardEvent }).originalEvent?.key
        if (k === 'r' || k === 'R' || k === 'f' || k === 'F') {
          ;(e as unknown as { preventDefaultAction: boolean }).preventDefaultAction = true
        }
      })

      // 瓦片加载信号 → 宿主 data 属性。
      // ⚠️ OSD 6 默认 WebGL 渲染器**不发 'tile-drawn'**(加了会报错),所以用
      // 'tile-loaded'(瓦片取回并**解码成图**)。它同时验证了 C1.1c 命门①:拼过
      // JPEGTables 头的瓦片能在真浏览器里解码——解不了会走 tile-load-failed。
      // 这两个计数也给 C1.3/加载指示器一个进度信号。
      let loaded = 0
      viewer.addHandler('tile-loaded', () => {
        loaded += 1
        host.dataset.tilesLoaded = String(loaded)
      })
      viewer.addHandler('tile-load-failed', () => {
        host.dataset.tileFailed = String(Number(host.dataset.tileFailed || 0) + 1)
      })
      onReady?.(viewer)
    }

    if (metaProp) {
      start(metaProp)
    } else {
      void fetchSlideMeta(assetId)
        .then((m) => start(m))
        .catch(() => {
          /* 页面层负责错误 UI;这里只是不启动 */
        })
    }

    return () => {
      cancelled = true
      viewerRef.current?.destroy()
      viewerRef.current = null
    }
  }, [assetId, metaProp, onReady])

  return <div ref={hostRef} data-testid="slide-viewer" className="h-full w-full bg-black" />
}
