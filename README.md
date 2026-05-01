# sub2api-lite

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
- Mac 数据库同步：`docs/sub2api-migration.md`
- 参考笔记：`docs/sub2api-reference-notes.md`

## 本地运行

```powershell
pnpm install
pnpm dev
```

默认端口：

- 前端：`http://127.0.0.1:5173`
- 后端：`http://127.0.0.1:3000`

默认 SQLite 文件：`backend/data/sub2api-lite.sqlite3`。

如需指定位置：

```powershell
$env:SQLITE_PATH = "F:\sub2api-lite-data\sub2api-lite.sqlite3"
pnpm --filter sub2api-lite-backend dev
```

## 当前可用功能

- 供应商列表：内置 OpenAI
- 账户管理：创建 / 编辑 / 删除 OpenAI OAuth 与 API Key 账户，OAuth 支持手动授权和 Refresh Token 授权
- 账户管理：列表只显示名称、类型、供应商、并发、状态、用量、优先级、最近使用时间和操作；密钥只在编辑弹窗中查看和修改
- 分组管理：创建 / 编辑 / 删除分组，并绑定账户
- API Key 管理：创建 / 编辑 / 删除 API Key，列表直接显示完整密钥并支持复制
- 代理管理：创建 / 编辑 / 删除 HTTP、HTTPS、SOCKS5 代理
- 使用记录：网关真实请求后写入状态、模型、token、缓存命中 token 和成本
- 系统设置：可编辑默认 OpenAI Base URL、默认并发

## 从现有 Mac sub2api 同步账户

当前同步 OpenAI API Key 与 OAuth 账户；API Key 同步名称、备注、`base_url`、`api_key`，OAuth 同步 token、邮箱和组织信息：

```powershell
pnpm --filter sub2api-lite-backend sync:openai-from-mac
```

完整流程见 `docs/sub2api-migration.md`。

## 测试与验证

```powershell
cd F:\sub2api-lite
pnpm typecheck
pnpm build
pnpm test:smoke
```

本地真实网关验证见 `docs/dev-runbook.md`，当前已验证 `dli.li-300-15`、`/v1/models`、`/v1/responses` 非流式与流式、用量和成本入库。
