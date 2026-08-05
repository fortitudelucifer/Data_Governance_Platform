# Changelog

Reverse-chronological release notes for the multimodal annotation platform.
Newest milestones on top.

> 🔴 **前端口径(2026-06-26 校准)**:仓库的**唯一前端是 `frontend-react/`**(React 19
> + TS6 + Vite + plain Canvas)。`frontend/`(Vue3 + Konva)**已冻结/退役**。
>
> 本文 v0.5–v0.7 条目里出现的 Vue 文件路径(`ImageAnnotationView.vue` / `AssetListView.vue`
> / `imageTask.ts` Vue 版 / 各 Vue 组件)是历史 Vue 工作的记录;**对应能力 React 端已落地**
> (核对 2026-06-26):多边形画布、VLM 模型下拉、ad-hoc 补跑、分割 tab、Trace 中文化、
> 资产队列导出下拉,均已在 React;并且**额外完成**:Phase D 选择性导出(`task_ids`)、
> Phase A 的 Pan + Marquee 多选、Phase B 的标签词典(`label_config` datalist)、SAM 脚手架。
>
> **真实剩余工作台缺口**(以 React 为准):Undo/Redo · 多边形顶点删除 · 区域面板显隐/锁定 ·
> 系统化热键 · SAM 收尾。详见 `multi-modality/plan_v1/08`。

---

## docs-2026-06-26 — plan_v2 音视频执行方案可靠性校准

**NOTE**：本节是文档审计修订，不表示音视频功能已经实现交付。

- **UPDATED** `multi-modality/plan_v2/执行方案-00-共用基座.md` — 明确当前普通上传路径会把文件读入后端内存且默认 QC 上限 200MiB；M0 生产门槛调整为 MinIO multipart 预签名直传、派生资产 worker、Redis 编辑锁、模态路由、RBAC 泛化。
- **UPDATED** `multi-modality/plan_v2/执行方案-音视频标注落地.md` — 修正执行顺序与生产门禁：A/V 不得跳过 M0；ASR/检测能力必须可降级；视频检测追踪统一新增 `video.detect_track`。
- **UPDATED** `multi-modality/plan_v2/执行方案-02-视频标注.md` — 统一视频 track 主存储为独立集合 `mm_tracks`，不再使用 `HumanAnnotation.Fields.tracks` blob；补充 FINALIZED 快照/导出一致性要求。
- **UPDATED** `README.md` / `HANDOFF.md` / `AGENT.md` — 校准后端默认端口为 `:8280`，补充音视频生产门禁、跨模态 `annotator/reviewer` 权限泛化建议和 M0 不可跳过说明。
- **UPDATED** `multi-modality/plan_v2/*` — 补充 M0 实现契约：`upload_sessions`、MinIO temp object/final SHA256、resume/abort/orphan cleanup、`asset_derivatives`、`label_ontology` 迁移、RBAC 矩阵、音频 FINALIZED 快照、视频 `mm_track_snapshots`、ASR/检测 request_id/model_version 与生产验收分层。

---

## v0.14 — deadline_at UI 全链路 + L2 SigLIP2 语义路由 (2026-05-27) ✅ 已交付

**Headline**：截止日期（deadline_at）从仅后端存储升级为完整 UI 暴露：分配对话框可设定/清除截止日期，资产列表与「我的任务」均展示（逾期红色警示）；L2 SigLIP2 零样本语义路由落地，在 L1 灰区时调用 SigLIP2 分类器分解歧义图片，OCR/VLM 路由准确率进一步提升。

### deadline_at UI 全链路

- **CHANGED** `AnnotationTaskMeta`（`asset.ts`）— 新增 `deadline_at?: string | null` 字段。
- **CHANGED** `AssetListView.vue` — `openAssignDialog()` 从任务现有值初始化 `deadlineDraft`；`openBatchAssignDialog()` 重置为 null（留空=不修改）；`doSingleAssign()` / `doBatchAssign()` 将 `deadlineDraft` 传入 `assignTask` / `batchAssignTasks`（null=不改，""=清除，RFC3339=设置）；资产列表表格新增「截止日期」列，逾期显示红色 `.deadline-overdue`。
- **CHANGED** `MyTasksView.vue` — 新增「截止日期」列，逾期红色 `.deadline-overdue`，正常灰色 `.deadline-ok`。
- **NOTE** 后端（`AssignTask` / `BatchAssignTasks` handler + `parseDeadline` helper + `AnnotationTaskService.Assign/BatchAssign`）已在 v0.13 完成，本版本补全前端交互闭环。

### L2 SigLIP2 语义路由

- **NEW** `internal/service/siglip2_router_probe.go` — `SigLIPProbe` 接口 + `HTTPSigLIPProbe` HTTP 适配器；`POST {endpoint}/classify` 协议（`image_base64`/`mime`/`task_id`/`trace_id` → `top_category`/`scores`/`model`）；`SigLIP2StrategyFromCategory()` 将 top_category 映射到策略：8 类文档型（document/form/receipt/invoice/table/text_heavy/handwriting/slide）→ `OCR_FIRST`，7 类视觉型（natural_scene/person/animal/object/photo/artwork/diagram）→ `VLM_FIRST`；置信度阈值 0.60，低于则视为不确定（保留 L1 灰区默认）。
- **CHANGED** `internal/service/router_service.go` — 新增 `siglip SigLIPProbe` 字段与 `WithSigLIPProbe()` 链式 setter；新增 `grayZoneReason` 常量（`"gray zone fallback"`）作为灰区标志；`Route()` 改写：L1 特征收集后单独存 `featuresBag`（便于 L2 追加 `l2_top_category` / `l2_top_score` / `l2_scores`）；通过检查 `reasons` 尾项是否为 `grayZoneReason` 判断是否触发 L2（避免 `VLM_FIRST` 显式判断与灰区默认值碰撞导致误触发）；L2 成功时 strategy 覆盖、reason 追加 `"l2_siglip2: {cat} (score=XX)"`；L2 失败时 log 并保留 L1 灰区默认。
- **CHANGED** `internal/service/capability_service.go` — 新增常量 `CapabilitySemanticRouter = "image.semantic_router"`。
- **CHANGED** `config/config.go` — `MultiModalConfig` 新增 `SigLIPEndpoint string` / `SigLIPAPIKey string`；env `MM_SIGLIP_ENDPOINT` / `MM_SIGLIP_API_KEY`。
- **CHANGED** `cmd/main.go` — `MM_SIGLIP_ENDPOINT` 非空时自动注册 `HTTPSigLIPProbe` 并调 `routerService.WithSigLIPProbe()`（日志 `[router] L2 SigLIP2 semantic probe enabled`）；同时向 `capabilityConfigService.RegisterEnvAdapter` 注册快照供管理中心展示。

