import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, Loader2, AlertTriangle, Eye, EyeOff, Pencil, Trash2 } from 'lucide-react'

import { taskApi } from '@/api/imageTask'
import { assetApi } from '@/api/asset'
import { trackApi, type VideoTrack } from '@/api/videoTask'
import { fetchVolumeMeta, loadAssetVolume, type VolumeMeta } from '@/api/volume'
import { MPRView, type MeasureShape } from '@/components/domain/volume-annotation/MPRView'
import { MPR_PLANES, planeSliceCount, planeToVoxel, type MPRPlane, type Vec3 } from '@/lib/mprGeometry'
import { isCanonicalDirection } from '@/lib/mprOrientation'
import {
  annotatedSlices,
  keyframesToMaskVolume,
  maskVolumeToKeyframes,
  paintBrush,
  type MaskKeyframe,
} from '@/lib/maskSlice'
import { formatVolume, segmentStats, sliceAreaMm2 } from '@/lib/maskStats'
import { applyOp, rebuildInto, replay, type DraftOp, type DraftState } from '@/lib/draftLog'
import { DraftWriter, discardDraft, loadDraft } from '@/lib/draftStore'
import { exportVolumeSegmentation } from '@/api/export'
import { deleteServerDraft, fetchServerDraft, putServerDraft } from '@/api/draft'
import {
  angleDeg,
  describeMeasurement,
  distanceMm,
  formatDeg,
  formatMm,
  RECIST_MIN_TARGET_MM,
  type Measurement,
} from '@/lib/measure'
import { buildWindowLUT } from '@/lib/windowLevel'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import * as perms from '@/lib/roles'
import { useEditLock } from '@/hooks/useEditLock'

// 可编辑的任务态(与图片/视频/病理一致):待认领 / 进行中 / 被驳回返工。其它态
// (审核中、已完成、已归档)对标注员只读——复核员看到的也是只读段。
const EDITABLE_STATES = new Set(['HUMAN_PENDING', 'HUMAN_IN_PROGRESS', 'QA_REJECTED'])

// #19 整卷 uint16 常驻的浏览器内存预算(字节)。大卷 + 多段掩膜会把标签页 OOM,超预算就拒。
// 768MB:512³ 整卷(268MB)+ 若干段仍有余量;1024³(≈2GB)会被拒,交给桌面工具处理。
const VOLUME_MEMORY_BUDGET = 768 << 20

// 3D 体数据(CT/MRI)标注工作台 · MPR 三视(执行方案-04 · C3.2)。
//
// 数据流:volume_meta(几何+窗位)→ 并发拉逐片 16-bit PNG → 装成一卷 Uint16Array
// → 三个平面各自重切 + 窗位 LUT 上屏。**全程 16-bit**(C0.5):调窗只是换一张
// LUT 重画,不回服务器、不丢精度。
//
// C-H3:几何/十字一律在体素索引空间;世界朝向来自 volume_meta.direction。
// 若 handedness 为负(单轴镜像),顶部常驻警告——左右翻转在医学影像里是会出人命
// 的那种错,宁可显眼地提示,也不静默渲染。

/** 一个分割段 = 一条 voxel_mask 轨迹 + 一卷常驻掩膜。 */
interface Segment {
  /** 前端稳定标识:已存段用 track id,新段用临时 id。 */
  uid: string
  /** 已保存到后端的轨迹 id(新建未保存时为空)。 */
  trackId?: string
  /** 每任务的逻辑 track 号(服务端分配)。 */
  trackNum?: number
  version?: number
  label: string
  color: string
  visible: boolean
  dirty: boolean
  mask: Uint8Array
}

/** 段的默认配色(高对比、彼此可分——[[annotator-ux-simplicity]]:别用太淡的色)。 */
const SEG_COLORS = ['#ff5050', '#4ade80', '#60a5fa', '#fbbf24', '#c084fc', '#f472b6', '#22d3ee', '#fb923c']

/** 从 axios 错误里取后端文案(护栏那条 400 必须原样呈现给标注员)。 */
function errText(e: unknown): string {
  const d = (e as { response?: { data?: { message?: string; error?: string } } })?.response?.data
  return d?.message ?? d?.error ?? (e instanceof Error ? e.message : String(e))
}

