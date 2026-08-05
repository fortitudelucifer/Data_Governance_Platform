import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import OpenSeadragon from 'openseadragon'
import { ArrowLeft, AlertTriangle, Loader2, Trash2, Crosshair, Download, Sparkles, Eye, EyeOff } from 'lucide-react'

import { taskApi, type Shape } from '@/api/imageTask'
import { trackApi, type TrackUpsert, type VideoKeyframe, type VideoTrack } from '@/api/videoTask'
import { fetchSlideMeta, type SlideMeta } from '@/api/slide'
import { exportSlideRegions } from '@/api/export'
import { SlideViewer } from '@/components/domain/slide/SlideViewer'
import { SlideAnnotationOverlay } from '@/components/domain/slide/SlideAnnotationOverlay'
import { SlideCellsOverlay } from '@/components/domain/slide/SlideCellsOverlay'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import * as perms from '@/lib/roles'
import { useEditLock } from '@/hooks/useEditLock'

// 可编辑的任务态(与 image/volume 一致):待认领 / 进行中 / 被驳回返工。
// 其它态(审核中、已完成、已归档)对标注员只读——复核员看到的也是只读区域。
const EDITABLE_STATES = new Set(['HUMAN_PENDING', 'HUMAN_IN_PROGRESS', 'QA_REJECTED'])

// 病理全切片**区域标注**工作台(C1.3)。任务粒度 = ROI(C-Q4 拍板);区域(手绘、
// 几十/片)走**现有 polygon/bbox 轨迹**——零后端改动,与视频/图片同一载荷层。
//
// 几何一律存**原生 level-0 像素**:与瓦片端点、导出、bbox/polygon 轨迹同空间。
// 视口归 OSD(SlideViewer),标注在其上的 SVG 叠加层(SlideAnnotationOverlay);
// 两者由 OSD 视口每帧同步(osdOverlayTransform),坐标不漂。

const REGION_COLORS = ['#ef4444', '#f97316', '#eab308', '#22c55e', '#06b6d4', '#3b82f6', '#a855f7', '#ec4899']

interface TrackRef {
  objectId?: string // 后端轨迹 id(新建未保存时为空)
  trackNum?: number // 每任务逻辑 track 号(服务端分配)
  version?: number // 乐观锁版本
}

function chunk2(flat: number[]): number[][] {
  const out: number[][] = []
  for (let i = 0; i + 1 < flat.length; i += 2) out.push([flat[i], flat[i + 1]])
  return out
}
function bboxOf(points: number[][]) {
  const xs = points.map((p) => p[0]), ys = points.map((p) => p[1])
  return { xmin: Math.min(...xs), ymin: Math.min(...ys), xmax: Math.max(...xs), ymax: Math.max(...ys) }
}
function errText(e: unknown): string {
  const d = (e as { response?: { data?: { message?: string; error?: string } } })?.response?.data
  return d?.message ?? d?.error ?? (e instanceof Error ? e.message : String(e))
}

/** 轨迹(单关键帧 frame=0)→ 画布 Shape。轨迹元数据挂在 attrs 里供保存回写。 */
function trackToShape(t: VideoTrack): Shape | null {
  const kf = t.keyframes?.[0]
  if (!kf) return null
  const ref: TrackRef = { objectId: t.id, trackNum: t.track_id, version: t.version }
  if (t.kind === 'polygon' && kf.points && kf.points.length >= 6) {
    return { id: t.id, kind: 'polygon', label: t.label, points: chunk2(kf.points), color: t.color, source: t.source, attrs: { ref } }
  }
  if (kf.bbox && kf.bbox.length === 4) {
    const [x, y, w, h] = kf.bbox
    return { id: t.id, kind: 'bbox', label: t.label, points: [[x, y], [x + w, y + h]], color: t.color, source: t.source, attrs: { ref } }
  }
  return null
}

/** Shape → 单关键帧轨迹 upsert(几何在 level-0 像素;WSI 无时间维,ts_ms=0)。 */
function shapeToUpsert(s: Shape): TrackUpsert {
  const ref = (s.attrs?.ref ?? {}) as TrackRef
  const kf: VideoKeyframe = { frame: 0, ts_ms: 0, outside: false, occluded: false, source: 'human' }
  if (s.kind === 'polygon') kf.points = s.points.flat()
  else { const b = bboxOf(s.points); kf.bbox = [b.xmin, b.ymin, b.xmax - b.xmin, b.ymax - b.ymin] }
  return { id: ref.objectId, track_id: ref.trackNum, label: s.label || '区域', kind: s.kind, color: s.color, keyframes: [kf], version: ref.version }
}

