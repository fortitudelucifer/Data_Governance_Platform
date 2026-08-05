# CLAUDE.md — 接手须知

多模态数据标注平台。**文本 / 图片 / 音频 / 视频**四条管线均已上线并经真环境验证。
下一步：**Phase C 医学影像已拍板（2026-07-18）：先 3D（CT/MRI）后病理**，从 C0 起步；
Phase D（具身智能）保持草案。

> ## ✅ 已落地（2026-07-17）：载荷层收敛进 Postgres（方案-07）
> 方案：`multi-modality/plan_v2/Archive/执行方案-07-载荷层迁移-Postgres.md`（N1–N7 全部完成；
> 旧方案文档已整体封存进 `Archive/`，plan_v2 根目录只放新栈版的 00/04/05）。
> - 载荷层收敛为 10 张 **"提升列 + jsonb payload"** 表
>   （`000002_payload_tables.sql`：ai_results / trace_logs / human_annotations /
>   final_annotations / annotation_tracks / track_snapshots / track_rounds /
>   review_comments / text_ai_candidates / text_ai_judge_runs）。
>   查询/外键/乐观锁走提升列，**整个文档读写只走 payload jsonb**；字段更新用
>   `payload = payload || delta` 顶层合并，列与 payload 不会漂。
> - 载荷表 id 是 24 位 hex TEXT（`repository.NewHexID()`），沿用历史 id 的
>   出参形状——**API 出参零变化**（H3）。
> - 载荷表全部 `FK ON DELETE CASCADE` 指向数据脊柱：**P-H1「id 嫁接」从结构上
>   消亡**（编不出指向不存在任务的载荷行）。`trace_logs` 刻意无 FK（可观测性
>   比对象活得久，task_id=0 是文本线约定）。
> - 幂等升级成约束：每任务单 active 标注/track 号、快照 `(final_annotation_id,
>   track_id)`、`run_id` 唯一——重放安全是 schema 的属性，不再靠代码自觉。
> - QAService.Pass/Reject 与 finalize 同库同事务（N4）；跨库补偿机器随之退役
>   （`compensation.go` 还在但两步已同库，仅保审计日志形状）。
> - 载荷模型统一在 `internal/model/payload`（别名 `paymodel`），payload 键由
>   json tag 决定。
> - 语义锁在 `payload_repo_test.go`：乐观锁 409、快照幂等重放、单 active、
>   裁决不动 version 等 8 条，真 Postgres + 变异验证。
> - 2026-07-17 执行了**最后一次三清 + 空库自举**。
>   此后开发依赖只有 **Postgres + Redis（+ 本地 blob / MinIO）**。

- 后端 `backend/` — Go 1.25 · Gin · GORM · **PostgreSQL** · Redis · MinIO
- 前端 `frontend/` — React 19 · TS · Vite · shadcn/ui · Zustand · TanStack Query
- Windows + PowerShell 开发；CI 在 ubuntu runner 上

> ## ✅ 已落地（2026-07-15）：关系库 = PostgreSQL，单真源 schema
>
> 执行方案-06 的 M1–M11 已全部落地并过三层验收（P1–P4，净环境）：
> - **schema 唯一真源** = `backend/internal/repository/migrations/000001_init.sql`
>   （goose，Postgres 方言）。AutoMigrate 已删；旧 SQLite 迁移链（7 .sql + 000008 Go）
>   的内容全部并入这一个建库迁移，**内容零丢失**。
> - **M6**：`UNIQUE(dataset_id, sha256)` 部分唯一索引（`WHERE qc_status='passed'`），
>   上传走 `ON CONFLICT DO NOTHING` ——「并发上传同一文件插两行」从结构上关死。
>   `AllowDuplicate`（允许重复导入）功能随之移除。
> - **M7**：数据脊柱全加 `FK ON DELETE CASCADE`；删数据集逐资产清 blob/载荷行
>   （`CompensationHandler.WithAssetService`），实测 0 孤儿。**不加指向 users 的 FK**
>   （owner_id/uploader_id 的「0 = 系统」写入约定）。
> - **M9**：仓储/模型/连接串统一为 `repository.DB` · `internal/model/relational`
>   （别名 `dbmodel`）· `DATABASE_URL`。
> - **M11**：数据集级 AI 配置收敛为一列 `datasets.ai_config`（jsonb，按 capability 分键）；
>   读写只走 `VideoAIConfigFromDataset / PatchAIConfig`。
> - **单测也跑真 Postgres**（`internal/testutil.DB`：每个夹具一个独立 schema + 真
>   goose 迁移——测试踩到的唯一约束/外键/jsonb 就是生产 schema 本身）。SQLite/
>   glebarez 与 `gorm.io/datatypes` 已**彻底移除**（自研 jsonb 类型）；go.mod 已无
>   方言残留，go.sum 仅剩 goose 测试依赖的 sqlite 校验行，无任何代码路径。测试库不可达时 t.Skip
>   并大声提示（那不是绿）——把 `data_governance_test` 库当硬依赖。
>   ⚠️ fixture 的 search_path **刻意不含 public**：否则 goose 会看见集成测试留在
>   public 的版本表而静默跳过建表（踩过）；pg_trgm 装在 pg_catalog 以便各 schema 可见。
> - `postgres_integration_test.go` 用同一测试库的 public schema 验 P1–P3
>   （约束存在性、并发去重恰一行、级联）；CI 后端 job 的 postgres:16 service 真跑迁移。
> - **R-01**：前端权限走能力层（`roles.ts` 的 `canAnnotate/canReview/...`），页面零角色
>   字面量；真值表单测 + 变异验证。注意：**任务指派后端是 admin-only**，前端已对齐。
>
> **2026-07-14 评审拍的四个语义**（决策记录 `multi-modality/plan_v3/Archive/Fable-评审-意见.md`，
> 已随一次性评审材料整体封存；注意其「载荷库保留」前提已被 07 取代）：
> ① **资产 = manifest**——多文件资产（DICOM series / scene）的 `sha256` = manifest 哈希
> （已写进迁移文件注释）；② `asset_derivatives.kind` **不做 DB 枚举**（已如此建表）；
> ③ `datasets.ai_config` 收敛列（已落地）；④ `annotations/` 几何 blob **永不同步删除**，
> 墓碑 + 宽限期 GC（Phase C/D 实现时生效，00·契约规则 3）。
> 另拍板：**C/D 不新增 `medical_*`/`lidar_*` 角色**，PHI 资质作为用户属性单独建模（04·C-H1）。

---

## 跑起来

