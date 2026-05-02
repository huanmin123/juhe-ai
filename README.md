# 聚合 AI

一个更轻量的中转与管理项目，先从整体架构开始设计，再按阶段落地。

## 技术栈

- 前端：Vue 3 + TypeScript + Ant Design Vue
- 后端：Node.js + TypeScript
- 存储：SQLite
- 文档：`docs/`

## 当前阶段

- 完整模块先按“供应商、账户、API 密钥、代理、使用记录、系统设置、分组”设计
- 第一期只实现 OpenAI 供应商
- OpenAI 第一期账户类型：OAuth + API Key
- 分组绑定账户，API Key 再绑定分组

## 入口文档

- 整体架构：`docs/architecture.md`
- 第一阶段计划：`docs/phase-1-plan.md`
- 参考笔记：`docs/sub2api-reference-notes.md`
- 前端样式规范：`docs/前端样式规范指导.md`
- 功能开发指导：`docs/功能开发指导.md`
- 大文件重构指南：`docs/大文件重构指南.md`
- 问题修复指导：`docs/问题修复指导.md`

## 本地运行

```powershell
pnpm install
pnpm dev
```

默认端口：

- 前端：`http://127.0.0.1:5173`
- 后端：`http://127.0.0.1:3000`
- 中转 Base URL：`http://127.0.0.1:3000/v1`

项目按“目录可移植”设计：后端读取 `backend/.env`，前端读取 `frontend/.env`；默认 SQLite 文件放在 `backend/data/juhe-ai.sqlite3`。这些配置文件跟着项目目录走，不依赖系统环境变量，拷贝整个目录到其他电脑或服务器后只要保留 `.env` 和 `backend/data/` 即可继续使用。

```powershell
Copy-Item backend/.env.example backend/.env
Copy-Item frontend/.env.example frontend/.env
# 按需编辑 backend/.env 和 frontend/.env
pnpm --filter juhe-ai-backend dev
```

服务器或局域网部署时，通常只需要改项目内配置文件：

- `backend/.env`：`JUHE_AI_HOST=0.0.0.0`、`JUHE_AI_PORT=3000`、`JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3`
- `frontend/.env`：`VITE_JUHE_AI_API_BASE_URL=/api`、`VITE_JUHE_AI_GATEWAY_BASE_URL=http://你的域名或IP:3000/v1`
- 需要代理时在代理管理页给账户绑定代理；`JUHE_AI_OAUTH_PROXY_URL` 只作为 OAuth token 换取/刷新的可选兜底，不再默认写死本机代理。

对外请求统一兼容 OpenAI 协议：客户端填本服务 `/v1` 作为 Base URL，API Key 填 API 密钥页生成的本地网关密钥；后续提供方也优先适配成 OpenAI 兼容格式。
## 当前可用功能

- 供应商列表：内置 OpenAI，默认 Base URL 归属供应商定义
- 账户管理：创建 / 编辑 / 删除 OpenAI OAuth 与 API Key 账户，OAuth 支持手动授权和 Refresh Token 授权
- 账户管理：列表只显示名称、类型、供应商、并发、状态、用量、优先级、最近使用时间和操作；密钥只在编辑弹窗中查看和修改
- 分组管理：创建 / 编辑 / 删除分组，并绑定账户
- API Key 管理：创建 / 编辑 / 删除 API Key，列表直接显示完整密钥，页面展示中转 Base URL 并支持复制
- 代理管理：创建 / 编辑 / 删除 HTTP、HTTPS、SOCKS5 代理
- 使用记录：网关真实请求后写入状态、模型、token、缓存命中 token 和成本
- 系统设置：可编辑系统名称、系统图标、默认并发、临时不可调用和流熔断参数


## 测试与验证

```powershell
pnpm typecheck
pnpm build
pnpm test:smoke
```

本地真实网关验证见 `docs/dev-runbook.md`，烟测会使用启用的 OpenAI 账户验证 `/v1/models`、`/v1/responses` 非流式与流式、用量和成本入库。

