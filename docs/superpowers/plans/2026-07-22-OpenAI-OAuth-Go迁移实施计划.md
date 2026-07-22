# OpenAI OAuth Go 迁移实施计划

> 设计依据：[OpenAI OAuth Go 迁移设计](../../migration/OpenAI-OAuth-Go迁移设计.md)。本计划执行期间 Node 仍是生产 owner；除非用户明确批准生产切流或减法阶段，不修改生产 owner、不删除 Node。

## 1. 交付目标

完成 OpenAI OAuth 管理接口、session / PKCE、token client、账户持久化和 Access Token 保活 worker 的 Go-native 实现，为后续 management / worker / gateway 分别切流提供证据。第一轮优先完成迁移覆盖面，仅执行必要定向验证；重型真实依赖、并发和全量回归集中在统一验收轮。

## 2. 基线与前置

- 实施分支每批开始前 fetch / rebase 最新 `origin/master`，记录基线 SHA；不要在有其他代理改动的主工作区直接开发。
- 先合入 `f4066292d` 的 Node OAuth migration golden。golden 只冻结 Node 事实，`knownNodeDefects` 不得转成 Go 兼容要求。
- 每个并行 agent 使用独立 worktree 和互斥文件 owner；共享 `router.go`、`server.go`、owner manifest、migration catalog 和文档索引由整合 agent 串行处理。
- 每个实现切片遵守 TDD：先失败契约 / 行为测试，再实现，再跑目标包测试和 `git diff --check`。
- 本计划不要求第一轮跑真实 OpenAI 上游、全仓 race 或浏览器全流程；这些不得因此标记为已验收。

## 3. 可并行工作包

### A. Contract 与领域错误

文件 owner：`backend-go/internal/modules/managementopenaioauth/*_test.go`、`backend-go/internal/modules/managementopenaioauth/errors.go`、OAuth Go testdata。

- [ ] 从 Node golden 生成 Go 可读的 contract fixture，不复制 Node 缺陷为断言。
- [ ] 定义六个 operation、稳定 error code、typed error 和安全中文 message。
- [ ] 固定凭据 token 字段 / 用户配置字段集合及合并规则。
- [ ] 固定 callback parser、`expires_in`、JWT metadata 的有界解析。

必要验证：目标 package 单元测试；fixture 与 Node golden version / authority 对照。

### B. Session / PKCE 状态机

文件 owner：`backend-go/internal/modules/managementopenaioauth/session/*`、可选独立 Redis Lua 文件。

- [ ] 先写 pending / processing / exchanged / consumed 状态机测试。
- [ ] 实现 `crypto/rand` state、verifier、challenge 和 session ID。
- [ ] 实现 owner / state constant-time 校验、processing lease、重试 release、exchanged resume 和 consumed tombstone。
- [ ] 实现全局 1024 / owner 8 / TTL 30m 容量语义。
- [ ] session payload 使用稳定 secret 加密；错误和日志不含 state/verifier/token。
- [ ] Redis unavailable 时 fail closed；内存 store 仅做相同语义的测试 / 单进程开发实现。

必要验证：单元测试 + Redis contract test；真实 Redis 并发 / lease 留到统一验收。

### C. Token HTTP client

文件 owner：`backend-go/internal/modules/managementopenaioauth/token/*`。

- [ ] 用假 TLS / HTTP server 先覆盖 form 与 JSON body、headers、timeout、取消、redirect 拒绝和 `256KiB+1`。
- [ ] 实现专用 Transport、proxy resolver、bounded reader 和安全错误分类。
- [ ] 覆盖缺 access token、非法 expires、invalid JSON、upstream 4xx/5xx、invalid_grant。
- [ ] JWT payload 只提取有界非权威 metadata。

必要验证：token package tests 和目标 vet；不请求真实 OpenAI。

### D. Store port 与 PostgreSQL CAS

文件 owner：OAuth 新 store port、`backend-go/internal/store/postgres/managementopenaioauth*`、对应 query / test。

- [ ] 定义创建所需 profile / group / account target 读取和 credential CAS port。
- [ ] 复用 `secretcrypto.JSONCodec` 与现有账户 create / update `config_revision`，不新建另一套密文格式。
- [ ] 外部 HTTP 不持有 transaction；写入用 expected revision。
- [ ] 覆盖创建唯一冲突、账户越权 / 授权实例、CAS 0 行、refresh token 轮换和只清 OAuth refresh failure。
- [ ] 若现有 schema 足够则不新增 migration；确需 session / worker durable 字段时先独立设计，不能把 Redis 临时状态塞进业务表。

