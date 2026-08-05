import { describe, expect, it } from 'vitest'

import { annotatedSlices, bboxRLEToMask, keyframesToMaskVolume, maskToBboxRLE, maskVolumeToKeyframes, paintBrush, paintBrushLine } from './maskSlice'

// 一裁一放是整条掩膜链路最容易静默出错的地方:错了掩膜整体平移几个像素,
// 画面上依然是个像模像样的形状,位置却是错的(而这在医学影像里就是"标错了
// 器官/病灶")。所以往返、边界、非方形都逐条钉死。

const W = 12
const H = 9

function maskFromRows(rows: string[]): Uint8Array {
  const m = new Uint8Array(W * H)
  rows.forEach((r, y) => {
    for (let x = 0; x < r.length; x++) if (r[x] === '#') m[y * W + x] = 1
  })
  return m
}
function rowsFromMask(m: Uint8Array): string[] {
  return Array.from({ length: H }, (_, y) =>
    Array.from({ length: W }, (_, x) => (m[y * W + x] ? '#' : '.')).join(''),
  )
}

const shape = [
  '............',
  '............',
  '...###......',
  '..#####.....',
  '..#####.....',
  '...###......',
  '............',
  '............',
  '............',
]

describe('maskToBboxRLE', () => {
  it('bbox 紧贴前景(不多不少)', () => {
    const bm = maskToBboxRLE(maskFromRows(shape), W, H)!
    expect(bm).not.toBeNull()
    // 前景列 2..6,行 2..5 → bbox = [x=2, y=2, w=5, h=4]
    expect(bm.bbox).toEqual([2, 2, 5, 4])
    expect(bm.rle.size).toEqual([4, 5]) // [h, w]
  })

  it('全空掩膜 → null(不产生零面积标注)', () => {
    expect(maskToBboxRLE(new Uint8Array(W * H), W, H)).toBeNull()
  })

  it('掩膜尺寸不符 → 抛错', () => {
    expect(() => maskToBboxRLE(new Uint8Array(5), W, H)).toThrow(/want/)
  })
})

describe('往返:画 → 裁 → 存 → 放回,必须逐像素相同', () => {
  it('普通形状', () => {
    const orig = maskFromRows(shape)
    const back = bboxRLEToMask(maskToBboxRLE(orig, W, H)!, W, H)
    expect(rowsFromMask(back)).toEqual(rowsFromMask(orig))
  })

  it('单像素(退化 bbox)', () => {
    const m = new Uint8Array(W * H)
    m[5 * W + 7] = 1
    const bm = maskToBboxRLE(m, W, H)!
    expect(bm.bbox).toEqual([7, 5, 1, 1])
    expect([...bboxRLEToMask(bm, W, H)]).toEqual([...m])
  })

  it('贴四个边界(最容易差一像素的地方)', () => {
    const m = new Uint8Array(W * H)
    m[0] = 1 // 左上角
    m[W - 1] = 1 // 右上角
    m[(H - 1) * W] = 1 // 左下角
    m[(H - 1) * W + (W - 1)] = 1 // 右下角
    const bm = maskToBboxRLE(m, W, H)!
    expect(bm.bbox).toEqual([0, 0, W, H])
    expect([...bboxRLEToMask(bm, W, H)]).toEqual([...m])
  })

  it('非方形且偏置的块(x≠y 时行列不会互换)', () => {
    const m = new Uint8Array(W * H)
    // 宽 4 高 2 的块,放在 (x=6, y=1)
    for (let y = 1; y <= 2; y++) for (let x = 6; x <= 9; x++) m[y * W + x] = 1
    const bm = maskToBboxRLE(m, W, H)!
    expect(bm.bbox).toEqual([6, 1, 4, 2])
    expect([...bboxRLEToMask(bm, W, H)]).toEqual([...m])
  })

  it('两块分离的区域共用一个 bbox,放回后仍然分离', () => {
    const m = maskFromRows([
      '............',
      '.##......##.',
      '.##......##.',
      '............',
      '............',
      '............',
      '............',
      '............',
      '............',
    ])
    const bm = maskToBboxRLE(m, W, H)!
    expect(bm.bbox).toEqual([1, 1, 10, 2])
    expect([...bboxRLEToMask(bm, W, H)]).toEqual([...m])
  })
})

