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
async function makeSubmittable(page: Page, taskId: number) {
  await page.evaluate(async (id) => {
    await fetch(`/api/tasks/${id}/qa/reject`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      credentials: 'include', body: JSON.stringify({ note: 'e2e reset' }),
    })
  }, taskId)
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
