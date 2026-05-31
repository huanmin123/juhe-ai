# BUG-0011 非 LTS 内置 SQLite 能力不完整

## 基本信息

- 编号：BUG-0011
- 状态：已修复
- 严重程度：P2
- 发现时间：2026-05-17
- 发现方式：用户反馈
- 模块：后端 / 脚本 / 文档 / 部署
- 关联计划：无
- 关联 bug：BUG-0008
- 责任人：Codex

## 问题概述

- 现象：其他电脑运行 `pnpm -r --parallel dev` 时，前端正常启动，后端在记录域建表阶段报 SQLite 能力缺失错误。
- 期望：后端启动前明确拒绝不符合要求的 Node.js 运行时，并提示使用官方 LTS 与完整 SQLite 能力。
- 实际：旧预检只验证 `node:sqlite` 能否导入，Node.js v23.11.0 可以通过导入检查，但内置 SQLite 编译能力不满足当时 schema 要求。
- 影响范围：使用非 LTS Node.js 或自带 SQLite 编译能力不完整的环境。

## 复现步骤

1. 在 Node.js v23.11.0 环境运行 `pnpm -r --parallel dev`。
2. 后端执行 `tsx src/scripts/preflight/check-node-sqlite.ts && tsx watch src/server.ts`。
3. 预检通过后，记录域 schema 初始化阶段报 SQLite 能力缺失错误。

## 环境信息

- 分支 / 版本：2026-05-17 本地开发版本。
- 数据状态：与业务数据无关，记录域 schema 初始化即可复现。
- 系统 / Node 版本：用户反馈环境为 Node.js v23.11.0；本机验证环境为 Node.js v22.19.0 LTS。
- 是否稳定复现：在 SQLite 编译能力不完整的运行时稳定复现。

## 根因分析

- 表象：SQLite 建表时报能力缺失错误。
- 真实根因：项目当时依赖 Node 内置 SQLite 的额外编译能力，但旧运行时预检只检查 `node:sqlite` 导入能力，没有实际验证 schema 所需能力；文档和 `engines` 还允许了非 LTS 的 23.x。
- 为什么会发生：`node:sqlite` 是 Node.js 内置运行时能力，具体 SQLite 编译选项随 Node 发行版而变；“能导入模块”不等于“项目需要的 SQLite 能力完整”。

## 修复方案

- 修改点：后端预检增加 `process.release.lts` 检查，非官方 LTS 直接停止启动。
- 修改点：后端预检使用内存 SQLite 执行真实建表和查询，提前发现 SQLite 能力缺失。
- 修改点：根目录和后端 `engines.node` 收敛到当前支持的 LTS 范围，并新增 `.npmrc` 开启 `engine-strict`。
- 修改点：发布打包脚本和发布包启动脚本复用运行时预检，开发、打包、部署入口统一口径。
- 修改点：README、开发文档、部署文档和 SQLite 存储说明同步为官方 LTS + 完整 SQLite 能力要求。
- 行为影响：非 LTS Node.js 和 SQLite 能力不完整的 Node.js 会更早失败；当前支持 22.x LTS（>=22.13.0）和 24.x LTS（>=24.11.0）。
- 发布异常处理：如运行时预检误判，修正当前预检脚本、`engines`、`.npmrc` 和文档口径；不恢复非 LTS 支持。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 运行时检查 | 当前环境是 LTS，且支持 `node:sqlite` 基础建表与查询 | `pnpm --filter juhe-ai-backend check:runtime` | 通过 | 通过 | 已通过 |
| 类型检查 | 代码类型检查 | `pnpm typecheck` | 通过 | 通过 | 已通过 |
| 构建验证 | 项目构建 | `pnpm build` | 通过 | 通过 | 已通过 |
| 发布路径验证 | 编译后的预检产物可执行 | `node backend/dist/scripts/preflight/check-node-sqlite.js` | 通过 | 通过 | 已通过 |
| 文档检查 | 非历史文档不再推荐 Node 23.x | `rg -n '23\\.4\\.0|node:sqlite ok' README.md docs deploy scripts package.json backend/package.json -S --glob '!docs/bug/**'` | 无结果 | 无结果 | 已通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先运行 `pnpm --filter juhe-ai-backend check:runtime`。
- 重点看 `node -v`、`process.release.lts`、`process.execPath` 和预检中的原始 SQLite 错误。
- 不要只用 `import 'node:sqlite'` 判断运行时是否可用；必须实际验证项目需要的 SQLite 建表与查询能力。

## 完成总结

- 完成时间：2026-05-17
- 结论：启动、打包和部署入口已统一要求官方 Node.js LTS，并在启动前验证完整 SQLite 能力。
- 后续建议：如果后续 schema 继续依赖 SQLite 扩展能力，应把对应能力加入运行时预检，而不是只检查模块导入。
