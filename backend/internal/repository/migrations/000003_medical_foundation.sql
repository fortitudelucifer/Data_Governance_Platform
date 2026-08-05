-- +goose Up
-- =============================================================================
-- 执行方案-04 · C0.1:医学影像地基(Phase C,2026-07-18 拍板"先 3D 后病理")。
--
-- datasets.data_source 是 DeidGate(fail-closed 导入闸门)的来源声明:
--   ''                    — 非医学数据集,闸门不关心(历史数据集全部落在这里);
--   'public_deidentified' — 公开已去标识数据集:医学导入直通,但走同一条审计路径;
--   'clinical'            — 临床真实数据:去标识管线配置好之前一律拒收。
--     真实病人数据是明确的未来需求(04《C-Q1 预留设计》),接入时是"打开闸门",
--     不是"重新修渠"——闸门、审计、UID 映射接口从本迁移起就存在。
--
-- 刻意不做 CHECK 枚举(与 asset_derivatives.kind 同一决定):来源种类扩展
-- (如未来的 'clinical_deidentified_batch')不需要二次迁移;非法值由应用层
-- DeidGate 拒绝——fail-closed 语义下,不认识的值 = 拒收,比 DB 枚举更严。
-- =============================================================================

ALTER TABLE datasets ADD COLUMN data_source VARCHAR(32) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE datasets DROP COLUMN data_source;
