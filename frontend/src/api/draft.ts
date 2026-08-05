import { client } from './client'

import type { DraftOp } from '@/lib/draftLog'

// 服务端标注草稿（C0.6b）。
//
// 与本地 IndexedDB 草稿（C0.6a）**分工不同，互不替代**：
//   · 本地草稿：逐笔即时落盘，崩溃丢失窗口≈0；只在这台机器上。
//   · 服务端草稿：每 MM_DRAFT_FLUSH_INTERVAL（默认 5 分钟）推一次，解决
//     **换机器续作**——在家开始、到公司接着画，最多丢一个间隔。
//
// ⚠️ 草稿不是标注：不进导出、不进 QA。导出只读 track_snapshots。

export interface ServerDraft {
  task_id: number
  user_id: number
  baseline: Record<string, { trackId: string; version: number } | null>
  ops: DraftOp[]
  op_count: number
  saved_at: string
  client_rev: number
}

export interface DraftFetch {
  draft: ServerDraft | null
  /** 服务端下发的上传间隔（秒）。间隔属于部署配置，不写死在前端。 */
  flushIntervalSec: number
}

export async function fetchServerDraft(taskId: number): Promise<DraftFetch> {
  const res = await client.get<{ draft: ServerDraft | null; flush_interval_sec: number }>(
    `/tasks/${taskId}/draft`,
  )
  return {
    draft: res.data.draft ?? null,
    flushIntervalSec: res.data.flush_interval_sec || 300,
  }
}

export async function putServerDraft(
  taskId: number,
  body: { baseline: unknown; ops: DraftOp[]; op_count: number; client_rev: number },
): Promise<void> {
  await client.put(`/tasks/${taskId}/draft`, body)
}

export async function deleteServerDraft(taskId: number): Promise<void> {
  await client.delete(`/tasks/${taskId}/draft`)
}
