// windowLevel.ts — 16-bit 体素 → 8-bit 显示的窗宽/窗位映射(执行方案-04 · C3.2).
//
// 这是 C0.5 "位深选 16-bit" 决策的兑现:切片以 16-bit PNG 送到浏览器,窗宽窗位
// **在客户端实时应用**(拖一下就换显示,不重新生成派生物)。映射用 DICOM
// PS3.3 C.11.2.1.2 的 LINEAR 函数——放射科医生在自家 PACS 上看到的就是这个公式,
// 跨软件一致(和视频侧"导出的框=画布的框"同一种纪律:不自己发明映射)。
//
// 数据流:PNG 像素 p ∈ [0,65535] → 真值 r = p*sliceSclSlope + sliceSclInter
//   (来自 volume_meta,把有符号数据的上移折进了线性映射)→ 显示 d = wl(r,W,C).

/**
 * DICOM LINEAR 窗位映射:真值 real → 显示灰阶 [0,255].
 * lo/hi 是窗的两端;窗外钳到 0/255,窗内线性。
 */
export function applyWindowLevel(real: number, width: number, center: number): number {
  const w = Math.max(width, 1) // DICOM 要求 W>=1;W<=0 无意义
  if (w <= 1) {
    // 退化为阈值:中心处一刀切(避免除以 W-1=0)。
    return real < center ? 0 : 255
  }
  const lo = center - 0.5 - (w - 1) / 2
  const hi = center - 0.5 + (w - 1) / 2
  if (real <= lo) return 0
  if (real > hi) return 255
  const d = ((real - (center - 0.5)) / (w - 1) + 0.5) * 255
  return Math.round(clamp(d, 0, 255))
}

/**
 * buildWindowLUT precomputes PNG-pixel → display for a fixed (window, level,
 * slice scaling). Rendering a whole slice is then one array lookup per pixel
 * (`display[i] = lut[pixel[i]]`) — the only way per-frame windowing stays
 * interactive on a 512² slice. 65536 entries = one uint8 per possible pixel.
 */
export function buildWindowLUT(
  width: number,
  center: number,
  sliceSclSlope: number,
  sliceSclInter: number,
): Uint8Array {
  const lut = new Uint8Array(65536)
  for (let p = 0; p < 65536; p++) {
    const real = p * sliceSclSlope + sliceSclInter
    lut[p] = applyWindowLevel(real, width, center)
  }
  return lut
}

function clamp(x: number, lo: number, hi: number): number {
  return x < lo ? lo : x > hi ? hi : x
}
