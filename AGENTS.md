# AGENTS.md

## 根文档职责

- 本文件只承担项目级导航、核心业务边界和高频事件入口，不复制专题文档正文。
- 具体架构、功能、前后端实现、测试、问题、重构、迁移和报告规则以 `docs/` 下对应权威文档为准。
- 本文件不自动触发生产部署或线上运维流程；只有用户主动提出生产操作并明确提供适用资料后，才读取和执行对应范围内的外部操作规范。

## 项目定位

- 这里是 `juhe-ai`，定位为轻量级 OpenAI 兼容中转与账号管理项目。
- 前端使用 Vue 3 + TypeScript + Ant Design Vue；后端使用 Node.js + TypeScript，并包含后台 worker 和本地 DB service。
- 当前启用 OpenAI 供应商，支持 OpenAI OAuth 与 OpenAI API Key 两种账户创建方式；其他供应商保留架构扩展空间。

## 核心业务边界

- 当前项目事实以 `docs/architecture/架构总览.md` 和 `docs/functions/README.md` 为准，历史计划不替代当前架构和功能文档。
- 路由层级固定为 `API Key -> 路由策略 -> 分组 -> AI 账户 -> 供应商 / 协议能力`。
- API Key 只绑定路由策略；路由策略负责路由模式和分组绑定；分组协调 AI 账户；AI 账户和供应商管理真实上游语义。
- 普通路由只绑定一个分组；混合智能、权重、故障回退和轮询规则由策略路由维护。
- 客户端画像由网关内部自动识别，不作为 API Key、路由策略、分组或普通 AI 账户的用户配置项。
- 跨协议转换属于混合供应商账户能力，不写成 API Key 或路由策略里的显式协议桥接规则。

## 本地管理页面测试

- 需要通过项目管理页面执行 AI 账户、模型、探针或其他本地联调测试时，默认启动隔离的开发实例并使用开发自动登录；不得把截图、识别或人工输入验证码作为默认测试步骤。
- 隔离后端至少设置 `JUHE_AI_DEV_AUTO_LOGIN_USERNAME=admin` 和 `JUHE_AI_AUTH_CAPTCHA_DISABLED=true`，并为业务库、用量库、统计库、日志和运行时文件设置独立临时目录，禁止污染现有开发数据。
- 隔离前端通过 `VITE_JUHE_AI_BACKEND_TARGET` 和 `VITE_JUHE_AI_GATEWAY_BASE_URL` 指向该隔离后端；使用未占用的新端口，不停止或复用用户已经运行的前后端进程。
- 页面打开后应直接验证自动登录是否生效，再进入目标管理页面执行真实操作。只有自动登录功能本身就是被测对象，或自动登录经排查确认不可用时，才允许测试登录流程；仍不得尝试破解验证码。
- 测试凭据只能从用户明确指定的本地文件或当次消息读取，不写入源码、文档、日志或测试产物；输出、审计检查和最终报告必须脱敏。

## 事件导航

| 事件 | 必读入口 |
| --- | --- |
| 文档结构、新增文档、重命名或引用调整 | `docs/README.md` |
| 项目定位、模块边界、数据关系或网关主流程变化 | `docs/architecture/架构总览.md` |
| 新功能、字段、接口、存储、脚本或关键流程 | `docs/architecture/功能开发指导.md` 和 `docs/functions/README.md` |
| 前端页面、布局、样式、交互、文案或品牌 | `docs/architecture/frontend/README.md` |
| 后端接口、存储、网关、脚本、worker 或队列 | `docs/architecture/backend/README.md` |
| 需求计划、执行进度或关联文档 | `docs/plans/README.md` |
| 本地安装、运行、联调、测试或验证 | `docs/develop/README.md` |
| bug、异常、测试失败或数据不一致 | `docs/architecture/问题修复指导.md`，必要时记录到 `docs/bug/README.md` |
| 大文件拆分、职责调整或重复逻辑收敛 | `docs/architecture/大文件重构指南.md`，复盘记录到 `docs/refactors/README.md` |
| Node 后端向 Go 迁移 | `docs/migration/README.md` |
| 压测、性能分析、容量或验证报告 | `docs/reports/README.md` |
