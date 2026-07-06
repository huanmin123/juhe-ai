# PLAN-0081 Node 转 Go 渐进减法迁移

## 基本信息

- 编号：PLAN-0081
- 状态：进行中
- 创建时间：2026-07-06
- 更新时间：2026-07-06
- 需求来源：用户对话
- 执行者：AI / 维护者
- 关联模块：后端 / 存储 / 网关 / 后台 worker / 公开接口 / 管理接口 / 部署 / 文档 / 验证

## 需求目标

- 背景：当前 Node 后端为了处理并发、阻塞、事件循环、SQLite 写锁、IPC 和后台任务隔离，已经积累了大量复杂机制。用户已明确决定彻底抛弃 Node 后端，转向 Go，并彻底去掉 SQLite，不再维护 standalone / performance 两套模式。
- 目标：通过渐进式、减法式迁移，把后端逐步迁移到 Go + PostgreSQL + Redis；迁移一个模块就删除一个 Node 旧模块和对应 SQLite / 双模式旧路径，最终只保留前端构建所需的 Node 工具链。
- 交付物：Go 迁移目录、迁移总计划、Go 架构基线、模块顺序、测试验收策略、开发构建部署调整，以及后续代码迁移计划入口。

## 范围边界

### 本次包含

- [x] 新增 `docs/migration/` 迁移目录。
- [x] 规划渐进减法迁移原则和阶段。
- [x] 规划 Go 后端目标架构、技术依赖、并发和线程安全边界。
- [x] 规划 PostgreSQL + Redis 单模式存储目标和 SQLite 移除范围。
- [x] 规划模块迁移顺序，明确公开接口、后台接口优先，真实网关最后。
- [x] 规划完整测试与验收策略。
- [x] 规划开发、安装、构建、部署、Docker、服务化和回滚调整。
- [x] 更新文档入口和计划索引。

### 本次不包含

- 不创建 Go 后端代码：本次先落文档基线。
- 不删除 Node 后端代码：只有某个模块由 Go 接管并通过验收后，才按减法规则删除。
- 不立即修改数据库 schema 或生成离线数据导入脚本：Go 迁移期间如需要 schema 调整或旧 SQLite 数据导入 PostgreSQL，另建具体计划。
- 不迁移真实网关：网关必须在公开接口、后台接口、存储和 worker 迁移成熟后执行。

## 关联文档

- 迁移目录：`docs/migration/README.md`
- 迁移总览：`docs/migration/迁移规划总览.md`
- Go 架构基线：`docs/migration/Go后端架构基线.md`
- 存储目标：`docs/migration/存储目标与SQLite移除.md`
- 模块清单：`docs/migration/模块迁移顺序与减法清单.md`
- 测试策略：`docs/migration/测试与验收策略.md`
- 开发构建部署：`docs/migration/开发构建部署调整.md`
- 架构总览：`docs/architecture/架构总览.md`
- 后端架构入口：`docs/architecture/backend/README.md`
- 开发入口：`docs/develop/README.md`
- 部署入口：`docs/deploy/README.md`

## 方案概述

- 方案原则：渐进式、减法迁移、单 owner、测试先行、Go 原生优先、网关最后。
- 数据变化：本次不改 schema；迁移目标为 PostgreSQL + Redis 单模式，后续 schema 只保留当前最优结构，不在运行路径保留旧 SQLite 结构兼容、双写或双读。
- 接口变化：本次不改接口；后续迁移需保证当前公开契约和管理契约等价，或先更新功能文档和测试预期。
- 前端变化：本次不改前端；后续前端继续以 Vue 3 + TypeScript + Ant Design Vue 维护。
- 后端变化：目标后端从 Node.js + TypeScript 迁移到 Go，逐步删除 Node 后端模块、DB service、SQLite adapter、SQLite 配置和双模式分支。
- 数据处理策略：既有 SQLite 数据如需保留，只通过一次性离线导出、清洗、导入 PostgreSQL 处理，不写入 Go 或 Node 运行路径。

## 执行拆解

