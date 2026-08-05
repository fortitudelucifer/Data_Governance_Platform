import { paintBrush, paintBrushLine } from './maskSlice'
import type { Vec3 } from './mprGeometry'

// 标注草稿的**操作日志**(执行方案-00 契约 · C0.6)。
//
// 契约原话:"笔画操作日志落 IndexedDB(本地自动草稿,逐笔即时)+ 每 5 分钟后台
// flush 到服务端草稿前缀,Ctrl+Z 回放同一份日志——**三个需求一份机制**"。
// 验收:勾画 30 分钟 → 强杀浏览器 → 重开 → 恢复到最后一笔。
//
// ## 为什么记"操作"而不是"结果"
//
// 逐体素勾画是小时级工作,而整卷掩膜是 11 MB 量级。每笔都存整卷 = 写爆
// IndexedDB 且卡死交互;只在保存时存结果 = 崩溃就丢几小时。记操作两头都便宜:
// 一笔 dab 是几十字节,回放一遍是纯计算。撤销也只是"少放最后一段"——同一份日志。
//
// ## 最关键的一条:实时路径与回放路径**必须是同一个函数**
//
// 恢复的正确性 = "回放出来的掩膜 ≡ 崩溃前的掩膜"。若页面用 A 改掩膜、回放用 B,
// 两边任何一点不一致(取整、边界、擦除语义)都会让恢复出来的东西**看着像模像样
// 但和标注员画的不是一个东西**——本项目最贵的那类 bug(不报错、内容错)。
// 所以页面**也**必须经 `applyOp` 改掩膜,不许自己直接动 buffer。
// 两条路径同码 → 漂移不是"靠自觉避免",是**编不出来**。
// 单测里有一条性质测试盯着这件事(实时逐笔 vs 从空回放,逐字节相等)。

/** 一次笔刷落点(mousedown/mousemove 的每个采样点)。 */
export interface DabOp {
  op: 'dab'
  /** 段的稳定 uid(不是 trackId——新建段还没有 trackId)。 */
  seg: string
  z: number
  u: number
  v: number
  /** 笔刷半径 */
  r: number
  erase: boolean
  /** 上一采样点(拖动中);缺省 = 一笔的起点,只落单个圆盘。有则沿线栅格化补空洞(#20)。 */
  pu?: number
  pv?: number
}

/** 把第 z 层复制到上下各 span 层。 */
export interface PropagateOp {
  op: 'propagate'
  seg: string
  z: number
  span: number
}

/** 清空第 z 层。 */
export interface ClearSliceOp {
  op: 'clearSlice'
  seg: string
  z: number
}

/**
 * 新建一个段。
 *
 * ⚠️ **这条不能省，它是"新建段 → 画半小时 → 崩溃"这个最常见场景的全部依赖**。
 * 只记涂抹不记建段，恢复时那个段在服务端根本不存在，每一笔都会落到"段不存在"
 * 上被跳过——横幅欢快地说"检测到 12 笔未保存的勾画"，点恢复什么也没有，
 * 且不报任何错。真浏览器崩溃测试就是这么逮到它的。
 */
export interface AddSegOp {
  op: 'addSeg'
  seg: string
  label: string
  color: string
}

/** 改名 / 改色。丢了不致命，但"恢复回来叫回分割 1"对标注员是实打实的返工。 */
export interface PatchSegOp {
  op: 'patchSeg'
  seg: string
  label?: string
  color?: string
}

export type DraftOp = DabOp | PropagateOp | ClearSliceOp | AddSegOp | PatchSegOp

/** 回放/实时共用的状态容器：掩膜体 + 段元信息。 */
export interface DraftState {
  masks: Map<string, Uint8Array>
  segs: Map<string, { label: string; color: string }>
}

/**
 * 一次草稿快照。**baseline 不能省**:
 * 段可能是从服务端载入的(已有 trackId + version),此时"从空回放"是错的——
 * 必须先按 baseline 载入服务端那一份,再把 ops 叠上去。恢复时若服务端 version
 * 已经变了(别人存过),不能静默回放,得让用户知道。
 */
export interface DraftRecord {
  taskId: string
  assetId: string
  /** 段 uid → 该段开始记录时的服务端身份;`null` = 本地新建、服务端还没有。 */
  baseline: Record<string, { trackId: string; version: number } | null>
  ops: DraftOp[]
  /** 最后一次落盘时间(ms),用于"恢复到 X 分钟前"的提示文案。 */
  savedAt: number
}

/**
 * 把一个操作作用到掩膜体上（**就地**修改）。
 *
 * 实时涂抹与崩溃回放共用此函数——见文件头注释。返回是否真的改到了东西
 * （段不存在时返回 false，回放遇到已删段可据此跳过而不是崩掉）。
 */
