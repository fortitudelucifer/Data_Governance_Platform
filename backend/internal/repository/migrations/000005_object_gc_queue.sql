-- +goose Up
-- =============================================================================
-- 对象存储 GC 队列(durable outbox)——存储生命周期底座(包6 review C 簇 epic 第一块)。
--
-- # 解决什么
--
-- 全仓大量"对象先写 / 关系后写"与"删关系 / 删对象"的路径,补偿都是**内存里的
-- best-effort `store.Delete`**:进程一崩、对象存储一抖,blob 就成了无主孤儿(源
-- 文件、派生物、multipart 临时对象),而这些孤儿可能就是 PHI。review 里 #15/#17/
-- #19/#20/#4 全是这一类。
--
-- # 怎么解决
--
-- 把"该删哪些对象"**和删关系行放进同一个数据库事务**记进这张队列:事务提交 =
-- 关系行没了 + 待删对象已登记,两者原子。之后一个常驻 janitor 排空这张表(Delete /
-- DeletePrefix),成功删行、失败带指数退避重排。于是"删一半崩溃 / 对象存储临时失败"
-- 从"永久泄漏"降级为"稍后自动重试直到成功",可观测、可重放。
--
-- 刻意**无外键**:它登记的对象 URI 往往是"关系行已经删掉"的东西,GC 记录必须活得
-- 比它们久(与 trace_logs 同理)。
-- =============================================================================
CREATE TABLE object_gc_queue (
    id              BIGSERIAL PRIMARY KEY,
    storage_uri     TEXT        NOT NULL,               -- 带 scheme 的 storageURI(Delete/DeletePrefix 都收它)
    is_prefix       BOOLEAN     NOT NULL DEFAULT FALSE,  -- true → DeletePrefix(volume_slices/ 等目录)
    reason          TEXT        NOT NULL DEFAULT '',     -- 审计:哪条路径登记的(delete asset / dataset / multipart abort…)
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),  -- 到期才被 janitor 领取(退避)
    last_error      TEXT        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_object_gc_due ON object_gc_queue(next_attempt_at);

-- +goose Down
DROP TABLE IF EXISTS object_gc_queue;
