import { compact, type DraftOp, type DraftRecord } from './draftLog'

// 草稿的**本地持久层**(C0.6):IndexedDB。
//
// ## 为什么是 IndexedDB 而不是 localStorage
// localStorage 是**同步**的(每次写都阻塞主线程,涂抹时逐笔写 = 掉帧)且只有
// ~5 MB。IndexedDB 异步、配额是磁盘级。
//
// ## 为什么要攒批(而不是真·逐笔落盘)
// 契约要"逐笔即时",但一次 mousemove 能产生每秒上百个 dab,逐个开事务会把
// 主线程拖垮——**为了不丢数据而卡住标注员,是本末倒置**。这里的折中:
// 攒够 FLUSH_OPS 条或 FLUSH_MS 毫秒就落盘,并在 `visibilitychange` /
// `pagehide` 时强制落盘。丢失窗口 = 最后不到 1 秒的笔画,且只在"整个进程被
// 强杀且来不及触发 pagehide"时才发生。
//
// ⚠️ 不要用 `beforeunload` 做最后落盘:移动端和现代浏览器的 bfcache 下它**不保证
// 触发**;`pagehide` + `visibilitychange:hidden` 才是当前推荐组合。

const DB_NAME = 'dg-annotation-draft'
const DB_VERSION = 1
const STORE = 'drafts'

/** 攒批阈值:够这么多条就落盘。 */
const FLUSH_OPS = 40
/** 攒批时限:距上次落盘超过这么久就落盘(毫秒)。 */
const FLUSH_MS = 800

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE, { keyPath: 'taskId' })
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

function tx<T>(db: IDBDatabase, mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    const t = db.transaction(STORE, mode)
    const req = fn(t.objectStore(STORE))
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

/**
 * 一个任务的草稿写入器。逐笔 `push`，内部攒批落盘。
 *
 * **失败要看得见**:草稿写不进去(隐私模式、配额满、IndexedDB 被禁)时,标注员
 * 以为自己有兜底、实则没有——这正是"静默出错"。所以 `onError` 必须由调用方接住
 * 并显示到界面上,而不是 console.warn 了事。
 */
export class DraftWriter {
  private buf: DraftOp[] = []
  private all: DraftOp[] = []
  private lastFlush = 0
  private timer: ReturnType<typeof setTimeout> | null = null
  private db: Promise<IDBDatabase> | null = null
  private disposed = false

  // 注意：不能用构造器参数属性（`private readonly x: T` 写在参数位）——
  // 本仓库 tsconfig 开了 `erasableSyntaxOnly`，那是需要 TS 生成运行时代码的语法。
  private readonly meta: { taskId: string; assetId: string }
  private baseline: DraftRecord['baseline']
  private readonly onError: (e: unknown) => void

  constructor(
    meta: { taskId: string; assetId: string },
    baseline: DraftRecord['baseline'],
    onError: (e: unknown) => void,
  ) {
    this.meta = meta
    this.baseline = baseline
    this.onError = onError
    this.onPageHide = this.onPageHide.bind(this)
    this.onVisibility = this.onVisibility.bind(this)
    // pagehide 覆盖关闭/导航；visibilitychange 覆盖切后台（移动端唯一可靠的钩子）。
    window.addEventListener('pagehide', this.onPageHide)
    document.addEventListener('visibilitychange', this.onVisibility)
  }

  /** 页面要走了：无条件落盘。 */
  private onPageHide() {
    void this.flush()
  }

  /** 只在真的转入后台时落盘；转回前台不必写。 */
  private onVisibility() {
    if (document.visibilityState === 'hidden') void this.flush()
  }

  /** 记一笔。**同步返回**——绝不能让落盘拖住涂抹手感。 */
  push(op: DraftOp) {
    if (this.disposed) return
    this.buf.push(op)
    this.all.push(op)
    const due = this.buf.length >= FLUSH_OPS || Date.now() - this.lastFlush > FLUSH_MS
    if (due) void this.flush()
    else if (!this.timer) this.timer = setTimeout(() => void this.flush(), FLUSH_MS)
  }

  /** 段的服务端身份变了（新建段刚保存出 trackId）→ 更新基线。 */
  setBaseline(baseline: DraftRecord['baseline']) {
    this.baseline = baseline
  }

  /** 当前已记录的操作（只读快照，供撤销计算边界用）。 */
  get ops(): readonly DraftOp[] {
    return this.all
  }

  /**
   * 撤销后截断日志到 n 条。
   *
   * ⚠️ 不截断的话会出一个很别扭的 bug：撤销掉几笔 → 崩溃 → 恢复，
   * **被撤销的笔画全都复活**。标注员明明撤销了，重开又回来了，还不报错。
   * 截断后立刻落盘，不等攒批。
   */
  truncate(n: number) {
    this.all = this.all.slice(0, n)
    this.buf = []
    this.lastFlush = 0 // 强制下次 flush 真的写
    void this.flushNow()
  }

  /** 无条件落盘（即使缓冲区为空也要写——截断后的日志变短了，得覆盖旧记录）。 */
  private async flushNow(): Promise<void> {
    if (this.timer) { clearTimeout(this.timer); this.timer = null }
    this.lastFlush = Date.now()
    try {
      this.db ??= openDB()
      const db = await this.db
      const rec: DraftRecord = {
        taskId: this.meta.taskId,
        assetId: this.meta.assetId,
        baseline: this.baseline,
        // 压紧再落盘:半小时勾画能攒几万条,不压会让恢复时的读取变慢。
        ops: compact(this.all),
        savedAt: Date.now(),
      }
      await tx(db, 'readwrite', (s) => s.put(rec))
    } catch (e) {
      this.onError(e)
    }
  }

  /** 立即落盘（缓冲区为空则跳过）。保存成功后调用 `clear()`，不是这个。 */
  async flush(): Promise<void> {
    if (this.buf.length === 0) {
      if (this.timer) { clearTimeout(this.timer); this.timer = null }
      return
    }
    this.buf = []
    await this.flushNow()
  }

  /** 正式保存成功后丢弃草稿——留着会在下次进页面时误报"有未提交的草稿"。 */
  async clear(): Promise<void> {
    this.all = []
    this.buf = []
    try {
      this.db ??= openDB()
      await tx(await this.db, 'readwrite', (s) => s.delete(this.meta.taskId))
    } catch (e) {
      this.onError(e)
    }
  }

  dispose() {
    this.disposed = true
    if (this.timer) clearTimeout(this.timer)
    window.removeEventListener('pagehide', this.onPageHide)
    document.removeEventListener('visibilitychange', this.onVisibility)
  }
}

/** 读回某任务的草稿；没有则返回 null。 */
export async function loadDraft(taskId: string): Promise<DraftRecord | null> {
  try {
    const db = await openDB()
    const rec = await tx<DraftRecord | undefined>(db, 'readonly', (s) => s.get(taskId))
    return rec ?? null
  } catch {
    // 读不到草稿不该挡住开工——正常打开任务即可，只是没有恢复。
    return null
  }
}

/** 丢弃某任务的草稿（标注员选择"不恢复"）。 */
export async function discardDraft(taskId: string): Promise<void> {
  try {
    const db = await openDB()
    await tx(db, 'readwrite', (s) => s.delete(taskId))
  } catch {
    /* 丢不掉就算了，下次仍会提示，不影响数据安全 */
  }
}
