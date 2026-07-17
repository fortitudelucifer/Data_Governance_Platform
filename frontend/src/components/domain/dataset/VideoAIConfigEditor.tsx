import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  TRIGGER_LABELS, VIDEO_AI_MAX_FRAMES_CEILING, VIDEO_AI_SAMPLE_STEP_CEILING,
  videoAIConfigApi, type VideoAIConfig, type VideoAITrigger,
} from '@/api/videoAIConfig'

interface Props {
  datasetId: number
  datasetName: string
  onClose: () => void
}

const num = (v: string, fallback: number) => {
  const n = Number(v)
  return Number.isFinite(n) ? n : fallback
}

// VideoAIConfigEditor：detect_track 的数据集级成本闸门（B2.8）。
// GPU 时间大致与「采样帧数」成正比，所以 max_frames 是唯一真正限制「一次点击
// 能花掉多少 GPU」的数字——它属于数据集所有者，不属于点按钮的人。
export function VideoAIConfigEditor({ datasetId, datasetName, onClose }: Props) {
  const qc = useQueryClient()
  const [cfg, setCfg] = useState<VideoAIConfig | null>(null)
  const [note, setNote] = useState('')

  const { data, isLoading } = useQuery({
    queryKey: ['video-ai-config', datasetId],
    queryFn: () => videoAIConfigApi.get(datasetId),
  })
  // 只在首次拿到服务端值时播种本地表单。若跟着 `data` 走，保存后的 refetch
  // 会把管理员刚改的下一个字段又覆盖回服务端的旧值——改得快就丢改动。
  useEffect(() => { setCfg((c) => c ?? data ?? null) }, [data])

  const saveMut = useMutation({
    mutationFn: (c: VideoAIConfig) => videoAIConfigApi.update(datasetId, c),
    onSuccess: (stored) => {
      // 服务端会夹住越界值并回显实际生效的配置——显示它，
      // 而不是让管理员以为自己存进去的是 99999。
      setCfg(stored)
      // 只更新缓存，不触发 refetch：refetch 的响应会晚于用户的下一次编辑到达。
      qc.setQueryData(['video-ai-config', datasetId], stored)
      setNote('已保存。超出上限的数值已被自动收敛到服务端允许的最大值。')
    },
    onError: (e: any) => setNote(`保存失败：${e?.response?.data?.message || e?.message}`),
  })

  const set = <K extends keyof VideoAIConfig>(k: K, v: VideoAIConfig[K]) =>
    setCfg((c) => (c ? { ...c, [k]: v } : c))

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={onClose}>
      <div className="max-h-[85vh] w-full max-w-lg overflow-auto rounded-xl border p-5 shadow-xl"
        style={{ background: 'var(--card)', borderColor: 'var(--border)' }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="text-base font-semibold">AI 预标注设置 · {datasetName}</h2>
          <Button size="sm" variant="ghost" onClick={onClose}><X className="h-4 w-4" /></Button>
        </div>
        <p className="mb-4 text-xs" style={{ color: 'var(--muted-foreground)' }}>
          检测+追踪是 GPU 重活，耗时大致与「采样帧数」成正比。这里设的是这个数据集的天花板：
          标注员可以在工作台挑模型、也可以采得更稀疏，但抬不高帧数上限。
        </p>

        {isLoading || !cfg ? (
          <div className="flex justify-center py-8"><Loader2 className="h-5 w-5 animate-spin" /></div>
        ) : (
          <div className="space-y-3 text-sm">
            <label className="block">
              <span className="mb-1 block text-xs font-medium">触发模式</span>
              <select value={cfg.trigger} onChange={(e) => set('trigger', e.target.value as VideoAITrigger)}
                title="触发模式"
                className="h-9 w-full rounded-md border px-2 outline-none"
                style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                {(Object.keys(TRIGGER_LABELS) as VideoAITrigger[]).map((t) => (
                  <option key={t} value={t}>{TRIGGER_LABELS[t]}</option>
                ))}
              </select>
            </label>

            <div className="grid grid-cols-2 gap-3">
              <label className="block">
                <span className="mb-1 block text-xs font-medium">检测模型</span>
                <select value={cfg.model} onChange={(e) => set('model', e.target.value as VideoAIConfig['model'])}
                  className="h-9 w-full rounded-md border px-2 outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                  <option value="yolo">YOLO26x（快）</option>
                  <option value="rtdetr">RT-DETR（准）</option>
                </select>
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium">追踪器</span>
                <select value={cfg.tracker} onChange={(e) => set('tracker', e.target.value as VideoAIConfig['tracker'])}
                  className="h-9 w-full rounded-md border px-2 outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }}>
                  <option value="botsort">BoT-SORT（带 ReID，少串 ID）</option>
                  <option value="bytetrack">ByteTrack（快）</option>
                </select>
              </label>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <label className="block">
                <span className="mb-1 block text-xs font-medium">采样步长（每 N 帧检测一次）</span>
                <input type="number" min={1} max={VIDEO_AI_SAMPLE_STEP_CEILING} value={cfg.sample_step}
                  onChange={(e) => set('sample_step', num(e.target.value, cfg.sample_step))}
                  className="h-9 w-full rounded-md border px-2 outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium">帧数上限（成本闸门，≤ {VIDEO_AI_MAX_FRAMES_CEILING}）</span>
                <input type="number" min={1} max={VIDEO_AI_MAX_FRAMES_CEILING} value={cfg.max_frames}
                  title="帧数上限"
                  onChange={(e) => set('max_frames', num(e.target.value, cfg.max_frames))}
                  className="h-9 w-full rounded-md border px-2 outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
              </label>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <label className="block">
                <span className="mb-1 block text-xs font-medium">最低置信度（低于此值的 track 丢弃）</span>
                <input type="number" min={0.01} max={1} step={0.05} value={cfg.min_score}
                  onChange={(e) => set('min_score', num(e.target.value, cfg.min_score))}
                  className="h-9 w-full rounded-md border px-2 outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium">最少关键帧（丢单帧闪烁）</span>
                <input type="number" min={1} value={cfg.min_keyframes}
                  onChange={(e) => set('min_keyframes', num(e.target.value, cfg.min_keyframes))}
                  className="h-9 w-full rounded-md border px-2 outline-none"
                  style={{ borderColor: 'var(--input)', background: 'var(--background)' }} />
              </label>
            </div>

            {note && <p className="text-xs" style={{ color: 'var(--muted-foreground)' }}>{note}</p>}

            <div className="flex justify-end gap-2 pt-1">
              <Button size="sm" variant="outline" onClick={onClose}>取消</Button>
              <Button size="sm" disabled={saveMut.isPending} onClick={() => saveMut.mutate(cfg)}>
                {saveMut.isPending && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}保存
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
