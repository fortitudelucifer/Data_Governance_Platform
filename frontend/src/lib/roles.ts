// 权限能力层（R-01）。
//
// 页面该问「你**能不能**标注」，不该问「你**是不是** annotator」——前者在
// Phase C/D 加新模态时零改页面，后者一加角色就要改所有页面。所以：
//   · 角色数组是本文件的**私有实现细节**，不再导出；
//   · 页面只允许 import 下面的 can*() 能力函数（含 stores/auth.ts）。
//
// ⚠️ 真值的**真源是后端** backend/internal/server/routes.go 的 gate 组合
// （rolesAnnotate / rolesReview / RequireRole("admin")，routes.go:262-264 与各
// 路由处）。前端只做 UX 隐藏，后端永远是执行真源——**改 routes.go 的 gate
// 必须同步本文件**，真值表单测（roles.test.ts）会抓住漂移。

// 与 routes.go:262 rolesAnnotate 逐项对齐。
const ANNOTATOR_ROLES = ['admin', 'annotator', 'image_annotator', 'audio_annotator', 'video_annotator'] as const
// 与 routes.go:263 rolesReview 逐项对齐。
const REVIEWER_ROLES = ['admin', 'reviewer', 'image_reviewer', 'audio_reviewer', 'video_reviewer'] as const

type Role = string | null | undefined

function hasAnyRole(role: Role, roles: readonly string[]): boolean {
  return !!role && roles.includes(role)
}

/** 能不能标注（上传资产、保存草稿、提交、调用 AI 预标注）。后端 gate：rolesAnnotate。 */
export function canAnnotate(role: Role): boolean {
  return hasAnyRole(role, ANNOTATOR_ROLES)
}

/** 能不能审核（QA 通过/驳回、逐 track 裁决、返工 diff）。后端 gate：rolesReview。 */
export function canReview(role: Role): boolean {
  return hasAnyRole(role, REVIEWER_ROLES)
}

/** 能不能导出。多模态导出端点（export.video/audio/…）的后端 gate 是 rolesReview。 */
export function canExport(role: Role): boolean {
  return canReview(role)
}

/**
 * 能不能指派任务。后端 gate 是 RequireRole("admin")（routes.go /tasks/:id/assign、
 * /tasks/batch-assign）——**不是** reviewer：此前页面把指派 UI 展示给所有
 * reviewer，点下去后端 403，这里按真源收紧。
 */
export function canAssign(role: Role): boolean {
  return role === 'admin'
}

/** 能不能删除样本/文档（硬删除是 curation 动作）。后端 gate：rolesReview。 */
export function canDelete(role: Role): boolean {
  return canReview(role)
}

/** 能不能进入标注/审核工作区（导航与路由守卫用）。 */
export function canAnnotateOrReview(role: Role): boolean {
  return canAnnotate(role) || canReview(role)
}
