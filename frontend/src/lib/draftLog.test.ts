import { describe, expect, it } from 'vitest'

import { applyOp, compact, rebuildInto, replay, type DraftOp, type DraftState } from './draftLog'
import type { Vec3 } from './mprGeometry'

// C0.6 草稿日志。**这里唯一真正重要的断言是"回放 ≡ 实时"**:恢复出来的掩膜
// 若与崩溃前不是逐字节相同,标注员看到的是一份"像模像样但不是自己画的"东西,
// 而且不报任何错——本项目最贵的那类 bug。其余测试都是在给这条护航。

const dims: Vec3 = [16, 12, 8]
const N = dims[0] * dims[1] * dims[2]
const fresh = (): DraftState => ({
  masks: new Map([['A', new Uint8Array(N)]]),
  segs: new Map([['A', { label: 'A', color: '#f00' }]]),
})

/** 确定性伪随机:测试必须可复现,崩了要能照着种子复算。 */
function lcg(seed: number) {
  let s = seed >>> 0
  return () => ((s = (s * 1664525 + 1013904223) >>> 0) / 2 ** 32)
}

function randomOps(seed: number, n: number): DraftOp[] {
  const rnd = lcg(seed)
  const ops: DraftOp[] = []
  for (let i = 0; i < n; i++) {
    const z = Math.floor(rnd() * dims[2])
    const roll = rnd()
    if (roll < 0.8) {
      ops.push({
        op: 'dab',
        seg: 'A',
        z,
        // 刻意让坐标**越界**一部分:笔刷压边是真实操作,边界处理若两条路径
        // 不一致,恰恰在这里显形。
        u: Math.floor(rnd() * (dims[0] + 6)) - 3,
        v: Math.floor(rnd() * (dims[1] + 6)) - 3,
        r: 1 + Math.floor(rnd() * 5),
        erase: rnd() < 0.25,
      })
    } else if (roll < 0.93) {
      ops.push({ op: 'propagate', seg: 'A', z, span: 1 + Math.floor(rnd() * 3) })
    } else {
      ops.push({ op: 'clearSlice', seg: 'A', z })
    }
  }
  return ops
}

describe('回放 ≡ 实时(C0.6 的核心性质)', () => {
  it.each([1, 42, 1337, 20260719])('种子 %i:逐笔实时应用与从空回放,逐字节相同', (seed) => {
    const ops = randomOps(seed, 400)

    // ① 实时:一边产生操作一边改掩膜(模拟标注员真在画)
    const live = fresh()
    for (const op of ops) applyOp(live, dims, op)

    // ② 回放:崩溃后重开,从空白按日志重放
    const restored = fresh()
    const applied = replay(restored, dims, ops)

    expect(applied).toBe(ops.length)
    expect(restored.masks.get('A')!).toEqual(live.masks.get('A')!)
    // 防空断言:这批操作必须真的画出了东西,否则"两边都是全零"也会相等。
    expect(live.masks.get('A')!.some((v) => v === 1)).toBe(true)
  })

  it('压紧后回放仍与实时相同(压紧不能改变语义)', () => {
    const ops = randomOps(777, 500)
    const live = fresh()
    for (const op of ops) applyOp(live, dims, op)

    const packed = compact(ops)
    const restored = fresh()
    replay(restored, dims, packed)

    expect(restored.masks.get('A')!).toEqual(live.masks.get('A')!)
    // 压紧要真的压掉东西,否则这条测试是空的。
    expect(packed.length).toBeLessThan(ops.length)
  })
})

