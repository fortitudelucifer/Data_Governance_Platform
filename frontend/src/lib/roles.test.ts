import { describe, expect, it } from 'vitest'
import { canAnnotate, canAnnotateOrReview, canAssign, canDelete, canExport, canReview } from './roles'

// 能力 × 角色真值表（R-01）。
//
// 真源是 backend/internal/server/routes.go 的 gate 组合：
//   rolesAnnotate（routes.go:262）→ canAnnotate
//   rolesReview（routes.go:263）→ canReview / canExport / canDelete
//   RequireRole("admin")（/tasks/:id/assign、/tasks/batch-assign）→ canAssign
// 改 routes.go 的 gate 必须同步 roles.ts 与本表——本表的意义就是抓住这种漂移。
//
// 变异验证（写测试的自我要求，抓空断言）：把 roles.ts 的 canAnnotate 改成恒
// 返回 true → 下表 reviewer/未知角色 行必须把本测试打红。已于 2026-07-15 实际
// 验证过一次。

type Row = [role: string, annotate: boolean, review: boolean, exportt: boolean, assign: boolean, del: boolean]

const TABLE: Row[] = [
  //  role                标注     审核     导出     指派     删除
  ['admin',               true,    true,    true,    true,    true],
  ['annotator',           true,    false,   false,   false,   false],
  ['image_annotator',     true,    false,   false,   false,   false],
  ['audio_annotator',     true,    false,   false,   false,   false],
  ['video_annotator',     true,    false,   false,   false,   false],
  ['reviewer',            false,   true,    true,    false,   true],
  ['image_reviewer',      false,   true,    true,    false,   true],
  ['audio_reviewer',      false,   true,    true,    false,   true],
  ['video_reviewer',      false,   true,    true,    false,   true],
  // 未知角色 / 空值一律无能力（宁可少显示，也不给出点了必 403 的按钮）。
  ['viewer',              false,   false,   false,   false,   false],
  ['medical_annotator',   false,   false,   false,   false,   false],
  ['',                    false,   false,   false,   false,   false],
]

describe('roles capability truth table', () => {
  it.each(TABLE)('%s', (role, annotate, review, exportt, assign, del) => {
    expect(canAnnotate(role), `canAnnotate(${role})`).toBe(annotate)
    expect(canReview(role), `canReview(${role})`).toBe(review)
    expect(canExport(role), `canExport(${role})`).toBe(exportt)
    expect(canAssign(role), `canAssign(${role})`).toBe(assign)
    expect(canDelete(role), `canDelete(${role})`).toBe(del)
    expect(canAnnotateOrReview(role), `canAnnotateOrReview(${role})`).toBe(annotate || review)
  })

  it('null / undefined 一律无能力', () => {
    for (const r of [null, undefined]) {
      expect(canAnnotate(r)).toBe(false)
      expect(canReview(r)).toBe(false)
      expect(canExport(r)).toBe(false)
      expect(canAssign(r)).toBe(false)
      expect(canDelete(r)).toBe(false)
      expect(canAnnotateOrReview(r)).toBe(false)
    }
  })
})
