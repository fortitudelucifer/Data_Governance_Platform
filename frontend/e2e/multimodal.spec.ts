import { test, expect, type Page } from '@playwright/test'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

import { loadState } from './seed'

// 多模态基础功能冒烟：驱动真实预览构建（:4173），覆盖用户反复踩的点——
// 总览统计、资产列表、视频工作台加载（防 "Failed to fetch dynamically
// imported module"）、一键全选、视频导入(上传)、删除、审核批注/导航、成本闸门。
//
// 数据由 globalSetup 播种（见 e2e/seed.ts），不再硬编码开发机上的 dataset/task ID。
// reviewTask 与 editTask 是**两个不同的任务**：审核用例会把 reviewTask 推进审核态，
// 而成本闸门用例需要一个可编辑的任务才能看到「AI 预标注」区块。

const S = loadState()
const HERE = dirname(fileURLToPath(import.meta.url))
const UPLOAD_FIXTURE = join(HERE, 'fixtures', 'upload_test.mp4')

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 15_000 })
}

// 收集致命的动态导入 / 运行时错误——用户反复遇到的 lazy 模块加载失败。
function collectFatal(page: Page): string[] {
  const errs: string[] = []
  page.on('pageerror', (e) => errs.push('pageerror: ' + String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errs.push('console: ' + m.text()) })
  return errs
}
function assertNoModuleError(errs: string[]) {
  const fatal = errs.filter((e) => /Failed to fetch dynamically imported module|Importing a module script failed/i.test(e))
  expect(fatal, 'lazy 模块加载失败').toEqual([])
}

// 删除确认用的是 window.confirm —— 全局接受。
test.beforeEach(async ({ page }) => {
  page.on('dialog', (d) => d.accept())
})

test('总览页加载并显示统计', async ({ page }) => {
  const errs = collectFatal(page)
  await login(page)
  await page.goto('/')
  await expect(page.getByRole('link', { name: /数据集/ }).first()).toBeVisible()
  await expect(page.locator('body')).toContainText(/数据集|任务|标注/)
  assertNoModuleError(errs)
})

test('视频资产列表加载 + 两条视频可见', async ({ page }) => {
  const errs = collectFatal(page)
  await login(page)
  await page.goto(`/datasets/${S.videoDatasetId}/assets`)
  await expect(page.getByRole('button', { name: /导入数据/ })).toBeVisible()
  await expect(page.getByText(S.reviewVideoFile)).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText(S.editVideoFile)).toBeVisible()
  assertNoModuleError(errs)
})

test('视频标注工作台加载（无动态导入错误 + 播放器渲染）', async ({ page }) => {
  const errs = collectFatal(page)
  await login(page)
  await page.goto(`/video-tasks/${S.editTaskId}`)
  await expect(page.getByRole('button', { name: /返回/ })).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTitle('播放/暂停 (空格)')).toBeVisible({ timeout: 15_000 })
  // 时间码文本 mm:ss.mmm（由帧索引/时长渲染）
  await expect(page.getByText(/\d+:\d{2}\.\d{3}/).first()).toBeVisible({ timeout: 15_000 })
  assertNoModuleError(errs)
})

test('一键全选（勾选后出现批量操作）', async ({ page }) => {
  await login(page)
  await page.goto(`/datasets/${S.videoDatasetId}/assets`)
  await expect(page.getByText(S.reviewVideoFile)).toBeVisible({ timeout: 15_000 })
  await page.getByText('全选', { exact: false }).first().click()
  await expect(page.getByRole('button', { name: /删除选中/ })).toBeVisible()
})

test('导入视频（上传）→ 出现在列表 → 表格视图删除', async ({ page }) => {
  await login(page)
  await page.goto(`/datasets/${S.videoDatasetId}/assets`)
  await expect(page.getByRole('button', { name: /导入数据/ })).toBeVisible()
  await page.locator('input[type="file"]').setInputFiles(UPLOAD_FIXTURE)
  await expect(page.getByText('upload_test.mp4')).toBeVisible({ timeout: 30_000 })

  await page.getByTitle('表格 / 任务看板').click()
  const row = page.locator('tr', { hasText: 'upload_test.mp4' })
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.getByTitle('删除样本（永久）').click() // confirm 由 beforeEach 接受
  await expect(page.getByText('upload_test.mp4')).toHaveCount(0, { timeout: 15_000 })
})

