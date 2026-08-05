import React, { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { workbenchBackTo } from '@/lib/navBack'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import WaveSurfer from 'wavesurfer.js'
import RegionsPlugin, { type Region } from 'wavesurfer.js/dist/plugins/regions.esm.js'
import {
  ArrowLeft, ChevronLeft, ChevronRight, Play, Pause,
  Trash2, Save, Send, Lock, Loader2, ZoomIn, Check, Sparkles, X,
} from 'lucide-react'
import { taskApi, capabilityApi, TASK_STATE_LABELS, TASK_STATE_COLOR, type Shape } from '@/api/imageTask'
import { assetApi } from '@/api/asset'
import { datasetApi } from '@/api/dataset'
import { audioApi, normalizePeaks } from '@/api/audioTask'
import { useEditLock } from '@/hooks/useEditLock'
import { useResizablePanel, SplitHandle } from '@/components/common/ResizablePanel'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useAuthStore } from '@/stores/auth'
import { canAnnotate as roleCanAnnotate, canReview as roleCanReview } from '@/lib/roles'

// 音频能力的展示名（可选）。真正决定「AI 预标注」面板出现哪些能力的是后端
// 已注册适配器（`asr.transcribe` 或任意 `audio.*`）——在「能力配置」注册本地/
// 云端音频模型后自动出现，未登记名则回退显示 capability id。
const AUDIO_CAPS: Record<string, string> = {
  'asr.transcribe': '语音转写 + 说话人 (FunASR)',
  'audio.transcribe': '整段转写 (Qwen2.5-Omni)',
  'audio.classifier': '音频分类',
  'audio.emotion': '情绪识别',
}
const isAudioCap = (c: string) => c === 'asr.transcribe' || c.startsWith('audio.')
const audioCapLabel = (c: string) => AUDIO_CAPS[c] ?? c

const EDITABLE_STATES = new Set(['HUMAN_PENDING', 'HUMAN_IN_PROGRESS', 'QA_REJECTED'])
const REGION_KIND = 'audio_region'
const REGION_FILL = 'rgba(13,148,136,0.16)'
const REGION_FILL_SEL = 'rgba(13,148,136,0.40)'

// 情绪标签展示（emotion2vec / SenseVoice）。未登记的原样显示。
const EMOTION_META: Record<string, { label: string; color: string }> = {
  angry: { label: '愤怒', color: '#ef4444' },
  happy: { label: '高兴', color: '#22c55e' },
  sad: { label: '悲伤', color: '#3b82f6' },
  neutral: { label: '中性', color: '#94a3b8' },
  fearful: { label: '害怕', color: '#a855f7' },
  fear: { label: '害怕', color: '#a855f7' },
  disgusted: { label: '厌恶', color: '#84cc16' },
  disgust: { label: '厌恶', color: '#84cc16' },
  surprised: { label: '惊讶', color: '#f59e0b' },
  surprise: { label: '惊讶', color: '#f59e0b' },
}
// 标注员可选的情绪(与模型输出同格式 "中文/english"，颜色/导出一致)。
const EMOTION_OPTIONS = ['开心/happy', '中立/neutral', '生气/angry', '难过/sad', '吃惊/surprised', '厌恶/disgusted', '害怕/fearful']
function EmotionTag({ emotion }: { emotion: string }) {
  // emotion2vec_plus 输出形如 "开心/happy"：取英文键匹配颜色，中文部分展示；
  // 纯英文标签则用内置中文名。
  const parts = emotion.split('/')
  const en = (parts.length > 1 ? parts[1] : parts[0]).toLowerCase().trim()
  const m = EMOTION_META[en]
  const label = parts.length > 1 ? parts[0].trim() : (m?.label ?? emotion)
  const color = m?.color ?? 'var(--muted-foreground)'
  return (
    <span className="ml-1.5 inline-flex items-center rounded px-1 align-middle text-[9px]"
      style={{ color, background: `color-mix(in srgb, ${color} 14%, transparent)` }} title={`情绪：${label}`}>
      {label}
    </span>
  )
}

const secToMs = (s: number) => Math.round(s * 1000)
const fmtSec = (sec: number) => {
  const m = Math.floor(sec / 60)
  const s = sec - m * 60
  return `${m}:${s.toFixed(2).padStart(5, '0')}`
}