必要验证：sql/query 结构测试 + store mock；真实 PostgreSQL CAS 留到整合轮。

### E. 管理 service

文件 owner：`backend-go/internal/modules/managementopenaioauth/service*.go`。

- [ ] 组合 A-D，实现 auth URL、code create、refresh-token create、manual refresh、两种 reauthorize。
- [ ] 创建配置复用现有账户 normalization；强制 gpt/oauth/pending_test/unschedulable。
- [ ] code path 按 session 状态续跑，DB 瞬时失败不重复交换 code。
- [ ] code path 在 `exchanged` 状态预分配 account ID；覆盖“账户已提交、session complete 失败”后只回读原账户。
- [ ] Refresh Token 创建使用 mutation guard 保存的预分配 ID / 成功结果，防重复账户。
- [ ] reauthorize 保留非 token 凭据字段；CAS 冲突按设计最多恢复一次。
- [ ] operation log 只写安全字段；提交后 health dispatch 失败不回滚。

必要验证：service table tests，覆盖每个 typed error 和 side-effect 次数。

### F. HTTP、router 与 app 装配

文件 owner：OAuth 新 HTTP 文件；`router.go`、`server.go` 由整合 agent 单写。

- [ ] 两组前缀注册相同六条相对路由。
- [ ] 对齐 self/admin scope、strict body、`256KiB`、no-store、read/write limiter 和 auth 顺序。
- [ ] 只返回成功 data envelope 或稳定 error envelope；字段白名单测试禁止 secrets。
- [ ] `JUHE_AI_MANAGEMENT_API_ENABLED=false` 时不注册；生产默认 owner 不改变。
- [ ] 前端 request-capture 证明路径和 body 不漂移，并开始使用顶层 `code`。

必要验证：handler/router/app 定向测试；真实 listener 后置。

### G. Access Token refresh job

文件 owner：`backend-go/internal/jobs/openaioauthrefresh/*`、独立 worker handler；worker mux / CLI 由整合 agent 串行接线。

- [ ] 定义 candidate projection、设置范围、job payload / result 与指标。
- [ ] 实现 bounded candidate read、per-account Redis lease、有界 goroutine fan-out 和 config revision CAS。
- [ ] 实现 retry backoff、连续 3 次 OAuth refresh failure、恢复、cache invalidation。
- [ ] Node worker owner 时不注册 Go periodic task；添加单 owner guard test。
- [ ] 不实现主动 usage refresh / 质量探测。

必要验证：job/worker tests，目标 race 可在该 package 执行；真实多实例和 shutdown 后置。

### H. Golden、文档与删除清单

文件 owner：OAuth golden、本文、迁移设计及后续 OAuth 专项记录；中心 README 由整合 agent 更新。

- [ ] Node 发生 OAuth 业务调整时先更新 golden，再判断 Go 是同步还是以缺陷记录拒绝照搬。
- [ ] 维护字段、错误、owner、配置、Redis key、operation log 和验证矩阵。
- [ ] 提前生成 Node 删除清单，但在用户宣布减法阶段前不执行。

## 4. 整合顺序

1. 合入 A，统一类型和错误命名。
2. 合入 B、C、D；三者文件隔离，可并行完成。
3. 合入 E，解决账户 create/update 复用边界。
4. 合入 F；由一个整合 agent 统一修改 router/app，避免并行冲突。
5. 合入 G；worker mux、CLI、config 和 settings catalog 串行整合。
6. fetch 最新 master，审计 Node OAuth / account / provider / worker / frontend 漂移；只同步确认影响本迁移 owner 的变化。
7. 运行第一轮必要验证并提交迁移实现，保持生产 owner 为 Node。

每次 cherry-pick 后执行 `git diff --check` 和受影响 package 编译。发生同一共享文件冲突时停止该批并由整合 agent 按当前 master 语义手工合并，不选择整文件 ours/theirs。

## 5. 第一轮必要验证

