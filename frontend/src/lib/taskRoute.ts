// Maps an asset modality to its annotation-workspace route. Single source of
// truth so the per-modality workspace path is not hardcoded across pages.
// See plan_v2 执行方案-00-共用基座 T0.5.
export function taskRouteFor(modality: string | null | undefined, taskId: number | string): string {
  switch (modality) {
    case 'audio':
      return `/audio-tasks/${taskId}`
    case 'video':
      return `/video-tasks/${taskId}`
    // Phase C 医学影像(执行方案-04):体数据走 MPR 三视工作台。
    case 'volume':
      return `/volume-tasks/${taskId}`
    // 病理全切片(C1):深缩放查看器 + 区域标注叠加层。绝不能落到 image 分支——
    // ImageAnnotationPage 会整图载入,46000px 切片直接拖垮浏览器。
    case 'wsi':
      return `/slide-tasks/${taskId}`
    case 'image':
    default:
      return `/image-tasks/${taskId}`
  }
}