describe('compact', () => {
  it('clearSlice 之前、同段同层的操作被丢弃', () => {
    const ops: DraftOp[] = [
      { op: 'dab', seg: 'A', z: 3, u: 5, v: 5, r: 2, erase: false },
      { op: 'dab', seg: 'A', z: 3, u: 6, v: 6, r: 2, erase: false },
      { op: 'clearSlice', seg: 'A', z: 3 },
      { op: 'dab', seg: 'A', z: 3, u: 7, v: 7, r: 2, erase: false },
    ]
    expect(compact(ops)).toEqual([ops[2], ops[3]])
  })

  it('**不跨层裁剪**:别的层的操作不受影响', () => {
    const ops: DraftOp[] = [
      { op: 'dab', seg: 'A', z: 2, u: 5, v: 5, r: 2, erase: false },
      { op: 'clearSlice', seg: 'A', z: 3 },
    ]
    expect(compact(ops)).toEqual(ops)
  })

  it('**不跨段裁剪**:同层但别的段不受影响', () => {
    const ops: DraftOp[] = [
      { op: 'dab', seg: 'B', z: 3, u: 5, v: 5, r: 2, erase: false },
      { op: 'clearSlice', seg: 'A', z: 3 },
    ]
    expect(compact(ops)).toEqual(ops)
  })

  it('propagate 之前的操作不许被裁(它要读那一层的内容)', () => {
    // 若把 propagate 当成"只影响 op.z"来裁,dab 会被 clearSlice 裁掉,
    // 传播出去的就成了空白层——回放与实时立刻不一致。
    const ops: DraftOp[] = [
      { op: 'dab', seg: 'A', z: 3, u: 5, v: 5, r: 3, erase: false },
      { op: 'propagate', seg: 'A', z: 3, span: 2 },
      { op: 'clearSlice', seg: 'A', z: 3 },
    ]
    expect(compact(ops)).toEqual(ops)

    const live = fresh()
    for (const op of ops) applyOp(live, dims, op)
    const restored = fresh()
    replay(restored, dims, compact(ops))
    expect(restored.masks.get('A')!).toEqual(live.masks.get('A')!)
    // 第 3 层被清空,但第 1/5 层应留有传播过去的内容。
    const plane = dims[0] * dims[1]
    expect(live.masks.get('A')!.subarray(3 * plane, 4 * plane).some((v) => v === 1)).toBe(false)
    expect(live.masks.get('A')!.subarray(5 * plane, 6 * plane).some((v) => v === 1)).toBe(true)
  })
})

describe('段生命周期(真浏览器崩溃测试逮到的洞)', () => {
  // 现实中最常见的场景恰恰是最危险的:新建一段 → 画半小时 → 崩溃。
  // 那个段服务端根本不存在,恢复时若日志里没有 addSeg,每一笔都会被"段不存在"
  // 跳过——横幅说"检测到 N 笔未保存",点恢复什么也没有,**且不报任何错**。
  // 这个洞是真浏览器崩溃验收测出来的,补一条单测让它以后毫秒级暴露。
  const emptyState = (): DraftState => ({ masks: new Map(), segs: new Map() })

  it('从**完全空白**的状态回放:新建段的勾画必须被完整恢复', () => {
    const ops: DraftOp[] = [
      { op: 'addSeg', seg: 'S1', label: '肿瘤', color: '#ff0000' },
      { op: 'dab', seg: 'S1', z: 3, u: 8, v: 6, r: 3, erase: false },
      { op: 'patchSeg', seg: 'S1', label: '肝肿瘤' },
    ]
    const st = emptyState()
    expect(replay(st, dims, ops)).toBe(3)
    expect(st.masks.get('S1')!.some((v) => v === 1)).toBe(true)
    expect(st.segs.get('S1')).toEqual({ label: '肝肿瘤', color: '#ff0000' })
  })

  it('回放两遍不该把已画的内容清零(addSeg 幂等)', () => {
    const ops: DraftOp[] = [
      { op: 'addSeg', seg: 'S1', label: 'x', color: '#fff' },
      { op: 'dab', seg: 'S1', z: 3, u: 8, v: 6, r: 3, erase: false },
    ]
    const st = emptyState()
    replay(st, dims, ops)
    const painted = st.masks.get('S1')!.filter((v) => v === 1).length
    replay(st, dims, ops) // 重复回放
    expect(st.masks.get('S1')!.filter((v) => v === 1).length).toBe(painted)
  })

  it('compact **永不**丢弃 addSeg(丢了它整段勾画就没了)', () => {
    const ops: DraftOp[] = [
      { op: 'addSeg', seg: 'S1', label: 'x', color: '#fff' },
      { op: 'dab', seg: 'S1', z: 3, u: 8, v: 6, r: 3, erase: false },
      { op: 'clearSlice', seg: 'S1', z: 3 },
      { op: 'dab', seg: 'S1', z: 3, u: 2, v: 2, r: 2, erase: false },
    ]
    const packed = compact(ops)
    expect(packed[0]).toEqual(ops[0]) // addSeg 还在，且仍在最前
    expect(packed.length).toBeLessThan(ops.length) // 确实压掉了东西
    // 压紧后从空回放仍与实时一致
    const live = emptyState()
    for (const op of ops) applyOp(live, dims, op)
    const rest = emptyState()
    replay(rest, dims, packed)
    expect(rest.masks.get('S1')!).toEqual(live.masks.get('S1')!)
  })
})

