import { defineConfig, devices } from '@playwright/test'

// 前端 e2e：对运行中的前端预览(:4173)做主链路冒烟。
// 需后端在 :8280 运行（preview 代理 /api → :8280）。webServer 自动起 preview。
// globalSetup 先播种数据（幂等），把 ID 写进 e2e/.e2e-state.json —— 此前 spec 里
// 硬编码了开发机上的 dataset/task ID，CI 上根本跑不起来。
export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  timeout: process.env.CI ? 60_000 : 30_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list']],
  use: {
    baseURL: 'http://localhost:4173',
    headless: true,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'npm run preview',
    url: 'http://localhost:4173',
    timeout: 60_000,
    reuseExistingServer: true,
  },
})