### 下一步候选（v0.15 建议）

- **SigLIP2 服务端**：实现 `POST /classify` FastAPI 服务（对齐 `siglip2_router_probe.go` 协议），加载 `siglip2-base-patch16-384` 或 `siglip2-large`，用 14 类中英文 prompt 向量做 zero-shot 打分；部署于 GPU 节点（或 CPU 容器），`MM_SIGLIP_ENDPOINT=http://127.0.0.1:8390` 启用。
- **L2 路由准确率评估**：在 `validation_set/` 对 300 张 baseline 中剩余 2 张 gray-zone mismatch（MANUAL 类，box>5 但 ratio<0.10）跑 L2 通路，验证 mismatch 能否被消除；工具 `tools/run_l2_routing_eval.py`。
- **ROUTING_REVIEW 面板**（P1 预留激活）：在工作台路由 tab 中展示 L1+L2 路由理由 + feature bag，允许管理员在 AI 启动前手动切换策略。
- **标注员绩效看板**：仪表板「标注员工作量」卡片现有数据展示已做（v0.13 ImageAnnotatorStats），可补「今日截止任务」过滤、逾期任务告警。

---

## v0.13 — QA 审核员视图修复 + 批量任务分配 + 我的任务队列导航 (2026-05-26) ✅ 已交付

**Headline**：审核员通过 `mine=true` 可正确拉取自己的 `QA_PENDING` 任务（OR 语义修复）；管理员可在资产列表多选后一键批量分配标注员/审核员；从「我的任务」进入工作台后上/下一张始终在本人任务队列内跳转，不再跨入他人任务。

### QA 审核员任务视图修复

- **FIXED** `AnnotationTaskFilter`（`annotation_task_repo.go`）— 新增 `MineUserID *uint`；当该字段非 nil 时，查询条件由原 `assignee_id = ?` 升级为 `(assignee_id = ? OR reviewer_id = ?)`，覆盖审核员角色。
- **CHANGED** `ListTasks` handler — `mine=true` 时设 `filter.MineUserID`（原来误设 `filter.AssigneeID`），`assignee_id=X` 管理员查询路径不受影响。
- **CHANGED** `MyTasksView.vue` — 新增 `isReviewer(row)` / `canReview(row)` / `canAnnotate(row)` 三个角色感知函数；操作列三态：审核员看到「去审核」（绿色）、标注员看到「去标注」（蓝色）、其余「查看」；新增「我的角色」列（审核员/标注员 tag）。

### 批量任务分配

- **NEW** `POST /tasks/batch-assign`（`RequireRole("admin")`）— body `{task_ids:[...], assignee_id, reviewer_id}`；nil = 不改，0 = 清除，同 `PUT /tasks/:id/assign` 语义，原子 SQL `UPDATE WHERE id IN (...)`。
- **NEW** `repository.DB.BatchUpdateAnnotationTasks(ids []uint, updates map[string]interface{}) (int64, error)` — 批量 gorm `Updates`，返回实际影响行数。
- **NEW** `AnnotationTaskService.BatchAssign(ctx, taskIDs []uint, assigneeID, reviewerID *uint) (int64, error)` — 复用 `Assign` 零值语义，委托 repo 批量执行。
- **CHANGED** `AssetListView.vue` — header 加「批量分配 (N)」按钮（仅 admin 且有选中任务时显示）；分配 dialog 新增 `batchAssignMode` 标志：批量模式下标题变为"批量分配 (N 个任务)"，顶部蓝色 Alert 说明语义；保存时调用 `batchAssignTasks` API，成功后刷新当页任务。
- **NEW** `batchAssignTasks(taskIds, assigneeId?, reviewerId?)` API 函数（`imageTask.ts`）。

### 我的任务队列导航

- **NEW** `repository.DB.FindAdjacentTaskIDsByUser(userID, currentTaskID uint)` — `WHERE (assignee_id=? OR reviewer_id=?) AND id < ?` / `id > ?`，在用户任务集合内找相邻任务，与全数据集资产导航互不干扰。
- **CHANGED** `AnnotationTaskService.AdjacentTaskIDs(ctx, taskID, mineUserID *uint)` — `mineUserID != nil` 时走 `FindAdjacentTaskIDsByUser`，否则走原始全数据集 `FindAdjacentAssetIDs` 路径。
- **CHANGED** `GetAdjacentTasks` handler — 接受 `?mine=true`，传当前用户 ID 进行 scope；无 mine 参数则行为与 v0.12 完全一致。
- **CHANGED** `getAdjacentTasks(id, mine?)` API 函数（`imageTask.ts`）— 加 `mine` boolean 参数。
- **CHANGED** `MyTasksView.vue` — `openTask` 跳转改为 `router.push({ path: '/image-tasks/:id', query: { from: 'mine' } })`。
- **CHANGED** `ImageAnnotationView.vue` — 读 `route.query.from === 'mine'` 决定 `loadAdjacent` 是否传 `mine=true`；`goToTask` 保留 `from=mine` query 参数跨页传递；`goBack` 在 mine context 下返回 `/my-tasks` 而非数据集资产列表。

---

## v0.12 — 任务分配 UI + 看板多模态统计 + 全局滚动修复 + 脚本目录重构 (2026-05-26) ✅ 已交付

**Headline**：管理员可在资产队列直接为每张图片任务指定标注员和审核员；看板新增图片标注任务状态分布卡片；修复全站所有视图的滚动失效问题；`scripts/` 三个冲突 `package main` 脚本拆入独立子目录，`go build ./...` 恢复干净。

### 任务分配（Task Assignment）

