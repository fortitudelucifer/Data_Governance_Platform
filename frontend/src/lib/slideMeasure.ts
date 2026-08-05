// WSI 测量的纯数学(C1.35)。危险和 C3.25(体数据尺子)、C1.1a(探针 mpp)是同一类:
// **拿像素当微米**——错的读数长得完全正常,会进病理报告。
//
// 两条纪律:
//   ① WSI 面内像素是**方的**(SVS 用单一 `MPP` = µm/px),所以物理长度 =
//      level-0 像素距离 × mpp。几何本就存 level-0 像素(与瓦片/导出同空间),
//      不碰屏幕缩放——绕过 level-0 空间的换算都要把缩放/朝向算进去,漏一项就悄悄偏。
//   ② **mpp = 0 是"未知"**(C1.1a:探针拿不到 MPP 时置 0,绝不默认 1.0)→ 测量必须
//      **禁用**,返回 null,绝不用一个编出来的标尺给出看着合理的读数。

/**
 * 两点(level-0 像素)间的物理长度,单位 µm。
 *
 * @returns µm 长度;若 mpp 未知(<=0)返回 null——调用方据此禁用测量,别显示假读数。
 */
export function lengthMicrons(ax: number, ay: number, bx: number, by: number, mpp: number): number | null {
  if (!(mpp > 0)) return null
  return Math.hypot(bx - ax, by - ay) * mpp
}

/**
 * 长度显示文案。病理常用 µm(细胞/腺体尺度);≥1000µm 换 mm 更好读(大区域)。
 * 分界点固定在 1000µm——跨界换单位只影响显示,不影响存的数(µm)。
 */
export function formatMicrons(um: number): string {
  return um >= 1000 ? `${(um / 1000).toFixed(2)} mm` : `${um.toFixed(1)} µm`
}
