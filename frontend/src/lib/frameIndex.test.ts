import { describe, expect, it } from 'vitest'

import { seekTimeMs, timeToFrame } from './frameIndex'

// ffprobe 把 pts 存成整毫秒：30fps 下 166.667 → 167。这 0.333ms 的误差正是
// 帧号数学的所有麻烦来源。
const ptsFor = (fps: number, n: number) =>
  Array.from({ length: n }, (_, i) => Math.round((i * 1000) / fps))

describe('seekTimeMs / timeToFrame 必须互为逆', () => {
  // rVFC 的 mediaTime 有两种可能：帧的真实 pts，或刚才 seek 的时间。
  // 两种都必须映射回同一帧，否则跳转就会时对时错。
  for (const fps of [24, 25, 29.97, 30, 50, 60]) {
    it(`fps=${fps}：seek 到第 k 帧后，两种 mediaTime 都算回 k`, () => {
      const n = 40
      const pts = ptsFor(fps, n)
      const bad: string[] = []
      for (let k = 0; k < n - 1; k++) {
        const seeked = seekTimeMs(pts, fps, k)
        const truePts = (k * 1000) / fps // rVFC 回报帧的真实 pts（未取整）

        const fromSeek = timeToFrame(pts, fps, seeked)
        const fromPts = timeToFrame(pts, fps, truePts)
        if (fromSeek !== k) bad.push(`帧${k}: 回报 seek 时间 → ${fromSeek}`)
        if (fromPts !== k) bad.push(`帧${k}: 回报帧 pts → ${fromPts}`)
      }
      expect(bad, bad.slice(0, 5).join(' / ')).toEqual([])
    })
  }

  // 回归钉子：半帧偏移（曾经的实现）会把一半的帧跳错。
  it('半帧偏移会跳错——这正是被修掉的那个 bug', () => {
    const fps = 30
    const pts = ptsFor(fps, 10)
    const halfFrameSeek = (k: number) => pts[k] + 500 / fps
    const wrong = [...Array(8).keys()].filter((k) => timeToFrame(pts, fps, halfFrameSeek(k)) !== k)
    expect(wrong.length, '半帧偏移本就该出错；若这里为空，说明前提假设变了').toBeGreaterThan(0)
  })
})

describe('timeToFrame 边界', () => {
  const fps = 30
  const pts = ptsFor(fps, 10)

  it('首帧之前 → 0，末帧之后 → 末帧（不外推）', () => {
    expect(timeToFrame(pts, fps, -100)).toBe(0)
    expect(timeToFrame(pts, fps, 0)).toBe(0)
    expect(timeToFrame(pts, fps, 99999)).toBe(9)
  })

  it('正好落在 pts 上 → 该帧', () => {
    pts.forEach((t, k) => expect(timeToFrame(pts, fps, t)).toBe(k))
  })

  it('帧索引缺失时按 fps 估算', () => {
    expect(timeToFrame([], 30, 1000)).toBe(30)
    expect(timeToFrame([], 25, 400)).toBe(10)
  })
})

describe('seekTimeMs', () => {
  it('落在该帧的显示区间内，且不越到下一帧', () => {
    const fps = 30
    const pts = ptsFor(fps, 10)
    for (let k = 0; k < 9; k++) {
      const t = seekTimeMs(pts, fps, k)
      expect(t).toBeGreaterThan(pts[k] - 1) // 容忍 pts 的毫秒取整
      expect(t).toBeLessThan(pts[k + 1])
    }
  })

  it('帧索引缺失时退化为 fps 估算', () => {
    expect(seekTimeMs([], 30, 3)).toBeCloseTo(100 + 250 / 30, 3)
  })
})
