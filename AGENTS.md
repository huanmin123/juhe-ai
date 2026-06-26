# AGENTS.md

## 项目定位

- 这里是 `juhe-ai`，定位为轻量级 OpenAI 兼容中转与账号管理项目。
- 前端使用 Vue 3 + TypeScript + Ant Design Vue。
- 后端使用 Node.js + TypeScript。
- 产品当前闭环包含 OpenAI 供应商、系统账户、系统团队、统一授权、AI 账户、分组、API Key、代理、使用记录、统计、日志、原始审计、系统设置、后台 worker 和本地 DB service。
- 当前启用 OpenAI 供应商，并支持 OpenAI OAuth 与 OpenAI API Key 两种账户创建方式；其他供应商保留架构扩展空间。
- 分组绑定账户，API Key 绑定分组；中转调度时通过 API Key 找到分组，再从分组内账户选择可用账号。

## 工作原则

- 保持轻量部署和清晰边界，在现有闭环上按实际需要扩展能力。
- 需求变化如果影响模块边界，先更新 `docs/architecture/架构总览.md` 和 `docs/functions/` 下对应功能文档；如果影响阶段范围或跟踪计划，再更新 `docs/plans/`。
- 开发默认不做任何向下兼容；实现只按当前最优解、最新模型和最新字段结构设计，不为旧代码、旧 API、旧数据库、旧数据或历史部署保留兼容分支。
- 数据库字段、表结构、代码模型和接口契约可以直接调整到当前最优形态；如果影响本地数据库，直接改 schema 和当前读写逻辑，不在运行时代码里写旧结构兼容、双读双写、自动迁移或临时补丁。
- 上线或迁移时由用户按当前数据库字段和表结构单独做结构同步与数据迁移；AI 只在明确要求时提供一次性离线 SQL / 脚本，不把这些迁移逻辑并入主代码。
- 账户相关语义保持轻量、清晰、可维护，避免提前引入重型实现。
- 不要擅自引入重型网关逻辑、分布式依赖或多实例假设；当前优先维护 OpenAI 账号接入、统一授权、网关调度、统计审计和运维排障的稳定闭环。
- UI、API、数据结构尽量先对齐，再开始大规模实现。
- 前端面向中文用户，页面文案、提示、空态、悬浮提示、组件内置文案都必须保持中文；Ant Design Vue 必须通过全局 `a-config-provider` 使用中文 locale，不要让默认英文文案暴露到页面。

## 性能底线

- 大文件、日志、审计 payload、导入导出结果和任何可能频繁读取的文件，必须使用 offset / cursor / stream / 分块窗口机制读取；禁止在运行路径中先把完整文件读入内存再 `split`、过滤或分页。
- 需要持续追新增内容的文件必须记录游标，按已确认处理完成的位置继续读取；按行处理时只能在完整行结束后推进 offset，遇到轮转、截断或文件标识变化时显式重置游标。
- 只有小配置文件、极小系统文件、测试脚本、显式有大小上限的请求体 / 响应体捕获和加解密小 buffer 可以例外；例外不能扩散到日志、记录、审计或业务明细的大文件路径。
- 业务统计、额度判断、趋势、TopN、摘要卡片和授权消耗报表必须由 background worker 或离线重建脚本按游标增量写入预聚合表 / 窗口表；API 路由、repository 请求路径和前端页面禁止实时扫描明细表或缓存桶再 `SUM/GROUP BY/reduce` 汇总。
- 如果现有表结构不能支撑新的统计窗口或维度，先新增 staged / window / summary 表并补后台 job；不能为了赶页面在请求链路临时聚合，也不能让前端承担业务统计口径。

## 通用规范入口

