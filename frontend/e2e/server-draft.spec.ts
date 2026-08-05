import { expect, test, type Page } from '@playwright/test'

// C0.6b 服务端草稿。⚠️ 需要体数据任务，默认跳过。
// 本地：`TID=<体任务id> npx playwright test e2e/server-draft.spec.ts`
//
// ⚠️ 后端默认间隔 5 分钟，测试等不起。跑之前把后端起成短间隔：
//   $env:MM_DRAFT_FLUSH_INTERVAL="3s"; .\scripts\start-backend.ps1 -WaitForReady
// 间隔由服务端下发（GET /tasks/:id/draft 的 flush_interval_sec），前端不写死，
// 所以改环境变量就能让这条测试跑得动——这也顺带验证了"间隔真的来自服务端"。
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

test('C0.6b 服务端草稿：画笔 → 自动上传 → 面板显示已同步', async ({ page, request }) => {
  test.skip(!TID, '需要体数据任务:TID=<id>')

  // 先清掉服务端残留，否则"已同步"可能是上一轮留下的（假绿）。
  const login1 = await request.post('http://localhost:8280/auth/login', {
    data: { username: 'admin', password: 'admin123' },
  })
  const token = (await login1.json()).token
  await request.delete(`http://localhost:8280/tasks/${TID}/draft`, {
    headers: { Authorization: `Bearer ${token}` },
  })

  await login(page)
  await openTask(page)

  // 初始应显示"尚未同步"——防空断言：若一开始就显示已同步，后面的断言没有意义。
  const sync = page.getByTestId('srv-sync')
  await expect(sync).toContainText('尚未同步')

  await page.getByRole('button', { name: '+ 新建' }).click()
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /开始涂抹/ }).click()
  const box = (await page.locator('canvas').first().boundingBox())!
  await page.mouse.move(box.x + box.width * 0.45, box.y + box.height * 0.5)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width * 0.5, box.y + box.height * 0.55)
  await page.mouse.up()

  // 等定时器触发（后端 MM_DRAFT_FLUSH_INTERVAL=3s 时约 3–6 秒）
  await expect(sync).toContainText('已同步', { timeout: 30_000 })
  console.log('面板:', (await sync.textContent())?.replace(/\s+/g, ' ').trim())

  // 服务端确实收到了，而不是前端自己写了个"已同步"。
  const got = await request.get(`http://localhost:8280/tasks/${TID}/draft`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  const body = await got.json()
  console.log('服务端 op_count:', body.draft?.op_count, '· 间隔:', body.flush_interval_sec)
  expect(body.draft, '服务端应真的存下了草稿').not.toBeNull()
  expect(body.draft.op_count).toBeGreaterThan(0)
  // 日志里必须有建段那条——少了它，换机器恢复出来会是空的（C0.6a 踩过）。
  expect(JSON.stringify(body.draft.ops)).toContain('addSeg')
})