// 审核批注锚定 frame+track（B3.1）：审核员钉一条批注 → 标注员点它一键跳到
// 问题帧并选中该 track → 未修复时提交被拦下 → 标记「已修复」后放行。
test('审核批注：锚定 frame+track，点击跳转，未修复不得提交', async ({ page }) => {
  const errs = collectFatal(page)
  await login(page)

  // 驱动真实 REST 走到 QA_REJECTED + 恰好一条锚定批注（页面同源，带 cookie）。
  // 先清掉历史批注，否则上一次失败的残留会让待修复计数对不上（且提交会被拦）。
  const setup = await page.evaluate(async ({ taskId, frame, trackId }) => {
    const post = (u: string, b: unknown) =>
      fetch(u, { method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'include', body: JSON.stringify(b) })
    const { items = [] } = await (await fetch(`/api/tasks/${taskId}/review-comments`, { credentials: 'include' })).json()
    for (const c of items) await fetch(`/api/tasks/${taskId}/review-comments/${c.id}`, { method: 'DELETE', credentials: 'include' })
    await post(`/api/tasks/${taskId}/submit`, {})
    await post(`/api/tasks/${taskId}/qa/reject`, { note: 'e2e' })
    const r = await post(`/api/tasks/${taskId}/review-comments`, { frame, track_id: trackId, body: `e2e：第 ${frame} 帧这个框偏左` })
    return { status: r.status, body: await r.json() }
  }, { taskId: S.reviewTaskId, frame: S.humanFrame, trackId: S.humanTrackId })
  expect(setup.status).toBe(200)
  const commentId = setup.body.id as string

  await page.goto(`/video-tasks/${S.reviewTaskId}`)
  await expect(page.getByText('审核批注')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByText('1 条待修复')).toBeVisible()

  // 等帧索引就绪：seekToFrame 把目标 clamp 到 [0, frameCount-1]，
  // frameCount 仍为 0 时任何跳转都会落回第 0 帧。
  await expect(page.getByText(/^\/ [1-9]\d*$/)).toBeVisible({ timeout: 20_000 })

  // 点锚点标签：整行都是跳转热区，而标签一定在按钮的 stopPropagation 区域之外
  const anchor = `第 ${S.humanFrame} 帧 · #${S.humanTrackId}`
  await expect(page.getByText(anchor)).toBeVisible()
  await page.getByText(anchor).click()
  await expect(page.locator('input.font-mono').first()).toHaveValue(String(S.humanFrame), { timeout: 10_000 })

  // 未修复 → 提交被后端拦下，错误文案出现
  await page.getByRole('button', { name: '提交', exact: true }).click()
  await expect(page.getByText(/仍有未处理的审核批注/)).toBeVisible({ timeout: 10_000 })

  // 标「已修复」→ 计数消失 → 提交放行
  await page.getByRole('button', { name: '已修复' }).click()
  await expect(page.getByText('1 条待修复')).toHaveCount(0, { timeout: 10_000 })

  assertNoModuleError(errs)

  await page.evaluate(async ({ taskId, cid }) => {
    await fetch(`/api/tasks/${taskId}/review-comments/${cid}`, { method: 'DELETE', credentials: 'include' })
  }, { taskId: S.reviewTaskId, cid: commentId })
})

