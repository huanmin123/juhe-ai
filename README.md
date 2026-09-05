# juhe-ai

`juhe-ai` 是一个轻量、可扩展的 AI 账号调度与 OpenAI-compatible 网关。客户端只需配置本地 API Key 和一个稳定入口；账号凭据、模型能力、分组、路由、授权、用量与审计统一由后台管理。

> 后端已由 Node.js 全量迁移到 Go（`backend-go/` 三项目：gateway / jobs / maintenance），Node 后端已归档至 `migration-backup/`。项目事实以 [整体架构](docs/architecture/架构总览.md) 为准，迁移终局见 [Node到Go全量迁移终局报告-20260905](docs/reports/Node到Go全量迁移终局报告-20260905.md)。

## 适用场景

- 为 Codex、OpenAI SDK 与兼容客户端提供稳定的本地网关入口。
- 集中管理多个供应商账户、上游凭据、代理、模型和可用性策略。
- 将调用侧的 API Key 与上游账号解耦，按路由策略调度账号池。
- 需要查看用量、成本、性能、操作记录和请求审计的个人或团队。
- 优先从单机轻量部署开始，并可按需采用 PostgreSQL + Redis 性能模式。

## 核心模型

```text
客户端
  └─ 本地 API Key
       └─ 路由策略
            └─ 分组
                 └─ AI 账户
                      └─ 供应商 / 协议能力 / 上游凭据
```

- **API Key**：调用入口，负责鉴权、额度、状态和选择一个路由策略。
- **路由策略**：负责分组绑定与路由方式；支持普通、混合智能、权重、故障回退和轮询。
- **分组**：协调账号池和分组内调度，不承载供应商或协议语义。
- **AI 账户**：保存真实上游账户、凭据、代理、并发、时间计划、模型限制和错误策略。
- **供应商与协议档案**：定义上游协议、模型目录、价格与专项能力；客户端画像仅在网关内部自动识别。

详细边界见 [策略路由设计](docs/functions/策略路由设计.md) 与 [整体架构](docs/architecture/架构总览.md)。

## 功能概览

- OpenAI-compatible 网关：支持服务根路径和 `/v1` 入口。
- 多供应商账户：当前内置 `openai`、`gpt`、`xai`、`anthropic`、`deepseek`、`glm`、`gemini` 与 `hybrid` 账户能力。
- 账户管理：API Key、OAuth、代理、模型映射、账号测试、健康检测与时间计划。
- 调度与保护：分组调度、并发控制、来源级保护、显式故障回退和客户端会话亲和。
- 授权与管理：系统账户、团队、统一授权、API Key 额度与权限边界。
- 可观测性：使用记录、统计、性能、运行日志、操作日志、原始审计和模型检测。
- 存储模式：默认 standalone 使用 SQLite；显式 performance 模式使用 PostgreSQL + Redis。

OpenAI-compatible 指本地入口、认证方式和错误结构的兼容性；不同上游账户的协议与功能能力并不相同。默认情况下，真实上游失败会返回当前失败；仅在用户显式配置并满足重放条件时才会尝试 `retry_next`。

## 快速开始

### 环境要求

- Go `1.26.x`，可从 `PATH` 调用
- Node.js `22+` 与 pnpm `9+`（前端开发服务器与构建工具链）
- Windows 环境推荐 PowerShell 7

### 安装与启动

在项目根目录执行：

```powershell
pnpm install
pnpm dev
```

`pnpm dev` 是 go-only 启动器：自动拉起 Go `juhe-ai-gateway`（管理面、公开面与 `/v1` 网关链）、Go `juhe-ai-jobs` 与前端；开发数据统一落在 gitignore 的 `.local/dev/`，不依赖已归档的 Node 后端。

默认开发地址：

| 服务 | 地址 |
| --- | --- |
| 管理后台 | `http://127.0.0.1:5173/__aisys__/` |
| 系统 API | `http://127.0.0.1:3000/__aisys__/api` |
| 网关入口 | `http://127.0.0.1:3000/v1` |

当前运行流程与环境边界见 [开发运行说明](docs/develop/运行说明.md)；部署配置请从 [部署文档](docs/deploy/README.md) 选择对应场景。

### 接入客户端

1. 进入管理后台，创建或导入 AI 账户，并将账户加入分组。
2. 创建路由策略，按需要选择普通、混合智能、权重、故障回退或轮询模式。
3. 创建本地 API Key，并绑定该路由策略。
4. 在客户端填入网关地址与本地 API Key：

```text
Base URL: http://127.0.0.1:3000/v1
API Key: 后台创建的本地 API Key
```

客户端使用的是本地 API Key，不是上游供应商的 API Key。

## 常用命令

```powershell
# 启动开发环境（Go gateway + Go jobs + 前端）
pnpm dev

# 类型检查与静态检查
pnpm typecheck
pnpm lint

# 构建全部工作区
pnpm build

# 构建 Windows 发布包（go-only：前端 dist + 三 Go 二进制 + 部署脚本）
pnpm package:release:windows
```

完整测试矩阵、真实账户验证和性能验证见 [开发测试与验证说明](docs/develop/测试与验证说明.md)。

## 架构与运行形态

- `frontend/`：Vue 3、TypeScript 与 Ant Design Vue 管理后台。
- `backend-go/`：Go 后端三项目（`go.work`）；`projects/gateway` 是唯一 HTTP 主入口（管理 API、公开面、`/v1` 网关链、chat），`projects/jobs` 承载后台任务与探针/统计/retention 任务族，`projects/maintenance` 提供 schema/seed 一次性 CLI。
- 默认单机运行由 `juhe-ai-gateway` 与 `juhe-ai-jobs` 两个常驻进程组成；原 Node.js 后端（含 worker 与 DB service）已归档至 `migration-backup/node/final-archive/`。

存储、进程职责见 [架构总览](docs/architecture/架构总览.md) 与 [Go 三项目架构基线](docs/migration/Go三项目架构基线.md)。

## 部署与文档

- [开发文档](docs/develop/README.md)：安装、运行、测试与验证。
- [部署文档](docs/deploy/README.md)：按部署场景选择构建、Docker、Windows、Linux、macOS、代理与 HTTPS 指南。
- [功能文档](docs/functions/README.md)：账户、路由、模型、授权、存储、统计和网关的具体契约。
- [前端架构](docs/architecture/frontend/README.md) 与 [后端架构](docs/architecture/backend/README.md)：实现边界和维护约束。
- [迁移文档](docs/migration/README.md)：已完成的 Node→Go 终局迁移状态、规则与历史记录索引。

## 项目边界

- 默认目标是可管理的单机 AI 网关与账号调度；分布式部署属于显式配置能力，不是默认运行形态。
- 本项目不将 API Key 作为跨协议转换或客户端画像的配置入口；这些能力分别归属混合供应商账户和网关运行时。
- 数据、配置或 schema 与当前契约不一致时，应按当前 schema 离线修复或重建，不在运行链路中保留未知兼容分支。
- 迁移中的功能是否已完成生产接管，以迁移与功能文档中的可核验验收记录为准。

## 其他
问题或建议：QQ群 `1105515344`
