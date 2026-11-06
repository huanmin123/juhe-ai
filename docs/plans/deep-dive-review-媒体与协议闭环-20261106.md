# 深挖复审台账：网关多媒体域与协议/调度闭环

- 锚定范围：`/v1` 网关的媒体域（图像生成、同步音频、异步视频/长音频任务面）、OpenAI/Anthropic/Gemini 协议面、媒体调度域（StatelessMediaDispatch）及其并发、计费与运维闭环。
- 发起：2026-11-06，用户显式发起（`/deep-dive-review`）；授权模式：**report-only → fix**（用户 2026-11-06 明确要求“必须处理的问题修复”，A 类即修；B/C 项未擅自改语义）。
- 问题记录：`docs/plans/deep-dive-review-媒体与协议闭环-20261106-issues.md`
- 收敛预算：最多 6 轮 / 本会话。
- 需求脉络：
  - 目标：从用户调用视角验证「视频 → 音频 → 图像」全链路是否闭环，各协议与调度、并发、性能是否正常。
  - 非目标：不做生产发布、不修改源码（report-only）、不评价上游中转站自身质量。
- 涉及文件：`backend-go/projects/gateway/{cmd/juhe-ai-gateway,internal/gatewaymedia,internal/gatewaydispatch,internal/gatewayrouting,internal/gatewaypreauth,internal/gatewayresponse,internal/gatewayopenai,internal/gatewayanthropic,internal/gatewaygemini,internal/pricing,internal/gatewayusage}`、`backend-go/projects/jobs/internal/{retention,jobregistry}`、`backend-go/shared/contracts/connector_registry.go`、`maintenance/internal/schema/*`。
- 关联契约：`docs/functions/音频视频模型接入与统一媒体网关设计.md`、`对话与非对话媒体调度分离设计.md`、`媒体上游协议契约与Mock上游规格.md`、`模型价格与用量统计口径.md`、`火山方舟账号接入.md`、`架构总览.md`。
- 代码基线：`master` @ `dc57069c7`，审查开始时工作区干净。
- 工作区边界：只写 `.local/review-tmp/` 与 `docs/plans/deep-dive-review-*`；dev 业务库写入（账户/API Key/任务/用量）仅限审查用，不触碰生产、不改源码。
- 当前阶段：已收口（report-only）。
- 收敛计数：R3 前有新增确认项（报告只统计确认，不宣称三轮全干净）；本轮按“预算 + 用户可裁决”收口，终报如实标注。

## 实机基线（R0，2026-11-06）

环境：dev gateway `http://127.0.0.1:3300`；真实上游账户：火山方舟官号（Seedance/Seedream/Seed-TTS/Seed-Audio/SeedASR）+ `api.zeekai.cc` 中转（gpt/anthropic/gemini/grok/国模）。

| 面 | 请求 | 结果 | 关键证据 |
| --- | --- | --- | --- |
| 模型目录 | `GET /v1/models` | 200，28 模型 | 账户并集生效 |
| 图像（按张价） | seedream-5-0-flash 1024/2048 b64 | 200，5-9s | costUsd 0.01714，outputImageCount 推导正常 |
| 图像（token 价） | gpt-image-2 1024 b64 | 200，14.4s，1.38MB | **costUsd 缺失（零计费）→ F1** |
| TTS（字符价） | seed-tts-2.0 | 200，70KB MP3 | ttsInputChars=13，cost 0.000557 |
| TTS（时长价） | seed-audio-1.0 | 200，实际 4.467s | audioInputSeconds=4.4，cost 0.010476 |
| 长转写 | volc.seedasr.auc（公网 wav） | create 200 → completed | content 返回全文，audioInputSeconds=33.621，cost 0.001067 |
| 视频创建 | seedance-2.0 4s 720p | 200，1.1s | job queued，provider_job_id 回显 |
| 视频轮询 | `GET /v1/videos/{id}` ×7 | in_progress → completed（约 105s） | 本地状态推进 |
| 视频产物 | `GET /v1/videos/{id}/content` | 200，2.24MB mp4 | 零存储流式代理 |
| 视频计费 | media-jobs + usage | costUsd 0.5656，outputVideoSeconds=4 | 终态回填 |
| 视频取消 | create → 3s 后 DELETE | 204；本地 cancelled；**上游仍 running** | media_jobs.costUsd=0 → F3 |
| 已完成 DELETE | DELETE completed | 204；本地仍 completed；content 仍 200 | 无实际删除 → F3 |
| OpenAI Chat | grok-4.5 / glm-5.3 / doubao-seed-2-1-pro | 200（4.9s / 4.0s / 9.8s） | grok 上游模型漂移为 `grok-4.5-build` |
| OpenAI Responses | gpt-6.1-sol | 200，57s | 上游慢 |
| OpenAI Chat | gpt-5.5（zeekai） | 503 upstream_retryable_error | 上游 429 |
| Anthropic 原生 | claude-sonnet-5（base 修正后） | 200，3.0s | cost 0.0013588 |
| Gemini 原生 | gemini-3.8-flash（base 修正后） | 200，3.1s | cost 0.000195 |
| OpenAI Chat | claude/gemini 模型 | 503 `*_native_unsupported` | 契约现状（无 chat→messages/generate 桥） |
| 并发 | 20 chat | 20×200，p50≈10s，max 15.1s | 上游延迟主导 |
| 并发 | 10 TTS | 10×200，max 4.4s | — |
| 并发 | 5 image | 5×200，max 10.2s | — |
| 混合并发 | 6 chat + 4 TTS + 2 image | 12×200，max 17.7s | — |
| 并发上限 1 | 4 chat | 4×200，错峰 5.7-13.4s | 排队生效，无 429/泄漏 |