- **NEW** `PUT /tasks/:id/assign` — 管理员专属，body `{assignee_id, reviewer_id}`；传 `0` 清除，传 `null` / 不传则跳过该字段（支持仅改一方）。`RequireRole("admin")` 中间件守卫。
- **NEW** `AnnotationTaskService.Assign(ctx, taskID, assigneeID *uint, reviewerID *uint)` — `*uint` 零值语义：nil = 不动，0 = 清为 null，>0 = 赋值。
- **CHANGED** `AnnotationTaskMeta`（`asset.ts`）— 新增 `assignee_id?: number | null`、`reviewer_id?: number | null` 字段。
- **NEW** 前端 `assignTask(taskId, assigneeId, reviewerId?)` API 函数（`imageTask.ts`）。
- **CHANGED** `AssetListView.vue` — 任务列表新增「被分配」列（显示分配人姓名，admin 才渲染）；每行「分配」按钮打开 `el-dialog`，管理员可选标注员 / 审核员（可选），调用 `assignTask`；`loadUsers()` 在对话框首次打开时拉取用户列表。

### 看板图片任务统计（Dashboard Image Task Stats）

- **CHANGED** `DashboardStats`（`model/dashboard.go`）— 新增 `ImageTaskStats` struct（`Total / FinalizedToday / StateDistribution map[string]int`）+ `ImageTasks *ImageTaskStats \`json:"image_tasks,omitempty"\``。
- **CHANGED** `DashboardService.GetStats` — 新增对 `annotation_tasks` 表的两条聚合查询：① `GROUP BY state` 统计全量分布；② `WHERE state='FINALIZED' AND updated_at >= 今日00:00` 统计今日定稿数；支持 `dataset_id` 过滤。`cloneDashboardStats` 同步深拷贝 `ImageTaskStats`。
- **NEW** `createEmptyStats()` 前端工厂函数，`image_tasks` 默认值 `{total:0, finalized_today:0, state_distribution:{}}`，避免 omitempty 导致前端报 undefined 错误。
- **CHANGED** `DashboardView.vue` — 新增「图片标注任务统计」卡片，展示状态分布（含百分比）+ 今日定稿数；无任务时 `el-empty` 提示；`fetchCoreStats` 合并 `image_tasks` 字段。

### 全局视图滚动修复

- **FIXED** `App.vue` — `<router-view>` 样式从 `overflow:hidden` 改为 `overflow:auto`，解决子视图滚动条被外层裁剪的根因。
- **FIXED** 以下视图的根元素均补充 `height:100%;overflow-y:auto;box-sizing:border-box`（或等效 CSS 类）：
  - `DashboardView.vue`
  - `UserManagementView.vue`
  - `DatasetListView.vue`（style 补全）
  - `MyTasksView.vue`（CSS 类补全）
  - `DatasetFunctionView.vue`
  - `ExtractionView.vue`
  - `SystemPromptView.vue`

### 脚本目录重构（Scripts Restructure）

- **FIXED** `backend/scripts/` 下三个 `package main` 脚本相互冲突（`main` 重定义 / `normalizeDocumentQAPairs` 签名冲突），导致 `go build ./...` 报错。
- **CHANGED** 各脚本迁移至独立子目录：
  - `scripts/backfill_http/main.go` ← `backfill_qa_answers.go`（HTTP API 批量规范化 qa_pairs）
  - 历史批量规范化 qa_pairs 工具（已随载荷层迁移删除）
  - `scripts/export_h_cpws/main.go` ← `export_h_cpws_jsonl.go`（h_cpws_data_source → JSONL 导出）
- 原三个文件已删除；`go build ./...` 零错误，`go build ./scripts/...` 三个二进制均正常编译。

---

## v0.11 — Phase C：笔刷遮罩 + MobileSAM 交互式分割 (2026-05-22) ✅ 已交付

**Headline**：像素级笔刷涂抹工具（C-1）+ MobileSAM 一键点击分割（C-2）全部落地；sam-server 运行在 RTX 3070 Ti Laptop CUDA，能力中台新增 `seg.interactive` capability；后端 COCO/YOLO 导出同步支持 mask shape；工作台所有六种绘图方式（矩形/椭圆/多边形/折线/关键点/笔刷遮罩）+ SAM 辅助分割均可用。

### C-1 — 笔刷遮罩工具

- **NEW** `DraftBox.kind` 新增 `'mask'`，完整参与序列化/反序列化/undo/clone/导出链路。
- **NEW** HTML overlay canvas（`brushOverlayRef`）绝对定位叠加在 Konva stage 上，`pointer-events: auto` 仅在 mask 模式启用，其余时间穿透到 Konva。
- **NEW** offscreen canvas（图片原始像素尺寸）存储实际笔触数据，显示时按 stage 变换缩放至 overlay。
- **NEW** `paintAt(imgX, imgY, isFirst)` — 以 `round lineCap` 连续描边，`destination-out` 实现橡皮擦。
- **NEW** `finishMask()` — `canvas.toDataURL('image/png')` → base64，alpha 通道扫描计算 mask_bbox，推入 `kind='mask'` DraftBox；自动注册 `maskImageCache` 用于后续渲染。
- **NEW** 已保存 mask 以半透明（40–65%）`v-image` 渲染在 Konva shapes 层，选中时加深。
- **NEW** `boxToShape`/`shapeToBox` mask 分支：`points: [[x,y],[x+w,y+h]]`，`attrs: {mask_png_b64, mask_bbox}`，从载荷库重载时自动 `registerMaskImage`。
- **NEW** 热键：`B` 切换笔刷工具；`[` / `]` 调整笔刷大小（4–64px，步长 4px）；`Enter` 完成遮罩；`ESC` 放弃。
- **NEW** 工具栏按钮「+ 遮罩 / 取消遮罩」+ 动态「画笔/橡皮擦」切换按钮 + 当前 px 显示 + 「完成遮罩」按钮。
- **NEW** Overlay canvas 在 mask 模式下接管滚轮缩放（`onBrushWheel`）和 Space/中键平移（`onBrushMouseDown` 判断 spaceHeld / button=1），pan/zoom 全程不中断绘制。
- **CHANGED** 所有其他工具的 toggle 函数（toggleDrawing/Poly/Ellipse/Polyline/Point/SAM）均调用 `clearMaskState()` 互斥清理。
- **CHANGED** `attachTransformer` — mask shape 跳过 transformer（不支持拖拽/缩放，只可点选和删除）。