export function applyOp(state: DraftState, dims: Vec3, op: DraftOp): boolean {
  const { masks, segs } = state
  const [nx, ny, nz] = dims
  const planeSize = nx * ny

  // 段生命周期的两条不碰掩膜内容，先处理（它们没有 z）。
  if (op.op === 'addSeg') {
    if (masks.has(op.seg)) return false // 幂等：重复回放不该把已画的内容清零
    masks.set(op.seg, new Uint8Array(nx * ny * nz))
    segs.set(op.seg, { label: op.label, color: op.color })
    return true
  }
  if (op.op === 'patchSeg') {
    const cur = segs.get(op.seg)
    if (!cur) return false
    segs.set(op.seg, { label: op.label ?? cur.label, color: op.color ?? cur.color })
    return true
  }

  const mask = masks.get(op.seg)
  if (!mask) return false
  if (op.z < 0 || op.z >= nz) return false

  switch (op.op) {
    case 'dab': {
      const slice = mask.subarray(op.z * planeSize, (op.z + 1) * planeSize)
      // #20 有上一采样点(拖动中)→ 沿线连续落笔补空洞;否则(一笔起点)单个圆盘。
      if (op.pu !== undefined && op.pv !== undefined) {
        paintBrushLine(slice, nx, ny, op.pu, op.pv, op.u, op.v, op.r, op.erase)
      } else {
        paintBrush(slice, nx, ny, op.u, op.v, op.r, op.erase)
      }
      return true
    }

    case 'propagate': {
      // 用 slice(拷贝)而不是 subarray(视图)取源。
      // ⚠️ 诚实说明:**当前语义下两者等价**——只写 z±d、从不写 z 本身,视图不会
      // 被边写边读污染(变异测试证实:换成 subarray 全部测试仍绿)。这里拷贝是
      // **防御性**的:哪天 span 的语义改成含 z、或改成累积传播,视图版会当场出
      // 一个"不报错但内容错"的 bug。不写成"必须用 slice 否则会污染"——那是假
      // 注释,而假注释比没注释更糟(CLAUDE.md 头条教训)。
      const src = mask.slice(op.z * planeSize, (op.z + 1) * planeSize)
      for (let d = 1; d <= op.span; d++) {
        for (const z of [op.z - d, op.z + d]) {
          if (z < 0 || z >= nz) continue
          mask.set(src, z * planeSize)
        }
      }
      return true
    }

    case 'clearSlice':
      mask.fill(0, op.z * planeSize, (op.z + 1) * planeSize)
      return true
  }
}

/**
 * 从给定基线掩膜出发回放整份日志（**就地**修改传入的掩膜）。
 *
 * 传进来的 masks 应当已经按 baseline 载入服务端那一份；本函数只负责叠加。
 * 遇到已不存在的段跳过而不是抛错——段可能在崩溃前就被删了，删除本身不进
 * 操作日志（删段走服务端，不是草稿的职责）。
 */
export function replay(state: DraftState, dims: Vec3, ops: DraftOp[]): number {
  let applied = 0
  for (const op of ops) if (applyOp(state, dims, op)) applied++
  return applied
}

/**
 * 撤销/重做的实现：把状态复位到基线，再回放前 n 条操作。
 *
 * 契约要求"Ctrl+Z 回放同一份日志"——撤销不是"求每个操作的逆"（笔刷的逆需要
 * 记住被覆盖的每个体素，成本和复杂度都高），而是**少放几条重来**。
 *
 * ⚠️ **就地复位，绝不新建缓冲区**：`mask.set(baseline)` 而不是
 * `new Uint8Array(baseline)`。掩膜体是 11 MB 量级，换新缓冲区会让持有它的
 * 引用全部失效，而且新数组一旦参与 React 的 prop 比较就会**把整个渲染进程
 * 卡死**（CLAUDE.md 记着这个坑）。身份恒定 → 只有内容变，谁都不用重新拿引用。
 *
 * `baseline` 里没有的段（= 崩溃/编辑期间新建的）复位成全零；回放里的 addSeg
 * 会把它们重新建出来。
 */
export function rebuildInto(state: DraftState, dims: Vec3, baseline: Map<string, Uint8Array>, ops: DraftOp[]): number {
  for (const [uid, mask] of state.masks) {
    const base = baseline.get(uid)
    if (base) mask.set(base) // 就地覆盖回基线
    else mask.fill(0) // 编辑期间新建的段：回到空白
  }
  // 段元信息也要复位，否则撤销掉一次改名后名字还留着。
  for (const uid of [...state.segs.keys()]) if (!baseline.has(uid)) state.segs.delete(uid)
  return replay(state, dims, ops)
}

/**
 * 日志压紧：丢掉**被后续 clearSlice 完全覆盖**的历史操作。
 *
 * 逐笔记录半小时后日志会有几万条。压紧只做一条**保守且可证**的规则：
 * 某段某层的 clearSlice 之前，该段该层的所有操作都无意义。
 * propagate 会写别的层，所以它**不能**被当作"只影响 op.z"来裁——
 * 一旦把它按单层裁掉，回放结果就和实时结果不一样了（性质测试会逮到）。
 */
export function compact(ops: DraftOp[]): DraftOp[] {
  const keep: DraftOp[] = []
  // 从后往前扫：记住每个 (seg,z) 上是否已经遇到过 clearSlice。
  const cleared = new Set<string>()
  for (let i = ops.length - 1; i >= 0; i--) {
    const op = ops[i]
    // 段生命周期的操作**永不压缩**：丢掉 addSeg，那个段连同它的全部勾画就没了。
    if (op.op === 'addSeg' || op.op === 'patchSeg') {
      keep.push(op)
      continue
    }
    const key = `${op.seg}:${op.z}`
    if (op.op === 'propagate') {
      // 保守：propagate 写的是别的层，不参与单层裁剪，且它之前的操作也不能裁
      // （它要读 op.z 的内容）。遇到它就把该段所有 cleared 标记作废。
      for (const k of [...cleared]) if (k.startsWith(`${op.seg}:`)) cleared.delete(k)
      keep.push(op)
      continue
    }
    if (cleared.has(key)) continue // 后面有 clearSlice 把这层清了，这条无意义
    keep.push(op)
    if (op.op === 'clearSlice') cleared.add(key)
  }
  return keep.reverse()
}
