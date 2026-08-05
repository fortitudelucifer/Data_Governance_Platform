import { expect, test, type Page } from '@playwright/test'

// C0.6c 撤销/重做。契约要求"Ctrl+Z 回放同一份日志"——与草稿共用一套机制。
//
// ⚠️ 需要体数据任务（seed 尚未播体数据），默认跳过。
// 本地：`TID=<体任务id> npx playwright test e2e/undo-redo.spec.ts`
const TID = process.env.TID

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 20_000 })
}

async function openTask(page: Page) {
  await page.goto(`/volume-tasks/${TID}`)
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

/** 当前段的体积文本——用它当掩膜内容的指纹。 */
async function volumeText(page: Page): Promise<string> {
  const el = page.locator('aside').getByText(/体积：/).first()
  if ((await el.count()) === 0) return '(无)'
  return ((await el.textContent()) ?? '').replace(/\s+/g, '')
}

/** 画一笔，返回画完后的体积文本。 */
async function stroke(page: Page, at: number): Promise<string> {
  const box = (await page.locator('canvas').first().boundingBox())!
  await page.mouse.move(box.x + box.width * at, box.y + box.height * 0.5)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width * (at + 0.03), box.y + box.height * 0.55)
  await page.mouse.up()
  await page.waitForTimeout(250)
  return volumeText(page)
}

test('C0.6c 撤销/重做:逐笔回退且可重做', async ({ page }) => {
  test.skip(!TID, '需要体数据任务:TID=<id> 环境变量')

  await login(page)
  await openTask(page)
  await page.getByRole('button', { name: '+ 新建' }).click()
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /开始涂抹/ }).click()

  const v1 = await stroke(page, 0.4)
  const v2 = await stroke(page, 0.48)
  const v3 = await stroke(page, 0.56)
  console.log('三笔后体积:', v1, '→', v2, '→', v3)
  // 防空断言:三笔必须各自真的增加了体积,否则后面的回退比对毫无意义。
  expect(new Set([v1, v2, v3]).size, '三笔应产生三个不同的体积').toBe(3)

  // --- 撤销两次 → 应回到第一笔的状态 ---
  await page.getByTestId('undo').click()
  await page.waitForTimeout(200)
  expect(await volumeText(page), '撤销一次应回到第二笔').toBe(v2)
  await page.getByTestId('undo').click()
  await page.waitForTimeout(200)
  expect(await volumeText(page), '撤销两次应回到第一笔').toBe(v1)

  // --- 重做一次 → 回到第二笔 ---
  await page.getByTestId('redo').click()
  await page.waitForTimeout(200)
  expect(await volumeText(page), '重做应回到第二笔').toBe(v2)

  // --- 键盘 Ctrl+Z 也要通 ---
  await page.keyboard.press('Control+z')
  await page.waitForTimeout(200)
  expect(await volumeText(page), 'Ctrl+Z 应与按钮等效').toBe(v1)

  // --- 全部撤销 → 段还在但为空 ---
  await page.getByTestId('undo').click()
  await page.waitForTimeout(300)
  await expect(page.getByTestId('undo')).toBeDisabled() // 没得撤了

  const errs: string[] = []
  page.on('console', (m) => m.type() === 'error' && errs.push(m.text()))
  expect(errs).toEqual([])
})

test('C0.6c 撤销后刷新页面:被撤销的笔画**不该复活**', async ({ page }) => {
  test.skip(!TID, '需要体数据任务:TID=<id> 环境变量')

  // 撤销只改内存、不截断草稿日志的话，会出一个很别扭的 bug：
  // 撤销掉几笔 → 崩溃/刷新 → 恢复，被撤销的笔画全都回来了，且不报错。
  await login(page)
  await openTask(page)
  await page.getByRole('button', { name: '+ 新建' }).click()
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /开始涂抹/ }).click()

  await stroke(page, 0.4)
  const v2 = await stroke(page, 0.5)
  await page.getByTestId('undo').click()
  await page.waitForTimeout(200)
  const afterUndo = await volumeText(page)
  expect(afterUndo).not.toBe(v2)
  await page.waitForTimeout(1200) // 等草稿截断落盘

  await openTask(page) // 重新进入（模拟重开）
  await page.getByTestId('draft-restore').click()
  await page.waitForTimeout(600)

  expect(await volumeText(page), '恢复后应是撤销之后的状态，不是撤销之前').toBe(afterUndo)
})
