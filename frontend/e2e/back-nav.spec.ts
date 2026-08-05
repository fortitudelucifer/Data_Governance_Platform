import { test, expect, type Page } from '@playwright/test'

import { loadState } from './seed'

const S = loadState()

// 工作台顶栏「返回」= 回到来处，否则回上一级（数据集资产列表），最后才兜底 /my-tasks。
// 三个模态共用同一套语义（此前：图片写死 /my-tasks，音/视频用浏览器后退）。

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 15_000 })
}
const path = (p: Page) => new URL(p.url()).pathname

// waitForURL 一通过，React 还没卸载上一页——此时 getByRole('返回') 会抓到
// **资产列表自己的**返回按钮（它跳 /datasets）。必须等工作台挂载出来再点。
async function clickWorkbenchBack(page: Page) {
  // 「上一个/下一个（任务）」只有工作台有，资产列表和我的任务都没有 ——
  // 用它确认新页面真的挂载了，再去点返回。
  await expect(page.getByRole('button', { name: /上一个/ })).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: /返回/ }).first().click()
}

test('从资产列表进工作台 → 返回回到资产列表（视频）', async ({ page }) => {
  await login(page)
  await page.goto(`/datasets/${S.videoDatasetId}/assets`)
  await expect(page.getByText(S.editVideoFile)).toBeVisible({ timeout: 15_000 })
  await page.getByText(S.editVideoFile).first().click()
  await page.waitForURL(/video-tasks/, { timeout: 15_000 })

  await clickWorkbenchBack(page)
  await expect.poll(() => path(page), { timeout: 10_000 }).toBe(`/datasets/${S.videoDatasetId}/assets`)
})

test('从我的任务进工作台 → 返回回到我的任务（视频）', async ({ page }) => {
  await login(page)
  await page.goto('/my-tasks')
  await page.waitForTimeout(2000)
  const row = page.locator('[class*="cursor-pointer"]').first()
  if (!(await row.count())) test.skip(true, '当前无我的任务')
  await row.click()
  await page.waitForURL(/-tasks\/\d+/, { timeout: 15_000 })

  await clickWorkbenchBack(page)
  await expect.poll(() => path(page), { timeout: 10_000 }).toBe('/my-tasks')
})

test('直接开工作台 URL（无来处）→ 返回回到该数据集的资产列表', async ({ page }) => {
  await login(page)
  // 播种的图片任务；此前这里会硬跳 /my-tasks
  await page.goto(`/image-tasks/${S.imageTaskId}`)
  await expect(page.getByRole('button', { name: /返回/ })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: /返回/ }).click()
  await expect.poll(() => path(page), { timeout: 10_000 }).toBe(`/datasets/${S.imageDatasetId}/assets`)
})

test('在工作台里翻到下一个任务后，返回仍回到来处（图片）', async ({ page }) => {
  await login(page)
  await page.goto(`/datasets/${S.imageDatasetId}/assets`)
  await page.waitForTimeout(2500)
  await page.goto(`/image-tasks/${S.imageTaskId}`)
  await expect(page.getByRole('button', { name: /返回/ })).toBeVisible({ timeout: 15_000 })
  // 直接开的 URL 无 state → 应回 305 的资产列表；翻页后仍应如此（而非退回上一个任务）
  const next = page.getByRole('button', { name: /下一?[个]?/ }).last()
  if (await next.count() && await next.isEnabled()) {
    await next.click()
    await page.waitForTimeout(1500)
  }
  await page.getByRole('button', { name: /返回/ }).click()
  await expect.poll(() => path(page), { timeout: 10_000 }).toBe(`/datasets/${S.imageDatasetId}/assets`)
})
