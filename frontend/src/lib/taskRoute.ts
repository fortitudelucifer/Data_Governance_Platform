// Maps an asset modality to its annotation-workspace route. Single source of
// truth so the per-modality workspace path is not hardcoded across pages.
// See plan_v2 执行方案-00-共用基座 T0.5.
export function taskRouteFor(modality: string | null | undefined, taskId: number | string): string {
  switch (modality) {
    case 'audio':
      return `/audio-tasks/${taskId}`
    case 'video':
      return `/video-tasks/${taskId}`
    case 'image':
    default:
      return `/image-tasks/${taskId}`
  }
}