describe('rebuildInto(撤销的实现)', () => {
  const snap = (st: DraftState) => new Map([...st.masks].map(([k, v]) => [k, v.slice()]))

  it('回退 n 条 ≡ 只做前 n 条(撤销的正确性)', () => {
    const ops = randomOps(99, 200)
    const base = snap(fresh())

    // 做满 200 条，再回退到前 120 条
    const st = fresh()
    for (const op of ops) applyOp(st, dims, op)
    rebuildInto(st, dims, base, ops.slice(0, 120))

    // 对照：从头只做前 120 条
    const ref = fresh()
    for (const op of ops.slice(0, 120)) applyOp(ref, dims, op)

    expect(st.masks.get('A')!).toEqual(ref.masks.get('A')!)
    // 防空断言：前 120 条要真的画出了东西，且与做满 200 条不同
    expect(ref.masks.get('A')!.some((v) => v === 1)).toBe(true)
    const full = fresh()
    for (const op of ops) applyOp(full, dims, op)
    expect(full.masks.get('A')!).not.toEqual(ref.masks.get('A')!)
  })

  it('**就地复位,不换缓冲区**(换了会把渲染进程卡死——CLAUDE.md 的坑)', () => {
    const st = fresh()
    const before = st.masks.get('A')!
    rebuildInto(st, dims, snap(fresh()), [])
    expect(st.masks.get('A')).toBe(before) // 同一个对象引用
  })

  it('全部撤销 → 回到基线(不是回到空)', () => {
    // 基线 = 从服务端载入的已有标注。撤销自己这次的编辑,不该把别人存过的抹掉。
    const st = fresh()
    applyOp(st, dims, { op: 'dab', seg: 'A', z: 2, u: 8, v: 6, r: 3, erase: false })
    const base = snap(st) // 把这一笔当作"服务端已有的内容"
    const mine: DraftOp[] = [{ op: 'dab', seg: 'A', z: 5, u: 4, v: 4, r: 2, erase: false }]
    for (const op of mine) applyOp(st, dims, op)

    rebuildInto(st, dims, base, []) // 撤销掉我的全部操作
    expect(st.masks.get('A')!).toEqual(base.get('A')!)
    expect(st.masks.get('A')!.some((v) => v === 1)).toBe(true) // 基线内容还在
  })

  it('撤销掉建段 → 那个段回到空白,且段元信息也撤销', () => {
    const st: DraftState = { masks: new Map(), segs: new Map() }
    const ops: DraftOp[] = [
      { op: 'addSeg', seg: 'S1', label: '肿瘤', color: '#f00' },
      { op: 'dab', seg: 'S1', z: 3, u: 8, v: 6, r: 3, erase: false },
    ]
    replay(st, dims, ops)
    expect(st.segs.get('S1')?.label).toBe('肿瘤')

    rebuildInto(st, dims, new Map(), []) // 基线里没有 S1
    expect(st.masks.get('S1')!.some((v) => v === 1)).toBe(false)
    expect(st.segs.has('S1')).toBe(false) // 元信息一并撤销
  })
})

