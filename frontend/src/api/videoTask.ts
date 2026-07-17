import { client } from './client'

// Frame index derivative (M0 media-worker). frame→pts_ms map + the 坐标系 anchor
// (rotation + display W/H). Drives rVFC 0-offset frame correction.
export interface FrameIndex {
  count: number
  pts_ms: number[]
  fps?: number
  duration_ms?: number
  rotation?: number // CW display degrees {0,90,180,270}
  width?: number // display width (rotation-applied)
  height?: number // display height
  playback?: boolean // true → source was transcoded; play the playback_mp4 derivative
}

export interface VideoKeyframe {
  frame: number
  ts_ms: number
  bbox?: number[] // [x,y,w,h] in display pixel space
  points?: number[]
  outside: boolean
  occluded: boolean
  source?: string
  // Per-keyframe ontology attributes (scope="keyframe"); invariant attrs live on the track.
  attrs?: Record<string, unknown>
}

export interface VideoTrack {
  id: string
  task_id: number
  dataset_id: number
  asset_id: number
  track_id: number
  label: string
  kind?: string
  color?: string
  attrs?: Record<string, any>
  keyframes: VideoKeyframe[]
  source: string // ai | human
  adopted_from?: string | null
  // 审核员对单条 track 的裁决（B3.1）。空 = 尚未裁决。
  review_status?: 'passed' | 'rejected'
  review_note?: string
  version: number
  is_active: boolean
}

// 返工 diff（B3.1）：两轮提交之间的差异。审核员复核返工时只看真正动过的地方。
export interface TrackChange {
  track_id: number
  label: string
  fields: string[] // label / color / kind / attrs
  keyframes: { added: number[]; removed: number[]; moved: number[] }
  first_frame?: number // 最早被改动的帧，跳转目标
}

export interface TrackDiff {
  from_round: number
  to_round: number
  added: number[] // 只在新一轮存在的 track_id
  removed: number[] // 只在旧一轮存在的 track_id
  changed: TrackChange[]
}

// Single-track upsert payload (per-track granularity; version required to update).
export interface TrackUpsert {
  id?: string
  track_id?: number
  label: string
  kind?: string
  color?: string
  attrs?: Record<string, any>
  keyframes: VideoKeyframe[]
  version?: number
}

export const videoApi = {
  getFrameIndex: (assetId: number) =>
    client.get<FrameIndex>(`/assets/${assetId}/derivative/frame_index`).then((r) => r.data),

  // Same-origin + cookie auth: <video> plays this URL directly (Range/seek, PH-1).
  bodyUrl: (assetId: number) => `/api/assets/${assetId}/body`,

  // Transcoded H.264 derivative for sources the browser can't play natively
  // (HEVC/AV1/…). Served with Range/seek like the original body.
  playbackUrl: (assetId: number) => `/api/assets/${assetId}/derivative/playback_mp4`,

  // SAM 2.1 on the CURRENT frame: the workspace captures the displayed frame to
  // PNG and sends image_b64; the backend traces the mask into a polygon. Used by
  // the canvas SAM tool (点选→多边形关键帧).
  segmentFrame: (taskId: number, points: number[][], imageB64: string, box?: number[]) =>
    client.post<{ polygons: number[][]; score: number; mask_png_b64: string }>(
      `/tasks/${taskId}/segment`, { points, image_b64: imageB64, ...(box ? { box } : {}) }, { timeout: 60000 },
    ).then((r) => r.data),
}

export const trackApi = {
  list: (taskId: number, params?: { source?: string; label?: string }) =>
    client.get<{ tracks: VideoTrack[] }>(`/tasks/${taskId}/tracks`, { params }).then((r) => r.data.tracks),

  // Upsert one track. Throws on 409 (stale version) — caller refreshes.
  put: (taskId: number, body: TrackUpsert) =>
    client.put<{ track: VideoTrack }>(`/tasks/${taskId}/tracks`, body).then((r) => r.data.track),

  remove: (taskId: number, trackObjectId: string) =>
    client.delete(`/tasks/${taskId}/tracks/${trackObjectId}`).then((r) => r.data),

  adopt: (taskId: number, trackObjectId: string) =>
    client.post<{ track: VideoTrack }>(`/tasks/${taskId}/tracks/${trackObjectId}/adopt`).then((r) => r.data.track),

  // 逐 track 裁决（B3.1）。status 传 '' 表示撤销裁决。
  // 只要还有 track 是 rejected，整体「审核通过」会被后端拒绝。
  review: (taskId: number, trackObjectId: string, status: 'passed' | 'rejected' | '', note = '') =>
    client.post<{ ok: boolean }>(`/tasks/${taskId}/tracks/${trackObjectId}/review`, { status, note }).then((r) => r.data),

  // 返工 diff（B3.1）：两轮提交之间标注员到底改了什么。
  // 不传 from/to 就对比最近两轮；只提交过一轮时 diff 为 null（不是错误）。
  diff: (taskId: number, from?: number, to?: number) =>
    client.get<{ diff: TrackDiff | null; reason?: string }>(`/tasks/${taskId}/diff`, { params: { from, to } }).then((r) => r.data),

  // Manually run AI detection+tracking (det-server) → writes mm_tracks(source:ai).
  // Synchronous; re-running archives prior AI tracks first (no dup pile-up).
  detectTrack: (taskId: number, opts?: { model?: string; tracker?: string; sample_step?: number }) =>
    client.post<{ ok: boolean; tracks_written: number }>(`/tasks/${taskId}/detect-track`, opts ?? {}, { timeout: 300000 }).then((r) => r.data),

  // Batch-adopt AI tracks: all / by label / by score threshold (B2.7).
  adoptBatch: (taskId: number, filter: { all?: boolean; track_ids?: string[]; label?: string; min_score?: number }) =>
    client.post<{ adopted: number; tracks: VideoTrack[] }>(`/tasks/${taskId}/adopt-tracks`, filter).then((r) => r.data),

  // SAM2 cross-frame propagation (B2.2): one point on one frame → whole-clip mask
  // track. Synchronous (SAM2 propagation takes seconds); long timeout.
  propagate: (taskId: number, body: { frame: number; points: number[][]; box?: number[]; sample_step?: number; label?: string; auto_adopt?: boolean }) =>
    client.post<{ ok: boolean; keyframes: number }>(`/tasks/${taskId}/propagate`, body, { timeout: 300000 }).then((r) => r.data),
}
