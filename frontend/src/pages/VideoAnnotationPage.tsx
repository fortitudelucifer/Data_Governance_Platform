import React, { useEffect, useMemo, useRef, useState, useCallback } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ArrowLeft, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, Play, Pause,
  SkipBack, SkipForward, Save, Send, Lock, Loader2, Trash2, Check, EyeOff, Eye,
  Undo2, Redo2, Diamond, X, Sparkles, Copy, ArrowRightToLine, Filter,
  Square, SquareDashed, HelpCircle, Maximize, AlertTriangle,
} from 'lucide-react'
import { taskApi, TASK_STATE_LABELS, TASK_STATE_COLOR, type Shape } from '@/api/imageTask'
import { assetApi } from '@/api/asset'
import { datasetApi } from '@/api/dataset'
import { videoApi, trackApi, type VideoTrack, type VideoKeyframe } from '@/api/videoTask'
import { interpolateAt, sortKeyframes, type Keyframe } from '@/lib/trackInterpolation'
import { InteractiveCanvas } from '@/components/domain/image-annotation/InteractiveCanvas'
import { useEditLock } from '@/hooks/useEditLock'
import { useResizablePanel, SplitHandle } from '@/components/common/ResizablePanel'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useAuthStore } from '@/stores/auth'
import { canAnnotate as roleCanAnnotate, canReview as roleCanReview } from '@/lib/roles'
import { videoAIConfigApi, queueFull } from '@/api/videoAIConfig'
import { workbenchBackTo } from '@/lib/navBack'
import { seekTimeMs, timeToFrame as frameAtTime } from '@/lib/frameIndex'
import { ReviewCommentPanel } from '@/components/domain/video-annotation/ReviewCommentPanel'
import { ReworkDiffPanel } from '@/components/domain/video-annotation/ReworkDiffPanel'

const EDITABLE_STATES = new Set(['HUMAN_PENDING', 'HUMAN_IN_PROGRESS', 'QA_REJECTED'])
const PALETTE = ['#e6194B', '#3cb44b', '#4363d8', '#f58231', '#911eb4', '#42d4f4', '#f032e6', '#bfef45']
const SPEEDS = [0.25, 0.5, 1, 1.5, 2, 4]
const PLAYBACK_SPEED_KEY = 'video:playbackSpeed'
const FRAME_STEP_KEY = 'video:frameStep'
const MAX_FRAME_STEP = 1000
const MARKER_BUDGET = 700
// det-server 可选：检测模型 × 追踪器（固定由 sidecar 提供）。
const DET_MODELS = [{ v: 'yolo', label: 'YOLO26x（默认）' }, { v: 'rtdetr', label: 'RT-DETR' }]
const DET_TRACKERS = [{ v: 'botsort', label: 'BoT-SORT（ReID·推荐）' }, { v: 'bytetrack', label: 'ByteTrack（快）' }]
// 采样间隔：每 N 帧检测一次（关键帧密度 vs 速度/待校对量的权衡）。中间帧线性插值。
const DET_STEPS = [
  { v: 1, label: '每帧（最密·最慢）' }, { v: 2, label: '每 2 帧' }, { v: 5, label: '每 5 帧（默认）' },
  { v: 10, label: '每 10 帧' }, { v: 15, label: '每 15 帧' }, { v: 30, label: '每 30 帧（最疏·最快）' },
]
const DET_STEP_KEY = 'video.detStep'
// COCO-80 类：作为视频标签建议（det 检出的类都在其中），叠加到数据集本体之上。
const COCO_CLASSES = [
  'person', 'bicycle', 'car', 'motorcycle', 'airplane', 'bus', 'train', 'truck', 'boat', 'traffic light',
  'fire hydrant', 'stop sign', 'parking meter', 'bench', 'bird', 'cat', 'dog', 'horse', 'sheep', 'cow',
  'elephant', 'bear', 'zebra', 'giraffe', 'backpack', 'umbrella', 'handbag', 'tie', 'suitcase', 'frisbee',
  'skis', 'snowboard', 'sports ball', 'kite', 'baseball bat', 'baseball glove', 'skateboard', 'surfboard', 'tennis racket', 'bottle',
  'wine glass', 'cup', 'fork', 'knife', 'spoon', 'bowl', 'banana', 'apple', 'sandwich', 'orange',
  'broccoli', 'carrot', 'hot dog', 'pizza', 'donut', 'cake', 'chair', 'couch', 'potted plant', 'bed',
  'dining table', 'toilet', 'tv', 'laptop', 'mouse', 'remote', 'keyboard', 'cell phone', 'microwave', 'oven',
  'toaster', 'sink', 'refrigerator', 'book', 'clock', 'vase', 'scissors', 'teddy bear', 'hair drier', 'toothbrush',
]

type TrackDraft = VideoTrack & { _key: string; _dirty: boolean; _hidden?: boolean; _locked?: boolean }
type Geom = { bbox?: number[]; points?: number[] }

type RVFCMeta = { mediaTime: number; presentedFrames: number }
type RVFCVideo = HTMLVideoElement & {
  requestVideoFrameCallback?: (cb: (now: number, meta: RVFCMeta) => void) => number
  cancelVideoFrameCallback?: (h: number) => void
}