```powershell
# 依赖：PostgreSQL 16(5432, 容器 data_governance_postgres) + Redis(6379, 容器 data_governance_redis) + ffmpeg
# 首次：docker run -d --name data_governance_postgres -e POSTGRES_PASSWORD=postgres `
#         -e POSTGRES_DB=data_governance -p 5432:5432 postgres:16
#       docker exec data_governance_postgres psql -U postgres -c "CREATE DATABASE data_governance_test"
#       （data_governance_test 给单测/集成测试用，是一次性的，别放 dev 数据）
#       docker run -d --name data_governance_redis -p 6379:6379 redis:7-alpine
.\scripts\start-backend.ps1 -WaitForReady    # → :8280，空库会自动跑迁移并建 admin/admin123
cd frontend; npm install; npm run dev  # → :5173
```

脚本**每次启动都会重新编译** `app.exe`（见下面第 5 条：以前不编译，害我拿旧二进制
跑出过一次假绿的 e2e）。

**存储是隔离的**：Postgres **`data_governance`**（另有一次性的 `data_governance_test`
给集成测试 DROP SCHEMA 用，别把 dev 数据放进去）· Redis **db 1**（专用容器
data_governance_redis；db 1 是既有配置默认值，沿用别改）。对象存储 dev 走本地
blob（`MM_OBJECT_STORE_DRIVER=local`），**无需 MinIO 容器**；上规模再建。

**ffmpeg**：脚本按 `MM_FFMPEG_PATH` → `tools/bin/` → PATH 的顺序找。二进制不进 git
（`.gitignore` 挡着）。没有它 → 帧索引出不来 → 视频工作台总帧数 0、跳帧全被 clamp
到 0，且**不报错**。`winget install ffmpeg` 或把 exe 丢进 `tools/bin/`。

三个测试层，**全都要过**：

```bash
cd backend        && go vet ./... && go test ./...          # 71 个测试文件，全部跑真 Postgres（需 data_governance_test 库；TEST_DATABASE_URL 可覆盖）
cd frontend && npm run lint && npm test && npm run build   # vitest 61 用例
cd frontend && npx playwright test                    # e2e 23 条，需后端在跑
```

CI 三个 job：后端（**postgres:16 service**，集成测试真跑迁移）/ 前端(lint+test+build)
/ **e2e（起真后端 + Postgres + Redis + ffmpeg，空库播种）**。

---

## ⚠️ 会静默出错的地方（本项目最贵的教训）

这个项目的 bug 有一类特别危险：**不报错，一切看起来完全正常，但内容是错的**——
导出的文件、落库的资产、甚至"全绿的测试"。已经栽过七次。下面每一条都是真事故的产物。

### 1. 插值契约被实现了两遍，靠共享夹具锁死

`InterpolateAt` 在 **Go**（`backend/internal/service/track_interpolation.go`，算导出器写进文件的框）
和 **TS**（`frontend/src/lib/trackInterpolation.ts`，算画布上标注员看到的框）各有一份。

两边读**同一批 golden 夹具**：仓库根 `testdata/interpolation/`。
后端从 `internal/service` 用 `../../../testdata/interpolation`，前端从 `src/lib` 也是三级向上。
**`testdata/` 必须待在仓库根**，动了它两边都挂。CI 两侧都跑。

改任何一端，另一端必须同步，否则「导出的框 ≠ 标注员看到的框」，而且**没有任何错误**。

> 曾经只有 Go 那半边被锁着，TS 侧裸奔了几个月——而两个文件的头注释都白纸黑字写着
> "both ends are locked"。**假注释比没注释更糟**：后来的人读到它，会以为改 TS 是安全的。

### 2. 帧号数学：`seekTimeMs` 与 `timeToFrame` 必须互为逆

`frontend/src/lib/frameIndex.ts`。seek 落在帧内 **1/4** 处，不是 1/2——
`timeToFrame` 用最近邻取整，分界线恰在半帧处，压线会跳错一帧。
`requestVideoFrameCallback` 的 `mediaTime` 有时回报帧 pts、有时回报 seek 时间，
两种都必须算回同一帧。30fps 下半帧偏移会让 8 帧里 5 帧跳错，且时对时错。

这段数学咬过三次，所以抽出来单测了（12 个用例，核心是互逆性质）。**别塞回组件里。**

### 3. 坐标系纪律

几何一律存**旋转已应用的显示像素空间**。`rotA.mp4` 是 270° 旋转，宽高对调——
资产的 `width/height` 若不是显示尺寸，YOLO 归一化会 0 除、COCO 会导出 `width: 0`。

### 4. 幽灵去重：缓存返回一条**不存在**的资产（2026-07-14 已修，别把它加回来）

上传走 SHA256 去重。以前 `UploadImage` 会**先读**一个持久、无 TTL 的 Redis 键
`asset:sha256:{datasetID}:{sha}`，命中就直接返回缓存里的那一行——**从不校验这一行
在库里还存不存在**。这才是根因：不是「两个环境共用了 Redis」，而是**去重信任了一个
从不校验的缓存**。任何「Redis 比库活得久」的情形都会中招：

- 两个环境共用一个 Redis db，dataset id 一撞就串（迁库时踩过，卡了半小时）；
- **换个空 `data.db` 做净环境自检、但没 flush Redis** —— dataset id 从 1 重来，
  直接吃到上一个库的键。**本文档自己推荐的测试流程，就是这个 bug 的触发器。**

症状：上传返回 200 且 `deduplicated: true`，但资产表是空的，任务也永远建不出来，
**不报任何错**。

**修法**：SHA 去重缓存**整个拿掉**，去重只问数据库（`FindAssetBySHA256` 走
`idx_assets_sha256` 索引；而这条路径上游已经把整个文件读进内存并 SHA256 过一遍了，
省掉的那次查询根本不可测量）。多段上传的 `RegisterAsset` 一直就是只查库的，从没出
过这个问题——它本身就是「只问库足够快」的活证据。

> **别再往去重路径上加缓存。** 回归网在
> `backend/internal/service/asset_service_dedup_test.go`：用 miniredis 把缓存投毒成
> 「键存在、指向库里没有的资产」，断言上传仍然落库。把缓存读加回去，它立刻挂。
>
> 老 Redis 库里残留的 `asset:sha256:*` 键已无人读取，是惰性垃圾，可以直接
> `redis-cli -n 1 --scan --pattern 'asset:sha256:*' | xargs redis-cli -n 1 del` 清掉。

### 5. 陈旧的 `app.exe`：三层测试全绿，测的却不是你写的代码

`scripts/start-backend.ps1` **以前不编译**——只检查 `app.exe` 存不存在，不管它是不是
旧的。于是改完 Go 代码直接跑脚本，起来的是**上一次编译的二进制**：后端单测绿（它们
编译当前源码），e2e 也绿（它打的是旧二进制），**而你的改动一行都没被执行过**。

我在 2026-07-14 这个 session 里就这么骗过自己一次：e2e 23 条全过，用的却是前一天的
二进制。**这是「绿色可以是假的」最阴的一种。**

现在脚本每次启动都 `go build`（`-SkipBuild` 可跳过，别用）。另外 git-bash 的
`pkill -f app.exe` 在 Windows 上**静默杀不掉**——用 `Stop-Process -Name app -Force`，
杀完拿 `Get-Process app` 确认，别信 pkill 的退出码。

### 5b. 陈旧的 `dist/`：同一个坑的**前端版**

`frontend/playwright.config.ts` 的 webServer 是 `npm run preview`——它服务的是
**`dist/`**（上一次 build 的静态产物），**不是源码**。改完 `.tsx` 直接跑 playwright，
**绿的是旧包，你的改动一行都没执行过**。

2026-07-19 我就这么骗过自己一次：修完布局做变异验证，**把布局改回坏的，测试照样全绿**
——因为浏览器拿到的是修复前就已经 build 好的包。差点据此得出"这个测试是空断言"的
错误结论，进而把一个**正确的修复**当成无效的删掉。

现在 command 改成 `npm run build && npm run preview`。但 `reuseExistingServer: true`
命中时整条 webServer 会被跳过——**本地改了前端源码，跑 playwright 前务必先
`npm run build`**。（vite preview 每次请求都从磁盘读 dist，重新 build 后不必重启。）

### 5c. 布局断言最容易写成空断言

用"文档里第一个能滚的 `overflow-auto` div"这种**启发式**定位滚动容器，会抓到
AppShell 的**侧边导航栏**——被测页面是死是活它都绿。→ 用 `data-testid` 锚定
**被测元素本身**，并断言"scrollTop 真的动得了"（`el.scrollTop = 99999` 后读回 > 0），
而不是"scrollHeight > clientHeight"（元素被父容器裁切时这条恒为假，反而漏报）。

### 5d. 三方验证要选「决策可能与你不同」的那一方

C3.4 导出 NIfTI 时栽过：我们的解析器和 nibabel **口味相同**（sform 优先），
于是 nibabel 三方锁全绿、自证往返全绿、连预览图都正常——直到装上 3D Slicer，
标签图与原图**错开 75mm**。

根因：NIfTI 可带两份朝向（sform 矩阵 / qform 四元数），真实文件里两者会矛盾
（MNI152 就是：sform 原点 −75.8，qform 原点 0），而生态**选择相反**：
**nibabel / FSL 认 sform，ITK / 3D Slicer / ANTs 认 qform**。

两条教训：
- **口味相同的第三方等于没验。** 真正的裁判是**下游实际消费者**
  （3D Slicer / QuPath / pycocotools），不是任意一个库。
- **别替用户仲裁源数据的歧义。** 我原本"只写 sform 不写 qform"，理由是
  "两份不一致是 L/R 翻转的经典来源"——方向对、结论错：防住了单文件内部矛盾，
  却造出**影像与标签跨文件不一致**，而两个文件单独看都完全正常。
  正解是两份原样带过去，下游用哪条规则都对影像和标签一视同仁。

### 5e. 判断解剖左右，别依赖世界坐标的绝对符号

同一次验收里，修完产品后左右判断反而翻红——是**判据**错了：用「世界 x 的正负」
判左右要求源文件原点正确，而真实数据经常不正确。上面那份 MNI152 的 qform
原点根本没设（默认 0），Slicer 优先 qform → 整个脑袋被放到 x∈[0,152mm]、
中线跑到 +76mm，同一体素在两种解释下得出**相反的左右**。

→ 解剖左右本就是**相对影像自身中线**的。先用脑组织质心定中线再判方位，
这个判据对「原点没设对」免疫。（复验脚本：`exports/verify_in_slicer.bat`）

### 5f. 测试替身简化掉的区别，正是 bug 藏身的地方

C0.6b 踩到：`ObjectStore.PutAt` 收**裸 key**，而 `Get/Delete` 收**带 scheme 的
storageURI**（`local://…` / `minio://bucket/…`）。给草稿服务写的内存替身把两者
当成同一个东西，于是"写用 key、读也用 key"的错误在单测里**全绿**——真环境却是
**PUT 返回 `saved:true`、GET 恒为空**（写入还报成功，最恶心的那种失败）。

