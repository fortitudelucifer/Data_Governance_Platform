// e2e 播种：把测试需要的数据集/资产/任务/track 建出来，并把 ID 写进
// e2e/.e2e-state.json 供各 spec 读取。
//
// 为什么要有这个文件：此前 e2e 硬编码了开发机上的 dataset 326 / task 411,420 /
// image task 1。这意味着 (a) CI 上根本跑不起来，(b) 一次跑崩留下的残渣会污染下
// 一次（我们已经真的踩到过：某个用例把 420 提交进审核态，另一个用例就假失败）。
//
// 本文件**幂等**：按名字找，找不到才建。本地反复跑不会堆积垃圾；CI 空库上从零建好。

import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const STATE_FILE = join(HERE, '.e2e-state.json')

// 后端直连（不走前端 preview 的 /api 代理）
const API = process.env.E2E_API ?? 'http://localhost:8280'

export const ADMIN = { username: 'admin', password: 'admin123' }

// 名字带前缀，肉眼可辨、也便于清理。**不要**改名——spec 里按 ID 用，
// 但人工排查时靠它认。
export const NAMES = {
  video: '[e2e] video',
  image: '[e2e] image',
  filler: (i: number) => `[e2e] filler ${String(i).padStart(3, '0')}`,
}

// 数据集列表 pageSize=20；分页用例要翻到第 3 页 → 至少 41 个数据集。
export const MIN_DATASETS_FOR_3_PAGES = 41

export interface E2EState {
  videoDatasetId: number
  /** 审核类用例专用：带 2 条 track，会被提交/驳回（状态会变） */
  reviewTaskId: number
  reviewAssetId: number
  /** 编辑类用例专用：始终 HUMAN_PENDING，没有 track。
   *  与 reviewTask 分开，是因为审核用例会把任务推进审核态——共用一个任务时，
   *  「成本闸门」那条断言的 AI 预标注区块就整块不渲染，变成假失败（真踩过）。 */
  editTaskId: number
  editAssetId: number
  /** 提交流用例专用：带一条 track（否则后端拒绝提交），状态会被改。
   *  再开一个任务是因为提交会把状态推进 QA_PENDING，塞进 editTask 就会
   *  把「成本闸门」那条用例连带弄挂。 */
  submitTaskId: number
  imageDatasetId: number
  imageTaskId: number
  /** AI track 的置信度，低置信导航用例按它设阈值 */
  aiScore: number
  /** AI track 的首关键帧。**刻意不是 0**：视频本就从第 0 帧开始，断言「跳到 0」
   *  即使跳转根本没发生也会通过；而且第 0 帧处解码器的 mediaTime 舍入会让断言
   *  在慢机器上飘到 1（CI 首次运行就 flaky 了一次）。 */
  aiFirstFrame: number
  /** 人工 track 的唯一关键帧，审核批注锚定用例跳这一帧 */
  humanFrame: number
  /** 人工 track 的 track_id */
  humanTrackId: number
  /** reviewTask 视频的总帧数（工作台顶栏显示 "/ N"） */
  frameCount: number
  /** 两个视频的文件名，资产列表用例按它找行 */
  reviewVideoFile: string
  editVideoFile: string
  /** 数据集名字，数据集列表用例按它找行 */
  videoDatasetName: string
}

export function loadState(): E2EState {
  if (!existsSync(STATE_FILE)) {
    throw new Error(`缺少 ${STATE_FILE}：playwright globalSetup 没跑过？`)
  }
  return JSON.parse(readFileSync(STATE_FILE, 'utf8')) as E2EState
}

// ---------- HTTP 小工具 ----------

let token = ''
let myUserId = 0

async function api(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers)
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (init.body && typeof init.body === 'string') headers.set('Content-Type', 'application/json')
  return fetch(API + path, { ...init, headers })
}

async function json<T>(path: string, init: RequestInit = {}): Promise<T> {
  const r = await api(path, init)
  if (!r.ok) throw new Error(`${init.method ?? 'GET'} ${path} → ${r.status} ${await r.text()}`)
  return (await r.json()) as T
}