describe('bboxRLEToMask 的防错', () => {
  it('bbox 与 RLE.size 不符 → 抛错(错位的根源)', () => {
    const bm = maskToBboxRLE(maskFromRows(shape), W, H)!
    const tampered = { ...bm, bbox: [2, 2, 6, 4] as [number, number, number, number] }
    expect(() => bboxRLEToMask(tampered, W, H)).toThrow(/不符/)
  })

  it('超出切片范围的部分被裁掉而不是崩溃', () => {
    const bm = maskToBboxRLE(maskFromRows(shape), W, H)!
    const shifted = { ...bm, bbox: [W - 2, H - 2, bm.bbox[2], bm.bbox[3]] as [number, number, number, number] }
    const out = bboxRLEToMask(shifted, W, H)
    expect(out.length).toBe(W * H) // 没崩,且尺寸正确
  })
})

describe('掩膜体 ↔ 关键帧(存取的桥)', () => {
  const dims: [number, number, number] = [W, H, 5] // nx, ny, nz

  it('只为非空切片产生关键帧,frame/ts_ms 都用 z', () => {
    const vol = new Uint8Array(W * H * 5)
    // 只在 z=1 和 z=3 画东西
    vol.set(maskFromRows(shape), 1 * W * H)
    const single = new Uint8Array(W * H)
    single[4 * W + 8] = 1
    vol.set(single, 3 * W * H)

    const kfs = maskVolumeToKeyframes(vol, dims)
    expect(kfs.map((k) => k.frame)).toEqual([1, 3]) // 空切片不占关键帧
    expect(kfs.map((k) => k.ts_ms)).toEqual([1, 3]) // z 充当时间轴
    expect(kfs[0].bbox).toEqual([2, 2, 5, 4])
    expect(kfs[1].bbox).toEqual([8, 4, 1, 1])
  })

  it('全空掩膜体 → 没有关键帧', () => {
    expect(maskVolumeToKeyframes(new Uint8Array(W * H * 5), dims)).toEqual([])
  })

  it('往返:掩膜体 → 关键帧 → 掩膜体,逐体素相同', () => {
    const vol = new Uint8Array(W * H * 5)
    vol.set(maskFromRows(shape), 1 * W * H)
    vol.set(maskFromRows(shape), 4 * W * H)
    const back = keyframesToMaskVolume(maskVolumeToKeyframes(vol, dims), dims)
    expect([...back]).toEqual([...vol])
  })

  it('切片不会串到别的 z(装配错位会被抓到)', () => {
    const vol = new Uint8Array(W * H * 5)
    const m = new Uint8Array(W * H)
    m[0] = 1
    vol.set(m, 2 * W * H) // 只有 z=2 的 (0,0) 亮
    const back = keyframesToMaskVolume(maskVolumeToKeyframes(vol, dims), dims)
    expect(back[2 * W * H]).toBe(1)
    expect(back[0]).toBe(0) // z=0 不该被写
    expect(back[1 * W * H]).toBe(0)
  })

  it('超出当前卷的历史关键帧被跳过而不是崩', () => {
    const kfs = maskVolumeToKeyframes(
      (() => {
        const v = new Uint8Array(W * H * 5)
        v.set(maskFromRows(shape), 1 * W * H)
        return v
      })(),
      dims,
    )
    const shifted = kfs.map((k) => ({ ...k, frame: 99 }))
    expect(() => keyframesToMaskVolume(shifted, dims)).not.toThrow()
  })

  it('体尺寸不符 → 抛错', () => {
    expect(() => maskVolumeToKeyframes(new Uint8Array(10), dims)).toThrow(/want/)
  })
})

