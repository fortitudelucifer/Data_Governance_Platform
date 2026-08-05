// WSI 标注叠加层的**坐标同步数学**(C1.3a)。这是病理标注最容易出静默错的地方:
// 叠加层与瓦片一旦不同步,画的框看着正常却**落在错的细胞上**,不报任何错(同
// 坐标系纪律)。所以抽成纯函数单测 + 变异锁死(与 slideTileSource、mprGeometry 同处理)。
//
// # 模型
//
// 几何一律存**原生 level-0 像素坐标**(WSI 的"显示像素空间")——OSD 的 tiledImage
// 源尺寸就是 level-0(46000×32914),所以 image 坐标 = level-0 像素,和 bbox/polygon
// 轨迹的 Keyframe.Points 同一个空间,导出/复核不用再换算。
//
// 屏幕(viewer element 像素)↔ image 是一个**仿射:各向同性缩放 + 平移**(我们的
// 查看器禁用了旋转,没有 rotation/flip 项):
//     screen = image * scale + offset
//
// 每帧从 OSD 采样两点(image (0,0) 与 (1,0) 的 viewer-element 像素)反解出这个仿射,
// 叠加层的 <g> 只更新这一个 transform,形状坐标恒在 image 空间——重画零成本,且
// **用 OSD 自己的坐标换算作为唯一真源**,不另算一套(另算必漂)。

export interface OverlayAffine {
  /** viewer-element 像素 / image 像素(各向同性)。 */
  scale: number
  /** image (0,0) 的屏幕 x。 */
  ox: number
  /** image (0,0) 的屏幕 y。 */
  oy: number
}

/** image 坐标 → 屏幕(viewer element)像素。 */
export function imageToScreen(a: OverlayAffine, x: number, y: number): [number, number] {
  return [x * a.scale + a.ox, y * a.scale + a.oy]
}

/** 屏幕(viewer element)像素 → image 坐标。imageToScreen 的逆。 */
export function screenToImage(a: OverlayAffine, sx: number, sy: number): [number, number] {
  return [(sx - a.ox) / a.scale, (sy - a.oy) / a.scale]
}

/**
 * 由 image (0,0) 与 image (1,0) 的屏幕像素反解仿射。
 *
 * scale 取 x 方向单位向量的屏幕长度(无旋转 → image x 轴平行屏幕 x 轴)。OSD 视口
 * 是各向同性缩放,x/y 尺度相等,所以一个 scale 足够;调用方可另取 (0,1) 断言 y 尺度
 * 一致(测试里做了)。
 *
 * @throws 若两点重合(scale=0,除零)——视口未就绪时不该调。
 */
export function recoverAffine(p00: { x: number; y: number }, p10: { x: number; y: number }): OverlayAffine {
  const scale = p10.x - p00.x
  if (!Number.isFinite(scale) || scale === 0) {
    throw new Error(`叠加层仿射反解失败:image x 单位向量屏幕长度为 ${scale}(视口未就绪?)`)
  }
  return { scale, ox: p00.x, oy: p00.y }
}

/** SVG <g> 的 transform 串:matrix(scale,0,0,scale,ox,oy)。 */
export function affineToMatrix(a: OverlayAffine): string {
  return `matrix(${a.scale},0,0,${a.scale},${a.ox},${a.oy})`
}