export function AudioAnnotationPage() {
  const { id } = useParams<{ id: string }>()
  const taskId = Number(id)
  const navigate = useNavigate()
  const location = useLocation()
  const qc = useQueryClient()
  const role = useAuthStore((s) => s.user?.role ?? '')
  const canAnnotate = roleCanAnnotate(role)
  const canReview = roleCanReview(role)

  // --- data ---
  const { data: task } = useQuery({ queryKey: ['task', taskId], queryFn: () => taskApi.get(taskId) })
  const { data: asset } = useQuery({
    queryKey: ['asset-detail', task?.asset_id],
    queryFn: () => assetApi.detail(task!.asset_id),
    enabled: !!task?.asset_id,
  })
  const { data: humanAnn } = useQuery({
    queryKey: ['human-annotation', taskId],
    queryFn: () => taskApi.getHumanAnnotation(taskId),
  })
  const { data: ontology } = useQuery({
    queryKey: ['ontology', task?.dataset_id],
    queryFn: () => datasetApi.getOntology(task!.dataset_id),
    enabled: !!task?.dataset_id,
    staleTime: 5 * 60 * 1000,
  })
  const { data: adjacent } = useQuery({
    queryKey: ['adjacent', taskId],
    queryFn: () => taskApi.getAdjacent(taskId, true),
  })
  // 顶栏「返回」与「提交后没有下一条」共用同一个去处：来处 → 上一级 → /my-tasks
  const backTo = workbenchBackTo(location.state, task?.dataset_id)
  const { data: waveform } = useQuery({
    queryKey: ['waveform', task?.asset_id],
    queryFn: () => audioApi.getWaveform(task!.asset_id).catch(() => null),
    enabled: !!task?.asset_id && asset?.preprocess_status === 'ready',
  })
  const { data: aiResults } = useQuery({
    queryKey: ['ai-results', taskId],
    queryFn: () => taskApi.getAIResults(taskId),
  })
  const { data: caps = [] } = useQuery({
    queryKey: ['capabilities'],
    queryFn: () => capabilityApi.list(),
    staleTime: 5 * 60 * 1000,
  })
  // 能力→模型清单（统一来自能力配置/env 适配器）；驱动「自选模型」下拉。
  const { data: capModels = [] } = useQuery({
    queryKey: ['cap-models'],
    queryFn: () => capabilityApi.models(),
    staleTime: 5 * 60 * 1000,
  })
  const [modelSel, setModelSel] = useState<Record<string, string>>({})

  const { width: panelWidth, startDrag, containerRef: splitRef } = useResizablePanel('audio.panelWidth', { initial: 320 })

  const editableState = !!task && EDITABLE_STATES.has(task.state)
  const { lock, readOnly: lockedByOther } = useEditLock(taskId, canAnnotate && editableState)
  const editable = canAnnotate && editableState && !lockedByOther

  // --- region state (source of truth = React; wavesurfer mirrors visually) ---
  const [regions, setRegions] = useState<Shape[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [currentSegId, setCurrentSegId] = useState<string | null>(null) // 正在播放的段
  const [editingId, setEditingId] = useState<string | null>(null)       // 正在内联编辑转写的段
  const [dirty, setDirty] = useState(false)
  const [savedFlash, setSavedFlash] = useState(false)
  const [saveErr, setSaveErr] = useState('')
  const [saving, setSaving] = useState(false)
  const [playing, setPlaying] = useState(false)
  // 右侧"自由编辑"草稿（点选段落后可在右栏改 说话人 / 转写），失焦提交。
  const [speakerDraft, setSpeakerDraft] = useState('')
  const [textDraft, setTextDraft] = useState('')

  const containerRef = useRef<HTMLDivElement>(null)
  const wsRef = useRef<WaveSurfer | null>(null)
  const regionsPluginRef = useRef<ReturnType<typeof RegionsPlugin.create> | null>(null)
  const regionObjs = useRef<Map<string, Region>>(new Map())
  const rowRefs = useRef<Map<string, HTMLTableRowElement>>(new Map())
  const loadingRef = useRef(false)
  const playUntilRef = useRef<number | null>(null) // 逐段试听：到此秒数暂停
  const editableRef = useRef(editable)
  editableRef.current = editable

  const ontologyLabels = ontology?.labels ?? []
  const speakerPresets = ontology?.speakers?.preset ?? []

  const sorted = useMemo(
    () => [...regions].sort((a, b) => (a.time_start_ms ?? 0) - (b.time_start_ms ?? 0)),
    [regions],
  )
  const sortedRef = useRef(sorted)
  sortedRef.current = sorted

  const speakerOptions = useMemo(() => {
    const set = new Set<string>(speakerPresets)
    regions.forEach((s) => { const sp = s.attrs?.speaker as string; if (sp) set.add(sp) })
    return Array.from(set)
  }, [regions, speakerPresets])

  // Initial regions: human annotation if present, else seed from ASR segments.
  const initialRegions = useMemo<Shape[]>(() => {
    const human = (humanAnn?.shapes ?? []).filter((s) => s.kind === REGION_KIND)
    if (human.length) return human
    const segs = aiResults?.asr?.segments ?? []
    return segs.map((seg, i) => ({
      id: `asr-${i}`, kind: REGION_KIND, points: [],
      time_start_ms: seg.start_ms, time_end_ms: seg.end_ms,
      attrs: { tier: 'speaker', speaker: seg.speaker, text: seg.text, source: 'asr', confidence: seg.confidence, emotion: seg.emotion },
      color: REGION_FILL,
    }))
  }, [humanAnn, aiResults])

  const initRef = useRef<Shape[]>(initialRegions)
  initRef.current = initialRegions

  useEffect(() => {
    if (dirty) return
    setRegions(initialRegions)
    setEditingId(null)
  }, [initialRegions, dirty])

  // --- wavesurfer lifecycle ---
  useEffect(() => {
    if (!asset || !containerRef.current) return
    const ws = WaveSurfer.create({
      container: containerRef.current,
      url: audioApi.bodyUrl(asset.id),
      ...(waveform ? { peaks: [normalizePeaks(waveform)], duration: (asset.duration_ms ?? 0) / 1000 || undefined } : {}),
      waveColor: '#d4d4d8', progressColor: '#71717a', cursorColor: '#0d9488',
      height: 110, minPxPerSec: 60, normalize: true, autoScroll: true,
    })
    const regionsPlugin = ws.registerPlugin(RegionsPlugin.create())
    wsRef.current = ws
    regionsPluginRef.current = regionsPlugin

    ws.on('play', () => setPlaying(true))
    ws.on('pause', () => setPlaying(false))
    ws.on('finish', () => setPlaying(false))

    // 播放时跟踪「当前段」：用于行高亮 + 自动滚动 + 逐段试听暂停。
    ws.on('timeupdate', (t: number) => {
      if (playUntilRef.current != null && t >= playUntilRef.current) {
        ws.pause(); playUntilRef.current = null
      }
      const ms = t * 1000
      const seg = sortedRef.current.find((s) => (s.time_start_ms ?? 0) <= ms && ms < (s.time_end_ms ?? 0))
      setCurrentSegId((cur) => (cur === (seg?.id ?? null) ? cur : seg?.id ?? null))
    })

    ws.on('ready', () => {
      loadingRef.current = true
      regionObjs.current.forEach((r) => r.remove())
      regionObjs.current.clear()
      initRef.current.forEach((s) => {
        const r = regionsPlugin.addRegion({
          id: s.id,
          start: (s.time_start_ms ?? 0) / 1000,
          end: (s.time_end_ms ?? 0) / 1000,
          color: REGION_FILL,
          drag: editableRef.current,
          resize: editableRef.current,
          content: regionLabelText(s),
        })
        regionObjs.current.set(s.id, r)
      })
      loadingRef.current = false
      if (editableRef.current) regionsPlugin.enableDragSelection({ color: REGION_FILL })
    })

    regionsPlugin.on('region-created', (region: Region) => {
      if (loadingRef.current) return
      const shape: Shape = {
        id: region.id, kind: REGION_KIND, points: [],
        time_start_ms: secToMs(region.start), time_end_ms: secToMs(region.end),
        attrs: { tier: 'speaker', source: 'human' }, color: REGION_FILL,
      }
      regionObjs.current.set(region.id, region)
      setRegions((prev) => [...prev, shape])
      setSelectedId(region.id)
      setDirty(true)
    })

    regionsPlugin.on('region-updated', (region: Region) => {
      setRegions((prev) => prev.map((s) =>
        s.id === region.id ? { ...s, time_start_ms: secToMs(region.start), time_end_ms: secToMs(region.end) } : s))
      setDirty(true)
    })

    regionsPlugin.on('region-clicked', (region: Region, e: MouseEvent) => {
      e.stopPropagation()
      setSelectedId(region.id)
      playSegmentById(region.id)
    })

    return () => {
      ws.destroy(); wsRef.current = null; regionsPluginRef.current = null; regionObjs.current.clear()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [asset?.id, waveform, aiResults])

  // 选中区间高亮（波形）
  useEffect(() => {
    regionObjs.current.forEach((r, rid) => {
      r.setOptions({ color: rid === selectedId ? REGION_FILL_SEL : REGION_FILL })
    })
  }, [selectedId, regions])

  // 播放时把「当前段」行滚动到视野内
  useEffect(() => {
    if (!playing || !currentSegId) return
    rowRefs.current.get(currentSegId)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [currentSegId, playing])

  const selected = useMemo(() => regions.find((s) => s.id === selectedId) ?? null, [regions, selectedId])
  // 选中变化（或该段被别处改动）时，同步右栏草稿。
  useEffect(() => {
    setSpeakerDraft((selected?.attrs?.speaker as string) ?? '')
    setTextDraft((selected?.attrs?.text as string) ?? '')
  }, [selectedId, selected?.attrs?.speaker, selected?.attrs?.text])

  // --- region mutations ---
  function updateRegion(id: string, patch: { text?: string; speaker?: string; label?: string; emotion?: string }) {
    setRegions((prev) => prev.map((s) => {
      if (s.id !== id) return s
      return {
        ...s,
        label: patch.label !== undefined ? (patch.label || undefined) : s.label,
        attrs: {
          ...s.attrs,
          ...(patch.text !== undefined ? { text: patch.text || undefined } : {}),
          ...(patch.speaker !== undefined ? { speaker: patch.speaker || undefined } : {}),
          ...(patch.emotion !== undefined ? { emotion: patch.emotion || undefined } : {}),
          source: 'human', // 编辑 AI 段 = 采纳
        },
      }
    }))
    if (patch.speaker !== undefined || patch.label !== undefined) {
      regionObjs.current.get(id)?.setOptions({ content: patch.speaker || patch.label || '段' })
    }
    setDirty(true)
  }

  function seekTo(ms: number) {
    wsRef.current?.setTime(Math.max(0, ms / 1000))
  }

  // 逐段试听：从段首播放，到段尾自动暂停。
  function playSegmentById(id: string) {
    const s = sortedRef.current.find((r) => r.id === id)
    if (!s || !wsRef.current) return
    playUntilRef.current = (s.time_end_ms ?? 0) / 1000
    wsRef.current.setTime((s.time_start_ms ?? 0) / 1000)
    wsRef.current.play()
  }

  function selectSeg(id: string, opts?: { play?: boolean }) {
    setSelectedId(id)
    const s = sortedRef.current.find((r) => r.id === id)
    if (s) {
      if (opts?.play) playSegmentById(id)
      else seekTo(s.time_start_ms ?? 0)
    }
  }

  function startEdit(id: string) {
    setSelectedId(id)
    setEditingId(id)
    const s = sortedRef.current.find((r) => r.id === id)
    if (s) seekTo(s.time_start_ms ?? 0)
  }

  function commitText(id: string, value: string) {
    const s = sortedRef.current.find((r) => r.id === id)
    if (s && (s.attrs?.text ?? '') !== value) updateRegion(id, { text: value })
  }

  function commitAndAdvance(id: string, value: string) {
    commitText(id, value)
    const idx = sortedRef.current.findIndex((s) => s.id === id)
    const next = sortedRef.current[idx + 1]
    setEditingId(next ? next.id : null)
    if (next) { setSelectedId(next.id); seekTo(next.time_start_ms ?? 0) }
  }

  function selectAdjacent(delta: number) {
    const idx = sortedRef.current.findIndex((s) => s.id === selectedId)
    const base = idx === -1 ? (delta > 0 ? -1 : sortedRef.current.length) : idx
    const next = sortedRef.current[base + delta]
    if (next) selectSeg(next.id)
  }

  function deleteSeg(id: string) {
    if (!editable) return
    regionObjs.current.get(id)?.remove()
    regionObjs.current.delete(id)
    setRegions((prev) => prev.filter((s) => s.id !== id))
    if (selectedId === id) setSelectedId(null)
    setDirty(true)
  }

  async function save() {
    setSaving(true); setSaveErr('')
    try {
      const others = (humanAnn?.shapes ?? []).filter((s) => s.kind !== REGION_KIND)
      await taskApi.putHumanAnnotation(taskId, { shapes: [...others, ...regions] })
      await qc.invalidateQueries({ queryKey: ['human-annotation', taskId] })
      setDirty(false); setSavedFlash(true); setTimeout(() => setSavedFlash(false), 1800)
    } catch (e: any) {
      setSaveErr(e?.response?.data?.error || e?.message || '保存失败')
    } finally { setSaving(false) }
  }

  async function submit() {
    if (dirty) await save()
    await taskApi.submit(taskId)
    await qc.invalidateQueries({ queryKey: ['task', taskId] })
  }

  const [invokeMsg, setInvokeMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null)
  const invokeMut = useMutation({
    mutationFn: ({ capability, model }: { capability: string; model?: string }) => taskApi.invoke(taskId, capability, model),
    onSuccess: async (_res, vars) => {
      setInvokeMsg({ kind: 'ok', text: `${AUDIO_CAPS[vars.capability] ?? vars.capability} 完成` })
      await qc.invalidateQueries({ queryKey: ['ai-results', taskId] })
      await qc.invalidateQueries({ queryKey: ['human-annotation', taskId] })
    },
    onError: (e: any) => setInvokeMsg({ kind: 'err', text: e?.response?.data?.error || e?.message || 'AI 调用失败' }),
  })
  const audioCaps = caps.filter(isAudioCap)

  // --- review (A3.1): reviewer 通过/驳回，复用图片侧 qaPass/qaReject 范式 ---
  const reviewing = canReview && task?.state === 'QA_PENDING'
  const [reviewNote, setReviewNote] = useState('')
  const qaMut = useMutation({
    mutationFn: (pass: boolean) => (pass ? taskApi.qaPass(taskId, reviewNote) : taskApi.qaReject(taskId, reviewNote)),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['task', taskId] })
      if (adjacent?.next_task_id) navigate(`/audio-tasks/${adjacent.next_task_id}`, { state: location.state })
      else navigate(backTo)
    },
  })

  // 翻到相邻任务时把「来处」一起带过去，否则翻两下之后「返回」就找不到北了。
  function goto(tid?: number | null) { if (tid) navigate(`/audio-tasks/${tid}`, { state: location.state }) }

  // 全局键盘（编辑转写时由 textarea 接管）：空格 播放/暂停 · ↑↓ 上/下一段 · Del 删除
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      if (e.code === 'Space') { e.preventDefault(); wsRef.current?.playPause() }
      else if (e.key === 'ArrowDown') { e.preventDefault(); selectAdjacent(1) }
      else if (e.key === 'ArrowUp') { e.preventDefault(); selectAdjacent(-1) }
      else if (e.key === 'Delete' && selectedId) { deleteSeg(selectedId) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  return (
    <div className="flex h-screen flex-col" style={{ background: 'var(--background)' }}>
      {/* top bar */}
      <div className="flex h-13 shrink-0 items-center gap-3 border-b px-4 py-2.5" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
        <Button variant="ghost" size="sm" onClick={() => navigate(backTo)}><ArrowLeft className="mr-1 h-4 w-4" />返回</Button>
        <span className="text-sm font-medium">{asset?.original_name ?? '音频任务'}</span>
        {task && <Badge variant="outline" className={`text-[10px] ${TASK_STATE_COLOR[task.state] ?? ''}`}>{TASK_STATE_LABELS[task.state] ?? task.state}</Badge>}
        {asset?.duration_ms != null && <span className="font-mono text-xs" style={{ color: 'var(--muted-foreground)' }}>{fmtSec(asset.duration_ms / 1000)}</span>}
        <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>{sorted.length} 段</span>
        <div className="ml-auto flex items-center gap-2">
          {lockedByOther && (
            <span className="flex items-center gap-1 rounded-md border px-2 py-1 text-xs" style={{ borderColor: 'var(--border)', background: 'var(--muted)', color: 'var(--muted-foreground)' }}>
              <Lock className="h-3 w-3" />只读：被用户 {lock?.owner} 锁定
            </span>
          )}
          <Button variant="ghost" size="sm" disabled={!adjacent?.prev_task_id} onClick={() => goto(adjacent?.prev_task_id)}><ChevronLeft className="h-4 w-4" />上一个</Button>
          <Button variant="ghost" size="sm" disabled={!adjacent?.next_task_id} onClick={() => goto(adjacent?.next_task_id)}>下一个<ChevronRight className="h-4 w-4" /></Button>
          {editable && (
            <>
              <Button size="sm" variant={dirty ? 'default' : 'outline'} onClick={save} disabled={saving}
                style={savedFlash ? { background: 'var(--chart-2)', color: 'var(--primary-foreground)' } : undefined}>
                {saving ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : savedFlash ? <Check className="mr-1 h-4 w-4" /> : <Save className="mr-1 h-4 w-4" />}
                {savedFlash ? '已保存' : '保存'}
              </Button>
              <Button size="sm" onClick={submit} disabled={saving}><Send className="mr-1 h-4 w-4" />提交</Button>
            </>
          )}
          {saveErr && <span className="text-xs" style={{ color: 'var(--destructive)' }}>{saveErr}</span>}
        </div>
      </div>

      <div ref={splitRef} className="flex flex-1 overflow-hidden">
        {/* main: waveform (thin) + transcript table (primary) */}
        <div className="flex min-w-0 flex-1 flex-col gap-2 p-3">
          <div className="thin-scroll shrink-0 overflow-hidden rounded-md border p-2.5" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
            <div ref={containerRef} className="w-full" style={{ minHeight: 124 }} />
            {asset && asset.preprocess_status !== 'ready' && (
              <div className="mt-1.5 text-xs" style={{ color: 'var(--muted-foreground)' }}>波形生成中（preprocess={asset.preprocess_status}）…可先播放</div>
            )}
            <div className="mt-2 flex items-center gap-3">
              <Button size="sm" variant="outline" onClick={() => wsRef.current?.playPause()}>
                {playing ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}
              </Button>
              <span className="flex items-center gap-1 text-xs" style={{ color: 'var(--muted-foreground)' }}><ZoomIn className="h-3.5 w-3.5" />缩放</span>
              <input type="range" min={20} max={400} defaultValue={60} onChange={(e) => wsRef.current?.zoom(Number(e.target.value))} className="av-range h-1 w-36 cursor-pointer" />
              {editable && <span className="text-xs" style={{ color: 'var(--muted-foreground)' }}>波形拖拽=新建段 · 空格 播放 · ↑↓ 切段</span>}
            </div>
          </div>

          {/* transcript table — primary work surface */}
          <datalist id="emo-presets">{EMOTION_OPTIONS.map((em) => <option key={em} value={em}>{em.split('/')[0]}</option>)}</datalist>
          <div className="thin-scroll flex-1 overflow-auto rounded-md border" style={{ borderColor: 'var(--border)', background: 'var(--card)' }}>
            <table className="w-full text-sm">
              <thead className="sticky top-0 z-10" style={{ background: 'var(--muted)', color: 'var(--muted-foreground)' }}>
                <tr className="text-[11px]">
                  <th className="w-9 px-2 py-2"></th>
                  <th className="w-10 px-2 py-2 text-left font-medium">#</th>
                  <th className="w-24 px-2 py-2 text-left font-medium">时间</th>
                  <th className="w-24 px-2 py-2 text-left font-medium">说话人</th>
                  <th className="w-24 px-2 py-2 text-left font-medium">情绪</th>
                  <th className="px-2 py-2 text-left font-medium">转写（点击编辑 · Enter 存并跳下一段）</th>
                  <th className="w-8 px-2 py-2"></th>
                </tr>
              </thead>
              <tbody>
                {sorted.map((s, i) => {
                  const isCur = s.id === currentSegId
                  const isSel = s.id === selectedId
                  const bg = isSel ? 'var(--accent)' : isCur ? 'color-mix(in srgb, var(--chart-2) 12%, transparent)' : undefined
                  return (
                    <tr key={s.id}
                      ref={(el) => { if (el) rowRefs.current.set(s.id, el); else rowRefs.current.delete(s.id) }}
                      onClick={() => selectSeg(s.id)}
                      className="border-t align-top hover:bg-[var(--muted)]"
                      style={{ borderColor: 'var(--border)', background: bg }}>
                      <td className="px-1 py-1.5 text-center">
                        <button title="试听该段" onClick={(e) => { e.stopPropagation(); playSegmentById(s.id) }}
                          className="inline-flex h-6 w-6 items-center justify-center rounded hover:bg-[var(--background)]" style={{ color: 'var(--primary)' }}>
                          {isCur && playing ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
                        </button>
                      </td>
                      <td className="px-2 py-1.5 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>{i + 1}</td>
                      <td className="px-2 py-1.5 font-mono text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                        {fmtSec((s.time_start_ms ?? 0) / 1000)}<br />{fmtSec((s.time_end_ms ?? 0) / 1000)}
                      </td>
                      <td className="px-2 py-1.5" onClick={(e) => e.stopPropagation()}>
                        <select value={(s.attrs?.speaker as string) || ''} disabled={!editable}
                          onChange={(e) => updateRegion(s.id, { speaker: e.target.value })}
                          className="w-full rounded border px-1 py-0.5 text-xs outline-none disabled:opacity-60"
                          style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                          <option value="">—</option>
                          {speakerOptions.map((sp) => <option key={sp} value={sp}>{sp}</option>)}
                        </select>
                      </td>
                      <td className="px-2 py-1.5" onClick={(e) => e.stopPropagation()}>
                        {editable ? (
                          <input list="emo-presets" defaultValue={(s.attrs?.emotion as string) || ''} placeholder="情绪（可填/选）"
                            onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); (e.target as HTMLInputElement).blur() } }}
                            onBlur={(e) => { const v = e.target.value.trim(); if (v !== ((s.attrs?.emotion as string) || '')) updateRegion(s.id, { emotion: v }) }}
                            className="w-full rounded border px-1 py-0.5 text-xs outline-none"
                            style={{ borderColor: 'var(--input)', background: 'var(--background)',
                              color: EMOTION_META[((s.attrs?.emotion as string) || '').split('/').pop()!.toLowerCase()]?.color }} />
                        ) : (
                          s.attrs?.emotion ? <EmotionTag emotion={s.attrs.emotion as string} /> : <span style={{ color: 'var(--muted-foreground)' }}>—</span>
                        )}
                      </td>
                      <td className="px-2 py-1.5" onClick={(e) => e.stopPropagation()}>
                        {editingId === s.id ? (
                          <textarea autoFocus defaultValue={(s.attrs?.text as string) ?? ''} rows={2}
                            disabled={!editable}
                            onKeyDown={(e) => {
                              if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); commitAndAdvance(s.id, (e.target as HTMLTextAreaElement).value) }
                              else if (e.key === 'Escape') { e.preventDefault(); setEditingId(null) }
                            }}
                            onBlur={(e) => commitText(s.id, e.target.value)}
                            className="w-full resize-none rounded border px-2 py-1 text-xs outline-none"
                            style={{ borderColor: 'var(--primary)', background: 'var(--background)' }} />
                        ) : (
                          <div onClick={() => editable && startEdit(s.id)}
                            className={`min-h-[1.5rem] whitespace-pre-wrap rounded px-1 py-0.5 text-xs ${editable ? 'cursor-text hover:bg-[var(--background)]' : ''}`}>
                            {(s.attrs?.text as string) || <span style={{ color: 'var(--muted-foreground)' }}>（空白 · 点击输入）</span>}
                            {s.attrs?.source === 'asr' && <span className="ml-1.5 align-middle text-[9px]" style={{ color: 'var(--chart-2)' }}>AI</span>}
                          </div>
                        )}
                      </td>
                      <td className="px-1 py-1.5 text-center" onClick={(e) => e.stopPropagation()}>
                        {editable && (
                          <button title="删除该段" onClick={() => deleteSeg(s.id)}
                            className="inline-flex h-6 w-6 items-center justify-center rounded text-red-500 hover:bg-red-50">
                            <Trash2 className="h-3.5 w-3.5" />
                          </button>
                        )}
                      </td>
                    </tr>
                  )
                })}
                {sorted.length === 0 && (
                  <tr><td colSpan={7} className="px-3 py-10 text-center text-xs" style={{ color: 'var(--muted-foreground)' }}>
                    暂无段落 · 在波形上拖拽新建，或右侧「AI 预标注」运行转写
                  </td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        <SplitHandle onPointerDown={startDrag} title="拖动调节宽度" />

        {/* right: AI + selected-segment meta */}
        <div className="flex shrink-0 flex-col border-l" style={{ width: panelWidth, borderColor: 'var(--border)', background: 'var(--card)' }}>
          <div className="border-b px-3 py-2.5 text-sm font-medium" style={{ borderColor: 'var(--border)' }}>{reviewing ? '审核 & 段落' : 'AI & 段落'}</div>
          <div className="thin-scroll flex-1 space-y-3 overflow-auto p-3">
            {reviewing && (
              <div className="space-y-2 rounded-md border p-2.5" style={{ borderColor: 'var(--primary)', background: 'var(--muted)' }}>
                <div className="flex items-center gap-1.5 text-[11px] font-medium" style={{ color: 'var(--muted-foreground)' }}>
                  <Check className="h-3.5 w-3.5" style={{ color: 'var(--primary)' }} />审核 · ▶ 逐段试听、核对转写/说话人/边界后裁决
                </div>
                <textarea value={reviewNote} onChange={(e) => setReviewNote(e.target.value)} rows={4}
                  placeholder="审核意见（驳回时必填）…"
                  className="w-full resize-none rounded-md border p-2 text-xs outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                <div className="flex flex-wrap gap-1.5">
                  {['转写不准', '说话人错误', '边界不贴合', '漏标段落'].map((q) => (
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
            {editable && audioCaps.length > 0 && (
              <div className="space-y-2 rounded-md border p-2.5" style={{ borderColor: 'var(--border)', background: 'var(--muted)' }}>
                <div className="flex items-center gap-1.5 text-[11px] font-medium" style={{ color: 'var(--muted-foreground)' }}>
                  <Sparkles className="h-3.5 w-3.5" style={{ color: 'var(--primary)' }} />AI 预标注 · 自选模型再调用
                </div>
                {audioCaps.map((c) => {
                  const ms = capModels.filter((m) => m.capability_type === c)
                  const sel = modelSel[c] ?? (ms[0]?.model ?? '')
                  const running = invokeMut.isPending && invokeMut.variables?.capability === c
                  return (
                    <div key={c} className="space-y-1">
                      <div className="text-xs">{audioCapLabel(c)}</div>
                      <div className="flex items-center gap-2">
                        <select value={sel} disabled={ms.length === 0}
                          onChange={(e) => setModelSel((p) => ({ ...p, [c]: e.target.value }))}
                          className="min-w-0 flex-1 rounded border px-1 py-1 text-xs outline-none disabled:opacity-60"
                          style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                          {ms.length === 0 && <option value="">（无可用模型 · 去「能力配置」接入）</option>}
                          {ms.map((m) => (
                            <option key={`${m.provider_name}|${m.model}`} value={m.model}>
                              {m.provider_name}{m.model ? ` · ${m.model}` : ''}
                            </option>
                          ))}
                        </select>
                        <Button size="sm" variant="outline" disabled={invokeMut.isPending || ms.length === 0}
                          onClick={() => { setInvokeMsg(null); invokeMut.mutate({ capability: c, model: sel || undefined }) }}>
                          {running ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : '运行'}
                        </Button>
                      </div>
                    </div>
                  )
                })}
                {invokeMsg && <p className="text-[11px]" style={{ color: invokeMsg.kind === 'ok' ? 'var(--chart-2)' : 'var(--destructive)' }}>{invokeMsg.text}</p>}
                <p className="text-[10px]" style={{ color: 'var(--muted-foreground)' }}>结果作为「AI」段，校对修改即采纳。</p>
              </div>
            )}

            {selected && (
              <div className="space-y-2.5 rounded-md border p-2.5" style={{ borderColor: 'var(--primary)', background: 'var(--muted)' }}>
                <div className="flex items-center gap-2">
                  <span className="font-mono text-[11px]" style={{ color: 'var(--muted-foreground)' }}>
                    第 {sorted.findIndex((s) => s.id === selected.id) + 1} 段 · {fmtSec((selected.time_start_ms ?? 0) / 1000)} → {fmtSec((selected.time_end_ms ?? 0) / 1000)}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <span className="w-12 shrink-0 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>说话人</span>
                  <input value={speakerDraft} list="spk-presets" disabled={!editable} placeholder="如 spk0 / S1"
                    onChange={(e) => setSpeakerDraft(e.target.value)}
                    onBlur={() => updateRegion(selected.id, { speaker: speakerDraft })}
                    className="w-full rounded-md border px-2 py-1 text-xs outline-none disabled:opacity-60"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  <datalist id="spk-presets">{speakerOptions.map((sp) => <option key={sp} value={sp} />)}</datalist>
                </div>
                <div className="flex items-start gap-2">
                  <span className="w-12 shrink-0 pt-1 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>转写</span>
                  <textarea value={textDraft} disabled={!editable} rows={5} placeholder="该段转写文本（自由编辑）"
                    onChange={(e) => setTextDraft(e.target.value)}
                    onBlur={() => updateRegion(selected.id, { text: textDraft })}
                    className="w-full resize-none rounded-md border px-2 py-1 text-xs outline-none disabled:opacity-60"
                    style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                </div>
                <div className="flex items-center gap-2">
                  <span className="w-12 shrink-0 text-[11px]" style={{ color: 'var(--muted-foreground)' }}>标签</span>
                  {ontologyLabels.length > 0 ? (
                    <select value={selected.label ?? ''} disabled={!editable}
                      onChange={(e) => updateRegion(selected.id, { label: e.target.value })}
                      className="w-full rounded-md border px-2 py-1 text-xs outline-none disabled:opacity-60"
                      style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                      <option value="">（未选）</option>
                      {ontologyLabels.map((l) => <option key={l.name} value={l.name}>{l.display || l.name}</option>)}
                    </select>
                  ) : (
                    <input value={selected.label ?? ''} disabled={!editable} placeholder="标签"
                      onChange={(e) => updateRegion(selected.id, { label: e.target.value })}
                      className="w-full rounded-md border px-2 py-1 text-xs outline-none disabled:opacity-60"
                      style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
                  )}
                </div>
                {editable && (
                  <div className="flex gap-2">
                    <Button size="sm" variant="outline" className="flex-1" onClick={() => playSegmentById(selected.id)}><Play className="mr-1 h-3.5 w-3.5" />试听</Button>
                    <Button size="sm" variant="outline" className="border-red-200 text-red-600" onClick={() => deleteSeg(selected.id)}><Trash2 className="mr-1 h-3.5 w-3.5" />删除</Button>
                  </div>
                )}
              </div>
            )}
          </div>
          <div className="border-t px-3 py-2 text-[11px] leading-relaxed" style={{ borderColor: 'var(--border)', color: 'var(--muted-foreground)' }}>
            ▶ 试听该段 · 点转写文字直接改 · <b>Enter</b> 存并跳下一段 · 空格 播放/暂停 · ↑↓ 切段 · 改完点「保存」
          </div>
        </div>
      </div>
    </div>
  )
}

function regionLabelText(s: Shape): string {
  return (s.attrs?.speaker as string) || s.label || '段'
}

export default AudioAnnotationPage
