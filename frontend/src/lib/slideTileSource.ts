import type { SlideMeta } from '@/api/slide'

// WSI 瓦片源的**层级映射数学**(C1.2c)。这是整个查看器最容易出静默错的地方:
// 映错了 DZI 层↔原生层,瓦片会取到别处组织,画面看着像模像样却是错的。所以
// 抽成纯函数单独测(与插值契约 trackInterpolation、测量 measure 同一处理)。
//
// # 为什么用 DZI 层模型 + tileExists 门,而不是硬塞原生层
//
// OpenSeadragon 的层级模型是标准 DZI:level 从 0(最粗)到 maxLevel(全分辨率),
// **每层 /2**。而原生金字塔只有 3 层(×1/×4/×16,见 C1.1c 实测),中间的 /2、/8
// 层没有原生瓦片。
//
// 两条路:
//   (a) 硬把原生 3 层当成 OSD 的 3 层 → 要覆写 OSD 的 getLevelScale/getBestLevel,
//       踩 OSD 内部选层逻辑,版本一变就崩(OSD 内部好几处假设 /2)。
//   (b) **按标准 DZI 层呈现,但只有原生层对应的 DZI 层有真瓦片;其余 DZI 层
//       用 `tileExists=false` 关掉,OSD 自动从最近的粗层放大填充**——零服务端计算,
//       且不碰 OSD 内部。← 采用这条。
//
// 关键前提(真实样本证实):Aperio 的降采样是 **2 的幂**(×4=2²、×16=2⁴),所以
// 原生层尺寸精确落在 DZI 层上(全宽/2ⁿ)。11500 = 46000/4 = 全宽/2² = DZI 的
// (maxLevel-2) 层,严丝合缝。非 2 的幂时四舍五入到最近 DZI 层——最坏是该原生层
// 不被用、OSD 从别层放大,**不会取错瓦片**(tileExists 门兜底,绝不 404)。

export interface SlideTileSourceSpec {
  width: number
  height: number
  tileSize: number
  /** DZI 最大层(= 全分辨率层)。 */
  maxLevel: number
  /**
   * DZI 最小层 = **最粗原生层**对应的 DZI 层,不是 0。
   *
   * ⚠️ 这条踩过:设成 0 时,fit-zoom 下 OSD 想要一个很粗的层(如 DZI 10),而我们
   * 只有 12/14/16 有原生瓦片。OSD 的 tileExists 回退是**往更粗的层找**(往下),
   * 一路到 0 全是 false → 什么都不加载、也不报错。把 minLevel 钉在最粗原生层,
   * OSD 就把 fit-zoom 夹到这一层(存在),从它缩放填充更粗的视图。
   */
  minLevel: number
  /** DZI level → 原生 level 索引。只含有真瓦片的那几层。 */
  dziToNative: Map<number, number>
}

/**
 * 由 slide_meta 构建瓦片源规格。
 *
 * @throws 如果没有金字塔层(不是 WSI)。
 */
export function buildSlideTileSource(meta: SlideMeta): SlideTileSourceSpec {
  if (!meta.levels || meta.levels.length === 0) {
    throw new Error('slide 没有金字塔层')
  }
  const width = meta.width
  const height = meta.height
  // 瓦片尺寸取 level0(SVS 各层同尺寸;CMU-1 是 240,大切片是 256)。
  const tileSize = meta.levels[0].tile_width || 256

  // DZI 最大层 = 覆盖最大边所需的层数。level maxLevel 的边长 = 2^maxLevel。
  const maxDim = Math.max(width, height)
  const maxLevel = Math.ceil(Math.log2(maxDim))

  // 每个原生层按其 downsample 映射到最近的 DZI 层。
  const dziToNative = new Map<number, number>()
  meta.levels.forEach((lv, nativeIdx) => {
    const ds = lv.downsample > 0 ? lv.downsample : 1
    // downsample = 2^k → 该层在 DZI 的 (maxLevel - k) 层。
    const k = Math.round(Math.log2(ds))
    const dziLevel = maxLevel - k
    if (dziLevel >= 0) {
      // 若两个原生层四舍五入到同一 DZI 层(异常金字塔),保留更精细的(downsample 小的)。
      const existing = dziToNative.get(dziLevel)
      if (existing === undefined || meta.levels[existing].downsample > ds) {
        dziToNative.set(dziLevel, nativeIdx)
      }
    }
  })

  if (dziToNative.size === 0) {
    throw new Error('slide 层映射为空（downsample 数据异常？）')
  }
  const minLevel = Math.min(...dziToNative.keys())
  return { width, height, tileSize, maxLevel, minLevel, dziToNative }
}

/** 某 DZI 层在给定原生层下的瓦片网格(列数, 行数)。 */
export function tileGridAt(meta: SlideMeta, nativeIdx: number, tileSize: number): { cols: number; rows: number } {
  const lv = meta.levels[nativeIdx]
  return {
    cols: Math.ceil(lv.width / tileSize),
    rows: Math.ceil(lv.height / tileSize),
  }
}

/**
 * 某 (dziLevel, col, row) 是否有真瓦片。
 *
 * 两条都要满足:① 该 DZI 层对应一个原生层;② (col,row) 落在该原生层的瓦片网格内。
 * 不满足则返回 false,OSD 会从最近的粗层放大填充——**绝不请求不存在的瓦片**。
 */
export function tileExistsAt(spec: SlideTileSourceSpec, meta: SlideMeta, dziLevel: number, col: number, row: number): boolean {
  const nativeIdx = spec.dziToNative.get(dziLevel)
  if (nativeIdx === undefined) return false
  const { cols, rows } = tileGridAt(meta, nativeIdx, spec.tileSize)
  return col >= 0 && row >= 0 && col < cols && row < rows
}