## 问题条目表（收口）

| ID | 来源 | 假设 | 关键依据 | 状态 |
| --- | --- | --- | --- | --- |
| F1 | R1-C/F + runtime | gpt-image 族成功请求零计费 | usage.go:49-72 + billing.go:511-513 + gpt.json 无 outputCostPerImage；runtime costUsd 缺失 | 已修（runtime 复核 0.01035） |
| F2 | R1-B + runtime | 缺 speech_token 账户遮蔽健康账户 | chain_driver.go:1902-1920 + candfilters 无凭据门；runtime 400→disable→200 | 已修（runtime 复核 200/命中健康账户） |
| F3 | R1-A/F + runtime | 取消/过期不计上游成本；completed DELETE 204 no-op | chain_media_video.go:1625-1652 + jobsrepo.go:219；runtime 上游 running/cost 0 | 部分修（补 Warn）；语义待 C6/C7 |
| F5 | R1-D | audio/video 无独立 lane 并发上限 | enginehelpers.go:154-161 记入 text lane；文档 §3 称独立 | 已修（文档回正） |
| F6 | R1-D | 图像 lane 由 text 升级时带出会话亲和 | preflight.go:938-951/1067-1075/1247；Claim/Remember 无 lane 门 | 已修（单测覆盖） |
| N1 | R3 | media_jobs 缺 owner/created 索引；retention 全表扫描风险 | pg_schema_business_indexes.go:1214-1228；jobsrepo.go:314-326/363/434 | 已修（golden 通过；部署 ensure 应用） |
| N2 | R3 | 任务面上游传输错误零日志 | chain_media_video.go:2106-2113 | 已修（补 Warn） |
| N3 | R3 | 管理/用户面 content 流中断静默 | chain_media_jobs_center.go:119 | 已修（补 Warn） |
| N4 | R3 | retention 吞吐上限 1.2 万行/天，无追尾循环 | mediajobs.go:59/96 + schedule.go:169-173 | 已修（单测覆盖） |
| F4 | R1-E + runtime | base_url 缺版本段是校验缺陷 | — | 已证伪（版本段由账户承载为明文契约） |
| F7 | R2 | usage 混排 attempt/探针行影响统计 | statreads 无过滤 | 待定（需口径裁决） |
| F8 | R1-E + runtime | 跨协议 503 可重试语义 | chain_driver.go:919/933 | C 类（产品/错误语义裁决） |

## 覆盖记录（R1-R3）

- R1 维度制：状态一致性、并发与时序、取消与中断、错误路径、资源生命周期、数据流契约、外部输入、回归兼容、性能放大、测试有效性；6 个独立角色（视频任务面、音频、图像、调度、协议、计费）+ 4 个补充角色，共产出约 60 条假设。
- R2 证伪轮：2 个独立验证角色复核 F1/F2/F3/F4/F5/F6；F1/F2/F3/F5/F6 confirmed，F4 refuted（附契约原文与实现证据）。
- R3 终态复核（运维与数据生命周期视角）：新增 N1-N4；任务面鉴权/归属/零存储/终态唯一性/部署契约核为干净。
- 未覆盖/未验证：
  - 生产 `media_jobs` 实际行数与创建速率（N1/N4 量级）；
  - gpt-image 之外的 token 价图像模型（目录仅 gpt-image 族）；
  - OpenAI 同步 STT（`/v1/audio/transcriptions`，无可用上游账户）；
  - 图像 2MB 截断路径（seedream b64 均 <2MB，未触发）；
  - 多副本/滚动发布场景（单机形态，多节点设计未实施）。