→ 两条：
- 替身要**复现被测代码依赖的那个区别**（这里是 key ≠ URI），而不是图省事拉平。
  拉平掉的那一维，就是测试再也看不见的那一维。
- 确定性键 + 不落库 URI 的调用方，必须能从 key 反推 URI
  （已加 `ObjectStore.URIForKey`），否则写进去就再也读不出来。

### 5g. 用"合理区间"当断言 ≈ 没有断言

C3.25 连着栽两次，都是变异测试逮到的：
- 单测写"浮点越界不产出 NaN"，取的却是沿轴共线的整数点——cos 恰好等于 1，
  去掉夹逼照样通过。→ 随机搜 300 万组共线三点，找到真正让
  `cos = 1.0000000000000002` 的一组**冻进测试**。
- e2e 写"长度读数应在 20–120mm"，而 MNI152 是**各向同性**的，把 spacing 整个
  漏乘得到 62.1，**仍落在区间内**。→ 改成用真实 spacing 算出期望值再比
  （0.3 × 207 体素 × 0.737 = 45.8mm，实测 45.7），容差 3mm。

→ 两条：**能算出期望值就别写区间**；**夹具要选能区分对错的那一组**
（各向同性数据测不出轴序/spacing 错误，正交单位阵测不出朝向错误——同一个道理）。

### 6. 稀疏几何的陷阱

`Keyframe` 有 `Bbox` 和 `Points` 两种几何。SAM2 传播产出的 mask 轨迹**只有 `points`，没有 bbox**——
逐帧导出器（MOT/COCO/YOLO/Datumaro）若只看 `bbox`，整条轨迹会被**静默丢弃**，导出文件毫无异常。
四种格式的逐帧展开统一走 `trackInterp{boxAt}`（`video_export_service.go`），别各写各的。

### 7. 「id 嫁接」：跨库整数 id 引用曾让新资产**继承旧资产的标注**（07 已从结构上关死）

载荷层还独立成库的时代，标注 / track / 快照按 `asset_id` / `task_id`（关系库整数 id）**跨库**
索引，而空关系库的 id 从 1 重来——于是「只清关系库、不清载荷库」= 新 1 号资产直接
继承旧 1 号资产的标注，长在错的视频上，**不报任何错**。这是 06/07 两次迁移里最贵的
一类事故（P-H1）。

> **07 之后它编不出来了**：载荷行与任务同库，且全部 `FK ON DELETE CASCADE`——
> 指向不存在任务的载荷行插不进去，删数据集连载荷一起消失。「三清」作为运维仪式
> 退役（最后一次在 2026-07-17）。**但两条 Postgres 禁令仍然有效**：
> ❌ `TRUNCATE ... RESTART IDENTITY`、❌ `ALTER SEQUENCE ... RESTART`
> （`trace_logs` 无 FK，id 重用会把旧 trace 缝到新任务上；禁令写在
> 000001_init.sql 文件头）。清环境的正路是**整库 DROP DATABASE 重建 +
> Redis db1 flushdb + 清 `backend/storage/assets`**，别搞表级局部清理。

---

## 数据模型要点

```
数据集 → 资产 → 任务 → 标注 → FINALIZED 快照 → 导出
```

- **`annotation_tracks`** — track + keyframes 解耦（CVAT 式）。不变属性在 track 层，逐帧状态在 keyframe。
- **`track_snapshots`** — QA 通过时写入，**导出的唯一真源**。导出永远不读 `annotation_tracks`（会漂）。
- **`track_rounds`** — 每次提交的 track 快照，用于返工 diff（`annotation_tracks` 是原地覆盖的，不留历史）。
- 载荷表通用形状：**提升列**（查询/FK/唯一约束/乐观锁）+ **payload jsonb**（整文档唯一读源）。
  改字段 = `payload || delta` 顶层合并 + 同步提升列，**永远别只改其中一边**。
- 乐观锁：track 有 `version`，冲突返回 409。
- `outside: true` 的关键帧起**停止产帧**，不做跨 outside 的线性过渡，**不外推**。

## AI 能力

`CapabilityAdapter` 接口 + `capabilityForStrategy` 路由。模型服务跑在 **103 GPU 机**（RTX 3090）：

| 能力 | 端点 |
|---|---|
| `video.detect_track` (YOLO/RT-DETR + BoT-SORT) | `<MODEL_HOST>:8382` |
| `video.sam2_propagate` (跨帧 mask 传播) | `<MODEL_HOST>:8384` |
| `seg.sam2_volume` (体数据跨切片传播, C4.2) | `MM_SEG_VOLUME_ENDPOINT`（待部署；本地 CPU stub :8385） |
| `seg.interactive` (MobileSAM 点选) | `<MODEL_HOST>:8381` |
| `asr.transcribe` (FunASR + emotion2vec) | `<MODEL_HOST>:8390` |
| LiteLLM 网关 → DashScope VLM | `<MODEL_HOST>:4000` |

服务端代码在 `deploy-to-103/`（Dockerfile + server.py），部署步骤见
`multi-modality/plan_v2/部署运维-103-GPU模型服务器接入全过程.md`。

**GPU 成本闸门（B2.8）**：`GPUGate` 串行化 + 有界候诊室，满了返回 **429 而非 500**。
数据集级 AI 配置在 `datasets.ai_config`（jsonb，按 capability 分键；M11 由
`video_ai_config` 收敛而来）里，`max_frames` 是**硬天花板**——调用方只能调小，
天花板属于数据集所有者，不属于点按钮的人。

**DashScope 的坑**：这个账号只开通了**稳定版**模型名（`qwen-vl-max` / `qwen-vl-plus`）。
`-latest` 快照别名会返回 **403**。别「顺手升级」成 `-latest`。

---

## 测试纪律（比测试本身更重要）

**绿色可以是假的。** 这个 session 里，三个真 bug 是靠下面的手段挖出来的，常规 CI 全绿。

### 净环境自检
开发库上永远绿的 bug，换成**空库 + 独立 Redis db** 立刻挂（= 重建的空 Postgres）。
这样挖出过：Redis 的 db 被静默丢弃（`redis://host/1` 变成 db 0，两个环境共用缓存）、
数据集搜索只过滤当前页（第 5 页上的永远搜不到，因为开发库里目标恰好在第 1 页）。

### 变异测试：先证明测试会挂
写完断言，**故意把功能改坏，确认它真的失败**。用这个手段抓到过一条**空断言**——
断言「跳到第 0 帧」，而视频本来就从第 0 帧开始，**跳转根本没发生也会通过**。
（种子里把首关键帧挪到第 5 帧才修好。）

### 断言查询计划：必须挑「没有逃生通道」的那条查询（2026-07-23 踩到）

给「我的任务」补 `assignee_id`/`reviewer_id` 索引时，回归测试第一版断言的是

```sql
SELECT * FROM annotation_tasks WHERE (assignee_id=? OR reviewer_id=?)
ORDER BY id DESC LIMIT 20        -- ← 挑错了
```
「不走 Seq Scan」+「计划里有 Index」。**把索引删掉，测试照样绿。**

原因：带 `ORDER BY id DESC LIMIT 20` 时，规划器改走
`Index Scan Backward using annotation_tasks_pkey` + Filter，凑够 20 行就收工——
**主键索引把两条断言都白白满足了**。这条查询恰好是唯一能绕开缺失索引的形态。

改成断言 `COUNT(*)`（列表页每次都先跑它取总数）才有效：它没有 `ORDER BY` 这条
逃生通道，只能在 Seq Scan 与位图索引扫描之间二选一。双向验证：有索引 PASS、
删索引 FAIL 并打出 `Seq Scan on annotation_tasks`。

两条通用教训：
- **`EXPLAIN` 类断言别只说「不是 Seq Scan」**——主键/其它索引都可能顶上来。
  要么断言**具体用到了哪个索引名**，要么挑一条没有替代路径的查询。
