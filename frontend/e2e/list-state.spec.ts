import { test, expect, type Page } from '@playwright/test'

import { loadState } from './seed'

const S = loadState()

// 用户报告：翻到数据集列表的某一页，点进去一个数据集，再「返回」——回到的是
// 第 1 页，而不是刚才那一页。根因：page/搜索/排序放在 useState 里，不在 URL；
// 且资产列表/文档列表的「返回」是 navigate('/datasets') 写死。

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 15_000 })
}
const url = (p: Page) => new URL(p.url())

async function gotoPage3(page: Page) {
  await expect(page.getByText(/第 1 \/ \d+ 页/)).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: '下一页' }).click()
  await page.getByRole('button', { name: '下一页' }).click()
  await expect.poll(() => url(page).searchParams.get('page'), { timeout: 10_000 }).toBe('3')
}

test('数据集列表：页码进 URL，刷新后仍在该页', async ({ page }) => {
  await login(page)
  await page.goto('/datasets')
  await gotoPage3(page)

  // 刷新后仍是第 3 页（URL 是状态的真源）
  await page.reload()
  await expect(page.getByText(/第 3 \/ \d+ 页/)).toBeVisible({ timeout: 15_000 })
  expect(url(page).searchParams.get('page')).toBe('3')
})

test('第 1 页不写进 URL（保持链接干净）', async ({ page }) => {
  await login(page)
  await page.goto('/datasets')
  await expect(page.getByText(/第 1 \/ \d+ 页/)).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: '下一页' }).click()
  await expect.poll(() => url(page).searchParams.get('page'), { timeout: 10_000 }).toBe('2')
  await page.getByRole('button', { name: '首页' }).click()
  await expect.poll(() => url(page).searchParams.get('page')).toBeNull()
})

test('搜索进 URL 并重置到第 1 页', async ({ page }) => {
  await login(page)
  await page.goto('/datasets?page=3')
  await page.getByPlaceholder(/搜索/).fill(S.videoDatasetName)
  await expect.poll(() => url(page).searchParams.get('q'), { timeout: 10_000 }).toBe(S.videoDatasetName)
  // 换了搜索词就该回到第 1 页，否则可能停在一个空页上
  expect(url(page).searchParams.get('page')).toBeNull()
  await expect(page.getByText(S.videoDatasetName)).toBeVisible()
})

// 注意：第 3 页上的数据集是文本模态，点开进的是 DocumentListPage。
// 资产列表（AssetListPage）那条路径由下面「带搜索…」的用例覆盖 —— 两个页面各有
// 一个「返回」按钮，只测一个会漏掉另一个（做变异测试时我就先漏了）。
test('翻到第 3 页 → 点进数据集 → 返回，仍在第 3 页（用户报的那个 · 走 DocumentListPage）', async ({ page }) => {
  await login(page)
  await page.goto('/datasets')
  await gotoPage3(page)

  // 点开本页第一个数据集（进资产列表或文档列表）
  await page.getByRole('button', { name: '打开' }).first().click()
  await page.waitForURL(/\/datasets\/\d+\/(assets|documents)/, { timeout: 15_000 })

  // 列表页自己的「返回」（等它挂载：只有它有「导入数据」/「导入文档」）
  await expect(page.getByRole('button', { name: /导入/ })).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: /返回/ }).first().click()

  await expect.poll(() => url(page).pathname, { timeout: 10_000 }).toBe('/datasets')
  expect(url(page).searchParams.get('page')).toBe('3')
})

test('带搜索点进数据集 → 返回，搜索词还在（走 AssetListPage）', async ({ page }) => {
  await login(page)
  await page.goto('/datasets')
  await page.getByPlaceholder(/搜索/).fill(S.videoDatasetName)
  await expect(page.getByText(S.videoDatasetName)).toBeVisible({ timeout: 10_000 })

  await page.getByRole('button', { name: '打开' }).first().click()
  await page.waitForURL(new RegExp(`/datasets/${S.videoDatasetId}/assets`), { timeout: 15_000 })
  await expect(page.getByRole('button', { name: /导入数据/ })).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: /返回/ }).first().click()

  await expect.poll(() => url(page).searchParams.get('q'), { timeout: 10_000 }).toBe(S.videoDatasetName)
  await expect(page.getByPlaceholder(/搜索/)).toHaveValue(S.videoDatasetName)
})
