// trackInterpolation.ts — frontend mirror of the Go interpolation contract
// (backend/internal/service/track_interpolation.go). Both are locked by the
// shared golden fixtures in repo-root testdata/interpolation/: Go reads them in
// track_interpolation_test.go, this side in ./trackInterpolation.test.ts, and CI
// runs both. Keep the two implementations identical in behavior — the canvas the
// annotator sees must equal the frames the exporter writes. Drift raises no
// error anywhere: it silently ships an export that disagrees with what the
// annotator approved.
//
// Rules (CVAT-aligned): interpolate on ts_ms; no extrapolation; an outside:true
// keyframe starts a gap (no transition across it — a visible→outside segment
// holds the visible geometry); occluded is passthrough.

export interface Keyframe {
  frame: number
  ts_ms: number
  bbox?: number[] // [x,y,w,h] in rotation-applied display pixel space
  points?: number[] // polygon/polyline/keypoints, flat [x,y,...]
  outside: boolean
  occluded: boolean
  source?: string
}

export interface InterpGeom {
  bbox?: number[]
  points?: number[]
  occluded: boolean
}

/** Sort keyframes ascending by ts_ms (interpolation requires sorted input). */
export function sortKeyframes(kfs: Keyframe[]): Keyframe[] {
  return [...kfs].sort((a, b) => a.ts_ms - b.ts_ms)
}

/**
 * Geometry at tsMs, or null when the object is not shown (gap / outside / out of
 * range). `kfs` must be sorted ascending by ts_ms.
 */
export function interpolateAt(kfs: Keyframe[], tsMs: number): InterpGeom | null {
  const n = kfs.length
  if (n === 0) return null
  const first = kfs[0]
  const last = kfs[n - 1]
  if (tsMs < first.ts_ms || tsMs > last.ts_ms) return null
  if (tsMs >= last.ts_ms) {
    return last.outside ? null : geomOf(last)
  }
  for (let i = 0; i < n - 1; i++) {
    const lo = kfs[i]
    const hi = kfs[i + 1]
    if (tsMs < lo.ts_ms || tsMs >= hi.ts_ms) continue
    if (lo.outside) return null
    if (tsMs === lo.ts_ms) return geomOf(lo)
    if (hi.outside) return geomOf(lo) // hold visible geometry, no transition
    const t = (tsMs - lo.ts_ms) / (hi.ts_ms - lo.ts_ms)
    return lerpGeom(lo, hi, t)
  }
  return null
}

function geomOf(k: Keyframe): InterpGeom {
  return { bbox: clone(k.bbox), points: clone(k.points), occluded: k.occluded }
}

function lerpGeom(a: Keyframe, b: Keyframe, t: number): InterpGeom {
  return {
    bbox: lerpSlice(a.bbox, b.bbox, t),
    points: lerpSlice(a.points, b.points, t),
    occluded: a.occluded, // state held from the segment's start keyframe
  }
}

function lerpSlice(a: number[] | undefined, b: number[] | undefined, t: number): number[] | undefined {
  if (!a || !b || a.length === 0 || a.length !== b.length) return clone(a)
  return a.map((v, i) => v + (b[i] - v) * t)
}

function clone(s: number[] | undefined): number[] | undefined {
  return s ? s.slice() : undefined
}
