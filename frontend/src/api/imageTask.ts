import { client } from './client'
import type { AnnotationTaskMeta } from './asset'

export interface Shape {
  id: string
  kind: string          // 'bbox' | 'polygon' | 'point' | 'audio_region' ...
  label?: string
  points: number[][]
  attrs?: Record<string, any>
  confidence?: number
  source?: string
  color?: string        // 自定义颜色（hex）；空则按标签/默认取色
  // 时序标注（音频区间 / 视频时间点）。后端 payload Shape 已含这两字段。
  time_start_ms?: number
  time_end_ms?: number
}

export interface HumanAnnotation {
  task_id: number
  asset_id: number
  shapes: Shape[]
  texts?: Record<string, string>
  fields?: Record<string, any>
  qa_status: string
  review_note?: string
  version: number
}

export interface OCRBox { x: number; y: number; w: number; h: number; text: string; confidence: number }
export interface OCRResult { boxes: OCRBox[]; structured_md?: string; status: string }
export interface VLMResult { caption?: string; tags?: string[]; status: string }
export interface SegPolygon { class_name: string; class_id: number; points: number[][]; bbox: number[]; score: number }
export interface SegResult { polygons: SegPolygon[]; status: string }

export interface ASRSegment { start_ms: number; end_ms: number; text: string; speaker?: string; confidence?: number; emotion?: string; emotion_scores?: Record<string, number> }
export interface ASRResult { language?: string; duration_ms?: number; segments: ASRSegment[]; status: string }

export interface AIResults { ocr?: OCRResult; vlm?: VLMResult; seg?: SegResult; asr?: ASRResult }

export interface SAMSegmentResult { polygons: number[][]; score: number; mask_png_b64: string }

export interface AdjacentTasks { prev_task_id?: number | null; next_task_id?: number | null }

export interface InvokeResult { status: string; latency_ms?: number; error?: string; [k: string]: any }

export interface ModelProviderRef {
  provider_id: number; provider_name: string; model_id: string
  capability_type: string; endpoint_mode: string; version: string
}
export interface AIRun {
  id: string; run_id: string; capability_type: string; provider: ModelProviderRef
  status: string; error?: string; latency_ms: number; cost: number
  estimated_cost: number; attempt: number; started_at: string; finished_at: string
}
export interface TraceLog {
  id: string; capability_type: string; provider: string; model: string
  status: string; error?: string; latency_ms: number; created_at: string
}
export interface RoutingResult {
  id: string; strategy: string; reasons: string[]; features: Record<string, any>
  need_ocr: boolean; need_caption: boolean
  recommended_models?: ModelProviderRef[]; fallback_chain?: ModelProviderRef[]
  created_at: string
}
export interface TaskTrace {
  routing: RoutingResult | null
  ai_runs: AIRun[]
  trace_logs: TraceLog[]
}

export interface TaskAssignPayload {
  assignee_id?: number
  reviewer_id?: number
  deadline_at?: string
}

// 可补跑 AI 的任务状态（对应旧 Vue adhocAllowedStates）
export const ADHOC_ALLOWED_STATES = new Set([
  'AI_PENDING', 'HUMAN_PENDING', 'HUMAN_IN_PROGRESS', 'QA_PENDING', 'QA_REJECTED', 'FINALIZED', 'EXPORTED',
])
// 可重新路由的任务状态（终态）
export const REROUTE_ALLOWED_STATES = new Set(['FINALIZED', 'EXPORTED', 'QC_FAILED', 'REPROCESS'])

// VLM 可选模型（经 LiteLLM 网关）
export const VLM_MODELS = [
  { value: 'qwen-vl-plus', label: 'qwen-vl-plus（快）' },
  { value: 'qwen-vl-max', label: 'qwen-vl-max（强）' },
]

// 任务状态中文映射
export const TASK_STATE_LABELS: Record<string, string> = {
  CREATED: '已创建',
  ROUTING: '路由中',
  AI_PENDING: '待 AI 处理',
  AI_RUNNING: 'AI 处理中',
  HUMAN_PENDING: '待标注',
  HUMAN_IN_PROGRESS: '标注中',
  QA_PENDING: '待审核',
  FINALIZED: '已完成',
  EXPORTED: '已导出',
  QC_FAILED: '质检失败',
  REPROCESS: '重新处理',
}

export const TASK_STATE_COLOR: Record<string, string> = {
  HUMAN_PENDING: 'border-amber-200 text-amber-700 bg-amber-50',
  HUMAN_IN_PROGRESS: 'border-blue-200 text-blue-700 bg-blue-50',
  QA_PENDING: 'border-purple-200 text-purple-700 bg-purple-50',
  FINALIZED: 'border-emerald-200 text-emerald-700 bg-emerald-50',
  EXPORTED: 'border-emerald-200 text-emerald-700 bg-emerald-50',
  QC_FAILED: 'border-red-200 text-red-700 bg-red-50',
}

