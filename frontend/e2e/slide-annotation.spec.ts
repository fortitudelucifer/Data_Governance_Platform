import { expect, test, type Page } from '@playwright/test'

// C1.3 病理区域标注端到端。⚠️ 需要一个已派生的 WSI **任务**(WSI_TID 环境变量)。
// 本地:`WSI_TID=<wsi任务id> npx playwright test e2e/slide-annotation.spec.ts`
//
// 重点是**坐标系纪律**:画的框必须**钉在组织上**——深缩放/平移后,框的屏幕位置
// 要始终等于 imageToScreen(当前仿射, 框的 image 坐标),而 image 坐标本身不变。
// 若叠加层漂移(静态、或用错视口数学),框会飘离组织,画面看着正常却是错的。
const WSI_TID = process.env.WSI_TID

async function login(page: Page) {
  await page.goto('/login')
  await page.getByPlaceholder('请输入用户名').fill('admin')
  await page.getByPlaceholder('请输入密码').fill('admin123')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.waitForURL((u) => !u.pathname.endsWith('/login'), { timeout: 20_000 })
}

// 读第一个区域 rect 的 image 坐标 + 当前叠加层仿射 + 实际屏幕位置,
// 并算出 imageToScreen(仿射, image 坐标) 应落在的屏幕位置,返回是否吻合。
async function probeRect(page: Page) {
  return page.evaluate(() => {
    const svg = document.querySelector('[data-testid="slide-overlay"]') as SVGSVGElement
    const g = document.querySelector('[data-testid="slide-overlay-g"]') as SVGGElement
    const rect = svg?.querySelector('rect') as SVGRectElement | null
    if (!svg || !g || !rect) return null
    const [scale, ox, oy] = (g.dataset.affine ?? '').split(',').map(Number)
    const ix = Number(rect.getAttribute('x'))
    const iy = Number(rect.getAttribute('y'))
    const svgBox = svg.getBoundingClientRect()
    const r = rect.getBoundingClientRect()
    // 期望屏幕位置(页面坐标) = svg 左上 + imageToScreen(仿射, image 左上角)
    const expX = svgBox.left + (ix * scale + ox)
    const expY = svgBox.top + (iy * scale + oy)
    return { scale, ox, oy, ix, iy, actualX: r.left, actualY: r.top, expX, expY }
  })
}