| 范围 | 证明 | 状态 |
| --- | --- | --- |
| Node golden | Node 六路由、PKCE/session、token、错误和持久化事实未漂移 | 待执行 |
| Go contract | Go 目标契约与两个缺陷修复均有测试 | 待执行 |
| session | 状态迁移、owner/state、lease、retry、resume、capacity | 待执行 |
| token client | body/header/timeout/bound/redirect/proxy/error | 待执行 |
| service | 六 operation 成功与主要错误、凭据合并、提交/complete 故障窗口、side effect | 待执行 |
| store | create / CAS / conflict / failure-state scope | 待执行 |
| HTTP | admin/self、middleware、strict JSON、status/code、无 secrets | 待执行 |
| worker | candidate / singleflight / backoff / threshold / owner guard | 待执行 |
| 静态 | 目标 `go test`、目标 `go vet`、`git diff --check` | 待执行 |

## 6. 统一验收轮

- [ ] fresh Goose PostgreSQL + Redis + Asynq integration，且不是 `SKIP`。
- [ ] Node 创建 OAuth 账户后 Go refresh / reauthorize；Go 创建后 Node list / gateway read。
- [ ] 32+ 并发领取同一 session 只有一个有效 lease；网络失败可安全重试；成功 code 只交换一次。
- [ ] 两个 worker 实例竞争同一账户只写一次，不发生旧 refresh token 覆盖。
- [ ] 客户端中断、worker shutdown、Redis 故障、DB CAS、代理超时、OpenAI 5xx / invalid_grant 矩阵。
- [ ] 前端连接真实 Go listener 的 admin/self create / reauthorize / refresh 流程。
- [ ] 日志、operation log、HTTP、trace、metrics、Asynq payload 和 Redis session 检查无明文 secrets。
- [ ] 经批准的小流量真实 OpenAI smoke；没有真实凭据时保持“未验证”，不能用 mock 替代。
- [ ] 精确 management path、worker scheduler、gateway 各自单 owner和回滚演练。
- [ ] `go test ./...`、`go test -race` 相关包、`go vet ./...`、`go mod tidy -diff`、前后端 typecheck / build 和 Node OAuth 回归。

## 7. 切流与回滚任务

- [ ] 切 management OAuth 路径前冻结 / 接受失效在途 Node session，记录路由表和回滚命令。
- [ ] 路由只指向 Go 后执行 auth-url / create / reauthorize / refresh smoke；观察稳定 error code 和 operation log。
- [ ] 切 worker 前停止 Node scheduler、等待当前刷新结束，确认 Go worker ready 后再注册 periodic task。
- [ ] gateway OAuth 适配和 refresh-on-request 独立迁移完成后才切 gateway owner。
- [ ] 回滚时不双跑、不回写旧 token；session 失效，账户密文继续复用。

## 8. 减法阶段清单

只有用户明确宣布开始减法迁移后执行：

- [ ] 删除 Node `openai-oauth.routes.ts`、`openai-oauth.service.ts` 和 Access Token refresh service。
- [ ] 删除 Node system-api mounts、background registry / scheduler、DB service OAuth operation 和无调用 repository。
- [ ] 删除仅供 Node OAuth 的 IPC、进程内 queue / cache / lock 与回归脚本；保留迁移 golden 作为历史契约或改为 Go authority。
- [ ] 前端 API 只指向 Go 保留路径；删除按中文 message 分支。
- [ ] 删除 Node OAuth 配置读取，但保留 Go 仍使用的共享配置说明。
- [ ] `rg` 复查 route、operation key、settings、Redis namespace、token 字段、error code 和 worker 名称没有孤儿引用。
- [ ] 完整验收与回滚观察窗结束后，更新迁移清单和相应粗粒度 owner；单个 OAuth 删除不能越权把整个 management / worker / gateway 标为 Go。

## 9. 风险登记

| 风险 | 控制 |
| --- | --- |
| token endpoint 已轮换 refresh token，但客户端中断 | token 请求开始后脱离客户端取消，完成 CAS 持久化 |
| 同一 code 因 DB 失败重复交换 | encrypted `exchanged` session 续跑 |
| 多 worker 覆盖新 token | Redis lease + config revision CAS + 最多一次重读 |
| Redis session 泄漏 verifier/token | AEAD 密文、TTL、容量、日志禁令、生产 secret 必填 |
| 通过 JWT metadata 越权 | claim 明确非权威，不参与权限/owner/唯一约束 |
| OAuth 切片被误当完整 owner | management/worker/gateway 分开门禁，manifest 粗粒度延后 |
| 第一轮轻验证掩盖真实问题 | 所有重型项保留“待执行”，统一验收轮前不得宣称生产接管 |
