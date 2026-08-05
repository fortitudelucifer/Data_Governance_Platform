import { expect, test, type Page } from '@playwright/test'

// C4.2 AI 点选传播。⚠️ 需要体数据任务 + 后端接了 sam2-volume（本地可用 CPU stub）。
// 本地：先起 stub（python deploy-to-103/sam2-volume-stub.py），后端设
// MM_SEG_VOLUME_ENDPOINT=http://127.0.0.1:8385，再
//   TID=<体任务id> npx playwright test e2e/ai-propagate.spec.ts
//
// 这条测试盯的**不是**"SAM2 分割得准"（那是模型的事，由 103 端到端验），而是
// **平台分层**：AI 点一下 → 冒出一个和手画段一模一样的段 → 能选中/能被画笔编辑
// → 计入分割数。AI 输出若不是"普通段"，这里立刻露馅。
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
  await page.waitForTimeout(1000)
}

test('C4.2 AI 点选 → 追加一个可编辑的分割段', async ({ page }) => {
  test.skip(!TID, '需要体数据任务 + sam2-volume 端点（本地 stub）')

  await login(page)
  await openTask(page)

  const segList = page.getByTestId('segment-list')
  const before = (await segList.count()) ? await segList.locator('li').count() : 0

  // 进 AI 模式 → 点轴位画布中心。
  await page.getByTestId('ai-propagate').click()
  await expect(page.getByTestId('ai-propagate')).toContainText('进行中')
  const box = (await page.locator('canvas').first().boundingBox())!
  await page.mouse.click(box.x + box.width * 0.5, box.y + box.height * 0.5)

  // 传播完成 → 提示出现，段数 +1。
  const msg = page.getByTestId('ai-msg')
  await expect(msg).toContainText('AI 分割完成', { timeout: 60_000 })
  console.log('AI 提示:', (await msg.textContent())?.replace(/\s+/g, ' ').trim())

  await expect(page.getByTestId('segment-list')).toBeVisible()
  const after = await page.getByTestId('segment-list').locator('li').count()
  expect(after, 'AI 段应作为一个新分割段出现').toBe(before + 1)

  // AI 模式应已退出（点一次就产一个段，不该连点误触）。
  await expect(page.getByTestId('ai-propagate')).not.toContainText('进行中')

  // **关键**：新段能被画笔编辑——证明它是"普通段"，不是特殊只读对象。
  // 新段自动选中；开涂抹应可用（不被禁用）。
  await expect(page.getByRole('button', { name: /开始涂抹/ })).toBeEnabled()
  await page.getByRole('button', { name: /开始涂抹/ }).click()
  await expect(page.getByRole('button', { name: /涂抹中/ })).toBeVisible()

  const errs: string[] = []
  page.on('console', (e) => e.type() === 'error' && errs.push(e.text()))
  expect(errs).toEqual([])
})

test('C4.2 AI / 涂抹 / 测量三者互斥', async ({ page }) => {
  test.skip(!TID, '需要体数据任务 + sam2-volume 端点')

  await login(page)
  await openTask(page)
  await page.getByRole('button', { name: '+ 新建' }).click()
  await page.keyboard.press('Escape')

  // 开涂抹 → 开 AI：涂抹必须关掉。
  await page.getByRole('button', { name: /开始涂抹/ }).click()
  await expect(page.getByRole('button', { name: /涂抹中/ })).toBeVisible()
  await page.getByTestId('ai-propagate').click()
  await expect(page.getByRole('button', { name: /开始涂抹/ }), '开 AI 后涂抹必须关').toBeVisible()

  // 开测量 → AI 必须退出（不再显示"进行中"）。
  await page.getByTestId('measure-ruler').click()
  await expect(page.getByTestId('ai-propagate'), '开测量后 AI 必须退出').not.toContainText('进行中')
})