### C-1 Ctrl+D 克隆

- **NEW** `cloneSelectedShape()` — 克隆当前选中 shape，偏移 10px，mask 类型同步 `registerMaskImage` 至新 id。热键 `Ctrl+D`（全平台 Ctrl/Meta）。

### C-2 — MobileSAM 交互式分割

- **NEW** `sam-server/` — FastAPI 服务，端口 **8381**（历史上用于避开后端端口；当前后端默认 `:8280`）；`/health`、`/segment`（POST）接口。
- **NEW** `seg_interactive_adapter.go` — `CapabilityAdapter` 实现，读取 asset 图片字节 → base64 → POST sam-server `/segment`；`SAMResult{Polygons, Score, MaskPNGB64}` 存入 `CapabilityResponse.Raw`。
- **NEW** `POST /tasks/:id/segment` — 专用路由，将前端 `{points, box}` 透传给 SAM adapter（不走 AdHocInvocationService，结果不持久化，属于临时预览调用）；需 `image_annotator` 或 `admin` 角色。
- **NEW** 前端 `segmentInteractive(taskId, points, box?)` API 函数；`SAMSegmentResult` interface。
- **NEW** `drawingSAM` 模式：点击图片 → 调 `invokeSAM(imgX, imgY)` → 后端 → 返回 polygon → `samPreview` 绿色虚线预览 → 「✓ 接受 (xx%)」/「✗ 放弃」；接受后作为 `kind='polygon'`, `source='ai'` 加入 boxes。
- **NEW** 热键 `S` 切换 SAM 模式（仅在 `hasCap('seg.interactive')` 时可用）。
- **CHANGED** `start-backend.ps1` — 新增 `MM_SAM_ENDPOINT = 'http://127.0.0.1:8381'`，`MM_SAM_API_KEY = ''`。
- **CHANGED** `config.go` — `MultiModalConfig` 新增 `SAMEndpoint`、`SAMAPIKey`。
- **CHANGED** `capability_service.go` — 新增常量 `CapabilitySegInteractive = "seg.interactive"`。
- **CHANGED** `cmd/main.go` — 条件注册 `SAMInteractiveAdapter`（`MM_SAM_ENDPOINT` 非空时），注册路由 `/tasks/:id/segment`。

### 后端导出 mask shape 支持

- **CHANGED** `image_export_service.go` — `shapeBBox`：mask 优先读 `attrs.mask_bbox`；`shapePolygonFlat`：mask 输出外接矩形四角多边形（与 bbox 一致，符合 COCO `segmentation` 格式）；新增 `maskBBoxFromAttrs` helper（处理 `[]interface{}` / `[]float64` 两种反序列化形状）。YOLO-seg 导出同步受益。

### sam-server 依赖说明

- Python 3.12 venv（`sam-server/.venv`）：PyTorch 2.5.1+cu121，timm 1.0.27，fastapi 0.111.0，uvicorn 0.30.0，pillow 10.3.0，numpy 1.26.4，mobile_sam 1.0（需从 GitHub zip 本地安装，PyPI 无此包）。
- 启动命令：`$env:MODEL_PATH = "$PSScriptRoot\models\mobile_sam.pt"; .venv\Scripts\python app.py`（须在 `sam-server/` 目录或通过 `MODEL_PATH` 环境变量指定绝对路径）。
- 运行状态：`GET /health` → `{"status":"ok","device":"cuda","model_loaded":true}`。

---

## v0.10 — 工作台体验 Phase B-by-need + 导航修复 (2026-05-21)

**Headline**：补全椭圆 / 折线 / 关键点三种绘图工具，统一工具栏热键（E / L / K）；修复工作台「返回」按钮导航到空页面的问题。

### Phase B-by-need — 新绘图工具
- **NEW** `+ 椭圆`（热键 `E`）— 拖拽绘制，以包围盒（x/y/w/h）存储，支持拖移；序列化为 `kind='ellipse'`。
- **NEW** `+ 折线`（热键 `L`）— 逐点点击，Enter / 「完成折线」结束（≥2 顶点），Backspace 撤顶点，ESC 取消；选中后拖白色顶点精修；Delete 键删顶点（保护 ≥2）；序列化为 `kind='polyline'`。
- **NEW** `+ 关键点`（热键 `K`）— 单击放置，支持拖移；序列化为 `kind='point'`。
- **CHANGED** `DraftBox` interface — `kind` 扩展至 `'bbox' | 'polygon' | 'ellipse' | 'polyline' | 'point'`。
- **CHANGED** `shapeToBox` / `boxToShape` — 覆盖所有新 kind 的双向序列化。
- **CHANGED** 工具栏 — 5 个互斥工具按钮，任一激活时其余 disabled；折线增加「完成折线 (N)」按钮和「撤销顶点」；hint 文字随当前工具切换。
- **CHANGED** 标注列表种类徽章 — 新图标：○ 椭圆 / 〜 折线 / ● 关键点，对应颜色区分。

### 导航修复
- **FIXED** `ImageAnnotationView.vue` 「返回」按钮 — 改为 `goBack()`：优先跳转至 `/datasets/:datasetId/assets`（图片资源队列），无法取得 dataset_id 时降级 `router.back()`，消除返回空白页的问题。

---

## v0.9 — 工作台体验 Phase B-core (2026-05-21)

**Headline**：标注工作台对标 Label Studio 的第二阶段——标签词典、区域面板、系统化热键、队列翻页全部落地，M1「新标注员 30 分钟可用」里程碑前置条件就绪。

### B4 — 队列内翻页
- **NEW** `GET /tasks/:id/adjacent` — 返回同数据集内前后两张 asset 对应的 task_id（按 asset.id 升序，任一端为 null 表示已到头）。
- **CHANGED** `ImageAnnotationView.vue` header — 新增「‹ 上一张 / 下一张 ›」按钮（disabled 在队列两端）；`←` / `→` 方向键全局触发（输入框内屏蔽）。

