import { planeToVoxel, type MPRPlane, type Vec3 } from './mprGeometry'

// 测量工具的纯数学核心（执行方案-04 · C3.25）。
//
// 放射科医生每天用得最多的不是画笔，是尺子：RECIST 用最长径判断肿瘤是缓解还是
// 进展，直接决定治疗方案改不改。所以这里算出来的毫米数不是"参考值"，是会进
// 报告的数。
//
// # 这个文件唯一的危险：拿屏幕像素当毫米
//
// 体素常常是**各向异性**的——CT 常见 0.7×0.7×5.0 mm。同样"屏幕上 100 像素长"
// 的一条线，横着量和竖着量的物理长度可能差 7 倍。而错误的结果长得完全正常：
// 一个三位数的毫米值，没有任何东西会报错。
//
// 因此：
//   · 一切测量都在**体素索引空间**取点，换算时乘上该轴的 spacing；
//   · 面内两轴的 spacing 由 `planeToVoxel` 反推，**与重切共用同一套轴映射**，
//     不在这里另写一份 switch（两处各写各的，迟早漂）。
//
// # 为什么不做"屏幕坐标 → 毫米"的快捷方式
//
// 画布有缩放、有朝向翻转（mprOrientation）。任何绕过体素空间的换算都要把这些
// 一并算进去，而漏掉其中一项不会崩溃，只会让数字悄悄偏掉。

/** 面内一点（体素索引空间的 u,v；与 MPRView 的 onPick 一致）。 */
export interface PlanePoint {
  u: number
  v: number
}

/** 一次长度测量。 */
export interface RulerMeasurement {
  kind: 'ruler'
  plane: MPRPlane
  slice: number
  a: PlanePoint
  b: PlanePoint
  /** 物理长度（mm），由 spacing 换算。 */
  lengthMm: number
}

/** 一次角度测量：顶点在 b。 */
export interface AngleMeasurement {
  kind: 'angle'
  plane: MPRPlane
  slice: number
  a: PlanePoint
  b: PlanePoint
  c: PlanePoint
  /** 夹角（度，0–180）。 */
  degrees: number
}

export type Measurement = RulerMeasurement | AngleMeasurement

/**
 * 求某个平面的面内两轴各自对应的体素轴 spacing。
 *
 * **不在这里写 switch**：用 `planeToVoxel` 探两个单位步长，看体素坐标动了哪一维。
 * 重切、拾取、测量因此共用同一套轴映射——哪天平面定义改了，三处一起变，
 * 不会出现"画面对、尺子错"这种只影响数字的漂移。
 */
export function planeSpacing(plane: MPRPlane, spacing: Vec3): [number, number] {
  const o = planeToVoxel(plane, 0, 0, 0)
  const du = planeToVoxel(plane, 0, 1, 0)
  const dv = planeToVoxel(plane, 0, 0, 1)
  const axisOf = (p: Vec3): number => {
    for (let i = 0; i < 3; i++) if (p[i] !== o[i]) return i
    return 0
  }
  return [spacing[axisOf(du)], spacing[axisOf(dv)]]
}

/**
 * 面内两点的物理距离（mm）。
 *
 * 各向异性下必须**逐轴**乘 spacing 再求模，不能先求像素距离再乘一个"平均
 * spacing"——后者在斜线上永远是错的，且错得看不出来。
 */
export function distanceMm(a: PlanePoint, b: PlanePoint, plane: MPRPlane, spacing: Vec3): number {
  const [su, sv] = planeSpacing(plane, spacing)
  const du = (b.u - a.u) * su
  const dv = (b.v - a.v) * sv
  return Math.hypot(du, dv)
}

/**
 * 三点夹角（顶点在 b），单位度。
 *
 * 同样先把两条边换算到**物理空间**再求角：各向异性下，体素空间里的 90° 在
 * 真实解剖上并不是 90°。医生量的是解剖角度（例如股骨颈干角），不是数组下标的角度。
 */
export function angleDeg(a: PlanePoint, b: PlanePoint, c: PlanePoint, plane: MPRPlane, spacing: Vec3): number {
  const [su, sv] = planeSpacing(plane, spacing)
  const v1 = { x: (a.u - b.u) * su, y: (a.v - b.v) * sv }
  const v2 = { x: (c.u - b.u) * su, y: (c.v - b.v) * sv }
  const n1 = Math.hypot(v1.x, v1.y)
  const n2 = Math.hypot(v2.x, v2.y)
  if (n1 === 0 || n2 === 0) return 0 // 退化：两点重合，没有角度可言
  let cos = (v1.x * v2.x + v1.y * v2.y) / (n1 * n2)
  cos = Math.min(1, Math.max(-1, cos)) // 浮点误差可能让 cos 越界 → acos 得 NaN
  return (Math.acos(cos) * 180) / Math.PI
}

/**
 * 长度的显示文案。
 *
 * 医学报告里长度用 mm、保留一位小数（RECIST 1.1 的惯例）；超过 100mm 仍用 mm
 * 而不换算成 cm——报告是按 mm 记的，换单位只会给读数的人添一次心算。
 */
export function formatMm(mm: number): string {
  return `${mm.toFixed(1)} mm`
}

export function formatDeg(deg: number): string {
  return `${deg.toFixed(1)}°`
}

/**
 * RECIST 1.1 的靶病灶可测量性阈值。
 *
 * 低于阈值的病灶按指南不可作为靶病灶（CT 常规层厚下长径 <10mm）。这里只做
 * **提示**、不做拦截：阈值依赖层厚与病灶类型（淋巴结用短径 ≥15mm），把它写死
 * 成硬规则会在别的场景下拦错东西。判断是医生的事，工具只负责把数摆出来。
 */
export const RECIST_MIN_TARGET_MM = 10

/** 单次测量的一行摘要（面板/导出共用，避免两处各写各的格式）。 */
export function describeMeasurement(m: Measurement): string {
  const where = `${planeLabel(m.plane)} 第 ${m.slice + 1} 层`
  return m.kind === 'ruler'
    ? `${where} · 长度 ${formatMm(m.lengthMm)}`
    : `${where} · 角度 ${formatDeg(m.degrees)}`
}

function planeLabel(p: MPRPlane): string {
  return p === 'axial' ? '轴位' : p === 'coronal' ? '冠状' : '矢状'
}
