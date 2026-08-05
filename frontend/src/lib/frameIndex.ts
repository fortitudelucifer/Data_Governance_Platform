// frameIndex.ts — 帧号 ↔ 时间的互转，从 VideoAnnotationPage 抽出来。
//
// 抽出来是因为它咬过我们三次，而组件里的 useCallback 没法单测。两个函数必须
// **互为逆**：seekTimeMs(k) 之后，无论 requestVideoFrameCallback 回报的是
// 「帧的 pts」还是「刚才 seek 的时间」，timeToFrame 都得算回 k。

/** 帧号 → 帧显示区间内的一个安全时刻（ms）。 */
export function seekTimeMs(ptsMs: number[], fps: number, k: number): number {
  const base = ptsMs[k] != null ? ptsMs[k] : (k * 1000) / fps
  // 落在帧内 1/4 处，不是 1/2。这个偏移必须同时满足：
  //  (a) 大于 pts_ms 的毫秒取整误差（ffprobe 把 166.667 存成 167，误差 ~0.5ms），
  //      否则 rVFC 回报真实 pts 时，最近邻会把它算成**上一帧**；
  //  (b) 明显小于半帧，否则 rVFC 回报 seek 时间时会越过最近邻的分界线
  //      （分界线就在半帧处），落到**下一帧**。
  // 半帧偏移恰好压在分界线上：30fps 下 8 帧里有 5 帧会跳错。
  return base + 250 / fps
}

/** 时间（ms）→ 最接近的帧号。ptsMs 为空时退化为按 fps 估算。 */
export function timeToFrame(ptsMs: number[], fps: number, ms: number): number {
  if (ptsMs.length === 0) return Math.round((ms / 1000) * fps)
  const hi0 = ptsMs.length - 1
  if (ms <= ptsMs[0]) return 0
  if (ms >= ptsMs[hi0]) return hi0
  let lo = 0
  let hi = hi0
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (ptsMs[mid] < ms) lo = mid + 1
    else hi = mid
  }
  // 最近邻（而非 floor）：pts_ms 是毫秒取整过的，floor 会因 0.x ms 的误差退一帧。
  return lo > 0 && Math.abs(ptsMs[lo - 1] - ms) <= Math.abs(ptsMs[lo] - ms) ? lo - 1 : lo
}