## 扩散记录

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |
| R1 | jobs/retention + mediajobsadmin | 调用依赖 | 媒体任务生命周期与清理 |
| R2 | connector_registry + connection 校验 | 共享契约 | base_url 契约裁决 |
| R3 | schema 索引 + jobregistry 调度 | 共享存储 | 任务表查询与清理吞吐 |

## 裁决清单（C 类）

| ID | 一句话 | 来源轮次 | 状态 |
| --- | --- | --- | --- |
| C1 | 取消/过期任务是否追上游实际计量（当前恒 0） | R1 | **已裁决（2026-11-06）：不追计，接受上游可能继续计费的一次性损失；契约 §10 已回正** |
| C2 | 已完成任务 DELETE 是否应物理删除本地行/产物 | R1 | **已裁决（2026-11-06）：保持幂等 204，终态行保留至 TTL；契约 §4.2 已明确** |
| C3 | 跨协议模型（claude/gemini）走 OpenAI Chat 入口返回 503 可重试，是否改 400/404 | R1 | **已裁决并实施：改 400 invalid_request_error + 入口提示** |
| C4 | audio/video 是否需要独立 lane 并发上限，还是回正文档 | R1 | **已裁决（2026-11-06）：保持现状，文档已回正** |
| C5 | usage 列表是否应排除后台探针/attempt 行 | R2 | **已裁决（2026-11-06）：保留记录 + 口径文档说明；后续可加 trafficSource 筛选** |

## 修复记录（fix 模式，2026-11-06）

| 批 | 内容 | 验证证据 | 落点复核 |
| --- | --- | --- | --- |
| B1 | F1 计费门修正；F2 语音凭据候选门；F6 升级清亲和；N2/N3 日志；F3 取消 Warn；N1 两索引；N4 清理分批 | `pricing` 全包、`gatewaydispatch` 全包、`retention`/`schema` 目标测试、`preauth`/`cmd` 目标测试全绿；实机 gpt-image-2 cost 0.01035、ASR 200 命中健康账户、TTS/图像/对话冒烟 200 | 见问题记录“修复批次”；F3 语义与 C 类未改 |

- 全量 `cmd/juhe-ai-gateway` 包未跑完：本机虚拟内存不足（`VirtualAlloc failed/out of memory`，非代码失败），已用目标测试覆盖改动面；`gatewaypreauth` 存在既有失败 `TestWhModelMappingProjection`（构造已退役 `hybrid` 供应商，与本批无关）。
- N1 的索引 DDL 在部署时由 `maintenance upgrade` / `--ensure-schema` 应用（幂等）；dev 当前未执行 ensure，功能复核不依赖索引。

## 终报

- 终止原因：report-only 深挖完成后，用户明确要求修复“必须处理的问题”，转入 fix 模式并按 A 类即修完成；R1 攻击 + R2 证伪 + R3 终态复核，R3 新增 N1-N4，未满足“连续三轮零新确认”的完整收敛协议，按用户授权收口并如实披露。
- 扫描范围与轮次：见上；假设约 60 条，确认 9 条（F1/F2/F3/F5/F6/N1-N4），证伪 7 条（F4、TTS 单位、b64>256KB、视频秒自算、字符 byte、ns_json、跨 Key 越权）。
- 处置统计：已修 9 条（F1/F2/F5/F6/F8/N1/N2/N3/N4），部分修 1 条（F3 可观测部分，成本/删除语义按裁决接受现状），C 类 5 条已全部裁决（C1 不计费、C2 保持 204、C3 改 400、C4 保持现状、C5 保留记录+文档），证伪 7 条。
- 验证证据：`deep-dive-review-媒体与协议闭环-20261106-issues.md` 的“修复批次”与 `.local/review-tmp/` 运行产物。
- 移交清单：无待裁决项；生产发布时随 `maintenance upgrade` 应用 N1 索引；取消成本按 C1 裁决接受。
- 未提交改动：本批修改的源码/测试/文档见 `git diff`；工作区同时存在其他并行会话的改动（authsys/modelcheckauth/媒体 SEC-01 等），交付时按文件区分，未回滚他人改动。