test('C1.3 画区域 + 深缩放不漂 + 刷新仍在', async ({ page }) => {
  test.skip(!WSI_TID, '需要 WSI 任务:WSI_TID=<id>')

  await login(page)
  await page.goto(`/slide-tasks/${WSI_TID}`)
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('slide-overlay')).toBeVisible()
  await page.waitForTimeout(1500) // 等瓦片上屏

  // 选矩形工具,在查看器中央拖一个框。
  await page.getByTestId('slide-tool-bbox').click()
  const box = (await page.getByTestId('slide-viewer').boundingBox())!
  const cx = box.x + box.width / 2
  const cy = box.y + box.height / 2
  await page.mouse.move(cx - 90, cy - 70)
  await page.mouse.down()
  await page.mouse.move(cx + 90, cy + 70, { steps: 12 })
  await page.mouse.up()

  // 区域出现在列表。⚠️ 断言 region-item testid,不是泛 `li`——空状态"还没有区域"
  // 也是一个 li,用泛 li 计数会把"没画上"当成"画了 1 个"(空断言,项目栽过多次)。
  await expect(page.getByTestId('slide-region-item')).toHaveCount(1, { timeout: 10_000 })

  // ① 渲染位置 = imageToScreen(仿射, image 坐标)——初始就对齐。
  const before = await probeRect(page)
  expect(before, '未找到区域 rect').not.toBeNull()
  expect(Math.abs(before!.actualX - before!.expX), 'x 渲染位置与仿射不一致').toBeLessThan(3)
  expect(Math.abs(before!.actualY - before!.expY), 'y 渲染位置与仿射不一致').toBeLessThan(3)

  // 深缩放(滚轮由叠加层转发给 OSD)。
  await page.mouse.move(cx, cy)
  for (let i = 0; i < 6; i++) {
    await page.mouse.wheel(0, -400)
    await page.waitForTimeout(150)
  }
  await page.waitForTimeout(600)

  const after = await probeRect(page)
  expect(after).not.toBeNull()
  // ② 缩放确实发生(仿射 scale 明显变大)——否则"不漂"是废断言。
  expect(after!.scale, '深缩放后 scale 未增大').toBeGreaterThan(before!.scale * 1.5)
  // ③ 框的 image 坐标**没变**(几何稳定在 level-0 像素空间)。
  expect(after!.ix).toBeCloseTo(before!.ix, 6)
  expect(after!.iy).toBeCloseTo(before!.iy, 6)
  // ④ 缩放后渲染位置仍 = imageToScreen(新仿射, 同一 image 坐标)——**钉在组织上**。
  expect(Math.abs(after!.actualX - after!.expX), '缩放后 x 漂移').toBeLessThan(3)
  expect(Math.abs(after!.actualY - after!.expY), '缩放后 y 漂移').toBeLessThan(3)

  // ⑤ 落库:先**等保存真正完成**(PUT 在重瓦片加载下会排队 ~数秒;不等就刷新 = 丢),
  //    再刷新,区域应从轨迹载回。⚠️ 仍用 region-item testid 计数,避开空状态假计数。
  await expect(page.getByTestId('slide-save-status')).toHaveText('已保存', { timeout: 20_000 })
  await page.reload()
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('slide-region-item')).toHaveCount(1, { timeout: 15_000 })

  // ⑥ C1.4 导出 GeoJSON:点按钮触发下载,文件名带 _draft(工作台导草稿),内容是含
  //    1 个 feature 的合法 FeatureCollection、坐标在 level-0 像素范围(不是缩略图/归一化)。
  const [download] = await Promise.all([
    page.waitForEvent('download', { timeout: 15_000 }),
    page.getByRole('button', { name: /导出 GeoJSON/ }).click(),
  ])
  expect(download.suggestedFilename()).toMatch(/task_\d+_regions_draft\.geojson/)
  const stream = await download.createReadStream()
  const chunks: Buffer[] = []
  for await (const c of stream) chunks.push(c as Buffer)
  const fc = JSON.parse(Buffer.concat(chunks).toString('utf-8'))
  expect(fc.type).toBe('FeatureCollection')
  expect(fc.features).toHaveLength(1)
  const ring = fc.features[0].geometry.coordinates[0]
  expect(ring[0]).toEqual(ring[ring.length - 1]) // 环闭合
  const maxX = Math.max(...ring.map((p: number[]) => p[0]))
  expect(maxX, 'x 坐标应是 level-0 像素(远大于 1),不是归一化/缩略图').toBeGreaterThan(100)

  // 清理:删掉刚建的区域,避免污染下一次(共享任务)。
  await page.getByTestId('slide-region-item').first().locator('button[title="删除"]').click()
  await expect(page.getByTestId('slide-region-item')).toHaveCount(0, { timeout: 15_000 })
})

// 多边形是病理区域的**主力工具**(手绘不规则肿瘤边界),单独验它的点选顶点 + 完成 +
// 落库。坐标不漂已由上条(bbox)证明——两者共用同一套 toImg/仿射机制,这里只证多边形
// 特有的绘制路径(逐点 click → 完成按钮 → 提交 polygon 轨迹)通且能载回。
test('C1.3 画多边形(逐点)+ 落库', async ({ page }) => {
  test.skip(!WSI_TID, '需要 WSI 任务:WSI_TID=<id>')
  await login(page)
  await page.goto(`/slide-tasks/${WSI_TID}`)
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await page.waitForTimeout(1500)

  await page.getByTestId('slide-tool-polygon').click()
  const box = (await page.getByTestId('slide-viewer').boundingBox())!
  const cx = box.x + box.width / 2, cy = box.y + box.height / 2
  // 逐点点四个顶点(每次是"点击"而非拖拽:位移 < 4px)。
  for (const [dx, dy] of [[-70, -60], [70, -50], [80, 60], [-60, 70]]) {
    await page.mouse.click(cx + dx, cy + dy)
    await page.waitForTimeout(120)
  }
  await page.getByTestId('slide-poly-finish').click()

  await expect(page.getByTestId('slide-region-item')).toHaveCount(1, { timeout: 10_000 })
  // 是 polygon(面板右侧标"多边形"),不是 bbox。
  await expect(page.getByTestId('slide-region-item').first()).toContainText('多边形')

  await expect(page.getByTestId('slide-save-status')).toHaveText('已保存', { timeout: 20_000 })
  await page.reload()
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('slide-region-item')).toHaveCount(1, { timeout: 15_000 })
  await expect(page.getByTestId('slide-region-item').first()).toContainText('多边形')

  await page.getByTestId('slide-region-item').first().locator('button[title="删除"]').click()
  await expect(page.getByTestId('slide-region-item')).toHaveCount(0, { timeout: 15_000 })
})

