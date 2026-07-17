-- +goose Up
-- =============================================================================
-- 执行方案-07 · 载荷层表(提升列 + jsonb payload)。
--
-- 设计范式(每张表相同):**提升列 + payload jsonb**。
--   · payload = 整个文档(json tag 序列化),是读路径的唯一真源——读 = 反序列化 payload;
--   · 提升列 = 查询/外键/唯一键/排序用的副本,仓储层写入时与 payload 同步维护
--     (字段级更新走 `payload = payload || delta` 顶层合并,两边一条 UPDATE 保持一致);
--   · id 为应用生成的 24 位 hex 文本(沿用历史 id 形状)——API 出参形状零变化。
--
-- 外键即墓志铭:标注/track/快照按整数 id 引用资产与任务,曾经只是应用层的
-- 约定俗成——清一半存储就"嫁接"(06·P-H1)。现在它们是真外键 ON DELETE CASCADE,
-- 嫁接在 schema 层面不可能发生。trace_logs 刻意**无外键**:观测数据要活得比
-- 它指向的对象久,且文本线调用没有任务(task_id=0)。
--
-- jsonb 内部键一律不建 GIN:所有查询路径都走提升列(列不够就提升,
-- 不用 jsonb 路径查询凑合——它会静默慢)。
-- =============================================================================