- **行数太少时规划器一律选 Seq Scan**（扫 4 行比走索引便宜），所以这类测试
  **必须自己造够数据量 + `ANALYZE`**，空表上断言等于没断言。

### e2e 并行不安全：钉死 `workers: 1`（2026-07-23 定位）

`playwright.config.ts` 的 `fullyParallel: false` **只管住同一文件内**串行，
文件之间仍按默认 worker 数（CPU/2，16 核机 = 8）并行。实测：

| workers | 结果 |
|---|---|
| 8（默认） | 2 失败 |
| 4 | 2 失败 |
| **1** | **23 全过**（连跑两次稳定） |

每次失败的用例组合都不同，报错永远是 login 的
`page.waitForURL: Timeout 15000ms exceeded`。

**别急着怪产品慢**——后端每层都实测过：登录 **60ms**（并发 10× 不变）、
`/dashboard/stats` **3ms**（并发 8× 不变）、`/tasks?mine=1` **6ms**。
是同机 8 个 Chromium + Postgres/Redis 容器 + 后端 + vite preview 抢资源，
把页面 `load` 事件拖过 15s；再叠加 e2e 本就有的共享可变状态（见下节），
并行只会放大成**随机假红**。

已在配置里钉成 `workers: 1`：全量 57s vs 并行 38s。**多 20 秒换确定性，值——
假红比慢贵得多，它会让人开始习惯性忽略红灯。**

### e2e 数据靠播种，不准硬编码 ID
`frontend/e2e/seed.ts` 幂等播种（按名字找，找不到才建），CI 空库从零建。
**绝不要写 `dataset 326` / `task 420` 这种开发机 ID**——CI 上跑不起来，且残渣会污染下一次。

播种里刻意分了三个视频任务：`reviewTask`（会被推进审核态）、`editTask`（始终可编辑）、
`submitTask`（会被提交）。**共用一个任务必然假失败**——这个坑一天内踩了两次。

### 跨软件验证，不自证
导出格式的正确性用**外部工具**验：KITTI devkit、QuPath、3D Slicer。
自己写的导出器 + 自己写的断言 = 一起错也发现不了。

---

## 我犯过的错（别重复）

- **写了一句假注释**（"the frontend vitest suite consumes the SAME files"），它假了几个月，
  我还把它当事实**抄进了执行方案文档**。→ **断言之前先核实**，尤其是关于"某某已经有测试/已经被锁住"的话。
- **误提交到 main**。→ 先开分支。
- **Shell 的 `&&` 误报**：`git ls-files 不存在的文件 | head -1 && echo "被跟踪"` 会打印"被跟踪"——
  `git ls-files` 对未跟踪文件输出空但退出 0。→ 用 `git ls-files --error-unmatch`。
- **单测全绿、整条竖切却是断的**（2026-07-18，C3.1 实测）：单测各自调服务层，
  绕过了 QC、建集 API、worker 认领这些"接缝"，把**四个洞同时盖住**——建集 API
  收不了 `data_source`（医学数据集永远收不了资产）、QC 不认识 NIfTI、普通上传
  路径没给 volume 入队、worker 认领的模态白名单硬编码 audio/video。每个洞的症状
  都是"卡住且不报错"。→ **新模态/新管线接完，必须从 HTTP 口子拿真实文件跑一遍
  端到端**，别信"单测全绿"。同类接缝要标出"三处必须一致"（入队条件 / 认领白名单 /
  derive 分支）。
- **go test 缓存会回放 SKIP**：Postgres 容器停了 → DB 测试全 skip（包级仍是 ok）；
  起好容器再跑，**缓存把上一轮的 skip 原样回放**，看起来"还是跳/还是绿"。
  变异验证时更致命：文件改回去=缓存命中=红都看不见。→ 验证修复/变异必带
  `-count=1`，且看 `--- SKIP` 行数，别只看 ok。（C0.1 当场差点被骗。）
- **Playwright：`waitForURL` 通过 ≠ 旧页面已卸载**。`getByRole('返回')` 可能抓到上一页的按钮。
  → 先等新页面独有的元素出现。
- **Playwright：`svg rect` 会抓到 lucide 图标**（它们也是 svg）。→ scope 到
  `svg[preserveAspectRatio="none"]`。
- **Playwright 测崩溃恢复的两个必要条件**（2026-07-19，C0.6 验收）：① 必须用
  **persistent context**——默认 context 的 IndexedDB 随 context 一起蒸发，
  测出来的永远是"数据没了"；② 必须是**真崩溃**（CDP `Page.crash`）而不是
  `close()`——close 会触发 pagehide，那正是我们主动 flush 的优雅路径，
  恰好绕过要防的场景。另：`Page.crash` 的 promise **既不 resolve 也不 reject**
  （回复它的渲染进程已经死了），`.catch()` 救不了，必须用超时赛掉，否则测试卡死。
- **Playwright：别用"会被点击改变的属性"做选择器**（2026-07-19 踩到）：按
  `title="隐藏"` 定位显隐按钮，点击后 title 变成"显示"→ 自动重试会把所有同类按钮
  挨个点一遍、最后无限等待。**看起来完全像应用卡死**，实则是选择器自伤。
  → 用 `data-testid` 或位置选择器；且注意 `aside` 里有多个 `ul>li` 列表时
  `.last()` 会串到另一个列表。

---

## 当前状态与待办

> ### 📐 这份文档写什么、不写什么（2026-07-19 定，先立规矩不搬东西）
>
> 每个里程碑现在被写两遍——这里一遍、`multi-modality/plan_v2/执行方案-04` 一遍，
> 详略已经在漂。**两处描述同一件事、其中一处过时**正是本项目最贵的失效模式
> （"执行方案-00 里还留着 Mongo"就是这么被逮到的）。所以定权责：
>
> - **CLAUDE.md 只留两类**：① 新会话**不先知道就会把东西搞坏**的（约定、静默出错的
>   地方、跑起来、测试纪律）；② **跨里程碑**的教训与结论。
> - **逐里程碑的实现细节归 04**。这里只记一句话结论 + 提交号，不复制 04 的正文。
> - ⚠️ **但 04 不在版本库里**（`.gitignore` 忽略整个 `/multi-modality/`，它在一个
>   **独立的本地私有仓库**里管版本）。所以**任何安全关键的结论必须在 CLAUDE.md
>   里自足**——不能写成"详见 04"就算完，04 可能不在读者手上。
>
> **本次不搬动任何已有内容**：C 阶段正在 churn，此时划边界等于在还会变的材料上
> 划线，之后每归错一次文档就开始说谎。规则先立住、让《当前状态与待办》停止膨胀，
> 等 C 收尾再一次性收敛。

**已完成**：文本 · 图片 · 音频(A1/A2/A3) · 视频(B1/B2/B3 全部，含四眼规则、逐 track 裁决、
返工 diff、六种导出格式流式写出、GPU 成本闸门、审核导航)。

**2026-07-18 拍板（Phase C 开工）**：
- 走医学影像线，**先 3D（C3/C4）后病理（C1/C2）**；里程碑顺序 C0 → C3 → C4 → C1 → C2。
- 00《稠密几何存储契约》**已定稿**（Gate 0 勾上），批准时附三点强调：人工数据绝不丢失、
  **间隔自动保存**（草稿机制升级为硬需求）、**库表 ↔ 文件本体映射必须清晰**。