describe('applyOp 的边界与容错', () => {
  it('段不存在 → 返回 false 而不是抛错(回放遇到已删段要能跳过)', () => {
    expect(applyOp(fresh(), dims, { op: 'clearSlice', seg: '不存在', z: 0 })).toBe(false)
  })

  it('层号越界 → 返回 false,不越界写别的层', () => {
    const m = fresh()
    expect(applyOp(m, dims, { op: 'dab', seg: 'A', z: 99, u: 1, v: 1, r: 3, erase: false })).toBe(false)
    expect(applyOp(m, dims, { op: 'dab', seg: 'A', z: -1, u: 1, v: 1, r: 3, erase: false })).toBe(false)
    // **恰好压线的 z === nz 才是真正的边界**:此前只测了 99,把 `>=` 写成 `>`
    // 全部测试照样绿(变异测试逮到)。差一位的错永远发生在压线那一格上。
    expect(applyOp(m, dims, { op: 'dab', seg: 'A', z: dims[2], u: 1, v: 1, r: 3, erase: false })).toBe(false)
    expect(applyOp(m, dims, { op: 'clearSlice', seg: 'A', z: dims[2] })).toBe(false)
    expect(applyOp(m, dims, { op: 'propagate', seg: 'A', z: dims[2], span: 2 })).toBe(false)
    // 最后一个合法层必须仍然可写(否则上面的断言可以靠"全都拒绝"作弊通过)。
    expect(applyOp(m, dims, { op: 'dab', seg: 'A', z: dims[2] - 1, u: 8, v: 6, r: 3, erase: false })).toBe(true)
    const plane = dims[0] * dims[1]
    expect(m.masks.get('A')!.subarray(0, (dims[2] - 1) * plane).some((v) => v !== 0)).toBe(false)
  })

  it('propagate 在体数据两端被夹住,不写到卷外', () => {
    const m = fresh()
    applyOp(m, dims, { op: 'dab', seg: 'A', z: 0, u: 8, v: 6, r: 3, erase: false })
    applyOp(m, dims, { op: 'propagate', seg: 'A', z: 0, span: 3 })
    const plane = dims[0] * dims[1]
    expect(m.masks.get('A')!.subarray(1 * plane, 2 * plane).some((v) => v === 1)).toBe(true)
    expect(m.masks.get('A')!.length).toBe(N) // 没有越界写坏 buffer
  })

  it('propagate **上下两侧都要写**', () => {
    // 变异测试逮到的空档:此前只断言了 z+d 一侧,把 propagate 改成单向传播,
    // 全部测试照样绿。而核心性质测试**结构上抓不到**这个——实时与回放都走
    // applyOp,一起错就一起对。性质测试保的是"两条路径一致",不是"语义正确";
    // 语义得由这种具体断言来钉。
    const m = fresh()
    const mid = 4
    applyOp(m, dims, { op: 'dab', seg: 'A', z: mid, u: 8, v: 6, r: 3, erase: false })
    applyOp(m, dims, { op: 'propagate', seg: 'A', z: mid, span: 2 })
    const plane = dims[0] * dims[1]
    const painted = (z: number) => m.masks.get('A')!.subarray(z * plane, (z + 1) * plane).some((v) => v === 1)
    for (const z of [mid - 2, mid - 1, mid + 1, mid + 2]) {
      expect(painted(z), `第 ${z} 层应被传播到`).toBe(true)
    }
    expect(painted(mid - 3)).toBe(false) // span 之外不该被写
    expect(painted(mid + 3)).toBe(false)
  })

  it('擦除是真的擦掉(否则 erase 分支形同虚设)', () => {
    const m = fresh()
    applyOp(m, dims, { op: 'dab', seg: 'A', z: 2, u: 8, v: 6, r: 4, erase: false })
    const painted = m.masks.get('A')!.filter((v) => v === 1).length
    expect(painted).toBeGreaterThan(0)
    applyOp(m, dims, { op: 'dab', seg: 'A', z: 2, u: 8, v: 6, r: 4, erase: true })
    expect(m.masks.get('A')!.some((v) => v === 1)).toBe(false)
  })
})
