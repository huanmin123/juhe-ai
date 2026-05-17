# BUG-0008 Node 23 启动缺少 node:sqlite 提示

## 基本信息

- 编号：BUG-0008
- 状态：已修复
- 严重程度：P2
- 发现时间：2026-05-14
- 发现方式：用户反馈
- 模块：后端 / 脚本 / 文档
- 关联计划：无
- 关联 bug：无
- 责任人：Codex

## 问题概述

- 现象：部分本地环境运行 `pnpm dev` 时，前端正常启动，后端在 `tsx watch src/server.ts` 阶段报 `ERR_UNKNOWN_BUILTIN_MODULE: No such built-in module: node:sqlite`。
- 期望：后端启动前先检测当前 Node.js 是否支持 `node:sqlite`，不支持时输出可读提示和升级建议。
- 实际：业务代码静态导入 `node:sqlite`，不支持的 Node.js 会由 ESM loader 直接抛错，提示缺少上下文。
- 影响范围：Node.js 23.0.0 等不支持免 flag `node:sqlite` 的本地开发环境，以及可能使用相近版本的发布包运行环境。

## 复现步骤

1. 在 Node.js v23.0.0 环境运行 `pnpm dev`。
2. 后端执行 `tsx watch src/server.ts`。
3. ESM loader 在加载 `backend/src/storage/database.ts` 时抛出 `ERR_UNKNOWN_BUILTIN_MODULE`。

## 环境信息

- 分支 / 版本：2026-05-14 本地开发版本。
- 数据状态：与数据库内容无关，启动前模块加载阶段即可复现。
- 系统 / Node 版本：Node.js v23.0.0 稳定复现；后续在 BUG-0011 中已改为只推荐官方 Node.js LTS，并补充 FTS5 能力预检。
- 是否稳定复现：是。

## 根因分析

- 表象：后端启动时报找不到内置模块 `node:sqlite`。
- 真实根因：项目使用 Node 内置 SQLite，旧文档只写 `22.5.0+`，但部分 22.x / 23.x 版本仍不能无条件导入 `node:sqlite`；启动脚本也没有在开发入口前输出项目级说明。
- 为什么会发生：`node:sqlite` 是运行时能力，不是 npm 依赖；不同本地 Node 版本即使主版本更高，也可能不满足当前项目的导入方式。

## 修复方案

- 修改点：新增 `backend/src/scripts/preflight/check-node-sqlite.ts`，通过动态 `import('node:sqlite')` 做启动前预检，并输出当前版本、Node 路径、建议版本和验证命令。
- 修改点：后端 `dev` 和 `start` 脚本先执行运行时预检，再启动服务。
- 修改点：发布包启动脚本、打包脚本、README、开发安装说明和部署文档统一更新 Node.js 版本要求。
- 兼容影响：不改变业务运行逻辑；不支持 `node:sqlite` 的环境会更早失败，并给出明确升级提示。
- 回滚方式：回滚后端 package 脚本和预检脚本，同时恢复旧版本要求；不建议回滚。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 运行时检查 | 当前环境支持 `node:sqlite` | `pnpm --filter juhe-ai-backend check:runtime` | 通过 | 通过 | 已通过 |
| 类型检查 | 代码类型检查 | `pnpm typecheck` | 通过 | 通过 | 已通过 |
| 构建验证 | 后端构建并生成预检产物 | `pnpm --filter juhe-ai-backend build` | 通过 | 通过 | 已通过 |
| 文档检查 | 旧版本口径清理 | `rg -n '22\\.5\\.0|22\\.5\\+' README.md docs deploy scripts package.json backend/package.json` | 无结果 | 无结果 | 已通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先运行 `pnpm --filter juhe-ai-backend check:runtime`。
- 重点看 `node -v` 和 `process.execPath`，确认实际启动的 Node 不是旧版本或非预期版本管理器路径。
- 不要按 npm 依赖缺失排查；`node:sqlite` 是 Node.js 内置模块能力。

## 完成总结

- 完成时间：2026-05-14
- 结论：开发和发布包启动入口已增加前置检查，文档中的 Node.js 版本要求已统一。
- 后续建议：如后续继续依赖 Node 新内置能力，也优先做运行时预检和文档同步。
