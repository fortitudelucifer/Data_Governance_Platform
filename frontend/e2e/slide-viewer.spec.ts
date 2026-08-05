import { expect, test, type Page } from '@playwright/test'

// C1.2 病理查看器端到端。⚠️ 需要一个已派生的 WSI 资产（asset id 由 AID 环境变量给）。
// 本地：`AID=<wsi资产id> npx playwright test e2e/slide-viewer.spec.ts`
//
// 重点不是"页面渲染了"，而是**瓦片真的从后端加载出来了**——OSD 用 ajax 带 JWT 取
// 我们的 /assets/:id/tile 端点。若鉴权头没带上、或层映射请求了不存在的瓦片，
// 这里能看见（网络请求状态 + canvas 真的画了非黑像素）。
const AID = process.env.AID

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 20_000 })
}

test('C1.2 打开真实 WSI：瓦片经鉴权端点加载并绘制', async ({ page }) => {
  test.skip(!AID, '需要 WSI 资产:AID=<id>')

  // 收集瓦片请求，验证鉴权与状态。
  const tileReqs: { url: string; status: number }[] = []
  page.on('response', (r) => {
    const u = r.url()
    if (/\/assets\/\d+\/tile\/\d+\/\d+\/\d+/.test(u)) {
      tileReqs.push({ url: u, status: r.status() })
    }
  })

  await login(page)
  await page.goto(`/slides/${AID}`)

  // 头部信息栏出现（slide_meta 到手）
  await expect(page.getByText(/层 ·/)).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('slide-viewer')).toBeVisible()

  // 等瓦片加载：至少一个 tile 请求成功。
  await expect
    .poll(() => tileReqs.filter((t) => t.status === 200).length, { timeout: 30_000 })
    .toBeGreaterThan(0)

  const ok = tileReqs.filter((t) => t.status === 200)
  const bad = tileReqs.filter((t) => t.status >= 400)
  console.log(`瓦片请求：${ok.length} 成功 / ${bad.length} 失败（${tileReqs.length} 总）`)
  console.log('样例:', ok[0]?.url.replace(/^https?:\/\/[^/]+/, ''))

  // ① 没有 401——瓦片端点要鉴权(curl 无 token 实测 401),瓦片能 200 就证明 OSD 的
  //    ajaxHeaders 把 JWT 带上了。用状态断言而不是查请求头:Playwright 对 XHR 的
  //    Authorization 头暴露不稳定,而 200 vs 401 是端点行为的硬证据。
  const unauth = tileReqs.filter((t) => t.status === 401)
  expect(unauth.length, `有 ${unauth.length} 个瓦片 401——JWT 没带上`).toBe(0)

  // ② **没有 404**——层映射不该请求不存在的瓦片（tileExists 门的作用）
  const notFound = tileReqs.filter((t) => t.status === 404)
  expect(notFound.length, `不该请求不存在的瓦片，却有 ${notFound.length} 个 404`).toBe(0)

  // ③ 瓦片取回并**解码成图**（tile-loaded；OSD 6 WebGL 不发 tile-drawn）。
  //    这同时验证了 C1.1c 命门①:拼过 JPEGTables 头的瓦片能在真浏览器里解码。
  const viewer = page.getByTestId('slide-viewer')
  await expect
    .poll(async () => Number((await viewer.getAttribute('data-tiles-loaded')) ?? 0), { timeout: 15_000 })
    .toBeGreaterThan(0)
  // ④ 没有解码失败——拼错头的瓦片会走 tile-load-failed
  const failed = Number((await viewer.getAttribute('data-tile-failed')) ?? 0)
  expect(failed, `有 ${failed} 个瓦片解码失败——JPEGTables 拼接坏了？`).toBe(0)

  // ⑤ 缩放交互不报错（滚轮放大）
  const errs: string[] = []
  page.on('console', (m) => m.type() === 'error' && errs.push(m.text()))
  const box = (await page.getByTestId('slide-viewer').boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.wheel(0, -300) // 放大
  await page.waitForTimeout(1500)
  expect(errs, 'OSD 交互不应有控制台报错').toEqual([])
})

// 多层金字塔的**层切换**只有在多层切片上才被真正走到——单层切片（AID）永远只请求
// level 0。用大切片 CMU-1.svs（46000×32914，原生 ×1/×4/×16）验：从 fit（最粗原生层）
// 深缩放到全分辨率，应当**跨多个原生层**取瓦片、且不 404。层映射数学已在
// slideTileSource.test.ts 用这张片的真实数字 + 变异锁死;这条是它的浏览器端到端对照。
const AID_BIG = process.env.AID_BIG
test('C1.2 大切片深缩放：跨原生层取瓦片且不 404', async ({ page }) => {
  test.skip(!AID_BIG, '需要多层 WSI 资产:AID_BIG=<id>')

  // tile URL 里的第一个数字段 = **原生层索引**（getTileUrl 用 nativeIdx 拼）。
  const byLevel = new Map<number, number>()
  let notFound = 0
  page.on('response', (r) => {
    const m = /\/assets\/\d+\/tile\/(\d+)\/\d+\/\d+/.exec(r.url())
    if (!m) return
    if (r.status() === 404) notFound += 1
    if (r.status() === 200) {
      const lvl = Number(m[1])
      byLevel.set(lvl, (byLevel.get(lvl) ?? 0) + 1)
    }
  })

  await login(page)
  await page.goto(`/slides/${AID_BIG}`)
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  const box = (await page.getByTestId('slide-viewer').boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)

  // 深缩放到全分辨率：滚轮放大**直到全分辨率层(level 0)出现**再停,而不是猜固定
  // 次数(切片尺寸/视口大小一变，"够不够深"就飘)。上限兜底防死循环。
  for (let i = 0; i < 30 && (byLevel.get(0) ?? 0) === 0; i++) {
    await page.mouse.wheel(0, -500)
    await page.waitForTimeout(200)
  }
  // 全分辨率下平移，拉进未加载的 level 0 瓦片(证明平移也走对了层)。
  await page.mouse.down()
  await page.mouse.move(box.x + box.width / 2 - 200, box.y + box.height / 2 - 150, { steps: 8 })
  await page.mouse.up()
  await page.waitForTimeout(1200)

  // ① 深缩放后最细的原生层（level 0 = 全分辨率）被请求了——否则「放大到全分辨率」
  //    这句是假的（层映射把全分辨率映丢了会在这里现形）。
  await expect.poll(() => byLevel.get(0) ?? 0, { timeout: 15_000 }).toBeGreaterThan(0)
  // ② 至少跨了两个原生层（fit 用粗层、放大用细层）——单层切片给不出这个证据，
  //    这正是本用例相对于上一条的增量:层切换在真浏览器里真的发生了。
  expect(byLevel.size, `只请求了原生层 [${[...byLevel.keys()].sort()}]，没发生层切换`).toBeGreaterThan(1)
  // ③ 全程无 404——中间 DZI 层（无原生对应）必须被 tileExists 门挡住，绝不落到端点。
  expect(notFound, `深缩放中有 ${notFound} 个瓦片 404——tileExists 门漏了中间层`).toBe(0)
})