async function login() {
  const r = await fetch(`${API}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(ADMIN),
  })
  if (!r.ok) throw new Error(`login → ${r.status} ${await r.text()}`)
  const body = (await r.json()) as { token: string; user: { id: number } }
  token = body.token
  myUserId = body.user.id // 没有 /auth/me；登录响应里就带着
}

async function waitFor<T>(what: string, fn: () => Promise<T | null>, timeoutMs = 90_000): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let last: unknown
  while (Date.now() < deadline) {
    try {
      const v = await fn()
      if (v !== null && v !== undefined) return v
    } catch (e) {
      last = e
    }
    await new Promise((r) => setTimeout(r, 500))
  }
  throw new Error(`等待「${what}」超时（${timeoutMs}ms）${last ? `：${last}` : ''}`)
}

// ---------- 数据集 ----------

interface Dataset { id: number; name: string; modality: string }

async function listAllDatasets(): Promise<Dataset[]> {
  const out: Dataset[] = []
  for (let page = 1; ; page++) {
    const r = await json<{ items?: Dataset[]; total?: number }>(`/datasets?page=${page}&page_size=100`)
    const items = r.items ?? []
    out.push(...items)
    if (items.length < 100) return out
  }
}

async function ensureDataset(name: string, modality: string, all: Dataset[]): Promise<number> {
  const found = all.find((d) => d.name === name)
  if (found) return found.id
  const created = await json<{ id: number }>('/datasets', {
    method: 'POST',
    body: JSON.stringify({ name, modality, annotation_type: 'qa', case_type: 'criminal' }),
  })
  return created.id
}

// ---------- 资产 ----------

interface AssetRow { id: number; original_name: string; task?: { id: number } | null }

async function listAssets(datasetId: number): Promise<AssetRow[]> {
  const r = await json<{ items?: AssetRow[] }>(`/datasets/${datasetId}/assets?page=1&page_size=100`)
  return r.items ?? []
}

async function uploadAsset(datasetId: number, file: string): Promise<void> {
  const bytes = readFileSync(join(HERE, 'fixtures', file))
  const form = new FormData()
  form.append('file', new Blob([bytes]), file)
  const r = await api(`/datasets/${datasetId}/assets`, { method: 'POST', body: form })
  if (!r.ok) throw new Error(`upload ${file} → ${r.status} ${await r.text()}`)
}

/** 资产存在则复用（按原始文件名），否则上传并等它出任务。 */
async function ensureAsset(datasetId: number, file: string): Promise<AssetRow> {
  const existing = (await listAssets(datasetId)).find((a) => a.original_name === file)
  if (existing?.task) return existing
  if (!existing) await uploadAsset(datasetId, file)
  return waitFor(`${file} 的任务创建`, async () => {
    const a = (await listAssets(datasetId)).find((x) => x.original_name === file)
    return a?.task ? a : null
  })
}

/** 视频要等 media-worker 派生出帧索引，否则工作台的帧数是 0、跳帧全被 clamp 到 0。 */
async function waitFrameIndex(assetId: number): Promise<number> {
  const idx = await waitFor<{ count: number }>('帧索引派生', async () => {
    const r = await api(`/assets/${assetId}/derivative/frame_index`)
    if (!r.ok) return null
    const v = (await r.json()) as { count?: number }
    return (v.count ?? 0) > 0 ? (v as { count: number }) : null
  })
  return idx.count
}

// ---------- track ----------

interface Keyframe { frame: number; ts_ms: number; bbox?: number[]; outside?: boolean; occluded?: boolean }
interface TrackRow { id: string; track_id: number; label: string; version: number; keyframes: Keyframe[] }

const kf = (frame: number, bbox: number[]): Keyframe =>
  ({ frame, ts_ms: (frame * 1000) / 30, bbox, outside: false, occluded: false })

/**
 * 幂等，且**自我纠正**：光「有就跳过」不够——夹具形状改了以后，开发机上那条早先
 * 播下的 track 会一直是旧样子，于是本地绿、CI 挂（或反过来）。这里发现首关键帧
 * 不对就原地更新。
 */
async function ensureTracks(taskId: number, aiScore: number, aiFirstFrame: number, humanFrame: number): Promise<number> {
  const cur = await json<{ tracks: TrackRow[] }>(`/tasks/${taskId}/tracks`)
  const ai = cur.tracks.find((t) => t.label === 'cow')
  const human = cur.tracks.find((t) => t.label === '牛')

  if (ai && human) {
    const first = [...ai.keyframes].sort((a, b) => a.frame - b.frame)[0]
    if (first?.frame !== aiFirstFrame) {
      await json(`/tasks/${taskId}/tracks`, {
        method: 'PUT',
        body: JSON.stringify({
          id: ai.id, version: ai.version, track_id: ai.track_id, label: 'cow', kind: 'bbox', color: '#f59e0b',
          attrs: { ai_score: aiScore, ai_model: 'yolo', ai_class: 'cow' },
          keyframes: [kf(aiFirstFrame, [10, 10, 60, 60]), kf(30, [120, 40, 60, 60])],
        }),
      })
    }
    return human.track_id
  }

  // AI track：带 ai_score，供「跳到下一低置信 track」用例。
  await json(`/tasks/${taskId}/tracks`, {
    method: 'PUT',
    body: JSON.stringify({
      track_id: 0, label: 'cow', kind: 'bbox', color: '#f59e0b',
      attrs: { ai_score: aiScore, ai_model: 'yolo', ai_class: 'cow' },
      keyframes: [kf(aiFirstFrame, [10, 10, 60, 60]), kf(30, [120, 40, 60, 60])],
    }),
  })
  // 人工 track：单关键帧，审核批注锚定到这一帧。
  const created = await json<{ track: { track_id: number } }>(`/tasks/${taskId}/tracks`, {
    method: 'PUT',
    body: JSON.stringify({
      track_id: 0, label: '牛', kind: 'bbox', color: '#3b82f6',
      keyframes: [kf(humanFrame, [50, 50, 40, 40])],
    }),
  })
  return created.track.track_id
}

/** 提交流用例的任务至少要有一条 track，否则后端拒绝提交（无草稿）。 */
async function ensureSubmittable(taskId: number): Promise<void> {
  const cur = await json<{ tracks: TrackRow[] }>(`/tasks/${taskId}/tracks`)
  if (cur.tracks.length > 0) return
  await json(`/tasks/${taskId}/tracks`, {
    method: 'PUT',
    body: JSON.stringify({
      track_id: 0, label: 'submit-flow', kind: 'bbox',
      keyframes: [kf(0, [10, 10, 30, 30])],
    }),
  })
}

/** 把 editTask 指派给 admin —— 否则「我的任务」是空的，
 *  「从我的任务进工作台 → 返回」那条用例在净库/CI 上会被 skip。 */
async function assignToSelf(taskId: number): Promise<void> {
  await json(`/tasks/${taskId}/assign`, {
    method: 'PUT',
    body: JSON.stringify({ assignee_id: myUserId }),
  })
}

// ---------- 入口 ----------

export async function seed(): Promise<E2EState> {
  await login()

  let all = await listAllDatasets()
  const videoDatasetId = await ensureDataset(NAMES.video, 'video', all)
  const imageDatasetId = await ensureDataset(NAMES.image, 'image', all)

  // 分页用例要第 3 页存在。开发机上早就够了；CI 空库要补齐。
  all = await listAllDatasets()
  for (let i = all.length; i < MIN_DATASETS_FOR_3_PAGES; i++) {
    await ensureDataset(NAMES.filler(i), 'text', all)
  }

  const reviewAsset = await ensureAsset(videoDatasetId, 'seed_video.mp4')
  const frameCount = await waitFrameIndex(reviewAsset.id)
  const editAsset = await ensureAsset(videoDatasetId, 'seed_video2.mp4')
  await waitFrameIndex(editAsset.id)
  const submitAsset = await ensureAsset(videoDatasetId, 'seed_video3.mp4')
  await waitFrameIndex(submitAsset.id)
  const imageAsset = await ensureAsset(imageDatasetId, 'seed_image.png')

  const aiScore = 0.84
  const aiFirstFrame = 5
  const humanFrame = 37
  const humanTrackId = await ensureTracks(reviewAsset.task!.id, aiScore, aiFirstFrame, humanFrame)
  await assignToSelf(editAsset.task!.id)
  await ensureSubmittable(submitAsset.task!.id)

  const state: E2EState = {
    videoDatasetId,
    reviewTaskId: reviewAsset.task!.id,
    reviewAssetId: reviewAsset.id,
    editTaskId: editAsset.task!.id,
    editAssetId: editAsset.id,
    submitTaskId: submitAsset.task!.id,
    imageDatasetId,
    imageTaskId: imageAsset.task!.id,
    aiScore,
    aiFirstFrame,
    humanFrame,
    humanTrackId,
    frameCount,
    reviewVideoFile: 'seed_video.mp4',
    editVideoFile: 'seed_video2.mp4',
    videoDatasetName: NAMES.video,
  }
  writeFileSync(STATE_FILE, JSON.stringify(state, null, 2))
  return state
}
