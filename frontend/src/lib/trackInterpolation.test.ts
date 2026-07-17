import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import { interpolateAt, sortKeyframes, type Keyframe } from './trackInterpolation'

// 插值契约的单一真源被实现了两遍：Go（导出器用，写进文件的框）和这份 TS
// （画布用，标注员看到的框）。两边必须行为一致，否则会出现「标注员在第 37 帧
// 明明看到框、导出的 MOT 里那一帧是空的」——不报错，文件看起来完全正常。
//
// 仓库根 testdata/interpolation/ 的 golden 夹具就是为此存在。
// backend/internal/service/track_interpolation_test.go 读的是**同一批文件**。
// 这个测试补上了此前缺失的另一半：在它之前，TS 侧没有任何东西锁着。

const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURE_DIR = join(HERE, '..', '..', '..', 'testdata', 'interpolation')

interface Query {
  frame: number
  ts_ms: number
  present: boolean
  bbox?: number[]
  // points 覆盖 polygon / mask 轨迹（SAM2 传播写的就是这个）。Go 侧原本只解析
  // bbox，polygon 夹具会「解析成功但什么都不断言」——这个洞一并补上了。
  points?: number[]
  occluded?: boolean
}
interface Fixture {
  name?: string
  desc?: string
  keyframes: Keyframe[]
  queries: Query[]
}

function loadFixtures(): { file: string; fx: Fixture }[] {
  const files = readdirSync(FIXTURE_DIR).filter((f) => f.endsWith('.json')).sort()
  return files.map((file) => ({
    file,
    fx: JSON.parse(readFileSync(join(FIXTURE_DIR, file), 'utf8')) as Fixture,
  }))
}

const fixtures = loadFixtures()

describe('插值 golden 夹具（与 Go 侧共享同一批文件）', () => {
  // 夹具目录空了就等于这道闸没了 —— 与 Go 侧的 `no fixtures found` 同一个断言。
  it('至少能读到一个夹具', () => {
    expect(fixtures.length).toBeGreaterThan(0)
  })

  for (const { file, fx } of fixtures) {
    describe(fx.name ?? file, () => {
      // 夹具里的关键帧本就按 ts_ms 递增；仍走一遍 sortKeyframes，
      // 因为运行时（VideoAnnotationPage）也是这么调的。
      const kfs = sortKeyframes(fx.keyframes)

      // 与 Go 侧 almostEqualSlice 同一个容差
      const expectAlmostEqual = (got: number[] | undefined, want: number[], what: string) => {
        expect(got, `${what} 缺失`).toBeDefined()
        expect(got!.length, `${what} 长度`).toBe(want.length)
        got!.forEach((v, j) => {
          expect(Math.abs(v - want[j]), `${what}[${j}] = ${v}, want ${want[j]}`).toBeLessThanOrEqual(1e-6)
        })
      }

      for (const [i, q] of fx.queries.entries()) {
        it(`query[${i}] frame=${q.frame} ts=${q.ts_ms} → ${q.present ? '可见' : '不产帧'}`, () => {
          const got = interpolateAt(kfs, q.ts_ms)

          expect(got !== null, `present=${got !== null}, want ${q.present}`).toBe(q.present)
          if (!q.present) return

          // present=true 却既没给 bbox 也没给 points，等于什么都没断言 —— 夹具写错了
          expect(q.bbox !== undefined || q.points !== undefined,
            'present=true 的 query 必须至少给出 bbox 或 points').toBe(true)

          if (q.bbox) expectAlmostEqual(got!.bbox, q.bbox, 'bbox')
          if (q.points) expectAlmostEqual(got!.points, q.points, 'points')
          if (q.occluded !== undefined) expect(got!.occluded).toBe(q.occluded)
        })
      }
    })
  }
})

// 与 Go 侧 TestInterpolateAt_EmptyAndDegenerate 对应。
describe('退化输入', () => {
  it('空关键帧不产帧', () => {
    expect(interpolateAt([], 0)).toBeNull()
    expect(interpolateAt([], 100)).toBeNull()
  })
})

describe('sortKeyframes', () => {
  const kf = (ts: number): Keyframe =>
    ({ frame: ts / 100, ts_ms: ts, bbox: [0, 0, 1, 1], outside: false, occluded: false })

  it('按 ts_ms 升序，且不改动入参', () => {
    const input = [kf(300), kf(100), kf(200)]
    const sorted = sortKeyframes(input)
    expect(sorted.map((k) => k.ts_ms)).toEqual([100, 200, 300])
    expect(input.map((k) => k.ts_ms)).toEqual([300, 100, 200]) // 原数组未被就地排序
  })
})
