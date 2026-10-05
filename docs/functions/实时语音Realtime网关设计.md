# 实时语音 Realtime 网关设计（M5）

> 状态：设计定稿（2026-10-04）。**M5b 全量已交付**——M5b1 基建面（2026-10-04：依赖/mockupstream WS 四场景/lane 与 endpoint mode 词表/协议枚举与目录收录/ephemeral token 面（`POST /v1/realtime/client_secrets` 已挂载）/三设置键）；**M5b2 WS 桥接面**（2026-10-04：`GET /v1/realtime` WS 升级 handler（`cmd/juhe-ai-gateway/chain_realtime.go`，认证二选一/受理边界/双向泵/生命周期/并发限制）、usage 终态落库（audio token $32/$64 计价）、client_secrets 签发侧目录校验补齐、链级测试与 acceptance E2E（`TestFullchainMediaRealtimeSession`）——ephemeral token 生命周期经链级 miniredis 测试覆盖，gateway 二进制内 token 面受 standalone 热质量门禁（强制 memory 运行态驱动）不可装配，E2E 以 Bearer 会话覆盖）。父计划：[计划-20261003T164247971Z-音频视频模型接入与多供应商扩展](../plans/计划-20261003T164247971Z-音频视频模型接入与多供应商扩展.md)。本设计是《[音频视频模型接入与统一媒体网关设计](音频视频模型接入与统一媒体网关设计.md)》M5 期的展开；报文级契约见《[媒体上游协议契约与Mock上游规格](媒体上游协议契约与Mock上游规格.md)》§4.4。

## 1. 目标与非目标

### 目标

1. 网关支持 OpenAI Realtime 形态的 **WebSocket 双向流**实时语音会话（视频通话、语音沟通场景），首版上游为 openai/gpt 账户（`wss://api.openai.com/v1/realtime`）**事件透传代理**。
2. ephemeral token 面：`POST /v1/realtime/client_secrets`（API Key 认证）签发短期 token，浏览器客户端用查询参数连接（浏览器 WebSocket 无法设自定义头）。
3. 会话级 usage 抽取与计费（从事件流旁路解析，不重写帧）。
4. `realtime` lane 启用（豁免热质量排序/电路 lane 作用域，沿 M2 W8 媒体裁决）、目录收录 realtime 协议模型。
5. Mock WS 上游（mockupstream 扩 WebSocket 能力），验收不依赖真实账户。

### 非目标

1. **Gemini Live 跨协议双向转换**（OpenAI Realtime 事件 ↔ Gemini BidiGenerateContent）——双向流状态机转换是独立大工程，独立立项（M6），本设计只预留转换层挂载点。
2. WebRTC 变体（OpenAI 另有 WebRTC 接入面）——只做 WebSocket。
3. 会话内容（音频字节/事件序列）**不落任何存储**（零存储原则延续）；审计只记会话元数据（模型/账户/时长/usage 汇总/断开原因）。
4. 不做服务端会话恢复/续接（客户端断开重连=新会话重新派发）。
5. 不做会话级工具沙箱/媒体转码。

## 2. 对外契约（下游只认 OpenAI Realtime 形态）

| 端点 | 语义 |
| --- | --- |
| `POST /v1/realtime/client_secrets` | API Key 认证；body `{"model":"gpt-realtime"}`；返回 `{"value":"<token>","expires_at":...}`（OpenAI 形态） |
| `GET wss://<gateway>/v1/realtime?model=<model>` | WebSocket 升级；认证二选一：`Authorization: Bearer <API Key>`（服务端客户端）或 `?token=<ephemeral>`（浏览器） |

- 升级成功后为 OpenAI Realtime 事件协议双向流（`session.update`/`session.created`/`input_audio_buffer.append`/`response.create`/`response.audio.delta`/`response.done` 等），**网关对事件内容透传不重写**（转换层属 M6，届时以 adapter 挂载点介入）。
- 模型在升级请求指定（`model` 查询参数）；会话中途不可换模型。
- `realtime` 目录模型（`gpt-realtime` 系，mode=audio、协议 `realtime`）进 `/v1/models` 与管理面音频分类。

## 3. 架构：WS 桥接层（网关新增基础设施）

