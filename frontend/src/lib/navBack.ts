// navBack.ts — 「返回」按钮的共用语义 + 列表页把状态放进 URL 的小工具。
//
// 两个反复踩到的坑：
//   1. `navigate(-1)` 不是「上一级」，是「浏览器后退」。翻过相邻任务之后它退回
//      上一个任务；直接开 URL 或刷新过，它会退出整个应用。
//   2. 分页/筛选放在 useState 里，用户点进详情再返回就回到了第 1 页——路由回对了，
//      状态没了。放进 URL 后，「来处」带上 search 就能原样复原。

import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'

export interface NavFromState {
  from?: string
}

/** 跳进详情页时带上来处（含 query，才能复原分页/筛选）。 */
export function fromState(location: { pathname: string; search?: string }) {
  return { state: { from: location.pathname + (location.search ?? '') } satisfies NavFromState }
}

/** 「返回」去哪：优先来处，否则给定的上一级兜底。 */
export function backToOr(state: unknown, fallback: string): string {
  return (state as NavFromState | null)?.from || fallback
}

/**
 * 工作台顶栏「返回」：来处 → 上一级（本任务所属数据集的资产列表）→ /my-tasks。
 */
export function workbenchBackTo(state: unknown, datasetId?: number | null): string {
  return backToOr(state, datasetId ? `/datasets/${datasetId}/assets` : '/my-tasks')
}

/**
 * 读写 URL query 的列表状态。返回 [searchParams, patch]。
 *
 * patch 只改传入的键；值为 null/undefined/'' 时删除该键（保持 URL 干净，
 * 也让「第 1 页」不写成 ?page=1）。`replace` 用于输入框这类高频改动，
 * 免得每敲一个字就往浏览器历史里压一条。
 */
export function useUrlParams() {
  const [params, setParams] = useSearchParams()
  const patch = useCallback(
    (next: Record<string, string | number | null | undefined>, replace = false) => {
      setParams(
        (prev) => {
          const out = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(next)) {
            if (v === null || v === undefined || v === '') out.delete(k)
            else out.set(k, String(v))
          }
          return out
        },
        { replace },
      )
    },
    [setParams],
  )
  return [params, patch] as const
}

/** URL 里的正整数（缺省/非法 → fallback）。 */
export function urlInt(params: URLSearchParams, key: string, fallback = 1): number {
  const n = Number(params.get(key))
  return Number.isInteger(n) && n > 0 ? n : fallback
}