describe('paintBrush', () => {
  it('圆形笔刷落在中心附近,半径外不涂', () => {
    const m = new Uint8Array(W * H)
    paintBrush(m, W, H, 5, 4, 2)
    expect(m[4 * W + 5]).toBe(1) // 圆心
    expect(m[4 * W + 3]).toBe(1) // 距离 2,在半径内
    expect(m[4 * W + 0]).toBe(0) // 距离 5,在半径外
  })

  it('擦除模式清零,且不越界', () => {
    const m = new Uint8Array(W * H).fill(1)
    paintBrush(m, W, H, 0, 0, 2, true) // 贴角擦除
    expect(m[0]).toBe(0)
    expect(m[H * W - 1]).toBe(1) // 远处不受影响
  })

  it('画完能编码成 RLE(与后续保存链路连得上)', () => {
    const m = new Uint8Array(W * H)
    paintBrush(m, W, H, 6, 4, 3)
    const bm = maskToBboxRLE(m, W, H)
    expect(bm).not.toBeNull()
    expect([...bboxRLEToMask(bm!, W, H)]).toEqual([...m])
  })
})

describe('annotatedSlices(已标注层清单)', () => {
  const dims: [number, number, number] = [W, H, 5]

  it('只列出有内容的层,并给出面积', () => {
    const vol = new Uint8Array(W * H * 5)
    vol.set(maskFromRows(shape), 1 * W * H) // shape 面积 = 3+5+5+3 = 16
    const one = new Uint8Array(W * H)
    one[0] = 1
    vol.set(one, 4 * W * H)

    const stats = annotatedSlices(vol, dims)
    expect(stats.map((s) => s.z)).toEqual([1, 4])
    expect(stats[0].voxels).toBe(16)
    expect(stats[1].voxels).toBe(1)
  })

  it('空掩膜体 → 空清单', () => {
    expect(annotatedSlices(new Uint8Array(W * H * 5), dims)).toEqual([])
  })

  it('与关键帧清单一致(同一批 z,两条路径不许漂)', () => {
    const vol = new Uint8Array(W * H * 5)
    vol.set(maskFromRows(shape), 2 * W * H)
    vol.set(maskFromRows(shape), 3 * W * H)
    expect(annotatedSlices(vol, dims).map((s) => s.z)).toEqual(
      maskVolumeToKeyframes(vol, dims).map((k) => k.frame),
    )
  })
})

describe('paintBrushLine(#20 快速拖动补空洞)', () => {
  it('填补相邻采样之间的空洞', () => {
    const w = 40
    const h = 10
    const mask = new Uint8Array(w * h)
    // 两点相距 20px,远超 2r=4:只画两端圆盘会在中间留大片空洞。
    paintBrushLine(mask, w, h, 5, 5, 25, 5, 2)
    expect(mask[5 * w + 15]).toBe(1) // 中点必须被覆盖
    for (let x = 6; x <= 24; x += 3) expect(mask[5 * w + x]).toBe(1) // 线上连续无缝
    // 对照:两个孤立圆盘会在中点留空(证明这不是"到处都填")。
    const holed = new Uint8Array(w * h)
    paintBrush(holed, w, h, 5, 5, 2)
    paintBrush(holed, w, h, 25, 5, 2)
    expect(holed[5 * w + 15]).toBe(0)
  })
})

describe('keyframesToMaskVolume(#9 outside 与导出器对齐)', () => {
  it('outside 帧不产像素(否则前端显示、导出跳过)', () => {
    const dims: [number, number, number] = [4, 4, 3]
    const rle = maskToBboxRLE(new Uint8Array([1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1]), 4, 4)!
    const base = { ts_ms: 0, bbox: rle.bbox, rle: rle.rle, occluded: false }
    const vol = keyframesToMaskVolume(
      [
        { ...base, frame: 0, outside: false },
        { ...base, frame: 1, outside: true }, // 带 RLE 的 outside 帧:必须被跳过
      ],
      dims,
    )
    expect(annotatedSlices(vol, dims).map((s) => s.z)).toEqual([0]) // 只有第 0 层有像素
  })
})
