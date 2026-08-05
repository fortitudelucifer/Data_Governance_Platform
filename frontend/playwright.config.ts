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
  // ⚠️ `fullyParallel: false` 只保证**同一文件内**串行，文件之间仍按默认
  // worker 数（CPU/2，本机 16 核 = 8）并行。这套用例并行不安全，实测：
  //   workers=8 → 2 失败   workers=4 → 2 失败   workers=1 → 23 全过
  // 每次失败的用例组合都不同，报错永远是 login 的
  // `page.waitForURL: Timeout 15000ms exceeded`。
  //
  // **不是产品慢**——后端每层都实测过：登录 60ms（并发 10× 不变）、
  // /dashboard/stats 3ms（并发 8× 不变）、/tasks?mine=1 6ms。是同一台机器上
  // 8 个 Chromium 实例 + Postgres/Redis 容器 + 后端 + vite preview 抢资源，
  // 页面 load 事件被拖过 15s。叠加 e2e 本就有的共享可变状态（播种幂等，但
  // 提交/驳回状态不是），并行只会放大成随机假红。
  //
  // 钉成 1：全量 57.7s vs 并行 38s —— 多 20 秒换确定性，值。
  // 假红比慢更贵：它会让人开始习惯性忽略红灯。
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list']],
  use: {
    baseURL: 'http://localhost:4173',
    headless: true,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  // ⚠️ preview 服务的是 **dist/**(静态产物),不是源码——e2e 测的永远是"上一次
  // build 的前端"。这与后端"陈旧 app.exe"是同一个坑(CLAUDE.md 第 5 条)的前端版:
  // 改完 tsx 直接跑 playwright,**绿的是旧包,你的改动一行都没跑过**。
  // 2026-07-19 就靠这个骗过一次变异验证:把布局改回坏的,测试照样全绿。
  // 所以 command 先 build 再 preview;但 reuseExistingServer 命中时会整条跳过,
  // **本地改了前端源码,跑 playwright 前务必自己先 `npm run build`**。
  // (vite preview 每次请求都从磁盘读 dist,重新 build 后无需重启服务器。)
  webServer: {
    command: 'npm run build && npm run preview',
    url: 'http://localhost:4173',
    timeout: 60_000,
    reuseExistingServer: true,
  },
})
