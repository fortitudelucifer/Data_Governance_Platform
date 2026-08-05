import { test, expect, type Page } from '@playwright/test'

import { loadState } from './seed'

// 提交/审核完成后的去处：三个模态必须一致 —— 有下一条就进下一条，没有就回**来处**。
// 此前图片回来处，音频/视频却硬跳「我的任务」；视频甚至根本不去下一条。
// 标完最后一张图回资产列表、标完最后一段视频跳我的任务，同一个平台两种行为。

const S = loadState()

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 15_000 })
}

// 提交会把状态推进 QA_PENDING；重跑时先驳回，让它回到可提交的状态。
// （驳回在非 QA_PENDING 时会失败，无害。）
// 把任务复位到「可提交」。后端允许提交的状态只有三种（qa_service.go）：
// HUMAN_IN_PROGRESS / HUMAN_PENDING / QA_REJECTED。
//
// ⚠️ 这里**断言的是复位后的结果状态，不是 reject 调用的返回码**：reject 只接受
// QA_PENDING（`task state X is not awaiting QA`），所以任务本来就已经是
// QA_REJECTED 时它必然 400 —— 那是合理的，不该当失败。
//
// 但**绝不能像以前那样整个吞掉**：任务一旦掉进 reject 回不来的终态
// （FINALIZED / EXPORTED，例如某次跑崩在提交之后、复位之前），复位会永远静默失败，
// 症状是后面「等不到提交按钮」的 15 秒超时 —— 完全看不出真因是上一次跑崩留下的
// 脏状态。e2e 用例共享同一批播种数据，且播种「找到就复用、从不重置状态」
// （seed.ts），所以这种脏状态会跨运行一直烂在开发机上；CI 每次空库，永远发现不了。
// 复位失败就**当场炸并说明白**，别让它伪装成别的错。
async function makeSubmittable(page: Page, taskId: number) {
  const SUBMITTABLE = ['HUMAN_IN_PROGRESS', 'HUMAN_PENDING', 'QA_REJECTED']
  const state = await page.evaluate(async (id) => {
    await fetch(`/api/tasks/${id}/qa/reject`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      credentials: 'include', body: JSON.stringify({ note: 'e2e reset' }),
    })
    // 不看 reject 的返回码，改看它到底有没有把任务弄成可提交的样子。
    const r = await fetch(`/api/tasks/${id}`, { credentials: 'include' })
    if (!r.ok) return `<读取任务失败 HTTP ${r.status}>`
    return (await r.json()).state as string
  }, taskId)

  expect(
    SUBMITTABLE,
    `任务 ${taskId} 复位失败，当前状态 ${state} —— 提交按钮不会出现。` +
      `多半是上一次 e2e 跑崩留下的脏状态（播种不会重置状态）。` +
      `修法：把该任务改回可提交状态，或重建 e2e 数据。`,
  ).toContain(state)
}

test('视频工作台：提交后没有下一条 → 回到来处（不是硬跳「我的任务」）', async ({ page }) => {
  await login(page)
  await makeSubmittable(page, S.submitTaskId)

  // 从资产列表进去，来处就是资产列表
  const assets = `/datasets/${S.videoDatasetId}/assets`
  await page.goto(assets)
  await expect(page.getByText('seed_video3.mp4')).toBeVisible({ timeout: 15_000 })
  await page.getByText('seed_video3.mp4').first().click()
  await page.waitForURL(new RegExp(`/video-tasks/${S.submitTaskId}`), { timeout: 15_000 })

  // 等工作台挂载（「上一个/下一个」是它独有的）
  await expect(page.getByRole('button', { name: /上一个/ })).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: '提交', exact: true }).click()

  // 关键断言：回资产列表，而不是 /my-tasks
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 15_000 }).toBe(assets)

  await makeSubmittable(page, S.submitTaskId) // 复原，别给下一次留脏状态
})

test('直接开工作台 URL（无来处）提交 → 回到该数据集的资产列表', async ({ page }) => {
  await login(page)
  await makeSubmittable(page, S.submitTaskId)

  await page.goto(`/video-tasks/${S.submitTaskId}`)
  await expect(page.getByRole('button', { name: /上一个/ })).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: '提交', exact: true }).click()

  // 没有来处 → 落到上一级（数据集的资产列表），仍然不是 /my-tasks
  await expect.poll(() => new URL(page.url()).pathname, { timeout: 15_000 })
    .toBe(`/datasets/${S.videoDatasetId}/assets`)

  await makeSubmittable(page, S.submitTaskId)
})
