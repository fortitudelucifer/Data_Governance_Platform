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
// holds the visible geometry); occluded is passthrough. Dense masks (rle) do NOT
// interpolate: a segment touching an rle-bearing keyframe holds the start
// keyframe wholesale — bbox included, because the RLE is bbox-local and must
// stay aligned with its own anchor box (00《稠密几何存储契约》规则 5).

/** Bbox-local dense mask in COCO compressed RLE form (voxel_mask, Phase C). */
export interface MaskRLE {
  size: [number, number] // [h, w] of the bbox-local mask grid
  counts: string
}

/** One cell in a `cells` keyframe (病理 C2): bbox + bbox-local RLE + class + score.
 *  Mirror of Go paymodel.CellInstance — json tags/field names must match exactly. */
export interface CellInstance {
  bbox: number[] // [x,y,w,h] in level-0 px; the RLE's anchor
  rle?: MaskRLE // bbox-local mask; absent = detection box only
  class?: string
  score: number
}

// ALL_TRACK_KINDS is the canonical geometry-kind enumeration, mirrored from the
// Go payload model (paymodel.AllTrackKinds). The meta-test in
// trackInterpolation.test.ts asserts every kind here has a golden fixture in
// testdata/interpolation/ — the same machine-enforced coverage as the Go side.
// Keep in sync with backend/internal/model/payload/video_track.go.
export const ALL_TRACK_KINDS = ['bbox', 'polygon', 'mask', 'polyline', 'keypoints', 'voxel_mask', 'cells'] as const

export interface Keyframe {
  frame: number
  ts_ms: number
  bbox?: number[] // [x,y,w,h] in rotation-applied display pixel space
  points?: number[] // polygon/polyline/keypoints, flat [x,y,...]
  rle?: MaskRLE // bbox-local dense mask — never lerped
  // instances holds a `cells` keyframe's cells (病理 C2). Like rle it never lerps.
  // Dispatch is by presence not kind: cells' rle is nested here so top-level rle is
  // absent — the hold guard must check instances or lerp silently drops every cell.
  instances?: CellInstance[]
  outside: boolean
  occluded: boolean
  source?: string
}

export interface InterpGeom {
  bbox?: number[]
  points?: number[]
  rle?: MaskRLE
  instances?: CellInstance[]
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
    // Dense masks (rle) and cell sets (instances) do not interpolate: hold the
    // start keyframe wholesale. Dispatch is by presence, not kind — cells' rle is
    // nested in instances so top-level rle is absent; without the instances check
    // a multi-keyframe cells track would lerp nil geometry and drop every cell.
    if (lo.rle || hi.rle || lo.instances?.length || hi.instances?.length) return geomOf(lo)
    const t = (tsMs - lo.ts_ms) / (hi.ts_ms - lo.ts_ms)
    return lerpGeom(lo, hi, t)
  }
  return null
}

function geomOf(k: Keyframe): InterpGeom {
  return { bbox: clone(k.bbox), points: clone(k.points), rle: cloneRLE(k.rle), instances: cloneInstances(k.instances), occluded: k.occluded }
}

function cloneRLE(r: MaskRLE | undefined): MaskRLE | undefined {
  return r ? { size: [r.size[0], r.size[1]], counts: r.counts } : undefined
}

// Deep-clone a cells keyframe's instances (mirror of Go cloneInstances). geomOf
// must clone or a present keyframe returns instances=undefined (fixture red).
function cloneInstances(insts: CellInstance[] | undefined): CellInstance[] | undefined {
  if (!insts || insts.length === 0) return undefined
  return insts.map((c) => ({ bbox: c.bbox.slice(), rle: cloneRLE(c.rle), class: c.class, score: c.score }))
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