export const taskApi = {
  list: (params: Record<string, any> = {}) =>
    client.get<{ items: AnnotationTaskMeta[]; total: number; page: number; page_size: number }>('/tasks', { params }).then((r) => r.data),

  get: (id: number) => client.get<AnnotationTaskMeta>(`/tasks/${id}`).then((r) => r.data),

  getHumanAnnotation: (id: number) =>
    client.get<HumanAnnotation | null>(`/tasks/${id}/human-annotation`).then((r) => r.data),

  putHumanAnnotation: (id: number, draft: Partial<HumanAnnotation>) =>
    client.put<HumanAnnotation>(`/tasks/${id}/human-annotation`, draft).then((r) => r.data),

  submit: (id: number) => client.post(`/tasks/${id}/submit`).then((r) => r.data),

  qaPass: (id: number, note = '') => client.post(`/tasks/${id}/qa/pass`, { note }).then((r) => r.data),
  qaReject: (id: number, note = '') => client.post(`/tasks/${id}/qa/reject`, { note }).then((r) => r.data),

  getAIResults: (id: number) => client.get<AIResults>(`/tasks/${id}/ai-results`).then((r) => r.data),

  getAdjacent: (id: number, mine = false) =>
    client.get<AdjacentTasks>(`/tasks/${id}/adjacent`, { params: { mine } }).then((r) => r.data),

  segment: (taskId: number, points: number[][], box?: number[]) =>
    client.post<SAMSegmentResult>(`/tasks/${taskId}/segment`, { points, ...(box ? { box } : {}) }).then((r) => r.data),

  assign: (taskId: number, payload: TaskAssignPayload) =>
    client.put<AnnotationTaskMeta>(`/tasks/${taskId}/assign`, payload).then((r) => r.data),

  batchAssign: (taskIds: number[], payload: TaskAssignPayload) =>
    client.post<{ updated: number }>('/tasks/batch-assign', { task_ids: taskIds, ...payload }).then((r) => r.data),

  // 主动补跑某项 AI 能力（capability 例: ocr.structure / vlm.caption / vlm.structured_extract / seg.instance）。
  // VLM 类能力可传 model 走 LiteLLM 网关；OCR/分割忽略 model。后端: POST /tasks/:id/invoke?capability=&model=
  invoke: (taskId: number, capability: string, model?: string) =>
    client
      .post<InvokeResult>(`/tasks/${taskId}/invoke`, null, {
        params: { capability, ...(model ? { model } : {}) },
      })
      .then((r) => r.data),

  // 重新路由：任务回到 ROUTING，AIWorker 按当前探针 + 阈值重跑。后端: POST /tasks/:id/reprocess
  reprocess: (taskId: number) => client.post(`/tasks/${taskId}/reprocess`).then((r) => r.data),

  // 路由结果（策略 / 依据 / 特征）。后端: GET /tasks/:id/routing
  getRouting: (taskId: number) =>
    client.get<RoutingResult>(`/tasks/${taskId}/routing`).then((r) => r.data),

  // AI 运行记录列表。后端: GET /tasks/:id/ai-runs
  getAIRuns: (taskId: number) =>
    client.get<AIRun[]>(`/tasks/${taskId}/ai-runs`).then((r) => r.data),

  // 调用追踪（路由 + ai_runs + trace_logs）。后端: GET /tasks/:id/trace
  getTrace: (taskId: number) =>
    client.get<TaskTrace>(`/tasks/${taskId}/trace`).then((r) => r.data),
}

// 能力→模型清单（统一 env 适配器 + 已启用 DB provider）。后端: GET /capabilities/models
export interface InvokableModel { capability_type: string; provider_name: string; model: string; source: string }

// 当前已注册的 AI 能力列表（capability_type）。后端: GET /capabilities → { capabilities: [...] }
export const capabilityApi = {
  list: () =>
    client
      .get<{ capabilities: string[] } | string[]>('/capabilities')
      .then((r) => (Array.isArray(r.data) ? r.data : r.data.capabilities ?? [])),
  // 工作台「自选模型」用：可选模型清单（来自能力配置/env 适配器）。
  models: (capabilityType?: string) =>
    client
      .get<InvokableModel[]>('/capabilities/models', { params: capabilityType ? { capability_type: capabilityType } : {} })
      .then((r) => (Array.isArray(r.data) ? r.data : [])),
}