```text
客户端 WS ←─┐                    ┌─→ 上游 WS（wss://api.openai.com/v1/realtime?model=...）
            │  1:1 双向桥（两 pump goroutine，文本帧透传）
            │  · 客户端→上游 pump：原样转发
            │  · 上游→客户端 pump：转发 + 旁路 usage 观测器（只解析不修改）
```

- **升级处理**：`/v1/realtime` 进协议门后走专用 WS handler（HTTP 101 hijack），**不进 chain 响应管道**（流式管道为 HTTP 语义，WS 是新面）。认证（API Key 或 ephemeral token）→ 模型解析 → 派发循环选账户（`realtime_session` endpoint mode 候选过滤）→ 建立上游 WS。
- **受理边界（M1 §7 裁决的 WS 版）**：上游 WS 连接失败（拨号/握手/TLS 失败、上游拒绝升级 4xx/5xx）= 受理前，可换账户重试；**上游 101 升级成功 = 受理**，此后连接存活期绑定账户，上游断开只通知客户端不换账户；客户端重连=新请求重新派发。
- **连接生命周期**：`upgrading → established → closed`；保活：WS ping/pong（间隔 20s，3 次无 pong 断开）；**空闲超时**（双向均无帧，系统设置键 `realtimeIdleTimeoutSeconds` 默认 120）；**最大会话时长**（`realtimeMaxSessionSeconds` 默认 1800，到期服务端主动 close code 1000 带终止事件语义说明）；任一侧断开→关闭另一侧。
- **并发上限**：每 API Key 并发 WS 连接数（`realtimeMaxConnectionsPerApiKey` 默认 5）；连接占用账户并发槽（沿既有账户并发语义计入，用完释放）。
- 账户禁用/额度熔断在**派发时点**生效（已建立会话不受影响，自然结束——不主动杀活会话）。

## 4. ephemeral token

- Redis：key `realtime_token:<random>`，TTL **120 秒**，value `{api_key_id, model, created_at}`；签发经 `POST /v1/realtime/client_secrets`（限流沿 API Key 用户请求限制体系）。
- 使用：`?token=` 查询参数；**TTL 内允许多次连接**（浏览器重连场景），过期即失效；token 与 API Key 双重校验（token 有效且模型一致）。
- 不记名复用防护：token 只能连接其签发时指定的 `model`（防枚举他人会话）。

## 5. usage / 计费

- 观测器旁路解析上游→客户端方向事件：`response.done`（`response.usage.input_tokens/output_tokens`、`*_token_details.audio_tokens`）、`rate_limits`（只观测不计量）——**累计到会话级汇总器**，事件帧本身不改动。
- 连接关闭（任一侧、任何原因）时终态写 `usage_records`：endpoint=`/v1/realtime`、audio token 计量（既有 `audio_input/audio_output` 行项与 token 单价）、会话时长秒（`realtime` 维度记录进 usage 元数据）；上游无任何 usage 事件（空会话）→ 0 计费 + `usage_missing`。
- 计费价：gpt-realtime 系目录行落 audio token 单价与 text token 单价、模态无关缓存读价（text/audio cached 同值时）、image 输入价——以 pricing.md 官方表明文为准（2026-10-05 遗留取证批逐行补齐：旧 `gpt-realtime` 行 audio $32/$64 + text $4/$16 + cached $0.40 + image $5；text/audio/image cached 异值的 mini 系不落模态无关 cached 通道；查不到不编造的裁决不变）。
- 终审 o-1 边界裁决（2026-10-04）：客户端升级失败于上游受理后（上游 101 已确立、客户端握手不完整）按代码现状定为契约——无会话不落 usage 终态行，关闭上游连接并释放账户并发槽与每 Key 连接位（`prepareRealtimeSession` 升级失败分支）。

## 6. lane / 词表 / 目录

- `gatewayproto` 增 `LaneRealtime = "realtime"`；`gatewayopenai/lane.go` 路径族判定 `GET /v1/realtime` → LaneRealtime；热质量/电路处理沿媒体裁决（热质量跳过、电路独立 lane 值隔离；四处成对词表加 `realtime`——W8 修复时 `realtime` 曾作非法样本，启用后测试样本换 `m5` 占位或移除）。
- endpoint mode `realtime_session`（GET /v1/realtime 升级请求）进词表四层（metadata_endpointmodes / accounts CHECK / accountscore·chain 词表 / 前端）。
- 目录：`gpt-realtime` 系模型（mode=audio、协议 `realtime`）放开收录——读链两份同源排除规则对**协议=realtime 且 mode=audio 的目录行**放行（模型名 token 排除仅对未标注行生效，沿 M1 裁决）；协议枚举（写路径/前端）加 `realtime`。