const chunk2 = (flat: number[]): number[][] => {
  const out: number[][] = []
  for (let i = 0; i + 1 < flat.length; i += 2) out.push([flat[i], flat[i + 1]])
  return out
}
const bboxOf = (pts: number[][]) => {
  const xs = pts.map((p) => p[0]), ys = pts.map((p) => p[1])
  const xmin = Math.min(...xs), ymin = Math.min(...ys)
  return [xmin, ymin, Math.max(...xs) - xmin, Math.max(...ys) - ymin]
}
// mask = 逐帧稠密分割（SAM2 传播的产物），几何上就是一条多边形轮廓；
// polygon = 手画的多边形。渲染/导出都按多边形处理（B1 收尾⑤）。
const isPolyKind = (k?: string) => k === 'polygon' || k === 'mask'
const shapeToGeom = (s: Shape): Geom => (s.kind === 'polygon' ? { points: s.points.flat() } : { bbox: bboxOf(s.points) })
const flatOf = (g: Geom | null): number[] => (!g ? [] : (g.bbox ?? g.points ?? []))
const geomChanged = (a: Geom | null, b: Geom): boolean => {
  const fa = flatOf(a), fb = flatOf(b)
  if (fa.length !== fb.length) return true
  for (let i = 0; i < fa.length; i++) if (Math.abs(fa[i] - fb[i]) > 0.5) return true
  return false
}
const fmtTC = (ms: number): string => {
  const t = Math.max(0, Math.round(ms))
  const m = Math.floor(t / 60000), s = Math.floor((t % 60000) / 1000), mm = t % 1000
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}.${String(mm).padStart(3, '0')}`
}
const readStoredNumber = (key: string, fallback: number): number => {
  try {
    if (typeof window === 'undefined') return fallback
    const value = Number(window.localStorage.getItem(key))
    return Number.isFinite(value) ? value : fallback
  } catch {
    return fallback
  }
}
const writeStoredNumber = (key: string, value: number) => {
  try {
    window.localStorage.setItem(key, String(value))
  } catch {
    // localStorage may be unavailable in hardened/private browser contexts.
  }
}
const clampFrameStep = (value: number) => Math.max(1, Math.min(MAX_FRAME_STEP, Math.round(value) || 1))
const readStoredSpeed = () => {
  const value = readStoredNumber(PLAYBACK_SPEED_KEY, 1)
  return SPEEDS.includes(value) ? value : 1
}
// 低置信阈值：ai_score 低于此值的 track 才进审核导航队列。
const LOW_CONF_KEY = 'video:lowConfThreshold'
const readStoredThreshold = () => {
  const v = readStoredNumber(LOW_CONF_KEY, 0.5)
  return v > 0 && v <= 1 ? v : 0.5
}

export function VideoAnnotationPage() {
  const { id } = useParams<{ id: string }>()
  const taskId = Number(id)
  const navigate = useNavigate()
  const location = useLocation()
  const role = useAuthStore((s) => s.user?.role ?? '')
  const canAnnotate = roleCanAnnotate(role)
  const canReview = roleCanReview(role)

  const { data: task } = useQuery({ queryKey: ['task', taskId], queryFn: () => taskApi.get(taskId) })
  const { data: asset } = useQuery({
    queryKey: ['asset-detail', task?.asset_id], queryFn: () => assetApi.detail(task!.asset_id), enabled: !!task?.asset_id,
    refetchInterval: (q) => (q.state.data && q.state.data.preprocess_status !== 'ready' ? 2000 : false),
  })
  const { data: frameIndex } = useQuery({
    queryKey: ['frame-index', task?.asset_id],
    queryFn: () => videoApi.getFrameIndex(task!.asset_id).catch(() => null),
    enabled: !!task?.asset_id && asset?.preprocess_status === 'ready',
  })
  const { data: ontology } = useQuery({
    queryKey: ['ontology', task?.dataset_id], queryFn: () => datasetApi.getOntology(task!.dataset_id),
    enabled: !!task?.dataset_id, staleTime: 5 * 60 * 1000,
  })
  const { data: adjacent } = useQuery({ queryKey: ['adjacent', taskId], queryFn: () => taskApi.getAdjacent(taskId, true) })
  // 顶栏「返回」与「提交/审核完成后没有下一条」共用同一个去处：来处 → 上一级 → /my-tasks
  const backTo = workbenchBackTo(location.state, task?.dataset_id)
  // 标注员是按队列干活的：交完这条就直接进下一条，没有下一条才离开工作台。
  // 三个模态必须一致——此前视频提交后总是硬跳「我的任务」，图片却回来处。
  const advanceOrLeave = () => {
    if (adjacent?.next_task_id) navigate(`/video-tasks/${adjacent.next_task_id}`, { state: location.state })
    else navigate(backTo)
  }
  const { data: serverTracks } = useQuery({ queryKey: ['tracks', taskId], queryFn: () => trackApi.list(taskId) })

  // B2.8 成本闸门：数据集可能关掉 AI 预标注；GPU 队列可能已满。两者都要在
  // 用户点下去之前就说清楚，而不是让他撞一个 400/429。
  const { data: videoAICfg } = useQuery({
    queryKey: ['video-ai-config', task?.dataset_id],
    queryFn: () => videoAIConfigApi.get(task!.dataset_id),
    enabled: !!task?.dataset_id, staleTime: 5 * 60 * 1000,
  })
  const { data: gpuQueues } = useQuery({
    queryKey: ['gpu-queue'],
    queryFn: () => videoAIConfigApi.gpuQueue(),
    refetchInterval: 5000, // 队列是别人也在改的共享状态
  })
  const aiDisabled = videoAICfg?.trigger === 'off'
  const detQueue = gpuQueues?.['video.detect_track']
  const detQueueFull = queueFull(detQueue)

  const { width: panelWidth, startDrag, containerRef: splitRef } = useResizablePanel('video.panelWidth', { initial: 340 })

  const editableState = !!task && EDITABLE_STATES.has(task.state)
  const { lock, readOnly: lockedByOther } = useEditLock(taskId, canAnnotate && editableState)
  const editable = canAnnotate && editableState && !lockedByOther

  const ptsMs = useMemo(() => frameIndex?.pts_ms ?? [], [frameIndex])
  const frameCount = frameIndex?.count ?? ptsMs.length
  const fps = frameIndex?.fps || asset?.fps || 30
  const durationMs = frameIndex?.duration_ms || asset?.duration_ms || (frameCount * 1000) / fps
  const displayW = frameIndex?.width || asset?.width || 0
  const displayH = frameIndex?.height || asset?.height || 0
  const ontologyLabels = useMemo(() => ontology?.labels ?? [], [ontology])

  const [tracks, setTracks] = useState<TrackDraft[]>([])
  const [selectedKey, setSelectedKey] = useState<string | null>(null)
  const [currentFrame, setCurrentFrame] = useState(0)
  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState(readStoredSpeed)
  const [lowConfThreshold, setLowConfThreshold] = useState(readStoredThreshold)
  const [step, setStep] = useState(() => clampFrameStep(readStoredNumber(FRAME_STEP_KEY, 10)))
  const [frameInput, setFrameInput] = useState('0')
  const [saving, setSaving] = useState(false)
  const [savedFlash, setSavedFlash] = useState(false)
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null)
  const [, setHistTick] = useState(0)

  const videoRef = useRef<RVFCVideo | null>(null)
  const rvfcRef = useRef<number | null>(null)
  const undoRef = useRef<TrackDraft[][]>([])
  const redoRef = useRef<TrackDraft[][]>([])
  const restoredFrameRef = useRef(false)
  const lastFrameKey = `video:lastFrame:${taskId}`

  // hydrate + reset history when server tracks (re)load
  useEffect(() => {
    if (!serverTracks) return
    setTracks(serverTracks.map((t) => ({ ...t, _key: t.id, _dirty: false })))
    undoRef.current = []; redoRef.current = []; setHistTick((n) => n + 1)
  }, [serverTracks])

  // AI detect+track (B2): trigger + batch adopt. Reload tracks from server after.
  const queryClient = useQueryClient()
  const reloadTracks = useCallback(() => queryClient.invalidateQueries({ queryKey: ['tracks', taskId] }), [queryClient, taskId])
  // SAM on the current frame: capture the displayed <video> pixels → PNG b64 →
  // backend traces the mask into a polygon (点选→多边形关键帧).
  const captureFrame = useCallback((): string | null => {
    const v = videoRef.current as HTMLVideoElement | null
    if (!v || !v.videoWidth) return null
    const c = document.createElement('canvas')
    c.width = v.videoWidth; c.height = v.videoHeight
    const ctx = c.getContext('2d'); if (!ctx) return null
    ctx.drawImage(v, 0, 0, c.width, c.height)
    try { return c.toDataURL('image/png').split(',')[1] || null } catch { return null }
  }, [])
  const samSegment = useCallback(async (points: number[][], box?: number[]) => {
    const img = captureFrame()
    if (!img) throw new Error('无法截取当前帧（视频未就绪？）')
    const res = await videoApi.segmentFrame(taskId, points, img, box)
    return { polygons: res.polygons }
  }, [captureFrame, taskId])
  // SAM2 跨帧传播：用当前帧的点提示，一键把物体传播成整条 mask track。
  const [autoAdopt, setAutoAdopt] = useState(() => localStorage.getItem('video.autoAdopt') === '1')
  const onPropagate = useCallback(async (points: number[][], label: string) => {
    setMsg(null)
    const r = await trackApi.propagate(taskId, { frame: currentFrame, points, label: label || 'object', auto_adopt: autoAdopt })
    await reloadTracks()
    setMsg({ kind: 'ok', text: autoAdopt
      ? `SAM2 传播完成：已直接生成人工 track（${r.keyframes} 帧 mask）`
      : `SAM2 传播完成：生成 AI track（${r.keyframes} 帧 mask），校对后采纳` })
  }, [taskId, currentFrame, reloadTracks, autoAdopt])
  const aiCount = useMemo(() => tracks.filter((t) => t.source === 'ai').length, [tracks])
  const [detModel, setDetModel] = useState(DET_MODELS[0].v)
  const [detTracker, setDetTracker] = useState(DET_TRACKERS[0].v)
  const [detStep, setDetStep] = useState(() => readStoredNumber(DET_STEP_KEY, 5))
  // 视频标签建议 = 数据集本体（规范名+色）+ 当前 track 已用/AI 检出类 + COCO-80，去重。
  const videoLabelOptions = useMemo(() => {
    const seen = new Set<string>()
    const out: { name: string; display?: string; color?: string }[] = []
    const add = (name?: string, display?: string, color?: string) => {
      if (name && !seen.has(name)) { seen.add(name); out.push({ name, display, color }) }
    }
    ontologyLabels.forEach((l: any) => add(l.name, l.display, l.color))
    tracks.forEach((t) => add((t.attrs?.ai_class as string) || t.label))
    COCO_CLASSES.forEach((c) => add(c))
    return out
  }, [ontologyLabels, tracks])
  const detectMut = useMutation({
    mutationFn: () => trackApi.detectTrack(taskId, { model: detModel, tracker: detTracker, sample_step: detStep }),
    onSuccess: (r) => {
      reloadTracks()
      queryClient.invalidateQueries({ queryKey: ['gpu-queue'] })
      setMsg({ kind: 'ok', text: `AI 预标注完成：写入 ${r.tracks_written} 个 track（校对后采纳）` })
    },
    onError: (e: any) => {
      queryClient.invalidateQueries({ queryKey: ['gpu-queue'] })
      // 429 = 队列满，不是失败——别让标注员以为自己弄坏了什么。
      const busy = e?.response?.status === 429
      setMsg({
        kind: 'err',
        text: busy ? `GPU 忙不过来了：${e.response.data?.message || '队列已满'}，稍后再点一次即可`
          : `AI 预标注失败：${e?.response?.data?.message || e?.message || '模型服务不可达'}`,
      })
    },
  })
  const adoptAllMut = useMutation({
    mutationFn: () => trackApi.adoptBatch(taskId, { all: true }),
    onSuccess: (r) => { reloadTracks(); setMsg({ kind: 'ok', text: `已采纳 ${r.adopted} 个 AI track` }) },
    onError: (e: any) => setMsg({ kind: 'err', text: `采纳失败：${e?.response?.data?.message || e?.message}` }),
  })
  useEffect(() => { setFrameInput(String(currentFrame)) }, [currentFrame])
  useEffect(() => { writeStoredNumber(LOW_CONF_KEY, lowConfThreshold) }, [lowConfThreshold])
  useEffect(() => {
    if (videoRef.current) videoRef.current.playbackRate = speed
    writeStoredNumber(PLAYBACK_SPEED_KEY, speed)
  }, [speed])
  useEffect(() => { writeStoredNumber(FRAME_STEP_KEY, step) }, [step])
  useEffect(() => { restoredFrameRef.current = false }, [taskId])

  const currentTsMs = ptsMs[currentFrame] ?? (currentFrame * 1000) / fps

  // 实现在 @/lib/frameIndex（有单测，且与 seekTimeMs 互为逆）
  const timeToFrame = useCallback((ms: number) => frameAtTime(ptsMs, fps, ms), [ptsMs, fps])

  const seekToFrame = useCallback((k: number) => {
    const kk = Math.max(0, Math.min(Math.max(0, frameCount - 1), k))
    setCurrentFrame(kk) // always move the playhead, even before the video is ready
    const v = videoRef.current
    if (!v) return
    v.currentTime = seekTimeMs(ptsMs, fps, kk) / 1000
  }, [ptsMs, fps, frameCount])

  // 审核批注一键跳转：定位到批注锚定的帧，并选中它指向的 track。
  const jumpToAnchor = useCallback((frame?: number, trackId?: number) => {
    if (frame != null) seekToFrame(frame)
    if (trackId != null) {
      const t = tracks.find((x) => x.track_id === trackId)
      if (t) setSelectedKey(t._key)
    }
  }, [seekToFrame, tracks])

  useEffect(() => {
    if (!frameCount || restoredFrameRef.current) return
    restoredFrameRef.current = true
    const saved = readStoredNumber(lastFrameKey, 0)
    if (Number.isFinite(saved) && saved > 0) {
      const target = Math.min(saved, frameCount - 1)
      seekToFrame(target)
      setMsg({ kind: 'ok', text: `已回到上次标注位置：第 ${target} 帧` })
    }
  }, [frameCount, lastFrameKey, seekToFrame])

  useEffect(() => {
    if (frameCount > 0 && Number.isFinite(currentFrame)) {
      writeStoredNumber(lastFrameKey, currentFrame)
    }
  }, [currentFrame, frameCount, lastFrameKey])

  // rVFC: exact displayed frame → drives current frame + scrubber (0-offset).
  useEffect(() => {
    const v = videoRef.current
    if (!v || !v.requestVideoFrameCallback) return
    let stop = false
    const onFrame = (_n: number, meta: RVFCMeta) => {
      if (stop) return
      setCurrentFrame(timeToFrame(meta.mediaTime * 1000))
      rvfcRef.current = v.requestVideoFrameCallback!(onFrame)
    }
    rvfcRef.current = v.requestVideoFrameCallback(onFrame)
    return () => { stop = true; if (rvfcRef.current != null) v.cancelVideoFrameCallback?.(rvfcRef.current) }
  }, [timeToFrame, asset?.id])

  const selected = useMemo(() => tracks.find((t) => t._key === selectedKey) ?? null, [tracks, selectedKey])

  // 性能预算（B1 收尾①，50 track 下帧步进 <100ms）。overlay 是 SVG 不是 canvas，
  // 所以「脏矩形」在这里的等价物是：不重算没变过的东西。
  // 每次帧步进都要为每条可见 track 求一次插值几何，而 interpolateAt 要求输入按
  // ts_ms 有序——此前的写法在每一帧、对每一条 track 都 [...kfs].sort() 复制排序
  // 一遍。帧步进根本不改 keyframes，这份工作全是白做的（50 track × 13 关键帧 =
  // 每帧 50 次数组复制 + 排序）。按 track 缓存排序结果，只在 tracks 真的变了
  // （标注员编辑）时重建。
  const sortedKfByTrack = useMemo(() => {
    const m = new Map<string, Keyframe[]>()
    for (const t of tracks) m.set(t._key, sortKeyframes(t.keyframes as Keyframe[]))
    return m
  }, [tracks])
  const geomAt = useCallback(
    (t: TrackDraft, tsMs: number) =>
      interpolateAt(sortedKfByTrack.get(t._key) ?? sortKeyframes(t.keyframes as Keyframe[]), tsMs),
    [sortedKfByTrack],
  )

  const shapesAtFrame = useMemo<Shape[]>(() => {
    const out: Shape[] = []
    for (const t of tracks) {
      if (t._hidden) continue
      const g = geomAt(t, currentTsMs)
      if (!g) continue
      const attrs = { ...(t.attrs ?? {}), locked: !!t._locked }
      if (isPolyKind(t.kind) && g.points && g.points.length >= 6) {
        out.push({ id: t._key, kind: 'polygon', label: t.label, points: chunk2(g.points), color: t.color, source: t.source, attrs })
      } else if (g.bbox) {
        const [x, y, w, h] = g.bbox
        out.push({ id: t._key, kind: 'bbox', label: t.label, points: [[x, y], [x + w, y + h]], color: t.color, source: t.source, attrs })
      }
    }
    return out
  }, [tracks, currentTsMs, geomAt])

  // --- undo/redo-aware mutation ---
  const applyTracks = useCallback((updater: (prev: TrackDraft[]) => TrackDraft[]) => {
    undoRef.current.push(tracks)
    if (undoRef.current.length > 100) undoRef.current.shift()
    redoRef.current = []
    setTracks(updater)
    setHistTick((n) => n + 1)
  }, [tracks])
  const undo = useCallback(() => {
    if (!undoRef.current.length) return
    redoRef.current.push(tracks); setTracks(undoRef.current.pop()!); setHistTick((n) => n + 1)
  }, [tracks])
  const redo = useCallback(() => {
    if (!redoRef.current.length) return
    undoRef.current.push(tracks); setTracks(redoRef.current.pop()!); setHistTick((n) => n + 1)
  }, [tracks])
  const canUndo = undoRef.current.length > 0
  const canRedo = redoRef.current.length > 0

  const markDirty = (key: string, mut: (t: TrackDraft) => TrackDraft) =>
    applyTracks((prev) => prev.map((t) => (t._key === key && !t._locked ? { ...mut(t), _dirty: true } : t)))

  const upsertKeyframeGeom = (key: string, geom: Geom) => markDirty(key, (t) => {
    const existing = t.keyframes.find((k) => k.frame === currentFrame)
    const kfs = t.keyframes.filter((k) => k.frame !== currentFrame)
    const nk: VideoKeyframe = { frame: currentFrame, ts_ms: currentTsMs, ...geom, outside: false, occluded: existing?.occluded ?? false, source: 'human' }
    return { ...t, keyframes: sortKeyframes([...kfs, nk] as Keyframe[]) as VideoKeyframe[] }
  })

  const onCommitShape = (s: Shape) => {
    if (!editable) return
    // Label comes from the canvas ontology selector; color follows ontology when
    // matched, else falls back to the high-contrast palette.
    const label = s.label || ontologyLabels[0]?.name || '对象'
    const color = s.color || ontologyLabels.find((l) => l.name === label)?.color || PALETTE[tracks.length % PALETTE.length]
    const key = `new-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
    const kf: VideoKeyframe = { frame: currentFrame, ts_ms: currentTsMs, ...shapeToGeom(s), outside: false, occluded: false, source: 'human' }
    applyTracks((prev) => [...prev, {
      _key: key, _dirty: true, id: '', task_id: taskId, dataset_id: 0, asset_id: 0,
      track_id: 0, label, kind: s.kind === 'polygon' ? 'polygon' : 'bbox', color, attrs: {}, keyframes: [kf], source: 'human', version: 0, is_active: true,
    }])
    setSelectedKey(key)
  }
  const onUpdateShapes = (next: Shape[]) => {
    if (!editable) return
    for (const s of next) {
      const t = tracks.find((x) => x._key === s.id)
      if (!t || t._locked) continue
      const geom = shapeToGeom(s)
      if (geomChanged(geomAt(t, currentTsMs), geom)) upsertKeyframeGeom(t._key, geom)
    }
  }

  const deleteTrack = async (t: TrackDraft) => {
    if (t._locked) {
      setMsg({ kind: 'err', text: '该 track 已锁定，先解锁再删除' })
      return
    }
    if (t.id) { try { await trackApi.remove(taskId, t.id) } catch { /* ignore */ } }
    applyTracks((prev) => prev.filter((x) => x._key !== t._key))
    if (selectedKey === t._key) setSelectedKey(null)
  }
  const setFlagAtFrame = (key: string, flag: 'outside' | 'occluded') => {
    const target = tracks.find((t) => t._key === key)
    if (!target) return
    if (target._locked) { setMsg({ kind: 'err', text: '该 track 已锁定，先解锁再修改状态' }); return }
    const existing = target.keyframes.find((k) => k.frame === currentFrame)
    const g = geomAt(target, currentTsMs)
    if (!existing && !g) {
      setMsg({ kind: 'err', text: flag === 'outside' ? '当前帧没有可继承几何，先在可见帧打关键帧再标记出画' : '当前帧没有可见对象，不能标记遮挡' })
      return
    }
    markDirty(key, (t) => {
      const kfs = t.keyframes.filter((k) => k.frame !== currentFrame)
      const nk: VideoKeyframe = {
        frame: currentFrame, ts_ms: currentTsMs, bbox: existing?.bbox || g?.bbox, points: existing?.points || g?.points,
        outside: flag === 'outside' ? !(existing?.outside ?? false) : (existing?.outside ?? false),
        occluded: flag === 'occluded' ? !(existing?.occluded ?? false) : (existing?.occluded ?? false),
        source: 'human',
      }
      return { ...t, keyframes: sortKeyframes([...kfs, nk] as Keyframe[]) as VideoKeyframe[] }
    })
  }
  const deleteKeyframeAtFrame = (key: string, ask = false) => {
    const target = tracks.find((t) => t._key === key)
    if (!target) return
    if (target._locked) { setMsg({ kind: 'err', text: '该 track 已锁定，先解锁再删除关键帧' }); return }
    if (!target.keyframes.some((k) => k.frame === currentFrame)) {
      setMsg({ kind: 'err', text: '当前帧没有关键帧可删除；删除 track 请使用右侧红色按钮' })
      return
    }
    if (ask && !confirm(`只删除「${target.label} #${target.track_id || '新'}」在第 ${currentFrame} 帧的关键帧？\n不会删除整个 track。`)) return
    markDirty(key, (t) => ({ ...t, keyframes: t.keyframes.filter((k) => k.frame !== currentFrame) }))
  }
  const toggleHidden = (key: string) => applyTracks((prev) => prev.map((t) => (t._key === key ? { ...t, _hidden: !t._hidden } : t)))
  const toggleLocked = (key: string) => applyTracks((prev) => prev.map((t) => (t._key === key ? { ...t, _locked: !t._locked } : t)))
  const setAllHidden = (hidden: boolean) => applyTracks((prev) => prev.map((t) => ({ ...t, _hidden: hidden })))
  const setAllLocked = (locked: boolean) => applyTracks((prev) => prev.map((t) => ({ ...t, _locked: locked })))

  // A 档：复制 track / 关键帧传播 / 属性编辑 / 侧栏排序过滤
  const tsForFrame = useCallback((frame: number) => ptsMs[frame] ?? (frame * 1000) / fps, [ptsMs, fps])
  const duplicateTrack = (t: TrackDraft) => {
    const key = `dup-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
    applyTracks((prev) => {
      const maxNo = prev.reduce((m, x) => Math.max(m, x.track_id || 0), 0)
      return [...prev, { ...t, _key: key, _dirty: true, _hidden: false, _locked: false, id: '', track_id: maxNo + 1, source: 'human', version: 0, keyframes: t.keyframes.map((k) => ({ ...k })) }]
    })
    setSelectedKey(key)
    setMsg({ kind: 'ok', text: `已复制为新 track（校对后保存）` })
  }
  const propagateKeyframe = (key: string, n: number) => {
    const t = tracks.find((x) => x._key === key)
    if (!t || t._locked || !n) return
    const g = geomAt(t, currentTsMs)
    if (!g || (!g.bbox && !g.points)) { setMsg({ kind: 'err', text: '当前帧无可传播几何（先在该帧画/定位关键帧）' }); return }
    const target = Math.max(0, Math.min((frameCount || 1) - 1, currentFrame + n))
    if (target === currentFrame) return
    markDirty(key, (x) => {
      const kfs = x.keyframes.filter((k) => k.frame !== target)
      const nk: VideoKeyframe = { frame: target, ts_ms: tsForFrame(target), bbox: g.bbox, points: g.points, outside: false, occluded: false, source: 'human' }
      return { ...x, keyframes: sortKeyframes([...kfs, nk] as Keyframe[]) as VideoKeyframe[] }
    })
    setMsg({ kind: 'ok', text: `已把当前帧几何传播到第 ${target} 帧（中间自动插值）` })
  }
  const setAttr = (key: string, name: string, val: unknown) =>
    markDirty(key, (x) => ({ ...x, attrs: { ...(x.attrs ?? {}), [name]: val } }))

  // 智能跳转：下一/上一「有物体帧」或「空帧」。
  // 按「帧号」判定覆盖（不用 ts_ms：关键帧 ts 可能是 frame*1000/fps，与帧索引 pts_ms
  // 有浮点差，末关键帧会被误判成空帧）。语义与插值一致：不外推、outside 起停止产帧。
  // 按帧号排序的关键帧，同样按 track 缓存。jumpSmart 会沿着整段视频逐帧扫，
  // 若在循环里对每条 track 重排一次，1 小时的片子就是 10 万帧 × 50 track 次排序。
  const byFrameKfByTrack = useMemo(() => {
    const m = new Map<string, VideoKeyframe[]>()
    for (const t of tracks) m.set(t._key, [...t.keyframes].sort((a, b) => a.frame - b.frame))
    return m
  }, [tracks])
  const trackCoversFrame = useCallback((t: TrackDraft, f: number) => {
    const kfs = byFrameKfByTrack.get(t._key) ?? []
    if (!kfs.length || f < kfs[0].frame || f > kfs[kfs.length - 1].frame) return false
    let prev = kfs[0]
    for (const k of kfs) { if (k.frame <= f) prev = k; else break }
    return !prev.outside
  }, [byFrameKfByTrack])
  const frameHasObjects = useCallback((f: number) =>
    tracks.some((t) => !t._hidden && trackCoversFrame(t, f)),
  [tracks, trackCoversFrame])
  const jumpSmart = (want: 'object' | 'empty', dir: 1 | -1) => {
    const last = (frameCount || 1) - 1
    for (let f = currentFrame + dir; f >= 0 && f <= last; f += dir) {
      if (frameHasObjects(f) === (want === 'object')) { seekToFrame(f); return }
    }
    setMsg({ kind: 'err', text: want === 'object' ? '这个方向没有更多「有物体」帧了' : '这个方向没有更多「空」帧了' })
  }

  // 审核导航（B3.1）：逐帧看完一小时的片子不现实，审核员应该被直接送到模型
  // 最没把握的地方。ai_score 由 det-server 写进 track.attrs，采纳后的人工
  // track 也保留（adopted_from 血缘），所以两种都能跳。
  const lowConfTracks = useMemo(() => {
    const scored = tracks
      .map((t) => ({ t, score: typeof t.attrs?.ai_score === 'number' ? (t.attrs.ai_score as number) : null }))
      .filter((x): x is { t: TrackDraft; score: number } => x.score !== null && x.score < lowConfThreshold)
    // 先看最没把握的
    return scored.sort((a, b) => a.score - b.score)
  }, [tracks, lowConfThreshold])
  const lowConfKeys = useMemo(() => new Set(lowConfTracks.map((x) => x.t._key)), [lowConfTracks])

  const jumpNextLowConf = () => {
    if (!lowConfTracks.length) return
    const at = lowConfTracks.findIndex((x) => x.t._key === selectedKey)
    const next = lowConfTracks[(at + 1) % lowConfTracks.length] // at=-1 → 从头开始
    setSelectedKey(next.t._key)
    const first = [...next.t.keyframes].sort((a, b) => a.frame - b.frame)[0]
    if (first) seekToFrame(first.frame)
    setMsg({ kind: 'ok', text: `#${next.t.track_id} ${next.t.label} · 置信度 ${next.score.toFixed(2)}（${lowConfTracks.length} 条低置信，再点继续）` })
  }

  // F1 快捷键帮助 / F11 全屏（全屏作用于整个工作台根节点）
  const [helpOpen, setHelpOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const toggleFullscreen = () => {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
    else rootRef.current?.requestFullscreen?.().catch(() => {})
  }

  // 时间轴拖关键帧：把选中 track 在 fromFrame 的关键帧移到 toFrame（改帧号+ts_ms）。
  const moveKeyframe = (fromFrame: number, toFrame: number) => {
    if (!selected || selected._locked) return
    const to = Math.max(0, Math.min((frameCount || 1) - 1, toFrame))
    if (to === fromFrame) return
    markDirty(selected._key, (t) => {
      const kf = t.keyframes.find((k) => k.frame === fromFrame)
      if (!kf) return t
      const rest = t.keyframes.filter((k) => k.frame !== fromFrame && k.frame !== to)
      return { ...t, keyframes: sortKeyframes([...rest, { ...kf, frame: to, ts_ms: tsForFrame(to) }] as Keyframe[]) as VideoKeyframe[] }
    })
  }
  const attrsForLabel = useCallback((label: string): any[] =>
    ((ontologyLabels.find((l: any) => l.name === label)?.attributes as any[]) ?? []).filter((a: any) => !a.scope || a.scope === 'track'),
  [ontologyLabels])
  // keyframe-scope 属性：挂在「当前帧的关键帧」上（逐帧可变状态，CVAT 模型）
  const kfAttrsForLabel = useCallback((label: string): any[] =>
    ((ontologyLabels.find((l: any) => l.name === label)?.attributes as any[]) ?? []).filter((a: any) => a.scope === 'keyframe'),
  [ontologyLabels])
  const setKeyframeAttr = (key: string, name: string, val: unknown) =>
    markDirty(key, (t) => ({
      ...t,
      keyframes: t.keyframes.map((k) => (k.frame === currentFrame ? { ...k, attrs: { ...(k.attrs ?? {}), [name]: val } } : k)),
    }))
  const [trackFilter, setTrackFilter] = useState('')
  const [trackSort, setTrackSort] = useState<'id' | 'label' | 'kf' | 'score'>('id')
  const [propN, setPropN] = useState(10)

  // AI 置信度：det-server 写进 track.attrs.ai_score；采纳后的人工 track 也保留
  // （adopted_from 血缘）。纯手工 track 没有这个值。
  const scoreOf = (t: TrackDraft): number | null =>
    typeof t.attrs?.ai_score === 'number' ? (t.attrs.ai_score as number) : null

  const displayedTracks = useMemo(() => {
    const f = trackFilter.trim().toLowerCase()
    const arr = f ? tracks.filter((t) => (t.label || '').toLowerCase().includes(f) || String(t.track_id).includes(f)) : tracks
    const sorted = [...arr]
    if (trackSort === 'label') sorted.sort((a, b) => (a.label || '').localeCompare(b.label || ''))
    else if (trackSort === 'kf') sorted.sort((a, b) => b.keyframes.length - a.keyframes.length)
    else if (trackSort === 'score') {
      // 最没把握的排最前（B2.7）；没有置信度的手工 track 沉底，
      // 否则它们会以 0 分冒充「最可疑」，把审核员的注意力引偏。
      sorted.sort((a, b) => {
        const sa = scoreOf(a), sb = scoreOf(b)
        if (sa === null && sb === null) return (a.track_id || 0) - (b.track_id || 0)
        if (sa === null) return 1
        if (sb === null) return -1
        return sa - sb
      })
    }
    else sorted.sort((a, b) => (a.track_id || 0) - (b.track_id || 0))
    return sorted
  }, [tracks, trackFilter, trackSort])

  const gotoKeyframe = (dir: 1 | -1) => {
    if (!selected) return
    const frames = selected.keyframes.map((k) => k.frame).sort((a, b) => a - b)
    const target = dir > 0 ? frames.find((f) => f > currentFrame) : [...frames].reverse().find((f) => f < currentFrame)
    if (target != null) seekToFrame(target)
  }

  const save = async () => {
    setSaving(true); setMsg(null)
    try {
      for (const t of tracks.filter((x) => x._dirty)) {
        const saved = await trackApi.put(taskId, {
          id: t.id || undefined, track_id: t.track_id || undefined,
          label: t.label, kind: t.kind, color: t.color, attrs: t.attrs, keyframes: t.keyframes,
          version: t.id ? t.version : undefined,
        })
        setTracks((prev) => prev.map((x) => (x._key === t._key ? { ...saved, _key: x._key, _dirty: false, _hidden: x._hidden, _locked: x._locked } : x)))
      }
      setSavedFlash(true); setTimeout(() => setSavedFlash(false), 1600)
    } catch (e: any) {
      if (e?.response?.status === 409) {
        setMsg({ kind: 'err', text: 'track 被他处修改，正在刷新…' })
        const fresh = await trackApi.list(taskId)
        setTracks(fresh.map((t) => ({ ...t, _key: t.id, _dirty: false })))
        undoRef.current = []; redoRef.current = []
      } else setMsg({ kind: 'err', text: e?.response?.data?.error || e?.message || '保存失败' })
    } finally { setSaving(false) }
  }
  const submit = async () => {
    if (tracks.some((t) => t._dirty)) await save()
    try { await taskApi.submit(taskId); advanceOrLeave() } catch (e: any) { setMsg({ kind: 'err', text: e?.response?.data?.error || '提交失败' }) }
  }
  const dirtyCount = tracks.filter((t) => t._dirty).length

  // --- review (B3.1): reviewer 通过/驳回，复用 taskApi.qaPass/qaReject ---
  const reviewing = canReview && task?.state === 'QA_PENDING'
  const [reviewNote, setReviewNote] = useState('')
  const qaMut = useMutation({
    mutationFn: (pass: boolean) => (pass ? taskApi.qaPass(taskId, reviewNote) : taskApi.qaReject(taskId, reviewNote)),
    onSuccess: advanceOrLeave,
    // 通过会被后端拒绝（四眼规则 / 仍有被驳回的 track）——必须让审核员看到原因
    onError: (e: any) => setMsg({ kind: 'err', text: e?.response?.data?.error || '审核操作失败' }),
  })

  // 逐 track 裁决（B3.1）：审核员的逐物体清单。只要还有 track 是 rejected，
  // 整体「审核通过」就会被后端拒绝。
  const reviewTrackMut = useMutation({
    mutationFn: ({ objId, status }: { objId: string; status: 'passed' | 'rejected' | '' }) =>
      trackApi.review(taskId, objId, status),
    onSuccess: reloadTracks,
    onError: (e: any) => setMsg({ kind: 'err', text: e?.response?.data?.error || '裁决失败' }),
  })

  const togglePlay = useCallback(() => { const v = videoRef.current; if (!v) return; if (v.paused) { v.play(); setPlaying(true) } else { v.pause(); setPlaying(false) } }, [])
  // 翻到相邻任务时把「来处」一起带过去，否则翻两下之后「返回」就找不到北了。
  function goto(tid?: number | null) { if (tid) navigate(`/video-tasks/${tid}`, { state: location.state }) }

  // page-level keys: transport + save/undo/redo (canvas owns tool keys r/p/e/v).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // F1 帮助 / F11 全屏：即使焦点在输入框里也生效
      if (e.key === 'F1') { e.preventDefault(); setHelpOpen((v) => !v); return }
      if (e.key === 'F11') { e.preventDefault(); toggleFullscreen(); return }
      const tag = (e.target as HTMLElement)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      const mod = e.ctrlKey || e.metaKey
      if (mod && e.key.toLowerCase() === 's') { e.preventDefault(); save(); return }
      if (mod && e.key.toLowerCase() === 'z') { e.preventDefault(); if (e.shiftKey) redo(); else undo(); return }
      if (mod && e.key.toLowerCase() === 'y') { e.preventDefault(); redo(); return }
      if (mod) return
      if (e.code === 'Space') { e.preventDefault(); togglePlay() }
      else if (e.key === 'ArrowRight') { e.preventDefault(); seekToFrame(currentFrame + 1) }
      else if (e.key === 'ArrowLeft') { e.preventDefault(); seekToFrame(currentFrame - 1) }
      else if (e.key.toLowerCase() === 'c') { e.preventDefault(); seekToFrame(currentFrame - step) }
      else if (e.key.toLowerCase() === 'v' && e.shiftKey) { e.preventDefault(); seekToFrame(currentFrame + step) }
      else if (e.key.toLowerCase() === 'g') {
        e.preventDefault()
        const raw = window.prompt('跳转到帧号', String(currentFrame))
        if (raw == null) return
        const target = Number(raw.trim())
        if (Number.isFinite(target)) seekToFrame(Math.round(target))
      }
      // N = 审核导航：跳到下一条低置信 track（仅审核态）
      else if (e.key.toLowerCase() === 'n' && reviewing) { e.preventDefault(); jumpNextLowConf() }
      else if (e.key.toLowerCase() === 'l' && selected && editable) { e.preventDefault(); toggleLocked(selected._key) }
      else if (e.key.toLowerCase() === 'q' && selected && editable) { e.preventDefault(); setFlagAtFrame(selected._key, 'occluded') }
      else if (e.key.toLowerCase() === 'o' && selected && editable) { e.preventDefault(); setFlagAtFrame(selected._key, 'outside') }
      else if ((e.key === 'Delete' || e.key === 'Backspace') && selected && !selected._locked) { deleteKeyframeAtFrame(selected._key, true) }
      else if (/^[0-9]$/.test(e.key) && selected && editable && !selected._locked) {
        // 数字键 = 快捷标签（本体 hotkey）：改选中 track 的标签+颜色
        const lbl = ontologyLabels.find((l: any) => String(l.hotkey ?? '') === e.key)
        if (lbl) { e.preventDefault(); markDirty(selected._key, (x) => ({ ...x, label: lbl.name, color: lbl.color || x.color })) }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  // If the source codec wasn't browser-playable, the media-worker transcoded it
  // to an H.264 derivative (frameIndex.playback) — play that instead of the raw blob.
  const videoSrc = asset ? (frameIndex?.playback ? videoApi.playbackUrl(asset.id) : videoApi.bodyUrl(asset.id)) : undefined
  const videoBg = (
    <video ref={videoRef} src={videoSrc}
      className="pointer-events-none block h-full w-full object-contain"
      onPlay={() => setPlaying(true)} onPause={() => setPlaying(false)}
      onTimeUpdate={(e) => setCurrentFrame(timeToFrame(e.currentTarget.currentTime * 1000))}
      onLoadedMetadata={() => { if (videoRef.current) videoRef.current.playbackRate = speed; if (currentFrame > 0) seekToFrame(currentFrame) }} playsInline />
  )
  const jumpFrame = () => { const n = parseInt(frameInput, 10); if (!Number.isNaN(n)) seekToFrame(n) }

  return (
    <div ref={rootRef} className="flex h-screen flex-col" style={{ background: 'var(--background)' }}>
      {/* top bar */}
      <div className="flex h-13 shrink-0 items-center gap-3 border-b px-4 py-2.5" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
        <Button variant="ghost" size="sm" onClick={() => navigate(backTo)}><ArrowLeft className="mr-1 h-4 w-4" />返回</Button>
        <span className="text-sm font-medium">{asset?.original_name ?? '视频任务'}</span>
        {task && <Badge variant="outline" className={`text-[10px] ${TASK_STATE_COLOR[task.state] ?? ''}`}>{TASK_STATE_LABELS[task.state] ?? task.state}</Badge>}
        {displayW ? <span className="font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>{displayW}×{displayH}{frameIndex?.rotation ? ` · rot${frameIndex.rotation}` : ''}</span> : null}
        <div className="ml-auto flex items-center gap-2">
          {editable && (
            <>
              <Button size="sm" variant="ghost" disabled={!canUndo} onClick={undo} title="撤销 (Ctrl+Z)"><Undo2 className="h-4 w-4" /></Button>
              <Button size="sm" variant="ghost" disabled={!canRedo} onClick={redo} title="重做 (Ctrl+Shift+Z)"><Redo2 className="h-4 w-4" /></Button>
              <Button size="sm" variant="ghost" onClick={() => setHelpOpen(true)} title="快捷键帮助 (F1)"><HelpCircle className="h-4 w-4" /></Button>
              <Button size="sm" variant="ghost" onClick={toggleFullscreen} title="全屏 (F11)"><Maximize className="h-4 w-4" /></Button>
            </>
          )}
          {lockedByOther && (
            <span className="flex items-center gap-1 rounded-md border px-2 py-1 text-xs" style={{ borderColor: 'var(--border)', background: 'var(--muted)', color: 'var(--muted-foreground)' }}>
              <Lock className="h-3 w-3" />只读：被 {lock?.owner} 锁定
            </span>
          )}
          <Button variant="ghost" size="sm" disabled={!adjacent?.prev_task_id} onClick={() => goto(adjacent?.prev_task_id)}><ChevronLeft className="h-4 w-4" />上一个</Button>
          <Button variant="ghost" size="sm" disabled={!adjacent?.next_task_id} onClick={() => goto(adjacent?.next_task_id)}>下一个<ChevronRight className="h-4 w-4" /></Button>
          {editable && (
            <>
              <Button size="sm" variant={dirtyCount ? 'default' : 'outline'} onClick={save} disabled={saving} title="保存 (Ctrl+S)"
                style={savedFlash ? { background: 'var(--chart-2)', color: 'var(--primary-foreground)' } : undefined}>
                {saving ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : savedFlash ? <Check className="mr-1 h-4 w-4" /> : <Save className="mr-1 h-4 w-4" />}
                {savedFlash ? '已保存' : dirtyCount ? `保存 (${dirtyCount})` : '保存'}
              </Button>
              <Button size="sm" onClick={submit} disabled={saving}><Send className="mr-1 h-4 w-4" />提交</Button>
            </>
          )}
        </div>
      </div>

      <div ref={splitRef} className="flex flex-1 overflow-hidden">
        {/* main: canvas + player */}
        <div className="flex min-w-0 flex-1 flex-col">
          {asset && asset.preprocess_status !== 'ready' && (
            (asset.preprocess_status === 'failed' || asset.preprocess_status === 'rejected') ? (
              <div className="m-3 rounded-md border p-2.5 text-xs" style={{ borderColor: 'var(--destructive)', color: 'var(--destructive)', background: 'color-mix(in srgb, var(--destructive) 8%, transparent)' }}>
                ⚠ 视频预处理失败，无法标注：{asset.preprocess_error || '未知错误'}
                {asset.preprocess_status === 'rejected' && <span> · 请转成 H.264(baseline)+AAC 的 mp4 后重新上传（HEVC/H.265、AV1 浏览器不可靠播放）。</span>}
              </div>
            ) : (
              <div className="m-3 rounded-md border p-2.5 text-xs" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
                视频预处理中（{asset.preprocess_status}）… 帧索引/缩略图生成后可标注。
              </div>
            )
          )}
          <div className="flex min-h-0 flex-1">
            <InteractiveCanvas
              background={videoBg} imgW={displayW || 1280} imgH={displayH || 720}
              shapes={shapesAtFrame} taskId={taskId} readOnly={!editable} enableSam={editable} samSegment={samSegment} onPropagate={onPropagate}
              onCommitShape={onCommitShape} onUpdateShapes={onUpdateShapes}
              selectedIds={selectedKey ? [selectedKey] : []}
              onSelectionChange={(ids) => setSelectedKey(ids[ids.length - 1] ?? null)}
              labelColor={(lbl) => videoLabelOptions.find((l) => l.name === lbl)?.color || ''}
              labelOptions={videoLabelOptions} freeLabel
            />
          </div>

          {/* player: transport + scrubber */}
          <div className="shrink-0 border-t" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
            <div className="flex items-center gap-1.5 px-3 pt-2">
              <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => seekToFrame(0)} title="首帧"><ChevronsLeft className="h-4 w-4" /></Button>
              <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => seekToFrame(currentFrame - step)} title={`后退 ${step} 帧`}><SkipBack className="h-4 w-4" /></Button>
              <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => seekToFrame(currentFrame - 1)} title="上一帧 (←)"><ChevronLeft className="h-4 w-4" /></Button>
              <Button size="icon" variant="outline" className="h-8 w-8" onClick={togglePlay} title="播放/暂停 (空格)">{playing ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}</Button>
              <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => seekToFrame(currentFrame + 1)} title="下一帧 (→)"><ChevronRight className="h-4 w-4" /></Button>
              <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => seekToFrame(currentFrame + step)} title={`前进 ${step} 帧`}><SkipForward className="h-4 w-4" /></Button>
              <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => seekToFrame(frameCount - 1)} title="末帧"><ChevronsRight className="h-4 w-4" /></Button>

              {/* frame # input + timecode */}
              <div className="ml-2 flex items-center gap-1 text-xs">
                <span style={{ color: 'var(--muted-foreground)' }}>帧</span>
                <input value={frameInput} onChange={(e) => setFrameInput(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') jumpFrame() }} onBlur={jumpFrame}
                  className="h-7 w-16 rounded border px-1.5 text-center font-mono outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                <span className="font-mono" style={{ color: 'var(--muted-foreground)' }}>/ {frameCount ? frameCount - 1 : 0}</span>
                <span className="ml-2 font-mono" style={{ color: 'var(--foreground)' }}>{fmtTC(currentTsMs)}</span>
                <span className="font-mono" style={{ color: 'var(--muted-foreground)' }}> / {fmtTC(durationMs)}</span>
              </div>

              {/* keyframe nav for selected track */}
              {selected && (
                <div className="ml-2 flex items-center gap-1">
                  <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => gotoKeyframe(-1)} title="上一关键帧"><Diamond className="h-3.5 w-3.5 rotate-0" /><ChevronLeft className="h-3 w-3 -ml-1" /></Button>
                  <Button size="icon" variant="outline" className="h-8 w-8" onClick={() => gotoKeyframe(1)} title="下一关键帧"><ChevronRight className="h-3 w-3 -mr-1" /><Diamond className="h-3.5 w-3.5" /></Button>
                </div>
              )}

              {/* 智能跳转：下一「有物体」/「空」帧（按住 Shift 反向） */}
              <div className="ml-2 flex items-center gap-1">
                <Button size="sm" variant="outline" className="h-8 px-2 text-[11px]"
                  onClick={(e) => jumpSmart('object', e.shiftKey ? -1 : 1)}
                  title="跳到下一个「有物体」帧（Shift+点击 = 上一个）">
                  <Square className="mr-1 h-3 w-3" />有物体
                </Button>
                <Button size="sm" variant="outline" className="h-8 px-2 text-[11px]"
                  onClick={(e) => jumpSmart('empty', e.shiftKey ? -1 : 1)}
                  title="跳到下一个「空」帧（无任何可见对象；Shift+点击 = 上一个）">
                  <SquareDashed className="mr-1 h-3 w-3" />空帧
                </Button>
              </div>

              {/* 审核导航（B3.1）：直接跳到模型最没把握的 track，不必逐帧看完全片。
                  按 ai_score 从低到高循环；配合上面的倍速播放（overlay 跟着 rVFC 走）。 */}
              {reviewing && (
                <div className="ml-2 flex items-center gap-1">
                  <Button size="sm" variant="outline" className="h-8 px-2 text-[11px]"
                    disabled={!lowConfTracks.length}
                    onClick={jumpNextLowConf}
                    title={lowConfTracks.length
                      ? `跳到下一条低置信 track（ai_score < ${lowConfThreshold}），按置信度从低到高循环 (N)`
                      : '没有带 AI 置信度的低分 track——纯手工标注的 track 没有 ai_score'}>
                    <AlertTriangle className="mr-1 h-3 w-3" />
                    低置信{lowConfTracks.length ? ` (${lowConfTracks.length})` : ''}
                  </Button>
                  <input type="number" min={0.05} max={1} step={0.05} value={lowConfThreshold}
                    onChange={(e) => setLowConfThreshold(Math.min(1, Math.max(0.05, Number(e.target.value) || 0.5)))}
                    title="低置信阈值：ai_score 低于此值才算低置信"
                    className="h-8 w-14 rounded border px-1 text-center text-[11px] outline-none"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                </div>
              )}

              <div className="ml-auto flex items-center gap-2 text-xs">
                <label className="flex items-center gap-1" style={{ color: 'var(--muted-foreground)' }}>步长
                  <input type="number" min={1} max={MAX_FRAME_STEP} value={step} onChange={(e) => setStep(clampFrameStep(Number(e.target.value)))}
                    className="h-7 w-12 rounded border px-1 text-center outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                </label>
                <label className="flex items-center gap-1" style={{ color: 'var(--muted-foreground)' }}>速度
                  <select value={speed} onChange={(e) => setSpeed(Number(e.target.value))}
                    className="h-7 rounded border px-1 outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                    {SPEEDS.map((s) => <option key={s} value={s}>{s}×</option>)}
                  </select>
                </label>
                <span style={{ color: 'var(--muted-foreground)' }}>· 标注 {tracks.reduce((n, t) => n + t.keyframes.length, 0)} 关键帧 / {tracks.length} track</span>
              </div>
              {/* ontology label suggestions — shared by canvas label input & per-track relabel */}
              <datalist id="vid-labels">{videoLabelOptions.map((l) => <option key={l.name} value={l.name}>{l.display || l.name}</option>)}</datalist>
            </div>
            <Scrubber count={frameCount} current={currentFrame} ptsMs={ptsMs} fps={fps} durationMs={durationMs} tracks={tracks} selectedKey={selectedKey} onSeek={seekToFrame} onMoveKeyframe={editable ? moveKeyframe : undefined} />
            {msg && <div className="px-3 pb-1 text-xs" style={{ color: msg.kind === 'ok' ? 'var(--chart-2)' : 'var(--destructive)' }}>{msg.text}</div>}
          </div>
        </div>

        <SplitHandle onPointerDown={startDrag} title="拖动调节宽度" />

        {/* right: track list */}
        <div className="flex shrink-0 flex-col border-l" style={{ width: panelWidth, borderColor: 'var(--border)', background: 'var(--card)' }}>
          <div className="border-b px-3 py-2.5" style={{ borderColor: 'var(--border)' }}>
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">{reviewing ? '审核 & Tracks' : `Tracks · ${tracks.length}`}</span>
              {aiCount > 0 && <Badge variant="outline" className="text-[10px]" style={{ color: 'var(--chart-2)' }}>{aiCount} AI 待校对</Badge>}
            </div>
            {editable && !reviewing && (
              <div className="mt-2 space-y-1.5">
                {/* 模型链路：检测模型 × 追踪器（det-server：YOLO26x/RT-DETR + BoT-SORT/ByteTrack） */}
                <div className="flex items-center gap-1 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                  <span className="shrink-0">模型</span>
                  <select value={detModel} onChange={(e) => setDetModel(e.target.value)} disabled={detectMut.isPending}
                    className="h-6 min-w-0 flex-1 rounded border px-1 outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                    {DET_MODELS.map((m) => <option key={m.v} value={m.v}>{m.label}</option>)}
                  </select>
                  <select value={detTracker} onChange={(e) => setDetTracker(e.target.value)} disabled={detectMut.isPending}
                    className="h-6 min-w-0 flex-1 rounded border px-1 outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                    {DET_TRACKERS.map((m) => <option key={m.v} value={m.v}>{m.label}</option>)}
                  </select>
                </div>
                <div className="flex items-center gap-1 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                  <span className="shrink-0">间隔</span>
                  <select value={detStep} onChange={(e) => { const v = Number(e.target.value); setDetStep(v); try { localStorage.setItem(DET_STEP_KEY, String(v)) } catch { /* ignore */ } }} disabled={detectMut.isPending}
                    className="h-6 min-w-0 flex-1 rounded border px-1 outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} title="每 N 帧检测一次；中间帧线性插值。越密越准但越慢、待校对越多">
                    {DET_STEPS.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}
                  </select>
                </div>
                <label className="flex cursor-pointer select-none items-center gap-1.5 text-[11px]" style={{ color: 'var(--muted-foreground)' }}
                  title="勾选：SAM2 传播后直接生成人工 track（免再点采纳）；不勾：生成 AI track 走采纳流程">
                  <input type="checkbox" checked={autoAdopt}
                    onChange={(e) => { setAutoAdopt(e.target.checked); localStorage.setItem('video.autoAdopt', e.target.checked ? '1' : '0') }} />
                  SAM2 传播后自动采纳（直接人工 track）
                </label>
                {/* GPU 成本闸门（B2.8）：数据集关掉了就别让人白点；队列满了先说清楚。 */}
                {aiDisabled && (
                  <p className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                    该数据集已关闭 AI 预标注（管理员可在数据集设置里改）
                  </p>
                )}
                {!aiDisabled && detQueue && (detQueue.inflight > 0 || detQueue.waiting > 0) && (
                  <p className="text-[11px]" style={{ color: detQueueFull ? 'var(--destructive)' : 'var(--muted-foreground)' }}>
                    GPU 队列 {detQueue.waiting}/{detQueue.max_wait}
                    {detQueue.inflight > 0 && ' · 正在识别 1 个视频'}
                    {detQueueFull && ' · 已满，请稍后再试'}
                  </p>
                )}
                <div className="flex items-center gap-1.5">
                  <Button size="sm" variant="outline" className="h-7 flex-1 text-xs"
                    disabled={detectMut.isPending || aiDisabled || detQueueFull}
                    onClick={() => detectMut.mutate()}
                    title={aiDisabled ? '该数据集已关闭 AI 预标注'
                      : detQueueFull ? 'GPU 队列已满，请稍后再试'
                      : '用所选模型+追踪器生成待校对 track（会替换上次的 AI 结果，人工 track 不受影响）'}>
                    {detectMut.isPending ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <Sparkles className="mr-1 h-3.5 w-3.5" />}
                    {detectMut.isPending ? '识别中…' : 'AI 预标注'}
                  </Button>
                  {aiCount > 0 && (
                    <Button size="sm" variant="outline" className="h-7 text-xs" disabled={adoptAllMut.isPending}
                      style={{ color: 'var(--chart-2)', borderColor: 'var(--chart-2)' }}
                      onClick={() => adoptAllMut.mutate()} title="把全部 AI track 转为人工 track（AI 原件存档，可追溯）">
                      {adoptAllMut.isPending ? <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" /> : <Check className="mr-1 h-3.5 w-3.5" />}
                      采纳全部
                    </Button>
                  )}
                </div>
              </div>
            )}
          </div>
          <div className="thin-scroll flex-1 space-y-2 overflow-auto p-3">
            {/* 返工对比（B3.1）：只让审核员复核标注员这一轮真正动过的地方。 */}
            {reviewing && <ReworkDiffPanel taskId={taskId} onJump={jumpToAnchor} />}
            {/* 审核批注（B3.1）：锚定到 帧+track，点一行就跳过去。驳回后标注员
                必须逐条标记「已修复」才能重新提交。 */}
            <ReviewCommentPanel
              taskId={taskId}
              currentFrame={currentFrame}
              selectedTrackId={selected?.track_id || undefined}
              canReview={canReview}
              canAnnotate={canAnnotate}
              onJump={jumpToAnchor}
            />
            {reviewing && (
              <div className="space-y-2 rounded-md border p-2.5" style={{ borderColor: 'var(--primary)', background: 'var(--muted)' }}>
                <div className="flex items-center gap-1.5 text-[11px] font-medium" style={{ color: 'var(--muted-foreground)' }}>
                  <Check className="h-3.5 w-3.5" style={{ color: 'var(--primary)' }} />审核 · 逐帧核对 track/关键帧后裁决
                </div>
                <textarea value={reviewNote} onChange={(e) => setReviewNote(e.target.value)} rows={3}
                  placeholder="审核意见（驳回时必填）…"
                  className="w-full resize-none rounded-md border p-2 text-xs outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                <div className="flex flex-wrap gap-1.5">
                  {['框不贴合', 'track 漏标', '关键帧不足', 'ID 错乱'].map((q) => (
                    <button key={q} onClick={() => setReviewNote((n) => (n ? `${n}；${q}` : q))}
                      className="rounded-full border px-2 py-0.5 text-[11px] transition-colors hover:bg-[var(--accent)]"
                      style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>{q}</button>
                  ))}
                </div>
                <div className="flex gap-2">
                  <Button size="sm" variant="outline" className="flex-1 border-red-200 text-red-600" disabled={qaMut.isPending}
                    onClick={() => qaMut.mutate(false)}><X className="mr-1 h-3.5 w-3.5" />驳回重做</Button>
                  <Button size="sm" className="flex-1" style={{ background: 'var(--chart-2)' }} disabled={qaMut.isPending}
                    onClick={() => qaMut.mutate(true)}><Check className="mr-1 h-3.5 w-3.5" />审核通过</Button>
                </div>
              </div>
            )}
            {tracks.length === 0 && (
              <p className="rounded-md border border-dashed px-3 py-8 text-center text-xs" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
                暂无 track · 在画布用「矩形/多边形」工具画一个即新建（当前帧关键帧）
              </p>
            )}
            {/* 审核态也要有：审核员正是最需要「按置信度排序」把可疑 track 顶上来的人。
                批量隐藏/锁定按钮本就由 editable 单独控制。 */}
            {tracks.length > 1 && (
              <div className="flex items-center gap-1 rounded-md border px-1.5 py-1 text-[11px]" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
                <Filter className="h-3 w-3 shrink-0" style={{ color: 'var(--muted-foreground)' }} />
                <input value={trackFilter} onChange={(e) => setTrackFilter(e.target.value)} placeholder="筛选 标签/#"
                  className="h-6 min-w-0 flex-1 rounded border px-1 outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                <select value={trackSort} onChange={(e) => setTrackSort(e.target.value as typeof trackSort)}
                  title="排序：按置信度可把模型最没把握的 track 排到最前"
                  className="h-6 rounded border px-1 outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                  <option value="id">按#</option><option value="label">按标签</option>
                  <option value="kf">按关键帧数</option><option value="score">按置信度</option>
                </select>
                {editable && (
                  <>
                    <button title="全部隐藏 / 显示" onClick={() => setAllHidden(!tracks.every((t) => t._hidden))}
                      className="inline-flex h-6 w-6 items-center justify-center rounded hover:bg-[var(--background)]" style={{ color: 'var(--muted-foreground)' }}>
                      {tracks.every((t) => t._hidden) ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                    </button>
                    <button title="全部锁定 / 解锁" onClick={() => setAllLocked(!tracks.every((t) => t._locked))}
                      className="inline-flex h-6 w-6 items-center justify-center rounded hover:bg-[var(--background)]" style={{ color: tracks.every((t) => t._locked) ? 'var(--primary)' : 'var(--muted-foreground)' }}>
                      <Lock className="h-3.5 w-3.5" />
                    </button>
                  </>
                )}
              </div>
            )}
            {displayedTracks.map((t) => {
              const isSel = t._key === selectedKey
              const hasKfHere = t.keyframes.some((k) => k.frame === currentFrame)
              const kfHere = t.keyframes.find((k) => k.frame === currentFrame)
              // B2.7：模型最没把握的 track 要一眼看得见（琥珀色边框 + 分数徽标），
              // 选中态优先——否则选中哪一条就看不出来了。
              const score = scoreOf(t)
              const lowConf = lowConfKeys.has(t._key)
              return (
                <div key={t._key} onClick={() => setSelectedKey(t._key)}
                  className="cursor-pointer rounded-md border p-2.5 text-xs transition-colors"
                  style={{
                    borderColor: isSel ? 'var(--primary)' : lowConf ? 'var(--chart-4)' : 'var(--border)',
                    borderWidth: lowConf && !isSel ? 2 : 1,
                    background: isSel ? 'var(--accent)' : 'var(--background)',
                  }}>
                  <div className="flex items-center gap-2">
                    <span className="inline-block h-3 w-3 rounded-sm" style={{ background: t.color }} />
                    <span className="font-medium" style={{ opacity: t._hidden ? 0.5 : 1 }}>{t.label}</span>
                    <span style={{ color: 'var(--muted-foreground)' }}>#{t.track_id || '新'}</span>
                    {t.source === 'ai' && <Badge variant="outline" className="text-[9px]" style={{ color: 'var(--chart-2)' }}>AI</Badge>}
                    {t.kind === 'mask' && (
                      <Badge variant="outline" className="text-[9px]" title="逐帧稠密分割（SAM2 传播）：每帧一个关键帧，不做插值">mask</Badge>
                    )}
                    {score !== null && (
                      <Badge variant="outline" className="text-[9px]"
                        title={lowConf ? `AI 置信度 ${score.toFixed(2)}，低于阈值 ${lowConfThreshold}——优先核对` : `AI 置信度 ${score.toFixed(2)}`}
                        style={lowConf ? { color: 'var(--chart-4)', borderColor: 'var(--chart-4)' } : { color: 'var(--muted-foreground)' }}>
                        {score.toFixed(2)}
                      </Badge>
                    )}
                    {t.review_status === 'passed' && <Badge variant="outline" className="text-[9px]" style={{ color: 'var(--chart-2)', borderColor: 'var(--chart-2)' }}>已通过</Badge>}
                    {t.review_status === 'rejected' && <Badge variant="outline" className="text-[9px] border-red-400 text-red-600">已驳回</Badge>}
                    {t._dirty && <span className="text-[9px]" style={{ color: 'var(--chart-4)' }}>●未存</span>}
                    {t._locked && <Badge variant="outline" className="text-[9px]">锁定</Badge>}
                    {kfHere?.outside && <Badge variant="outline" className="text-[9px]" style={{ color: 'var(--chart-4)' }}>出画点</Badge>}
                    {kfHere?.occluded && <Badge variant="outline" className="text-[9px]" style={{ color: 'var(--primary)' }}>遮挡</Badge>}
                    <button title={t._hidden ? '显示' : '隐藏'} onClick={(e) => { e.stopPropagation(); toggleHidden(t._key) }}
                      className="ml-auto inline-flex h-5 w-5 items-center justify-center rounded hover:bg-[var(--muted)]" style={{ color: 'var(--muted-foreground)' }}>
                      {t._hidden ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                    </button>
                    <button title={t._locked ? '解除锁定' : '锁定防误编辑'} onClick={(e) => { e.stopPropagation(); toggleLocked(t._key) }}
                      className="inline-flex h-5 w-5 items-center justify-center rounded hover:bg-[var(--muted)]"
                      style={{ color: t._locked ? 'var(--primary)' : 'var(--muted-foreground)' }}>
                      <Lock className="h-3.5 w-3.5" />
                    </button>
                    <span style={{ color: 'var(--muted-foreground)' }}>{t.keyframes.length} kf</span>
                  </div>
                  {/* 逐 track 裁决（B3.1）：审核员的逐物体清单。再点一次同一裁决 = 撤销。 */}
                  {reviewing && t.id && (
                    <div className="mt-2 flex gap-1.5 border-t pt-2" onClick={(e) => e.stopPropagation()} style={{ borderColor: 'var(--border)' }}>
                      <Button size="sm" variant="outline" className="h-6 flex-1 text-[11px]" disabled={reviewTrackMut.isPending}
                        style={t.review_status === 'passed' ? { background: 'var(--chart-2)', color: '#fff', borderColor: 'var(--chart-2)' } : undefined}
                        onClick={() => reviewTrackMut.mutate({ objId: t.id, status: t.review_status === 'passed' ? '' : 'passed' })}>
                        <Check className="mr-1 h-3 w-3" />通过
                      </Button>
                      <Button size="sm" variant="outline" className="h-6 flex-1 text-[11px]" disabled={reviewTrackMut.isPending}
                        style={t.review_status === 'rejected' ? { background: '#ef4444', color: '#fff', borderColor: '#ef4444' } : { color: '#dc2626' }}
                        onClick={() => reviewTrackMut.mutate({ objId: t.id, status: t.review_status === 'rejected' ? '' : 'rejected' })}>
                        <X className="mr-1 h-3 w-3" />驳回
                      </Button>
                    </div>
                  )}
                  {isSel && editable && (
                    <div className="mt-2 space-y-2 border-t pt-2" style={{ borderColor: 'var(--border)' }}>
                      <div className="flex flex-wrap items-center gap-1.5">
                      <input value={t.label} list="vid-labels" placeholder="标签（可自由填/选）" onClick={(e) => e.stopPropagation()}
                        disabled={t._locked}
                        onChange={(e) => markDirty(t._key, (x) => ({ ...x, label: e.target.value }))}
                        className="w-24 rounded border px-1 py-0.5 text-[11px] outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                      <input type="color" value={t.color || '#e6194B'} disabled={t._locked} title="更换选框颜色" onClick={(e) => e.stopPropagation()}
                        onChange={(e) => markDirty(t._key, (x) => ({ ...x, color: e.target.value }))}
                        className="h-6 w-6 shrink-0 cursor-pointer rounded border p-0.5" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                      <button title="从当前帧标记对象出画；不会删除历史关键帧" disabled={t._locked} onClick={(e) => { e.stopPropagation(); setFlagAtFrame(t._key, 'outside') }}
                        className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]" style={{ borderColor: kfHere?.outside ? 'var(--chart-4)' : 'var(--border)', color: kfHere?.outside ? 'var(--chart-4)' : undefined }}>
                        <EyeOff className="h-3 w-3" />{kfHere?.outside ? '取消出画' : '标出画'}
                      </button>
                      <button title="当前帧对象被遮挡；保留 track 与几何" disabled={t._locked} onClick={(e) => { e.stopPropagation(); setFlagAtFrame(t._key, 'occluded') }}
                        className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]" style={{ borderColor: kfHere?.occluded ? 'var(--primary)' : 'var(--border)', color: kfHere?.occluded ? 'var(--primary)' : undefined }}>
                        {kfHere?.occluded ? '取消遮挡' : '遮挡'}
                      </button>
                      {hasKfHere && (
                        <button title="只删除当前帧关键帧；不会删除整个 track" disabled={t._locked} onClick={(e) => { e.stopPropagation(); deleteKeyframeAtFrame(t._key, true) }}
                          className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]" style={{ borderColor: 'var(--chart-4)', color: 'var(--chart-4)' }}>
                          <Diamond className="h-3 w-3" />删关键帧
                        </button>
                      )}
                      {t.source === 'ai' && !t._locked && (
                        <button onClick={(e) => { e.stopPropagation(); trackApi.adopt(taskId, t.id).then((h) => setTracks((prev) => prev.map((x) => x._key === t._key ? { ...h, _key: h.id, _dirty: false } : x))) }}
                          className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]" style={{ borderColor: 'var(--chart-2)', color: 'var(--chart-2)' }}>
                          <Check className="h-3 w-3" />采纳
                        </button>
                      )}
                      {t._locked && <span className="text-[11px]" style={{ color: 'var(--muted-foreground)' }}>已锁定</span>}
                      <button title="复制此 track 为新 track（同几何/标签，用于相似对象）" disabled={t._locked} onClick={(e) => { e.stopPropagation(); duplicateTrack(t) }}
                        className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]" style={{ borderColor: 'var(--border)' }}>
                        <Copy className="h-3 w-3" />复制
                      </button>
                      <span className="inline-flex items-center gap-0.5" onClick={(e) => e.stopPropagation()}>
                        <input type="number" value={propN} onChange={(e) => setPropN(Number(e.target.value) || 1)} title="传播帧数"
                          className="h-6 w-11 rounded border px-1 text-center text-[11px] outline-none" style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                        <button title="把当前帧几何复制到后 N 帧（生成关键帧，中间自动插值——静止对象快速铺满）" disabled={t._locked} onClick={(e) => { e.stopPropagation(); propagateKeyframe(t._key, propN) }}
                          className="inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]" style={{ borderColor: 'var(--border)' }}>
                          <ArrowRightToLine className="h-3 w-3" />传播
                        </button>
                      </span>
                      <button title="删除整个 track 及其全部关键帧" disabled={t._locked} onClick={(e) => { e.stopPropagation(); if (confirm(`删除整个 track「${t.label} #${t.track_id || '新'}」及其全部关键帧？\n如果只是对象离开画面，请使用「标出画」。`)) deleteTrack(t) }}
                        className="ml-auto inline-flex items-center gap-1 rounded border border-red-200 px-1.5 py-0.5 text-[11px] text-red-600">
                        <Trash2 className="h-3 w-3" />删 track
                      </button>
                      </div>
                      {attrsForLabel(t.label).length > 0 && (
                        <div className="flex flex-wrap items-center gap-2 border-t pt-1.5" style={{ borderColor: 'var(--border)' }}>
                          <span className="text-[10px]" style={{ color: 'var(--muted-foreground)' }}>属性</span>
                          {attrsForLabel(t.label).map((a: any) => (
                            <AttrControl key={a.name} attr={a} value={t.attrs?.[a.name]} disabled={t._locked} onChange={(v) => setAttr(t._key, a.name, v)} />
                          ))}
                        </div>
                      )}
                      {kfAttrsForLabel(t.label).length > 0 && (
                        <div className="flex flex-wrap items-center gap-2 border-t pt-1.5" style={{ borderColor: 'var(--border)' }}>
                          <span className="text-[10px]" style={{ color: 'var(--muted-foreground)' }} title="逐帧属性：挂在当前帧的关键帧上">本帧属性</span>
                          {kfHere ? kfAttrsForLabel(t.label).map((a: any) => (
                            <AttrControl key={a.name} attr={a} value={kfHere.attrs?.[a.name]} disabled={t._locked} onChange={(v) => setKeyframeAttr(t._key, a.name, v)} />
                          )) : (
                            <span className="text-[10px]" style={{ color: 'var(--muted-foreground)' }}>当前帧无关键帧 · 先在该帧改框/打关键帧</span>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
          <div className="border-t px-3 py-2 text-[11px] leading-relaxed" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
            矩形/多边形=新 track · 编辑拖框/顶点=当前帧关键帧 · 标出画≠删除 · 删关键帧≠删 track · 时间轴拖关键帧菱形=改帧号 · 0–9=快捷标签 · ◇◁▷ 关键帧间跳 · ←→ 逐帧 · C/Shift+V 步进 · G 跳帧 · L 锁定 · Q 遮挡 · O 出画 · 空格 播放 · Ctrl+Z/Y 撤销重做 · Ctrl+S 保存
          </div>
        </div>
      </div>

      {helpOpen && <ShortcutHelp onClose={() => setHelpOpen(false)} />}
    </div>
  )
}

// F1 快捷键帮助面板：一览全部快捷键（Esc / 点遮罩 / 再按 F1 关闭）。
function ShortcutHelp({ onClose }: { onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  const groups: { title: string; rows: [string, string][] }[] = [
    { title: '播放 / 导航', rows: [['空格', '播放 / 暂停'], ['← / →', '上一帧 / 下一帧'], ['C / Shift+V', '后退 / 前进 N 帧（步长可设）'], ['G', '跳到指定帧号'], ['◇◁ / ▷◇', '上一 / 下一关键帧（选中 track）'], ['有物体 / 空帧', '智能跳转（Shift+点击 = 反向）']] },
    { title: '对象 / track', rows: [['矩形 / 多边形', '画一个即新建 track（当前帧关键帧）'], ['拖框 / 拖顶点', '在当前帧生成关键帧'], ['0–9', '快捷标签（按本体 hotkey 改标签+颜色）'], ['L', '锁定 / 解锁选中 track'], ['Q', '当前帧遮挡（occluded）'], ['O', '当前帧出画（outside，≠ 删除）'], ['Del / Backspace', '删除当前帧关键帧（≠ 删 track）']] },
    { title: '时间轴', rows: [['拖动时间轴', '定位任意帧（三向联动）'], ['拖关键帧菱形', '改该关键帧的帧号']] },
    { title: '审核（仅审核态）', rows: [['N', '跳到下一条低置信 track（按 ai_score 从低到高循环）'], ['速度 ▾', '倍速播放，overlay 跟着走'], ['通过 / 驳回', '逐 track 裁决；还有 track 被驳回时整体不能通过'], ['返工对比', '只看标注员这一轮真正动过的地方']] },
    { title: '全局', rows: [['Ctrl+S', '保存'], ['Ctrl+Z / Ctrl+Y', '撤销 / 重做'], ['F1', '打开 / 关闭本帮助'], ['F11', '全屏']] },
  ]
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className="max-h-[85vh] w-full max-w-2xl overflow-auto rounded-xl border p-5 shadow-xl"
        style={{ background: 'var(--card)', borderColor: 'var(--border)' }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-base font-semibold">快捷键 · 视频标注</h2>
          <Button size="sm" variant="ghost" onClick={onClose}><X className="h-4 w-4" /></Button>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          {groups.map((g) => (
            <div key={g.title}>
              <div className="mb-1.5 text-[11px] font-medium" style={{ color: 'var(--muted-foreground)' }}>{g.title}</div>
              <div className="space-y-1">
                {g.rows.map(([k, v]) => (
                  <div key={k} className="flex items-start gap-2 text-xs">
                    <kbd className="shrink-0 rounded border px-1.5 py-0.5 font-mono text-[10px]" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>{k}</kbd>
                    <span style={{ color: 'var(--muted-foreground)' }}>{v}</span>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

// Draggable scrubber: drag/click to seek (3-way synced via currentFrame), second
// ticks + timecodes, ALL tracks' keyframe markers (annotated frames), selected
// track's keyframes prominent, hover preview.
function Scrubber({ count, current, ptsMs, fps, durationMs, tracks, selectedKey, onSeek, onMoveKeyframe }: {
  count: number; current: number; ptsMs: number[]; fps: number; durationMs: number
  tracks: TrackDraft[]; selectedKey: string | null; onSeek: (k: number) => void
  onMoveKeyframe?: (from: number, to: number) => void
}) {
  const ref = useRef<HTMLDivElement | null>(null)
  const dragging = useRef(false)
  const [hover, setHover] = useState<number | null>(null)
  const [dragKf, setDragKf] = useState<{ from: number; to: number } | null>(null)
  const selected = tracks.find((t) => t._key === selectedKey) ?? null
  type Marker = {
    key: string
    frame: number
    label: string
    color: string
    selected: boolean
    outside?: boolean
    occluded?: boolean
    aggregated?: boolean
  }
  const frameAt = (clientX: number): number => {
    const el = ref.current; if (!el || count <= 1) return 0
    const r = el.getBoundingClientRect()
    return Math.round(Math.max(0, Math.min(1, (clientX - r.left) / r.width)) * (count - 1))
  }
  const pct = (f: number) => (count > 1 ? (f / (count - 1)) * 100 : 0)

  // second ticks (cap to ~30 for long clips)
  const ticks = useMemo(() => {
    const totalSec = Math.max(1, Math.floor(durationMs / 1000))
    const stepSec = totalSec > 30 ? Math.ceil(totalSec / 30) : (totalSec > 12 ? 2 : 1)
    const out: { f: number; label: string }[] = []
    for (let s = 0; s <= totalSec; s += stepSec) {
      const f = ptsMs.length ? nearestFrame(ptsMs, s * 1000) : Math.round(s * fps)
      out.push({ f: Math.min(f, count - 1), label: `${s}s` })
    }
    return out
  }, [durationMs, ptsMs, fps, count])

  const keyframeMarkers = useMemo(() => {
    const selectedMarkers: Marker[] = []
    const otherMarkers: Marker[] = []
    tracks.forEach((t) => {
      if (t._hidden) return
      t.keyframes.forEach((k, i) => {
        const marker: Marker = {
          key: `${t._key}-${i}`,
          frame: k.frame,
          label: t.label,
          color: t.color || '#e6194B',
          selected: t._key === selectedKey,
          outside: k.outside,
          occluded: k.occluded,
        }
        if (marker.selected) selectedMarkers.push(marker)
        else otherMarkers.push(marker)
      })
    })

    const denom = Math.max(1, count - 1)
    const compact = (markers: Marker[], limit: number, buckets: number) => {
      if (limit <= 0) return []
      if (markers.length <= limit) return markers
      const seen = new Set<string>()
      const out: Marker[] = []
      for (const marker of markers) {
        const bucket = [
          Math.round((marker.frame / denom) * buckets),
          marker.outside ? 'outside' : 'visible',
          marker.occluded ? 'occluded' : 'clear',
        ].join(':')
        if (seen.has(bucket)) continue
        seen.add(bucket)
        out.push({ ...marker, aggregated: true })
        if (out.length >= limit) break
      }
      return out
    }

    const selectedLimit = Math.min(MARKER_BUDGET, Math.max(240, Math.floor(MARKER_BUDGET * 0.55)))
    const visibleSelected = compact(selectedMarkers, selectedLimit, 1600)
    const visibleOther = compact(otherMarkers, Math.max(0, MARKER_BUDGET - visibleSelected.length), 1000)
    return [...visibleOther, ...visibleSelected]
  }, [tracks, selectedKey, count])

  return (
    <div className="px-3 pb-2 pt-1 select-none">
      <div ref={ref} className="relative h-9 cursor-pointer rounded" style={{ background: 'var(--muted)' }}
        onPointerDown={(e) => { dragging.current = true; ref.current?.setPointerCapture?.(e.pointerId); onSeek(frameAt(e.clientX)) }}
        onPointerMove={(e) => { setHover(frameAt(e.clientX)); if (dragging.current) onSeek(frameAt(e.clientX)) }}
        onPointerUp={(e) => { dragging.current = false; ref.current?.releasePointerCapture?.(e.pointerId) }}
        onPointerLeave={() => setHover(null)}>
        {/* second ticks + labels */}
        {ticks.map((t, i) => (
          <div key={i} className="pointer-events-none absolute top-0 h-full" style={{ left: `${pct(t.f)}%` }}>
            <div className="h-2 w-px" style={{ background: 'var(--border)' }} />
            <span className="absolute top-2 -translate-x-1/2 font-mono text-[9px]" style={{ color: 'var(--muted-foreground)' }}>{t.label}</span>
          </div>
        ))}
        {/* selected track's active span (first→last keyframe) as a colored baseline
            so the selected track's coverage stands out on the timeline. */}
        {selected && selected.keyframes.length > 1 && (() => {
          const fs = selected.keyframes.map((k) => k.frame)
          const a = Math.min(...fs), b = Math.max(...fs)
          return <span className="pointer-events-none absolute bottom-0 h-1 rounded-full" style={{ left: `${pct(a)}%`, width: `${Math.max(pct(b) - pct(a), 0)}%`, background: selected.color || '#e6194B', opacity: 0.65 }} />
        })()}
        {/* Keyframe markers are budgeted for long videos: selected track first,
            other tracks merged by nearby timeline position. */}
        {keyframeMarkers.map((m) => {
          const draggable = m.selected && !!onMoveKeyframe && !m.aggregated
          const shownFrame = dragKf && dragKf.from === m.frame ? dragKf.to : m.frame
          return (
          <span key={m.key} title={draggable ? `拖动改帧号 · 帧 ${m.frame}` : `${m.label} · 帧 ${m.frame}${m.outside ? ' · outside' : ''}${m.occluded ? ' · occluded' : ''}${m.aggregated ? ' · 附近关键帧已聚合' : ''}`}
            onPointerDown={draggable ? (e) => { e.stopPropagation(); (e.target as HTMLElement).setPointerCapture?.(e.pointerId); setDragKf({ from: m.frame, to: m.frame }) } : undefined}
            onPointerMove={draggable ? (e) => { if (dragKf) { e.stopPropagation(); setDragKf({ from: dragKf.from, to: frameAt(e.clientX) }) } } : undefined}
            onPointerUp={draggable ? (e) => { if (dragKf) { e.stopPropagation(); (e.target as HTMLElement).releasePointerCapture?.(e.pointerId); if (dragKf.to !== dragKf.from) onMoveKeyframe!(dragKf.from, dragKf.to); setDragKf(null) } } : undefined}
            className={`absolute -translate-x-1/2 rotate-45 ${draggable ? 'pointer-events-auto cursor-ew-resize' : 'pointer-events-none'}`}
            style={{
              left: `${pct(shownFrame)}%`,
              bottom: m.selected ? 3 : 4,
              width: m.selected ? 12 : 8,
              height: m.selected ? 12 : 8,
              background: m.outside ? 'transparent' : m.color,
              border: `${m.selected ? 2 : 1.5}px solid ${m.selected ? 'var(--background)' : m.color}`,
              boxShadow: m.selected ? `0 0 0 1.5px ${m.color}` : 'none',
              opacity: m.selected ? 1 : (m.aggregated ? 0.62 : 0.75),
              zIndex: (dragKf && dragKf.from === m.frame) ? 6 : (m.selected ? 4 : 2),
            }} />
          )
        })}
        {/* hover preview */}
        {hover != null && (
          <div className="pointer-events-none absolute -top-6 z-10 -translate-x-1/2 whitespace-nowrap rounded px-1.5 py-0.5 font-mono text-[10px]"
            style={{ left: `${pct(hover)}%`, background: 'var(--foreground)', color: 'var(--background)' }}>
            帧 {hover} · {fmtTC(ptsMs[hover] ?? (hover * 1000) / fps)}
          </div>
        )}
        {hover != null && <span className="pointer-events-none absolute top-0 h-full w-px" style={{ left: `${pct(hover)}%`, background: 'var(--muted-foreground)' }} />}
        {/* playhead */}
        <span className="pointer-events-none absolute top-0 h-full w-0.5" style={{ left: `${pct(current)}%`, background: 'var(--primary)' }} />
      </div>
    </div>
  )
}

function nearestFrame(ptsMs: number[], ms: number): number {
  let lo = 0, hi = ptsMs.length - 1
  if (ms <= ptsMs[0]) return 0
  if (ms >= ptsMs[hi]) return hi
  while (lo < hi) { const mid = (lo + hi) >> 1; if (ptsMs[mid] < ms) lo = mid + 1; else hi = mid }
  return lo > 0 && Math.abs(ptsMs[lo - 1] - ms) <= Math.abs(ptsMs[lo] - ms) ? lo - 1 : lo
}

// One track-level ontology attribute control (boolean/select/number/text/multiselect).
function AttrControl({ attr, value, disabled, onChange }: {
  attr: any; value: any; disabled?: boolean; onChange: (v: any) => void
}) {
  const label = attr.display || attr.name
  const box = 'rounded border px-1 py-0.5 text-[11px] outline-none disabled:opacity-60'
  const st = { borderColor: 'var(--input)', background: 'var(--background)' }
  if (attr.type === 'boolean') return (
    <label className="inline-flex items-center gap-1 text-[11px]" onClick={(e) => e.stopPropagation()}>
      <input type="checkbox" checked={!!value} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />{label}
    </label>
  )
  if (attr.type === 'select') return (
    <label className="inline-flex items-center gap-1 text-[11px]" onClick={(e) => e.stopPropagation()}>{label}
      <select value={value ?? ''} disabled={disabled} onChange={(e) => onChange(e.target.value || undefined)} className={`h-6 ${box}`} style={st}>
        <option value="">—</option>
        {(attr.options ?? []).map((o: string) => <option key={o} value={o}>{o}</option>)}
      </select>
    </label>
  )
  if (attr.type === 'number') return (
    <label className="inline-flex items-center gap-1 text-[11px]" onClick={(e) => e.stopPropagation()}>{label}
      <input type="number" value={value ?? ''} disabled={disabled} className={`h-6 w-16 ${box}`} style={st}
        onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))} />
    </label>
  )
  return (
    <label className="inline-flex items-center gap-1 text-[11px]" onClick={(e) => e.stopPropagation()}>{label}
      <input value={value ?? ''} disabled={disabled} placeholder={attr.type === 'multiselect' ? '逗号分隔' : ''} className={`h-6 w-24 ${box}`} style={st}
        onChange={(e) => onChange(e.target.value || undefined)} />
    </label>
  )
}

export default VideoAnnotationPage
