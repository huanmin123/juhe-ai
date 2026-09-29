# BUG-0230：主模型生图工具自选 model 超出绑定账户支持集——派发被拒后主模型降级或轮次失败

## 基本信息

- 编号：BUG-0230
- 状态：待修复（已定性，方案待定）
- 严重程度：P2
- 发现时间：2026-09-29
- 发现方式：生产换绑 grok 生图链路验证（会话 `chat_conv_0a1be842667879680a56b64411f82065`）
- 模块：网关 / chat 工具执行链（generation_tools + gatewaydispatch）
- 关联 bug：无
- 责任人：待派单

## 问题概述

- 现象：会话生图绑定账户（imageBinding）仅支持部分生图模型时，主模型调用 `generate_image` 工具可在 `model` 参数里自由选择枚举（`gpt-image-2` / `grok-imagine-image` / `grok-imagine-quality`）；主模型选了绑定账户不支持的目标时，生图子调用派发被模型门拒绝（`model_unsupported`，503、attemptCount=0），主模型收到「工具执行失败」后或降级输出 SVG、或当轮失败。
- 期望：主模型传入的生图 model 与绑定账户支持集不一致时，工具执行层收敛到会话 `default_image_model`（或按绑定账户支持集约束），保证生图子调用始终派发到绑定账户。
- 实际：`executeGenerateImageTool` 对 `input["model"]` 原样透传（generation_tools.go：`imageModel := context.DefaultImageModel; if input["model"] != nil { imageModel = value }`），派发层按账户 `SupportedModels` 过滤直接拒绝。
- 影响范围：所有「绑定生图账户支持集 ≠ 工具枚举全集」的会话（典型：单上游 key 只开了部分生图模型）。生产实证：神影-grok-imagine 账户（仅 grok-imagine-image）上主模型两次传 `gpt-image-2` 被拒（审计 2026-09-29 13:16:19，model=gpt-image-2、无 account 解析、model_unsupported）；同会话主模型传对模型时链路全通（13:13:59 审计：model=grok-imagine-image、account=acc_685a32ce2d838f28、final_status_code=200）。

## 复现步骤

1. 会话生图绑定账户的 supportedModels 只含部分生图模型（如仅 grok-imagine-image）。
2. 发「画一个红色圆形」类消息；主模型可能给 `generate_image` 传 `model: gpt-image-2`（模型行为，非确定）。
3. 生图子调用 503 `model_unsupported`，主模型降级画 SVG 或轮次失败（`upstream_stream_failed` 与本缺陷无关，为主模型上游流稳定性）。

## 环境信息

- 分支 / 版本：生产国内单机 Docker go-only（103.36.63.105），gateway 0c2940a1fff（2026-09-29 11:57 发布）。
- 是否稳定复现：概率性（取决于主模型选模行为；两次实测一次传对一次传错）。

## 根因分析

- `newGenerateImageTool` 的 InputSchema `model` 枚举为全集三模型，未按会话生图绑定收敛。
- `executeGenerateImageTool` 透传主模型选择，无「绑定账户支持集」校验或回退。
- 派发链（候选模型过滤 + `gatewayRequestCapabilityMismatchReasonFor`）按账户 SupportedModels 拒绝，语义正确；缺陷在工具执行层未约束输入。

## 修复方向（待定稿）

- 首选：`executeGenerateImageTool`（或 ImageGeneration 回调内）当 `input.model` 不在绑定账户支持模型集合时回退 `context.DefaultImageModel`，并可在工具结果里注明已收敛；绑定支持集经 toolBindings 运行时携带。
- 备选：工具 schema 动态枚举（组装侧按绑定支持集生成 `model` enum），主模型无机会选错。
- 文档同步点：`docs/functions/AI问答工具体系与主子模型设计.md` §6.2（生图工具契约）。

## 关联事实（非本缺陷）

- 生图子调用 imageBinding 固定派发（含承载分组收敛）验证正常：账户 credentials 需含 `supported_endpoint_modes`（openai 族默认 4 项不含 `images_json`，须显式追加，否则 `endpoint_mode_unsupported`）；收敛后 endpoint mode 门与模型门逐层放行。
- 中转上游（api.shenwenai.com，gpt-5.6-terra）主模型流稳定性一般：生图验证轮 3 次中 2 次主模型流 30s 无数据被看门狗中断（`upstream_stream_failed`），属上游质量问题。