## 7. Mock 上游与依赖

> **状态（2026-10-04）**：本节基建已随 M5b1 交付——`gorilla/websocket` v1.5.3 进入 gateway 与 platform 模块（mockupstream 所在模块）；mockupstream `realtime.go` 实现四场景（echo 含 `usage_events`/`close_after` 脚本参数、reject_upgrade 403、close_after_established close 1000、idle 静默）+ `GET /v1/realtime` 白名单与 model 非空校验，`realtime_test.go` 真客户端断言。**acceptance E2E 已随 M5b2 交付**：`TestFullchainMediaRealtimeSession`（真 gorilla 客户端经真实 gateway 二进制到 mock 上游：受理边界两臂、事件往返逐字节、usage 终态金额、无 redis 降级契约）；链级测试（`chain_realtime_bridge_test.go`）覆盖 token 生命周期（miniredis 真 redis 协议）、空闲超时与并发上限。

- **新增依赖**：`gorilla/websocket`（Go WS 事实标准库；标准库无 WS 支持；三项目 go.mod 按需引入——仅 gateway）。
- mockupstream 扩 WS 能力（httptest server 升级层 + 场景）：
  - `media_realtime_echo`：升级成功；客户端事件原样回显 + 按脚本发 `response.done`（含 usage）+ close 语义
  - `media_realtime_reject_upgrade`：升级请求 403/429（受理前失败，换账户用例）
  - `media_realtime_close_after_established`：升级成功后立刻断（受理后断开用例）
  - `media_realtime_idle`：升级成功后静默（空闲超时用例）
- acceptance E2E：真 WS 客户端（测试内 gorilla 客户端）经网关 → mock 上游：事件往返、usage 终态落库、受理边界两臂、ephemeral token 生命周期、连接数上限。

## 8. 验收标准

> **状态（2026-10-04）**：M5b2 已实施并验证——1/2/3/4/6/7 经 `chain_realtime_bridge_test.go`（链级，含 miniredis token 面）与 `TestFullchainMediaRealtimeSession`（acceptance E2E）覆盖（token 生命周期的 E2E 臂因 standalone 热质量门禁不可装配 redis 运行态驱动，由链级 miniredis 测试覆盖）；5 经 usage/审计快照字段面（只有元数据，无内容）落实验证。

1. WS 客户端经网关与 mock 上游完成双向事件会话，帧内容逐字节等价（透传不重写）。
2. 受理边界：升级失败换账户（mock 请求记录断言候选顺序）；升级成功后上游断开不换（客户端收到 close）。
3. ephemeral token：签发→TTL→多次连接→过期失效→model 绑定校验。
4. usage：会话累计器终态落库金额正确；空会话 0 计费+usage_missing。
5. 零存储：审计/日志/DB 无音频字节与事件序列（仅元数据）。
6. 空闲超时/最大时长/并发上限行为符合设置键语义。
7. 既有链路（对话/图像/音频同步/视频异步/长音频）零回归。

## 9. 分期

- **M5b（本设计范围）**：OpenAI 透传代理全链（词表/lane/token 面/WS 桥接/usage/mock/E2E/文档）。
- **M6（独立立项）**：Gemini Live 跨协议双向转换（BidiGenerateContent ↔ Realtime 事件，状态机映射；届时本设计 §2 预留的"转换层 adapter 挂载点"启用，透传与转换按账户协议分派）。

## 10. 关联文档

- [音频视频模型接入与统一媒体网关设计](音频视频模型接入与统一媒体网关设计.md)：父设计（§3 lane、§7 受理边界、§12 M5 行）。
- [媒体上游协议契约与Mock上游规格](媒体上游协议契约与Mock上游规格.md) §4.4：OpenAI Realtime 报文与 Mock 规格。
- [接口契约与权限矩阵](接口契约与权限矩阵.md)：端点登记（随 M5b 交付）。
