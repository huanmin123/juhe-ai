# 问题-0201：supeai 对代理客户端 UA 做无响应挂起且系统 UA 兜底过度扩大

- 发现：2026-09-27（生产实锤：生产网关的系统出站请求——手动账户测试、J1 健康探针——对上游 supeai.cc 持续超时；抓包 + 对照实验定位）。与 BUG-0176 同链路不同层：0176 修的是"系统请求缺渠道身份被拒"，本问题是 0176 引入的 OpenCode UA-only 兜底**范围过度扩大**，恰好踩中 supeai 的 UA 挂起规则，把"可能被拒"变成了"必死"。

## 现象与根因（生产取证 + 代码取证）

1. **supeai.cc 对"已知代理客户端"UA 的 POST 做无响应挂起（tar pit）**。
   - 抓包证据：经代理隧道抓包，SOCKS 握手、TLS 握手全部正常，HTTP 请求送达后服务端不回任何字节，40~70 秒无响应直至客户端超时——不是网络故障，是服务端主动挂起。
   - UA A/B 对照（同一隧道、同一请求体）：`opencode/1.18.5`、`claude-cli/2.1.161 (external, cli)` 等已知代理客户端 UA 挂起；中性 UA（curl 默认、`Go-http-client/2.0`）同样请求 200 正常返回。
   - 定性：供应商侧针对代理客户端 UA 的选择性反代理策略，只影响"看起来像代理客户端"的系统请求。
2. **系统 UA 兜底过度扩大是项目侧根因**。
   - 链条：BUG-0176 在 `upstreamidentity.ApplySystemClientHeaders` 引入"无精确身份的系统 API Key 请求补 `User-Agent: opencode/1.18.5`"兜底，实际命中**所有** api_key 账户的系统请求（provider openai 泛化档案 `profile_openai_openai_v1` 即 supeai 类第三方上游的挂载组合）→ supeai 账户的手动测试与 J1 探针全部携带 OpenCode UA → 全部被挂起超时。
   - 该兜底本为 GLM 渠道兼容而设（AgentRouter 身份检查），对其它上游没有任何正收益，只有身份误报风险。

## 修复（2026-09-27）

`backend-go/shared/platform/upstreamidentity` 建立统一的「上游家族 → 客户端身份」选择，不再使用万金油兜底：

1. **GLM 家族** → ZCode 全套（`User-Agent: ZCode/3.11.2` + `HTTP-Referer` + `X-ZCode-App-Version` + `X-Title`）：provider `glm`，或 profile 以 `profile_glm_` 前缀（含 general 档案），或 `profile_hybrid_openai_chat_v1` / `profile_hybrid_anthropic_messages_v1`（当前桥接目标均为 GLM）。
2. **GPT/Codex 家族** → 仅设 Codex Desktop 静态 UA（导出常量 `CodexDesktopUserAgent`，accountprobe 的 codex_responses 动态头分支共用同一常量）：provider `gpt`/`codex`，或 profile 以 `profile_gpt_`/`profile_codex_` 前缀。originator/session-id 等每请求动态头仍由调用方生成。
3. **Anthropic 家族**（provider `anthropic` 或 `profile_anthropic_anthropic_v1`）：OAuth 应用完整 Claude Code 身份（不变）；API Key **不注入任何客户端身份**，保持传输层默认 Go UA。裁决记录：曾实现 `applyClaudeCodeAPIKey`（Claude UA + `x-app` + x-stainless 系列，不含 OAuth 专用 `anthropic-beta` 段），但本文现象节的 A/B 对照已实测 `claude-cli/2.1.161 (external, cli)` 属于被 supeai 挂起的"已知代理客户端"UA——对该上游注入 Claude 头与 OpenCode 兜底同罪，且 OAuth 身份头对 API Key 请求本就语义失真，最终删除该函数（`upstreamidentity/identity.go` 注释与 `identity_test.go` "must not fabricate any client identity" 锁定）。
4. **Gemini / xai** 既有精确分支不变。
5. **无法识别的上游一律不注入任何身份**，保持空 UA 由传输层补 Go 默认值（实测中性 UA 对 supeai 类上游最安全）。OpenCode 万金油兜底删除；`OpenCodeUserAgent` 常量仅为兼容引用保留。**裁决记录**：provider `openai` 承载"通用 OpenAI-compatible 供应商"泛化档案（schema 描述即如此，supeai 类上游挂此组合），不构成 GPT 家族依据——若按 provider openai 注入 Codex Desktop UA，本修复对 supeai 落空。
6. 调用方已设置的显式 User-Agent 一律不覆盖（整族身份一起沉默，不补充成混合身份）；空 provider 不得凭空命中任何家族。
7. **附带修复（Anthropic 探测 payload 瘦身）**：`accountprobe/protocol.go` 的 anthropic messages 探测请求 `max_tokens` 32000→1024，并删除 `thinking:{type:adaptive}` 与 `output_config:{effort:high}`——非流式探测要等完整响应，推理增强会让首字节超过 Cloudflare 类代理的 100s 上限（上游回 524），也远超手动测试 60s 诊断预算。该载荷形状对所有 anthropic 协议探针/手动测试生效。

## 验证

- 包测试全绿：`upstreamidentity`（家族选择/未知上游无 UA/已有 UA 不覆盖/anthropic api_key 无 beta）、`accountprobe`（泛化探针 Go 默认 UA、codex UA 锁定常量）、`upstreamcatalog`（泛化目录请求中性 UA、GLM General 改 ZCode）、`accounthealth`（泛化探针空 UA、GLM Coding 双协议 ZCode）、`modelcheckowner`（hybrid 改 ZCode）；四模块 `go build` 通过；改动文件 `gofmt -l` 干净。
- `modelcheckowner` 全包另有 5 个 PG 门禁用例失败，原因为 dev PG 实例缺 `juhe_ai_sub2api_dev_app` 角色（与 BUG-0196 复查记录的同源环境问题，失败点在临时子库创建，与本次改动无关），dev bootstrap 后自愈。
- 真实集成待生产发布后观察：supeai 账户手动测试与 J1 探针不再必死（见遗留 1）。
- 2026-09-27 23:19 已发布，观察期开始。

## 遗留（定级均不阻塞）

1. **supeai 存在与 UA 无关的间歇性无响应**：同请求换时段/换 UA 偶发挂死（同样 40~70 秒零字节），属供应商服务质量问题，项目侧无法修复。修复后手动测试仍可能偶发超时，但"必死"消失；建议为该上游配置备用渠道或降低其可用性权重。
2. supeai 的挂起规则清单（哪些 UA 触发）不可知，只能以"中性 UA + 官方客户端身份白名单"策略规避；若其未来把 Go 默认 UA 也列入挂起，需要再评估。
3. `accounthealth/probe.go` 的 codex 分支仍硬编码同一 Codex Desktop UA 字符串（本次未纳入写入范围），与 `upstreamidentity.CodexDesktopUserAgent` 存在两处文本；后续触碰该文件时应改引常量。
