// png16.ts — 16-bit 灰度 PNG 解码(执行方案-04 · C3.2b).
//
// **为什么必须自己解码**:浏览器把 PNG 交给 <img>/canvas 时,canvas 2D 每通道
// 只有 8-bit——getImageData 读回来的 16-bit 数据已经被截断了,C0.5 的位深保真
// 当场作废,而且**不报任何错**(画面看着正常,HU 值全错)。所以体切片走这里
// 手工解码,拿到真正的 Uint16Array 再交给 mprRender 的 LUT 映射。
//
// 作用域刻意窄:只解后端 encodeSlicePNG16 产出的形状——**灰度(color type 0)、
// 位深 16、非隔行**。遇到别的形状直接抛错,不猜、不将就(猜错就是静默错误)。
// 与 Go 编码器锁在同一份夹具上:testdata/volume/gray16_ramp.png,两端各测一半
// (见 backend/internal/service/volume_slice_fixture_test.go)。

export interface Gray16Image {
  width: number
  height: number
  pixels: Uint16Array // 行优先,x 最快,长度 width*height
}

const PNG_MAGIC = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]

/** 解码 16-bit 灰度 PNG。输入是整个 .png 文件字节。 */
export async function decodeGray16PNG(bytes: Uint8Array): Promise<Gray16Image> {
  if (bytes.length < 8 || PNG_MAGIC.some((b, i) => bytes[i] !== b)) {
    throw new Error('png16: not a PNG (bad magic)')
  }
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  let off = 8
  let width = 0
  let height = 0
  const idatParts: Uint8Array[] = []

  while (off + 8 <= bytes.length) {
    const len = view.getUint32(off)
    const type = String.fromCharCode(bytes[off + 4], bytes[off + 5], bytes[off + 6], bytes[off + 7])
    const dataStart = off + 8
    if (type === 'IHDR') {
      width = view.getUint32(dataStart)
      height = view.getUint32(dataStart + 4)
      const bitDepth = bytes[dataStart + 8]
      const colorType = bytes[dataStart + 9]
      const interlace = bytes[dataStart + 12]
      // 只认后端产出的那一种形状;其余一律拒绝而不是猜。
      if (bitDepth !== 16) throw new Error(`png16: bit depth ${bitDepth}, want 16`)
      if (colorType !== 0) throw new Error(`png16: color type ${colorType}, want 0 (grayscale)`)
      if (interlace !== 0) throw new Error('png16: interlaced PNG not supported')
    } else if (type === 'IDAT') {
      idatParts.push(bytes.subarray(dataStart, dataStart + len))
    } else if (type === 'IEND') {
      break
    }
    off = dataStart + len + 4 // + CRC
  }
  if (width === 0 || height === 0) throw new Error('png16: missing IHDR')
  if (idatParts.length === 0) throw new Error('png16: missing IDAT')

  const raw = await inflate(concat(idatParts))
  return unfilterGray16(raw, width, height)
}

/** zlib 解压(PNG 的 IDAT 是 zlib 包装的 deflate)。 */
async function inflate(data: Uint8Array): Promise<Uint8Array> {
  const ds = new DecompressionStream('deflate')
  const stream = new Blob([data as BlobPart]).stream().pipeThrough(ds)
  const buf = await new Response(stream).arrayBuffer()
  return new Uint8Array(buf)
}

function concat(parts: Uint8Array[]): Uint8Array {
  const total = parts.reduce((n, p) => n + p.length, 0)
  const out = new Uint8Array(total)
  let at = 0
  for (const p of parts) {
    out.set(p, at)
    at += p.length
  }
  return out
}

/**
 * 逐扫描线去滤波并读出 big-endian 16-bit 样本。
 * PNG 每行前置一个 filter 字节(0..4);Go 的编码器是按行择优的,所以五种都要实现
 * ——少实现一种,遇到就会解出**看着像图但像素全错**的结果。
 * 灰度16 的 bpp = 2 字节。
 */
function unfilterGray16(raw: Uint8Array, width: number, height: number): Gray16Image {
  const bpp = 2
  const stride = width * bpp
  const expected = height * (stride + 1)
  if (raw.length < expected) {
    throw new Error(`png16: inflated ${raw.length} bytes, want ${expected}`)
  }
  const lines = new Uint8Array(height * stride) // 去滤波后的原始字节
  for (let y = 0; y < height; y++) {
    const ft = raw[y * (stride + 1)]
    const src = y * (stride + 1) + 1
    const dst = y * stride
    const up = dst - stride
    for (let x = 0; x < stride; x++) {
      const rawByte = raw[src + x]
      const a = x >= bpp ? lines[dst + x - bpp] : 0 // 左
      const b = y > 0 ? lines[up + x] : 0 // 上
      const c = y > 0 && x >= bpp ? lines[up + x - bpp] : 0 // 左上
      let val: number
      switch (ft) {
        case 0:
          val = rawByte
          break
        case 1:
          val = rawByte + a
          break
        case 2:
          val = rawByte + b
          break
        case 3:
          val = rawByte + ((a + b) >> 1)
          break
        case 4:
          val = rawByte + paeth(a, b, c)
          break
        default:
          throw new Error(`png16: unknown filter type ${ft} on row ${y}`)
      }
      lines[dst + x] = val & 0xff
    }
  }
  // 16-bit 样本是 **big-endian**(PNG 规范),别按小端读——读反了值全错但图还"像图"。
  const pixels = new Uint16Array(width * height)
  for (let p = 0; p < pixels.length; p++) {
    pixels[p] = (lines[p * 2] << 8) | lines[p * 2 + 1]
  }
  return { width, height, pixels }
}

function paeth(a: number, b: number, c: number): number {
  const p = a + b - c
  const pa = Math.abs(p - a)
  const pb = Math.abs(p - b)
  const pc = Math.abs(p - c)
  if (pa <= pb && pa <= pc) return a
  if (pb <= pc) return b
  return c
}