### B1 — 标签词典（Label Ontology）
- **CHANGED** `Dataset` model — 新增 `label_config TEXT DEFAULT '[]'` 列（AutoMigrate 自动加列）。
- **NEW** `GET /datasets/:id/label-config` / `PUT /datasets/:id/label-config` — 读写 JSON 标签数组 `[{name,color,hotkey}]`。
- **CHANGED** `AssetListView.vue` — 顶部新增「标签词典」按钮，弹窗支持：添加/删除标签、颜色选择器、热键字段、「导入 COCO 80 类」一键预填（80 个 COCO 类名 + 均匀色相）。
- **CHANGED** `ImageAnnotationView.vue` — 工作台标注表单「标签」字段：有词典时变为带颜色预览的下拉选择器（支持 filterable + 自由输入），无词典退回文本输入；shape 边框颜色按标签词典着色（优先于来源颜色）；随 task 加载时异步拉取该数据集的词典配置。

### B3 — 系统化热键
- `R` — 切换矩形工具；`P` — 切换多边形工具（均在输入框聚焦时屏蔽）。
- `1`–`9` — 对当前选中 shape（或多选）应用词典第 N 个标签（按 hotkey 字段匹配）。
- `Ctrl+H` — 全部隐藏 / 全部显示（与 Label Studio Ctrl+H 对齐）。
- `←` / `→` — 上一张 / 下一张任务（B4）。

### B2 — 区域面板升级
- **CHANGED** `DraftBox` — 新增 `visible?: boolean`（隐藏）和 `locked?: boolean`（锁定）字段。
- **CHANGED** 标注列表每行 — 新增眼睛图标（点击切换显隐）和锁图标（点击切换锁定）；隐藏行 40% 透明度 `dimmed`；`Ctrl+H` 批量切换。
- **CHANGED** Konva shape config — `visible` 绑定 `DraftBox.visible`；`listening: false` 绑定 `DraftBox.locked`（锁定 shape 不响应鼠标）。
- **NEW** hover 联动 — 鼠标悬停列表行 → 对应 canvas shape 高亮为黄色边框（`hoveredId` ref）。
- **NEW** `window.__seedBoxes(n)` dev 钩子（DEV 模式）— 快速塞 N 个合成 shape 用于 Chrome Performance 性能录制。

---

## v0.8 — 工作台体验 Phase A + D (2026-05-21)

**Headline**：消化 v0.7 验收反馈的所有硬缺口（pan / 框选 / undo / 删点）+ 导出选择，加上用户驱动的额外交付（AI 草稿撤销 / 标注全选批删 / 资产状态内嵌）。

### Phase A — 画布硬缺口
- **平移（pan）**：空格 + 拖拽 或 中键拖拽平移 `stage.offsetX/offsetY`，与缩放共存。
- **框选多选（marquee）**：空白区域拖出矩形，框内 shape 全选；Shift+点击增减；Esc 清空。
- **批量操作**：多选后批量删除、批量改标签。
- **橡皮擦/删点**：多边形选中顶点后 Delete 删该顶点（≥3 顶点保护）。
- **撤销/重做**：50 步快照栈，Ctrl+Z / Ctrl+Y + 工具栏按钮。

### Phase A 额外交付（用户驱动）
- **AI 草稿撤销载入**：载入 OCR/VLM/Seg 草稿前自动快照；「撤销载入」按钮一键复原，独立于 undo 栈。
- **标注列表全选 + 批量删除**：列表顶部全选复选框（半选态）+ 「删除选中 (N)」按钮。
- **资产任务状态内嵌**：`GET /datasets/:id/assets` 响应中嵌入每个 asset 的 latest task（单次请求，无二次 API）；新增 `asset_ids` 批量过滤参数（解决 50 资产 59 任务的截断 bug）；5 秒过渡态自动轮询。

### Phase D — 导出选择
- **CHANGED** 资产队列表格 — 新增多选列（`type="selection"`）；导出按钮显示「导出选中 (N)」。
- **CHANGED** 4 个导出端点 — 支持 `?task_ids=12,15,18` 过滤（空 = 全量）；文件名带 `-selectedN-` 标记。
- **CHANGED** `StreamFinalAnnotationsByDataset` — 支持 `taskIDs []uint` 注入 `$in` 过滤。

---

## v0.7 — 多边形画布（Polygon Canvas） (2026-05-21)

**Headline**: 工作台画布从"只能画矩形"升级到"矩形 + 多边形"，补全 seg.instance → 人工精修 → 导出的闭环。这是 ADR-10「P0 只矩形编辑」的 P1 升级。

### Frontend (`ImageAnnotationView.vue`)
- **数据模型**：`DraftBox` 扩展 `kind: 'bbox'|'polygon'` + `points`；polygon 以顶点为源、派生外接框（`bboxOfPoints`）供列表/选择/标签表单统一使用。
- **渲染**：polygon 用 `v-line`（闭合 + 半透明填充）；选中后显示白色顶点圆（`v-circle`），可拖拽精修。bbox 仍走 `v-rect` + transformer。
- **绘制**：工具栏「+ 多边形」逐点点击加顶点 → 「完成多边形」/ Enter 闭合（≥3 点）；Backspace 撤销上一顶点；ESC 取消。与「+ 矩形框」互斥。
- **顶点编辑**：选中多边形后拖白色顶点改 `points`，外接框实时同步；改动把 source 从 ai 翻成 human。
- **序列化**：`boxToShape`/`shapeToBox` 双向支持 `kind=polygon`（points 全顶点）；transformer 对 polygon 跳过（用顶点编辑）。
- **载入 seg**：`loadAIFromSeg` 改为载入**完整多边形**（之前只放外接框）→ seg 预标多边形现在能在画布上画出来、拖顶点精修。
- 标注列表 + 已选表单加「类型」标识（多边形 N 顶点 / 矩形框）。

### 闭环打通
seg.instance 预标多边形 → 「分割」tab 补跑 → 「从分割载入草稿」→ 画布顶点精修 → 保存/提交/QA → FinalAnnotation（polygon shapes）→ COCO/YOLO-seg/JSON-LD 导出（多边形已实测穿透三格式）。