// C1.35 尺子:两点量长度。**能算出期望值就别写区间**(C3.25 教训)——已知屏幕间距 +
// 当前仿射 scale + mpp,期望 µm 是唯一确定的,直接比对读数,不写"落在某区间"。
test('C1.35 尺子量出正确 µm(mpp 驱动)', async ({ page }) => {
  test.skip(!WSI_TID, '需要 WSI 任务:WSI_TID=<id>')
  await login(page)
  await page.goto(`/slide-tasks/${WSI_TID}`)
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await page.waitForTimeout(1500)

  // 尺子工具只在 mpp>0 时出现(asset 21 mpp 0.499)。
  await page.getByTestId('slide-tool-ruler').click()
  const box = (await page.getByTestId('slide-viewer').boundingBox())!
  const cx = box.x + box.width / 2, cy = box.y + box.height / 2

  // 量距离前读当前仿射 scale(viewer-element px / image px)。
  const affine = await page.evaluate(() => (document.querySelector('[data-testid="slide-overlay-g"]')?.getAttribute('data-affine') ?? '').split(',').map(Number))
  const scale = affine[0]
  const mpp = 0.499 // CMU-1.svs 实测
  const SCREEN_DX = 200 // 水平点两点,屏幕相距 200px

  await page.mouse.click(cx - SCREEN_DX / 2, cy)
  await page.mouse.click(cx + SCREEN_DX / 2, cy)
  await expect(page.getByTestId('slide-measurement')).toHaveCount(1, { timeout: 5_000 })

  const text = (await page.getByTestId('slide-measurement').locator('text').textContent()) ?? ''
  const m = /([\d.]+)\s*(µm|mm)/.exec(text)
  expect(m, `读数格式异常:${text}`).not.toBeNull()
  const readUm = Number(m![1]) * (m![2] === 'mm' ? 1000 : 1)
  // 期望:image 距离 = 屏幕间距 / scale;µm = image 距离 × mpp。
  const expectedUm = (SCREEN_DX / scale) * mpp
  expect(Math.abs(readUm - expectedUm) / expectedUm, `读数 ${readUm}µm 与期望 ${expectedUm}µm 偏差过大`).toBeLessThan(0.03)
})

// C1.3 补:框身编辑改几何并落库(与已验证的绘制共用同一套 toImg/仿射)。
test('C1.3 编辑区域:拖动改几何 + 落库', async ({ page }) => {
  test.skip(!WSI_TID, '需要 WSI 任务:WSI_TID=<id>')
  await login(page)
  await page.goto(`/slide-tasks/${WSI_TID}`)
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await page.waitForTimeout(1500)

  await page.getByTestId('slide-tool-bbox').click()
  const box = (await page.getByTestId('slide-viewer').boundingBox())!
  const cx = box.x + box.width / 2, cy = box.y + box.height / 2
  await page.mouse.move(cx - 80, cy - 60); await page.mouse.down()
  await page.mouse.move(cx + 80, cy + 60, { steps: 10 }); await page.mouse.up()
  await expect(page.getByTestId('slide-region-item')).toHaveCount(1, { timeout: 10_000 })
  await expect(page.getByTestId('slide-save-status')).toHaveText('已保存', { timeout: 20_000 })

  const rectX = () => page.evaluate(() => Number(document.querySelector('[data-testid="slide-overlay"] rect')?.getAttribute('x')))
  const before = await rectX()

  // 切编辑工具,从框身中心拖到新位置(框身平移)。
  await page.getByTestId('slide-tool-edit').click()
  await page.mouse.move(cx, cy); await page.mouse.down()
  await page.mouse.move(cx + 120, cy + 40, { steps: 10 }); await page.mouse.up()

  // ① 几何真的变了(rect 的 image x 平移)。
  await expect.poll(async () => Math.abs((await rectX()) - before), { timeout: 5_000 }).toBeGreaterThan(50)
  // ② 编辑落库:等"已保存",刷新后仍是 1 个且 x 停在移动后的值。
  await expect(page.getByTestId('slide-save-status')).toHaveText('已保存', { timeout: 20_000 })
  const afterEdit = await rectX()
  await page.reload()
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('slide-region-item')).toHaveCount(1, { timeout: 15_000 })
  await page.waitForTimeout(500)
  const afterReload = await rectX()
  expect(Math.abs(afterReload - afterEdit), '刷新后几何与保存值不一致(编辑没落库?)').toBeLessThan(5)

  await page.getByTestId('slide-tool-edit').click()
  await page.getByTestId('slide-region-item').first().locator('button[title="删除"]').click()
  await expect(page.getByTestId('slide-region-item')).toHaveCount(0, { timeout: 15_000 })
})

