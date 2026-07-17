import { seed } from './seed'

// Playwright globalSetup：跑测试前先把数据备齐（幂等）。
// 本地和 CI 走同一条路径——CI 上是空库，从零建；本地已有就复用。
export default async function globalSetup() {
  const t0 = Date.now()
  const state = await seed()
  console.log(
    `[e2e] 播种完成 (${Date.now() - t0}ms): dataset=${state.videoDatasetId} ` +
      `reviewTask=${state.reviewTaskId} editTask=${state.editTaskId} ` +
      `imageTask=${state.imageTaskId} frames=${state.frameCount}`,
  )
}