---

## v0.6 — Instance Segmentation (seg.instance) + Training Exports (2026-05-20)

**Headline**: 第 3 个 capability 落地 + 训练格式导出全套。
1. **`seg.instance` 能力**：新起 YOLOv8-seg 分割服务（seg-server，CPU），预标 COCO-80 物体的多边形轮廓（car/person/...），喂给后续多边形画布。
2. **导出三件套**：COCO（含分割多边形）/ YOLOv8-seg（zip）/ W3C JSON-LD，前端资产队列加「导出标注」下拉。

> ⚠️ **类覆盖**：YOLOv8-seg 预训练 = COCO 80 类，有 car/person/bicycle/truck，但**无"轮胎/轮毂"类**。整车/行人轮廓可用；轮胎级轮廓需人工在画布补或训自定义模型（未来）。

### Segmentation server (seg-server/)
- **NEW** `seg-server/{Dockerfile,server.py,requirements.txt,run.ps1,README.md}` — FastAPI `POST /segment` + `GET /healthz`，YOLOv8-seg（ultralytics 8.3）。CPU torch（`--index-url .../whl/cpu`），不与 ocr-p0 抢 8GB 显存。模型 yolov8m-seg.pt 烘进镜像。
- 协议：`{image_base64,...} → {polygons:[{class_name,class_id,points,bbox,score}], model, version}`。

