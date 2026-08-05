import { client } from './client'

// B2.8 成本闸门：detect_track 是 GPU 重活。触发模式 / 采样步长 / 帧数上限
// 是数据集级配置——天花板属于数据集所有者，不属于点按钮的人。
export type VideoAITrigger = 'manual' | 'auto' | 'off'

export interface VideoAIConfig {
  trigger: VideoAITrigger
  model: 'yolo' | 'rtdetr'
  tracker: 'botsort' | 'bytetrack'
  sample_step: number
  max_frames: number
  min_score: number
  min_keyframes: number
}

// 服务端硬上限（超出会被夹住并回显实际生效值）
export const VIDEO_AI_MAX_FRAMES_CEILING = 3000
export const VIDEO_AI_SAMPLE_STEP_CEILING = 300

export const TRIGGER_LABELS: Record<VideoAITrigger, string> = {
  manual: '手动触发（标注员在工作台点「AI 预标注」）',
  auto: '自动触发（视频导入后自动排队预标注）',
  off: '关闭（该数据集不允许 AI 预标注）',
}

// GPU 队列积压。inflight = 正在跑，waiting = 排队中，max_wait = 候诊室上限。
export interface GPUQueueStats {
  inflight: number
  waiting: number
  concurrency: number
  max_wait: number
}

export const videoAIConfigApi = {
  get: (datasetId: number) =>
    client.get<VideoAIConfig>(`/datasets/${datasetId}/video-ai-config`).then((r) => r.data),

  // 返回的是**实际存储**的配置：越界值被夹住，而不是被拒绝。
  update: (datasetId: number, cfg: VideoAIConfig) =>
    client.put<VideoAIConfig>(`/datasets/${datasetId}/video-ai-config`, cfg).then((r) => r.data),

  gpuQueue: () =>
    client.get<{ queues: Record<string, GPUQueueStats> }>('/capabilities/gpu-queue').then((r) => r.data.queues),
}

// 队列是否已满（再点就是 429）。
export function queueFull(q?: GPUQueueStats): boolean {
  return !!q && q.max_wait > 0 && q.waiting >= q.max_wait
}