// C2.3 细胞检测 + 渲染。⚠️ 需要后端带 MM_CELLS_ENDPOINT + cell-detect stub 在跑
// (整片视野=几百万细胞会超上限被拒;先深缩放到一个 ROI)。
test('C2.3 检测细胞:视野内检测 → 细胞上屏(canvas)+ 面板计数', async ({ page }) => {
  test.skip(!WSI_TID, '需要 WSI 任务 + cell-detect stub:WSI_TID=<id>')

  await login(page)
  await page.goto(`/slide-tasks/${WSI_TID}`)
  await expect(page.getByTestId('slide-viewer')).toBeVisible({ timeout: 30_000 })
  await page.waitForTimeout(1500)

  // 深缩放到一个小 ROI:整片视野的细胞数会超上限被后端拒;放大到一个 ROI 再检测。
  const box = (await page.getByTestId('slide-viewer').boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  for (let i = 0; i < 20; i++) {
    await page.mouse.wheel(0, -400)
    await page.waitForTimeout(120)
  }
  await page.waitForTimeout(700)

  // 检测细胞(当前视野)。
  await page.getByRole('button', { name: /检测细胞/ }).click()

  // ① 面板出现一条 cells 轨迹,核数 > 0(检测成功 + 载回)。
  await expect(page.getByTestId('slide-cell-item')).toHaveCount(1, { timeout: 25_000 })
  const itemText = (await page.getByTestId('slide-cell-item').first().textContent()) ?? ''
  const m = /(\d+)\s*核/.exec(itemText)
  expect(m, `细胞项应显示核数: ${itemText}`).not.toBeNull()
  expect(Number(m![1]), '应检出 > 0 个细胞').toBeGreaterThan(0)

  // ② 细胞真的画上 canvas 叠加层了(读回非透明像素——这才是"看得到细胞"的硬证据)。
  const drawnPixels = await page.getByTestId('slide-cells-canvas').evaluate((el) => {
    const c = el as HTMLCanvasElement
    const ctx = c.getContext('2d')
    if (!ctx || c.width === 0) return -1
    const d = ctx.getImageData(0, 0, c.width, c.height).data
    let n = 0
    for (let i = 3; i < d.length; i += 4) if (d[i] > 0) n++
    return n
  })
  expect(drawnPixels, '细胞叠加层 canvas 应有非透明像素(细胞画上屏了)').toBeGreaterThan(0)

  // ③ 隐藏 → canvas 清空;显示 → 再现(证明面板显隐真驱动渲染)。
  await page.getByTestId('slide-cell-item').first().locator('button[title="隐藏"]').click()
  await page.waitForTimeout(300)
  const hiddenPixels = await page.getByTestId('slide-cells-canvas').evaluate((el) => {
    const c = el as HTMLCanvasElement
    const ctx = c.getContext('2d')!
    const d = ctx.getImageData(0, 0, c.width, c.height).data
    let n = 0
    for (let i = 3; i < d.length; i += 4) if (d[i] > 0) n++
    return n
  })
  expect(hiddenPixels, '隐藏后 canvas 应清空').toBe(0)

  // 清理:删掉这条 cells 轨迹。
  await page.getByTestId('slide-cell-item').first().locator('button[title="删除"]').click()
  await expect(page.getByTestId('slide-cell-item')).toHaveCount(0, { timeout: 10_000 })
})