- C-Q1（PHI）：开发期全用公开去标识数据集；**真实病人数据是明确的未来需求**——
  `DeidGate` fail-closed 导入闸门、sha256 算在去标识后字节、`UIDMapStore` 接口
  从 C0 就建；接入前还有三项待拍板（跑在哪台机器 / 映射表存放 / 合规确认），
  到时要主动提醒用户。详见 04《C-Q1 预留设计》。

  > **⚠️ 启用临床前必堵的缺口（2026-08-03 对抗式 review + 逐条证伪；安全关键,
  > 在此自足——04 不在库里，别写成"详见 04"就算完）。** 整条临床去标识路径**今天是
  > 休眠脚手架**：`pipeline=nil`（clinical 一律拒）、`Deidentify()` **全仓无人调用**、
  > `StripAssociatedImages` 只有测试在调。**闸门今天 fail-closed 是靠"pipeline 不存在"，
  > 不是靠去标识真的发生**——所以把 pipeline 接上非 nil 是个脚枪：会放行 clinical 却
  > 一步去标识都没做。启用前逐条堵掉（细节在记忆 `clinical-deid-blockers`）：
  > - **接 pipeline ≠ 去标识**：`DeidGate.Admit` 对 clinical-有pipeline 直接记 success、
  >   从不调 `Deidentify`；没有 importer 调它。必须做成原子入口「去标识→后验通过→
  >   才算 SHA / 写存储 / 记 success」。multipart 同理：`Init` 现在会签 `uploads/.../raw`
  >   直传 URL，启用后原始 PHI 会先进共享桶——clinical 不能走原始直传。
  > - **闸门只看 `dataset.Modality`、不看内容**：.nii/.svs 传进 image 数据集就绕过闸门、
  >   存成 image。`sniffMedia` 已能识别 volume/wsi kind → 非医学集里检出医学内容即拒。
  > - **文件名 PHI**：`OriginalName`（患者名/UID）原样落库 + 出参 JSON + 导出 file_name；
  >   去标识不碰文件名。临床改服务器生成的 opaque 名 + 白名单扩展名。
  > - **strip 只抹像素、不抹文本标签**：`StripAssociatedImages` 清 label/macro 像素 + 改链，
  >   但 `ImageDescription` / 厂商私有 ASCII 标签原样带出（DICOM PS3.15 tag 清洗是另一步，
  >   D-1）。**别把它当"全部去标识"**。且 tiled 式 / 非 Aperio 的 label 目前分类不出
  >   （tiled 一律当金字塔保留），接非 Aperio 前需更强判据。
  > - **审计写失败仍放行**（`DeidGate.log` 刻意吞错，注释"非致命"）：临床合规下是否改
  >   fail-closed（没写下 medical_ingest 审计就不准入）——**待用户拍**。
  >
  > **已修（2026-08-03）**：① 单文件上传把闸门拒绝错映射成 500 → 改 **400 + 指路文案**
  > （`asset_handler.go`，与 multipart Init 对齐；本地无 MinIO 时医学上传走单文件路径，
  > 这条会咬现在）。② `StripAssociatedImages` 收成 **fail-closed**：strip 数组数量不等 /
  > 类型不支持 / 越界 / 链在 next 指针处截断一律**报错**，不再"看着成功却残留可取证的 PHI
  > 像素"；越界长度在建补丁前挡住（堵 OOM）；内联 `ImageDescription` 照读（BigTIFF 8 字节
  > 值域塞得下 "label"，以前返回空 → 短 label 漏分类被保留）。3 条造畸形 TIFF 测试 + 逐条
  > 变异验红。**GPT 那轮 10 条存在性全属实、0 误报**，唯一缺的上下文就是"这条路今天是死的"。

- 剩余待答（到对应里程碑前再问）：C-Q6 是否体渲染（C3.2 前）。
  （C-Q4/C-Q5 已答，见下。）

**2026-07-23 拍板（C-Q4 病理对象粒度，C1 开工前的阻塞项）**：
用途 = 训练模型 + 科研统计 → **区域与细胞都要但分层，任务粒度 = ROI**。
- **区域**（annotation，几十/片，手绘）走现有 track/payload；**细胞**（detection，
  ~418/ROI、6.7 万/片，AI 产出 + 抽样复核）**与区域分开存**，但——
- ⚠️ **实测更正（2026-07-23）**：拍板时我说"细胞必须外置，24MB/片是上限的 6 倍"，
  **实测推翻了这条**（`tools/cq4_cell_scale_measure.py`，真实 IHC 图上量 372 个真核）：
  每核 **bbox-local RLE 仅 42 B**（简化多边形 156 B、原始轮廓 952 B），
  整片 RLE 2.7 MB **仍在 4MB 限内**；而任务粒度既已定为 ROI，一个 ROI 的全部
  细胞只有 **17 KB**。**不需要新建外置路径**——前提是**一个 ROI 一条 track 装
  全部细胞**，不是一细胞一条 track。估算漏了 RLE 比多边形省 3.7×。
- **细胞与区域分开的真正理由不是字节**：① 行数（一细胞一 track = 整片 6.7 万行）；
  ② QA（6.7 万对象不可能四眼逐个裁决，只能抽样）；③ 编辑方式（AI 产出+修正 vs 手绘）。
- **任务 = ROI 不是整片**：编辑锁是 task 级，整片一把锁会锁死数天工作量；
  且公开数据集（PanNuke 256²、MoNuSeg/CoNSeP 1000²）本来就是 ROI 形态。
- ⚠️ **别和 C4.2 的分层原则搞混**：判据是**量级是否同一档**——同档必须同格式
  （C4.2：AI 产出的 voxel_mask 与手画逐字节相同）；差三个数量级必须分路径
  （区域 vs 细胞）。依据与出处记在 04《C-Q4 决策记录》。
- `multi-modality/plan_v2/执行方案-05-具身智能.md` 保持草案：D-Q1 数据来源、D-Q2 先
  3D 框还是点云分割、D-Q3 **坐标系 ego/global**（选错等于重写几何层）均未答。

**C0 地基进度**（不产出 UI，决定后面全部设计；细节在 04·C0 里程碑，本地文档）：
- C0.1 ✅ DeidGate fail-closed 闸门 + 医学模态地基（提交 15d1fe9）。
- C0.2 ✅ 派生物目录前缀不加列，`storage_uri` 尾随 `/` 约定 + kind 专属端点。
- C0.3 ✅ 体素分割量级实测（真实 MRI 脑分割 + 忠实 COCO RLE）：整卷 11.4MB 必外置、
  逐切片 RLE 仅 369KB（双护栏大幅未触及）、64³ 单笔编辑 1–2 块 ≤512KB——契约三条
  量级判断被真数证实。工具 `multi-modality/tools/c03_rle_scale_measure.py`（可指真 CT 复跑）。
- C0.4 ✅ RLE 不插值双端契约 + kind→夹具 meta-test 机器强制（dc7f980, 9276640）。
- C0.5 ✅ 位深决策 16-bit（8-bit 烘焙丢调窗，放射科不可用）；渲染性能基准随 C3.2 落地。
- C0.6a ✅ **本地自动草稿**（3a50652）：逐笔操作日志 → IndexedDB，崩溃后可恢复到
  最后一笔。**实时涂抹与崩溃回放共用同一个 `applyOp`**——页面改掩膜必须走
  `mutate()`，不许直接动 buffer；两条路径同码，"恢复出来的 ≠ 画的那个"这类
  静默错误**编不出来**。性质测试锁死（4 种子 × 400 随机操作逐字节相等）。
  真浏览器 CDP 崩溃验收通过。
- C0.6c ✅ **撤销/重做**（412834f）：与草稿共用同一份日志。撤销 = **少放几条重来**
  （`rebuildInto` 复位到基线再回放前 n 条），不是求逆。两条纪律：**基线是"服务端
  载入时的样子"而非空**（否则全部撤销会抹掉别人已保存的标注）；**就地 `mask.set()`
  复位、绝不新建缓冲区**（11 MB 数组换身份会卡死渲染进程）。撤销必须**同时截断
  草稿日志**，否则撤销后崩溃会让被撤销的笔画复活。
- C0.6b ✅ **服务端草稿**（5110818 / 9406357）：每 `MM_DRAFT_FLUSH_INTERVAL`
  （默认 5m，由服务端下发）推一次操作日志到对象存储 `uploads/` 短命前缀，
  按 (task,user) 隔离。解决**换机器续作**；崩溃兜底仍是本地那份。
  **草稿不是标注**：不进导出、不进 QA，导出只读 `track_snapshots`。
- **载荷层已就位**：`voxel_mask` 轨迹类型 + `Keyframe.RLE`；modality 扩 `wsi/volume`。

**C3 进度**（3D 体数据人工闭环）：
- C3.1a ✅ NIfTI 解析 + `VolumeMeta`（`volume_probe.go`）：sform/qform、CT-MRI 推断、
  窗位预设；**C-H3 手性**（direction 的 det 符号，左右翻转的机器警报）单测 + 变异
  钉死；真实 MNI152 跨数据验证（提交 1afe2ba）。
- C3.1b ✅ 逐切片 16-bit PNG + `volume_meta` 派生（`volume_derive.go`），接入 media
  worker 早分支（volume 走纯 Go，不碰 ffmpeg）；像素→HU 一次仿射（`SliceScl`），
  解码往返验证；坏文件终止式拒绝（提交 601391c）。
- C3.1c ✅ viewer 取数端点（`volume_meta` 走 `/derivative/:kind`；`GET /assets/:id/slice/:z`
  拼前缀取片，404/400 齐全）；DeidGate 前移到 multipart Init（clinical 连上传 URL 都拿不到）。
  **NIfTI 竖切贯通**：导入 → 闸门 → 派生 → 端点（提交 aeaff62）。
