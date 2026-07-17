import { test, expect, type Page } from '@playwright/test'

// 主链路冒烟：登录 → 应用骨架 → 数据集 → 我的任务。
// 防"白屏 / 路由崩 / 接口挂"这类最常见回归。需 admin/admin123 + 后端 :8280。

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  // 登录成功后离开 /login
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 15_000 })
}

test('登录成功并进入应用骨架', async ({ page }) => {
  await login(page)
  // 侧边栏「数据集」入口可见 = 已登录 + 骨架渲染正常
  await expect(page.getByRole('link', { name: /数据集/ }).first()).toBeVisible()
})

test('数据集列表页加载', async ({ page }) => {
  await login(page)
  await page.goto('/datasets')
  await expect(page.getByPlaceholder('搜索数据集...')).toBeVisible()
})

test('我的任务页加载', async ({ page }) => {
  await login(page)
  await page.goto('/my-tasks')
  await expect(page.getByText('我的任务').first()).toBeVisible()
})

test('未登录访问受保护页跳转登录', async ({ page }) => {
  // 全新 context 无持久化 token（auth 存于 localStorage）→ 受保护路由应跳登录
  await page.goto('/datasets')
  await expect(page).toHaveURL(/\/login/)
  await expect(page.getByPlaceholder('请输入用户名')).toBeVisible()
})
