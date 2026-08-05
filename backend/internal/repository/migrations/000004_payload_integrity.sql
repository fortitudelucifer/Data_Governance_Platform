-- +goose Up
-- =============================================================================
-- 载荷层完整性约束(包 4 对抗式 review · 数据脊柱审计)。
--
-- 07 把幂等"升级成约束",但漏了几处:注释声称幂等、实现却是裸 INSERT / 缺唯一键 /
-- 缺外键。本迁移把这些声称补成 schema 属性——「重放安全 / 不丢更新 / 不悬空」不再靠
-- 代码自觉,而是编不出来。
--
-- ⚠️ 若既有库里已存在重复 (task_id,version)(说明历史上真发生过丢更新 / 双终稿),
-- 建唯一索引会失败——那是**特性**:它逼你先修数据,而不是让重复继续悄悄累积。
-- 开发/测试库是净的,不受影响。
-- =============================================================================

-- #8 终稿每 (task_id,version) 至多一份。InsertFinalAnnotation 注释写「idempotently
-- per (task_id,version)」却是裸 INSERT → 双 Finalize 造两行,ORDER BY version DESC
-- 在同版本间不确定。补唯一键 + 仓储改幂等返回既有 id。
CREATE UNIQUE INDEX ux_final_annotations_task_version ON final_annotations(task_id, version);

-- #4 人工标注每 (task_id,version) 至多一份。两个客户端都读到 v4、都写 v5:后者先把
-- 前者的 v5 置 inactive 再插自己的 v5 → 前者内容被静默归档、库里两行 v5。active 部分
-- 唯一索引挡不住(它只保证一份 active)。补 (task_id,version) 唯一键 → 后写者插入
-- 冲突、整个事务回滚(含那步 deactivate)→ 先写者的 v5 保住,后写者拿 409。
CREATE UNIQUE INDEX ux_human_annotations_task_version ON human_annotations(task_id, version);

-- #12 routing 结果的幂等键。run/ocr/vlm/seg/asr 都有部分唯一索引,唯独 routing 没有
-- (InsertRoutingResult 裸 INSERT)→ worker 状态 CAS 前崩溃 / 租约到期重试会插第二条,
-- FindLatestRoutingResult 的 ORDER BY version 在同版本间不确定。
CREATE UNIQUE INDEX ux_ai_results_routing ON ai_results(task_id, version) WHERE kind = 'routing';

-- #7 track 快照的 final_annotation_id 之前是裸 TEXT、无外键 → 可指向不存在的终稿,
-- 且删终稿不连带清快照。补外键 ON DELETE CASCADE:快照永远指向真实终稿,终稿没了
-- 快照跟着走(与既有 task→final→snapshot 的级联链一致)。
ALTER TABLE track_snapshots
    ADD CONSTRAINT fk_track_snapshots_final
    FOREIGN KEY (final_annotation_id) REFERENCES final_annotations(id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE track_snapshots DROP CONSTRAINT IF EXISTS fk_track_snapshots_final;
DROP INDEX IF EXISTS ux_ai_results_routing;
DROP INDEX IF EXISTS ux_human_annotations_task_version;
DROP INDEX IF EXISTS ux_final_annotations_task_version;