- ⏳ C3.1 剩余：DICOM series / NRRD 导入器（含解析库选型，覆盖 LIDC-IDRI；
  NIfTI 已覆盖 MSD）——**欠账已登记**在 04 文首《待补能力登记》D-1/D-2 + 记忆
  `deferred-dicom-importer`，接真实 DICOM 前必补，别当"医学导入已完成"。
- C3.2a ✅ MPR 查看器两块纯数学核心（前端 `src/lib`）：`windowLevel.ts`（DICOM
  LINEAR 窗位映射 + LUT）+ `mprGeometry.ts`（三视重切采样 + 各向异性宽高比），
  各带单测 + 变异验证（提交 4b8e3e1）。
- C3.2b ✅ 渲染与数据通路全部落地：`mprRender.ts`（重切+LUT→RGBA，5319cac）·
  `png16.ts` **手写 16-bit PNG 解码 + Go↔TS 跨语言夹具锁**（93c4ba5，
  `testdata/volume/`）· `volumeLoader.ts` 并发取片按 z 装卷（fbb04ae）·
  MPR 三视工作台页 + 路由（e5db411）。
  ⚠️ **不能用 `<img>`/canvas 读切片像素**：canvas 2D 每通道 8-bit，会把 16-bit
  静默截断，C0.5 的位深保真当场作废且不报错——所以必须走 `png16.ts` 手工解码。
- ✅ **C3.1 竖切端到端实测通过**（真实 MNI152，bda801f）：建集→上传→QC→任务→
  worker 派生（volume_meta + 215 片 PNG16）→端点取数，并用 Python 独立解码
  交叉验证位深与像素映射。过程中修好 4 处"单测盖住的"断点 + 1 个既有去重 bug。
- C3.3a ✅ **COCO RLE 编解码（Go + TS）+ pycocotools 权威三方锁**：装了参考实现实跑
  比对，位串逐字节一致；验过的向量冻进 `testdata/rle/shapes.json`，两端各测一半
  （提交 5703adb）。**往返测试只证明自洽**——自洽但非标的编码器照样往返成功，
  直到导出到 3D Slicer 才发现全错。
- C3.3b ✅ 稠密掩膜服务端护栏（单帧 64KB / 整 track 4MB → 400，**文案必须指路
  voxel_label 并指明关键帧**；C0.3 实测量级安全通过，护栏不误伤）（6cf1892）。
- C3.3c-1 ✅ 切片掩膜 ↔ bbox-local RLE 互转 + 笔刷（`maskSlice.ts`）：**这一裁一放
  错了掩膜会整体平移，画面依旧像模像样但位置是错的**；13 条测试 + 2 条变异验证
  （b633ba9）。
- C3.3c-2 ✅ 笔刷接进画布 + voxel_mask 轨迹存取 + 跨切片传播（f661951）：掩膜以
  **整卷常驻**，三视共用同一份 → 叠加层与影像走同一个体素下标，**对齐是结构保证**；
  只在 axial 涂抹（关键帧按 z 存）；保存失败原样呈现后端护栏文案。
- ✅ **标注链路端到端实测通过**：PUT voxel_mask → 落库 → 读回 → 用 pycocotools
  独立解码，掩膜面积逐帧一致（RLE 经载荷层往返无损）；护栏实测 400 + 指路文案。
- ✅ **浏览器实测修正**（27989a7，用户实测 + Playwright 自查）：
  - **显示朝向没按 `direction` 摆（C-H3 真问题，自查发现）**：注释写了"渲染层应用
    direction"却根本没实现，一直按体素顺序上屏 → RAS 数据上下颠倒、冠状面看着像
    另一张轴位；左右则是神经科惯例（**上下颠倒一眼可见，左右翻转看不出来，却等于
    把左侧病灶报成右侧**）。新增 `mprOrientation.ts` 摆成放射科惯例 + **四边
    A/P/L/R/S/I 标签**（标签才是安全面）；翻转在采样时做，影像/掩膜/点选/十字线
    共用同一次映射。RAS 与 LPS 数据最终显示一致（有测试）。
  - **React `onWheel` 是 passive 监听**，里面 `preventDefault()` 无效 → Ctrl+滚轮
    缩放了整个浏览器。必须挂原生 `{passive:false}` 监听。
  - 新增右侧面板：三视层号 + 体素坐标 + 强度探针（CT 显 HU）+ **已标注层清单**
    （层号/面积，点击跳转）——215 层里找回画过的层，没清单只能滚轮碰运气。
- ✅ **多分割 + 面板可增删改**（0ae25ef）：每段一条 voxel_mask 轨迹，独立标签/颜色/
  可见性/未保存标记；可新建·选中·重命名·改色·显隐·删除·单独保存（删除先二次确认，
  已保存的连后端轨迹一起删）。面板统计参照 3D Slicer / OHIF：**物理体积 mm³/mL**
  （"285 体素"对医生没意义）、强度均值与极值、质心跳转、已标注层清单（mm²）。
- ⚠️ **会把整个页面卡死的坑（已修，别踩回去）**：把持有 11 MB TypedArray 的**新数组**
  作为**变化的 prop** 传给画布组件，React 渲染阶段能跑完但**提交阶段整个渲染进程
  卡死**——effect 再也不执行，页面连 `1+1` 都算不了，连 CDP 都取不回 CPU profile。
  → **大缓冲区放身份恒定的 ref，只用数字版本号通知重画**，永不参与 prop 变更比较。
  新增任何大体量数据（标签体、点云）照此办理。
- C3.4 ✅ **NIfTI 导出**（be66fbd / f17c207 / c3ca9f5 / 690794a）：单张多标签图 +
  逐段无损 zip（C-Q5 拍板"两种都出"）；重叠如实报账；草稿导出显式标记。
  **3D Slicer 跨软件验收 PASS**——过程中逮到"只写 sform"导致标签图错开 75mm
  的真 bug，见 5d/5e 两条教训。DICOM-SEG 后置为 D-3 欠账（依赖 D-1）。
- C3.25 ✅ **测量工具**（d7eb6d7）：RECIST 长径 + 角度。**危险点是拿屏幕像素当
  毫米**——各向异性下同样"屏幕 100 像素"横竖能差 7 倍，而错的结果长得完全正常。
  纪律：逐轴乘 spacing、面内轴映射由 `planeToVoxel` 反推（与重切共用，不另写
  switch）、角度先换算到物理空间。测试一律用各向异性夹具（各向同性测不出轴序错）。
- **C3 全线完成**：导入 → 派生 → 三视 → 多分割标注 → 草稿/撤销 → 保存 → 导出
  → 跨软件验证 → 测量，整条竖切通了。
- C4.2 ✅ **AI 点选跨切片传播**（c5307cf，`seg.sam2_volume`）：点体内一点 → SAM2
  把体数据当序列传播 → 一条 **voxel_mask 段（AI 源）**。**关键是分层**：AI 产出与
  手画段逐字节同格式，段面板/画笔/撤销/导出全部零改动复用——AI 段实测直接走 C3.4
  导出成 NIfTI。GPU 闸门（满 429）。`MM_SEG_VOLUME_ENDPOINT`；sidecar 在
  `deploy-to-103/`（真 server.py + 本地 CPU stub，103 不可达时用 stub 端到端测通）。
  C4.1（`seg.volume` 全自动器官分割）后置为 **D-4** 欠账。
- 派生 kind：`volume_slices`/`volume_meta`（+ 病理的 `dzi_tiles`/`slide_meta`）已注册。
- C1.1a ✅ **WSI 探针**（8edac38，`slide_probe.go`）：纯 Go 解金字塔 TIFF（svs/ndpi
  本质就是它），**不绑 OpenSlide**——探针要的全是普通 TIFF 结构，绑 cgo 只会让每台
  机器都要装 C 工具链。两个必须知道的点：
  - **BigTIFF**：真实大切片几乎都是 BigTIFF（8 字节偏移），小样本却是经典 TIFF。
    用 4 字节偏移读会得到**看起来合理但错的**尺寸且不报错 →「样本覆盖不到的路径
    必须自己造测试」。
  - **mpp 缺失 = 0（未知），绝不默认 1.0**：它是把像素换算成 µm 的依据，默认值会让
    测量错一个扫描倍率而读数看着完全正常（与 C3.25 同类）。