// 审核导航（B3.1）：倍速播放时 overlay 跟着走 + 一键跳到模型最没把握的 track。
// 播种的 AI track 带 ai_score=0.84，正好用来验证阈值语义：
// 阈值 0.5 → 无低置信（按钮禁用）；调到 0.9 → 命中 1 条，跳到它的首关键帧。
// 首关键帧刻意不是 0：视频本就从第 0 帧开始，断言「跳到 0」即使跳转没发生也会过。
test('审核导航：倍速播放 overlay 跟随 + 跳下一低置信 track', async ({ page }) => {
  const errs = collectFatal(page)
  await login(page)

  await page.evaluate(async (taskId) => {
    const post = (u: string, b: unknown) =>
      fetch(u, { method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'include', body: JSON.stringify(b) })
    const { items = [] } = await (await fetch(`/api/tasks/${taskId}/review-comments`, { credentials: 'include' })).json()
    for (const c of items) await fetch(`/api/tasks/${taskId}/review-comments/${c.id}`, { method: 'DELETE', credentials: 'include' })
    await post(`/api/tasks/${taskId}/submit`, {}) // → QA_PENDING，进入审核态
    localStorage.removeItem('video:lowConfThreshold')
    localStorage.removeItem('video:playbackSpeed')
  }, S.reviewTaskId)

  await page.goto(`/video-tasks/${S.reviewTaskId}`)
  await expect(page.getByText(/^\/ [1-9]\d*$/)).toBeVisible({ timeout: 20_000 })

  // 默认阈值 0.5：0.84 不算低置信 → 按钮无计数且禁用
  const lowConf = page.getByRole('button', { name: /低置信/ })
  await expect(lowConf).toBeVisible()
  await expect(lowConf).toBeDisabled()

  await page.locator('input[title^="低置信阈值"]').fill('0.9')
  await expect(page.getByRole('button', { name: /低置信 \(1\)/ })).toBeEnabled()

  await page.getByRole('button', { name: /低置信 \(1\)/ }).click()
  await expect(page.locator('input.font-mono').first()).toHaveValue(String(S.aiFirstFrame), { timeout: 10_000 })
  await expect(page.getByText(new RegExp(`置信度 ${S.aiScore}`))).toBeVisible()

  // 倍速播放：overlay 由 rVFC → currentFrame → shapesAtFrame 驱动。
  // 注意 scope 到 overlay 那张 svg：页面里 lucide 图标也是 <svg><rect>，
  // 直接 'svg rect' 会抓到工具栏图标（它当然永远不动）。
  const overlayRect = page.locator('svg[preserveAspectRatio="none"] rect').first()
  await expect(overlayRect).toBeVisible()
  const xAt = () => overlayRect.getAttribute('x')

  await page.locator('select').filter({ hasText: '×' }).selectOption('2')
  const frameBefore = await page.locator('input.font-mono').first().inputValue()
  const xBefore = await xAt()

  await page.evaluate(() => (document.querySelector('video') as HTMLVideoElement)?.play())
  await expect.poll(async () => Number(await page.locator('input.font-mono').first().inputValue()),
    { timeout: 10_000 }).toBeGreaterThan(Number(frameBefore))
  expect(await page.evaluate(() => (document.querySelector('video') as HTMLVideoElement)?.playbackRate)).toBe(2)
  await expect.poll(xAt, { timeout: 10_000 }).not.toBe(xBefore)
  await page.evaluate(() => (document.querySelector('video') as HTMLVideoElement)?.pause())

  assertNoModuleError(errs)
})

// B2.8 成本闸门：数据集级 AI 预标注配置（管理员）+ 工作台的队列/关闭提示。
// 越界值由服务端夹住并回显实际生效值——UI 必须显示真正存进去的数字。
test('成本闸门：数据集级 AI 配置越界被夹住 + 关闭后工作台禁用按钮', async ({ page }) => {
  const errs = collectFatal(page)
  await login(page)

  await page.goto('/datasets')
  await page.getByPlaceholder(/搜索/).fill(S.videoDatasetName)
  const row = page.locator('tr', { hasText: S.videoDatasetName })
  await expect(row).toBeVisible({ timeout: 15_000 })
  await row.getByTitle(/AI 预标注设置/).click()

  await expect(page.getByRole('heading', { name: /AI 预标注设置/ })).toBeVisible()
  const maxFrames = page.locator('input[title="帧数上限"]')
  await maxFrames.fill('99999')
  await page.getByRole('button', { name: '保存', exact: true }).click()

  // 服务端天花板 3000：UI 显示的是实际生效值，不是管理员输入的 99999
  await expect(page.getByText(/已被自动收敛/)).toBeVisible({ timeout: 10_000 })
  await expect(maxFrames).toHaveValue('3000')

  // 关掉 AI 预标注 → 工作台按钮禁用并说明原因。
  // 等真实的 PUT 响应，而不是等 "已保存" 文案——上一次保存的文案还在，
  // 断言它会立刻通过，测试就会抢在请求落库之前跑掉。
  await page.locator('select[title="触发模式"]').selectOption('off')
  const [putResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/video-ai-config') && r.request().method() === 'PUT'),
    page.getByRole('button', { name: '保存', exact: true }).click(),
  ])
  expect((await putResp.json()).trigger).toBe('off')
  await page.getByRole('button', { name: '取消' }).click()

  // 用 editTask 而不是 reviewTask：审核态下整个「AI 预标注」区块本就不渲染。
  await page.goto(`/video-tasks/${S.editTaskId}`)
  await expect(page.getByText('该数据集已关闭 AI 预标注（管理员可在数据集设置里改）')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByRole('button', { name: 'AI 预标注' })).toBeDisabled()

  assertNoModuleError(errs)

  // 复原：把数据集配置清回默认
  await page.evaluate(async (dsId) => {
    await fetch(`/api/datasets/${dsId}/video-ai-config`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, credentials: 'include',
      body: JSON.stringify({}),
    })
  }, S.videoDatasetId)
})