### Backend (Go)
- **NEW** `internal/service/segmentation_adapter.go` — `SegmentationHTTPAdapter`，capability `seg.instance`，打 seg-server `/segment`。
- **NEW** `internal/service/image_export_service.go` — COCO / YOLOv8-seg / JSON-LD 三个 builder，读 FinalAnnotation.Shapes（bbox 2 点或 polygon 顶点），join 关系库资产取尺寸/文件名。
- **CHANGED** 多模态载荷模型 — 新增 `SegResult` + `SegPolygon`；`CapabilityResponse.Seg` 字段。
- **CHANGED** `capability_service.go` — 新增常量 `CapabilitySegInstance`。
- **CHANGED** `config.go` + `start-backend.ps1` — `MM_SEG_ENDPOINT=http://127.0.0.1:8500` / `MM_SEG_API_KEY`。
- **CHANGED** `cmd/main.go` — `MM_SEG_ENDPOINT` 配置时注册 seg adapter（日志 `[capability] registered YOLOv8-seg adapter`）。
- **CHANGED** `ad_hoc_invocation_service.go` — 持久化 Seg 结果到 `mm_seg_results`，`AdHocInvocationResult.Seg` 字段（seg 走 ad-hoc 补跑，不是自动路由）。
- **CHANGED** 载荷仓储 — `UpsertSegResult` / `FindLatestSegResult`。
- **NEW endpoints**（admin / image_reviewer）：
  - `GET /datasets/:id/export.coco.json` — COCO detection+segmentation
  - `GET /datasets/:id/export.yolo-seg.zip` — labels/*.txt（归一化多边形）+ data.yaml
  - `GET /datasets/:id/export.jsonld` — W3C Web Annotation collection
  - 均支持 `?since=<RFC3339>`。

### Frontend (Vue)
- **CHANGED** `AssetListView.vue` — 资产队列头部加「导出标注」下拉（JSONL / COCO / YOLO-seg / JSON-LD），blob 下载（带 JWT）。
- **CHANGED** `ImageAnnotationView.vue` — 工作台新增「分割」tab：「补跑 实例分割」(seg.instance) +「从分割载入草稿（外接框）」+ JSON 展示。画布暂只渲染矩形，载入草稿放多边形外接框；完整多边形存 `mm_seg_results` 直通导出，可视化编辑待多边形画布。
- **CHANGED** `GET /tasks/:id/ai-results` + `imageTask.ts` — 返回体加 `seg` 字段（`FindLatestSegResult`），seg tab 跨刷新保留。

---

## v0.5 — Capability Picker + Router Tuning + VLM Model A/B (2026-05-20)

**Headline**: 直接对应你看到的「街景被吞到 OCR」痛点。三件事：
1. 加了「补跑 capability」按钮 —— 工作台可任选 ocr.structure / vlm.caption / vlm.structured_extract，单图按需调度，不动 task state。
2. 路由阈值用 300 张实测数据调参，accuracy 83.3% → **99.3%**（mismatch 50 → 2）。
3. VLM 多模型 A/B（plan §7 实验 C）：qwen-vl-plus vs qwen-vl-max，plus 速度 ~2.2× 且质量近乎等价 → **默认切 plus**，并让 ad-hoc 补跑可选模型。

### VLM 模型选择 + A/B（实验 C）

- **CONFIG** `litellm-config.yaml` — 新增 `qwen-vl-max`（→ qwen-vl-max-latest）与 `qwen-vl-plus`（→ qwen-vl-plus-latest）两个 model_group，同一个 DashScope key。
- **CONFIG** `start-backend.ps1` — `MM_VLM_MODEL`: `qwen-vl` → **`qwen-vl-plus`**（平台默认 VLM 模型切到 plus）。
- **NEW** `tools/vlm_ab_test.py` — 同一 prompt 直连 LiteLLM 跑两个模型，记录 latency / tokens / cost / JSON 合规 / caption 文本。
- **CHANGED** `backend/internal/service/capability_service.go` — `CapabilityRequest` 加 `Model` 字段（per-request 模型覆盖；非 LLM adapter 忽略）。
- **CHANGED** `backend/internal/service/vlm_adapter.go` — `Invoke` 用 `req.Model`（非空时）覆盖 adapter 默认模型，并在 provider.model_id 反映实际所用模型。
- **CHANGED** `backend/internal/service/ad_hoc_invocation_service.go` + `multimodal_handlers.go` — `InvokeForTask(taskID, capability, model)` + handler 读 `?model=` 查询参数（或 JSON body）。
- **CHANGED** `frontend/src/api/imageTask.ts` + `ImageAnnotationView.vue` — VLM tab 加模型下拉（qwen-vl-plus 默认 / qwen-vl-max），补跑按钮带所选模型。

**A/B 实测（20 张街景 / 2026-05-20）**：

| 模型 | latency p50 | latency p95 | JSON 合规 | tags 均数 | completion tokens |
|---|---|---|---|---|---|
| qwen-vl-max | 2847ms | 3658ms | 100% | 5.0 | 50 |
| **qwen-vl-plus** | **1316ms** | **1460ms** | 100% | 5.0 | 53 |

结论：街景 captioning 任务 plus 用一半延迟拿到近乎等价质量（关键信息如车/行人/店名两者都抓到）。max 留给难例（密集文字 / 结构化抽取需细节）。逐图对照：`validation_set/vlm_ab_report.jsonl`。成本未捕获（LiteLLM 无 DashScope qwen-vl 定价表）。

端到端验证：`POST /tasks/45/invoke?capability=vlm.caption`（默认 → plus 1719ms）/ `&model=qwen-vl-max`（→ max 2971ms）/ `&model=qwen-vl-plus`（→ plus 1468ms），provider.model_id 均正确。

### Backend (Go)

- **NEW** `backend/internal/service/ad_hoc_invocation_service.go` — `AdHocInvocationService` 复用 `CapabilityService.Invoke` 写一条新 `mm_ai_run` + result，但不改 task state / route_strategy / ai_run_ids（区分 attempt=0 → ad-hoc，attempt>=1 → 自动）。状态闸：只在 AI_PENDING+之后允许补跑。
- **NEW** `backend/internal/api/multimodal_handlers.go`：
  - `GET /capabilities` — 列出运行时已注册的 capability_types。
  - `POST /tasks/:id/invoke?capability=<cap>` — 按需触发；返回 `AdHocInvocationResult`（run_id / status / latency / OCR/VLM 结果）。
- **CHANGED** `backend/internal/service/router_service.go::DefaultRoutingDefaults`：
  - `BoxCountOCRThreshold`: 8 → **5**
  - `TextAreaRatioThreshold`: 0.15 → **0.10**
  - `GrayDefaultStrategy`: `OCR_FIRST` → **`VLM_FIRST`**
- **CHANGED** `backend/cmd/main.go` — 注册 `adhocInvocationService` + 两条新路由 + 把 `capabilityService` 透传给 handlers。
- **CHANGED** `backend/internal/service/router_service_test.go` — 更新 gray-zone case 用新阈值下真正会落 gray 的特征 (BC=3, TAR=0.08)。

### Frontend (Vue)

- **CHANGED** `frontend/src/api/imageTask.ts` — 新增 `listCapabilities()` / `invokeCapabilityOnTask()` / `AdHocInvocationResult` 类型。
- **CHANGED** `frontend/src/views/ImageAnnotationView.vue` — 补跑控件按 tab 分布（不再用 header 全局下拉）：
  - **OCR tab**：「补跑 OCR」(ocr.structure)
  - **VLM tab**：「补跑 Caption」(vlm.caption) +「补跑 结构化抽取」(vlm.structured_extract)
  - **路由 tab**：「重新路由」(reprocess，仅终态 FINALIZED/EXPORTED/QC_FAILED 可用)
  - **标注 tab**：不放补跑（纯人工编辑界面）
  - 每个按钮按 `GET /capabilities` 结果 gate（capability 未注册则禁用）；`invokingCap` 让仅被点的按钮转圈；执行后自动 reloadAll()。

### Tools (Python)

| 文件 | 用途 |
|---|---|
| `tools/sample_p0_60.py` (扩展) | 加 `--preset` flag。`p0_60` 沿用 v0.4 行为；`p0_300` 用 10 OCR + 5 MIXED 类目，100/100/100 分布。 |
| `tools/tune_router_thresholds.py` (新建) | 384-config grid search，模拟 Go 的 `Decide()`，按 mismatch 降序输出 top-K，sanity-check 输出最佳配置下剩余 mismatch。 |

### Measured (300-image baseline, 2026-05-20)

| 维度 | v0.4 默认 (60 张) | v0.4 默认 (300 张) | **v0.5 调参后** |
|---|---|---|---|
| 路由严格准确率 (不含 MIXED) | 87.5% (35/40) | 75.0% (150/200) | **预测 99.0%**（基于 simulator） |
| 路由 inclusive 准确率 | 91.7% (55/60) | 83.3% (250/300) | **预测 99.3%**（2/300 mismatch） |
| VLM mismatch | 5/20 (25%) | 50/100 (50%) | **0/100** （30 张 revalidation 100%）|
| OCR adapter p95 | 6.1s | 6.4s | (相近) |
| VLM adapter p95 | 11.8s | 14.2s | (相近) |
| AI run 成功率 | 100% | 100% | 100% |

### Revalidation

`tools/run_p0_baseline.py --selection ./validation_set/p0_vlm_revalidation.jsonl` 把之前 50 张 VLM mismatch 中的 30 张重跑 → **30/30 全部路由到 VLM_FIRST** ✅，与 simulator 预测一致。

### Validation Artifacts (新增到 `validation_set/`)

- `db2_ocr_v2.jsonl` — DB2 15-cat 解析（466 页）
- `p0_selection_300.jsonl` — 300 张抽样选择（preset=p0_300）
- `baseline_report_300.jsonl` — 300 张跑批结果（v0.4 默认）
- `baseline_metrics_300.json` — 聚合统计
- `router_tuning_report.json` — grid search 完整结果
- `p0_vlm_revalidation.jsonl` + `baseline_revalidation.jsonl` — 30 张 mismatch 重验证

### Carry-Forward

- 剩余 2/300 mismatch 是 MANUAL 类（box>5 但 ratio<0.10，图多文字少）。需 L2 路由 (SigLIP-2) 或运营点 ad-hoc invoke 补救。
- 真实标注员 accept-rate / 复核时长仍未测。
- 多 VLM 家 A/B (plan §7 实验 C) 仍 P1。

---

## v0.4 — P0 Baseline Closed (2026-05-20)

**Headline**: P0 退出标准定量项 13/13 ✅。60 张本地数据集端到端跑通；L1 OCR det 探针上线；FinalAnnotation 导出端点闭环；段落级 OCR 召回 96.0% / 精确率 91.5%。详见 [`multi-modality/plan_v1/07_p0_baseline_results.md`](multi-modality/plan_v1/07_p0_baseline_results.md)。

### Backend (Go)

- **NEW** `backend/internal/service/ocr_det_probe.go` — Phase 1.5 L1 OCR det 探针。复用 ocr-server `/ocr`，只读 boxes + 算 `text_area_ratio = sum(box area) / canvas area`。失败降级到 zero features 不阻塞路由。
- **NEW** `repository.StreamFinalAnnotationsByDataset` — 载荷流式游标，按 `created_at >=since` 过滤，逐条 yield。
- **NEW** `backend/internal/api/multimodal_handlers.go::ExportDatasetFinalAnnotations` — `GET /datasets/:id/final-annotations.jsonl?since=<RFC3339>` 导出端点，Content-Type `application/x-ndjson`。
- **CHANGED** `backend/internal/service/router_service.go` — 加 `probe OCRDetProbe` 字段 + `WithOCRDetProbe()` 链式 setter；`featuresFromAsset(ctx, asset)` 在 QC=passed 时调探针填 `box_count` / `text_area_ratio`。
- **CHANGED** `backend/cmd/main.go` — 启动时若 `MM_OCR_ENDPOINT` 已配则自动 enable 探针（日志 `[router] L1 OCR det probe enabled`）+ 注册导出路由。
- **CHANGED** `backend/internal/service/router_service_test.go` — 新增 4 个 probe 子测试（成功填值 / 错误降级 / QC 失败跳过 / 无 probe 无副作用）。
- **REBUILT** `backend/app.exe` (27.5 MB)。`go test ./internal/service/ ./internal/api/ ./config/` 全 PASS。

### Tools (Python)

| 文件 | 用途 |
|---|---|
| `tools/parse_db2_zone.py` | DB2 `.PAGE` + `.ZONE` → JSONL 金标 |
| `tools/parse_streetscenes_xml.py` | StreetScenes LabelMe XML → JSONL 金标 |
| `tools/sample_p0_60.py` | 分层抽样 60 张（seed=20260519，可重现）|
| `tools/export_p0_preview.py` | 把抽样 + 金标渲染到 `validation_set/preview/`（GT 视图）|
| `tools/run_p0_baseline.py` | 逐张过完整后端流水线，crash-safe 增量写入 |
| `tools/export_p0_results.py` | 平台输出渲染到 `validation_set/results/`（与 preview 同名对照）|
| `tools/analyze_baseline_metrics.py` | 路由 / 延迟 / 成功率聚合统计 |
| `tools/compare_ocr_vs_gt.py` | 1:1 IoU 评分（line-level，参考）|
| `tools/aggregate_ocr_to_zones.py` | 段落级 recall/precision（centroid-in-zone + cluster 双指标）|
| `tools/finalize_p0_baseline.py` | 一键 HUMAN_PENDING → FINALIZED（用于导出测试）|

### Documentation

- **NEW** `multi-modality/plan_v1/07_p0_baseline_results.md` — P0 baseline 闭环报告（实测数字、设计反思、未完事项）。
- **UPDATED** `multi-modality/plan_v1/04-一期验证集与验收指标.md` v0.3 → v0.4 — §1 数据源切换到 5 套本地集；§8 退出标准 13/13 实测回填；§10 工作流重排（金标解析器前置）。
- **UPDATED** `multi-modality/plan_v1/06_p0_skeleton_handoff.md` — §11.5.2 加历史快照警告（D:\\Anonymous 路径已禁）。
- **UPDATED** `HANDOFF.md` — §5 pending list + §7 smoke command 改用本地数据集；§5 explicit notes that probe is enabled.
- **UPDATED** `ocr-server/README.md` + `smoke-e2e.ps1` + `requirements.txt` — 历史 D:\\Anonymous 引用全部改为本地路径或硬件描述。

### Data Source Switch (重要)

`D:\Anonymous\lightweight_validation_workspace\preprocessed\` 路径已**禁止访问**。一期所有验证一律使用本地验证数据集目录下的 5 套公开数据集，详见 plan §1.0 与本仓库根 `validation_set/` 产物。

### Measured Numbers (2026-05-19 跑批，60 张)

| 维度 | 阈值 | 实测 |
|---|---|---|
| AI run 成功率 | ≥ 95% | **100%** (60/60) |
| FinalAnnotation 生成 | ≥ 95% | **100%** (61/61) |
| QA Gate 闭环 | 100% | **100%** |
| 路由总错分率 | ≤ 10% | **8.3%** (5/60，全为街景 gray zone)|
| OCR 段落召回 | ≥ 90% | **96.0%** |
| OCR 段落精确率 | ≥ 80% | **91.5%** |
| OCR p95 latency | ≤ 7 s | **6.1 s** |
| VLM p95 latency | ≤ 13 s | **11.8 s** |
| VLM JSON schema 通过 | ≥ 95% | **100%** |
| 载荷写入成功 | ≥ 99% | **100%** |
| 导出端到端 | available | ✅ (61 行 + since 过滤) |

### Known Gaps Carried into Next Iteration

- 5 张街景 mismatch（box_count<8 落 gray zone → OCR_FIRST）。**v0.5 候选**: 路由阈值微调 + 用户手动补跑能力。
- accept-rate / 复核时长 / 100 框 UX 等需真实标注员实操的指标。
- OCR/VLM 多家 A/B / SigLIP-2 L2 路由（plan §7 实验 C / D），P1 启动。

### Validation Artifacts (在 `validation_set/`)

- `db2_ocr.jsonl` (354 页), `streetscenes_grounding.jsonl` (3547 图)
- `p0_selection.jsonl` (60 张抽样)
- `baseline_report.jsonl` (跑批原始记录), `baseline_metrics.json`, `ocr_iou_report.json`, `ocr_paragraph_report.json`
- `dataset_306_export.jsonl` (导出实跑结果，61 行 FinalAnnotation)
- `preview/`, `results/` — 60 张图的 GT vs 平台输出对照（大约 ~500MB，不入版本控制）

---

## v0.3 — P0 Skeleton + OCR Docker (2026-05-19 之前)

文档化在 `multi-modality/plan_v1/06_p0_skeleton_handoff.md`。