- ✅ **WSI label/macro 附属图剥离**（D-1b，`slide_strip.go` 的 `StripAssociatedImages`）：
  临床切片的 label 上**印着病人姓名/病历号**（PS3.15 烧录像素文字）。剥离是**外科式两刀**
  ——① 改 IFD 链跳过 label/macro（重探针 `HasLabelImage=false`）；② 抹掉它们的 strip
  像素字节（PHI 物理消失，取证也扒不出）；**金字塔那几个 IFD 一个字节不碰**（否则
  C1.1c 的瓦片偏移就得跟着改，极易静默错一格）。自造小 TIFF 单测（CI 可跑）+ **真
  CMU-1.svs 验证**：剥掉 [label macro]、层数/MPP 不变、**ExtractTile 逐层逐字节对拍相等**
  （金字塔真没动），流式不吃满内存。⚠️ 它是 `DeidPipeline` 的 **WSI 臂**，随临床上线接
  （C-Q1 三项前置未决前，公开去标识数据的 label 本就匿名、不必剥，所以现在不接活路径）。
- C1.1c ✅ **不预生成 DZI**（真实 CMU-1.svs 实测拍板）：Aperio 瓦片是**缩略 JPEG**
  ——省掉了共享量化/Huffman 表（表存一次在 tag 347 `JPEGTables`，~289B）。裸瓦片
  以 `ffd8ffc0` 开头、没有 DQT/DHT，浏览器直接判「broken data stream」。修法是取瓦片时
  把那段表**拼回 SOI 之后**（`out = FFD8 + tables[2:-2] + tile[2:]`），**零重编码**。
  → 结论：**按需从原片取原生瓦片，不预生成 DZI 金字塔**（省一份等大的派生存储 + 一步
  离线转换）。前端用标准 DZI 层模型 + `tileExists` 门把没有原生对应的层关掉。
