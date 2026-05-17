# 聚合 AI

聚合 AI（`juhe-ai`）是一个轻量级 OpenAI 兼容中转、账号调度与 API Key 管理系统。

它适合个人、小团队或工作室把多个 OpenAI 上游账号集中管理起来，对外只暴露一个统一的 `/v1` 入口。客户端只需要填写本项目的 Base URL 和本地 API Key，账号选择、分组授权、代理、失败切换、用量统计和日志审计都在后台完成。

![聚合 AI 首页预览](resources/images/home-page.png)

![聚合 AI 管理后台统计预览](resources/images/statistics-page.png)

![聚合 AI 管理后台 AI账户](resources/images/aiuser-page.png)

## 你可以用它做什么

- **一个入口接所有客户端**：Codex、OpenAI SDK、Cherry Studio、NextChat 或其他 OpenAI 兼容客户端都可以接入同一个 `/v1`。
- **统一管理 OpenAI 账号**：支持 OpenAI OAuth 账号和 OpenAI API Key 账号，后台维护启停、优先级、并发、代理、到期时间和错误策略。
- **按分组分配调用权限**：分组绑定上游账号，API Key 绑定分组，不同用户、团队、业务可以隔离使用。
- **自动调度可用账号**：请求进来后自动从分组里选择可用账号；账号限流、异常或冷却时可以切换到同组其他账号。
- **保留完整用量记录**：记录请求、模型、Token、耗时、成本估算、错误摘要和账号命中情况，方便统计、审计和排障。
- **查看用量与性能趋势**：按最近 31 天日期范围查看账户请求、Token 和成本趋势；`AI性能监控` 支持用户侧查看自有账户、管理侧按用户或全部用户查看首 token / 总耗时的平均值和最大值趋势。
- **适合中文后台管理**：前端使用 Vue 3 + Ant Design Vue，后台页面、提示、空态和表单文案面向中文用户。
- **轻量部署**：默认 SQLite 本地存储，不需要 Redis、Kafka、PostgreSQL 或复杂网关集群。

## 为什么选择聚合 AI

- **客户端不用到处散落上游密钥**：只给客户端发本地 API Key，上游 OAuth Token 和 OpenAI API Key 留在后台。
- **多账号更容易维护**：账号状态、代理、优先级、分组、授权和使用记录在一个后台里看清楚。
- **部署成本低**：Node.js + SQLite 即可运行，适合单机、小服务器、家用主机或轻量云主机。
- **排障链路完整**：有使用记录、统计概览、运行日志、原始审计日志和系统监控，出问题时能追到具体请求。
- **架构边界清晰**：当前专注 OpenAI 兼容中转和账号管理，不引入重型分布式依赖；供应商扩展仍以现有边界为前提按需增加。

## 性能与稳定性

聚合 AI 的目标不是做重型分布式网关，而是把单机轻量中转做到稳定、清楚、好维护。

- 网关支持 OpenAI 兼容协议透传和 SSE 流式响应。
- 后台 worker 独立处理统计、日志索引、审计落库、数据清理和账号复测，减少对主 API 进程的影响。
- 本地 DB service 承接网关高频 SQLite 读写，降低同步数据库操作阻塞主进程的风险。
- 账号支持冷却、限流标记、失败切换和连接测试，减少单个账号异常对调用链路的影响。
- 实际吞吐主要取决于机器配置、上游 OpenAI、代理质量、模型响应速度和审计采样配置。

如果你需要明确的压测数据，可以在自己的服务器上按真实模型、真实代理和真实并发做测试；测试方法见 [测试与验证说明](docs/develop/测试与验证说明.md)。

## 最快启动

环境要求：

- Node.js 官方 LTS：`22.x >= 22.13.0` 或 `24.x >= 24.11.0`，且内置 SQLite 支持 FTS5
- pnpm `>= 9.0.0`
- Windows 推荐使用 PowerShell 7

在项目根目录执行：

```powershell
pnpm install
Copy-Item backend/.env.example backend/.env
Copy-Item frontend/.env.example frontend/.env
pnpm dev
```

启动后访问：

- 管理后台：`http://127.0.0.1:5173/__aisys__/`
- 后端系统 API：`http://127.0.0.1:3000/__aisys__/api`
- OpenAI 兼容入口：`http://127.0.0.1:3000/v1`

默认管理员：

```text
用户名：admin
密码：admin
```

首次登录后请立刻修改默认密码。

## 最快接入客户端

1. 登录后台。
2. 在 `AI 账户管理` 或 `我的 AI 账户` 添加 OpenAI OAuth 账号或 OpenAI API Key 账号。
3. 在 `API Key 管理` 或 `我的 API Key` 创建一个本地 API Key，并绑定可用分组。
4. 在客户端里填写：

```text
Base URL: http://127.0.0.1:3000/v1
API Key : 后台 API 密钥页面生成的本地 sk-... 密钥
```

注意：客户端填写的是聚合 AI 生成的本地 API Key，不是上游 OpenAI API Key。

## 最快部署

先在构建机器打包：

```powershell
pnpm install
pnpm package:release:windows
```

会生成：

```text
release/juhe-ai-release.zip
release/juhe-ai-release.tar.gz
```

部署到 Windows：

```powershell
Expand-Archive .\juhe-ai-release.zip -DestinationPath . -Force
Set-Location .\juhe-ai-release
pwsh .\start.ps1
```

部署到 macOS / Linux：

```bash
tar -xzf juhe-ai-release.tar.gz
cd juhe-ai-release
bash ./start.sh
```

启动后访问：

```text
http://服务器IP:3000/__aisys__/
```

如果要公网访问、反向代理、修改端口、设置开机自启或迁移数据，请看 [部署指南](docs/deploy/部署指南.md)。

## 常用命令

```powershell
# 开发启动
pnpm dev

# 类型检查
pnpm typecheck

# 构建
pnpm build

# 打包发布包
pnpm package:release:windows

# 真实网关烟测
pnpm test:smoke
```

## 技术栈

- 前端：Vue 3 + TypeScript + Vite + Ant Design Vue + ECharts
- 后端：Node.js + TypeScript + Express + Zod
- 存储：SQLite（`node:sqlite`）
- 日志：Pino + SQLite 搜索索引
- 包管理：pnpm workspace

## 文档

- [开发安装说明](docs/develop/安装指南.md)
- [开发运行说明](docs/develop/运行说明.md)
- [测试与验证说明](docs/develop/测试与验证说明.md)
- [构建指南](docs/deploy/构建指南.md)
- [部署指南](docs/deploy/部署指南.md)
- [整体架构](docs/architecture/架构总览.md)
- [核心功能设计](docs/functions/核心功能设计.md)
- [SQLite 存储说明](docs/functions/SQLite存储说明.md)
- [接口契约与权限矩阵](docs/functions/接口契约与权限矩阵.md)

## 当前边界

- 当前主要支持 OpenAI 供应商，其他供应商保留扩展空间。
- 当前定位是单机轻量部署，不默认引入 Redis、Kafka 或分布式任务队列。
- 默认使用 SQLite，本地数据库位于 `backend/data/`。
- 管理后台和网关由同一个后端服务承载，前端静态资源在发布包中由后端托管。

## Star 支持

如果这个项目对你有帮助，欢迎点一个 Star：

- GitHub：[https://github.com/huanmin123/juhe-ai](https://github.com/huanmin123/juhe-ai)
- Gitee：[https://gitee.com/huanminabc/juhe-ai](https://gitee.com/huanminabc/juhe-ai)

## QQ 群

![QQ群](resources/images/qq.png)
