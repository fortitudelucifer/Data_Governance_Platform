import { expect, test, type Page } from '@playwright/test'

// C3.4 导出按钮。⚠️ 需要体数据任务（seed 尚未播体数据），默认跳过。
// 本地：`TID=<体任务id> npx playwright test e2e/volume-export.spec.ts`
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
  await page.waitForTimeout(1200)
}

test('C3.4 导出:未保存时挡住,保存后可导出并显示草稿标记', async ({ page }) => {
  test.skip(!TID, '需要体数据任务:TID=<id> 环境变量')

  await login(page)
  await openTask(page)

  // --- 画一笔但**不保存** ---
  await page.getByRole('button', { name: '+ 新建' }).click()
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: /开始涂抹/ }).click()
  const box = (await page.locator('canvas').first().boundingBox())!
  await page.mouse.move(box.x + box.width * 0.45, box.y + box.height * 0.5)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width * 0.5, box.y + box.height * 0.55)
  await page.mouse.up()
  await page.waitForTimeout(300)

  // ① 未保存必须被挡住。导出取的是库里的内容，直接导会拿到一份**缺东西但完全
  //    正常**的文件——本项目最贵的那类 bug。
  await page.getByTestId('export-nifti').click()
  const msg = page.getByTestId('export-msg')
  await expect(msg).toBeVisible({ timeout: 10_000 })
  await expect(msg).toContainText('尚未保存')
  console.log('未保存拦截:', (await msg.textContent())?.replace(/\s+/g, ' ').trim())

  // --- 保存后再导出 ---
  await msg.getByText('知道了').click()
  await page.getByRole('button', { name: /保存全部/ }).click()
  await expect(page.getByText(/已保存/)).toBeVisible({ timeout: 20_000 })
  await page.waitForTimeout(500)

  // ② 该任务没过 QA → 后端 409 → **不自动降级**，弹出显式确认
  const dl = page.waitForEvent('download', { timeout: 30_000 })
  await page.getByTestId('export-nifti').click()
  const prompt = page.getByTestId('draft-export-prompt')
  await expect(prompt, '没有快照时应提示而不是静默导出草稿').toBeVisible({ timeout: 15_000 })
  await expect(prompt).toContainText('FINALIZED 快照')
  console.log('草稿确认:', (await prompt.textContent())?.replace(/\s+/g, ' ').trim().slice(0, 80))

  // ③ 显式选择草稿导出 → 真的下载，且结果标明是草稿
  await prompt.getByRole('button', { name: '仍以草稿导出' }).click()
  const file = await dl
  console.log('下载文件:', file.suggestedFilename())
  expect(file.suggestedFilename(), '草稿文件名必须自带 draft 标记').toContain('draft')

  await expect(msg).toBeVisible({ timeout: 15_000 })
  const text = (await msg.textContent()) ?? ''
  console.log('导出结果:', text.replace(/\s+/g, ' ').trim())
  expect(text, '必须告知这是草稿').toContain('草稿')
  expect(text, '必须给出标签映射（一张不知道 2 是什么的标签图对下游没用）').toContain('标签')
})