- 涉及 `docs/` 目录整体、文档新增、文档重命名或文档引用调整时，先参考 `docs/README.md`；目录说明文档可以命名为 `README.md`，其他 Markdown 文档文件名主体必须使用中文，必要的产品名、技术名词和编号可以保留原文。
- 涉及前端页面、布局、样式、表格、表单、交互、登录页、首页、应用壳、品牌展示或前端信息架构时，先参考 `docs/architecture/frontend/README.md`。
- 具体样式规范见 `docs/architecture/frontend/样式规范.md`；产品、品牌和配置边界见 `docs/architecture/frontend/产品与品牌边界.md`。
- 涉及新功能、字段、接口、存储、脚本或文档同步时，先参考 `docs/architecture/功能开发指导.md`。
- 涉及需求计划、执行进度、关联文档或任务追踪时，先参考 `docs/plans/README.md`。
- 涉及发布包、服务器部署、家庭宽带反代、启动脚本或发布注意事项时，构建先参考 `docs/deploy/构建指南.md`，部署先确认场景：云服务器 / VPS / 独立服务器看 `docs/deploy/scenarios/服务器部署方案.md`，家里电脑 / 家用主机 / 家庭宽带入口看 `docs/deploy/scenarios/家庭宽带反向代理方案.md`，通用发布包启动基线再看 `docs/deploy/部署指南.md`，并同时确认目标平台子目录：Linux 看 `docs/deploy/linux/README.md`，Windows 看 `docs/deploy/windows/README.md`，macOS 看 `docs/deploy/macos/README.md`。
- 涉及公网 HTTPS、证书、反向代理 TLS、免费证书自动续期或 Caddy / Nginx / Certbot 时，先参考 `docs/deploy/https/README.md`；新部署默认优先使用 Caddy 自动 HTTPS，已有 Nginx 运维体系再使用 Nginx + Certbot。
- 涉及状态检测、健康检查、自动重启、watchdog、launchd / systemd / Docker / Windows Service 自动恢复或防止重启风暴时，先参考 `docs/deploy/watchdog/README.md`；外部 watchdog 只负责主服务整体不可用时恢复，DB service / worker 子进程由主进程 supervisor 看护。
- 涉及上游网络访问、服务器安全上网、账号代理、Docker 容器访问宿主机代理或 sing-box 部署时，先参考 `docs/deploy/proxy/README.md`；juhe-ai 普通上游模型请求优先使用后台“代理管理”绑定到 AI 账户，`JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理。
- 涉及独立压测报告、性能分析报告、容量结论或验证分析报告时，先参考 `docs/reports/README.md`；Markdown 报告写入 `docs/reports/`，脚本生成的 JSON、log、CPU profile 等原始产物继续写入仓库根目录 `reports/`。
- 涉及开发环境、项目安装、本地启动、测试运行或验证流程时，先参考 `docs/develop/README.md`；安装看 `docs/develop/安装指南.md`，运行看 `docs/develop/运行说明.md`，测试看 `docs/develop/测试与验证说明.md`。
- 涉及大文件、职责拆分、模块重构或重复逻辑收敛时，先参考 `docs/architecture/大文件重构指南.md`；可复用的重构案例再记录到 `docs/refactors/`。
- 涉及 bug、异常、测试失败、数据不一致或运行问题时，先参考 `docs/architecture/问题修复指导.md`；如果问题有复用价值、容易复发或需要历史关联，再记录到 `docs/bug/README.md`。
- 以上规范只写通用原则；当前业务事实以 `docs/architecture/架构总览.md`、`docs/functions/README.md`、`docs/develop/运行说明.md` 和 `docs/develop/测试与验证说明.md` 为准；`docs/plans/第一阶段计划.md` 作为早期阶段归档保留，不再替代当前功能文档。

## 目录约定

- `frontend/`：前端页面、组件、状态和 API 请求层。
- `backend/`：Node API、业务逻辑、数据访问、中间件和后续网关能力。
- `docs/`：阶段计划、参考说明、决策记录。
- `docs/architecture/`：架构设计、前端规范和通用开发 / 修复 / 重构指南。
- `docs/functions/`：功能说明、专题调研和存储说明。
- `docs/develop/`：开发环境安装、项目运行、测试和验证说明。
- `docs/deploy/`：构建、发布包、部署场景、服务器部署、家庭宽带反代、三端部署差异、网络代理、HTTPS 证书、启动、备份恢复和部署排障说明。
- `docs/reports/`：人工整理后的压测、性能分析、容量结论和验证分析报告。
- `reports/`：脚本生成的 JSON、log、CPU profile 和临时采样结果等原始产物。
- `docs/refactors/`：重构设计、拆分过程、验证结果和复盘案例。
- `docs/plans/`：需求计划、执行记录、关联文档和索引。
- `docs/bug/`：bug 编号、修复记录、复发记录和历史关联。

## 模块边界

- `providers`：供应商定义，描述不同供应商支持哪些账户创建方式。
- `accounts`：上游账号管理，保存账号状态、凭据引用、并发、代理、透传和错误策略。
- `groups`：账号分组，承接账号集合，并作为 API Key 的授权边界。
- `api-keys`：对外访问密钥，绑定分组并控制访问状态。
- `proxies`：代理配置，供账号绑定使用。
- `usage-records`：请求、用量、耗时、错误和账号命中记录。
- `settings`：系统级配置、默认策略和安全配置。

## 变更习惯

- 改代码前先看计划文档是否需要补充。
- 如果新增字段或流程，先写清楚默认值和用途。
- 后续如果涉及真实转发、代理或并发控制，再补对应验证步骤。





