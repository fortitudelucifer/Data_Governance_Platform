import { expect, test, type Page } from '@playwright/test'

// C3.25 测量工具。⚠️ 需要体数据任务，默认跳过。
// 本地：`TID=<体任务id> npx playwright test e2e/measure.spec.ts`
//
// 这条测试的重点不是"画出了一条线"，而是**毫米数在物理上是对的**。
// 画一条线永远会成功；数字错了没有任何东西会报错，而这个数会进医学报告。
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

/** 在画布上按**相对比例**点一下（避开手算显示坐标）。 */
async function clickAt(page: Page, fx: number, fy: number) {
  const box = (await page.locator('canvas').first().boundingBox())!
  await page.mouse.click(box.x + box.width * fx, box.y + box.height * fy)
  await page.waitForTimeout(150)
}

test('C3.25 长度测量：读数与体素间距一致', async ({ page }) => {
  test.skip(!TID, '需要体数据任务:TID=<id>')

  await login(page)
  await openTask(page)

  // 取几何真值：MNI152 是各向同性 0.737mm，但代码不该假设这一点。
  const meta = await page.evaluate(async () => {
    const r = await fetch('/api/assets/9/derivative/volume_meta', {
      headers: { Authorization: 'Bearer ' + (localStorage.getItem('token') ?? '') },
    })
    return r.ok ? ((await r.json()) as { spacing: number[]; dims: number[] }) : null
  })
  console.log('几何:', meta ? `dims=${meta.dims} spacing=${meta.spacing}` : '(取不到)')

  await page.getByTestId('measure-ruler').click()
  await expect(page.getByTestId('measure-ruler')).toContainText('进行中')

  // 点两下量一条横线：同一 v、不同 u。
  await clickAt(page, 0.35, 0.5)
  await clickAt(page, 0.65, 0.5)

  const list = page.getByTestId('measure-list')
  await expect(list).toBeVisible({ timeout: 10_000 })
  const text = (await list.textContent()) ?? ''
  console.log('测量清单:', text.replace(/\s+/g, ' ').trim())

  const m = /(\d+(?:\.\d+)?)\s*mm/.exec(text)
  expect(m, '清单里应有一条 mm 读数').not.toBeNull()
  const mm = parseFloat(m![1])

  // ⚠️ **必须用真实 spacing 算出期望值再比**，不能只给个"合理区间"。
  // 第一版写的是 20–120mm 的宽区间，结果变异测试当场戳穿：MNI152 是各向同性
  // 的（0.737 三轴相同），把 spacing 整个漏乘会得到 62.1（体素数当毫米），
  // **仍然落在区间内** —— 断言等于没写。
  // 各向异性的语义由单测覆盖（measure.test.ts 用 0.5/2.0/5.0）；这里的职责是
  // 验证页面到几何的接线，所以必须钉到数值。
  expect(meta, '取不到 volume_meta 就无法验算，测试没有意义').not.toBeNull()
  const canvasW = await page.locator('canvas').first().evaluate((c) => (c as HTMLCanvasElement).width)
  const expected = 0.3 * canvasW * meta!.spacing[0] // 点击跨度 0.65-0.35
  console.log(`读数 ${mm} mm · 期望 ${expected.toFixed(1)} mm（0.3 × ${canvasW} 体素 × ${meta!.spacing[0].toFixed(3)} mm）`)
  // 容差 3mm：点击落点会被像素取整，但漏乘 spacing 会差 16mm 以上。
  expect(Math.abs(mm - expected), `读数 ${mm} 与几何算出的 ${expected.toFixed(1)} 不符`).toBeLessThan(3)

  // 摘要必须带平面与层号——报告里"哪一层量的"和数值同等重要。
  expect(text).toMatch(/轴位 第 \d+ 层/)
})

test('C3.25 角度测量：直角读数接近 90°', async ({ page }) => {
  test.skip(!TID, '需要体数据任务:TID=<id>')

  await login(page)
  await openTask(page)
  await page.getByTestId('measure-angle').click()

  // 各向同性数据上，屏幕直角 = 物理直角。三点：右、顶点、下。
  await clickAt(page, 0.65, 0.5)
  await clickAt(page, 0.45, 0.5)
  await clickAt(page, 0.45, 0.72)

  const text = (await page.getByTestId('measure-list').textContent()) ?? ''
  console.log('角度清单:', text.replace(/\s+/g, ' ').trim())
  const m = /(\d+(?:\.\d+)?)°/.exec(text)
  expect(m, '应有角度读数').not.toBeNull()
  const deg = parseFloat(m![1])
  console.log(`读数 ${deg}°`)
  expect(deg, '屏幕上的直角在各向同性数据上应接近 90°').toBeGreaterThan(75)
  expect(deg).toBeLessThan(105)
})

test('C3.25 测量与涂抹互斥，且测量可删', async ({ page }) => {
  test.skip(!TID, '需要体数据任务:TID=<id>')

  await login(page)
  await openTask(page)
  await page.getByRole('button', { name: '+ 新建' }).click()
  await page.keyboard.press('Escape')

  // 开涂抹 → 开测量：涂抹应自动关掉（同一次点击不能既落笔又量点）
  await page.getByRole('button', { name: /开始涂抹/ }).click()
  await expect(page.getByRole('button', { name: /涂抹中/ })).toBeVisible()
  await page.getByTestId('measure-ruler').click()
  await expect(page.getByRole('button', { name: /开始涂抹/ }), '开测量后涂抹必须关掉').toBeVisible()

  await clickAt(page, 0.4, 0.45)
  await clickAt(page, 0.6, 0.45)
  const list = page.getByTestId('measure-list')
  await expect(list.locator('li')).toHaveCount(1)

  await list.locator('li').first().locator('button').last().click()
  await expect(list).toHaveCount(0) // 清单空了整个 ul 就不渲染
  const errs: string[] = []
  page.on('console', (e) => e.type() === 'error' && errs.push(e.text()))
  expect(errs).toEqual([])
})