- [x] 更新相关长期文档入口。
- [x] 新增迁移目录主文档和示例文档。
- [x] 新增迁移总览、Go 架构基线、模块顺序、测试策略和部署调整。
- [ ] 建立 Go 后端最小工程骨架。
- [ ] 固定公开接口契约并迁移公开接口。
- [ ] 迁移后台管理只读接口。
- [ ] 迁移后台管理写接口和权限链路。
- [ ] 迁移 worker、存储 adapter 和维护脚本。
- [ ] 迁移真实中转网关。
- [ ] 删除 Node 后端发布链路并更新最终部署文档。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 文档结构 | 迁移目录入口 | 检查 `docs/migration/README.md` 和示例文档 | 新目录有 README 和示例文档 | 已通过 | 本次新增 |
| 文档链接 | 文档入口引用 | `rg "migration|迁移" docs` + 相对链接检查 | 入口文档能找到迁移目录和 PLAN-0081，新增链接指向真实文件 | 已通过 | 已执行 |
| 代码验证 | 当前代码类型检查 | `pnpm typecheck` | 当前前后端类型检查通过 | 不适用 | 本次只改文档 |
| Go 骨架 | Go 单元测试 | `go test ./...` | Go 代码测试通过 | 未执行 | 待 Go 代码落地 |
| 公开接口 | 公开接口契约 | Go API 契约测试 + 前端 smoke | 字段、权限和错误语义不缺失 | 未执行 | 后续模块迁移执行 |
| 后台接口 | 管理接口契约 | Go API 契约测试 + 权限回归 | CRUD、分页、权限和中文文案正确 | 未执行 | 后续模块迁移执行 |
| Worker | 后台任务 | Go worker 测试 + 游标验证 | 队列、游标、重试和保留期正确 | 未执行 | 后续模块迁移执行 |
| 存储 | PostgreSQL + Redis 单模式 | Go store 测试 + PG/Redis smoke + SQLite 引用清理检查 | 新库初始化、Redis 队列和运行态可用，Go 运行路径无 SQLite | 未执行 | 后续存储迁移执行 |
| 网关 | 真实中转网关 | mock AI / real e2e / 性能压测 | SSE、调度、审计、usage 和失败恢复正确 | 未执行 | 最后执行 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-07-06 | 进行中 | AI | 创建 Go 渐进减法迁移规划，先落文档基线，不改业务代码。 |
| 2026-07-06 | 进行中 | AI | 根据最新决策补充 PostgreSQL + Redis 单模式目标，新增 SQLite 移除范围和旧数据离线处理边界。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-07-06 | 后端目标运行时转 Go | 当前 Node 后端围绕事件循环和阻塞规避积累了过多复杂度 | 后续后端模块按 Go 架构迁移，Node 后端逐步删除 |
| 2026-07-06 | 采用渐进减法迁移 | 避免一次性重写风险，同时避免迁移后旧实现残留 | 每个模块 Go 接管后必须删除 Node 旧实现 |
| 2026-07-06 | 公开接口和后台接口优先，真实网关最后 | 公开/后台接口风险相对低，网关是最核心链路 | 网关迁移必须等待测试、存储和 worker 基线成熟 |
| 2026-07-06 | Go 架构优先使用标准库和轻量依赖 | 避免从 Node 复杂度迁移到框架复杂度 | 默认 `net/http`、`chi`、`slog`、`pgx`、`go-redis`；不引入 SQLite driver |
| 2026-07-06 | Go 后端只保留 PostgreSQL + Redis 单模式 | 继续保留 SQLite 会让 schema、repository、测试和部署长期双倍维护 | 不再引入 Go SQLite driver；删除 standalone / performance 模式分支；旧 SQLite 数据只走离线导入 PostgreSQL |

## 验收标准

- [x] `docs/migration/` 有 README 和示例文档。
- [x] 迁移原则、阶段、减法规则和网关最后策略已经写入文档。
- [x] Go 技术基线、目录结构、并发、线程安全、内存和大数据边界已经写入文档。
- [x] PostgreSQL + Redis 单模式和 SQLite 移除范围已经写入文档。
- [x] 测试与验收策略覆盖公开接口、后台接口、worker、存储、网关、性能和删除验证。
- [x] 开发、构建、部署、Docker、服务化、配置和回滚调整已有规划。
- [ ] 后续每个代码迁移模块都有独立验证记录和删除证据。

## 验证记录

- 类型检查：不适用，本次只改文档。
- 构建：不适用，本次只改文档。
- 文档检查：已执行，命令：`rg "PLAN-0081|Go 渐进减法迁移|Node 转 Go|migration/README" docs`；补充执行 `rg "Go 1\\.26|SQLite driver 评估|modernc\\.org/sqlite|mattn/go-sqlite3|SQLite standalone 模式默认|PostgreSQL / Redis 仅|让 SQLite / PostgreSQL / Redis|standalone SQLite、高性能" docs/migration docs/plans/计划-0081-Node转Go渐进减法迁移.md`，未发现 Go 目标继续保留 SQLite / 双模式的旧表述。
- 手动验证：已执行，新增文档命名、入口索引和相对链接检查通过；相对链接检查结果为 `All checked markdown links resolve.`。
- 未验证项：Go 代码测试、接口契约测试、网关测试均待后续代码迁移阶段执行。

## 风险与注意事项

- 风险 1：Go 迁移不能误以为“goroutine 等于无限并发”，PostgreSQL 连接池、Redis 队列、上游账号并发和队列容量仍需硬边界。
- 风险 2：双运行时过渡容易残留旧实现，必须坚持单 owner 和删除证据。
- 风险 3：SQLite 数据若需要保留，必须单独离线导入 PostgreSQL，不能把双读双写或启动迁移写进运行路径。
- 风险 4：真实网关迁移风险最高，必须在存储、worker、协议测试、SSE 和性能验证成熟后执行。
- 发布异常处理：每个模块生产切换前保留上一版发布包、配置和数据备份；schema 变化不依赖运行时自动回滚。

## 完成总结

- 完成时间：待后续迁移全部完成后填写。
- 实际完成内容：当前完成文档规划，代码迁移尚未开始。
- 主要改动位置：`docs/migration/`、`docs/plans/计划-0081-Node转Go渐进减法迁移.md`。
- 验证结果：待补充。
- 后续建议：下一步先建立 Go 最小工程骨架和公开接口契约测试。