function hexToRgb(hex: string): [number, number, number] {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim())
  if (!m) return [255, 80, 80]
  const n = parseInt(m[1], 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

/** 把页面的段列表包成回放器认识的状态容器（掩膜就地共享，不拷贝那 11 MB）。 */
function draftStateOf(segs: Segment[]): DraftState {
  return {
    masks: new Map(segs.map((sg) => [sg.uid, sg.mask])),
    segs: new Map(segs.map((sg) => [sg.uid, { label: sg.label, color: sg.color }])),
  }
}

export function VolumeAnnotationPage() {
  const { id } = useParams<{ id: string }>()
  const taskId = Number(id)
  const navigate = useNavigate()

  const { data: task } = useQuery({ queryKey: ['task', taskId], queryFn: () => taskApi.get(taskId) })
  const { data: asset } = useQuery({
    queryKey: ['asset-detail', task?.asset_id],
    queryFn: () => assetApi.detail(task!.asset_id),
    enabled: !!task?.asset_id,
  })
  const assetId = task?.asset_id

  // #15 编辑锁 + 任务态门禁(与图片/视频/病理一致):两名标注员同时打开→后到者只读;
  // 进入已提交/FINALIZED 任务→只读。否则画/删/AI/保存都放行,导致冲突或终稿后 live 漂移。
  const role = useAuthStore((s) => s.user)?.role ?? ''
  const editableState = !!task && EDITABLE_STATES.has(task.state)
  const { readOnly: lockedByOther } = useEditLock(taskId, perms.canAnnotate(role) && editableState)
  const editable = perms.canAnnotate(role) && editableState && !lockedByOther

  const { data: meta, error: metaError } = useQuery<VolumeMeta>({
    queryKey: ['volume-meta', assetId],
    queryFn: () => fetchVolumeMeta(assetId!),
    enabled: !!assetId,
  })

  const [volume, setVolume] = useState<Uint16Array | null>(null)
  const [progress, setProgress] = useState({ done: 0, total: 0 })
  const [loadError, setLoadError] = useState<string | null>(null)
  const [crosshair, setCrosshair] = useState<Vec3>([0, 0, 0])
  const [zoom, setZoom] = useState(1)
  const [win, setWin] = useState<{ width: number; center: number } | null>(null)

  // ---- 标注(voxel_mask)状态:**多分割** ----
  // 真实医学分割几乎不存在只标一个结构的场景(肝/肿瘤/血管各一段),3D Slicer 与
  // OHIF 的面板都是"段列表 + 当前选中段"。每段 = 一条 voxel_mask 轨迹(后端本就
  // 支持多条,track_id 由服务端分配/去重)。
  // 每段的掩膜以**整卷**常驻(与影像体同布局)→ 三视共用、结构性对齐;
  // 保存时才逐 z 切成 bbox-local RLE 关键帧。
  const [segments, setSegments] = useState<Segment[]>([])
  // #16 轨迹加载态:未就绪前禁止新建/涂抹(否则用户先建段涂抹、track list 迟到回包会
  // setSegments 整表覆盖);加载失败显式报错,绝不用空列表伪装成功。
  const [tracksLoaded, setTracksLoaded] = useState(false)
  const [tracksError, setTracksError] = useState<string | null>(null)
  const [activeId, setActiveId] = useState<string | null>(null)
  const [maskRev, setMaskRev] = useState(0) // 掩膜内容就地改，用版本号驱动重绘(逐笔)
  // #18 面板统计(annotatedSlices/segmentStats)会全卷扫描:只在**笔画结束/离散操作**后
  // 才重算,别绑 maskRev(每 mousemove 都扫全卷 → 大卷冻结、丢画笔事件)。
  const [statsRev, setStatsRev] = useState(0)
  const [paintMode, setPaintMode] = useState(false)
  const [brush, setBrush] = useState(6)
  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState<string | null>(null)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  // ---- C0.6 草稿:逐笔操作日志 → IndexedDB ----
  // 契约硬需求(2026-07-18 用户拍板):勾画 30 分钟 → 强杀浏览器 → 恢复到最后一笔。
  // writer 放 ref:它持有攒批缓冲与事件监听,身份必须恒定,不能随渲染重建。
  const draftRef = useRef<DraftWriter | null>(null)
  const [draftErr, setDraftErr] = useState<string | null>(null)
  const [pendingDraft, setPendingDraft] = useState<{ ops: DraftOp[]; savedAt: number } | null>(null)
  // ---- 撤销/重做(契约:与草稿共用同一份操作日志)----
  // 撤销 = **少放几条重来**,不是求每个操作的逆(笔刷的逆要记住被覆盖的每个体素)。
  // 所以需要一份"基线"掩膜:服务端载入时的样子。全部撤销应回到基线,而不是回到空
  // ——否则会把别人(或上次自己)已保存的标注一起抹掉。
  const baselineRef = useRef<Map<string, Uint8Array>>(new Map())
  // 撤销边界:每完成一笔(mouseup)/一次传播/一次清层记一个操作数。
  const marksRef = useRef<number[]>([])
  const redoRef = useRef<{ ops: DraftOp[]; mark: number }[]>([])
  // #20 一笔内的上一采样点:连线补空洞(快速拖动/合并事件时相邻圆盘之间不留缝)。endStroke 清空。
  const lastDabRef = useRef<{ z: number; u: number; v: number } | null>(null)
  const [histRev, setHistRev] = useState(0) // 驱动按钮禁用态重算
  // ---- C3.4 导出 ----
  const [exporting, setExporting] = useState(false)
  const [exportMsg, setExportMsg] = useState<string | null>(null)
  /** 后端说"没有快照"时,把待办的导出意图记下来,让用户显式选择以草稿导出。 */
  const [needDraft, setNeedDraft] = useState<{ split: boolean; reason: string } | null>(null)
  /** 服务端草稿最后一次推送的时间/条数，用于面板显示"上次同步"。 */
  const [srvSync, setSrvSync] = useState<{ at: number; ops: number } | null>(null)
  // ---- C4.2 AI 点选传播 ----
  // 点体内一点 → SAM2 把体数据当序列跨切片传播 → 一条 voxel_mask 段(AI 源)。
  // 产出与手画段**同格式**，所以直接当普通段载入、可继续用画笔修正、可导出。
  // 与涂抹/测量**三者互斥**：同一次点击只能有一个含义。
  const [aiMode, setAiMode] = useState(false)
  const [aiBusy, setAiBusy] = useState(false)
  const [aiMsg, setAiMsg] = useState<string | null>(null)
  // ---- C3.25 测量（RECIST 长径 / 角度）----
  // 放射科医生每天用得最多的不是画笔是尺子：RECIST 用最长径判断肿瘤缓解还是
  // 进展，直接决定治疗方案改不改。测量与分割**互斥**——同一次点击不可能既是
  // 落笔又是量点，让两者共存只会让人误画。
  const [measureMode, setMeasureMode] = useState<'off' | 'ruler' | 'angle'>('off')
  const [measurements, setMeasurements] = useState<Measurement[]>([])
  // 进行中的测量：**必须记住是在哪个平面开始的**。一条测量跨平面取点没有意义
  // （面内两轴的物理含义都变了），而临时点若不绑定平面，会同时画在三个视图上。
  const [pending, setPending] = useState<{ plane: MPRPlane; pts: { u: number; v: number }[] } | null>(null)
  /**
   * 周期 flush 的定时器只在 [meta, taskId] 上重建（否则每画一笔都重置 5 分钟，
   * 草稿永远推不出去）。但这样一来闭包里的 segments/maskRev 是**创建那一刻**的。
   * 所以把最新值放进 ref，由 flush 时读取——注释说"用 ref"，代码就必须真的用。
   */
  const latestRef = useRef({ segments: [] as Segment[], maskRev: 0 })
  latestRef.current = { segments, maskRev }

  const active = segments.find((s) => s.uid === activeId) ?? null
  const patchSeg = (uid: string, patch: Partial<Segment>) => {
    setSegments((list) => list.map((s) => (s.uid === uid ? { ...s, ...patch } : s)))
    // 改名/改色进草稿:丢了不致命,但"恢复回来又叫回分割 1"是实打实的返工。
    if (patch.label !== undefined || patch.color !== undefined) {
      draftRef.current?.push({ op: 'patchSeg', seg: uid, label: patch.label, color: patch.color })
    }
  }

  // 拉整卷。离开页面/换资产时中止——别让几百片请求在后台继续跑。
  useEffect(() => {
    if (!assetId || !meta) return
    const ac = new AbortController()
    setVolume(null)
    setLoadError(null)
    // #19 浏览器内存预算:整卷 uint16 常驻 = 2×体素;每段掩膜再各占 1×(+撤销基线 1×)。
    // 大卷 + 几段就能把标签页 OOM(整卷复制两遍那类)。超预算就明确拒,别让页面崩死。
    // 真正的稀疏切片/copy-on-write 存储是后续欠账;这里先设预算兜底。
    const volBytes = meta.dims[0] * meta.dims[1] * meta.dims[2] * 2
    if (volBytes > VOLUME_MEMORY_BUDGET) {
      setLoadError(
        `体数据约 ${(volBytes / (1 << 20)).toFixed(0)} MB,超过浏览器内存预算 ${VOLUME_MEMORY_BUDGET >> 20} MB` +
          `(每个分割段还要再占一份)。请在桌面工具(3D Slicer 等)中处理,或先降采样/裁剪。`,
      )
      return
    }
    setProgress({ done: 0, total: meta.dims[2] })
    loadAssetVolume(assetId, meta.dims, {
      signal: ac.signal,
      onProgress: (done, total) => setProgress({ done, total }),
    })
      .then((vol) => {
        setVolume(vol)
        setCrosshair([Math.floor(meta.dims[0] / 2), Math.floor(meta.dims[1] / 2), Math.floor(meta.dims[2] / 2)])
      })
      .catch((e: unknown) => {
        if ((e as { name?: string })?.name === 'AbortError') return
        setLoadError(e instanceof Error ? e.message : String(e))
      })
    return () => ac.abort()
  }, [assetId, meta])

  // 读回全部已存的 voxel_mask 轨迹 → 每条一段(可继续编辑)。
  useEffect(() => {
    if (!meta || !taskId) return
    let cancelled = false
    trackApi
      .list(taskId)
      .then((tracks: VideoTrack[]) => {
        if (cancelled) return
        setTracksError(null)
        setTracksLoaded(true) // #16 服务端轨迹已就绪 → 放开新建/涂抹
        const vms = tracks.filter((t: VideoTrack) => t.kind === 'voxel_mask' && t.is_active)
        const segs: Segment[] = vms.map((t, i) => ({
          uid: t.id,
          trackId: t.id,
          trackNum: t.track_id,
          version: t.version,
          label: t.label || `分割 ${i + 1}`,
          color: t.color || SEG_COLORS[i % SEG_COLORS.length],
          visible: true,
          dirty: false,
          mask: keyframesToMaskVolume(t.keyframes as unknown as MaskKeyframe[], meta.dims),
        }))
        setSegments(segs)
        setActiveId(segs[0]?.uid ?? null)
        setMaskRev((r) => r + 1)

        // 基线快照:撤销要回到"服务端载入时的样子"。只拷已存段(新建段基线为空)。
        baselineRef.current = new Map(segs.map((sg) => [sg.uid, sg.mask.slice()]))
        marksRef.current = []
        redoRef.current = []

        // ---- C0.6:建草稿写入器 + 检测上次遗留的草稿 ----
        const baseline = Object.fromEntries(
          segs.map((sg) => [sg.uid, { trackId: sg.trackId!, version: sg.version! }]),
        )
        draftRef.current?.dispose()
        draftRef.current = new DraftWriter({ taskId: String(taskId), assetId: String(assetId) }, baseline, (e) =>
          // 草稿写不进去(隐私模式/配额满/IndexedDB 被禁)必须**说出来**:
          // 标注员以为有兜底、实则没有,是最危险的那种"看起来正常"。
          setDraftErr(e instanceof Error ? e.message : String(e)),
        )
        void loadDraft(String(taskId)).then((rec) => {
          if (cancelled || !rec || rec.ops.length === 0) return
          // **不自动回放**。基线可能已经变(别人存过、或本人在另一台机器存过),
          // 静默叠加会让标注员拿到一份莫名其妙的掩膜。交给人决定。
          setPendingDraft({ ops: rec.ops, savedAt: rec.savedAt })
        })
      })
      .catch((e: unknown) => {
        // #16 加载失败不能伪装成"空列表"——那会让标注员在"没有既有段"的错觉下继续画,
        // 保存时又和真实轨迹撞版本。显式报错、禁编辑,让人重试。
        if (!cancelled) setTracksError(errText(e))
      })
    return () => {
      cancelled = true
    }
  }, [meta, taskId, assetId])

  /**
   * 服务端草稿：每 flush_interval_sec（默认 5 分钟）推一次操作日志。
   *
   * 与本地 IndexedDB 草稿分工不同：本地那份逐笔即时、崩溃丢失≈0，但只在这台
   * 机器上；这一份解决**换机器续作**。所以间隔可以很长——它不是崩溃兜底。
   *
   * ⚠️ 推送失败**不打断标注**：网断了、服务端挂了都不该让人没法画。但也不能
   * 悄悄失败——面板上会显示"上次同步"的时间，久未更新自己看得见。
   */
  useEffect(() => {
    if (!meta || !taskId) return
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | null = null

    const push = async () => {
      const w = draftRef.current
      if (stopped || !w || w.ops.length === 0) return
      try {
        await putServerDraft(taskId, {
          baseline: Object.fromEntries(
            latestRef.current.segments
              .filter((sg) => sg.trackId)
              .map((sg) => [sg.uid, { trackId: sg.trackId!, version: sg.version! }]),
          ),
          ops: [...w.ops],
          op_count: w.ops.length,
          client_rev: latestRef.current.maskRev,
        })
        if (!stopped) setSrvSync({ at: Date.now(), ops: w.ops.length })
      } catch (e: unknown) {
        // 413 = 草稿超限，后端文案会指路"先保存"。其余静默重试下一轮。
        const status = (e as { response?: { status?: number } })?.response?.status
        if (status === 413 && !stopped) setDraftErr(errText(e))
      }
    }

    void fetchServerDraft(taskId)
      .then(({ flushIntervalSec }) => {
        if (stopped) return
        const tick = () => {
          void push().finally(() => {
            if (!stopped) timer = setTimeout(tick, flushIntervalSec * 1000)
          })
        }
        timer = setTimeout(tick, flushIntervalSec * 1000)
      })
      .catch(() => {
        /* 拿不到配置就不做服务端草稿——本地那份仍然在工作 */
      })

    return () => {
      stopped = true
      if (timer) clearTimeout(timer)
    }
    // segments/maskRev 变化不该重启计时器（否则每一笔都会重置 5 分钟，永远推不出去）。
    // 用 ref 取最新值即可 —— 这里刻意只依赖 meta/taskId。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [meta, taskId])

  // 离开页面:落盘 + 摘监听。**先 flush 再 dispose**,否则攒批里的最后几笔会丢。
  useEffect(() => {
    return () => {
      const w = draftRef.current
      if (!w) return
      void w.flush().finally(() => w.dispose())
      draftRef.current = null
    }
  }, [taskId])

  /**
   * AI 点选传播。点体内一点 → 后端调 SAM2 → 新 voxel_mask 段追加进列表。
   *
   * ⚠️ **只追加新段，绝不重建已有段**：已有段的掩膜在内存里（可能有未保存编辑），
   * 从后端 list 整体重建会把这些编辑踩掉。所以只取返回的那条新轨迹。
   */
  const aiPropagate = async (plane: MPRPlane, u: number, v: number) => {
    if (!editable) return
    if (!meta || plane !== 'axial') {
      setAiMsg('AI 点选只在轴位视图（关键帧按 z 存）')
      return
    }
    setAiBusy(true)
    setAiMsg(null)
    const z = crosshair[2]
    try {
      const res = await trackApi.propagateVolume(taskId, {
        prompt_z: z, points: [[u, v, 1]], label: 'AI 分割',
      })
      // 取回刚建的那条轨迹，构造成段追加。
      const tracks = await trackApi.list(taskId)
      const t = tracks.find((x) => x.id === res.track_id)
      if (!t) {
        setAiMsg('传播成功但未取回新段，请刷新')
        return
      }
      const seg: Segment = {
        uid: t.id, trackId: t.id, trackNum: t.track_id, version: t.version,
        label: t.label || 'AI 分割',
        color: t.color || SEG_COLORS[segments.length % SEG_COLORS.length],
        visible: true, dirty: false,
        mask: keyframesToMaskVolume(t.keyframes as unknown as MaskKeyframe[], meta.dims),
      }
      // 新段也进撤销基线（它是服务端来的，全部撤销应回到含它的状态）。
      baselineRef.current.set(seg.uid, seg.mask.slice())
      setSegments((list) => [...list, seg])
      setActiveId(seg.uid)
      setMaskRev((r) => r + 1)
      setAiMode(false)
      setAiMsg(`AI 分割完成：${res.keyframes} 层，可用画笔修正后保存`)
    } catch (e: unknown) {
      const status = (e as { response?: { status?: number } })?.response?.status
      if (status === 429) {
        // GPU 候诊室满 → 429（B2.8），不是错误，稍后再点。
        setAiMsg('AI 服务正忙（队列已满），稍后再试')
      } else {
        setAiMsg(`AI 传播失败：${errText(e)}`)
      }
    } finally {
      setAiBusy(false)
    }
  }

  /** 一笔画完（mouseup）/一次整体操作完成 → 记一个撤销边界。 */
  const endStroke = () => {
    lastDabRef.current = null // #20 一笔结束,下一笔从起点重新落(不跨笔连线)
    const n = draftRef.current?.ops.length ?? 0
    const marks = marksRef.current
    if (marks[marks.length - 1] === n) return // 空笔画不记（点了没画到东西）
    marks.push(n)
    setHistRev((r) => r + 1)
    setStatsRev((r) => r + 1) // #18 一笔结束才重算面板统计
  }

  const undo = () => {
    const w = draftRef.current
    if (!w || !meta || marksRef.current.length === 0) return
    marksRef.current.pop()
    const target = marksRef.current[marksRef.current.length - 1] ?? 0
    const undone = w.ops.slice(target)
    redoRef.current.push({ ops: [...undone], mark: w.ops.length })

    const kept = w.ops.slice(0, target)
    w.truncate(target) // 草稿也截断：否则撤销后崩溃，被撤销的笔画会复活
    applyHistory(kept)
    setSaveMsg(`已撤销（还可撤销 ${marksRef.current.length} 步）`)
  }

  const redo = () => {
    const w = draftRef.current
    if (!w || !meta) return
    const item = redoRef.current.pop()
    if (!item) return
    for (const op of item.ops) w.push(op)
    marksRef.current.push(item.mark)
    applyHistory([...w.ops])
    setHistRev((r) => r + 1)
  }

  /** 把掩膜复位到基线再回放 ops——就地改 buffer，绝不新建（大缓冲区不换身份）。 */
  const applyHistory = (ops: DraftOp[]) => {
    if (!meta) return
    const state = draftStateOf(segments)
    rebuildInto(state, meta.dims, baselineRef.current, ops)
    setSegments((list) => {
      // 段可能因撤销掉 addSeg 而整个消失
      const alive = list.filter((sg) => baselineRef.current.has(sg.uid) || state.masks.has(sg.uid))
      const merged = alive.map((sg) => {
        const m = state.segs.get(sg.uid)
        return m ? { ...sg, label: m.label, color: m.color, dirty: true } : { ...sg, dirty: true }
      })
      setActiveId((cur) => (cur && merged.some((sg) => sg.uid === cur) ? cur : (merged[0]?.uid ?? null)))
      return merged
    })
    setMaskRev((r) => r + 1)
    setHistRev((r) => r + 1)
    setStatsRev((r) => r + 1) // #18 撤销/重做后重算面板统计(掩膜就地变、active 可能不变)
  }

  // Ctrl+Z / Ctrl+Shift+Z(或 Ctrl+Y)。**必须排除输入框**：
  // 重命名段时按 Ctrl+Z 应该撤销文字，不是撤销勾画。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey)) return
      const el = e.target as HTMLElement | null
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return
      const k = e.key.toLowerCase()
      if (k === 'z' && !e.shiftKey) { e.preventDefault(); undo() }
      else if ((k === 'z' && e.shiftKey) || k === 'y') { e.preventDefault(); redo() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  /** 恢复草稿:在当前(服务端载入的)掩膜之上回放操作日志。 */
  const restoreDraft = () => {
    if (!pendingDraft || !meta) return
    const state = draftStateOf(segments)
    const n = replay(state, meta.dims, pendingDraft.ops)
    const touched = new Set(pendingDraft.ops.map((o) => o.seg))

    setSegments((list) => {
      // ① 已有段：标脏 + 同步可能被改过的名字/颜色
      const merged = list.map((sg) => {
        if (!touched.has(sg.uid)) return sg
        const m = state.segs.get(sg.uid)
        return { ...sg, dirty: true, label: m?.label ?? sg.label, color: m?.color ?? sg.color }
      })
      // ② 回放**新建出来**的段：崩溃前建的段服务端没有，必须在这里补进列表，
      //    否则掩膜恢复了却没有对应的段，界面上等于什么都没发生。
      const known = new Set(list.map((sg) => sg.uid))
      for (const [uid, m] of state.segs) {
        if (known.has(uid)) continue
        const mask = state.masks.get(uid)
        if (!mask) continue
        merged.push({ uid, label: m.label, color: m.color, visible: true, dirty: true, mask })
      }
      // ③ 恢复后必须**选中**一个段：统计、已标注层清单、涂抹目标都只对当前段
      //    生效。不选中的话，掩膜其实已经回来了，但界面上一片空白，看起来
      //    完全像"恢复失败"——用户会以为工作丢了（崩溃验收测试逮到这一步）。
      setActiveId((cur) => (cur && merged.some((sg) => sg.uid === cur) ? cur : (merged[0]?.uid ?? null)))
      return merged
    })
    setMaskRev((r) => r + 1)
    // #18 面板统计现在绑 statsRev(不再是 maskRev)。恢复草稿是一次离散掩膜变更,
    // 若恢复到的是**当前已选中的既有段**(active 不变),不 bump statsRev 面板体积/
    // 面积就停在恢复前的旧值——mask 已更新、读数却是陈的。逐笔 dab 才刻意不算统计。
    setStatsRev((r) => r + 1)
    setPendingDraft(null)
    setSaveMsg(`已恢复 ${n} 笔未保存的操作，请确认后保存`)
  }

  const dropDraft = () => {
    void discardDraft(String(taskId))
    setPendingDraft(null)
  }

  // 默认窗:用 volume_meta 的第一个预设(CT 是软组织窗,MRI 是全量程)。
  useEffect(() => {
    if (meta && !win && meta.windows.length > 0) {
      setWin({ width: meta.windows[0].width, center: meta.windows[0].center })
    }
  }, [meta, win])

  // 叠加层:每个**可见**段一层,当前段更不透明(一眼看出在画哪一段)。
  // 叠加层放进 **ref**(身份恒定),只用 overlayRev 这个数字通知重画。
  // 大缓冲区绝不作为"变化的 prop"传下去——那会把渲染进程卡死(见 MPRView 注释)。
  const overlaysRef = useRef<{ volume: Uint8Array; rgb: [number, number, number]; alpha: number }[]>([])
  const [overlayRev, setOverlayRev] = useState(0)
  useEffect(() => {
    overlaysRef.current = segments
      .filter((sg) => sg.visible)
      .map((sg) => ({
        volume: sg.mask,
        rgb: hexToRgb(sg.color),
        alpha: sg.uid === activeId ? 0.55 : 0.35,
      }))
    setOverlayRev((r) => r + 1)
  }, [segments, activeId, maskRev])

  // 已标注层清单:逐层勾画时最需要的导航——215 层里找回自己画过的那几层,
  // 没有清单就只能滚轮一层层碰运气(用户实测提的第一个缺口)。
  const annotated = useMemo(
    () => (active && meta ? annotatedSlices(active.mask, meta.dims) : []),
    [active, statsRev, meta], // #18 按笔画结束/离散操作重算,不逐 dab
  )

  // 分割统计:体积(mm³/mL)/强度/质心。参照 3D Slicer 的 Segment Statistics 与
  // OHIF v3.10 的每段统计——**"285 体素"对医生没意义,"115 mm³"才有**。
  const stats = useMemo(
    () =>
      active && meta
        ? segmentStats(active.mask, volume, meta.dims, meta.spacing, meta.slice_scl_slope, meta.slice_scl_inter)
        : null,
    [active, statsRev, meta, volume], // #18 按笔画结束/离散操作重算,不逐 dab
  )

  // 调窗 = 重建 LUT 重画,不回服务器(16-bit 常驻内存的意义)。
  const lut = useMemo(() => {
    if (!meta || !win) return null
    return buildWindowLUT(win.width, win.center, meta.slice_scl_slope, meta.slice_scl_inter)
  }, [meta, win])

  /** 某平面的当前切片号 = 十字在该平面法向轴上的分量。 */
  const sliceOf = (plane: MPRPlane): number => {
    const [i, j, k] = crosshair
    return plane === 'axial' ? k : plane === 'coronal' ? j : i
  }
  /** 面内十字坐标 (u,v)。与 planeToVoxel 互为逆,轴序在同一处定义,不各写各的。 */
  const crossOf = (plane: MPRPlane): [number, number] => {
    const [i, j, k] = crosshair
    return plane === 'axial' ? [i, j] : plane === 'coronal' ? [i, k] : [j, k]
  }

  const setSlice = (plane: MPRPlane, next: number) => {
    setCrosshair(([i, j, k]) =>
      plane === 'axial' ? [i, j, next] : plane === 'coronal' ? [i, next, k] : [next, j, k],
    )
  }
  /** 在某平面点选 → 换算回体素十字(三视联动的核心)。 */
  const pickIn = (plane: MPRPlane, u: number, v: number) => {
    setCrosshair(planeToVoxel(plane, sliceOf(plane), u, v))
  }

  /** 在 axial 上涂抹:改**当前段**掩膜体的第 k 层。 */
  /**
   * **改掩膜的唯一入口**（C0.6）。所有变更都走这里：先记进草稿日志，再用
   * `applyOp` 落到掩膜上。
   *
   * ⚠️ 别在别处直接动 `seg.mask`。恢复的正确性 = "回放出来的 ≡ 崩溃前的"，
   * 而回放用的就是 `applyOp`；只要还有第二条改写路径，两者就会漂，
   * 且漂出来的结果**看着像模像样但不是标注员画的东西**，不报任何错。
   */
  const mutate = (op: DraftOp) => {
    if (!meta) return
    const state = draftStateOf(segments)
    if (!applyOp(state, meta.dims, op)) return
    draftRef.current?.push(op)
    // 涂抹的边界由 mouseup(endStroke)划；其余操作各自就是一个撤销单元。
    if (op.op !== 'dab') endStroke()
    redoRef.current = [] // 新操作让重做栈作废（编辑器通例）
    setMaskRev((r) => r + 1) // 就地改了 buffer，用版本号驱动重绘
    const seg = segments.find((sg) => sg.uid === op.seg)
    if (seg && !seg.dirty) patchSeg(op.seg, { dirty: true })
  }

  /**
   * 测量模式下的点击：攒够点数就落一条测量。
   * ruler 两点、angle 三点（顶点是第二点）。
   */
  const measureClick = (plane: MPRPlane, u: number, v: number) => {
    if (!meta || measureMode === 'off') return
    const need = measureMode === 'ruler' ? 2 : 3
    // 中途切到别的平面 → 从头开始，而不是把两个平面的点混成一条测量。
    const base = pending && pending.plane === plane ? pending.pts : []
    const pts = [...base, { u, v }]
    if (pts.length < need) {
      setPending({ plane, pts })
      return
    }
    const slice = sliceOf(plane)
    const m: Measurement =
      measureMode === 'ruler'
        ? {
            kind: 'ruler', plane, slice, a: pts[0], b: pts[1],
            lengthMm: distanceMm(pts[0], pts[1], plane, meta.spacing),
          }
        : {
            kind: 'angle', plane, slice, a: pts[0], b: pts[1], c: pts[2],
            degrees: angleDeg(pts[0], pts[1], pts[2], plane, meta.spacing),
          }
    setMeasurements((list) => [...list, m])
    setPending(null)
  }

  const paintAt = (u: number, v: number, erase: boolean) => {
    if (!editable || !active) return
    const z = crosshair[2]
    const prev = lastDabRef.current
    lastDabRef.current = { z, u, v }
    // 同一笔同一层内连上一采样点(#20);换层或新一笔则单个圆盘。
    mutate({ op: 'dab', seg: active.uid, z, u, v, r: brush, erase, ...(prev && prev.z === z ? { pu: prev.u, pv: prev.v } : {}) })
  }

  /**
   * 跨切片传播:把当前层复制到相邻 n 层(当前段)。
   * 器官在相邻切片上形状接近,这是逐层勾画最省力的一步(等价于视频的关键帧传播)。
   */
  const propagate = (span: number) => {
    if (!editable || !active) return
    const k = crosshair[2]
    mutate({ op: 'propagate', seg: active.uid, z: k, span })
    setSaveMsg(`已把第 ${k + 1} 层传播到上下各 ${span} 层`)
  }

  /** 本平面本层的测量图形。测量是**平面 + 层**绑定的：换层就不该再画出来，
      否则会让人以为病灶在这一层。 */
  const shapesFor = (plane: MPRPlane): MeasureShape[] =>
    measurements
      .map((m, i) => ({ m, i }))
      .filter(({ m }) => m.plane === plane && m.slice === sliceOf(plane))
      .map(({ m, i }) => ({
        id: `m${i}`,
        points: m.kind === 'ruler' ? [m.a, m.b] : [m.a, m.b, m.c],
        label: m.kind === 'ruler' ? formatMm(m.lengthMm) : formatDeg(m.degrees),
        active: false,
      }))

  const pendingFor = (plane: MPRPlane) =>
    pending && pending.plane === plane ? pending.pts : undefined

  /** 跳到某一层(点清单里的条目)。 */
  const gotoSlice = (z: number) => setCrosshair(([i, j]) => [i, j, z])

  const clearSlice = () => {
    if (!editable || !active) return
    mutate({ op: 'clearSlice', seg: active.uid, z: crosshair[2] })
  }

  // ---- 段的增删改 ----

  const addSegment = () => {
    if (!editable || !tracksLoaded || !meta) return // #15 只读禁建;#16 轨迹未就绪禁建(防迟到覆盖)
    const uid = `new-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
    const seg: Segment = {
      uid,
      label: `分割 ${segments.length + 1}`,
      color: SEG_COLORS[segments.length % SEG_COLORS.length],
      visible: true,
      dirty: false,
      mask: new Uint8Array(meta.dims[0] * meta.dims[1] * meta.dims[2]),
    }
    setSegments((list) => [...list, seg])
    setActiveId(uid)
    setRenamingId(uid) // 新建即进入重命名，省一次点击
    // ⚠️ 建段必须进草稿日志。漏了它 → 崩溃后这个段在服务端不存在，它的每一笔
    // 都会被"段不存在"跳过，恢复出来是空的且不报错（真浏览器崩溃验收逮到）。
    draftRef.current?.push({ op: 'addSeg', seg: uid, label: seg.label, color: seg.color })
  }

  /**
   * 删除一段:已保存的要连后端轨迹一起删。**先问一次**——人工标注删掉就没了
   * (00 契约第一条:人工数据绝不丢失),不给二次确认是不负责任的。
   */
  const deleteSegment = async (seg: Segment) => {
    if (!editable) return
    if (!window.confirm(`删除分割「${seg.label}」？该段的所有层掩膜都会被移除，且无法撤销。`)) return
    try {
      if (seg.trackId) await trackApi.remove(taskId, seg.trackId)
      setSegments((list) => list.filter((x) => x.uid !== seg.uid))
      setActiveId((cur) => (cur === seg.uid ? segments.find((x) => x.uid !== seg.uid)?.uid ?? null : cur))
      setMaskRev((r) => r + 1)
      setSaveMsg(`已删除「${seg.label}」`)
    } catch (e: unknown) {
      setSaveMsg(`删除失败：${errText(e)}`)
    }
  }

  /** 保存**当前段**:掩膜体 → 逐 z 的 bbox-local RLE 关键帧 → upsert 轨迹。 */
  const saveSegment = async (seg: Segment) => {
    if (!editable || !meta) return
    setSaving(true)
    setSaveMsg(null)
    try {
      const keyframes = maskVolumeToKeyframes(seg.mask, meta.dims)
      if (keyframes.length === 0) {
        // 已存过的段被清空 → 删掉轨迹,而不是存一个空标注。
        if (seg.trackId) {
          await trackApi.remove(taskId, seg.trackId)
          patchSeg(seg.uid, { trackId: undefined, trackNum: undefined, version: undefined, dirty: false })
          setSaveMsg(`「${seg.label}」已清空并移除`)
        } else {
          setSaveMsg(`「${seg.label}」没有内容，未保存`)
        }
        return
      }
      const saved = await trackApi.put(taskId, {
        id: seg.trackId,
        track_id: seg.trackNum,
        label: seg.label,
        kind: 'voxel_mask',
        color: seg.color,
        version: seg.version,
        keyframes: keyframes as unknown as VideoTrack['keyframes'],
      })
      patchSeg(seg.uid, {
        uid: seg.uid,
        trackId: saved.id,
        trackNum: saved.track_id,
        version: saved.version,
        dirty: false,
      })
      setSaveMsg(`「${seg.label}」已保存：${keyframes.length} 层`)
    } catch (e: unknown) {
      // 护栏(单帧 64KB / 整 track 4MB)会以 400 + 明确文案回来，原样呈现
      // ——那条文案会告诉他改走 voxel_label 外置工作流，不能吞掉。
      setSaveMsg(`保存失败：${errText(e)}`)
    } finally {
      setSaving(false)
    }
  }

  const saveAll = async () => {
    if (!editable) return
    for (const seg of segments.filter((x) => x.dirty || !x.trackId)) await saveSegment(seg)
    // ⚠️ 清草稿只能在**全部**段都干净之后。单段保存时别的段可能还脏,那时清掉
    // 等于把其它段的兜底删了——"保存了一段,结果崩溃后另一段全没" 是最恶心的丢法。
    // 用函数式读最新 state:上面的 patchSeg 是异步的,闭包里的 segments 是旧的。
    setSegments((latest) => {
      if (latest.every((x) => !x.dirty)) {
        void draftRef.current?.clear()
        // 服务端那份也要清：留着会让换机器时误报"有未保存改动"。
        void deleteServerDraft(taskId).catch(() => {})
        setSrvSync(null)
        // #17 保存全部后,当前掩膜就是**新基线**:刷新撤销基线 + 清空 undo/redo/marks,
        // 并把 DraftWriter 的基线(新 trackId/version)对齐。否则撤销会拿"保存前的旧基线"
        // 重建、把刚保存的内容抹掉,再保存又反向覆盖刚存的数据(数据丢失)。
        baselineRef.current = new Map(latest.map((sg) => [sg.uid, sg.mask.slice()]))
        marksRef.current = []
        redoRef.current = []
        draftRef.current?.setBaseline(
          Object.fromEntries(latest.filter((sg) => sg.trackId).map((sg) => [sg.uid, { trackId: sg.trackId!, version: sg.version! }])),
        )
        setHistRev((r) => r + 1)
      }
      return latest
    })
  }

  /**
   * 导出分割为 NIfTI。
   *
   * ⚠️ 先拦"有未保存改动"。导出取的是**库里**的轨迹,没保存的段根本不在库里
   * ——直接导会拿到一份缺东西的文件,而文件本身完全正常、不报任何错。
   * 这正是本项目最贵的那类 bug,所以宁可挡一下。
   */
  const doExport = async (split: boolean, draft = false) => {
    const dirty = segments.filter((sg) => sg.dirty || !sg.trackId)
    if (dirty.length > 0) {
      setExportMsg(
        `有 ${dirty.length} 个分割尚未保存（${dirty.map((d) => d.label).join('、')}），` +
          `导出取的是已保存到服务器的内容——请先「保存全部」，否则导出的文件会缺少这些改动。`,
      )
      return
    }
    setExporting(true)
    setExportMsg(null)
    setNeedDraft(null)
    try {
      const r = await exportVolumeSegmentation(taskId, { split, draft })
      const names = Object.entries(r.labelMap)
        .map(([v, n]) => `${v}=${n}`)
        .join('，')
      setExportMsg(
        [
          split ? '已导出逐段 zip（重叠无损）' : '已导出单张多标签图',
          r.source === 'draft' ? '【草稿：未通过 QA，不可作为交付物】' : '',
          names ? `标签：${names}` : '',
          // 重叠提示必须原样呈现——它说明单张图里丢了多少体素。
          r.overlapNote,
        ]
          .filter(Boolean)
          .join(' · '),
      )
    } catch (e: unknown) {
      const status = (e as { response?: { status?: number } })?.response?.status
      const msg = errText(e)
      if (status === 409 && !draft) {
        // 后端的 409 文案已经解释了草稿选项；给一个显式按钮，而不是自动降级
        // （自动降级 = 用户不知道自己拿到的是草稿）。
        setNeedDraft({ split, reason: msg })
      } else {
        setExportMsg(`导出失败：${msg}`)
      }
    } finally {
      setExporting(false)
    }
  }

  const backToAssets = () => {
    if (asset?.dataset_id) navigate(`/datasets/${asset.dataset_id}/assets`)
    else navigate(-1)
  }

  if (metaError) {
    return (
      <div className="p-6">
        <p className="text-sm text-red-600">体数据元信息读取失败：该资产可能尚未完成派生（volume_meta 未就绪）。</p>
        <Button variant="outline" className="mt-3" onClick={backToAssets}>
          <ArrowLeft className="mr-1 h-4 w-4" />返回
        </Button>
      </div>
    )
  }

  return (
    // AppShell 的主内容区是 `flex-1 overflow-hidden`——**每个页面自己负责滚动**
    // (本仓库其它页面都是 `flex-1 overflow-auto`)。这里漏了 min-h-0 + overflow-auto,
    // 于是页面被静默裁掉:三视图超出屏幕却没有滚动条,下半张影像根本够不着。
    // min-h-0 不能省:flex 子项默认 min-height:auto,不归零的话它撑破父容器而不滚动。
    <div data-testid="volume-page" className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4">
      <div className="flex items-center gap-3">
        <Button variant="outline" size="sm" onClick={backToAssets}>
          <ArrowLeft className="mr-1 h-4 w-4" />返回
        </Button>
        <h1 className="text-lg font-semibold">{asset?.original_name ?? `任务 ${taskId}`}</h1>
        {meta && (
          <span className="text-xs text-muted-foreground">
            {meta.modality.toUpperCase()} · {meta.dims.join('×')} · 体素 {meta.spacing.map((s) => s.toFixed(2)).join('×')} mm
          </span>
        )}
      </div>

      {/* C-H3:左手系(单轴镜像)= 左右可能翻转,显眼提示而不是静默渲染。 */}
      {meta && meta.handedness < 0 && (
        <div className="flex items-center gap-2 rounded border border-amber-500 bg-amber-50 px-3 py-2 text-sm text-amber-900">
          <AlertTriangle className="h-4 w-4" />
          该体数据的方向矩阵为<strong>左手系</strong>（direction 行列式为负）。左右方向存在翻转风险，
          导出/判读前请核对方位标记。
        </div>
      )}

      {/* #8 当前 MPR 只翻转、不做轴置换/斜切重采样。非轴对齐(置换/oblique)时三视平面会被
          标错(冠状标成轴位)、画笔沿错解剖轴、测量漏 affine 交叉项——显式提示,不静默渲染。 */}
      {meta && !isCanonicalDirection(meta.direction) && (
        <div className="flex items-center gap-2 rounded border border-amber-500 bg-amber-50 px-3 py-2 text-sm text-amber-900">
          <AlertTriangle className="h-4 w-4" />
          该体数据的朝向<strong>非轴对齐</strong>（存在轴置换或斜切）。当前三视只做翻转、不重采样，
          <strong>平面标签与测量可能不准</strong>，请在 3D Slicer 等工具中核对；导出的标签体本身不受影响。
        </div>
      )}

      {/* #16 轨迹加载失败:显式报错 + 禁编辑,绝不用空列表伪装成"没有段"。 */}
      {tracksError && (
        <div data-testid="tracks-error" className="flex items-center gap-2 rounded border border-red-500 bg-red-50 px-3 py-2 text-sm text-red-800">
          <AlertTriangle className="h-4 w-4" />
          既有分割加载失败：{tracksError}。为避免与真实轨迹撞版本，编辑已禁用，请刷新重试。
        </div>
      )}

      {/* #15 只读:非可编辑态 / 被他人锁定 / 无标注权限 → 只能查看,不能画/删/AI/保存。 */}
      {task && !editable && !tracksError && (
        <div data-testid="volume-readonly-banner" className="rounded border-b bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
          只读：此任务当前不可编辑（{lockedByOther ? '正被其他人编辑' : task.state}）。可平移/缩放/调窗/量测查看。
        </div>
      )}

      {/* C0.6:上次遗留的草稿。**不自动回放**——基线可能已变,静默叠加会让标注员
          拿到一份莫名其妙的掩膜。给出时间让他判断"是不是我上次崩的那次"。 */}
      {pendingDraft && (
        <div
          data-testid="draft-banner"
          className="flex flex-wrap items-center gap-3 rounded border border-blue-500 bg-blue-50 px-3 py-2 text-sm text-blue-900"
        >
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <span>
            检测到 <strong>{pendingDraft.ops.length}</strong> 笔未保存的勾画
            （{new Date(pendingDraft.savedAt).toLocaleString()}）。是否恢复？
          </span>
          <Button size="sm" data-testid="draft-restore" onClick={restoreDraft}>
            恢复
          </Button>
          <Button size="sm" variant="outline" data-testid="draft-discard" onClick={dropDraft}>
            丢弃
          </Button>
        </div>
      )}

      {/* AI 传播结果/提示。失败(含 429 队列满)都在这里明说，不弹窗打断。 */}
      {aiMsg && (
        <div
          data-testid="ai-msg"
          className="flex items-start gap-2 rounded border border-violet-500 bg-violet-50 px-3 py-2 text-sm text-violet-900"
        >
          <span className="flex-1">{aiMsg}</span>
          <button className="shrink-0 underline" onClick={() => setAiMsg(null)}>
            知道了
          </button>
        </div>
      )}

      {/* 导出结果:重叠提示必须显眼——它说明单张图里丢了多少人工数据。 */}
      {exportMsg && (
        <div
          data-testid="export-msg"
          className="flex items-start gap-2 rounded border border-blue-500 bg-blue-50 px-3 py-2 text-sm text-blue-900"
        >
          <span className="flex-1">{exportMsg}</span>
          <button className="shrink-0 underline" onClick={() => setExportMsg(null)}>
            知道了
          </button>
        </div>
      )}

      {/* 没有 QA 快照时**不自动降级**:自动导草稿 = 用户不知道自己拿到的是草稿。 */}
      {needDraft && (
        <div
          data-testid="draft-export-prompt"
          className="flex flex-wrap items-center gap-3 rounded border border-amber-500 bg-amber-50 px-3 py-2 text-sm text-amber-900"
        >
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <span className="flex-1">{needDraft.reason}</span>
          <Button size="sm" variant="outline" onClick={() => doExport(needDraft.split, true)}>
            仍以草稿导出
          </Button>
          <Button size="sm" variant="outline" onClick={() => setNeedDraft(null)}>
            取消
          </Button>
        </div>
      )}

      {/* 草稿写不进去要**说出来**:标注员以为有兜底、实则没有,是最危险的静默失败。 */}
      {draftErr && (
        <div className="rounded border border-amber-500 bg-amber-50 px-3 py-2 text-sm text-amber-900">
          ⚠️ 本地自动草稿不可用（{draftErr}）。崩溃将丢失未保存的勾画，请勤按「保存全部」。
        </div>
      )}

      {/* 窗宽窗位:预设 + 手调。换窗只重建 LUT 重画,不回服务器。 */}
      {meta && win && (
        <div className="flex flex-wrap items-center gap-2">
          {meta.windows.map((w) => (
            <Button
              key={w.name}
              size="sm"
              variant={win.width === w.width && win.center === w.center ? 'default' : 'outline'}
              onClick={() => setWin({ width: w.width, center: w.center })}
            >
              {w.name}
            </Button>
          ))}
          <label className="ml-2 flex items-center gap-1 text-xs">
            窗宽
            <input
              type="number"
              className="w-24 rounded border px-1 py-0.5"
              value={Math.round(win.width)}
              onChange={(e) => setWin({ ...win, width: Number(e.target.value) })}
            />
          </label>
          <label className="flex items-center gap-1 text-xs">
            窗位
            <input
              type="number"
              className="w-24 rounded border px-1 py-0.5"
              value={Math.round(win.center)}
              onChange={(e) => setWin({ ...win, center: Number(e.target.value) })}
            />
          </label>
          <span className="text-xs text-muted-foreground">滚轮 = 切层，Ctrl+滚轮 = 缩放</span>
        </div>
      )}

      {/* 标注工具条:作用于**当前选中段** */}
      {volume && meta && (
        <div className="flex flex-wrap items-center gap-2 rounded border bg-muted/30 px-2 py-1.5">
          <Button
            size="sm"
            variant={paintMode ? 'default' : 'outline'}
            disabled={!editable || !active}
            onClick={() => {
              // 测量与涂抹**互斥**：同一次点击不可能既是落笔又是量点，
              // 让两者共存只会让人在想量尺寸时把病灶涂花。
              setMeasureMode('off')
              setPending(null)
              setAiMode(false)
              setPaintMode((v) => !v)
            }}
            title={active ? undefined : '先在右侧新建或选中一个分割'}
          >
            {paintMode ? '涂抹中(Alt/右键=擦除)' : '开始涂抹'}
          </Button>
          <span className="mx-1 h-4 w-px bg-border" />
          {(['ruler', 'angle'] as const).map((mode) => (
            <Button
              key={mode}
              size="sm"
              variant={measureMode === mode ? 'default' : 'outline'}
              data-testid={`measure-${mode}`}
              onClick={() => {
                setPaintMode(false)
                setPending(null)
                setAiMode(false)
                setMeasureMode((cur) => (cur === mode ? 'off' : mode))
              }}
              title={mode === 'ruler' ? '两点测长（RECIST 长径）' : '三点测角，顶点是第二个点'}
            >
              {mode === 'ruler' ? '📏 长度' : '📐 角度'}
              {measureMode === mode ? '（进行中）' : ''}
            </Button>
          ))}
          {active && (
            <span className="flex items-center gap-1 text-xs">
              画笔目标：
              <span className="inline-block h-3 w-3 rounded-sm" style={{ backgroundColor: active.color }} />
              <span className="font-medium">{active.label}</span>
            </span>
          )}
          <label className="flex items-center gap-1 text-xs">
            笔刷
            <input type="range" min={1} max={30} value={brush} onChange={(e) => setBrush(Number(e.target.value))} />
            <span className="w-6">{brush}</span>
          </label>
          <Button size="sm" variant="outline" disabled={!editable || !active} onClick={() => propagate(1)}>
            传播 ±1 层
          </Button>
          <Button size="sm" variant="outline" disabled={!editable || !active} onClick={() => propagate(3)}>
            传播 ±3 层
          </Button>
          <Button size="sm" variant="outline" disabled={!editable || !active} onClick={clearSlice}>
            清空本层
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="undo"
            disabled={marksRef.current.length === 0}
            onClick={undo}
            title="撤销（Ctrl+Z）"
          >
            ↶ 撤销
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="redo"
            disabled={redoRef.current.length === 0}
            onClick={redo}
            title="重做（Ctrl+Shift+Z）"
          >
            ↷ 重做
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="export-nifti"
            disabled={exporting || segments.length === 0}
            onClick={() => doExport(false)}
            title="导出单张多标签 NIfTI（可直接拖进 3D Slicer）"
          >
            {exporting ? '导出中…' : '导出 NIfTI'}
          </Button>
          <Button
            size="sm"
            variant="outline"
            data-testid="export-split"
            disabled={exporting || segments.length === 0}
            onClick={() => doExport(true)}
            title="逐段二值文件 + labels.json，重叠区域无损"
          >
            导出逐段 zip
          </Button>
          <span className="mx-1 h-4 w-px bg-border" />
          <Button
            size="sm"
            variant={aiMode ? 'default' : 'outline'}
            data-testid="ai-propagate"
            disabled={!editable || aiBusy}
            onClick={() => {
              setPaintMode(false)
              setMeasureMode('off')
              setPending(null)
              setAiMode((v) => !v)
              setAiMsg(null)
            }}
            title="点体内一点，SAM2 跨切片自动分割（可再用画笔修正）"
          >
            {aiBusy ? 'AI 分割中…' : aiMode ? '✨ 点选目标（进行中）' : '✨ AI 点选'}
          </Button>
          <Button size="sm" onClick={saveAll} disabled={!editable || saving || segments.length === 0}>
            {saving ? '保存中…' : '保存全部'}
          </Button>
          {saveMsg && <span className="text-xs text-muted-foreground">{saveMsg}</span>}
        </div>
      )}

      {loadError && <p className="text-sm text-red-600">体数据加载失败：{loadError}</p>}

      {!volume && !loadError && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          正在加载体数据 {progress.done}/{progress.total} 层…
        </div>
      )}

      {volume && meta && lut && (
        <div className="flex items-start gap-4">
          {/* 三视按 2 列网格排(放射科工作站的常见布局),不用 flex-wrap
              ——后者会让矢状面被挤到下一行、右侧留一大片空白。 */}
          <div className="grid min-w-0 flex-1 grid-cols-2 gap-3">
          {MPR_PLANES.map((plane) => {
            const [cu, cv] = crossOf(plane)
            return (
              <MPRView
                key={plane}
                volume={volume}
                dims={meta.dims}
                spacing={meta.spacing}
                plane={plane}
                slice={Math.min(sliceOf(plane), planeSliceCount(meta.dims, plane) - 1)}
                lut={lut}
                crossU={cu}
                crossV={cv}
                zoom={zoom}
                onSliceChange={(next) => setSlice(plane, next)}
                onZoomChange={setZoom}
                onPick={(u, v) =>
                  aiMode
                    ? void aiPropagate(plane, u, v)
                    : measureMode === 'off'
                      ? pickIn(plane, u, v)
                      : measureClick(plane, u, v)
                }
                overlayShapes={shapesFor(plane)}
                pendingPoints={pendingFor(plane)}
                direction={meta.direction}
                overlaysRef={overlaysRef}
                overlayRev={overlayRev}
                // 只在 axial 涂抹:关键帧按 z 存,轴位是它的自然平面;
                // 冠状/矢状只读展示(在那两面上画会需要跨 z 写回,先不开)。
                paintMode={paintMode && plane === 'axial'}
                onPaint={paintAt}
                onPaintEnd={endStroke}
              />
            )
          })}
          </div>

          {/* 标注/定位面板:当前坐标 + 已标注层清单(可跳转) */}
          <aside className="w-56 shrink-0 rounded border p-2 text-xs">
            {/* 服务端草稿的同步状态（C0.6b）。推送失败不打断标注——网断了不该让人
                没法画——但**不能悄悄失败**：把"上次同步"摆出来，久未更新自己看得见。
                没有这一行，"失败不打断"就等于"失败无人知晓"。 */}
            <div
              data-testid="srv-sync"
              className="mb-2 rounded bg-muted/50 px-1.5 py-1 text-[11px] text-muted-foreground"
            >
              云端草稿：
              {srvSync ? (
                <span className="text-foreground">
                  {new Date(srvSync.at).toLocaleTimeString()} 已同步 {srvSync.ops} 笔
                </span>
              ) : (
                <span>尚未同步（每 5 分钟一次，用于换机器续作）</span>
              )}
            </div>
            <div className="mb-2 font-medium">当前位置</div>
            <div className="space-y-0.5 text-muted-foreground">
              <div>轴位 Axial：<span className="font-mono text-foreground">{crosshair[2] + 1} / {meta.dims[2]}</span></div>
              <div>冠状 Coronal：<span className="font-mono text-foreground">{crosshair[1] + 1} / {meta.dims[1]}</span></div>
              <div>矢状 Sagittal：<span className="font-mono text-foreground">{crosshair[0] + 1} / {meta.dims[0]}</span></div>
              <div className="pt-1">体素 (i,j,k)：<span className="font-mono text-foreground">{crosshair.join(', ')}</span></div>
              {/* HU/强度探针:直接读当前体素的真值(放射科的"先量再勾") */}
              <div>
                强度：
                <span className="font-mono text-foreground">
                  {(() => {
                    const [i, j, k] = crosshair
                    const idx = i + j * meta.dims[0] + k * meta.dims[0] * meta.dims[1]
                    const v = volume[idx]
                    if (v === undefined) return '—'
                    const real = v * meta.slice_scl_slope + meta.slice_scl_inter
                    return `${real.toFixed(1)}${meta.modality === 'ct' ? ' HU' : ''}`
                  })()}
                </span>
              </div>
            </div>

            {/* 分割列表:新建 / 选中 / 重命名 / 改色 / 显隐 / 删除。
                参照 3D Slicer 的 segment 表(颜色 + 可见性 + 状态)与 OHIF 的每段统计。 */}
            {/* 测量清单。RECIST 的数会进报告，所以要能回看、能跳回原位、能删。 */}
            <div className="mb-1 mt-3 flex items-center justify-between font-medium">
              <span>测量（{measurements.length}）</span>
              {measurements.length > 0 && (
                <button
                  className="text-muted-foreground underline-offset-2 hover:underline"
                  onClick={() => setMeasurements([])}
                >
                  清空
                </button>
              )}
            </div>
            {measurements.length === 0 ? (
              <p className="text-muted-foreground">
                {measureMode === 'off'
                  ? '点工具条的「长度 / 角度」开始测量。'
                  : measureMode === 'ruler'
                    ? '在影像上点两下量长度。'
                    : '点三下量角，顶点是第二个点。'}
              </p>
            ) : (
              <ul data-testid="measure-list" className="space-y-0.5">
                {measurements.map((m, i) => {
                  const small = m.kind === 'ruler' && m.lengthMm < RECIST_MIN_TARGET_MM
                  return (
                    <li key={i} className="flex items-center gap-1">
                      <button
                        className="min-w-0 flex-1 truncate rounded px-1 py-0.5 text-left hover:bg-accent"
                        title="跳到该测量所在层"
                        onClick={() => {
                          if (m.plane === 'axial') setCrosshair(([i2, j2]) => [i2, j2, m.slice])
                          else if (m.plane === 'coronal') setCrosshair(([i2, , k2]) => [i2, m.slice, k2])
                          else setCrosshair(([, j2, k2]) => [m.slice, j2, k2])
                        }}
                      >
                        {describeMeasurement(m)}
                      </button>
                      {/* RECIST 1.1：常规层厚下长径 <10mm 不作为靶病灶。只**提示**不拦截
                          ——阈值随层厚与病灶类型而变（淋巴结看短径 ≥15mm），
                          写死成硬规则会在别的场景拦错东西。判断是医生的事。 */}
                      {small && (
                        <span className="shrink-0 text-amber-600" title={`<${RECIST_MIN_TARGET_MM}mm，按 RECIST 1.1 通常不作为靶病灶`}>
                          ⚠
                        </span>
                      )}
                      <button
                        className="shrink-0 text-muted-foreground hover:text-red-600"
                        title="删除该测量"
                        onClick={() => setMeasurements((list) => list.filter((_, j) => j !== i))}
                      >
                        <Trash2 className="h-3 w-3" />
                      </button>
                    </li>
                  )
                })}
              </ul>
            )}

            <div className="mb-1 mt-3 flex items-center justify-between font-medium">
              <span>分割（{segments.length}）</span>
              <Button size="sm" variant="outline" className="h-6 px-2 text-xs" onClick={addSegment} disabled={!editable || !tracksLoaded}>
                + 新建
              </Button>
            </div>

            {segments.length === 0 ? (
              <p className="text-muted-foreground">还没有分割。点「+ 新建」开始。</p>
            ) : (
              <ul className="space-y-1" data-testid="segment-list">
                {segments.map((sg) => {
                  const isActive = sg.uid === activeId
                  return (
                    <li
                      key={sg.uid}
                      className={`rounded border px-1.5 py-1 ${
                        isActive ? 'border-primary bg-accent' : 'border-transparent bg-muted/40'
                      }`}
                    >
                      <div className="flex items-center gap-1">
                        <button
                          className="shrink-0 text-muted-foreground hover:text-foreground"
                          title={sg.visible ? '隐藏' : '显示'}
                          onClick={() => patchSeg(sg.uid, { visible: !sg.visible })}
                        >
                          {sg.visible ? <Eye className="h-3.5 w-3.5" /> : <EyeOff className="h-3.5 w-3.5" />}
                        </button>
                        <input
                          type="color"
                          className="h-4 w-4 shrink-0 cursor-pointer rounded-sm border-0 bg-transparent p-0"
                          value={sg.color}
                          title="修改颜色"
                          onChange={(e) => patchSeg(sg.uid, { color: e.target.value })}
                        />
                        {renamingId === sg.uid ? (
                          <input
                            autoFocus
                            className="min-w-0 flex-1 rounded border px-1 py-0.5 text-xs"
                            defaultValue={sg.label}
                            onBlur={(e) => {
                              patchSeg(sg.uid, { label: e.target.value.trim() || sg.label, dirty: true })
                              setRenamingId(null)
                            }}
                            onKeyDown={(e) => {
                              if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
                              if (e.key === 'Escape') setRenamingId(null)
                            }}
                          />
                        ) : (
                          <button
                            className="min-w-0 flex-1 truncate text-left"
                            title="点击选中，双击重命名"
                            onClick={() => setActiveId(sg.uid)}
                            onDoubleClick={() => setRenamingId(sg.uid)}
                          >
                            {sg.label}
                          </button>
                        )}
                        {sg.dirty && (
                          <span className="shrink-0 text-amber-600" title="有未保存改动">
                            ●
                          </span>
                        )}
                        <button
                          className="shrink-0 text-muted-foreground hover:text-foreground"
                          title="重命名"
                          onClick={() => setRenamingId(sg.uid)}
                        >
                          <Pencil className="h-3 w-3" />
                        </button>
                        <button
                          className="shrink-0 text-muted-foreground hover:text-red-600"
                          title="删除该分割"
                          onClick={() => deleteSegment(sg)}
                        >
                          <Trash2 className="h-3 w-3" />
                        </button>
                      </div>
                      {isActive && stats && (
                        <div className="mt-0.5 space-y-0.5 pl-5 text-muted-foreground">
                          {stats.voxels > 0 ? (
                            <>
                              <div>
                                体积：
                                <span className="font-mono text-foreground">{formatVolume(stats.volumeMm3)}</span>
                                <span className="ml-1">（{stats.sliceCount} 层）</span>
                              </div>
                              {stats.intensity && (
                                <div>
                                  强度{meta.modality === 'ct' ? '(HU)' : ''}：
                                  <span className="font-mono text-foreground">
                                    均 {stats.intensity.mean.toFixed(1)}
                                  </span>
                                  <span className="ml-1">
                                    [{stats.intensity.min.toFixed(0)}, {stats.intensity.max.toFixed(0)}]
                                  </span>
                                </div>
                              )}
                              {stats.centroid && (
                                <button
                                  className="underline-offset-2 hover:underline"
                                  onClick={() => setCrosshair(stats.centroid!)}
                                >
                                  质心 ({stats.centroid.join(', ')}) — 跳过去
                                </button>
                              )}
                            </>
                          ) : (
                            <div>尚无内容</div>
                          )}
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-6 px-2 text-xs"
                            disabled={!editable || saving}
                            onClick={() => saveSegment(sg)}
                          >
                            保存本段
                          </Button>
                        </div>
                      )}
                    </li>
                  )
                })}
              </ul>
            )}

            <div className="mb-1 mt-3 flex items-center justify-between font-medium">
              <span>已标注层{active ? `（${active.label}）` : ''}</span>
              <span className="text-muted-foreground">{annotated.length} 层</span>
            </div>
            {annotated.length === 0 ? (
              <p className="text-muted-foreground">{active ? '该段尚无标注。' : '未选中分割。'}</p>
            ) : (
              <ul className="max-h-48 space-y-0.5 overflow-auto">
                {annotated.map((sl) => (
                  <li key={sl.z}>
                    <button
                      className={`flex w-full items-center justify-between rounded px-1.5 py-0.5 text-left hover:bg-accent ${
                        sl.z === crosshair[2] ? 'bg-accent font-medium' : ''
                      }`}
                      onClick={() => gotoSlice(sl.z)}
                    >
                      <span>第 {sl.z + 1} 层</span>
                      <span className="text-muted-foreground">
                        {sliceAreaMm2(sl.voxels, meta.spacing).toFixed(0)} mm²
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </aside>
        </div>
      )}
    </div>
  )
}
