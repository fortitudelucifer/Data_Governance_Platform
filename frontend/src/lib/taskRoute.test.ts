import { describe, expect, it } from 'vitest'

import { taskRouteFor } from './taskRoute'

// 模态 → 工作台路由的真源。映射错了不会报错,只会把标注员领到错的工作台
// (体数据打开图片画布 = 一片空白),所以逐个钉死。

describe('taskRouteFor', () => {
  it('各模态各去各的工作台', () => {
    expect(taskRouteFor('image', 1)).toBe('/image-tasks/1')
    expect(taskRouteFor('audio', 2)).toBe('/audio-tasks/2')
    expect(taskRouteFor('video', 3)).toBe('/video-tasks/3')
    expect(taskRouteFor('volume', 4)).toBe('/volume-tasks/4') // Phase C:MPR 三视
    expect(taskRouteFor('wsi', 8)).toBe('/slide-tasks/8') // C1:病理绝不能落到 image 分支
  })

  it('未知/缺失模态回落到图片工作台(与既有行为一致)', () => {
    expect(taskRouteFor(null, 5)).toBe('/image-tasks/5')
    expect(taskRouteFor(undefined, 6)).toBe('/image-tasks/6')
    expect(taskRouteFor('mixed', 7)).toBe('/image-tasks/7')
  })
})
