# AGENT.md

**接手指令见 [`CLAUDE.md`](CLAUDE.md)**（Claude Code 会自动加载它；其它 agent 请手动读）。

那份文档里有：跑起来的命令、**会静默出错的四个地方**（本项目最贵的教训）、数据模型要点、
103 上的模型服务、测试纪律（净环境自检 / 变异测试 / 幂等播种）、我犯过的错、以及待用户拍板的事项。

## 深入阅读

| 文档 | 内容 |
|---|---|
| `multi-modality/plan_v2/执行方案-00-共用基座.md` | 资产 / 派生物 / 编辑锁 / 标签本体 / RBAC |
| `multi-modality/plan_v2/执行方案-01-音频标注.md` | 波形 · ASR · 说话人 · 情绪 · 导出 |
| `multi-modality/plan_v2/执行方案-02-视频标注.md` | **最完整的一份**：帧索引 · 插值规范 · track 模型 · AI 预标注 · 审核闭环 · 导出 |
| `multi-modality/plan_v2/执行方案-04-医学影像.md` | Phase C 草案 —— **文首是待拍板清单** |
| `multi-modality/plan_v2/执行方案-05-具身智能.md` | Phase D 草案 —— **文首是待拍板清单** |
| `multi-modality/plan_v2/执行方案-06-关系库迁移-Postgres.md` | **已拍板未开工**：关系库 → Postgres（新仓库）。描述的是**目标状态**，CLAUDE.md 描述的是现状 |
| `multi-modality/plan_v2/部署运维-103-GPU模型服务器接入全过程.md` | 模型服务从零部署到全链路连通 |
| `docs/2026-07-09-release/` | 项目介绍 / 系统架构 / 使用手册 / 部署运维指南 / AI 服务部署 |
| `docs/HANDOFF.md` | 上一轮交接记录 |

> ⚠️ `CHANGELOG.md` 和 `multi-modality/plan_v1/` 里有大量 **Vue 时代**的文件路径记录。
> 那两套 Vue 前端已在本次迁移中丢弃，**唯一前端是 `frontend/`**。
> 看到 `*.vue` 或 `frontend/xxx` 一律当历史记录读，不是现状。