- C1.2a/b ✅ **瓦片端点**（f85062b/e0942df，`slide_tile.go` + `GET /assets/:id/tile/
  :level/:col/:row`）：`ExtractTile` **O(1) 内存**——直接 seek 到
  `TileOffsets` 数组的第 idx 个元素（切片有 2 万+ 瓦片，绝不物化整个偏移数组），
  拼好 JPEGTables 头返回可独立解码的 JPEG。Go 输出与 Python PIL 解码**逐像素零差**
  （跨层验过）；真切片端到端通。⚠️ 瓦片字节数标签可能是 SHORT/LONG/**LONG8**
  （>4GB 切片）——按 `TileByteCounts` 的 TIFF 类型读元素大小，写死 4 字节会在
  大切片上读**看着合理却错的**长度（同 BigTIFF 那类，M5 变异逮到过：LONG 夹具让
  「写死 4 字节」等价，补了 SHORT 夹具 + 逐字节等值断言才变红）。
- C1.2c ✅ **深缩放查看器**（`SlideViewer.tsx` + `slideTileSource.ts`）：OpenSeadragon，
  层映射数学抽成纯函数单测 + 变异锁死（9 测 5 变异红；网格边界的越界判定就在这层，
  变异 col<cols+100 单测当场变红）。**两张真实切片浏览器端到端**（e2e
  `slide-viewer.spec.ts`）：
  - 小片（AID，单层）：瓦片经鉴权端点 200、0 个 404、`tile-loaded` 逐块触发且 0 失败
    ——**证明 C1.1c 拼过头的瓦片能在真浏览器里解码**；变异 `minLevel→0` 当场变红。
  - 大片（AID_BIG=CMU-1.svs，46000×32914 原生 ×1/×4/×16）：从 fit 深缩放**直到全
    分辨率层出现**再停（别猜固定滚轮次数，尺寸一变就飘），断言**跨了多个原生层取瓦片**
    （单层切片给不出这个证据）、level 0 被请求、全程 0 个 404。⚠️ 注意这条 e2e 的
    「无 404」不是越界判定的主测（居中缩放+小平移够不到网格边缘,把上界放宽 100 它照过）
    ——**越界的牙齿在单测**（col 180 不存在那条），e2e 只当跨层导航的活体对照。
  三个会静默出错的点：
  - **原生降采样是 ×4/×16 不是 ×2**：不能把原生 3 层硬塞成 OSD 的 3 层（会踩 OSD
    内部「每层 /2」的假设，版本一变就崩）。按**标准 DZI 层呈现**，只有原生层对应的
    DZI 层有真瓦片，其余层 `tileExists=false` → OSD 从最近粗层放大填充。Aperio 降采样
    是 2 的幂，原生层尺寸精确落在 DZI 层上（11500=46000/2²）。
  - **`minLevel` 必须是最粗原生层的 DZI 层，绝不是 0**：OSD 的 tileExists 回退是
    **往更粗层找**，minLevel=0 时 fit-zoom 想要很粗的层、一路到 0 全 false →
    **什么都不加载也不报错**。变异实测（把 minLevel 改回 0）e2e 当场变红。
  - **瓦片端点要 JWT → 不能用裸 `<img src>`**（带不了鉴权头）：走
    `loadTilesWithAjax:true` + `ajaxHeaders:{Authorization}`。且 **OSD 6 默认 WebGL
    渲染器不发 `tile-drawn` 事件**（加了报错、canvas 2D 也读不回像素）——验证「组织
    真上屏了」得用 `tile-loaded`（取回并解码成图），不是 tile-drawn/getImageData。
- C1.3 ✅ **区域标注**（`SlideAnnotationOverlay.tsx` + `osdOverlayTransform.ts` +
  `SlideAnnotationPage.tsx`，task-routed `/slide-tasks/:id`）：区域（手绘、几十/片，
  C-Q4 拍板）**复用现有 polygon/bbox 轨迹**（单关键帧 frame=0，几何在 **level-0 像素**）
  ——**零后端改动**，与视频/图片同一载荷层，导出/复核不换算。视口归 OSD，标注在其上
  的 SVG 叠加层，两者由 OSD 视口每帧同步。真 WSI 浏览器端到端（e2e
  `slide-annotation.spec.ts`，TID 驱动）：画框→深缩放→框**钉在组织上**（渲染位置始终
  = imageToScreen(当前仿射, image 坐标)、image 坐标不变）→ 保存→刷新从轨迹载回。
  坐标同步数学抽成纯函数单测 + 变异（漏平移/漏缩放各变红）；e2e 变异「去掉视口同步」
  当场变红（scale 不再增大）。四个会静默出错的点：
  - **坐标同步**：叠加层与瓦片不同步 → 框落在**错的细胞**上不报错（坐标系纪律的
    病理版）。纪律：① 屏幕位置**唯一真源 = OSD 自己的坐标换算**
    （imageToViewerElement / windowToImage），绝不另算一套视口数学；② `<g>` 的
    transform 每次 `update-viewport`（含动画帧）重算，静态叠加层会漂。
  - **React onWheel 又咬一次**：绘制时叠加层吃掉指针 → 滚轮也被吃 → OSD 缩不了。
    转发滚轮给 OSD 缩放，**必须原生 `{passive:false}`**（React onWheel 是 passive，
    preventDefault 无效，与 C3.2 同）。
  - **空状态也是一个 `<li>`**：区域列表"还没有区域"占位也是 li，用泛 `li` 计数会把
    「没画上/没落库」当成「1 个」（空断言，本项目栽过多次）。断言必须锚
    `data-testid=slide-region-item`。这条差点让我把「保存其实失败了」误判成通过。
  - **保存在重瓦片加载下排队 ~数秒**：浏览器每 host 6 连接被瓦片 GET 占满，PUT
    轨迹会排到 ~3-4s 后才回（curl 直连仅 0.2s）。**不等保存完成就刷新 = 丢标注**；
    e2e 必须先等「已保存」再 reload。真机上这几秒延迟由 saveMsg 给反馈。
  - **WSI 绝不能走 image 分支**：`taskRouteFor('wsi')→/slide-tasks/:id`；落到
    ImageAnnotationPage 会整图（46000px）载入拖垮浏览器。
  - 只读/复核态：`EDITABLE_STATES`（HUMAN_PENDING/IN_PROGRESS/QA_REJECTED）+
    `perms.canAnnotate`，非可编辑态叠加层与面板全只读（复核员看到的即只读区域）。
- C1.35 ✅ **测量/标尺**（`slideMeasure.ts` + 叠加层 ruler 工具）：点两下量长度，
  µm/mm。危险同 C3.25/C1.1a——**拿像素当微米**：长度 = level-0 像素距离 × mpp。
  ⚠️ **mpp=0（未知）→ 尺子工具直接不出现**，绝不用编的标尺给看着合理的读数。纯函数
  单测（3-4-5、水平线，算出期望值不写区间）+ 变异（漏 ×mpp、漏 mpp<=0 门各变红）；
  e2e **值校验**：已知屏幕间距/仿射 scale/mpp → 期望 µm 唯一确定，直接比读数（±3%）。
- C1.36 ✅ **定位区域**（面板 Crosshair）：46000px 切片上点区域→视口 `fitBounds` 飞过去
  （没定位就滚轮碰运气找回画过的区域；复核员据此"跳到标注"）。
- C1.4 ✅ **GeoJSON 导出**（`slide_export_service.go` + `slide_export_handler.go`，
  `GET /tasks/:id/export.geojson`）：QuPath 原生 FeatureCollection，坐标是**全分辨率
  level-0 像素**（正是区域轨迹存的空间，**零换算**）。数据源纪律与 C3.4 一致——默认只读
  `track_snapshots`（交付真源），无快照直接 409，草稿须显式 `?source=draft` 且文件名/
  响应头标 draft。会静默出错的点:**GeoJSON 环必须闭合**（首尾点相同,否则有的解析器
  当折线丢面积——"有形状、面积 0"），`closeRing` 统一补。Go 单测锁结构/坐标/闭合/
  颜色解析；bbox ROI 面积实测 = w×h 精确对上。
  - ✅ **QuPath 本体验收 PASS（D-1c，无头 QuPath 0.6 + 真 CMU-1.svs）**：装了 QuPath
    用**它自己的解析器**把导出的 GeoJSON 摆进真切片 level-0 像素空间——切片读成
    46000×32914、Tumor 面积 11,650,000（shoelace 精确对上）、ROI 面积 12,000,000
    （=4000×3000）、质心落在组织区且在界内。这抓的是 shapely 抓不到的**坐标系错**
    （level-0 vs 缩略图/归一化 → 面积天差地别）。脚本
    `exports/qupath_verify_headless.groovy`（`QuPath script -i <svs> -a <geojson>`；
    exports/ 是 .gitignore 的本地工具）。人眼"压在组织上"的 GUI 复检可选留给真实交付前
    ——面积/质心已覆盖同一类错，同 C3.4 当初的 3D Slicer。
- **AI 能力接线的分层原则**（C4.2 立）：AI 预标注必须产出**与手工完全同格式**的
  标注（同 track kind、同几何编码），否则会有两套存储/编辑/导出并各自漂。新接任何
  AI 分割能力照此办理——先确认它落库的形状就是人画的那个。

**2026-07-15 迁移修掉的三条**（曾在"已知坏"里，留档防止有人按旧描述找 bug）：
- ~~并发上传同一文件插两行~~ → `UNIQUE(dataset_id, sha256) WHERE qc_status='passed'`
  + `ON CONFLICT DO NOTHING`（M6）；真 Postgres 并发实测 10 发恰 1 行。
- ~~全库零外键~~ → 数据脊柱全加 FK `ON DELETE CASCADE`（M7）；users 方向刻意不加
  （owner_id/uploader_id 的「0 = 系统」写入约定）。
- ~~删数据集不级联~~ → 逐资产清 blob + FK 级联关系行与载荷行；真 app 实测 0 孤儿。

**已知坏 / 待清理**：
- `MM_LITELLM_CONFIG_PATH` 是**死功能**：LiteLLM 跑在 103 容器里读自己的 config，
  在网页里编辑本地 `litellm-config.yaml` **改了不生效**。要么删，要么改走 103 的 Admin API。
- `docker-compose.yml` 没迁过来——它 `build: ./sam-server ./asr-server`，而这俩目录不存在
  （sidecar 在 `deploy-to-103/`）。要用得重写。
- e2e 的共享可变状态仍然脆弱：一次跑崩会留残渣。播种是幂等的，但状态（提交/驳回）不是。
  （2026-07-23：**并行导致的随机假红已消除**——`workers: 1` 钉死，连跑两次 23/0，
  见《测试纪律》。但"跑崩留残渣"这条**没修**，根因仍在：用例共享同一批播种数据。）
- 视频侧 WebVTT 导出未做（B3 里唯一未做项，标着"可选"）。
- 前端 lint 有 150 个 warning（历史遗留，早已降级为 warn，0 errors）。

**验证数据**：`E:\dataset`（不在仓库里，也不该在）。

---

## 约定

- **署名只有仓库主人一个人。** 提交里**不准出现** `Co-Authored-By: Claude`、
  `Generated with Claude Code` 或任何 agent 的署名/尾注；PR 描述同理。
  agent 是工具，不是合作者——工具不署名。
- **不准擅自 git 提交或推送。** 只有用户明确要求时才 commit / push。
  （2026-07-14 我未经要求就提交了一次，是错的。）
- 提交信息用中文，说清**为什么**这么改，不只是改了什么。踩过的坑写进注释和提交信息——
  这个项目的价值有一半在"别再踩第二次"。
- 不确定就先核实再说。这个项目已经因为"想当然"付出过代价。

### 仓库是公开的：私有干 + 策展快照发布（别把 WIP 历史推上去）

GitHub `origin`（`Data_Governance_Platform`）是**公开仓库**。发布走 **squash 快照**，
**绝不**把工作分支直接 push：

- **私有全历史主干 = `trunk`**（原 `migrate/postgres`，2026-07-29 改名——名字带
  "migrate"会让人误以为是迁移遗留分支，其实它是从旧 Labelling 仓库重新 `init`
  的**再生根**，日常所有提交都在这）。它**从不 push**：几十上百条 WIP 历史 +
  任何中间敏感态都留在本地。
- **公开 `main` = 单条 squash 快照**（现在就 1 个 commit）。发布 = 用
  `git commit-tree "$(git rev-parse trunk^{tree})"` 把当前 tree 策展成**一条无父
  快照 commit**（想先审就临时挂个分支看、审完即删，不留常驻 staging 分支），
  `git push origin <sha>:main --force`，完事 `git branch -f main origin/main` 把本地
  `main` 对齐。于是公开仓库历史里不会夹带密钥 / PHI / 真实数据路径 / 中间态。
- **本地常驻两条分支**：`trunk`（私有全历史工作干，从不 push）+ `main`（镜像公开
  快照 `origin/main`，只在发布后对齐）。别再养 `public-main` / `public-snapshot` 之类
  的常驻 staging 分支——快照是 `commit-tree` 现搭现用、发完即弃的（2026-07-30 已把
  这些老分支清掉）。
- ⚠️ **别把 `trunk` merge/push 到公开 `main`**：两条历史**不同根**，硬并会把两段
  无关历史缝在一起，且会把整条 Phase C 医学线一次性公开。**公开什么、何时公开是
  显式决策，不是 git 的默认动作**（记忆 [[data-governance-postgres-migration]]：未获准前不 push）。
- **发布前安全清单**（每次策展快照必过一遍）：
  - 无密钥 / token（曾专门做过一次"清理密钥进公开仓库前的准备"，别让它回潮）；
  - 无真实病例 / 数据集**绝对路径**（`E:\dataset`、CMU-1.svs 具体路径等——测试里用
    环境变量兜底、别硬编码真机路径）；
  - `.deid-local/**`（PHI / UID 映射）、`multi-modality/**`（私有 plan 仓库）、
    `exports/**`（本地验证工具）——都已 `.gitignore`，确认没被 `git add -f` 破例带上；
  - 医学线代码本身（`slide_*` / `volume_*` / `deid_*`）是代码、不是 PHI，可公开——
    但**公开与否仍是用户决策**，不因"代码无 PHI"就自动发。

### 前端布局：新建页面必读

`AppShell` 主内容区是 `flex min-w-0 flex-1 flex-col overflow-hidden`——
**滚动条不由外壳提供，每个页面自己负责**。新页面的根节点必须是
`flex min-h-0 flex-1 flex-col overflow-auto`（仓库其它页面都是这个形状）。

- **`min-h-0` 不能省**：flex 子项默认 `min-height:auto`，不归零它会**撑破**父容器
  而不是触发滚动——表现就是页面被静默裁掉，没有滚动条、下半屏够不着（C3.3 踩过）。
- 内容可能变宽的子项（画布、宽表格）加 **`min-w-0`**：grid/flex 子项默认
  `min-width:auto`，会撑破列而不是让内层 `overflow-auto` 接管，表现是横向溢出屏幕。
- 这类 bug **不报错、不进控制台**，只能靠真浏览器 + 小视口发现——大屏上一切正常。