-- ---- AI 结果族(六个集合合一,kind 区分;kind 无 CHECK,与 asset_derivatives 同理)
CREATE TABLE ai_results (
    id              TEXT PRIMARY KEY,
    kind            VARCHAR(16) NOT NULL, -- routing | run | ocr | vlm | seg | asr
    task_id         BIGINT      NOT NULL REFERENCES annotation_tasks(id) ON DELETE CASCADE,
    asset_id        BIGINT      NOT NULL REFERENCES assets(id)           ON DELETE CASCADE,
    run_id          TEXT        NOT NULL DEFAULT '',
    capability_type VARCHAR(64) NOT NULL DEFAULT '',
    version         INTEGER     NOT NULL DEFAULT 0, -- routing 结果取最高版本
    started_at      timestamptz,                    -- run 排序用
    payload         JSONB       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_ai_results_task_kind ON ai_results(task_id, kind, created_at DESC);
-- AIRun 幂等键 (task_id, capability_type, run_id)(plan_v1/05 §2.2 + ADR-08)
CREATE UNIQUE INDEX ux_ai_results_run ON ai_results(task_id, capability_type, run_id)
    WHERE kind = 'run';
-- OCR/VLM/Seg/ASR 结果幂等键 (task_id, run_id)
CREATE UNIQUE INDEX ux_ai_results_result ON ai_results(kind, task_id, run_id)
    WHERE kind IN ('ocr', 'vlm', 'seg', 'asr');

-- ---- 观测:逐调用 trace(无 FK;文本线 task_id=0)
CREATE TABLE trace_logs (
    id              TEXT PRIMARY KEY,
    trace_id        TEXT        NOT NULL DEFAULT '',
    task_id         BIGINT      NOT NULL DEFAULT 0,
    run_id          TEXT        NOT NULL DEFAULT '',
    capability_type VARCHAR(64) NOT NULL DEFAULT '',
    provider        VARCHAR(100) NOT NULL DEFAULT '',
    model           VARCHAR(128) NOT NULL DEFAULT '',
    status          VARCHAR(16)  NOT NULL DEFAULT '',
    latency_ms      BIGINT      NOT NULL DEFAULT 0,
    payload         JSONB       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_trace_logs_task    ON trace_logs(task_id, created_at);
CREATE INDEX idx_trace_logs_created ON trace_logs(created_at);

-- ---- 人工标注(每任务至多一份 active——曾是"UpdateMany 再 Insert"的应用层约定,
--      现在是部分唯一索引,双写竞态从结构上关死)
CREATE TABLE human_annotations (
    id         TEXT PRIMARY KEY,
    task_id    BIGINT      NOT NULL REFERENCES annotation_tasks(id) ON DELETE CASCADE,
    asset_id   BIGINT      NOT NULL REFERENCES assets(id)           ON DELETE CASCADE,
    is_active  BOOLEAN     NOT NULL DEFAULT TRUE,
    version    INTEGER     NOT NULL DEFAULT 1,
    qa_status  VARCHAR(16) NOT NULL DEFAULT 'draft',
    payload    JSONB       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ux_human_annotations_active ON human_annotations(task_id) WHERE is_active;
CREATE INDEX idx_human_annotations_task ON human_annotations(task_id, version DESC);

-- ---- 终稿标注(不可变;导出一致性的关系半边)
CREATE TABLE final_annotations (
    id         TEXT PRIMARY KEY,
    task_id    BIGINT      NOT NULL REFERENCES annotation_tasks(id) ON DELETE CASCADE,
    asset_id   BIGINT      NOT NULL REFERENCES assets(id)           ON DELETE CASCADE,
    dataset_id BIGINT      NOT NULL REFERENCES datasets(id)         ON DELETE CASCADE,
    version    INTEGER     NOT NULL DEFAULT 1,
    payload    JSONB       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_final_annotations_task    ON final_annotations(task_id, version DESC);
CREATE INDEX idx_final_annotations_dataset ON final_annotations(dataset_id, created_at);

-- ---- 视频 track(CVAT 式 track+keyframe,keyframes 在 payload 里)
-- UNIQUE(task_id, track_id) WHERE is_active:导出以 track_id 为键,重复的活跃
-- track_id 会把两个物体静默合并成一条轨迹(B3 修过的真 bug)——曾靠
-- ActiveTrackNumberTaken 先查后写兜着,现在是 schema 的属性。
CREATE TABLE annotation_tracks (
    id            TEXT PRIMARY KEY,
    task_id       BIGINT      NOT NULL REFERENCES annotation_tasks(id) ON DELETE CASCADE,
    dataset_id    BIGINT      NOT NULL REFERENCES datasets(id)         ON DELETE CASCADE,
    asset_id      BIGINT      NOT NULL REFERENCES assets(id)           ON DELETE CASCADE,
    track_id      INTEGER     NOT NULL,
    label         VARCHAR(128) NOT NULL DEFAULT '',
    source        VARCHAR(8)  NOT NULL DEFAULT 'human', -- ai | human
    is_active     BOOLEAN     NOT NULL DEFAULT TRUE,
    version       INTEGER     NOT NULL DEFAULT 1,       -- 乐观锁,stale → 409
    review_status VARCHAR(16),                          -- NULL=未裁决 | passed | rejected
    payload       JSONB       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ux_tracks_task_num_active ON annotation_tracks(task_id, track_id) WHERE is_active;
CREATE INDEX idx_tracks_task_active    ON annotation_tracks(task_id, is_active);
CREATE INDEX idx_tracks_dataset_label  ON annotation_tracks(dataset_id, label);
CREATE INDEX idx_tracks_source_dataset ON annotation_tracks(source, dataset_id);

-- ---- FINALIZED track 快照(导出唯一真源;幂等键让 finalize 可安全重放)
CREATE TABLE track_snapshots (
    id                  TEXT PRIMARY KEY,
    task_id             BIGINT      NOT NULL REFERENCES annotation_tasks(id) ON DELETE CASCADE,
    dataset_id          BIGINT      NOT NULL REFERENCES datasets(id)         ON DELETE CASCADE,
    asset_id            BIGINT      NOT NULL REFERENCES assets(id)           ON DELETE CASCADE,
    final_annotation_id TEXT        NOT NULL,
    track_id            INTEGER     NOT NULL,
    label               VARCHAR(128) NOT NULL DEFAULT '',
    finalized_at        timestamptz NOT NULL DEFAULT now(),
    payload             JSONB       NOT NULL,
    UNIQUE (final_annotation_id, track_id)
);

CREATE INDEX idx_track_snapshots_task    ON track_snapshots(task_id, finalized_at DESC);
CREATE INDEX idx_track_snapshots_dataset ON track_snapshots(dataset_id, task_id, track_id);

-- ---- 提交轮次(返工 diff 的原料;(task, round) 唯一让双提交竞态变冲突而非重复)
CREATE TABLE track_rounds (
    id           TEXT PRIMARY KEY,
    task_id      BIGINT      NOT NULL REFERENCES annotation_tasks(id) ON DELETE CASCADE,
    round        INTEGER     NOT NULL,
    track_count  INTEGER     NOT NULL DEFAULT 0,
    submitted_at timestamptz NOT NULL DEFAULT now(),
    payload      JSONB       NOT NULL,
    UNIQUE (task_id, round)
);

-- ---- 审核批注(锚点 frame/track/time 在提升列,跳转查询用)
CREATE TABLE review_comments (
    id         TEXT PRIMARY KEY,
    task_id    BIGINT      NOT NULL REFERENCES annotation_tasks(id) ON DELETE CASCADE,
    dataset_id BIGINT      NOT NULL REFERENCES datasets(id)         ON DELETE CASCADE,
    asset_id   BIGINT      NOT NULL REFERENCES assets(id)           ON DELETE CASCADE,
    status     VARCHAR(16) NOT NULL DEFAULT 'open', -- open | resolved
    author_id  BIGINT      NOT NULL DEFAULT 0,
    payload    JSONB       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_review_comments_task ON review_comments(task_id, created_at);
CREATE INDEX idx_review_comments_open ON review_comments(task_id) WHERE status = 'open';

-- ---- 文本多模型候选 / Judge 运行(按 run_id 幂等)
CREATE TABLE text_ai_candidates (
    id         TEXT PRIMARY KEY,
    run_id     TEXT        NOT NULL UNIQUE,
    dataset_id BIGINT      NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    doc_key    VARCHAR(255) NOT NULL,
    payload    JSONB       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_text_candidates_doc ON text_ai_candidates(dataset_id, doc_key, created_at DESC);

CREATE TABLE text_ai_judge_runs (
    id         TEXT PRIMARY KEY,
    run_id     TEXT        NOT NULL UNIQUE,
    dataset_id BIGINT      NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    doc_key    VARCHAR(255) NOT NULL,
    payload    JSONB       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_text_judges_doc ON text_ai_judge_runs(dataset_id, doc_key, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS text_ai_judge_runs;
DROP TABLE IF EXISTS text_ai_candidates;
DROP TABLE IF EXISTS review_comments;
DROP TABLE IF EXISTS track_rounds;
DROP TABLE IF EXISTS track_snapshots;
DROP TABLE IF EXISTS annotation_tracks;
DROP TABLE IF EXISTS final_annotations;
DROP TABLE IF EXISTS human_annotations;
DROP TABLE IF EXISTS trace_logs;
DROP TABLE IF EXISTS ai_results;
