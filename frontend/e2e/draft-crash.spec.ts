import { chromium, expect, test, type BrowserContext, type Page } from '@playwright/test'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

// C0.6 验收(契约原话):**勾画 30 分钟 → 强杀浏览器 → 重开 → 恢复到最后一笔**。
//
// 两个细节决定这条测试是真是假:
//  ① 必须用 **persistent context**。Playwright 默认的 context 是一次性的,
//     IndexedDB 会随 context 一起蒸发——用默认 context 测"崩溃后恢复",
//     测的是"数据没了",无论代码对错都失败(或者更糟:恰好通过而毫无意义)。
//  ② 必须是**真崩溃**(CDP `Page.crash`),不是 close()。close() 会触发
//     pagehide,而 pagehide 里我们主动 flush 了——那样测的是优雅退出路径,
//     恰好绕过了"来不及保存"这个真正要防的场景。

// ⚠️ 需要一个**体数据任务**。`seed.ts` 目前只播文本/图片/音视频,所以默认跳过;
// 本地验收用 `TID=<体任务id> npx playwright test e2e/draft-crash.spec.ts` 跑。
// 等体数据进播种(C3 收尾)就把这个守卫去掉,让它进 CI——这条是契约的硬验收,
// 不该只靠"我上次手动跑过"。
const TID = process.env.TID
const BASE = 'http://localhost:4173'

async function login(page: Page) {
  await page.goto(`${BASE}/login`)
  if (page.url().includes('/login')) {
    await page.getByPlaceholder('请输入用户名').fill('admin')
    await page.getByPlaceholder('请输入密码').fill('admin123')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 20_000 })
  }
}

async function openTask(page: Page) {
  await page.goto(`${BASE}/volume-tasks/${TID}`)
  await expect(page.getByText(/轴位 Axial/).first()).toBeVisible({ timeout: 90_000 })
  await page.waitForFunction(
    () => {
      const c = document.querySelector('canvas') as HTMLCanvasElement | null
      return !!c && c.width > 1
    },
    null,
    { timeout: 90_000 },
  )
  await page.waitForTimeout(1200)
}

/** 面板里"已标注层"的总面积文本——用它当掩膜内容的指纹。 */
async function annotatedFingerprint(page: Page): Promise<string> {
  return page.evaluate(() => {
    const aside = document.querySelector('aside')
    if (!aside) return 'NO-ASIDE'
    const txt = [...aside.querySelectorAll('li')].map((li) => li.textContent?.trim() ?? '').join('|')
    return txt
  })
}

test('C0.6 验收:勾画 → 渲染进程崩溃 → 重开恢复到最后一笔', async () => {
  test.skip(!TID, '需要体数据任务:TID=<id> 环境变量(seed 尚未播体数据)')
  const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'dg-draft-'))
  let ctx: BrowserContext | null = null
  try {
    ctx = await chromium.launchPersistentContext(userDataDir, { viewport: { width: 1400, height: 900 } })
    const page = await ctx.newPage()
    await login(page)
    await openTask(page)

    // --- 新建一段并画几笔 ---
    await page.getByRole('button', { name: '+ 新建' }).click()
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: /开始涂抹/ }).click()

    const canvas = page.locator('canvas').first()
    const box = (await canvas.boundingBox())!
    for (let i = 0; i < 6; i++) {
      await page.mouse.move(box.x + box.width * (0.42 + i * 0.02), box.y + box.height * 0.5)
      await page.mouse.down()
      await page.mouse.move(box.x + box.width * (0.46 + i * 0.02), box.y + box.height * 0.54)
      await page.mouse.up()
    }
    // 攒批阈值是 800ms —— 等它真的落盘,否则测的是"还在缓冲区里"。
    await page.waitForTimeout(1500)

    const before = await annotatedFingerprint(page)
    console.log('崩溃前已标注层:', before)
    expect(before, '崩溃前必须真的画上了东西，否则后面比对的是两个空值').not.toBe('')
    expect(before).toMatch(/mm²/)

    // --- 强杀渲染进程(不是优雅关闭) ---
    const cdp = await ctx.newCDPSession(page)
    // ⚠️ `Page.crash` 的 promise **既不 resolve 也不 reject**——回复它的渲染进程
    // 已经死了。`.catch()` 对"永不落定"的 promise 无能为力(第一版就卡死在这，
    // 180 秒超时)。必须用超时把它赛掉。
    await Promise.race([
      cdp.send('Page.crash').catch(() => {}),
      new Promise((r) => setTimeout(r, 3000)),
    ])

    // --- 重开(同一 persistent context → IndexedDB 还在) ---
    const page2 = await ctx.newPage()
    await login(page2)
    await openTask(page2)

    // 必须弹出恢复横幅
    const banner = page2.getByTestId('draft-banner')
    await expect(banner, '崩溃后应检测到未保存草稿').toBeVisible({ timeout: 20_000 })
    console.log('横幅文案:', (await banner.textContent())?.replace(/\s+/g, ' ').trim())

    // 恢复前:掩膜应当是空的(证明恢复确实做了事，而不是本来就有)
    const beforeRestore = await annotatedFingerprint(page2)
    console.log('恢复前已标注层:', beforeRestore || '(空)')

    await page2.getByTestId('draft-restore').click()
    await page2.waitForTimeout(800)

    const after = await annotatedFingerprint(page2)
    console.log('恢复后已标注层:', after)
    expect(after, '恢复后的掩膜必须与崩溃前逐层一致').toBe(before)
    expect(after).not.toBe(beforeRestore) // 防空断言：恢复确实改变了状态

    const errs: string[] = []
    page2.on('console', (m) => m.type() === 'error' && errs.push(m.text()))
    expect(errs).toEqual([])
  } finally {
    await ctx?.close()
    fs.rmSync(userDataDir, { recursive: true, force: true })
  }
})