const geomKey = (s: Shape) => `${s.kind}|${s.label}|${s.color}|${JSON.stringify(s.points)}`

export function SlideAnnotationPage() {
  const { id } = useParams<{ id: string }>()
  const taskId = Number(id)
  const navigate = useNavigate()

  const { data: task } = useQuery({ queryKey: ['task', taskId], queryFn: () => taskApi.get(taskId) })
  const assetId = task?.asset_id
  const role = useAuthStore((s) => s.user)?.role ?? ''
  // #11:接编辑锁——两名标注员同时打开同一任务,后到者只读(与视频/图片工作台一致);
  // 否则两人都可编辑/删除/检测,既有区域持续冲突、新区域双写。
  const editableState = !!task && EDITABLE_STATES.has(task.state)
  const { readOnly: lockedByOther } = useEditLock(taskId, perms.canAnnotate(role) && editableState)
  const editable = perms.canAnnotate(role) && editableState && !lockedByOther
  const { data: meta, error: metaError } = useQuery<SlideMeta>({
    queryKey: ['slide-meta', assetId],
    queryFn: () => fetchSlideMeta(assetId!),
    enabled: !!assetId,
  })

  const [viewer, setViewer] = useState<OpenSeadragon.Viewer | null>(null)
  const [shapes, setShapes] = useState<Shape[]>([])
  const shapesRef = useRef<Shape[]>([])
  shapesRef.current = shapes
  const inflight = useRef<Set<string>>(new Set())          // 正在 PUT 的 shape id(#10 串行)
  const pendingSave = useRef<Map<string, Shape>>(new Map()) // 每个 shape 排队的最新一版
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [newLabel, setNewLabel] = useState('区域')
  const [newColor, setNewColor] = useState(REGION_COLORS[0])
  const [saveMsg, setSaveMsg] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  // C2.3 细胞:cells 轨迹(每个 ROI 一条,instances[] 装该 ROI 全部细胞)。
  const [cellTracks, setCellTracks] = useState<VideoTrack[]>([])
  const [hiddenCells, setHiddenCells] = useState<Set<string>>(new Set())
  const [detecting, setDetecting] = useState(false)

  // 载入既有轨迹:区域(polygon/bbox)走 shapes,细胞(cells)单独留成 VideoTrack[]。
  const { data: tracks, refetch: refetchTracks } = useQuery({ queryKey: ['slide-tracks', taskId], queryFn: () => trackApi.list(taskId), enabled: !!taskId })
  useEffect(() => {
    if (!tracks) return
    setShapes(tracks.map(trackToShape).filter((s): s is Shape => s != null))
    setCellTracks(tracks.filter((t) => t.kind === 'cells'))
  }, [tracks])

  const labelColor = useCallback((label?: string) => shapesRef.current.find((s) => s.label === label)?.color || REGION_COLORS[0], [])

  // 保存一个 shape(创建或更新轨迹),**按 shape 串行**(#10):同一区域在飞的保存没回来
  // 前,新改动只排队(合并成最新一版),回来后用**新拿到的 version** 存。否则:① 快速改标签
  // = 每字符一次并发 PUT、同一旧 version → 首个成功余下 409;② 新区域 objectId 还没回写就再
  // 改 = 又 create 一条重复轨迹。串行 + 合并把两者都关死,顺带天然去抖。回写 attrs.ref。
  const saveShape = useCallback(async (s: Shape) => {
    const key = s.id
    if (inflight.current.has(key)) {
      pendingSave.current.set(key, s) // 只留最新一版,等在飞的回来再存
      return
    }
    inflight.current.add(key)
    setSaving(true); setSaveMsg(null)
    try {
      const saved = await trackApi.put(taskId, shapeToUpsert(s))
      const ref: TrackRef = { objectId: saved.id, trackNum: saved.track_id, version: saved.version }
      setShapes((prev) => prev.map((x) => (x.id === s.id ? { ...x, id: saved.id, attrs: { ...x.attrs, ref } } : x)))
      if (selectedId === s.id) setSelectedId(saved.id)
      setSaveMsg('已保存')
      inflight.current.delete(key)
      // create 后 id 从客户端 id 变成 server id,两个键都查;用新 ref(新 version)存排队版。
      const q = pendingSave.current.get(key) ?? pendingSave.current.get(saved.id)
      if (q) {
        pendingSave.current.delete(key); pendingSave.current.delete(saved.id)
        void saveShape({ ...q, id: saved.id, attrs: { ...q.attrs, ref } })
      }
    } catch (e) {
      inflight.current.delete(key); pendingSave.current.delete(key)
      const status = (e as { response?: { status?: number } })?.response?.status
      if (status === 409) {
        // 版本冲突(别处改过)。**真重载**最新轨迹——以前只 setShapes(自己) 装样子、version
        // 还是旧的,于是后续保存一直 409(注释说"重载"实则没有,本项目典型的假注释)。
        setSaveMsg('别处已改动,已重载最新')
        await refetchTracks()
      } else {
        setSaveMsg('保存失败：' + errText(e))
      }
    } finally {
      setSaving(false)
    }
  }, [taskId, selectedId, refetchTracks])

  const handleCommit = useCallback((shape: Shape) => {
    setShapes((prev) => [...prev, shape])
    setSelectedId(shape.id)
    void saveShape(shape)
  }, [saveShape])

  // 编辑/删除:叠加层每次手势结束给全量 shapes;diff 出改动/删除的轨迹分别保存/删除。
  const handleUpdate = useCallback((next: Shape[]) => {
    const prev = shapesRef.current
    const removed = prev.filter((o) => !next.find((n) => n.id === o.id))
    const changed = next.filter((n) => {
      const o = prev.find((x) => x.id === n.id)
      return o && geomKey(o) !== geomKey(n)
    })
    setShapes(next)
    for (const r of removed) {
      const ref = (r.attrs?.ref ?? {}) as TrackRef
      if (ref.objectId) void trackApi.remove(taskId, ref.objectId).catch((e) => setSaveMsg('删除失败：' + errText(e)))
    }
    for (const c of changed) void saveShape(c)
  }, [saveShape, taskId])

  // 面板:改标签/颜色 → 更新 shape 并保存。
  const patchShape = useCallback((sid: string, patch: Partial<Shape>) => {
    const next = shapesRef.current.map((s) => (s.id === sid ? { ...s, ...patch } : s))
    setShapes(next)
    const target = next.find((s) => s.id === sid)
    if (target) void saveShape(target)
  }, [saveShape])

  const removeShape = useCallback((sid: string) => {
    handleUpdate(shapesRef.current.filter((s) => s.id !== sid))
    if (selectedId === sid) setSelectedId(null)
  }, [handleUpdate, selectedId])

  // C1.36 定位:把视口飞到某区域(46000px 切片上,没有定位就得滚轮碰运气找回画过的
  // 区域;复核员据此"跳到标注")。区域 bbox(image px)→ 视口矩形 → fitBounds。
  const locateShape = useCallback((s: Shape) => {
    if (!viewer) return
    const b = bboxOf(s.points)
    const pad = Math.max((b.xmax - b.xmin), (b.ymax - b.ymin)) * 0.4 || 200
    const rect = new OpenSeadragon.Rect(b.xmin - pad, b.ymin - pad, (b.xmax - b.xmin) + pad * 2, (b.ymax - b.ymin) + pad * 2)
    viewer.viewport.fitBounds(viewer.viewport.imageToViewportRectangle(rect), false)
  }, [viewer])

  // C1.4 导出 GeoJSON(QuPath)。工作台里区域尚未过 QA → 导草稿(?source=draft);
  // 已定稿的任务导快照。导不出(如没快照)把后端文案原样呈现。
  const [exporting, setExporting] = useState(false)
  const exportGeoJSON = useCallback(async () => {
    setExporting(true); setSaveMsg(null)
    try {
      await exportSlideRegions(taskId, editable)
    } catch (e) {
      setSaveMsg('导出失败：' + errText(e))
    } finally {
      setExporting(false)
    }
  }, [taskId, editable])

  // C2.3 在当前视野检测细胞:视口 → level-0 像素 ROI → detectCells → 重载轨迹。
  // 视野即 ROI(C-Q4 任务粒度=ROI);太大(细胞数超上限)后端 400 指路缩小 ROI。
  const detectCellsInView = useCallback(async () => {
    if (!viewer) return
    setDetecting(true); setSaveMsg(null)
    try {
      const b = viewer.viewport.viewportToImageRectangle(viewer.viewport.getBounds())
      const W = meta?.width ?? Number.MAX_SAFE_INTEGER, H = meta?.height ?? Number.MAX_SAFE_INTEGER
      const x = Math.max(0, b.x), y = Math.max(0, b.y)
      const roi = [x, y, Math.min(W - x, b.width), Math.min(H - y, b.height)]
      const res = await trackApi.detectCells(taskId, { roi, mpp: meta?.mpp })
      setSaveMsg(`检出 ${res.cells} 个细胞`)
      await refetchTracks()
    } catch (e) {
      setSaveMsg('检测失败：' + errText(e))
    } finally {
      setDetecting(false)
    }
  }, [viewer, taskId, meta, refetchTracks])

  const removeCellTrack = useCallback(async (trackId: string) => {
    try { await trackApi.remove(taskId, trackId); await refetchTracks() } catch (e) { setSaveMsg('删除失败：' + errText(e)) }
  }, [taskId, refetchTracks])

  const toggleCellTrack = useCallback((trackId: string) => {
    setHiddenCells((prev) => {
      const next = new Set(prev)
      if (next.has(trackId)) next.delete(trackId)
      else next.add(trackId)
      return next
    })
  }, [])

  // 定位到某 cells 轨迹的 ROI(attrs.roi="x_y_w_h";缺失则从实例算 bbox)。
  const locateCellTrack = useCallback((t: VideoTrack) => {
    if (!viewer) return
    let x = 0, y = 0, w = 0, h = 0
    const m = /^(\d+)_(\d+)_(\d+)_(\d+)$/.exec((t.attrs?.roi as string) || '')
    if (m) { x = +m[1]; y = +m[2]; w = +m[3]; h = +m[4] } else {
      const insts = t.keyframes?.[0]?.instances ?? []
      if (!insts.length) return
      let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity
      for (const c of insts) {
        if (!c.bbox) continue
        x0 = Math.min(x0, c.bbox[0]); y0 = Math.min(y0, c.bbox[1])
        x1 = Math.max(x1, c.bbox[0] + c.bbox[2]); y1 = Math.max(y1, c.bbox[1] + c.bbox[3])
      }
      x = x0; y = y0; w = x1 - x0; h = y1 - y0
    }
    const pad = Math.max(w, h) * 0.2 || 100
    viewer.viewport.fitBounds(viewer.viewport.imageToViewportRectangle(new OpenSeadragon.Rect(x - pad, y - pad, w + pad * 2, h + pad * 2)), false)
  }, [viewer])

  const totalCells = useMemo(() => cellTracks.reduce((n, t) => n + (t.keyframes?.[0]?.instances?.length ?? 0), 0), [cellTracks])

  const back = () => (task?.dataset_id ? navigate(`/datasets/${task.dataset_id}/assets`) : navigate(-1))
  const regionCount = useMemo(() => shapes.length, [shapes])

  if (metaError) {
    return (
      <div className="p-6">
        <p className="text-sm text-red-600">切片几何读取失败：{errText(metaError)}(派生 slide_meta 是否就绪?)</p>
        <Button variant="outline" className="mt-3" onClick={back}><ArrowLeft className="mr-1 h-4 w-4" />返回</Button>
      </div>
    )
  }

  return (
    <div data-testid="slide-annot-page" className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b px-4 py-2">
        <Button variant="outline" size="sm" onClick={back}><ArrowLeft className="mr-1 h-4 w-4" />返回</Button>
        {meta && (
          <span className="text-xs text-muted-foreground">
            {meta.width.toLocaleString()}×{meta.height.toLocaleString()} px · {meta.levels.length} 层 · {meta.vendor || '未知厂商'} ·{' '}
            {meta.mpp > 0 ? `${meta.mpp.toFixed(3)} µm/px（${meta.magnification || '?'}×）` : '标尺未知（无 MPP）'}
          </span>
        )}
        {meta?.has_label_image && (
          <span className="flex items-center gap-1 rounded border border-amber-500 bg-amber-50 px-2 py-0.5 text-xs text-amber-900">
            <AlertTriangle className="h-3 w-3" />含标签/宏观照——真实临床切片须先剥离 PHI
          </span>
        )}
        {editable && (
          <Button variant="outline" size="sm" className="ml-auto" onClick={detectCellsInView} disabled={detecting || !viewer}
            title="AI 检测当前视野内的细胞(视野即 ROI；太大会被拒,请先放大到一个 ROI)">
            <Sparkles className="mr-1 h-4 w-4" />{detecting ? '检测中…' : '检测细胞(当前视野)'}
          </Button>
        )}
        <Button variant="outline" size="sm" className={editable ? '' : 'ml-auto'} onClick={exportGeoJSON} disabled={exporting || regionCount === 0}
          title="导出 GeoJSON（QuPath 可直接导入；工作台里导的是当前草稿）">
          <Download className="mr-1 h-4 w-4" />{exporting ? '导出中…' : '导出 GeoJSON'}
        </Button>
        <span data-testid="slide-save-status" className="text-xs text-muted-foreground">
          {saving ? '保存中…' : saveMsg ?? `${regionCount} 个区域`}
        </span>
      </div>

      <div className="flex min-h-0 flex-1">
        {/* 查看器 + 标注叠加层(同一 relative 盒,叠加层 absolute inset-0 覆盖) */}
        <div className="relative min-h-0 min-w-0 flex-1 bg-black">
          {!meta && (
            <div className="absolute inset-0 z-10 flex items-center justify-center gap-2 text-sm text-white/70">
              <Loader2 className="h-4 w-4 animate-spin" />正在加载切片…
            </div>
          )}
          {meta && assetId != null && (
            <SlideViewer assetId={assetId} meta={meta} onReady={setViewer} />
          )}
          {/* 细胞叠加层(canvas)在区域 SVG 之下:细胞是细节,区域边界压在其上 */}
          {viewer && <SlideCellsOverlay viewer={viewer} cellTracks={cellTracks} hidden={hiddenCells} />}
          {viewer && (
            <SlideAnnotationOverlay
              viewer={viewer}
              shapes={shapes}
              readOnly={!editable}
              mpp={meta?.mpp ?? 0}
              activeLabel={newLabel}
              activeColor={newColor}
              labelColor={labelColor}
              onCommitShape={handleCommit}
              onUpdateShapes={handleUpdate}
              selectedId={selectedId}
              onSelect={setSelectedId}
            />
          )}
        </div>

        {/* 右侧区域面板 */}
        <aside className="flex w-72 min-h-0 shrink-0 flex-col overflow-auto border-l">
          {editable ? (
            <div className="border-b p-3">
              <div className="mb-2 text-xs font-medium text-muted-foreground">新建区域</div>
              <div className="flex items-center gap-2">
                <input value={newLabel} onChange={(e) => setNewLabel(e.target.value)} placeholder="标签"
                  className="h-8 w-32 rounded border px-2 text-sm outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                <input type="color" value={newColor} onChange={(e) => setNewColor(e.target.value)} title="新建区域颜色"
                  className="h-8 w-8 cursor-pointer rounded border p-0.5" style={{ borderColor: 'var(--input)' }} />
              </div>
              <div className="mt-2 flex flex-wrap gap-1">
                {REGION_COLORS.map((c) => (
                  <button key={c} title={c} onClick={() => setNewColor(c)}
                    className="h-4 w-4 rounded-sm border transition-transform hover:scale-110"
                    style={{ background: c, borderWidth: newColor.toLowerCase() === c ? 2 : 1, borderColor: newColor.toLowerCase() === c ? 'var(--foreground)' : 'transparent' }} />
                ))}
              </div>
              <p className="mt-2 text-[11px] text-muted-foreground">工具栏在左上：R 矩形 · P 多边形 · V 平移 · E 编辑</p>
            </div>
          ) : (
            <div data-testid="slide-readonly-banner" className="border-b p-3 text-xs text-muted-foreground">
              只读:此任务当前不可编辑（{task?.state ?? '…'}）。可平移缩放查看区域。
            </div>
          )}

          <div className="flex items-center justify-between px-3 py-2 text-xs font-medium text-muted-foreground">
            <span>区域（{regionCount}）</span>
            {selectedId && <span className="text-[11px]">已选中 1 个</span>}
          </div>
          <ul data-testid="slide-region-list" className="flex-1">
            {shapes.length === 0 && <li className="px-3 py-6 text-center text-xs text-muted-foreground">还没有区域。用左上工具栏画一个。</li>}
            {shapes.map((s) => (
              <li key={s.id} data-testid="slide-region-item"
                onClick={() => setSelectedId(s.id)}
                className="flex cursor-pointer items-center gap-2 border-b px-3 py-2 text-sm"
                style={{ background: s.id === selectedId ? 'var(--accent)' : 'transparent' }}>
                {editable ? (
                  <input type="color" value={s.color || REGION_COLORS[0]} onClick={(e) => e.stopPropagation()}
                    onChange={(e) => patchShape(s.id, { color: e.target.value })}
                    className="h-5 w-5 shrink-0 cursor-pointer rounded border p-0" title="改颜色" style={{ borderColor: 'var(--border)' }} />
                ) : (
                  <span className="h-3.5 w-3.5 shrink-0 rounded-sm border" style={{ background: s.color || REGION_COLORS[0], borderColor: 'var(--border)' }} />
                )}
                <input value={s.label || ''} readOnly={!editable} onClick={(e) => e.stopPropagation()}
                  onChange={(e) => editable && patchShape(s.id, { label: e.target.value })}
                  className="h-6 min-w-0 flex-1 rounded border px-1.5 text-sm outline-none" style={{ borderColor: 'transparent', background: 'transparent' }} />
                <span className="shrink-0 text-[11px] text-muted-foreground">{s.kind === 'bbox' ? '矩形' : '多边形'}</span>
                <button title="定位到此区域" data-testid="slide-region-locate" onClick={(e) => { e.stopPropagation(); setSelectedId(s.id); locateShape(s) }}
                  className="shrink-0 rounded p-1 text-muted-foreground hover:text-primary"><Crosshair className="h-3.5 w-3.5" /></button>
                {editable && (
                  <button title="删除" onClick={(e) => { e.stopPropagation(); removeShape(s.id) }}
                    className="shrink-0 rounded p-1 text-muted-foreground hover:text-red-600"><Trash2 className="h-3.5 w-3.5" /></button>
                )}
              </li>
            ))}
          </ul>

          {/* C2.3 细胞面板:每条 = 一个 ROI 的 AI 检测,显示细胞数,可显隐/定位/删除。
              细胞几何画在 canvas 叠加层上(数量大,不进 DOM)。 */}
          <div className="flex items-center justify-between border-t px-3 py-2 text-xs font-medium text-muted-foreground">
            <span>细胞（{totalCells}）</span>
            {cellTracks.length > 0 && <span className="text-[11px]">{cellTracks.length} 个 ROI</span>}
          </div>
          <ul data-testid="slide-cell-list">
            {cellTracks.length === 0 && (
              <li className="px-3 py-4 text-center text-[11px] text-muted-foreground">
                {editable ? '放大到一个 ROI，点上方「检测细胞」' : '暂无细胞检测'}
              </li>
            )}
            {cellTracks.map((t) => {
              const n = t.keyframes?.[0]?.instances?.length ?? 0
              const isHidden = hiddenCells.has(t.id)
              return (
                <li key={t.id} data-testid="slide-cell-item" className="flex items-center gap-2 border-b px-3 py-2 text-sm">
                  <Sparkles className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate">{t.label || 'AI 细胞'}</span>
                  <span className="shrink-0 text-[11px] text-muted-foreground">{n} 核</span>
                  <button title={isHidden ? '显示' : '隐藏'} onClick={() => toggleCellTrack(t.id)}
                    className="shrink-0 rounded p-1 text-muted-foreground hover:text-foreground">
                    {isHidden ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                  </button>
                  <button title="定位到此 ROI" onClick={() => locateCellTrack(t)}
                    className="shrink-0 rounded p-1 text-muted-foreground hover:text-primary"><Crosshair className="h-3.5 w-3.5" /></button>
                  {editable && (
                    <button title="删除" onClick={() => removeCellTrack(t.id)}
                      className="shrink-0 rounded p-1 text-muted-foreground hover:text-red-600"><Trash2 className="h-3.5 w-3.5" /></button>
                  )}
                </li>
              )
            })}
          </ul>
        </aside>
      </div>
    </div>
  )
}
