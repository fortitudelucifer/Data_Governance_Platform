# Data Governance Platform

多模态数据标注平台 —— **文本 / 图片 / 音频 / 视频**四条标注管线，采用「AI 预标注 +
人工精标质检」双层协同：模型自动完成重复性初级标注，人工专注审核与修正。

- **后端** `backend/` — Go 1.25 · Gin · GORM · **PostgreSQL** · Redis · MinIO
- **前端** `frontend/` — React 19 · TypeScript · Vite · shadcn/ui · Zustand · TanStack Query

---

## 架构

```
数据集 → 资产 → 任务 → 标注 → FINALIZED 快照 → 导出
```

- **关系脊柱**（用户 / 数据集 / 资产 / 任务 / 标签 / 审计）—— 跨模态形状稳定。
- **载荷表**（标注 / track / 快照 / AI 结果 / 批注）—— 同在 PostgreSQL：
  「提升列 + jsonb payload」，与脊柱同库同事务、外键级联。
- **对象存储**（MinIO / 本地）—— 原始文件与派生物（波形 / 帧索引 / 缩略图）。

视频标注采用 CVAT 式 **track + keyframe 解耦**：不变属性在 track 层，逐帧状态在
keyframe，中间线性插值。导出以 QA 通过时写入的 **FINALIZED 快照**为唯一真源。

---

## 跑起来

依赖：**PostgreSQL 16**(:5432) · **Redis**(:6379) ·
**ffmpeg**（视频帧索引需要）。

```bash
# PostgreSQL（关系库唯一方言；schema 由 goose 迁移自动建出）
docker run -d --name data_governance_postgres \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=data_governance \
  -p 5432:5432 postgres:16
# 单测/集成测试用的一次性库（勿放开发数据）
docker exec data_governance_postgres psql -U postgres \
  -c "CREATE DATABASE data_governance_test"
# 非默认连接串经 DATABASE_URL / TEST_DATABASE_URL 覆盖

# Redis（锁与缓存，非权威存储）
docker run -d --name data_governance_redis -p 6379:6379 redis:7-alpine

# 后端 → :8280（空库自动建 admin/admin123）
cd backend && go build -o app.exe ./cmd && ./app.exe

# 前端 → :5173
cd frontend && npm install && npm run dev
```

Windows + PowerShell 开发者可用 `scripts/start-backend.ps1 -WaitForReady`
（每次启动自动重新编译，并从 `scripts/secrets.local.ps1` 加载本地密钥）。

**密钥不进仓库**：复制 `scripts/secrets.local.example.ps1` 为
`scripts/secrets.local.ps1` 并填入真实值（该文件被 `.gitignore` 挡着）。

**ffmpeg** 不随仓库分发：`winget install ffmpeg` / `apt install ffmpeg`，或放进
`tools/bin/`。缺它 → 视频帧索引出不来（工作台总帧数为 0）。

---

## 测试

三层，全部要过（CI 三个 job 对应）：

```bash
# 后端（71 个测试文件，全部跑真 Postgres：每个夹具一个独立 schema + 真迁移；
# 需要上面的 data_governance_test 库，不可达时相关测试会跳过并大声提示）
cd backend && go vet ./... && go test ./...

# 前端（vitest 单测 + lint + 构建）
cd frontend && npm run lint && npm test && npm run build

# 端到端（Playwright，需后端在跑）
cd frontend && npx playwright test
```

e2e 数据靠 `frontend/e2e/seed.ts` **幂等播种**（按名字找，找不到才建），
CI 在空库上从零播种。**不要硬编码开发机的 dataset/task ID。**

---

## 目录

| 路径 | 内容 |
|---|---|
| `backend/internal/api` | HTTP handlers |
| `backend/internal/service` | 业务逻辑（含导出器、插值、AI 适配） |
| `backend/internal/model` | 关系库模型（`relational/`）+ 载荷文档模型（`payload/`） |
| `backend/internal/repository` | 数据访问 + 迁移 |
| `frontend/src/pages` | 各模态工作台与列表页 |
| `frontend/src/lib` | 帧号数学、插值（与后端共享 `testdata/interpolation/` 夹具） |
| `testdata/interpolation` | Go 与 TS 两端插值实现共享的 golden 夹具 |
